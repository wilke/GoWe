#!/usr/bin/env bash
# test-scatter-subwf.sh — E2E acceptance test for issue #164
# (scatter over a sub-workflow must run children in parallel, not block the
# scheduler, and be cancellable).
#
# Acceptance criteria verified here:
#   AC1: 6-item scatter over a 2-step sub-workflow (each child sleeps 10 s)
#        with 2 workers completes in < 60 s (serial baseline ~80-90 s) AND
#        child sleep-task execution intervals observably overlap.
#   AC2: a trivial single-tool submission made mid-scatter gets a task
#        within ~3 s (non-blocking scheduler).
#   AC4: gathered `msgs` output is ordered by scatter combination (a..f).
#   AC3: 20-item run, cancel the parent after the first child completes:
#        no PENDING child ever starts running afterwards, parent goes
#        terminal CANCELLED within ~15 s and STAYS CANCELLED.
#   AC3-DB: cancel cascade verified directly in the SQLite DB — every child
#        submission of the parent's proxy tasks reaches CANCELLED (not
#        RUNNING, not COMPLETED-as-orphan) within ~5 s of the parent going
#        CANCELLED, and holds no active tasks.
#
# Environment: builds via apptainer golang:1.24 (Go is not installed
# natively). Server on :8095 (NEVER :8091 — production), all state in /tmp.
# Set SKIP_BUILD=1 to reuse existing binaries in $BIN_DIR.

set -u

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT=8095
BASE="http://localhost:${PORT}/api/v1"
BIN_DIR="${BIN_DIR:-/tmp/gowe164-bin}"
WORK=/tmp/gowe164
DB=/tmp/gowe164.db
SERVER_LOG=/tmp/gowe164-server.log
SERVER_PID=""
W1_PID=""
W2_PID=""

# ---------------------------------------------------------------- helpers --

cleanup() {
    # Kill only processes we started, by saved PID.
    for pid in "$SERVER_PID" "$W1_PID" "$W2_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
        fi
    done
    sleep 1
    for pid in "$SERVER_PID" "$W1_PID" "$W2_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null
        fi
    done
}
trap cleanup EXIT

fail() {
    echo ""
    echo "FAIL: $*" >&2
    echo "--- last 40 server log lines ---" >&2
    tail -40 "$SERVER_LOG" >&2 2>/dev/null || true
    exit 1
}

pass() { echo "PASS: $*"; }

now_s() { date +%s.%N; }

elapsed() { # elapsed <start> -> seconds with 1 decimal
    python3 -c "import sys; print(f'{float(sys.argv[2])-float(sys.argv[1]):.1f}')" "$1" "$(now_s)"
}

api() { curl -sf --max-time 10 "$@"; }

sub_state() { # sub_state <id>
    api "$BASE/submissions/$1" | jq -r '.data.state'
}

submit() { # submit <cwl> <inputs> -> submission id on stdout
    # HOME is redirected to a scratch dir so the user's real ~/.gowe
    # credentials are never picked up — submissions must be anonymous.
    local out
    out="$(HOME=/tmp/gowe164/home "$BIN_DIR/gowe" submit "$1" -i "$2" \
        --server "http://localhost:$PORT" --no-upload 2>&1)" \
        || { echo "$out" >&2; return 1; }
    echo "$out" | sed -n 's/^Submission created: \([^ ]*\).*/\1/p'
}

# proxy_task_ids <parent-sub-id> -> JSON array of subworkflow proxy task IDs
proxy_task_ids() {
    api "$BASE/submissions/$1/tasks?limit=500" \
        | jq -c '[.data // [] | .[] | select(.executor_type == "subworkflow") | .id]'
}

# children <proxy-ids-json> -> JSON array of {id, state} for child submissions.
# The list endpoint does not return parent_task_id, but children carry a
# "parent_task" label pointing at their proxy task.
children() {
    api "$BASE/submissions?limit=500" \
        | jq -c --argjson ids "$1" \
          '[.data // [] | .[] | select((.labels.parent_task // "") as $p | $ids | index($p) != null) | {id, state}]'
}

# ------------------------------------------------------------------ build --

if [ "${SKIP_BUILD:-0}" != "1" ]; then
    echo "== Building binaries into $BIN_DIR (apptainer golang:1.24) =="
    mkdir -p /tmp/gomod "$BIN_DIR"
    apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "
        cd '$REPO' &&
        go build -o '$BIN_DIR/gowe-server' ./cmd/server &&
        go build -o '$BIN_DIR/gowe'        ./cmd/cli &&
        go build -o '$BIN_DIR/gowe-worker' ./cmd/worker
    " || { echo "FAIL: build failed" >&2; exit 1; }
fi
for b in gowe-server gowe gowe-worker; do
    [ -x "$BIN_DIR/$b" ] || { echo "FAIL: missing binary $BIN_DIR/$b" >&2; exit 1; }
done

# --------------------------------------------------------------- fixtures --

rm -rf "$WORK" /tmp/gowe164-w1 /tmp/gowe164-w2 /tmp/gowe164-out
rm -f "$DB" "$DB"-wal "$DB"-shm "$SERVER_LOG" /tmp/gowe164-w1.log /tmp/gowe164-w2.log
mkdir -p "$WORK" "$WORK/home" /tmp/gowe164-w1 /tmp/gowe164-w2 /tmp/gowe164-out

cat > "$WORK/scatter-sub.cwl" <<'EOF'
cwlVersion: v1.2
class: Workflow
requirements:
  ScatterFeatureRequirement: {}
  SubworkflowFeatureRequirement: {}
inputs:
  items: string[]
steps:
  work:
    scatter: item
    in: { item: items }
    out: [msg]
    run:
      class: Workflow
      inputs: { item: string }
      steps:
        a:
          in: { item: item }
          out: [t]
          run:
            class: CommandLineTool
            baseCommand: [bash, -c]
            inputs:
              item: { type: string }
            arguments: ["sleep 10 && echo $(inputs.item) > out.txt"]
            outputs:
              t: { type: File, outputBinding: { glob: out.txt } }
        b:
          in: { t: a/t }
          out: [msg]
          run:
            class: CommandLineTool
            baseCommand: [cat]
            inputs:
              t: { type: File, inputBinding: { position: 1 } }
            outputs:
              msg: { type: stdout }
      outputs:
        msg: { type: File, outputSource: b/msg }
outputs:
  msgs:
    type: File[]
    outputSource: work/msg
EOF

echo 'items: [a, b, c, d, e, f]' > "$WORK/inputs6.yml"
echo 'items: [x01, x02, x03, x04, x05, x06, x07, x08, x09, x10, x11, x12, x13, x14, x15, x16, x17, x18, x19, x20]' > "$WORK/inputs20.yml"

cat > "$WORK/trivial.cwl" <<'EOF'
cwlVersion: v1.2
class: Workflow
inputs:
  msg: string
steps:
  echo:
    in: { msg: msg }
    out: [out]
    run:
      class: CommandLineTool
      baseCommand: echo
      inputs:
        msg: { type: string, inputBinding: { position: 1 } }
      outputs:
        out: { type: stdout }
outputs:
  out: { type: File, outputSource: echo/out }
EOF
echo 'msg: hello' > "$WORK/trivial-inputs.yml"

# ------------------------------------------------------- server + workers --

echo "== Starting server on :$PORT and 2 workers (--runtime none) =="
"$BIN_DIR/gowe-server" --addr ":$PORT" --db "$DB" --default-executor worker \
    --allow-anonymous --scheduler-poll 1s --debug > "$SERVER_LOG" 2>&1 &
SERVER_PID=$!

for i in $(seq 1 30); do
    curl -sf --max-time 2 "http://localhost:$PORT/api/v1/health" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" 2>/dev/null || fail "server exited during startup"
    [ "$i" = 30 ] && fail "server did not become healthy on :$PORT"
    sleep 0.5
done

"$BIN_DIR/gowe-worker" --server "http://localhost:$PORT" --name w1 --runtime none \
    --workdir /tmp/gowe164-w1 --stage-out file:///tmp/gowe164-out --poll 500ms \
    > /tmp/gowe164-w1.log 2>&1 &
W1_PID=$!
"$BIN_DIR/gowe-worker" --server "http://localhost:$PORT" --name w2 --runtime none \
    --workdir /tmp/gowe164-w2 --stage-out file:///tmp/gowe164-out --poll 500ms \
    > /tmp/gowe164-w2.log 2>&1 &
W2_PID=$!
sleep 2
kill -0 "$W1_PID" 2>/dev/null || fail "worker w1 exited during startup"
kill -0 "$W2_PID" 2>/dev/null || fail "worker w2 exited during startup"

# ============================================ AC1 + AC2: parallelism run ==

echo ""
echo "== AC1: 6-item x 10 s scatter-over-subworkflow, 2 workers =="
T0="$(now_s)"
P1="$(submit "$WORK/scatter-sub.cwl" "$WORK/inputs6.yml")" || fail "AC1 submit failed"
[ -n "$P1" ] || fail "AC1: could not parse submission id from gowe submit output"
echo "   parent submission: $P1"

# AC2 mid-run: wait until the scatter is actually in flight, then submit a
# trivial workflow and measure time until it has a task.
sleep 4
echo "== AC2: trivial submission mid-scatter =="
T_AC2="$(now_s)"
P2="$(submit "$WORK/trivial.cwl" "$WORK/trivial-inputs.yml")" || fail "AC2 submit failed"
[ -n "$P2" ] || fail "AC2: could not parse submission id"
AC2_ELAPSED=""
for i in $(seq 1 40); do
    NTASKS="$(api "$BASE/submissions/$P2/tasks" | jq '.data | length')"
    if [ "${NTASKS:-0}" -ge 1 ]; then
        AC2_ELAPSED="$(elapsed "$T_AC2")"
        break
    fi
    sleep 0.2
done
[ -n "$AC2_ELAPSED" ] || fail "AC2: trivial submission $P2 got no task within 8 s (head-of-line blocking)"
AC2_OK="$(python3 -c "print(1 if float('$AC2_ELAPSED') <= 4.0 else 0)")"
[ "$AC2_OK" = "1" ] || fail "AC2: trivial submission got a task only after ${AC2_ELAPSED}s (> 4 s budget)"
pass "AC2: trivial submission $P2 got a task in ${AC2_ELAPSED}s"

# Wait for AC1 parent to finish.
AC1_STATE=""
for i in $(seq 1 120); do
    AC1_STATE="$(sub_state "$P1")"
    case "$AC1_STATE" in COMPLETED|FAILED|CANCELLED) break ;; esac
    sleep 1
done
AC1_WALL="$(elapsed "$T0")"
[ "$AC1_STATE" = "COMPLETED" ] || fail "AC1: parent $P1 ended in state '$AC1_STATE' after ${AC1_WALL}s (want COMPLETED)"
AC1_OK="$(python3 -c "print(1 if float('$AC1_WALL') < 60.0 else 0)")"
[ "$AC1_OK" = "1" ] || fail "AC1: wall time ${AC1_WALL}s >= 60 s (serial baseline ~80-90 s; children not parallel)"

# Overlap check: gather the sleep-step ("a") task intervals of every child
# and require at least one overlapping pair.
IDS1="$(proxy_task_ids "$P1")"
[ "$(echo "$IDS1" | jq 'length')" = "6" ] || fail "AC1: expected 6 subworkflow proxy tasks, got $(echo "$IDS1" | jq 'length')"
CH1="$(children "$IDS1")"
[ "$(echo "$CH1" | jq 'length')" = "6" ] || fail "AC1: expected 6 child submissions, got $(echo "$CH1" | jq 'length')"

INTERVALS="$WORK/ac1-intervals.txt"
: > "$INTERVALS"
for cid in $(echo "$CH1" | jq -r '.[].id'); do
    api "$BASE/submissions/$cid/tasks" \
        | jq -r --arg c "$cid" '.data[] | select(.step_id == "a") | "\($c) \(.started_at) \(.completed_at)"' \
        >> "$INTERVALS"
done
echo "   child sleep-task intervals (child start end):"
sed 's/^/     /' "$INTERVALS"
OVERLAPS="$(python3 - "$INTERVALS" <<'PYEOF'
import re
import sys
from datetime import datetime

def ts(s):
    # RFC3339 with nanoseconds (Go) — too precise for fromisoformat on
    # older Pythons; parse the seconds fraction manually.
    m = re.match(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?", s)
    base = datetime.strptime(m.group(1), "%Y-%m-%dT%H:%M:%S")
    frac = float("0." + m.group(2)) if m.group(2) else 0.0
    return base.timestamp() + frac

iv = []
for line in open(sys.argv[1]):
    parts = line.split()
    if len(parts) != 3 or "null" in parts:
        continue
    iv.append((ts(parts[1]), ts(parts[2]), parts[0]))
iv.sort()
n = 0
for i in range(len(iv)):
    for j in range(i + 1, len(iv)):
        if iv[j][0] < iv[i][1] and iv[i][0] < iv[j][1]:
            n += 1
print(n)
PYEOF
)"
[ "${OVERLAPS:-0}" -ge 1 ] || fail "AC1: no overlapping child sleep-task intervals — children ran serially (wall ${AC1_WALL}s)"
pass "AC1: parent COMPLETED in ${AC1_WALL}s (< 60 s) with $OVERLAPS overlapping child execution pairs"

# Log evidence of overlap (checkout/complete interleaving in the server log).
echo "   server-log evidence (task checkout/completion interleaving):"
grep -E 'task (checked out|completed by worker)' "$SERVER_LOG" | head -12 | sed 's/^/     /'

# ================================================= AC4: gathered outputs ==

echo ""
echo "== AC4: gathered msgs ordered a..f =="
MSGS_JSON="$(api "$BASE/submissions/$P1" | jq -c '.data.outputs.msgs')"
[ "$MSGS_JSON" != "null" ] && [ -n "$MSGS_JSON" ] || fail "AC4: parent outputs have no 'msgs' array"
NMSGS="$(echo "$MSGS_JSON" | jq 'length')"
[ "$NMSGS" = "6" ] || fail "AC4: expected 6 msgs entries, got $NMSGS: $MSGS_JSON"
GATHERED=""
for i in 0 1 2 3 4 5; do
    LOC="$(echo "$MSGS_JSON" | jq -r ".[$i].location // .[$i].path")"
    FPATH="${LOC#file://}"
    [ -f "$FPATH" ] || fail "AC4: msgs[$i] file not found at '$FPATH' (location: $LOC)"
    GATHERED="$GATHERED$(cat "$FPATH" | tr -d '[:space:]')"
done
[ "$GATHERED" = "abcdef" ] || fail "AC4: gathered msgs content order is '$GATHERED', want 'abcdef'"
pass "AC4: msgs gathered in scatter order (a b c d e f)"

# ==================================================== AC3: cancellation ==

echo ""
echo "== AC3: 20-item run, cancel parent after first child completes =="
P3="$(submit "$WORK/scatter-sub.cwl" "$WORK/inputs20.yml")" || fail "AC3 submit failed"
[ -n "$P3" ] || fail "AC3: could not parse submission id"
echo "   parent submission: $P3"

# Wait for the 20 proxy tasks / children to exist.
IDS3="[]"
for i in $(seq 1 30); do
    IDS3="$(proxy_task_ids "$P3")"
    [ "$(echo "$IDS3" | jq 'length')" = "20" ] && break
    sleep 1
done
[ "$(echo "$IDS3" | jq 'length')" = "20" ] || fail "AC3: expected 20 proxy tasks, got $(echo "$IDS3" | jq 'length')"

# Wait for the first child to COMPLETE. Task checkout is FIFO, so with 20
# children on 2 workers all step-"a" (10 s sleep) tasks run before any
# step "b": the first child completes only after ~(20/2)*10 s.
FIRST_DONE=""
for i in $(seq 1 180); do
    CH3="$(children "$IDS3")"
    NDONE="$(echo "$CH3" | jq '[.[] | select(.state == "COMPLETED")] | length')"
    if [ "${NDONE:-0}" -ge 1 ]; then FIRST_DONE=1; break; fi
    sleep 1
done
[ -n "$FIRST_DONE" ] || fail "AC3: no child completed within 180 s"

echo "   first child completed; cancelling parent $P3"
T_CANCEL="$(now_s)"
api -X PUT "$BASE/submissions/$P3/cancel" >/dev/null || fail "AC3: cancel request failed"

# Snapshot immediately after the cancel is accepted: children RUNNING at
# this instant may legitimately finish either way; anything else must never
# start running afterwards.
CH3="$(children "$IDS3")"
RUNNING_AT_CANCEL="$(echo "$CH3" | jq -c '[.[] | select(.state == "RUNNING") | .id]')"
NONTERM_AT_CANCEL="$(echo "$CH3" | jq -c '[.[] | select(.state == "PENDING" or .state == "RUNNING") | .id]')"
# Children allowed to end COMPLETED: already COMPLETED at the cancel instant,
# or RUNNING right then (their in-flight task may legitimately finish first).
ALLOWED_COMPLETED="$(echo "$CH3" | jq -c '[.[] | select(.state == "COMPLETED" or .state == "RUNNING") | .id]')"
echo "   at cancel-accept: $(echo "$CH3" | jq -c 'group_by(.state) | map({(.[0].state): length}) | add')"

# Watch until the parent is terminal: no child outside RUNNING_AT_CANCEL may
# ever be observed RUNNING.
PARENT_STATE=""
CANCEL_ELAPSED=""
while :; do
    CH3="$(children "$IDS3")"
    LATE="$(echo "$CH3" | jq -c --argjson ok "$RUNNING_AT_CANCEL" \
        '[.[] | select(.state == "RUNNING") | select(.id as $i | $ok | index($i) == null) | .id]')"
    [ "$LATE" = "[]" ] || fail "AC3: children left PENDING and started RUNNING after cancel: $LATE"
    PARENT_STATE="$(sub_state "$P3")"
    case "$PARENT_STATE" in COMPLETED|FAILED|CANCELLED) CANCEL_ELAPSED="$(elapsed "$T_CANCEL")"; break ;; esac
    TOOLONG="$(python3 -c "print(1 if float('$(elapsed "$T_CANCEL")') > 20.0 else 0)")"
    [ "$TOOLONG" = "1" ] && fail "AC3: parent $P3 not terminal 20 s after cancel (state: $PARENT_STATE)"
    sleep 0.5
done
[ "$PARENT_STATE" = "CANCELLED" ] || fail "AC3: parent terminal state is '$PARENT_STATE' after cancel, want CANCELLED"
AC3_OK="$(python3 -c "print(1 if float('$CANCEL_ELAPSED') <= 15.0 else 0)")"
[ "$AC3_OK" = "1" ] || fail "AC3: parent went terminal only ${CANCEL_ELAPSED}s after cancel (> 15 s budget)"

# --- AC3-DB: cancel cascade, asserted directly against the database --------
# Within ~5 s of the parent reaching CANCELLED, every child submission of the
# parent's subworkflow proxy tasks must be CANCELLED in the DB. Only children
# that were COMPLETED, or RUNNING (race window), at the cancel instant may end
# COMPLETED. No child may sit in PENDING/RUNNING, and no child may hold an
# active task (PENDING/SCHEDULED/QUEUED/RUNNING).
db_children() { # -> JSON [{id,state}] of P3's proxy-task children, from the DB
    sqlite3 -json "file:$DB?mode=ro" \
        "SELECT s.id, s.state FROM submissions s
         JOIN tasks t ON s.parent_task_id = t.id
         WHERE t.submission_id = '$P3' AND t.executor_type = 'subworkflow'
         ORDER BY s.id;" 2>/dev/null || echo "[]"
}
DB_CASCADE_ELAPSED=""
T_DB="$(now_s)"
DB_CH="[]"
DB_BAD="[]"
for i in $(seq 1 14); do   # 14 x 0.5 s = 7 s hard ceiling, budget 5 s below
    DB_CH="$(db_children)"
    DB_N="$(echo "$DB_CH" | jq 'length')"
    # Violations: any non-terminal child, or any child terminal in a state
    # other than CANCELLED without being in the allowed-COMPLETED set.
    DB_BAD="$(echo "$DB_CH" | jq -c --argjson ok "$ALLOWED_COMPLETED" \
        '[.[] | select(
            (.state == "PENDING" or .state == "RUNNING")
            or (.state != "CANCELLED" and ((.id as $i | $ok | index($i)) == null))
            or (.state == "FAILED")
         )]')"
    if [ "$DB_N" = "20" ] && [ "$DB_BAD" = "[]" ]; then
        DB_CASCADE_ELAPSED="$(elapsed "$T_DB")"
        break
    fi
    sleep 0.5
done
[ -n "$DB_CASCADE_ELAPSED" ] || fail "AC3-DB: cascade incomplete 7 s after parent CANCELLED — children in DB ($(echo "$DB_CH" | jq 'length') rows), violations: $DB_BAD"
DB_OK="$(python3 -c "print(1 if float('$DB_CASCADE_ELAPSED') <= 5.0 else 0)")"
[ "$DB_OK" = "1" ] || fail "AC3-DB: children reached CANCELLED only ${DB_CASCADE_ELAPSED}s after parent terminal (> 5 s budget)"
# No child may hold an active task in the DB.
DB_ACTIVE="$(sqlite3 -json "file:$DB?mode=ro" \
    "SELECT ct.id, ct.state, ct.submission_id FROM tasks ct
     WHERE ct.submission_id IN (
        SELECT s.id FROM submissions s
        JOIN tasks t ON s.parent_task_id = t.id
        WHERE t.submission_id = '$P3' AND t.executor_type = 'subworkflow')
     AND ct.state IN ('PENDING','SCHEDULED','QUEUED','RUNNING');" 2>/dev/null)"
[ -z "$DB_ACTIVE" ] || [ "$DB_ACTIVE" = "[]" ] || fail "AC3-DB: children still hold active tasks in DB: $DB_ACTIVE"
DB_DIST="$(echo "$DB_CH" | jq -c 'group_by(.state) | map({(.[0].state): length}) | add')"
pass "AC3-DB: all 20 children terminal in DB ${DB_CASCADE_ELAPSED}s after parent CANCELLED, no active child tasks; DB states: $DB_DIST"

# Settle, then verify: parent STAYS CANCELLED (CAS — no resurrection), and
# every child is terminal with no ex-PENDING child having run.
sleep 5
PARENT_STATE="$(sub_state "$P3")"
[ "$PARENT_STATE" = "CANCELLED" ] || fail "AC3: parent did not STAY CANCELLED — now '$PARENT_STATE' (finalize clobbered the cancel)"
CH3="$(children "$IDS3")"
NONTERM="$(echo "$CH3" | jq -c '[.[] | select(.state == "PENDING" or .state == "RUNNING") | .id]')"
[ "$NONTERM" = "[]" ] || fail "AC3: non-terminal children remain after cancel settled: $NONTERM"
ESCAPED="$(echo "$CH3" | jq -c --argjson nt "$NONTERM_AT_CANCEL" --argjson run "$RUNNING_AT_CANCEL" \
    '[.[] | select(.id as $i | $nt | index($i) != null) | select(.id as $i | $run | index($i) == null) | select(.state != "CANCELLED") | {id, state}]')"
[ "$ESCAPED" = "[]" ] || fail "AC3: children that were PENDING at cancel did not end CANCELLED: $ESCAPED"
# Running children were killed: no child may retain an active worker task.
for cid in $(echo "$CH3" | jq -r '.[].id'); do
    ACTIVE="$(api "$BASE/submissions/$cid/tasks?limit=500" \
        | jq -c '[.data // [] | .[] | select(.state == "PENDING" or .state == "SCHEDULED" or .state == "QUEUED" or .state == "RUNNING") | .id]')"
    [ "$ACTIVE" = "[]" ] || fail "AC3: child $cid still has active tasks after cancel settled: $ACTIVE"
done
FINAL_DIST="$(echo "$CH3" | jq -c 'group_by(.state) | map({(.[0].state): length}) | add')"
pass "AC3: parent CANCELLED ${CANCEL_ELAPSED}s after cancel and stayed CANCELLED; children final states: $FINAL_DIST"

echo ""
echo "ALL ACCEPTANCE CRITERIA PASSED"
echo "  AC1 wall: ${AC1_WALL}s (< 60 s), overlapping pairs: $OVERLAPS"
echo "  AC2 dispatch latency: ${AC2_ELAPSED}s"
echo "  AC3 cancel-to-terminal: ${CANCEL_ELAPSED}s"
echo "  AC3-DB cascade-to-CANCELLED (all 20 children, in DB): ${DB_CASCADE_ELAPSED}s"
echo "  AC4 output order: a b c d e f"
exit 0
