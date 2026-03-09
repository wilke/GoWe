#!/usr/bin/env bash
# start-services.sh — Start the GoWe + Shock + MongoDB stack via Apptainer instances.
#
# Usage:
#   ./start-services.sh                     Start full stack
#   ./start-services.sh --server-only       Start GoWe server only (Shock/Mongo external)
#   ./start-services.sh --env gowe-stack.env  Use custom env file
#
# Requires: apptainer, curl
# All services run on host networking (Apptainer default).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
SERVER_ONLY=false
ENV_FILE="$SCRIPT_DIR/gowe-stack.env"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --server-only)  SERVER_ONLY=true ;;
        --env)          ENV_FILE="$2"; shift ;;
        -h|--help)
            head -10 "$0" | tail -9 | sed 's/^# \?//'
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
# Load configuration
# ---------------------------------------------------------------------------
if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
else
    echo "WARNING: $ENV_FILE not found — using defaults." >&2
fi

# Defaults
SIF_DIR="${SIF_DIR:-$SCRIPT_DIR/sif}"
DATA_DIR="${DATA_DIR:-/data/gowe}"
INSTANCE_PREFIX="${INSTANCE_PREFIX:-gowe}"

MONGO_PORT="${MONGO_PORT:-27017}"
SHOCK_PORT="${SHOCK_PORT:-7445}"
SERVER_PORT="${SERVER_PORT:-8080}"

GOWE_DEFAULT_EXECUTOR="${GOWE_DEFAULT_EXECUTOR:-worker}"
GOWE_ALLOW_ANONYMOUS="${GOWE_ALLOW_ANONYMOUS:-true}"
GOWE_ANONYMOUS_EXECUTORS="${GOWE_ANONYMOUS_EXECUTORS:-local,docker,worker,container,apptainer}"
GOWE_LOG_LEVEL="${GOWE_LOG_LEVEL:-info}"
GOWE_SCHEDULER_POLL="${GOWE_SCHEDULER_POLL:-2s}"

SHOCK_TOKEN="${SHOCK_TOKEN:-}"
SHOCK_USE_HTTP="${SHOCK_USE_HTTP:-true}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo "==> $*"; }
err()  { echo "ERROR: $*" >&2; exit 1; }

wait_for_health() {
    local name="$1" check_cmd="$2" max_attempts="${3:-30}" interval="${4:-2}"
    log "Waiting for $name..."
    for i in $(seq 1 "$max_attempts"); do
        if eval "$check_cmd" >/dev/null 2>&1; then
            log "  $name is ready."
            return 0
        fi
        sleep "$interval"
    done
    err "$name failed to become healthy after $((max_attempts * interval))s"
}

instance_running() {
    apptainer instance list 2>/dev/null | grep -q "$1"
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
command -v apptainer >/dev/null 2>&1 || err "apptainer is required but not found in PATH"
command -v curl      >/dev/null 2>&1 || err "curl is required but not found in PATH"

# ---------------------------------------------------------------------------
# Create persistent data directories
# ---------------------------------------------------------------------------
mkdir -p "$DATA_DIR/mongo"
mkdir -p "$DATA_DIR/shock/data"
mkdir -p "$DATA_DIR/shock/logs"
mkdir -p "$DATA_DIR/server"

# ---------------------------------------------------------------------------
# Start MongoDB
# ---------------------------------------------------------------------------
if [ "$SERVER_ONLY" = false ]; then
    MONGO_INSTANCE="${INSTANCE_PREFIX}-mongo"

    if instance_running "$MONGO_INSTANCE"; then
        log "MongoDB instance $MONGO_INSTANCE already running — skipping."
    else
        [ -f "$SIF_DIR/mongodb.sif" ] || err "$SIF_DIR/mongodb.sif not found. Run build-sif.sh first."

        log "Starting MongoDB..."
        apptainer instance start \
            --bind "$DATA_DIR/mongo:/data/db" \
            "$SIF_DIR/mongodb.sif" \
            "$MONGO_INSTANCE"

        wait_for_health "MongoDB" \
            "apptainer exec instance://$MONGO_INSTANCE mongo --port $MONGO_PORT --eval 'db.adminCommand(\"ping\")'"
    fi
fi

# ---------------------------------------------------------------------------
# Start Shock
# ---------------------------------------------------------------------------
if [ "$SERVER_ONLY" = false ]; then
    SHOCK_INSTANCE="${INSTANCE_PREFIX}-shock"

    if instance_running "$SHOCK_INSTANCE"; then
        log "Shock instance $SHOCK_INSTANCE already running — skipping."
    else
        [ -f "$SIF_DIR/shock-server.sif" ] || err "$SIF_DIR/shock-server.sif not found. Run build-sif.sh first."

        log "Starting Shock server..."
        apptainer instance start \
            --bind "$DATA_DIR/shock/data:/usr/local/shock/data" \
            --bind "$DATA_DIR/shock/logs:/var/log/shock" \
            "$SIF_DIR/shock-server.sif" \
            "$SHOCK_INSTANCE" \
            --hosts=localhost \
            --basic=false \
            --force_yes=true \
            --api-url="http://localhost:${SHOCK_PORT}" \
            --api-port="${SHOCK_PORT}"

        wait_for_health "Shock" \
            "curl -sf http://localhost:${SHOCK_PORT}/"
    fi
fi

# ---------------------------------------------------------------------------
# Start GoWe Server
# ---------------------------------------------------------------------------
SERVER_INSTANCE="${INSTANCE_PREFIX}-server"

if instance_running "$SERVER_INSTANCE"; then
    log "GoWe server instance $SERVER_INSTANCE already running — skipping."
else
    [ -f "$SIF_DIR/gowe-server.sif" ] || err "$SIF_DIR/gowe-server.sif not found. Run build-sif.sh first."

    # Build server flags
    SERVER_ARGS=(
        -addr ":${SERVER_PORT}"
        -db "/data/gowe.db"
        -default-executor "$GOWE_DEFAULT_EXECUTOR"
        -log-level "$GOWE_LOG_LEVEL"
        -scheduler-poll "$GOWE_SCHEDULER_POLL"
    )

    if [ "$GOWE_ALLOW_ANONYMOUS" = true ]; then
        SERVER_ARGS+=(-allow-anonymous)
        SERVER_ARGS+=(-anonymous-executors "$GOWE_ANONYMOUS_EXECUTORS")
    fi

    # Shock upload backend
    if [ "$SERVER_ONLY" = false ] || [ -n "$SHOCK_TOKEN" ]; then
        SERVER_ARGS+=(
            -upload-backend shock
            -upload-shock-host "localhost:${SHOCK_PORT}"
        )
        if [ "$SHOCK_USE_HTTP" = true ]; then
            SERVER_ARGS+=(-upload-shock-http)
        fi
        if [ -n "$SHOCK_TOKEN" ]; then
            SERVER_ARGS+=(-upload-shock-token "$SHOCK_TOKEN")
        fi
    fi

    log "Starting GoWe server..."
    apptainer instance start \
        --bind "$DATA_DIR/server:/data" \
        "$SIF_DIR/gowe-server.sif" \
        "$SERVER_INSTANCE" \
        "${SERVER_ARGS[@]}"

    wait_for_health "GoWe Server" \
        "curl -sf http://localhost:${SERVER_PORT}/api/v1/health"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log ""
log "GoWe stack is running:"
if [ "$SERVER_ONLY" = false ]; then
    log "  MongoDB:      localhost:${MONGO_PORT}  (instance: ${INSTANCE_PREFIX}-mongo)"
    log "  Shock:        localhost:${SHOCK_PORT}  (instance: ${INSTANCE_PREFIX}-shock)"
fi
log "  GoWe Server:  localhost:${SERVER_PORT}  (instance: ${INSTANCE_PREFIX}-server)"
log ""
log "Next steps:"
log "  • Start workers:  ./start-worker.sh"
log "  • Stop stack:     ./stop-services.sh"
log "  • View instances: apptainer instance list"
