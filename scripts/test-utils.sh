#!/usr/bin/env bash
#
# test-utils.sh - Shared test utilities for GoWe test scripts
#
# NOTE: Requires bash 4+ for associative arrays. On macOS, install via:
#   brew install bash
# Then ensure /opt/homebrew/bin/bash or /usr/local/bin/bash is in your PATH.
#
# This file provides common functions for:
# - Color output and logging
# - Prerequisite checking
# - Result tracking
# - Report generation
#
# Usage:
#   source "$(dirname "$0")/test-utils.sh"
#

# Prevent multiple sourcing
if [ -n "$TEST_UTILS_SOURCED" ]; then
    return 0
fi
TEST_UTILS_SOURCED=1

# =============================================================================
# Environment Loading
# =============================================================================

# Determine project root (assumes test-utils.sh is in scripts/)
_TEST_UTILS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_PROJECT_ROOT="$(dirname "$_TEST_UTILS_DIR")"

# Load .env file if it exists and hasn't been loaded
load_env() {
    local env_file="${1:-$_PROJECT_ROOT/.env}"

    if [ -f "$env_file" ]; then
        # Export all variables from .env
        set -a
        # shellcheck source=/dev/null
        source "$env_file"
        set +a
        return 0
    fi
    return 1
}

# Auto-load .env if GOWE_ENV_LOADED is not set
if [ -z "$GOWE_ENV_LOADED" ] && [ -f "$_PROJECT_ROOT/.env" ]; then
    load_env "$_PROJECT_ROOT/.env"
    export GOWE_ENV_LOADED=1
fi

# Set default paths from environment or auto-detect
GOWE_PROJECT_ROOT="${GOWE_PROJECT_ROOT:-$_PROJECT_ROOT}"
GOWE_TESTDATA="${GOWE_TESTDATA:-$GOWE_PROJECT_ROOT/testdata}"
GOWE_WORKDIR="${GOWE_WORKDIR:-$GOWE_PROJECT_ROOT/tmp/workdir}"
GOWE_CONFORMANCE_DIR="${GOWE_CONFORMANCE_DIR:-$GOWE_TESTDATA/cwl-v1.2}"

# Test ports (avoid conflicts with running services)
GOWE_TEST_SERVER_LOCAL_PORT="${GOWE_TEST_SERVER_LOCAL_PORT:-8091}"
GOWE_TEST_DISTRIBUTED_PORT="${GOWE_TEST_DISTRIBUTED_PORT:-8090}"

# Export for use by child processes
export GOWE_PROJECT_ROOT GOWE_TESTDATA GOWE_WORKDIR GOWE_CONFORMANCE_DIR
export GOWE_TEST_SERVER_LOCAL_PORT GOWE_TEST_DISTRIBUTED_PORT

# =============================================================================
# Colors and Formatting
# =============================================================================

# Check if stdout is a terminal for color support
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    CYAN='\033[0;36m'
    BLUE='\033[0;34m'
    MAGENTA='\033[0;35m'
    BOLD='\033[1m'
    DIM='\033[2m'
    NC='\033[0m'
else
    RED=''
    GREEN=''
    YELLOW=''
    CYAN=''
    BLUE=''
    MAGENTA=''
    BOLD=''
    DIM=''
    NC=''
fi

# Unicode symbols (with fallback for non-UTF terminals)
if [ "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" = *UTF-8* ] 2>/dev/null || [ "$(locale charmap 2>/dev/null)" = "UTF-8" ]; then
    CHECKMARK="✓"
    CROSSMARK="✗"
    WARNING="⚠"
    SKIP="○"
    ARROW="→"
else
    CHECKMARK="PASS"
    CROSSMARK="FAIL"
    WARNING="WARN"
    SKIP="SKIP"
    ARROW="->"
fi

# =============================================================================
# Logging Functions
# =============================================================================

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_debug() {
    if [ "${VERBOSE:-false}" = "true" ]; then
        echo -e "${DIM}[DEBUG]${NC} $1"
    fi
}

log_header() {
    echo ""
    echo -e "${CYAN}${BOLD}=== $1 ===${NC}"
    echo ""
}

log_subheader() {
    echo -e "${BLUE}${BOLD}--- $1 ---${NC}"
}

# Log a test result with symbol
log_result() {
    local name="$1"
    local status="$2"
    local details="$3"

    case "$status" in
        pass|passed|0)
            echo -e "  ${GREEN}${CHECKMARK}${NC} ${name}${details:+: $details}"
            ;;
        fail|failed|1)
            echo -e "  ${RED}${CROSSMARK}${NC} ${name}${details:+: $details}"
            ;;
        warn|warning)
            echo -e "  ${YELLOW}${WARNING}${NC} ${name}${details:+: $details}"
            ;;
        skip|skipped)
            echo -e "  ${DIM}${SKIP}${NC} ${name}${details:+: $details}"
            ;;
        *)
            echo -e "  ${DIM}?${NC} ${name}${details:+: $details}"
            ;;
    esac
}

# =============================================================================
# Result Tracking
# =============================================================================

# Check bash version for associative array support
if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
    # Fallback for bash < 4: use simple arrays and helper functions
    TEST_RESULTS_KEYS=()
    TEST_RESULTS_VALUES=()
    TEST_DETAILS_VALUES=()

    _set_result() {
        local key="$1" value="$2" details="$3"
        local i
        for i in "${!TEST_RESULTS_KEYS[@]}"; do
            if [ "${TEST_RESULTS_KEYS[$i]}" = "$key" ]; then
                TEST_RESULTS_VALUES[$i]="$value"
                TEST_DETAILS_VALUES[$i]="$details"
                return
            fi
        done
        TEST_RESULTS_KEYS+=("$key")
        TEST_RESULTS_VALUES+=("$value")
        TEST_DETAILS_VALUES+=("$details")
    }

    _get_result() {
        local key="$1"
        local i
        for i in "${!TEST_RESULTS_KEYS[@]}"; do
            if [ "${TEST_RESULTS_KEYS[$i]}" = "$key" ]; then
                echo "${TEST_RESULTS_VALUES[$i]}"
                return
            fi
        done
        echo "unknown"
    }

    _get_details() {
        local key="$1"
        local i
        for i in "${!TEST_RESULTS_KEYS[@]}"; do
            if [ "${TEST_RESULTS_KEYS[$i]}" = "$key" ]; then
                echo "${TEST_DETAILS_VALUES[$i]}"
                return
            fi
        done
        echo ""
    }
else
    # Bash 4+: use associative arrays
    declare -A TEST_RESULTS
    declare -A TEST_DETAILS
fi

TOTAL_PASSED=0
TOTAL_FAILED=0
TOTAL_SKIPPED=0

# Record a test result
record_result() {
    local mode="$1"
    local status="$2"
    local details="$3"

    if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
        _set_result "$mode" "$status" "$details"
    else
        TEST_RESULTS["$mode"]="$status"
        TEST_DETAILS["$mode"]="$details"
    fi

    case "$status" in
        pass|passed|0)
            TOTAL_PASSED=$((TOTAL_PASSED + 1))
            ;;
        fail|failed|1)
            TOTAL_FAILED=$((TOTAL_FAILED + 1))
            ;;
        skip|skipped)
            TOTAL_SKIPPED=$((TOTAL_SKIPPED + 1))
            ;;
    esac
}

# Get result for a mode
get_result() {
    local mode="$1"
    if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
        _get_result "$mode"
    else
        echo "${TEST_RESULTS[$mode]:-unknown}"
    fi
}

# Get details for a mode
get_details() {
    local mode="$1"
    if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
        _get_details "$mode"
    else
        echo "${TEST_DETAILS[$mode]:-}"
    fi
}

# =============================================================================
# Prerequisite Checking
# =============================================================================

# Check if a command exists
check_command() {
    local cmd="$1"
    local install_hint="$2"

    if command -v "$cmd" &> /dev/null; then
        log_debug "Found: $cmd"
        return 0
    else
        log_error "Command not found: $cmd"
        if [ -n "$install_hint" ]; then
            log_info "Install with: $install_hint"
        fi
        return 1
    fi
}

# Check Go version
check_go_version() {
    local required="${1:-1.21}"

    if ! command -v go &> /dev/null; then
        log_error "Go is not installed"
        return 1
    fi

    local version
    version=$(go version | sed 's/go version go\([0-9]*\.[0-9]*\).*/\1/')

    if [ "$(printf '%s\n' "$required" "$version" | sort -V | head -n1)" = "$required" ]; then
        log_debug "Go version: $version (>= $required required)"
        return 0
    else
        log_error "Go version $version is too old (>= $required required)"
        return 1
    fi
}

# Check if Docker is available
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_debug "Docker not installed"
        return 1
    fi

    if ! docker info &> /dev/null; then
        log_debug "Docker daemon not running"
        return 1
    fi

    log_debug "Docker is available"
    return 0
}

# Check if docker-compose is available
check_docker_compose() {
    if command -v docker-compose &> /dev/null; then
        log_debug "docker-compose is available"
        return 0
    elif docker compose version &> /dev/null; then
        log_debug "docker compose (plugin) is available"
        return 0
    else
        log_debug "docker-compose not available"
        return 1
    fi
}

# Check if a port is available
check_port_available() {
    local port="$1"

    if lsof -i ":$port" > /dev/null 2>&1; then
        local pid
        pid=$(lsof -t -i ":$port" 2>/dev/null | head -1)
        local process
        process=$(ps -p "$pid" -o comm= 2>/dev/null || echo "unknown")
        log_debug "Port $port in use by $process (PID $pid)"
        return 1
    fi

    log_debug "Port $port is available"
    return 0
}

# Check if cwltest is installed
check_cwltest() {
    if command -v cwltest &> /dev/null; then
        log_debug "cwltest is installed"
        return 0
    else
        log_debug "cwltest not found"
        return 1
    fi
}

# Install cwltest if not present
ensure_cwltest() {
    if check_cwltest; then
        return 0
    fi

    log_info "Installing cwltest..."
    if pip install cwltest; then
        return 0
    else
        log_error "Failed to install cwltest"
        return 1
    fi
}

# =============================================================================
# Build Helpers
# =============================================================================

# Build a Go binary
build_binary() {
    local output="$1"
    local package="$2"
    local name="${3:-$(basename "$output")}"

    log_info "Building $name..."

    if go build -o "$output" "$package"; then
        log_debug "Built: $output"
        return 0
    else
        log_error "Failed to build $name"
        return 1
    fi
}

# Build all required binaries
build_all_binaries() {
    local project_dir="$1"

    local failed=0

    build_binary "$project_dir/bin/cwl-runner" "$project_dir/cmd/cwl-runner" "cwl-runner" || failed=1
    build_binary "$project_dir/bin/gowe" "$project_dir/cmd/cli" "gowe CLI" || failed=1
    build_binary "$project_dir/bin/server" "$project_dir/cmd/server" "server" || failed=1
    build_binary "$project_dir/bin/worker" "$project_dir/cmd/worker" "worker" || failed=1

    return $failed
}

# =============================================================================
# Conformance Test Helpers
# =============================================================================

# Clone CWL v1.2 conformance tests if needed
ensure_conformance_tests() {
    local project_dir="$1"
    local conformance_dir="$project_dir/testdata/cwl-v1.2"

    if [ -f "$conformance_dir/conformance_tests.yaml" ]; then
        log_debug "Conformance tests already present"
        return 0
    fi

    log_info "Cloning CWL v1.2 conformance tests..."
    if git clone --depth 1 https://github.com/common-workflow-language/cwl-v1.2.git "$conformance_dir"; then
        return 0
    else
        log_error "Failed to clone conformance tests"
        return 1
    fi
}

# Parse cwltest output to extract pass/fail counts
parse_cwltest_output() {
    local output_file="$1"

    # Look for the summary line like "Test Result: 250 tests passed, 128 tests failed"
    local summary
    summary=$(grep -E "(Test Result|tests passed|tests failed)" "$output_file" | tail -1)

    local passed=0
    local failed=0
    local total=0

    if echo "$summary" | grep -q "tests passed"; then
        passed=$(echo "$summary" | sed -n 's/.*\([0-9]\+\) tests passed.*/\1/p')
        failed=$(echo "$summary" | sed -n 's/.*\([0-9]\+\) tests failed.*/\1/p')
        total=$((passed + failed))
    fi

    echo "$passed/$total"
}

# =============================================================================
# Report Generation
# =============================================================================

# Generate a markdown report
generate_report() {
    local report_file="$1"
    local title="$2"

    local report_dir
    report_dir=$(dirname "$report_file")
    mkdir -p "$report_dir"

    {
        echo "# $title"
        echo ""
        echo "**Generated:** $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        echo "## Summary"
        echo ""
        echo "| Status | Count |"
        echo "|--------|-------|"
        echo "| Passed | $TOTAL_PASSED |"
        echo "| Failed | $TOTAL_FAILED |"
        echo "| Skipped | $TOTAL_SKIPPED |"
        echo ""
        echo "## Results by Mode"
        echo ""
        echo "| Mode | Status | Details |"
        echo "|------|--------|---------|"

        if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
            for i in "${!TEST_RESULTS_KEYS[@]}"; do
                local mode="${TEST_RESULTS_KEYS[$i]}"
                local status="${TEST_RESULTS_VALUES[$i]}"
                local details="${TEST_DETAILS_VALUES[$i]:-}"
                local symbol

                case "$status" in
                    pass|passed|0) symbol="$CHECKMARK" ;;
                    fail|failed|1) symbol="$CROSSMARK" ;;
                    skip|skipped) symbol="$SKIP" ;;
                    *) symbol="?" ;;
                esac

                echo "| $mode | $symbol | $details |"
            done | sort
        else
            for mode in "${!TEST_RESULTS[@]}"; do
                local status="${TEST_RESULTS[$mode]}"
                local details="${TEST_DETAILS[$mode]:-}"
                local symbol

                case "$status" in
                    pass|passed|0) symbol="$CHECKMARK" ;;
                    fail|failed|1) symbol="$CROSSMARK" ;;
                    skip|skipped) symbol="$SKIP" ;;
                    *) symbol="?" ;;
                esac

                echo "| $mode | $symbol | $details |"
            done | sort
        fi

        echo ""
        echo "---"
        echo "*Report generated by GoWe test suite*"
    } > "$report_file"

    log_info "Report saved to: $report_file"
}

# =============================================================================
# Process Management
# =============================================================================

# Wait for a URL to become available
wait_for_url() {
    local url="$1"
    local max_attempts="${2:-30}"
    local interval="${3:-1}"

    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if curl -s "$url" > /dev/null 2>&1; then
            return 0
        fi
        attempt=$((attempt + 1))
        sleep "$interval"
    done

    return 1
}

# Kill a process and wait for it to exit
kill_and_wait() {
    local pid="$1"
    local timeout="${2:-10}"

    if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
        return 0
    fi

    kill "$pid" 2>/dev/null || true

    local count=0
    while kill -0 "$pid" 2>/dev/null && [ $count -lt $timeout ]; do
        sleep 1
        count=$((count + 1))
    done

    if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
    fi

    wait "$pid" 2>/dev/null || true
}

# =============================================================================
# Timing
# =============================================================================

# Get current time in seconds (with nanosecond precision if available)
get_time() {
    if date +%s.%N &>/dev/null; then
        date +%s.%N
    else
        date +%s
    fi
}

# Calculate duration between two timestamps
calc_duration() {
    local start="$1"
    local end="$2"

    # Use bc if available for precision, otherwise awk
    if command -v bc &>/dev/null; then
        echo "scale=1; $end - $start" | bc
    else
        awk "BEGIN {printf \"%.1f\", $end - $start}"
    fi
}

# Format seconds as human-readable duration
format_duration() {
    local seconds="$1"
    local int_seconds
    int_seconds=$(echo "$seconds" | cut -d. -f1)

    if [ "$int_seconds" -lt 60 ]; then
        echo "${seconds}s"
    elif [ "$int_seconds" -lt 3600 ]; then
        local mins=$((int_seconds / 60))
        local secs=$((int_seconds % 60))
        echo "${mins}m ${secs}s"
    else
        local hours=$((int_seconds / 3600))
        local mins=$(((int_seconds % 3600) / 60))
        echo "${hours}h ${mins}m"
    fi
}

# =============================================================================
# Misc Utilities
# =============================================================================

# Get project root directory (assumes this script is in scripts/)
get_project_root() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    dirname "$script_dir"
}

# Create a temporary wrapper script for cwltest
create_cwltest_wrapper() {
    local gowe_binary="$1"
    local server_url="$2"
    local extra_args="${3:-}"

    local wrapper
    wrapper=$(mktemp)

    cat > "$wrapper" << EOF
#!/bin/bash
exec "$gowe_binary" run --server "$server_url" --quiet $extra_args "\$@"
EOF

    chmod +x "$wrapper"
    echo "$wrapper"
}
