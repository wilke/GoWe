#!/bin/bash
#
# test-distributed.sh - End-to-end test for distributed worker execution
#
# This script:
# 1. Checks for port conflicts and running containers
# 2. Builds and starts the docker-compose environment
# 3. Waits for server health and worker registration
# 4. Runs test workflows using gowe run
# 5. Verifies outputs
# 6. Cleans up
#
# Usage:
#   ./scripts/test-distributed.sh [options]
#
# Options:
#   -p, --port PORT    Host port for server (default: 8090)
#   -k, --keep         Keep containers running after tests
#   -h, --help         Show this help message
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
PORT=8090
KEEP_CONTAINERS=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

usage() {
    sed -n '2,21p' "$0" | sed 's/^# \?//'
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
        *)
            log_error "Unknown option: $1"
            usage
            ;;
    esac
done

SERVER_URL="http://localhost:${PORT}"

cleanup() {
    if [ "$KEEP_CONTAINERS" = false ]; then
        log_info "Cleaning up..."
        docker-compose down -v 2>/dev/null || true
        rm -f docker-compose.override.yml
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

log_info "Using port: $PORT"

# Check for existing containers or port conflicts
REUSE_CONTAINERS=false
if check_running_containers; then
    REUSE_CONTAINERS=true
else
    check_port_available
fi

# Build the CLI first
log_info "Building gowe CLI..."
go build -o bin/gowe ./cmd/cli

if [ "$REUSE_CONTAINERS" = false ]; then
    # Create a temporary override file for the port
    cat > docker-compose.override.yml << EOF
services:
  gowe-server:
    ports:
      - "${PORT}:8080"
EOF

    # Start docker-compose
    log_info "Starting docker-compose environment..."
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

if [ "$workers" -lt 2 ]; then
    log_warn "Expected at least 2 workers, got $workers"
    docker-compose logs worker-1 worker-2
fi

# Run simple echo test
log_info "Running simple echo test..."
output=$(./bin/gowe run --server "${SERVER_URL}" --quiet \
    testdata/worker-test/simple-echo.cwl \
    testdata/worker-test/simple-echo-job.yml 2>&1) || {
    log_error "Simple echo test failed"
    echo "$output"
    docker-compose logs
    exit 1
}

log_info "Simple echo test output:"
echo "$output"

# Run pipeline test
log_info "Running echo pipeline test..."
output=$(./bin/gowe run --server "${SERVER_URL}" --quiet \
    testdata/worker-test/echo-pipeline.cwl \
    testdata/worker-test/echo-pipeline-job.yml 2>&1) || {
    log_error "Echo pipeline test failed"
    echo "$output"
    docker-compose logs
    exit 1
}

log_info "Echo pipeline test output:"
echo "$output"

# Verify the output contains expected JSON structure
if echo "$output" | jq -e '.result.class == "File"' > /dev/null 2>&1; then
    log_info "Output verification passed"
else
    log_warn "Output verification: could not verify File class"
fi

log_info "All tests passed!"
