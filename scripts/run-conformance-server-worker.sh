#!/bin/bash
#
# run-conformance-server-worker.sh - Run CWL conformance tests with server + worker execution
#
# This script:
# 1. Starts a server with worker executor
# 2. Starts N workers (with optional GPU assignment)
# 3. Runs cwltest with gowe run as the tool
# 4. Monitors progress and aborts if stalled
# 5. Reports results and cleans up
#
# Usage:
#   ./scripts/run-conformance-server-worker.sh [options] [tags]
#
# Options:
#   -p, --port PORT          Port for server (default: 8094)
#   -w, --workers N          Number of workers (default: 2)
#   -g, --gpu-ids IDS        Comma-separated GPU IDs (e.g., "6,7")
#   -j, --parallel N         Parallel cwltest jobs (default: 4)
#   -t, --timeout SECS       Per-test timeout (default: 60)
#   -s, --stall-timeout SECS Abort after this many seconds of no progress (default: 120)
#   -n, --tests NUMS         Run specific test numbers (e.g., "1,3-6")
#   -k, --keep               Keep server/workers running after tests
#   -h, --help               Show this help message
#
# Examples:
#   ./scripts/run-conformance-server-worker.sh                       # All tests, 2 workers
#   ./scripts/run-conformance-server-worker.sh -g 6,7                # Workers on GPU 6,7
#   ./scripts/run-conformance-server-worker.sh -n 87,239             # Specific tests
#   ./scripts/run-conformance-server-worker.sh -w 1 -g 0 required   # 1 worker, required only
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
PORT=8094
NUM_WORKERS=2
GPU_IDS=""
PARALLEL=4
TEST_TIMEOUT=60
STALL_TIMEOUT=120
TEST_NUMS=""
KEEP=false
TAGS=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info()   { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()   { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error()  { echo -e "${RED}[ERROR]${NC} $1"; }
log_header() { echo -e "\n${CYAN}=== $1 ===${NC}\n"; }

usage() {
    sed -n '2,32p' "$0" | sed 's/^# \?//'
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -p|--port)       PORT="$2"; shift 2 ;;
        -w|--workers)    NUM_WORKERS="$2"; shift 2 ;;
        -g|--gpu-ids)    GPU_IDS="$2"; shift 2 ;;
        -j|--parallel)   PARALLEL="$2"; shift 2 ;;
        -t|--timeout)    TEST_TIMEOUT="$2"; shift 2 ;;
        -s|--stall-timeout) STALL_TIMEOUT="$2"; shift 2 ;;
        -n|--tests)      TEST_NUMS="$2"; shift 2 ;;
        -k|--keep)       KEEP=true; shift ;;
        -h|--help)       usage ;;
        -*)              log_error "Unknown option: $1"; usage ;;
        *)               TAGS="$1"; shift ;;
    esac
done

# Derived paths
WORK_DIR="/tmp/gowe-test-worker-${PORT}"
SERVER_URL="http://localhost:${PORT}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
REPORT="$PROJECT_DIR/conformance-results-server-worker-${TIMESTAMP}.txt"

# Parse GPU IDs into array
IFS=',' read -ra GPU_ARR <<< "${GPU_IDS:-}"

# Track PIDs for cleanup
SERVER_PID=""
WORKER_PIDS=()
CWLTEST_PID=""

cleanup() {
    if [ "$KEEP" = true ]; then
        log_info "Keeping server and workers running (--keep)"
        return
    fi
    # Kill cwltest first
    if [ -n "$CWLTEST_PID" ] && kill -0 "$CWLTEST_PID" 2>/dev/null; then
        kill "$CWLTEST_PID" 2>/dev/null || true
    fi
    # Kill workers
    for pid in "${WORKER_PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    sleep 1
    # Kill server
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# --- Preflight checks ---

if ! command -v cwltest &> /dev/null; then
    log_error "cwltest not found. Install with: pip install cwltest"
    exit 1
fi

if [ ! -f testdata/cwl-v1.2/conformance_tests.yaml ]; then
    log_error "Conformance tests not found: testdata/cwl-v1.2/conformance_tests.yaml"
    exit 1
fi

if [ ! -x ./bin/gowe-server ] || [ ! -x ./bin/gowe-worker ] || [ ! -x ./bin/gowe ]; then
    log_error "Binaries not found. Build first (make build or /build)"
    exit 1
fi

if curl -sf "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
    log_error "Port ${PORT} already in use"
    exit 1
fi

log_header "CWL v1.2 Conformance Tests (Server-Worker)"
log_info "Port: ${PORT}, Workers: ${NUM_WORKERS}, GPUs: ${GPU_IDS:-none}"
log_info "Stall timeout: ${STALL_TIMEOUT}s"
log_info "Report: ${REPORT}"

# --- Setup ---

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"/{uploads,logs}
for i in $(seq 1 "$NUM_WORKERS"); do
    mkdir -p "$WORK_DIR/workdir/w${i}"
done

# Build download dirs list
DOWNLOAD_DIRS="$WORK_DIR/uploads"
for i in $(seq 1 "$NUM_WORKERS"); do
    DOWNLOAD_DIRS="$DOWNLOAD_DIRS,$WORK_DIR/workdir/w${i}"
done

# --- Start server ---

log_info "Starting server..."
./bin/gowe-server \
    --addr ":${PORT}" \
    --db "$WORK_DIR/gowe.db" \
    --default-executor worker \
    --allow-anonymous --anonymous-executors "local,worker,container" \
    --scheduler-poll 100ms \
    --upload-backend local \
    --upload-local-dir "$WORK_DIR/uploads" \
    --upload-download-dirs "$DOWNLOAD_DIRS" \
    --log-level debug \
    > "$WORK_DIR/logs/server.log" 2>&1 &
SERVER_PID=$!
log_info "Server PID: $SERVER_PID"

# Wait for health
for attempt in $(seq 1 20); do
    if curl -sf "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
        break
    fi
    sleep 0.5
done
if ! curl -sf "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
    log_error "Server failed to start. Check $WORK_DIR/logs/server.log"
    exit 1
fi
log_info "Server healthy"

# --- Start workers ---

for i in $(seq 1 "$NUM_WORKERS"); do
    GPU_FLAGS=""
    idx=$((i - 1))
    if [ ${#GPU_ARR[@]} -gt 0 ] && [ $idx -lt ${#GPU_ARR[@]} ]; then
        GPU_FLAGS="--gpu --gpu-id ${GPU_ARR[$idx]}"
    fi

    ./bin/gowe-worker \
        --server "$SERVER_URL" \
        --runtime none \
        --name "worker-${i}" \
        --workdir "$WORK_DIR/workdir/w${i}" \
        --stage-out "file://$WORK_DIR/uploads" \
        --poll 200ms \
        --log-level debug \
        $GPU_FLAGS \
        > "$WORK_DIR/logs/worker-${i}.log" 2>&1 &
    WORKER_PIDS+=($!)
    log_info "Worker-${i} PID: ${WORKER_PIDS[-1]} ${GPU_FLAGS:+(${GPU_FLAGS})}"
done

sleep 2
ONLINE=$(curl -sf "${SERVER_URL}/api/v1/workers" 2>/dev/null \
    | python3 -c "import sys,json; w=json.load(sys.stdin).get('data') or []; print(sum(1 for x in w if x['state']=='online'))" 2>/dev/null || echo 0)
log_info "Workers online: ${ONLINE}/${NUM_WORKERS}"

if [ "$ONLINE" -eq 0 ]; then
    log_error "No workers came online. Check $WORK_DIR/logs/"
    exit 1
fi

# --- Create wrapper ---

WRAPPER="$WORK_DIR/wrapper.sh"
cat > "$WRAPPER" << EOF
#!/bin/bash
exec "$PROJECT_DIR/bin/gowe" run --server ${SERVER_URL} --quiet "\$@"
EOF
chmod +x "$WRAPPER"

# --- Run conformance tests ---

log_header "Running Tests"

CWLTEST_ARGS=(
    --test testdata/cwl-v1.2/conformance_tests.yaml
    --tool "$WRAPPER"
    -j"$PARALLEL"
    --timeout="$TEST_TIMEOUT"
    --verbose
)

if [ -n "$TEST_NUMS" ]; then
    CWLTEST_ARGS+=(-n "$TEST_NUMS")
elif [ -n "$TAGS" ]; then
    CWLTEST_ARGS+=(--tags "$TAGS")
fi

cwltest "${CWLTEST_ARGS[@]}" > "$REPORT" 2>&1 &
CWLTEST_PID=$!
log_info "cwltest PID: $CWLTEST_PID"

# --- Progress watchdog ---

POLL_INTERVAL=30
MAX_STALLS=$(( (STALL_TIMEOUT + POLL_INTERVAL - 1) / POLL_INTERVAL ))
LAST_LINES=0
STALL_COUNT=0

while kill -0 $CWLTEST_PID 2>/dev/null; do
    sleep $POLL_INTERVAL
    CURRENT_LINES=$(wc -l < "$REPORT" 2>/dev/null || echo 0)
    PASSED=$(grep -c "^Test \[" "$REPORT" 2>/dev/null || echo 0)
    FAILED=$(grep -c "^Test [0-9]* failed:" "$REPORT" 2>/dev/null || echo 0)

    STATES=$(curl -sf "${SERVER_URL}/api/v1/submissions" 2>/dev/null | python3 -c "
import sys,json
data=json.load(sys.stdin).get('data',[])
s={}
for x in data: s[x['state']]=s.get(x['state'],0)+1
print(' '.join(f'{k}={v}' for k,v in sorted(s.items())))
" 2>/dev/null || echo "?")

    log_info "[$(date +%H:%M:%S)] passed=$PASSED failed=$FAILED | subs: $STATES"

    if [ "$CURRENT_LINES" -eq "$LAST_LINES" ]; then
        STALL_COUNT=$((STALL_COUNT + 1))
        log_warn "No progress (${STALL_COUNT}/${MAX_STALLS}, abort at ${STALL_TIMEOUT}s)"
        if [ "$STALL_COUNT" -ge "$MAX_STALLS" ]; then
            log_error "ABORTING: no progress for ${STALL_TIMEOUT}s"
            kill $CWLTEST_PID 2>/dev/null || true
            sleep 2
            kill -9 $CWLTEST_PID 2>/dev/null || true
            break
        fi
    else
        STALL_COUNT=0
    fi
    LAST_LINES=$CURRENT_LINES
done

wait $CWLTEST_PID 2>/dev/null || true

# --- Results ---

log_header "Results"

if [ -f "$REPORT" ]; then
    echo ""
    tail -3 "$REPORT"
    echo ""

    FAIL_LINES=$(grep "^Test [0-9]* failed:" "$REPORT" | sort -t' ' -k2 -n || true)
    if [ -n "$FAIL_LINES" ]; then
        echo "Failures:"
        echo "$FAIL_LINES"
    fi
    echo ""
    log_info "Full report: $REPORT"
    log_info "Server log:  $WORK_DIR/logs/server.log"
else
    log_error "No report generated"
fi
