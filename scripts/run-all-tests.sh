#!/bin/bash
#
# run-all-tests.sh - Run conformance tests for all execution modes
#
# This script runs the CWL conformance test suite against all three
# supported execution modes:
# 1. cwl-runner (standalone CLI)
# 2. server with local execution (server + LocalExecutor)
# 3. distributed workers (docker-compose with workers)
#
# Usage:
#   ./scripts/run-all-tests.sh [options] [tags]
#
# Options:
#   -m, --mode MODE    Run only specified mode: cwl-runner, server-local, distributed
#   -s, --skip MODE    Skip specified mode (can be used multiple times)
#   -h, --help         Show this help message
#
# Examples:
#   ./scripts/run-all-tests.sh                    # Run required tests for all modes
#   ./scripts/run-all-tests.sh required           # Same as above (explicit)
#   ./scripts/run-all-tests.sh initial_work_dir   # Run IWDR tests for all modes
#   ./scripts/run-all-tests.sh -m cwl-runner      # Run only cwl-runner mode
#   ./scripts/run-all-tests.sh -s distributed     # Skip distributed mode
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Default values
TAGS=""
RUN_MODES=()
SKIP_MODES=()

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_header() {
    echo -e "\n${CYAN}${BOLD}=== $1 ===${NC}\n"
}

log_result() {
    local mode=$1
    local result=$2
    if [ $result -eq 0 ]; then
        echo -e "  ${GREEN}PASSED${NC} - $mode"
    else
        echo -e "  ${RED}FAILED${NC} - $mode"
    fi
}

usage() {
    sed -n '2,27p' "$0" | sed 's/^# \?//'
    exit 0
}

# Parse arguments
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

# Default tags
TAGS="${TAGS:-required}"

# Default to all modes if none specified
if [ ${#RUN_MODES[@]} -eq 0 ]; then
    RUN_MODES=("cwl-runner" "server-local" "distributed")
fi

# Remove skipped modes
for skip in "${SKIP_MODES[@]}"; do
    RUN_MODES=("${RUN_MODES[@]/$skip}")
done

# Filter out empty elements
FILTERED_MODES=()
for mode in "${RUN_MODES[@]}"; do
    if [ -n "$mode" ]; then
        FILTERED_MODES+=("$mode")
    fi
done
RUN_MODES=("${FILTERED_MODES[@]}")

if [ ${#RUN_MODES[@]} -eq 0 ]; then
    log_error "No modes to run (all skipped)"
    exit 1
fi

log_header "CWL v1.2 Conformance Tests - All Modes"
log_info "Tags: $TAGS"
log_info "Modes: ${RUN_MODES[*]}"

# Track results
declare -A RESULTS
FAILED=0

# Mode 1: cwl-runner (standalone CLI)
run_cwl_runner() {
    log_header "Mode 1: cwl-runner (standalone CLI)"

    if "$SCRIPT_DIR/run-conformance.sh" "$TAGS"; then
        RESULTS["cwl-runner"]=0
    else
        RESULTS["cwl-runner"]=1
        FAILED=1
    fi
}

# Mode 2: server with local execution
run_server_local() {
    log_header "Mode 2: Server with Local Execution"

    if "$SCRIPT_DIR/run-conformance-server-local.sh" "$TAGS"; then
        RESULTS["server-local"]=0
    else
        RESULTS["server-local"]=1
        FAILED=1
    fi
}

# Mode 3: distributed workers
run_distributed() {
    log_header "Mode 3: Distributed Workers (docker-compose)"

    if "$SCRIPT_DIR/run-conformance-distributed.sh" "$TAGS"; then
        RESULTS["distributed"]=0
    else
        RESULTS["distributed"]=1
        FAILED=1
    fi
}

# Run selected modes
for mode in "${RUN_MODES[@]}"; do
    case $mode in
        cwl-runner)
            run_cwl_runner
            ;;
        server-local)
            run_server_local
            ;;
        distributed)
            run_distributed
            ;;
        *)
            log_error "Unknown mode: $mode"
            log_error "Valid modes: cwl-runner, server-local, distributed"
            exit 1
            ;;
    esac
done

# Summary
log_header "Test Summary"

for mode in "${RUN_MODES[@]}"; do
    log_result "$mode" "${RESULTS[$mode]:-1}"
done

echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}${BOLD}All modes passed!${NC}"
    exit 0
else
    echo -e "${RED}${BOLD}Some modes failed.${NC}"
    exit 1
fi
