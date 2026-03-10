#!/bin/bash
#
# run-conformance-distributed-apptainer.sh - Run CWL conformance tests in distributed mode with Apptainer
#
# This script:
# 1. Builds server, worker, and CLI binaries via Apptainer (no native Go needed)
# 2. Starts the server with worker executor and upload backend
# 3. Starts 2 workers with --runtime apptainer
# 4. Runs cwltest with gowe run as the tool (upload mode)
# 5. Reports results
# 6. Cleans up
#
# Usage:
#   ./scripts/run-conformance-distributed-apptainer.sh [options] [tags]
#
# Options:
#   -p, --port PORT    Port for server (default: 8092)
#   -k, --keep         Keep processes running after tests
#   -h, --help         Show this help message
#
# Examples:
#   ./scripts/run-conformance-distributed-apptainer.sh                    # Run all tests
#   ./scripts/run-conformance-distributed-apptainer.sh -p 9092            # Use port 9092
#   ./scripts/run-conformance-distributed-apptainer.sh required           # Required tests only
#   ./scripts/run-conformance-distributed-apptainer.sh -k                 # Keep processes running
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
PORT=8092
KEEP_PROCESSES=false
TAGS=""
NUM_WORKERS=2

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_header() {
    echo -e "\n${CYAN}=== $1 ===${NC}\n"
}

usage() {
    sed -n '2,27p' "$0" | sed 's/^# \?//'
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -p|--port)
            PORT="$2"
            shift 2
            ;;
        -k|--keep)
            KEEP_PROCESSES=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        -*)
            log_error "Unknown option: $1"
            usage
            ;;
        *)
            TAGS="$1"
            shift
            ;;
    esac
done

SERVER_URL="http://localhost:${PORT}"

# Working directories
BASE_DIR="/tmp/gowe-distributed-$$"
UPLOAD_DIR="$BASE_DIR/gowe/uploads"
DATA_DIR="$BASE_DIR/data"
WORKDIR_BASE="$BASE_DIR/gowe/workdir"
DB_FILE="$BASE_DIR/gowe/gowe.db"

SERVER_PID=""
WORKER_PIDS=()

cleanup() {
    if [ "$KEEP_PROCESSES" = false ]; then
        log_info "Cleaning up..."
        for pid in "${WORKER_PIDS[@]}"; do
            if kill -0 "$pid" 2>/dev/null; then
                kill "$pid" 2>/dev/null || true
                wait "$pid" 2>/dev/null || true
            fi
        done
        if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
            kill "$SERVER_PID" 2>/dev/null || true
            wait "$SERVER_PID" 2>/dev/null || true
        fi
        rm -rf "$BASE_DIR"
        rm -f "$WRAPPER_SCRIPT"
    else
        log_info "Keeping processes running"
        if [ -n "$SERVER_PID" ]; then
            log_info "  Server PID: $SERVER_PID"
        fi
        for i in "${!WORKER_PIDS[@]}"; do
            log_info "  Worker $((i+1)) PID: ${WORKER_PIDS[$i]}"
        done
        log_info "  Working directory: $BASE_DIR"
        log_info "  Stop with: kill $SERVER_PID ${WORKER_PIDS[*]}"
    fi
}

trap cleanup EXIT

# Check if port is already in use
check_port_available() {
    if lsof -i ":$PORT" > /dev/null 2>&1; then
        local pid=$(lsof -t -i ":$PORT" 2>/dev/null | head -1)
        local process=$(ps -p "$pid" -o comm= 2>/dev/null || echo "unknown")
        log_error "Port $PORT is already in use by process $pid ($process)"
        log_error "Either stop that process or use a different port with: -p <port>"
        exit 1
    fi
}

# Check for apptainer
if ! command -v apptainer &> /dev/null; then
    log_error "apptainer not found. This script requires Apptainer for container execution."
    exit 1
fi

# Check for cwltest
if ! command -v cwltest &> /dev/null; then
    log_error "cwltest not found. Install it with: pip install cwltest"
    exit 1
fi

# Check for conformance tests
CONFORMANCE_DIR="testdata/cwl-v1.2"
if [ ! -f "$CONFORMANCE_DIR/conformance_tests.yaml" ]; then
    log_error "Conformance test file not found: $CONFORMANCE_DIR/conformance_tests.yaml"
    log_info "Cloning CWL v1.2 conformance tests..."
    git clone --depth 1 https://github.com/common-workflow-language/cwl-v1.2.git "$CONFORMANCE_DIR"
fi

log_header "CWL v1.2 Conformance Tests (Distributed-Apptainer)"

log_info "Using port: $PORT"
[ -n "$TAGS" ] && log_info "Tags: $TAGS"
log_info "Workers: $NUM_WORKERS"

check_port_available

# Build the binaries via Apptainer
log_info "Building binaries via Apptainer..."
mkdir -p "$PROJECT_DIR/bin" /tmp/gomod
apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c \
    "cd $PROJECT_DIR && go build -o bin/ ./cmd/server ./cmd/worker && go build -o bin/gowe ./cmd/cli"

# Create working directories
mkdir -p "$UPLOAD_DIR" "$DATA_DIR"

# Build the download-dirs list (uploads + data + all worker workdirs)
DOWNLOAD_DIRS="$UPLOAD_DIR,$DATA_DIR"
for i in $(seq 1 $NUM_WORKERS); do
    WORKER_WORKDIR="$WORKDIR_BASE/worker-${i}"
    mkdir -p "$WORKER_WORKDIR"
    DOWNLOAD_DIRS="$DOWNLOAD_DIRS,$WORKER_WORKDIR"
done

# Start server
log_info "Starting server..."
./bin/server \
    -addr ":${PORT}" \
    -db "$DB_FILE" \
    -default-executor worker \
    -allow-anonymous \
    -anonymous-executors "local,docker,worker,container" \
    -scheduler-poll 100ms \
    -upload-backend local \
    -upload-local-dir "$UPLOAD_DIR" \
    -upload-download-dirs "$DOWNLOAD_DIRS" \
    -log-level warn \
    &
SERVER_PID=$!

# Wait for server to be healthy
log_info "Waiting for server health..."
max_attempts=30
attempt=0
while [ $attempt -lt $max_attempts ]; do
    if curl -s "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
        log_info "Server is healthy (PID $SERVER_PID)"
        break
    fi
    attempt=$((attempt + 1))
    sleep 1
done

if [ $attempt -eq $max_attempts ]; then
    log_error "Server failed to become healthy"
    exit 1
fi

# Start workers
log_info "Starting $NUM_WORKERS workers with --runtime apptainer..."
for i in $(seq 1 $NUM_WORKERS); do
    WORKER_WORKDIR="$WORKDIR_BASE/worker-${i}"

    ./bin/worker \
        -server "${SERVER_URL}" \
        -runtime apptainer \
        -name "worker-${i}" \
        -workdir "$WORKER_WORKDIR" \
        -stage-out "file://$DATA_DIR" \
        -poll 500ms \
        -log-level warn \
        &
    WORKER_PIDS+=($!)
    log_info "  worker-${i} started (PID ${WORKER_PIDS[-1]})"
done

# Wait for workers to register
log_info "Waiting for workers to register..."
max_attempts=30
attempt=0
while [ $attempt -lt $max_attempts ]; do
    workers=$(curl -s "${SERVER_URL}/api/v1/workers" 2>/dev/null | grep -o '"id"' | wc -l | tr -d ' ')
    if [ "$workers" -ge "$NUM_WORKERS" ]; then
        break
    fi
    attempt=$((attempt + 1))
    sleep 1
done

# Check worker registration
workers=$(curl -s "${SERVER_URL}/api/v1/workers" 2>/dev/null | grep -o '"id"' | wc -l | tr -d ' ')
log_info "Registered workers: $workers"

if [ "$workers" -eq 0 ]; then
    log_error "No workers registered!"
    exit 1
fi

# Print configuration
log_info "Server: ${SERVER_URL} (default-executor: worker)"
log_info "Workers: $workers registered (runtime: apptainer)"
log_info "Stage-out: file://$DATA_DIR"
log_info "Upload mode: CLI uploads/downloads files via server"

# Create a wrapper script for cwltest (upload mode, no --no-upload)
WRAPPER_SCRIPT=$(mktemp)
cat > "$WRAPPER_SCRIPT" << EOF
#!/bin/bash
export GOWE_SERVER="${SERVER_URL}"
exec "$PROJECT_DIR/bin/gowe" run --quiet "\$@"
EOF
chmod +x "$WRAPPER_SCRIPT"

# Run conformance tests
log_header "Running Conformance Tests"

cd "$CONFORMANCE_DIR"

# Build cwltest command
CWLTEST_CMD="cwltest --test conformance_tests.yaml --tool $WRAPPER_SCRIPT --verbose"

if [ -n "$TAGS" ]; then
    CWLTEST_CMD="$CWLTEST_CMD --tags $TAGS"
fi

log_info "Running: $CWLTEST_CMD"
eval "$CWLTEST_CMD" 2>&1 | tee "$PROJECT_DIR/conformance-distributed-apptainer-results.txt"
RESULT=${PIPESTATUS[0]}

cd "$PROJECT_DIR"

rm -f "$WRAPPER_SCRIPT"
WRAPPER_SCRIPT=""

if [ $RESULT -ne 0 ]; then
    log_error "Some conformance tests failed"
    exit 1
fi

log_header "All Tests Completed"
