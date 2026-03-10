#!/usr/bin/env bash
# start-worker.sh — Start a GoWe worker on a compute node (bare metal).
#
# The worker runs directly on the host and uses Apptainer to execute
# CWL tool containers.  Shock is used for file staging by default.
#
# Usage:
#   ./start-worker.sh                           Start with defaults / env
#   ./start-worker.sh --name gpu-worker-0 --gpu --gpu-id 0
#   GOWE_SERVER_URL=http://head:8080 ./start-worker.sh
#
# All options can be set via environment variables (see gowe-stack.env.example)
# or via command-line flags below.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Load environment if available
# ---------------------------------------------------------------------------
ENV_FILE="${GOWE_ENV_FILE:-$SCRIPT_DIR/gowe-stack.env}"
if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
fi

# ---------------------------------------------------------------------------
# Defaults (from env or hard-coded)
# ---------------------------------------------------------------------------
SERVER_URL="${GOWE_SERVER_URL:-http://localhost:8080}"
WORKER_NAME="${GOWE_WORKER_NAME:-$(hostname)}"
WORKER_GROUP="${GOWE_WORKER_GROUP:-default}"
WORKDIR="${GOWE_WORKDIR:-/tmp/gowe-worker}"
RUNTIME="${GOWE_RUNTIME:-apptainer}"
POLL="${GOWE_POLL:-5s}"
STAGE_MODE="${GOWE_STAGE_MODE:-copy}"

SHOCK_HOST="${GOWE_SHOCK_HOST:-localhost:7445}"
SHOCK_TOKEN="${SHOCK_TOKEN:-}"
SHOCK_USE_HTTP="${SHOCK_USE_HTTP:-true}"

GPU_ENABLED="${GOWE_GPU:-false}"
GPU_ID="${GOWE_GPU_ID:-}"

WORKER_KEY="${GOWE_WORKER_KEY:-}"
DEBUG="${GOWE_DEBUG:-false}"

WORKER_BINARY="${GOWE_WORKER_BINARY:-$SCRIPT_DIR/../../bin/worker}"

# ---------------------------------------------------------------------------
# Parse CLI overrides
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --server)       SERVER_URL="$2";    shift ;;
        --name)         WORKER_NAME="$2";   shift ;;
        --group)        WORKER_GROUP="$2";  shift ;;
        --workdir)      WORKDIR="$2";       shift ;;
        --runtime)      RUNTIME="$2";       shift ;;
        --poll)         POLL="$2";          shift ;;
        --stage-mode)   STAGE_MODE="$2";    shift ;;
        --shock-host)   SHOCK_HOST="$2";    shift ;;
        --shock-token)  SHOCK_TOKEN="$2";   shift ;;
        --gpu)          GPU_ENABLED=true    ;;
        --gpu-id)       GPU_ID="$2"; GPU_ENABLED=true; shift ;;
        --worker-key)   WORKER_KEY="$2";    shift ;;
        --debug)        DEBUG=true          ;;
        --binary)       WORKER_BINARY="$2"; shift ;;
        -h|--help)
            head -14 "$0" | tail -13 | sed 's/^# \?//'
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
# Pre-flight checks
# ---------------------------------------------------------------------------
if [ ! -x "$WORKER_BINARY" ]; then
    echo "ERROR: Worker binary not found at $WORKER_BINARY" >&2
    echo "  Build it with: CGO_ENABLED=0 go build -o bin/worker ./cmd/worker" >&2
    exit 1
fi

if [ "$RUNTIME" = "apptainer" ]; then
    # Load HPC module if available
    module load apptainer 2>/dev/null || true

    if ! command -v apptainer >/dev/null 2>&1; then
        echo "ERROR: apptainer not found in PATH (runtime=$RUNTIME)" >&2
        echo "  Install Apptainer or load the module: module load apptainer" >&2
        exit 1
    fi
fi

# Ensure work directory exists
mkdir -p "$WORKDIR"

# ---------------------------------------------------------------------------
# Build command
# ---------------------------------------------------------------------------
STAGE_OUT="shock://${SHOCK_HOST}"

CMD=(
    "$WORKER_BINARY"
    -server     "$SERVER_URL"
    -name       "$WORKER_NAME"
    -group      "$WORKER_GROUP"
    -runtime    "$RUNTIME"
    -workdir    "$WORKDIR"
    -stage-out  "$STAGE_OUT"
    -stage-mode "$STAGE_MODE"
    -shock-host "$SHOCK_HOST"
    -poll       "$POLL"
)

if [ "$SHOCK_USE_HTTP" = true ]; then
    CMD+=(-shock-use-http)
fi

if [ -n "$SHOCK_TOKEN" ]; then
    CMD+=(-shock-token "$SHOCK_TOKEN")
fi

if [ "$GPU_ENABLED" = true ]; then
    CMD+=(-gpu)
    if [ -n "$GPU_ID" ]; then
        CMD+=(-gpu-id "$GPU_ID")
    fi
fi

if [ -n "$WORKER_KEY" ]; then
    CMD+=(-worker-key "$WORKER_KEY")
fi

if [ "$DEBUG" = true ]; then
    CMD+=(-debug)
fi

# ---------------------------------------------------------------------------
# Launch
# ---------------------------------------------------------------------------
echo "Starting GoWe worker..."
echo "  Server:   $SERVER_URL"
echo "  Name:     $WORKER_NAME"
echo "  Runtime:  $RUNTIME"
echo "  Workdir:  $WORKDIR"
echo "  StageOut: $STAGE_OUT"
if [ "$GPU_ENABLED" = true ]; then
    echo "  GPU:      enabled (id=${GPU_ID:-all})"
fi
echo ""

exec "${CMD[@]}"
