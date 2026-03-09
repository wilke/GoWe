#!/usr/bin/env bash
# build-sif.sh — Build all Apptainer SIF images for GoWe deployment.
#
# Usage:
#   ./build-sif.sh              Build all SIF images
#   ./build-sif.sh --server     Build server SIF only
#   ./build-sif.sh --shock      Build Shock + MongoDB SIFs only
#   ./build-sif.sh --no-build   Skip Go binary compilation
#
# Environment:
#   SIF_DIR     Output directory for SIF files (default: ./sif)
#   GO          Go compiler (default: go)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SIF_DIR="${SIF_DIR:-$SCRIPT_DIR/sif}"
GO="${GO:-go}"

BUILD_SERVER=true
BUILD_SHOCK=true
SKIP_GO_BUILD=false

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --server)     BUILD_SERVER=true; BUILD_SHOCK=false ;;
        --shock)      BUILD_SERVER=false; BUILD_SHOCK=true ;;
        --no-build)   SKIP_GO_BUILD=true ;;
        -h|--help)
            head -12 "$0" | tail -11 | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1
            ;;
    esac
    shift
done

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo "==> $*"; }
err()  { echo "ERROR: $*" >&2; exit 1; }

check_command() {
    command -v "$1" >/dev/null 2>&1 || err "$1 is required but not found in PATH"
}

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------
check_command apptainer

mkdir -p "$SIF_DIR"

# ---------------------------------------------------------------------------
# Step 1: Build Go binaries (static, CGO_ENABLED=0)
# ---------------------------------------------------------------------------
if [ "$SKIP_GO_BUILD" = false ] && [ "$BUILD_SERVER" = true ]; then
    check_command "$GO"

    log "Building Go binaries..."
    cd "$PROJECT_DIR"

    CGO_ENABLED=0 "$GO" build -o bin/server ./cmd/server
    log "  bin/server  ✓"

    CGO_ENABLED=0 "$GO" build -o bin/worker ./cmd/worker
    log "  bin/worker  ✓"

    CGO_ENABLED=0 "$GO" build -o bin/gowe ./cmd/cli
    log "  bin/gowe    ✓"
fi

# ---------------------------------------------------------------------------
# Step 2: Build GoWe server SIF
# ---------------------------------------------------------------------------
if [ "$BUILD_SERVER" = true ]; then
    log "Building gowe-server.sif..."

    # Verify prerequisites
    [ -x "$PROJECT_DIR/bin/server" ] || err "bin/server not found. Run without --no-build or build first."
    [ -d "$PROJECT_DIR/ui/assets" ]  || err "ui/assets directory not found."

    cd "$PROJECT_DIR"
    apptainer build --force "$SIF_DIR/gowe-server.sif" "$SCRIPT_DIR/gowe-server.def"
    log "  $SIF_DIR/gowe-server.sif  ✓"
fi

# ---------------------------------------------------------------------------
# Step 3: Build MongoDB SIF
# ---------------------------------------------------------------------------
if [ "$BUILD_SHOCK" = true ]; then
    log "Building mongodb.sif..."

    # Pull directly from Docker Hub — simplest and most reliable
    apptainer build --force "$SIF_DIR/mongodb.sif" docker://mongo:5.0
    log "  $SIF_DIR/mongodb.sif  ✓"
fi

# ---------------------------------------------------------------------------
# Step 4: Build Shock server SIF
# ---------------------------------------------------------------------------
if [ "$BUILD_SHOCK" = true ]; then
    log "Building shock-server.sif..."

    # Strategy 1: Convert from local Docker daemon (if available)
    if command -v docker >/dev/null 2>&1 && \
       docker image inspect shock-shock-server:latest >/dev/null 2>&1; then
        log "  Converting from local Docker image shock-shock-server:latest..."
        apptainer build --force "$SIF_DIR/shock-server.sif" \
            docker-daemon://shock-shock-server:latest

    # Strategy 2: Pull from MG-RAST Docker Hub
    elif apptainer build --force "$SIF_DIR/shock-server.sif" \
            docker://mgrast/shock-server:latest 2>/dev/null; then
        true  # success

    # Strategy 3: Use the definition file
    else
        log "  Docker image not available; building from definition file..."
        apptainer build --force "$SIF_DIR/shock-server.sif" \
            "$SCRIPT_DIR/shock-server.def"
    fi

    log "  $SIF_DIR/shock-server.sif  ✓"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log ""
log "SIF images built successfully in $SIF_DIR:"
ls -lh "$SIF_DIR"/*.sif 2>/dev/null || true
