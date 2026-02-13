#!/usr/bin/env bash
#
# End-to-end test: submit test-pipeline.cwl (Date → Sleep) against live BV-BRC
# and poll until completion.
#
# Prerequisites:
#   - GoWe server running on $GOWE_SERVER (default http://localhost:8080)
#   - Valid BV-BRC token (BVBRC_TOKEN env or ~/.patric_token)
#
# Usage:
#   ./scripts/test-e2e.sh                      # uses running server
#   ./scripts/test-e2e.sh --start-server       # starts a temporary server
#
set -euo pipefail

GOWE_SERVER="${GOWE_SERVER:-http://localhost:8080}"
WORKFLOW="cwl/workflows/test-pipeline.cwl"
INPUTS="cwl/jobs/test-pipeline.yml"
POLL_INTERVAL=10
MAX_POLLS=60  # 10 minutes max
SERVER_PID=""
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        echo "Stopping temporary server (PID $SERVER_PID)..."
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# --- Optionally start a temporary server ---
if [[ "${1:-}" == "--start-server" ]]; then
    DB_PATH="${TMPDIR:-/tmp}/gowe-e2e-$$.db"
    echo "Starting temporary server (db: $DB_PATH)..."
    cd "$PROJECT_DIR"
    go run ./cmd/server/ --db "$DB_PATH" --debug 2>/dev/null &
    SERVER_PID=$!
    # Wait for server to be ready.
    for i in $(seq 1 20); do
        if curl -sf "$GOWE_SERVER/api/v1/workflows/" >/dev/null 2>&1; then
            echo "Server ready."
            break
        fi
        if [ "$i" -eq 20 ]; then
            echo "FAIL: server did not start within 20 seconds."
            exit 1
        fi
        sleep 1
    done
fi

cd "$PROJECT_DIR"

# --- Submit ---
echo "Submitting $WORKFLOW..."
SUBMIT_OUTPUT=$(go run ./cmd/cli/ submit "$WORKFLOW" --inputs "$INPUTS" --server "$GOWE_SERVER" 2>&1)
echo "$SUBMIT_OUTPUT"

# Extract submission ID from output like "Submission created: sub_xxx (state: PENDING)"
SUB_ID=$(echo "$SUBMIT_OUTPUT" | grep -oE 'sub_[0-9a-f-]+' | head -1)
if [ -z "$SUB_ID" ]; then
    echo "FAIL: could not extract submission ID from output."
    exit 1
fi
echo ""
echo "Submission ID: $SUB_ID"
echo "Polling every ${POLL_INTERVAL}s (max ${MAX_POLLS} polls)..."
echo ""

# --- Poll ---
for i in $(seq 1 $MAX_POLLS); do
    RESP=$(curl -sf "$GOWE_SERVER/api/v1/submissions/$SUB_ID")
    STATE=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['state'])")

    # Print task states.
    TASKS=$(echo "$RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
parts=[]
for t in d.get('tasks',[]):
    ext = t.get('external_id','')
    ext_str = f' (job {ext})' if ext else ''
    parts.append(f\"{t['step_id']}={t['state']}{ext_str}\")
print('  '.join(parts))
")
    printf "[%s] %-12s %s\n" "$(date +%H:%M:%S)" "$STATE" "$TASKS"

    if [ "$STATE" = "COMPLETED" ]; then
        echo ""
        echo "========================================="
        echo "  PASS: pipeline completed successfully"
        echo "========================================="

        # Print final status.
        go run ./cmd/cli/ status "$SUB_ID" --server "$GOWE_SERVER" 2>&1
        exit 0
    fi

    if [ "$STATE" = "FAILED" ]; then
        echo ""
        echo "========================================="
        echo "  FAIL: pipeline failed"
        echo "========================================="

        # Print status + task details.
        go run ./cmd/cli/ status "$SUB_ID" --server "$GOWE_SERVER" 2>&1
        echo ""

        # Attempt to fetch logs for failed tasks.
        echo "$RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
for t in d.get('tasks',[]):
    if t['state'] == 'FAILED':
        print(f\"Task {t['step_id']} ({t['id']}) FAILED\")
" | while read -r line; do
            echo "$line"
        done
        exit 1
    fi

    sleep $POLL_INTERVAL
done

echo ""
echo "FAIL: timed out after $((MAX_POLLS * POLL_INTERVAL)) seconds."
exit 1
