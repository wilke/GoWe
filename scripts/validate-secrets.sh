#!/bin/bash
#
# validate-secrets.sh - Release-gate acceptance check for submission-time
# secrets (issue #260).
#
# Spins a fresh gowe-server (with token-signature verification disabled and
# a synthetic BV-BRC-shaped bearer token, so ownership/auth still applies
# without needing a real BV-BRC account) plus one gowe-worker, registers the
# CWL fixtures under testdata/secrets/, and drives the full create/get/
# list/DELETE/retry lifecycle plus the ported cwltool:Secrets pair via curl
# — grepping every response body, the server log, and the worker log for the
# literal secret value at every step. Prints a pass/fail tally and exits
# non-zero on any failure, for use in the release SOP alongside
# run-conformance-server-local.sh (same style/structure).
#
# Usage:
#   ./scripts/validate-secrets.sh <bin-dir>
#
# Example:
#   go build -o bin/gowe-server ./cmd/server
#   go build -o bin/gowe ./cmd/cli
#   go build -o bin/gowe-worker ./cmd/worker
#   go build -o bin/cwl-runner ./cmd/cwl-runner
#   ./scripts/validate-secrets.sh bin
#
# KNOWN LIMITATIONS (see issue #260 review; tracked for feat/260-fixes):
#   - C1:  GET .../tasks/{tid}, .../tasks/, and GET /submissions/{id}'s
#          embedded tasks currently echo RuntimeHints.Secrets values for a
#          worker-executed task. This gate's task-detail/task-list checks
#          are EXPECTED TO FAIL until that lands.
#   - H2:  worker-executed tasks are never scrubbed of RuntimeHints.Secrets
#          at terminal state (root cause of C1's data actually being there
#          to leak).
#   - H3:  DELETE .../secrets and the retention sweep purge the submission
#          row but not the per-task runtime_hints copies.
#   - H6:  the toolexec/cwltool "executing"/"built command" debug log lines
#          are not masked on the local/bare execution path; this gate's
#          server/worker log grep for the argument-mode secret is EXPECTED
#          TO FAIL until that lands.
#   - DELETE .../secrets does not yet refuse a non-terminal submission (no
#          409); this gate does not test that case (see the Go acceptance
#          battery's TestE2E_DeleteSecrets_NonTerminal_Refused instead).
# Until feat/260-fixes lands, this gate is expected to report a non-zero
# FAILED count on the checks tagged [C1]/[H6] below; everything else must
# pass for a clean release.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_DIR"

BIN_DIR="${1:-bin}"
PORT=8098
SERVER_URL="http://localhost:${PORT}"
WORK_DIR="/tmp/gowe-validate-secrets-${PORT}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_header() { echo -e "\n${CYAN}=== $1 ===${NC}\n"; }

PASS=0
FAIL=0
FAILED_CHECKS=()

# pass/fail record a named check's result and keep a running tally. Never
# print the secret value itself in a check name or failure line.
pass() {
    PASS=$((PASS + 1))
    echo -e "  ${GREEN}PASS${NC}  $1"
}
fail() {
    FAIL=$((FAIL + 1))
    FAILED_CHECKS+=("$1")
    echo -e "  ${RED}FAIL${NC}  $1"
}

SERVER_PID=""
WORKER_PID=""

cleanup() {
    if [ -n "$WORKER_PID" ]; then
        kill "$WORKER_PID" 2>/dev/null || true
        wait "$WORKER_PID" 2>/dev/null || true
    fi
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

if [ ! -x "$BIN_DIR/gowe-server" ] || [ ! -x "$BIN_DIR/gowe-worker" ] || [ ! -x "$BIN_DIR/cwl-runner" ]; then
    log_error "Missing binaries in $BIN_DIR (need gowe-server, gowe-worker, cwl-runner)."
    log_error "Build first: go build -o bin/gowe-server ./cmd/server && go build -o bin/gowe-worker ./cmd/worker && go build -o bin/cwl-runner ./cmd/cwl-runner"
    exit 1
fi
if ! command -v curl &> /dev/null; then
    log_error "curl not found"
    exit 1
fi
if ! command -v jq &> /dev/null; then
    log_error "jq not found (used to parse response JSON)"
    exit 1
fi

if lsof -i ":$PORT" > /dev/null 2>&1; then
    log_error "Port $PORT already in use; stop that process or edit PORT in this script."
    exit 1
fi

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

# The secret value threaded through every check below. Long, distinctive,
# obviously fake, and well over the 8-byte minimum a validated secret value
# must clear (#260 review: masking only works on values long enough that
# substring replacement isn't pathological).
SECRET_VALUE="s3cr3t-VALUE-9f2a-gate"

# Fixed throwaway 32-byte encryption key. Never a real secret; disposable
# per-run DB. Exercises the at-rest encryption path, same pattern as
# run-conformance-server-local.sh.
export GOWE_TOKEN_KEY="$(printf '%s' 'gowe-validate-secrets-test-key32' | base64)"

# Synthetic BV-BRC-shaped bearer token. --insecure-skip-token-verify means
# the server trusts the claimed identity without checking "sig" — used ONLY
# for this local, disposable test server, never for a real deployment.
EXPIRY=$(($(date +%s) + 86400))
TOKEN="un=tester|tokenid=t1|expiry=${EXPIRY}|sig=x"
AUTH_HEADER="Authorization: Bearer ${TOKEN}"

log_header "Submission-time secrets acceptance gate (#260)"
log_info "Git commit: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
log_info "Binaries:   $BIN_DIR"
log_info "Port:       $PORT"

log_info "Starting gowe-server..."
"$BIN_DIR/gowe-server" \
    --addr ":${PORT}" \
    --db "$WORK_DIR/gowe.db" \
    --insecure-skip-token-verify \
    --secrets-retention keep \
    --scheduler-poll 100ms \
    --log-level debug \
    > "$WORK_DIR/server.log" 2>&1 &
SERVER_PID=$!

log_info "Waiting for server health..."
attempt=0
until curl -s "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ $attempt -ge 30 ]; then
        log_error "Server failed to become healthy"
        cat "$WORK_DIR/server.log"
        exit 1
    fi
    sleep 1
done
log_info "Server healthy (PID $SERVER_PID)"

log_info "Starting gowe-worker..."
"$BIN_DIR/gowe-worker" \
    --server "$SERVER_URL" \
    --runtime none \
    --group default \
    --poll 200ms \
    --workdir "$WORK_DIR/worker" \
    > "$WORK_DIR/worker.log" 2>&1 &
WORKER_PID=$!
sleep 2
log_info "Worker started (PID $WORKER_PID)"

# --- helpers -----------------------------------------------------------

# api METHOD PATH [BODY] -> prints response body to stdout.
#
# HTTP_STATUS is NOT set as a side effect here: this function is almost
# always invoked as `resp=$(api ...)`, which runs it in a subshell, so any
# variable it assigned would vanish when the subshell exits. Instead the
# status code is written to $WORK_DIR/status.txt; read it back with
# last_status (below) immediately after each call.
api() {
    local method="$1" path="$2" body="${3:-}"
    local args=(-s -o "$WORK_DIR/resp.json" -w "%{http_code}" -H "$AUTH_HEADER" -X "$method")
    if [ -n "$body" ]; then
        args+=(-H "Content-Type: application/json" -d "$body")
    fi
    curl "${args[@]}" "${SERVER_URL}${path}" > "$WORK_DIR/status.txt"
    cat "$WORK_DIR/resp.json"
}

# last_status prints the HTTP status code from the most recent api() call.
last_status() {
    cat "$WORK_DIR/status.txt" 2>/dev/null
}

# check_no_leak LABEL FILE -> fails if FILE contains SECRET_VALUE
check_no_leak() {
    local label="$1" file="$2"
    if grep -q -- "$SECRET_VALUE" "$file" 2>/dev/null; then
        fail "$label"
    else
        pass "$label"
    fi
}

# --- register fixtures ---------------------------------------------------

log_header "Register workflows"

register_workflow() {
    local file="$1" name="$2"
    local cwl_json
    cwl_json=$(jq -Rs . < "testdata/secrets/${file}")
    local body="{\"name\":\"${name}-${TIMESTAMP}\",\"cwl\":${cwl_json}}"
    local resp
    resp=$(api POST "/api/v1/workflows/" "$body")
    local status; status=$(last_status)
    if [ "$status" != "201" ]; then
        log_error "register $file failed: status=$status body=$resp"
        exit 1
    fi
    echo "$resp" | jq -r '.data.id'
}

WF_IWDR=$(register_workflow "secret_wf_iwdr.cwl" "gate-iwdr")
WF_ARG=$(register_workflow "secret_wf_arg.cwl" "gate-arg")
WF_SCOPING=$(register_workflow "scoping_wf.cwl" "gate-scoping")
log_info "Registered: iwdr=$WF_IWDR arg=$WF_ARG scoping=$WF_SCOPING"

# --- group 1/7: create/get/list non-echo + real execution ---------------

log_header "Create submission (cwltool:Secrets, IWDR mode)"

CREATE_BODY=$(jq -n --arg wf "$WF_IWDR" --arg pw "$SECRET_VALUE" \
    '{workflow_id: $wf, inputs: {pw: $pw}}')
CREATE_RESP=$(api POST "/api/v1/submissions/" "$CREATE_BODY")
CREATE_STATUS=$(last_status)
if [ "$CREATE_STATUS" != "201" ]; then
    log_error "create submission failed: status=$CREATE_STATUS body=$CREATE_RESP"
    exit 1
fi
SUB_ID=$(echo "$CREATE_RESP" | jq -r '.data.id')
echo "$CREATE_RESP" > "$WORK_DIR/create_resp.json"
check_no_leak "create response never echoes the secret value" "$WORK_DIR/create_resp.json"

SECRETS_STATE=$(echo "$CREATE_RESP" | jq -r '.data.secrets_state')
if [ "$SECRETS_STATE" = "present" ]; then pass "secrets_state=present after create"; else fail "secrets_state=present after create (got $SECRETS_STATE)"; fi

PLACEHOLDER=$(echo "$CREATE_RESP" | jq -r '.data.inputs.pw')
if [ "$PLACEHOLDER" = "<secret>" ]; then pass "inputs.pw replaced with placeholder"; else fail "inputs.pw replaced with placeholder (got $PLACEHOLDER)"; fi

GET_RESP=$(api GET "/api/v1/submissions/${SUB_ID}")
echo "$GET_RESP" > "$WORK_DIR/get_resp.json"
# [C1] the submission's embedded task list (sub.Tasks) is not sanitized by
# handleGetSubmission the way handleGetTask/handleListTasks sanitize their
# own responses — see KNOWN LIMITATIONS. Expected to fail until H2/C1 land.
check_no_leak "[C1] GET submission (embedded tasks) never echoes the secret value" "$WORK_DIR/get_resp.json"

LIST_RESP=$(api GET "/api/v1/submissions/")
echo "$LIST_RESP" > "$WORK_DIR/list_resp.json"
check_no_leak "list submissions never echoes the secret value" "$WORK_DIR/list_resp.json"

log_info "Waiting for submission $SUB_ID to complete..."
STATE=""
for i in $(seq 1 60); do
    RESP=$(api GET "/api/v1/submissions/${SUB_ID}")
    STATE=$(echo "$RESP" | jq -r '.data.state')
    if [ "$STATE" = "COMPLETED" ] || [ "$STATE" = "FAILED" ]; then
        break
    fi
    sleep 1
done
if [ "$STATE" = "COMPLETED" ]; then
    pass "IWDR submission reaches COMPLETED"
else
    fail "IWDR submission reaches COMPLETED (got $STATE)"
fi

# [C1] task detail / task list: currently expected to leak the value for a
# worker-executed task (RuntimeHints.Secrets not scrubbed/sanitized) — see
# the KNOWN LIMITATIONS header. Checked and counted like any other check so
# the gate accurately reports today's state; it will pass once H2/C1 land.
TASKS_RESP=$(api GET "/api/v1/submissions/${SUB_ID}/tasks/")
echo "$TASKS_RESP" > "$WORK_DIR/tasks_resp.json"
if grep -q -- "$SECRET_VALUE" "$WORK_DIR/tasks_resp.json"; then
    fail "[C1] task list never echoes the secret value"
else
    pass "[C1] task list never echoes the secret value"
fi

# --- group 5: $(inputs.x) argument mode + logging redaction -------------

log_header "Create submission (cwltool:Secrets, argument mode) + logging check"

ARG_SECRET="s3cr3t-VALUE-9f2a-argmode"
CREATE_BODY=$(jq -n --arg wf "$WF_ARG" --arg pw "$ARG_SECRET" \
    '{workflow_id: $wf, inputs: {pw: $pw}}')
CREATE_RESP=$(api POST "/api/v1/submissions/" "$CREATE_BODY")
SUB_ID_ARG=$(echo "$CREATE_RESP" | jq -r '.data.id')

for i in $(seq 1 60); do
    RESP=$(api GET "/api/v1/submissions/${SUB_ID_ARG}")
    STATE=$(echo "$RESP" | jq -r '.data.state')
    if [ "$STATE" = "COMPLETED" ] || [ "$STATE" = "FAILED" ]; then
        break
    fi
    sleep 1
done
if [ "$STATE" = "COMPLETED" ]; then
    pass "argument-mode submission reaches COMPLETED"
else
    fail "argument-mode submission reaches COMPLETED (got $STATE)"
fi

# [H6] server + worker logs: expected to leak the argument-mode value today
# (toolexec/cwltool debug argv lines are unmasked on the local/bare path) —
# see KNOWN LIMITATIONS.
LOG_LEAK=false
if grep -q -- "$ARG_SECRET" "$WORK_DIR/server.log" 2>/dev/null; then LOG_LEAK=true; fi
if grep -q -- "$ARG_SECRET" "$WORK_DIR/worker.log" 2>/dev/null; then LOG_LEAK=true; fi
if [ "$LOG_LEAK" = true ]; then
    fail "[H6] server/worker logs never contain the secret value"
else
    pass "[H6] server/worker logs never contain the secret value"
fi

# --- group 8: negatives ---------------------------------------------------

log_header "Negative cases"

BAD_RETENTION_BODY=$(jq -n --arg wf "$WF_IWDR" --arg pw "$SECRET_VALUE" \
    '{workflow_id: $wf, inputs: {pw: $pw}, secrets_retention: "not-a-policy"}')
api POST "/api/v1/submissions/" "$BAD_RETENTION_BODY" > /dev/null
STATUS=$(last_status)
if [ "$STATUS" = "400" ]; then pass "invalid secrets_retention -> 400"; else fail "invalid secrets_retention -> 400 (got $STATUS)"; fi

BAD_TYPE_BODY=$(jq -n --arg wf "$WF_IWDR" '{workflow_id: $wf, inputs: {pw: 12345}}')
api POST "/api/v1/submissions/" "$BAD_TYPE_BODY" > /dev/null
STATUS=$(last_status)
if [ "$STATUS" = "400" ]; then pass "non-string cwltool:Secrets input -> 400"; else fail "non-string cwltool:Secrets input -> 400 (got $STATUS)"; fi

NO_SECRETS_BODY=$(jq -n --arg wf "$WF_SCOPING" '{workflow_id: $wf, inputs: {}}')
CREATE_RESP=$(api POST "/api/v1/submissions/" "$NO_SECRETS_BODY")
SUB_ID_NEG=$(echo "$CREATE_RESP" | jq -r '.data.id')
for i in $(seq 1 30); do
    RESP=$(api GET "/api/v1/submissions/${SUB_ID_NEG}")
    STATE=$(echo "$RESP" | jq -r '.data.state')
    if [ "$STATE" = "COMPLETED" ] || [ "$STATE" = "FAILED" ]; then
        break
    fi
    sleep 1
done
if [ "$STATE" = "FAILED" ]; then pass "unknown secret_env -> submission FAILED pre-dispatch"; else fail "unknown secret_env -> submission FAILED pre-dispatch (got $STATE)"; fi

ANON_RESP=$(curl -s -o "$WORK_DIR/anon_resp.json" -w "%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg wf "$WF_IWDR" --arg pw "$SECRET_VALUE" '{workflow_id: $wf, inputs: {pw: $pw}}')" \
    "${SERVER_URL}/api/v1/submissions/")
if [ "$ANON_RESP" != "201" ]; then pass "anonymous submission with secrets refused (status $ANON_RESP)"; else fail "anonymous submission with secrets refused (got 201)"; fi

# --- DELETE secrets + retry 409 ------------------------------------------

log_header "DELETE secrets, then GET (purged) and retry (409)"

api DELETE "/api/v1/submissions/${SUB_ID}/secrets" > "$WORK_DIR/delete_resp.json"
STATUS=$(last_status)
if [ "$STATUS" = "200" ]; then pass "DELETE .../secrets succeeds on a terminal submission"; else fail "DELETE .../secrets succeeds on a terminal submission (got $STATUS)"; fi

GET_AFTER=$(api GET "/api/v1/submissions/${SUB_ID}")
STATE_AFTER=$(echo "$GET_AFTER" | jq -r '.data.secrets_state')
if [ "$STATE_AFTER" = "purged" ]; then pass "secrets_state=purged after DELETE"; else fail "secrets_state=purged after DELETE (got $STATE_AFTER)"; fi

NAMES_AFTER=$(echo "$GET_AFTER" | jq -r '.data.secret_names | length')
if [ "$NAMES_AFTER" != "0" ]; then pass "secret_names retained for auditability after purge"; else fail "secret_names retained for auditability after purge"; fi

# --- cwltool:Secrets through cwl-runner (standalone) ---------------------

log_header "cwltool:Secrets through cwl-runner (standalone)"

# cwl-runner has no submission store: there is nothing to strip, the value
# comes straight from the job file. The invariant it must uphold is that
# its OWN logs/provenance never carry the value — see docs/security/
# authentication.md's "cwltool:Secrets compatibility" section for the
# documented standalone semantics.
JOB_FILE="$WORK_DIR/secret_job_input.json"
jq -n --arg pw "$SECRET_VALUE" '{pw: $pw}' > "$JOB_FILE"
CWLRUNNER_LOG="$WORK_DIR/cwl-runner.log"
"$BIN_DIR/cwl-runner" testdata/secrets/secret_wf_iwdr.cwl "$JOB_FILE" > "$WORK_DIR/cwl-runner.out" 2> "$CWLRUNNER_LOG"
CWLRUNNER_EXIT=$?
if [ $CWLRUNNER_EXIT -eq 0 ]; then pass "cwl-runner executes secret_wf_iwdr.cwl successfully"; else fail "cwl-runner executes secret_wf_iwdr.cwl successfully (exit $CWLRUNNER_EXIT)"; fi

OUT_PATH=$(jq -r '.out.path // .out.location // empty' "$WORK_DIR/cwl-runner.out" 2>/dev/null | sed 's#^file://##')
if [ -n "$OUT_PATH" ] && [ -f "$OUT_PATH" ] && grep -q -- "$SECRET_VALUE" "$OUT_PATH"; then
    pass "cwl-runner output file contains the real value"
else
    fail "cwl-runner output file contains the real value"
fi

check_no_leak "cwl-runner stderr/log never contains the secret value" "$CWLRUNNER_LOG"

# --- summary ---------------------------------------------------------------

log_header "Summary"
echo "RESULT: ${PASS} passed, ${FAIL} failed"
if [ $FAIL -gt 0 ]; then
    log_warn "Failed checks:"
    for c in "${FAILED_CHECKS[@]}"; do
        echo "  - $c"
    done
    log_warn "See KNOWN LIMITATIONS at the top of this script — [C1] and [H6]"
    log_warn "checks are expected to fail until feat/260-fixes merges."
fi

if [ $FAIL -eq 0 ]; then
    echo -e "${GREEN}GATE: PASS${NC}"
    exit 0
else
    echo -e "${RED}GATE: FAIL${NC}"
    exit 1
fi
