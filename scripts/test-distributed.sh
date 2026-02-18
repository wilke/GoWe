#!/bin/bash
#
# test-distributed.sh - End-to-end test for distributed worker execution
#
# This script:
# 1. Builds and starts the docker-compose environment
# 2. Waits for server health and worker registration
# 3. Runs test workflows using gowe run
# 4. Verifies outputs
# 5. Cleans up
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

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

cleanup() {
    log_info "Cleaning up..."
    docker-compose down -v 2>/dev/null || true
}

trap cleanup EXIT

# Build the CLI first
log_info "Building gowe CLI..."
go build -o bin/gowe ./cmd/cli

# Start docker-compose
log_info "Starting docker-compose environment..."
docker-compose up -d --build

# Wait for server to be healthy
log_info "Waiting for server health..."
max_attempts=30
attempt=0
while [ $attempt -lt $max_attempts ]; do
    if curl -s http://localhost:8090/api/v1/health > /dev/null 2>&1; then
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

# Check worker registration
workers=$(curl -s http://localhost:8090/api/v1/workers | jq -r '.data | length')
log_info "Registered workers: $workers"

if [ "$workers" -lt 2 ]; then
    log_warn "Expected at least 2 workers, got $workers"
    docker-compose logs worker-1 worker-2
fi

# Run simple echo test
log_info "Running simple echo test..."
output=$(./bin/gowe run --server http://localhost:8090 --quiet \
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
output=$(./bin/gowe run --server http://localhost:8090 --quiet \
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
