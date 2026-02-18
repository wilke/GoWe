#!/bin/bash
#
# run-conformance-distributed.sh - Run CWL conformance tests against distributed setup
#
# This script:
# 1. Starts the docker-compose environment with workers
# 2. Builds the gowe CLI with the run command
# 3. Runs cwltest with gowe run as the tool
# 4. Reports results
# 5. Cleans up
#
# Usage:
#   ./scripts/run-conformance-distributed.sh [tags]
#
# Examples:
#   ./scripts/run-conformance-distributed.sh              # Run all tests
#   ./scripts/run-conformance-distributed.sh required     # Run only required tests
#   ./scripts/run-conformance-distributed.sh command_line_tool  # Run CommandLineTool tests
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Parse arguments
TAGS="${1:-}"

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

cleanup() {
    log_info "Cleaning up..."
    docker-compose down -v 2>/dev/null || true
}

trap cleanup EXIT

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

# Build the CLI
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
    if curl -s http://localhost:8080/api/v1/health > /dev/null 2>&1; then
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
workers=$(curl -s http://localhost:8080/api/v1/workers | jq -r '.data | length')
log_info "Registered workers: $workers"

# Print configuration
log_info "Server: http://localhost:8080 (default-executor: worker)"
log_info "Workers: $workers registered"

# Create a wrapper script for cwltest
WRAPPER_SCRIPT=$(mktemp)
cat > "$WRAPPER_SCRIPT" << 'EOF'
#!/bin/bash
exec "$(dirname "$0")/../bin/gowe" run --server http://localhost:8080 --quiet "$@"
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
                if ./bin/gowe run --server http://localhost:8080 --quiet "$cwl_file" "$job_file" > /dev/null 2>&1; then
                    echo -e "  ${GREEN}PASSED${NC}"
                    passed=$((passed + 1))
                else
                    echo -e "  ${RED}FAILED${NC}"
                    failed=$((failed + 1))
                fi
            else
                log_info "Test [$total]: $base (no job file)"
                if ./bin/gowe run --server http://localhost:8080 --quiet "$cwl_file" > /dev/null 2>&1; then
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

    if [ $failed -gt 0 ]; then
        exit 1
    fi
    exit 0
fi

# Add tags if specified
if [ -n "$TAGS" ]; then
    CWLTEST_CMD="$CWLTEST_CMD --tags $TAGS"
fi

CWLTEST_CMD="$CWLTEST_CMD --tool '$SCRIPT_DIR/../bin/gowe run --server http://localhost:8080 --quiet'"

log_info "Running: $CWLTEST_CMD"
eval "$CWLTEST_CMD" || {
    log_error "Some conformance tests failed"
    exit 1
}

log_header "All Tests Completed"
