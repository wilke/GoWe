#!/usr/bin/env bash
#
# run-staging-tests.sh - Run staging backend integration tests
#
# This script tests all staging backends (file://, S3, Shock, SharedFS)
# using both unit tests and integration tests with Docker containers.
#
# Usage:
#   ./scripts/run-staging-tests.sh [options] [backend...]
#
# Backends:
#   file      Local filesystem staging (no Docker required)
#   shared    SharedFS staging (symlink mode, no Docker required)
#   s3        S3/MinIO staging (requires Docker)
#   shock     Shock staging (requires Docker)
#   all       Run all backends (default)
#
# Options:
#   -u, --unit-only      Run only unit tests (no Docker)
#   -i, --integration    Run integration tests (requires Docker)
#   -k, --keep           Keep Docker containers running after tests
#   -v, --verbose        Verbose output
#   -h, --help           Show this help message
#
# Examples:
#   ./scripts/run-staging-tests.sh              # Run all tests
#   ./scripts/run-staging-tests.sh file shared  # Test file and shared backends
#   ./scripts/run-staging-tests.sh -u           # Unit tests only (no Docker)
#   ./scripts/run-staging-tests.sh s3 -i        # S3 integration tests
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Source shared utilities
source "$SCRIPT_DIR/test-utils.sh"

cd "$PROJECT_DIR"

# =============================================================================
# Configuration
# =============================================================================

BACKENDS=()
UNIT_ONLY=false
RUN_INTEGRATION=false
KEEP_CONTAINERS=false
VERBOSE=false

# Docker compose file for test services
COMPOSE_FILE="docker-compose.test.yml"

# =============================================================================
# Help
# =============================================================================

usage() {
    sed -n '2,30p' "$0" | sed 's/^# \?//'
    exit 0
}

# =============================================================================
# Argument Parsing
# =============================================================================

while [[ $# -gt 0 ]]; do
    case $1 in
        -u|--unit-only)
            UNIT_ONLY=true
            shift
            ;;
        -i|--integration)
            RUN_INTEGRATION=true
            shift
            ;;
        -k|--keep)
            KEEP_CONTAINERS=true
            shift
            ;;
        -v|--verbose)
            VERBOSE=true
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
            BACKENDS+=("$1")
            shift
            ;;
    esac
done

# Default to all backends
if [ ${#BACKENDS[@]} -eq 0 ]; then
    BACKENDS=("all")
fi

# Expand "all" to individual backends
if [[ " ${BACKENDS[*]} " =~ " all " ]]; then
    BACKENDS=("file" "shared" "s3" "shock")
fi

# If neither unit-only nor integration specified, run both
if [ "$UNIT_ONLY" = false ] && [ "$RUN_INTEGRATION" = false ]; then
    RUN_INTEGRATION=true
fi

# =============================================================================
# Cleanup
# =============================================================================

cleanup() {
    if [ "$KEEP_CONTAINERS" = false ]; then
        if [ -f "$PROJECT_DIR/$COMPOSE_FILE" ]; then
            log_info "Stopping test containers..."
            docker-compose -f "$COMPOSE_FILE" down -v 2>/dev/null || true
        fi
    else
        log_info "Keeping containers running (use 'docker-compose -f $COMPOSE_FILE down -v' to stop)"
    fi
}

trap cleanup EXIT

# =============================================================================
# Test Functions
# =============================================================================

# Run unit tests for staging package
run_unit_tests() {
    log_subheader "Unit Tests: pkg/staging"

    local test_args="-v"
    if [ "$VERBOSE" = false ]; then
        test_args=""
    fi

    if go test $test_args ./pkg/staging/...; then
        record_result "unit-tests" "pass" "All unit tests passed"
        return 0
    else
        record_result "unit-tests" "fail" "Some unit tests failed"
        return 1
    fi
}

# Run file:// stager tests
test_file_stager() {
    log_subheader "Testing: file:// stager"

    local test_args="-v -run File"
    if [ "$VERBOSE" = false ]; then
        test_args="-run File"
    fi

    if go test $test_args ./pkg/staging/...; then
        record_result "file-stager" "pass" "file:// staging works"
        return 0
    else
        record_result "file-stager" "fail" "file:// staging failed"
        return 1
    fi
}

# Run SharedFS stager tests
test_shared_stager() {
    log_subheader "Testing: SharedFS stager"

    local test_args="-v -run Shared"
    if [ "$VERBOSE" = false ]; then
        test_args="-run Shared"
    fi

    if go test $test_args ./pkg/staging/...; then
        record_result "shared-stager" "pass" "SharedFS staging works"
        return 0
    else
        record_result "shared-stager" "fail" "SharedFS staging failed"
        return 1
    fi
}

# Start MinIO for S3 tests
start_minio() {
    log_info "Starting MinIO..."

    docker-compose -f "$COMPOSE_FILE" up -d minio minio-setup

    log_info "Waiting for MinIO to be ready..."
    if wait_for_url "http://localhost:9000/minio/health/live" 30 2; then
        log_info "MinIO is ready"
        # Wait a bit more for bucket setup
        sleep 3
        return 0
    else
        log_error "MinIO failed to start"
        docker-compose -f "$COMPOSE_FILE" logs minio
        return 1
    fi
}

# Run S3/MinIO integration tests
test_s3_stager() {
    log_subheader "Testing: S3 stager (MinIO)"

    if [ "$RUN_INTEGRATION" = false ]; then
        log_info "Skipping S3 integration tests (use -i to enable)"
        record_result "s3-stager" "skip" "Integration tests disabled"
        return 0
    fi

    if ! check_docker; then
        log_warn "Docker not available, skipping S3 tests"
        record_result "s3-stager" "skip" "Docker not available"
        return 0
    fi

    if ! start_minio; then
        record_result "s3-stager" "fail" "MinIO failed to start"
        return 1
    fi

    local test_args="-v -tags=integration -run S3"
    if [ "$VERBOSE" = false ]; then
        test_args="-tags=integration -run S3"
    fi

    export S3_ENDPOINT="localhost:9000"
    export S3_ACCESS_KEY="minioadmin"
    export S3_SECRET_KEY="minioadmin"

    if go test $test_args ./pkg/staging/...; then
        record_result "s3-stager" "pass" "S3 staging works"
        return 0
    else
        record_result "s3-stager" "fail" "S3 staging failed"
        return 1
    fi
}

# Start Shock server
start_shock() {
    log_info "Starting Shock server..."

    docker-compose -f "$COMPOSE_FILE" up -d shock-server

    log_info "Waiting for Shock to be ready..."
    if wait_for_url "http://localhost:7445/" 60 2; then
        log_info "Shock is ready"
        return 0
    else
        log_error "Shock failed to start"
        docker-compose -f "$COMPOSE_FILE" logs shock-server
        return 1
    fi
}

# Run Shock integration tests
test_shock_stager() {
    log_subheader "Testing: Shock stager"

    if [ "$RUN_INTEGRATION" = false ]; then
        log_info "Skipping Shock integration tests (use -i to enable)"
        record_result "shock-stager" "skip" "Integration tests disabled"
        return 0
    fi

    if ! check_docker; then
        log_warn "Docker not available, skipping Shock tests"
        record_result "shock-stager" "skip" "Docker not available"
        return 0
    fi

    if ! start_shock; then
        record_result "shock-stager" "fail" "Shock failed to start"
        return 1
    fi

    local test_args="-v -tags=integration -run Shock"
    if [ "$VERBOSE" = false ]; then
        test_args="-tags=integration -run Shock"
    fi

    export SHOCK_HOST="localhost:7445"

    if go test $test_args ./pkg/staging/...; then
        record_result "shock-stager" "pass" "Shock staging works"
        return 0
    else
        record_result "shock-stager" "fail" "Shock staging failed"
        return 1
    fi
}

# =============================================================================
# Main
# =============================================================================

log_header "GoWe Staging Backend Tests"

log_info "Backends: ${BACKENDS[*]}"
log_info "Unit tests: $([ "$UNIT_ONLY" = true ] && echo "only" || echo "yes")"
log_info "Integration tests: $([ "$RUN_INTEGRATION" = true ] && echo "yes" || echo "no")"

# Check prerequisites
if ! check_go_version 1.21; then
    exit 1
fi

# Track overall success
FAILED=0
START_TIME=$(get_time)

# Run unit tests first
if [ "$UNIT_ONLY" = true ] || [ "$RUN_INTEGRATION" = true ]; then
    run_unit_tests || FAILED=1
fi

# Run backend-specific tests
for backend in "${BACKENDS[@]}"; do
    case $backend in
        file)
            test_file_stager || FAILED=1
            ;;
        shared)
            test_shared_stager || FAILED=1
            ;;
        s3)
            test_s3_stager || FAILED=1
            ;;
        shock)
            test_shock_stager || FAILED=1
            ;;
        *)
            log_warn "Unknown backend: $backend"
            ;;
    esac
done

END_TIME=$(get_time)
DURATION=$(calc_duration "$START_TIME" "$END_TIME")

# =============================================================================
# Summary
# =============================================================================

log_header "Staging Test Summary"

echo ""
for mode in "${!TEST_RESULTS[@]}"; do
    log_result "$mode" "${TEST_RESULTS[$mode]}" "${TEST_DETAILS[$mode]}"
done | sort

echo ""
echo -e "Duration: $(format_duration "$DURATION")"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}${BOLD}All staging tests passed!${NC}"
    exit 0
else
    echo -e "${RED}${BOLD}Some staging tests failed.${NC}"
    exit 1
fi
