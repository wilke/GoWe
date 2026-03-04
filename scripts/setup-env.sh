#!/usr/bin/env bash
#
# setup-env.sh - Initialize GoWe development/testing environment
#
# This script:
# 1. Creates the ~/.gowe config directory
# 2. Copies .env.example to .env if needed
# 3. Auto-detects and sets paths
# 4. Validates prerequisites
# 5. Optionally builds binaries
#
# Usage:
#   ./scripts/setup-env.sh [options]
#
# Options:
#   -b, --build       Build all binaries after setup
#   -f, --force       Overwrite existing .env file
#   -t, --test        Run quick validation tests after setup
#   -q, --quiet       Minimal output
#   -h, --help        Show this help message
#
# Examples:
#   ./scripts/setup-env.sh           # Basic setup
#   ./scripts/setup-env.sh -b        # Setup and build
#   ./scripts/setup-env.sh -b -t     # Setup, build, and test
#   ./scripts/setup-env.sh -f        # Force overwrite .env
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Options
BUILD=false
FORCE=false
RUN_TEST=false
QUIET=false

# Colors
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    NC='\033[0m'
else
    RED='' GREEN='' YELLOW='' CYAN='' BOLD='' NC=''
fi

log_info() {
    [ "$QUIET" = true ] || echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    [ "$QUIET" = true ] || echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

log_success() {
    [ "$QUIET" = true ] || echo -e "${GREEN}[OK]${NC} $1"
}

usage() {
    sed -n '2,25p' "$0" | sed 's/^# \?//'
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -b|--build)
            BUILD=true
            shift
            ;;
        -f|--force)
            FORCE=true
            shift
            ;;
        -t|--test)
            RUN_TEST=true
            shift
            ;;
        -q|--quiet)
            QUIET=true
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

cd "$PROJECT_DIR"

echo -e "${CYAN}${BOLD}=== GoWe Environment Setup ===${NC}"
echo ""

# =============================================================================
# Step 1: Create config directory
# =============================================================================

log_info "Creating config directory..."

GOWE_CONFIG_DIR="$HOME/.gowe"
mkdir -p "$GOWE_CONFIG_DIR"
log_success "Config directory: $GOWE_CONFIG_DIR"

# =============================================================================
# Step 2: Create .env file
# =============================================================================

log_info "Setting up environment file..."

ENV_FILE="$PROJECT_DIR/.env"
ENV_EXAMPLE="$PROJECT_DIR/.env.example"

if [ -f "$ENV_FILE" ] && [ "$FORCE" = false ]; then
    log_warn ".env already exists (use -f to overwrite)"
else
    if [ ! -f "$ENV_EXAMPLE" ]; then
        log_error ".env.example not found"
        exit 1
    fi

    # Copy and customize .env
    cp "$ENV_EXAMPLE" "$ENV_FILE"

    # Auto-detect and set project paths
    sed -i.bak "s|^GOWE_PROJECT_ROOT=.*|GOWE_PROJECT_ROOT=$PROJECT_DIR|" "$ENV_FILE"
    sed -i.bak "s|^GOWE_TESTDATA=.*|GOWE_TESTDATA=$PROJECT_DIR/testdata|" "$ENV_FILE"
    sed -i.bak "s|^GOWE_WORKDIR=.*|GOWE_WORKDIR=$PROJECT_DIR/tmp/workdir|" "$ENV_FILE"
    sed -i.bak "s|^GOWE_CONFORMANCE_DIR=.*|GOWE_CONFORMANCE_DIR=$PROJECT_DIR/testdata/cwl-v1.2|" "$ENV_FILE"

    # Set Docker host path map for distributed tests
    DOCKER_PATH_MAP="/workdir=$PROJECT_DIR/tmp/workdir:/testdata=$PROJECT_DIR/testdata/cwl-v1.2"
    sed -i.bak "s|^DOCKER_HOST_PATH_MAP=.*|DOCKER_HOST_PATH_MAP=$DOCKER_PATH_MAP|" "$ENV_FILE"

    # Clean up backup files
    rm -f "$ENV_FILE.bak"

    log_success "Created .env with auto-detected paths"
fi

# =============================================================================
# Step 3: Create working directories
# =============================================================================

log_info "Creating working directories..."

mkdir -p "$PROJECT_DIR/tmp/workdir"
mkdir -p "$PROJECT_DIR/tmp/outputs"
mkdir -p "$PROJECT_DIR/bin"
mkdir -p "$PROJECT_DIR/reports"

log_success "Working directories created"

# =============================================================================
# Step 4: Validate prerequisites
# =============================================================================

log_info "Checking prerequisites..."

PREREQ_OK=true

# Check Go
if command -v go &> /dev/null; then
    GO_VERSION=$(go version | sed 's/go version go\([0-9]*\.[0-9]*\).*/\1/')
    log_success "Go $GO_VERSION"
else
    log_error "Go not found - install from https://go.dev"
    PREREQ_OK=false
fi

# Check Docker (optional)
if command -v docker &> /dev/null; then
    if docker info &> /dev/null; then
        log_success "Docker available"
    else
        log_warn "Docker installed but daemon not running"
    fi
else
    log_warn "Docker not found (optional, needed for container tests)"
fi

# Check cwltest
if command -v cwltest &> /dev/null; then
    log_success "cwltest installed"
else
    log_warn "cwltest not found - install with: pip install cwltest"
fi

# Check git
if command -v git &> /dev/null; then
    log_success "git available"
else
    log_error "git not found"
    PREREQ_OK=false
fi

if [ "$PREREQ_OK" = false ]; then
    log_error "Some prerequisites are missing"
    exit 1
fi

# =============================================================================
# Step 5: Clone conformance tests if needed
# =============================================================================

log_info "Checking conformance tests..."

CONFORMANCE_DIR="$PROJECT_DIR/testdata/cwl-v1.2"
if [ -f "$CONFORMANCE_DIR/conformance_tests.yaml" ]; then
    log_success "Conformance tests present"
else
    log_info "Cloning CWL v1.2 conformance tests..."
    git clone --depth 1 https://github.com/common-workflow-language/cwl-v1.2.git "$CONFORMANCE_DIR"
    log_success "Conformance tests cloned"
fi

# =============================================================================
# Step 6: Build binaries (optional)
# =============================================================================

if [ "$BUILD" = true ]; then
    log_info "Building binaries..."

    go build -o bin/cwl-runner ./cmd/cwl-runner
    log_success "Built bin/cwl-runner"

    go build -o bin/gowe ./cmd/cli
    log_success "Built bin/gowe"

    go build -o bin/server ./cmd/server
    log_success "Built bin/server"

    go build -o bin/worker ./cmd/worker
    log_success "Built bin/worker"
fi

# =============================================================================
# Step 7: Run validation tests (optional)
# =============================================================================

if [ "$RUN_TEST" = true ]; then
    log_info "Running validation tests..."

    # Run unit tests
    if go test ./... -short > /dev/null 2>&1; then
        log_success "Unit tests passed"
    else
        log_warn "Some unit tests failed (run 'go test ./...' for details)"
    fi
fi

# =============================================================================
# Summary
# =============================================================================

echo ""
echo -e "${CYAN}${BOLD}=== Setup Complete ===${NC}"
echo ""
echo "Configuration:"
echo "  Config dir:      $GOWE_CONFIG_DIR"
echo "  Environment:     $ENV_FILE"
echo "  Testdata:        $PROJECT_DIR/testdata"
echo "  Working dir:     $PROJECT_DIR/tmp/workdir"
echo ""
echo "Next steps:"
echo "  1. Review and customize .env if needed"
echo "  2. Source the environment: source .env"
echo "  3. Build binaries: go build -o bin/cwl-runner ./cmd/cwl-runner"
echo "  4. Run tests: ./scripts/run-all-tests.sh -t 1"
echo ""
echo "Quick test:"
echo "  ./scripts/run-all-tests.sh -m cwl-runner -q"
echo ""
