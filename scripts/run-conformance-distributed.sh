#!/bin/bash
#
# run-conformance-distributed.sh - Run CWL conformance tests against distributed setup
#
# This script:
# 1. Checks for port conflicts and running containers
# 2. Starts the docker-compose environment with workers
# 3. Builds the gowe CLI with the run command
# 4. Runs cwltest with gowe run as the tool
# 5. Reports results
# 6. Cleans up
#
# Usage:
#   ./scripts/run-conformance-distributed.sh [options] [tags]
#
# Options:
#   -p, --port PORT    Host port for server (default: 8090)
#   -k, --keep         Keep containers running after tests
#   -h, --help         Show this help message
#
# Examples:
#   ./scripts/run-conformance-distributed.sh                    # Run all tests on port 8090
#   ./scripts/run-conformance-distributed.sh -p 9090            # Use port 9090
#   ./scripts/run-conformance-distributed.sh -p 9090 required   # Port 9090, required tests only
#   ./scripts/run-conformance-distributed.sh -k                 # Keep containers after tests
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
PORT=8090
KEEP_CONTAINERS=false
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
            KEEP_CONTAINERS=true
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

cleanup() {
    if [ "$KEEP_CONTAINERS" = false ]; then
        log_info "Cleaning up..."
        docker-compose down -v 2>/dev/null || true
    else
        log_info "Keeping containers running (use 'docker-compose down -v' to stop)"
    fi
}

trap cleanup EXIT

# Check for running containers
check_running_containers() {
    local running=$(docker-compose ps -q 2>/dev/null | wc -l | tr -d ' ')
    if [ "$running" -gt 0 ]; then
        log_warn "Found $running running container(s) from previous run"

        # Check if they're healthy
        if curl -s "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
            log_info "Existing server is healthy on port $PORT"
            read -p "Use existing containers? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                return 0  # Use existing
            fi
        fi

        log_info "Stopping existing containers..."
        docker-compose down -v 2>/dev/null || true
    fi
    return 1  # Need to start new containers
}

# Check if port is already in use by something else
check_port_available() {
    if lsof -i ":$PORT" > /dev/null 2>&1; then
        local pid=$(lsof -t -i ":$PORT" 2>/dev/null | head -1)
        local process=$(ps -p "$pid" -o comm= 2>/dev/null || echo "unknown")

        # Check if it's our docker-compose
        if curl -s "${SERVER_URL}/api/v1/health" 2>/dev/null | grep -q "gowe\|healthy"; then
            return 0  # It's our server
        fi

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
CONFORMANCE_DIR="testdata/cwl-conformance"
if [ ! -d "$CONFORMANCE_DIR" ]; then
    log_error "Conformance test directory not found: $CONFORMANCE_DIR"
    exit 1
fi

log_header "CWL v1.2 Conformance Tests (Distributed)"

log_info "Using port: $PORT"

# Check for existing containers or port conflicts
REUSE_CONTAINERS=false
if check_running_containers; then
    REUSE_CONTAINERS=true
else
    check_port_available
fi

# Build the CLI
log_info "Building gowe CLI..."
go build -o bin/gowe ./cmd/cli

if [ "$REUSE_CONTAINERS" = false ]; then
    # Update docker-compose port mapping dynamically
    export GOWE_HOST_PORT=$PORT

    # Start docker-compose
    log_info "Starting docker-compose environment..."

    # Create a temporary override file for the port
    cat > docker-compose.override.yml << EOF
services:
  gowe-server:
    ports:
      - "${PORT}:8080"
EOF

    docker-compose up -d --build

    # Wait for server to be healthy
    log_info "Waiting for server health..."
    max_attempts=30
    attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if curl -s "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
            log_info "Server is healthy"
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    if [ $attempt -eq $max_attempts ]; then
        log_error "Server failed to become healthy"
        docker-compose logs gowe-server
        exit 1
    fi

    # Wait for workers to register
    log_info "Waiting for workers to register..."
    sleep 5
fi

# Check worker registration
workers=$(curl -s "${SERVER_URL}/api/v1/workers" | jq -r '.data | length')
log_info "Registered workers: $workers"

if [ "$workers" -eq 0 ]; then
    log_error "No workers registered!"
    docker-compose logs
    exit 1
fi

# Print configuration
log_info "Server: ${SERVER_URL} (default-executor: worker)"
log_info "Workers: $workers registered"

# Create a wrapper script for cwltest
WRAPPER_SCRIPT=$(mktemp)
cat > "$WRAPPER_SCRIPT" << EOF
#!/bin/bash
exec "\$(dirname "\$0")/../bin/gowe" run --server ${SERVER_URL} --quiet "\$@"
EOF
chmod +x "$WRAPPER_SCRIPT"

# Run conformance tests
log_header "Running Conformance Tests"

# Build cwltest command
CWLTEST_CMD="cwltest"

# Check if we have a conformance test YAML
if [ -f "$CONFORMANCE_DIR/conformance_tests.yaml" ]; then
    CWLTEST_CMD="$CWLTEST_CMD --test $CONFORMANCE_DIR/conformance_tests.yaml"
elif [ -f "$CONFORMANCE_DIR/conformance_test_v1.2.yaml" ]; then
    CWLTEST_CMD="$CWLTEST_CMD --test $CONFORMANCE_DIR/conformance_test_v1.2.yaml"
else
    # Run individual test files
    log_warn "No conformance test YAML found, running individual tests..."

    passed=0
    failed=0
    total=0

    for cwl_file in "$CONFORMANCE_DIR"/*.cwl; do
        if [ -f "$cwl_file" ]; then
            base=$(basename "$cwl_file" .cwl)
            job_file="$CONFORMANCE_DIR/${base}-job.yml"

            total=$((total + 1))

            if [ -f "$job_file" ]; then
                log_info "Test [$total]: $base"
                if ./bin/gowe run --server "${SERVER_URL}" --quiet "$cwl_file" "$job_file" > /dev/null 2>&1; then
                    echo -e "  ${GREEN}PASSED${NC}"
                    passed=$((passed + 1))
                else
                    echo -e "  ${RED}FAILED${NC}"
                    failed=$((failed + 1))
                fi
            else
                log_info "Test [$total]: $base (no job file)"
                if ./bin/gowe run --server "${SERVER_URL}" --quiet "$cwl_file" > /dev/null 2>&1; then
                    echo -e "  ${GREEN}PASSED${NC}"
                    passed=$((passed + 1))
                else
                    echo -e "  ${RED}FAILED${NC}"
                    failed=$((failed + 1))
                fi
            fi
        fi
    done

    log_header "Results"
    echo -e "Passed: ${GREEN}$passed${NC}"
    echo -e "Failed: ${RED}$failed${NC}"
    echo -e "Total:  $total"

    # Clean up override file
    rm -f docker-compose.override.yml

    if [ $failed -gt 0 ]; then
        exit 1
    fi
    exit 0
fi

# Add tags if specified
if [ -n "$TAGS" ]; then
    CWLTEST_CMD="$CWLTEST_CMD --tags $TAGS"
fi

CWLTEST_CMD="$CWLTEST_CMD --tool '$SCRIPT_DIR/../bin/gowe run --server ${SERVER_URL} --quiet'"

log_info "Running: $CWLTEST_CMD"
eval "$CWLTEST_CMD" || {
    log_error "Some conformance tests failed"
    rm -f docker-compose.override.yml
    exit 1
}

# Clean up override file
rm -f docker-compose.override.yml

log_header "All Tests Completed"
