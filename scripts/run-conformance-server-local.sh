#!/bin/bash
#
# run-conformance-server-local.sh - Run CWL conformance tests with server using local execution
#
# This script:
# 1. Builds the server and gowe CLI
# 2. Starts the server with local executor (no docker-compose)
# 3. Runs cwltest with gowe run as the tool
# 4. Reports results
# 5. Cleans up
#
# Usage:
#   ./scripts/run-conformance-server-local.sh [options] [tags]
#
# Options:
#   -p, --port PORT    Port for server (default: 8091)
#   -k, --keep         Keep server running after tests
#   -h, --help         Show this help message
#
# Examples:
#   ./scripts/run-conformance-server-local.sh                    # Run required tests
#   ./scripts/run-conformance-server-local.sh -p 9091            # Use port 9091
#   ./scripts/run-conformance-server-local.sh required           # Run required tests
#   ./scripts/run-conformance-server-local.sh initial_work_dir   # Run IWDR tests
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
PORT=8091
KEEP_SERVER=false
TAGS=""

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
            KEEP_SERVER=true
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

# Default tags to "required" if not specified
TAGS="${TAGS:-required}"

SERVER_URL="http://localhost:${PORT}"
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        if [ "$KEEP_SERVER" = false ]; then
            log_info "Stopping server (PID $SERVER_PID)..."
            kill "$SERVER_PID" 2>/dev/null || true
            wait "$SERVER_PID" 2>/dev/null || true
        else
            log_info "Keeping server running (PID $SERVER_PID)"
            log_info "Stop with: kill $SERVER_PID"
        fi
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

log_header "CWL v1.2 Conformance Tests (Server-Local)"

log_info "Using port: $PORT"
log_info "Tags: $TAGS"

check_port_available

# Build the binaries
log_info "Building server..."
go build -o bin/server ./cmd/server

log_info "Building gowe CLI..."
go build -o bin/gowe ./cmd/cli

# Start server with local executor and fast polling for tests
log_info "Starting server with local executor..."
./bin/server \
    -addr ":${PORT}" \
    -default-executor local \
    -allow-anonymous \
    -anonymous-executors "local,docker,worker,container" \
    -scheduler-poll 100ms \
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

# Print configuration
log_info "Server: ${SERVER_URL} (default-executor: local)"

# Create a wrapper script for cwltest (it expects a single executable)
WRAPPER_SCRIPT=$(mktemp)
cat > "$WRAPPER_SCRIPT" << EOF
#!/bin/bash
exec "$PROJECT_DIR/bin/gowe" run --server ${SERVER_URL} --quiet "\$@"
EOF
chmod +x "$WRAPPER_SCRIPT"

# Cleanup wrapper on exit
cleanup_wrapper() {
    rm -f "$WRAPPER_SCRIPT"
}
trap 'cleanup; cleanup_wrapper' EXIT

# Run conformance tests
log_header "Running Conformance Tests"

cd "$CONFORMANCE_DIR"

log_info "Running cwltest with tags: $TAGS"

cwltest \
    --test conformance_tests.yaml \
    --tool "$WRAPPER_SCRIPT" \
    --tags "$TAGS" \
    --verbose \
    2>&1 | tee "$PROJECT_DIR/conformance-server-local-results.txt"

RESULT=${PIPESTATUS[0]}

cd "$PROJECT_DIR"

if [ $RESULT -eq 0 ]; then
    log_header "All $TAGS tests passed!"
else
    log_header "Some tests failed"
    exit 1
fi
