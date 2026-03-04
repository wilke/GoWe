#!/usr/bin/env bash
#
# run-all-tests.sh - Comprehensive test runner for all GoWe execution modes
#
# NOTE: Requires bash 4+ for associative arrays. On macOS, install via:
#   brew install bash
#
# This script runs all test types across all execution modes, with cwl-runner
# as the gold standard baseline. All modes should eventually pass 378/378
# CWL v1.2 conformance tests.
#
# Usage:
#   ./scripts/run-all-tests.sh [options] [test-type]
#
# Test Types:
#   all         Run all tests (default)
#   conformance CWL conformance tests only
#   unit        Go unit tests only
#   staging     Staging backend tests only
#
# Options:
#   -m, --mode MODE     Run only specified execution mode
#   -s, --skip MODE     Skip specified mode (can be used multiple times)
#   -t, --tier N        Run only tier N tests (1, 2, or 3)
#   -q, --quick         Quick mode: required tests only (84 tests)
#   --full              Full mode: all conformance tests (378 tests)
#   --no-docker         Skip tests requiring Docker
#   --parallel          Use --parallel flag for cwl-runner (tests parallel execution)
#   -v, --verbose       Verbose output
#   -r, --report        Generate markdown report
#   -h, --help          Show this help message
#
# Execution Modes:
#   cwl-runner          Direct CLI execution (gold standard)
#   cwl-runner-parallel Direct CLI with --parallel flag
#   server-local        Server + LocalExecutor
#   distributed-bare    Server + Workers (--runtime=bare)
#   distributed-docker  Server + Workers (--runtime=docker, Docker-in-Docker)
#
# Tier System:
#   Tier 1 (Core):    cwl-runner, cwl-runner-parallel, Go unit tests
#   Tier 2 (Server):  server-local, distributed-* modes
#   Tier 3 (Staging): file://, SharedFS, S3, Shock staging tests
#
# Examples:
#   ./scripts/run-all-tests.sh                    # Full test suite, required tests
#   ./scripts/run-all-tests.sh --full             # Full test suite, all 378 tests
#   ./scripts/run-all-tests.sh -q                 # Quick: required tests only
#   ./scripts/run-all-tests.sh -t 1               # Tier 1 tests only (CI fast path)
#   ./scripts/run-all-tests.sh -m cwl-runner      # Only cwl-runner mode
#   ./scripts/run-all-tests.sh --no-docker        # Skip Docker-dependent tests
#   ./scripts/run-all-tests.sh unit               # Go unit tests only
#   ./scripts/run-all-tests.sh staging            # Staging tests only
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

# Test configuration
TEST_TYPE="all"
TAGS="required"
TIER=""
RUN_MODES=()
SKIP_MODES=()
NO_DOCKER=false
VERBOSE=false
GENERATE_REPORT=false
USE_PARALLEL=false

# Ports for server modes
SERVER_LOCAL_PORT=8091
DISTRIBUTED_PORT=8090

# Results tracking - use compatibility layer for bash < 4
if [ "${BASH_VERSINFO[0]}" -ge 4 ]; then
    declare -A MODE_RESULTS
    declare -A MODE_DETAILS
    declare -A MODE_DURATION
else
    # Bash 3.x compatibility: use indexed arrays
    MODE_RESULTS_KEYS=()
    MODE_RESULTS_VALUES=()
    MODE_DETAILS_VALUES=()
    MODE_DURATION_VALUES=()

    # Helper to set a result
    _set_mode_result() {
        local key="$1" result="$2" details="$3" duration="$4"
        local i
        for i in "${!MODE_RESULTS_KEYS[@]}"; do
            if [ "${MODE_RESULTS_KEYS[$i]}" = "$key" ]; then
                MODE_RESULTS_VALUES[$i]="$result"
                MODE_DETAILS_VALUES[$i]="$details"
                MODE_DURATION_VALUES[$i]="$duration"
                return
            fi
        done
        MODE_RESULTS_KEYS+=("$key")
        MODE_RESULTS_VALUES+=("$result")
        MODE_DETAILS_VALUES+=("$details")
        MODE_DURATION_VALUES+=("$duration")
    }

    # Helper to get a result
    _get_mode_result() {
        local key="$1"
        local i
        for i in "${!MODE_RESULTS_KEYS[@]}"; do
            if [ "${MODE_RESULTS_KEYS[$i]}" = "$key" ]; then
                echo "${MODE_RESULTS_VALUES[$i]}"
                return
            fi
        done
        echo ""
    }

    # Helper to get details
    _get_mode_details() {
        local key="$1"
        local i
        for i in "${!MODE_RESULTS_KEYS[@]}"; do
            if [ "${MODE_RESULTS_KEYS[$i]}" = "$key" ]; then
                echo "${MODE_DETAILS_VALUES[$i]}"
                return
            fi
        done
        echo ""
    }

    # Helper to get duration
    _get_mode_duration() {
        local key="$1"
        local i
        for i in "${!MODE_RESULTS_KEYS[@]}"; do
            if [ "${MODE_RESULTS_KEYS[$i]}" = "$key" ]; then
                echo "${MODE_DURATION_VALUES[$i]}"
                return
            fi
        done
        echo ""
    }
fi

TIER1_FAILED=0
TIER2_FAILED=0
TIER3_FAILED=0

# Wrapper functions to abstract array access
set_mode_result() {
    local mode="$1" result="$2" details="$3" duration="$4"
    if [ "${BASH_VERSINFO[0]}" -ge 4 ]; then
        MODE_RESULTS["$mode"]="$result"
        MODE_DETAILS["$mode"]="$details"
        MODE_DURATION["$mode"]="$duration"
    else
        _set_mode_result "$mode" "$result" "$details" "$duration"
    fi
}

get_mode_result() {
    local mode="$1"
    if [ "${BASH_VERSINFO[0]}" -ge 4 ]; then
        echo "${MODE_RESULTS[$mode]:-}"
    else
        _get_mode_result "$mode"
    fi
}

get_mode_details() {
    local mode="$1"
    if [ "${BASH_VERSINFO[0]}" -ge 4 ]; then
        echo "${MODE_DETAILS[$mode]:-}"
    else
        _get_mode_details "$mode"
    fi
}

get_mode_duration() {
    local mode="$1"
    if [ "${BASH_VERSINFO[0]}" -ge 4 ]; then
        echo "${MODE_DURATION[$mode]:-}"
    else
        _get_mode_duration "$mode"
    fi
}

# =============================================================================
# Help
# =============================================================================

usage() {
    sed -n '2,50p' "$0" | sed 's/^# \?//'
    exit 0
}

# =============================================================================
# Argument Parsing
# =============================================================================

while [[ $# -gt 0 ]]; do
    case $1 in
        -m|--mode)
            RUN_MODES+=("$2")
            shift 2
            ;;
        -s|--skip)
            SKIP_MODES+=("$2")
            shift 2
            ;;
        -t|--tier)
            TIER="$2"
            shift 2
            ;;
        -q|--quick)
            TAGS="required"
            shift
            ;;
        --full)
            TAGS=""  # Empty means all tests
            shift
            ;;
        --no-docker)
            NO_DOCKER=true
            shift
            ;;
        --parallel)
            USE_PARALLEL=true
            shift
            ;;
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        -r|--report)
            GENERATE_REPORT=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        -*)
            log_error "Unknown option: $1"
            usage
            ;;
        all|conformance|unit|staging)
            TEST_TYPE="$1"
            shift
            ;;
        *)
            # Treat as tags
            TAGS="$1"
            shift
            ;;
    esac
done

# =============================================================================
# Mode Selection
# =============================================================================

# Define all available modes by tier
TIER1_MODES=("unit" "cwl-runner" "cwl-runner-parallel")
TIER2_MODES=("server-local" "distributed-bare" "distributed-docker")
TIER3_MODES=("staging-file" "staging-shared" "staging-s3" "staging-shock")

# Build list of modes to run based on tier and options
get_modes_to_run() {
    local modes=()

    # If specific modes requested, use those
    if [ ${#RUN_MODES[@]} -gt 0 ]; then
        modes=("${RUN_MODES[@]}")
    else
        # Otherwise, select by tier
        case "$TIER" in
            1)
                modes=("${TIER1_MODES[@]}")
                ;;
            2)
                modes=("${TIER2_MODES[@]}")
                ;;
            3)
                modes=("${TIER3_MODES[@]}")
                ;;
            "")
                # All tiers based on test type
                case "$TEST_TYPE" in
                    unit)
                        modes=("unit")
                        ;;
                    conformance)
                        modes=("${TIER1_MODES[@]}" "${TIER2_MODES[@]}")
                        ;;
                    staging)
                        modes=("${TIER3_MODES[@]}")
                        ;;
                    all)
                        modes=("${TIER1_MODES[@]}" "${TIER2_MODES[@]}" "${TIER3_MODES[@]}")
                        ;;
                esac
                ;;
        esac
    fi

    # Remove skipped modes
    local filtered=()
    for mode in "${modes[@]}"; do
        local skip=false
        for skip_mode in "${SKIP_MODES[@]}"; do
            if [ "$mode" = "$skip_mode" ]; then
                skip=true
                break
            fi
        done

        # Skip Docker-dependent modes if --no-docker
        if [ "$NO_DOCKER" = true ]; then
            case "$mode" in
                distributed-docker|staging-s3|staging-shock)
                    skip=true
                    ;;
            esac
        fi

        if [ "$skip" = false ]; then
            filtered+=("$mode")
        fi
    done

    echo "${filtered[@]}"
}

# =============================================================================
# Test Runner Functions
# =============================================================================

# Run Go unit tests
run_unit_tests() {
    log_subheader "Go Unit Tests"

    local start_time
    start_time=$(get_time)

    local test_args=""
    if [ "$VERBOSE" = true ]; then
        test_args="-v"
    fi

    local output_file="$PROJECT_DIR/test-unit-results.txt"

    if go test $test_args ./... 2>&1 | tee "$output_file"; then
        local end_time
        end_time=$(get_time)
        local duration
        duration=$(calc_duration "$start_time" "$end_time")

        set_mode_result "unit" "pass" "All unit tests passed" "$duration"
        return 0
    else
        local end_time
        end_time=$(get_time)
        local duration
        duration=$(calc_duration "$start_time" "$end_time")

        set_mode_result "unit" "fail" "Some unit tests failed" "$duration"
        TIER1_FAILED=1
        return 1
    fi
}

# Run cwl-runner conformance tests
run_cwl_runner() {
    local parallel_flag="$1"
    local mode_name="cwl-runner"
    local extra_args=""

    if [ "$parallel_flag" = "true" ]; then
        mode_name="cwl-runner-parallel"
        extra_args="--parallel"
    fi

    log_subheader "cwl-runner $([ -n "$extra_args" ] && echo "$extra_args")"

    local start_time
    start_time=$(get_time)

    local runner="$PROJECT_DIR/bin/cwl-runner"
    local conformance_dir="$PROJECT_DIR/testdata/cwl-v1.2"
    local output_file="$PROJECT_DIR/conformance-${mode_name}-results.txt"

    # Build if needed
    if [ ! -f "$runner" ]; then
        build_binary "$runner" "./cmd/cwl-runner" || return 1
    fi

    # Ensure conformance tests exist
    ensure_conformance_tests "$PROJECT_DIR" || return 1

    # Build cwltest command
    local cwltest_cmd="cwltest --test conformance_tests.yaml --tool $runner"

    if [ -n "$extra_args" ]; then
        cwltest_cmd="$cwltest_cmd --tool-arg=\"$extra_args\""
    fi

    if [ -n "$TAGS" ]; then
        cwltest_cmd="$cwltest_cmd --tags $TAGS"
    fi

    cwltest_cmd="$cwltest_cmd --verbose"

    # Run tests
    cd "$conformance_dir"

    local result=0
    if eval "$cwltest_cmd" 2>&1 | tee "$output_file"; then
        result=0
    else
        result=1
    fi

    cd "$PROJECT_DIR"

    local end_time
    end_time=$(get_time)
    local duration
    duration=$(calc_duration "$start_time" "$end_time")

    # Parse results
    local summary=""
    if [ -f "$output_file" ]; then
        # Look for "X tests passed" pattern
        local passed
        passed=$(grep -oE "[0-9]+ tests? passed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
        local failed
        failed=$(grep -oE "[0-9]+ tests? failed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
        local total=$((passed + failed))
        if [ $total -gt 0 ]; then
            summary="$passed/$total passed"
        fi
    fi

    if [ $result -eq 0 ]; then
        set_mode_result "$mode_name" "pass" "${summary:-All tests passed}" "$duration"
    else
        set_mode_result "$mode_name" "fail" "${summary:-Some tests failed}" "$duration"
        TIER1_FAILED=1
    fi

    return $result
}

# Run server-local conformance tests
run_server_local() {
    log_subheader "Server-Local Mode"

    local start_time
    start_time=$(get_time)

    local output_file="$PROJECT_DIR/conformance-server-local-results.txt"

    # Build server and CLI
    build_binary "$PROJECT_DIR/bin/server" "./cmd/server" || return 1
    build_binary "$PROJECT_DIR/bin/gowe" "./cmd/cli" || return 1

    # Check port
    if ! check_port_available $SERVER_LOCAL_PORT; then
        log_error "Port $SERVER_LOCAL_PORT is in use"
        set_mode_result "server-local" "fail" "Port $SERVER_LOCAL_PORT in use" ""
        return 1
    fi

    # Start server
    local server_pid=""
    cleanup_server() {
        if [ -n "$server_pid" ]; then
            kill_and_wait "$server_pid"
        fi
    }

    log_info "Starting server on port $SERVER_LOCAL_PORT..."
    ./bin/server \
        -addr ":${SERVER_LOCAL_PORT}" \
        -default-executor local \
        -allow-anonymous \
        -anonymous-executors "local,docker,worker,container" \
        -scheduler-poll 100ms \
        -log-level warn \
        &
    server_pid=$!

    # Wait for health
    if ! wait_for_url "http://localhost:${SERVER_LOCAL_PORT}/api/v1/health" 30 1; then
        log_error "Server failed to become healthy"
        cleanup_server
        set_mode_result "server-local" "fail" "Server failed to start" ""
        return 1
    fi

    log_info "Server is healthy (PID $server_pid)"

    # Create wrapper script
    local wrapper
    wrapper=$(create_cwltest_wrapper "$PROJECT_DIR/bin/gowe" "http://localhost:${SERVER_LOCAL_PORT}")

    # Ensure conformance tests
    ensure_conformance_tests "$PROJECT_DIR" || { cleanup_server; return 1; }

    # Run tests
    cd "$PROJECT_DIR/testdata/cwl-v1.2"

    local cwltest_cmd="cwltest --test conformance_tests.yaml --tool $wrapper"
    if [ -n "$TAGS" ]; then
        cwltest_cmd="$cwltest_cmd --tags $TAGS"
    fi
    cwltest_cmd="$cwltest_cmd --verbose"

    local result=0
    if eval "$cwltest_cmd" 2>&1 | tee "$output_file"; then
        result=0
    else
        result=1
    fi

    cd "$PROJECT_DIR"

    # Cleanup
    rm -f "$wrapper"
    cleanup_server

    local end_time
    end_time=$(get_time)
    local duration
    duration=$(calc_duration "$start_time" "$end_time")

    # Parse results
    local passed
    passed=$(grep -oE "[0-9]+ tests? passed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
    local failed
    failed=$(grep -oE "[0-9]+ tests? failed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
    local total=$((passed + failed))
    local summary="$passed/$total passed"

    if [ $result -eq 0 ]; then
        set_mode_result "server-local" "pass" "$summary" "$duration"
    else
        set_mode_result "server-local" "fail" "$summary" "$duration"
        TIER2_FAILED=1
    fi

    return $result
}

# Run distributed tests (docker-compose)
run_distributed() {
    local runtime="${1:-bare}"
    local mode_name="distributed-$runtime"

    log_subheader "Distributed Mode ($runtime)"

    if ! check_docker; then
        log_warn "Docker not available, skipping $mode_name"
        set_mode_result "$mode_name" "skip" "Docker not available" ""
        return 0
    fi

    local start_time
    start_time=$(get_time)

    local output_file="$PROJECT_DIR/conformance-${mode_name}-results.txt"

    # Build CLI
    build_binary "$PROJECT_DIR/bin/gowe" "./cmd/cli" || return 1

    # Check port
    if ! check_port_available $DISTRIBUTED_PORT; then
        # Check if it's our docker-compose
        if curl -s "http://localhost:${DISTRIBUTED_PORT}/api/v1/health" 2>/dev/null | grep -q "healthy"; then
            log_info "Using existing docker-compose environment"
        else
            log_error "Port $DISTRIBUTED_PORT is in use by another process"
            set_mode_result "$mode_name" "fail" "Port $DISTRIBUTED_PORT in use" ""
            return 1
        fi
    else
        # Start docker-compose
        log_info "Starting docker-compose environment..."

        # Create override for port
        cat > docker-compose.override.yml << EOF
services:
  gowe-server:
    ports:
      - "${DISTRIBUTED_PORT}:8080"
  worker-1:
    command:
      - "-server"
      - "http://gowe-server:8080"
      - "-runtime"
      - "$runtime"
      - "-name"
      - "worker-1"
      - "-workdir"
      - "/workdir/scratch"
      - "-stage-out"
      - "file:///workdir/outputs"
      - "-poll"
      - "500ms"
      - "-debug"
  worker-2:
    command:
      - "-server"
      - "http://gowe-server:8080"
      - "-runtime"
      - "$runtime"
      - "-name"
      - "worker-2"
      - "-workdir"
      - "/workdir/scratch"
      - "-stage-out"
      - "file:///workdir/outputs"
      - "-poll"
      - "500ms"
      - "-debug"
EOF

        docker-compose up -d --build

        # Wait for health
        if ! wait_for_url "http://localhost:${DISTRIBUTED_PORT}/api/v1/health" 60 2; then
            log_error "Server failed to become healthy"
            docker-compose logs gowe-server
            docker-compose down -v 2>/dev/null || true
            rm -f docker-compose.override.yml
            set_mode_result "$mode_name" "fail" "Server failed to start" ""
            return 1
        fi

        # Wait for workers
        log_info "Waiting for workers to register..."
        sleep 5
    fi

    # Check workers
    local workers
    workers=$(curl -s "http://localhost:${DISTRIBUTED_PORT}/api/v1/workers" | grep -o '"id"' | wc -l | tr -d ' ')
    log_info "Registered workers: $workers"

    if [ "$workers" -eq 0 ]; then
        log_warn "No workers registered, continuing anyway..."
    fi

    # Set up path mapping
    local testdata_abs
    testdata_abs=$(cd "$PROJECT_DIR/testdata" && pwd)
    export GOWE_PATH_MAP="${testdata_abs}=/testdata"
    export GOWE_SERVER="http://localhost:${DISTRIBUTED_PORT}"

    # Create wrapper
    local wrapper
    wrapper=$(mktemp)
    cat > "$wrapper" << EOF
#!/bin/bash
export GOWE_PATH_MAP="${testdata_abs}=/testdata"
export GOWE_SERVER="http://localhost:${DISTRIBUTED_PORT}"
exec "$PROJECT_DIR/bin/gowe" run --quiet "\$@"
EOF
    chmod +x "$wrapper"

    # Ensure conformance tests
    ensure_conformance_tests "$PROJECT_DIR" || return 1

    # Run tests
    cd "$PROJECT_DIR/testdata/cwl-v1.2"

    local cwltest_cmd="cwltest --test conformance_tests.yaml --tool $wrapper"
    if [ -n "$TAGS" ]; then
        cwltest_cmd="$cwltest_cmd --tags $TAGS"
    fi
    cwltest_cmd="$cwltest_cmd --verbose"

    local result=0
    if eval "$cwltest_cmd" 2>&1 | tee "$output_file"; then
        result=0
    else
        result=1
    fi

    cd "$PROJECT_DIR"

    # Cleanup
    rm -f "$wrapper"
    rm -f docker-compose.override.yml
    docker-compose down -v 2>/dev/null || true

    local end_time
    end_time=$(get_time)
    local duration
    duration=$(calc_duration "$start_time" "$end_time")

    # Parse results
    local passed
    passed=$(grep -oE "[0-9]+ tests? passed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
    local failed
    failed=$(grep -oE "[0-9]+ tests? failed" "$output_file" | head -1 | grep -oE "[0-9]+" || echo "0")
    local total=$((passed + failed))
    local summary="$passed/$total passed"

    if [ $result -eq 0 ]; then
        set_mode_result "$mode_name" "pass" "$summary" "$duration"
    else
        set_mode_result "$mode_name" "fail" "$summary" "$duration"
        TIER2_FAILED=1
    fi

    return $result
}

# Run staging tests
run_staging_tests() {
    local backend="$1"
    local mode_name="staging-$backend"

    log_subheader "Staging: $backend"

    local start_time
    start_time=$(get_time)

    local result=0
    case "$backend" in
        file|shared)
            # These don't need Docker
            if go test -v -run "$(echo "$backend" | sed 's/./\u&/')" ./pkg/staging/...; then
                result=0
            else
                result=1
            fi
            ;;
        s3)
            if ! check_docker; then
                log_warn "Docker not available, skipping S3 tests"
                set_mode_result "$mode_name" "skip" "Docker not available" ""
                return 0
            fi

            # Start MinIO
            docker-compose -f docker-compose.test.yml up -d minio minio-setup
            if ! wait_for_url "http://localhost:9000/minio/health/live" 30 2; then
                docker-compose -f docker-compose.test.yml down -v 2>/dev/null || true
                set_mode_result "$mode_name" "fail" "MinIO failed to start" ""
                return 1
            fi
            sleep 3

            export S3_ENDPOINT="localhost:9000"
            export S3_ACCESS_KEY="minioadmin"
            export S3_SECRET_KEY="minioadmin"

            if go test -v -tags=integration -run S3 ./pkg/staging/...; then
                result=0
            else
                result=1
            fi

            docker-compose -f docker-compose.test.yml down -v 2>/dev/null || true
            ;;
        shock)
            if ! check_docker; then
                log_warn "Docker not available, skipping Shock tests"
                set_mode_result "$mode_name" "skip" "Docker not available" ""
                return 0
            fi

            # Start Shock mock
            docker-compose -f docker-compose.test.yml up -d shock-mock
            if ! wait_for_url "http://localhost:7445/" 30 2; then
                docker-compose -f docker-compose.test.yml down -v 2>/dev/null || true
                set_mode_result "$mode_name" "fail" "Shock failed to start" ""
                return 1
            fi

            export SHOCK_HOST="localhost:7445"

            if go test -v -tags=integration -run Shock ./pkg/staging/...; then
                result=0
            else
                result=1
            fi

            docker-compose -f docker-compose.test.yml down -v 2>/dev/null || true
            ;;
    esac

    local end_time
    end_time=$(get_time)
    local duration
    duration=$(calc_duration "$start_time" "$end_time")

    if [ $result -eq 0 ]; then
        set_mode_result "$mode_name" "pass" "All tests passed" "$duration"
    else
        set_mode_result "$mode_name" "fail" "Some tests failed" "$duration"
        TIER3_FAILED=1
    fi

    return $result
}

# =============================================================================
# Main
# =============================================================================

log_header "GoWe Comprehensive Test Suite"

# Get modes to run
MODES_TO_RUN=($(get_modes_to_run))

if [ ${#MODES_TO_RUN[@]} -eq 0 ]; then
    log_error "No modes to run (all skipped or filtered out)"
    exit 1
fi

# Display configuration
log_info "Test type: $TEST_TYPE"
log_info "Tags: ${TAGS:-all}"
log_info "Modes: ${MODES_TO_RUN[*]}"
[ -n "$TIER" ] && log_info "Tier: $TIER"
[ "$NO_DOCKER" = true ] && log_info "Docker tests: disabled"

# Check prerequisites
log_subheader "Prerequisites"

if ! check_go_version 1.21; then
    exit 1
fi
log_result "Go version" "pass" "$(go version | sed 's/go version //')"

if check_cwltest; then
    log_result "cwltest" "pass" "installed"
else
    log_info "Installing cwltest..."
    ensure_cwltest || exit 1
    log_result "cwltest" "pass" "installed"
fi

if check_docker; then
    log_result "Docker" "pass" "available"
else
    log_result "Docker" "skip" "not available"
fi

# Build binaries
log_subheader "Building"
build_binary "$PROJECT_DIR/bin/cwl-runner" "./cmd/cwl-runner" || exit 1
build_binary "$PROJECT_DIR/bin/gowe" "./cmd/cli" || exit 1
build_binary "$PROJECT_DIR/bin/server" "./cmd/server" || exit 1
log_result "Binaries" "pass" "built successfully"

# Track overall timing
SUITE_START=$(get_time)

# Run tests by tier
log_header "[TIER 1] Core Execution Tests"

for mode in "${MODES_TO_RUN[@]}"; do
    case "$mode" in
        unit)
            run_unit_tests || true
            ;;
        cwl-runner)
            run_cwl_runner false || true
            ;;
        cwl-runner-parallel)
            run_cwl_runner true || true
            ;;
    esac
done

log_header "[TIER 2] Server Mode Tests"

for mode in "${MODES_TO_RUN[@]}"; do
    case "$mode" in
        server-local)
            run_server_local || true
            ;;
        distributed-bare)
            run_distributed "bare" || true
            ;;
        distributed-docker)
            run_distributed "docker" || true
            ;;
    esac
done

log_header "[TIER 3] Staging Backend Tests"

for mode in "${MODES_TO_RUN[@]}"; do
    case "$mode" in
        staging-file)
            run_staging_tests "file" || true
            ;;
        staging-shared)
            run_staging_tests "shared" || true
            ;;
        staging-s3)
            run_staging_tests "s3" || true
            ;;
        staging-shock)
            run_staging_tests "shock" || true
            ;;
    esac
done

SUITE_END=$(get_time)
SUITE_DURATION=$(calc_duration "$SUITE_START" "$SUITE_END")

# =============================================================================
# Summary
# =============================================================================

log_header "Test Summary"

# Tier 1 summary
echo -e "${CYAN}[TIER 1] Core Execution${NC}"
for mode in unit cwl-runner cwl-runner-parallel; do
    result=$(get_mode_result "$mode")
    if [ -n "$result" ]; then
        dur=$(get_mode_duration "$mode")
        details=$(get_mode_details "$mode")
        log_result "$mode" "$result" "${details}${dur:+ (${dur}s)}"
    fi
done

# Tier 2 summary
echo ""
echo -e "${CYAN}[TIER 2] Server Modes${NC}"
for mode in server-local distributed-bare distributed-docker; do
    result=$(get_mode_result "$mode")
    if [ -n "$result" ]; then
        dur=$(get_mode_duration "$mode")
        details=$(get_mode_details "$mode")
        log_result "$mode" "$result" "${details}${dur:+ (${dur}s)}"
    fi
done

# Tier 3 summary
echo ""
echo -e "${CYAN}[TIER 3] Staging Backends${NC}"
for mode in staging-file staging-shared staging-s3 staging-shock; do
    result=$(get_mode_result "$mode")
    if [ -n "$result" ]; then
        dur=$(get_mode_duration "$mode")
        details=$(get_mode_details "$mode")
        log_result "$mode" "$result" "${details}${dur:+ (${dur}s)}"
    fi
done

echo ""
echo -e "Total duration: $(format_duration "$SUITE_DURATION")"

# Overall status
echo ""
echo -e "${BOLD}=== Overall Status ===${NC}"

if [ $TIER1_FAILED -eq 0 ]; then
    echo -e "Tier 1: ${GREEN}PASSED${NC} (gold standard verified)"
else
    echo -e "Tier 1: ${RED}FAILED${NC} (core tests must pass)"
fi

if [ $TIER2_FAILED -eq 0 ]; then
    echo -e "Tier 2: ${GREEN}PASSED${NC}"
else
    echo -e "Tier 2: ${YELLOW}PARTIAL${NC} (known issues)"
fi

if [ $TIER3_FAILED -eq 0 ]; then
    echo -e "Tier 3: ${GREEN}PASSED${NC}"
else
    echo -e "Tier 3: ${YELLOW}PARTIAL${NC} (some skipped/failed)"
fi

# Generate report if requested
if [ "$GENERATE_REPORT" = true ]; then
    REPORT_DATE=$(date +%y%m%d)
    REPORT_FILE="$PROJECT_DIR/reports/test-results-${REPORT_DATE}.md"
    mkdir -p "$PROJECT_DIR/reports"

    {
        echo "# GoWe Test Results - $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        echo "## Configuration"
        echo ""
        echo "- **Test Type:** $TEST_TYPE"
        echo "- **Tags:** ${TAGS:-all}"
        echo "- **Duration:** $(format_duration "$SUITE_DURATION")"
        echo ""
        echo "## Results by Tier"
        echo ""
        echo "### Tier 1: Core Execution"
        echo ""
        echo "| Mode | Status | Details |"
        echo "|------|--------|---------|"
        for mode in unit cwl-runner cwl-runner-parallel; do
            result=$(get_mode_result "$mode")
            if [ -n "$result" ]; then
                symbol="?"
                case "$result" in
                    pass) symbol="$CHECKMARK" ;;
                    fail) symbol="$CROSSMARK" ;;
                    skip) symbol="$SKIP" ;;
                esac
                details=$(get_mode_details "$mode")
                echo "| $mode | $symbol | $details |"
            fi
        done
        echo ""
        echo "### Tier 2: Server Modes"
        echo ""
        echo "| Mode | Status | Details |"
        echo "|------|--------|---------|"
        for mode in server-local distributed-bare distributed-docker; do
            result=$(get_mode_result "$mode")
            if [ -n "$result" ]; then
                symbol="?"
                case "$result" in
                    pass) symbol="$CHECKMARK" ;;
                    fail) symbol="$CROSSMARK" ;;
                    skip) symbol="$SKIP" ;;
                esac
                details=$(get_mode_details "$mode")
                echo "| $mode | $symbol | $details |"
            fi
        done
        echo ""
        echo "### Tier 3: Staging Backends"
        echo ""
        echo "| Mode | Status | Details |"
        echo "|------|--------|---------|"
        for mode in staging-file staging-shared staging-s3 staging-shock; do
            result=$(get_mode_result "$mode")
            if [ -n "$result" ]; then
                symbol="?"
                case "$result" in
                    pass) symbol="$CHECKMARK" ;;
                    fail) symbol="$CROSSMARK" ;;
                    skip) symbol="$SKIP" ;;
                esac
                details=$(get_mode_details "$mode")
                echo "| $mode | $symbol | $details |"
            fi
        done
        echo ""
        echo "---"
        echo "*Generated by GoWe test suite*"
    } > "$REPORT_FILE"

    log_info "Report saved to: $REPORT_FILE"
fi

# Exit code
if [ $TIER1_FAILED -ne 0 ]; then
    echo ""
    echo -e "${RED}${BOLD}Tier 1 tests failed - core functionality broken${NC}"
    exit 1
fi

if [ $TIER2_FAILED -ne 0 ] || [ $TIER3_FAILED -ne 0 ]; then
    echo ""
    echo -e "${YELLOW}${BOLD}Some tests failed but core functionality works${NC}"
    exit 0  # Exit 0 since Tier 1 passed
fi

echo ""
echo -e "${GREEN}${BOLD}All tests passed!${NC}"
exit 0
