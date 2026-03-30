#!/usr/bin/env bash
#
# Start GoWe server and workers for production use.
#
# Usage:
#   ./scripts/start-server.sh              # defaults: port=8091, base=/scout/wf
#   GOWE_PORT=9090 ./scripts/start-server.sh
#   BASE_DIR=/data/gowe ./scripts/start-server.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Configuration (override via environment)
GOWE_PORT="${GOWE_PORT:-8091}"
BASE_DIR="${BASE_DIR:-/scout/wf}"
IMAGE_DIR="${IMAGE_DIR:-/scout/containers}"
PRE_STAGE_DIR="${PRE_STAGE_DIR:-/local_databases}"
NUM_WORKERS="${NUM_WORKERS:-2}"
LOG_LEVEL="${LOG_LEVEL:-info}"
ADMINS="${ADMINS:-awilke,awilke@bvbrc,olson,olson@bvbrc}"

# Derived paths
DB_PATH="$BASE_DIR/gowe/gowe.db"
UPLOAD_DIR="$BASE_DIR/gowe/uploads"
LOG_DIR="$BASE_DIR/gowe/logs"
PID_DIR="$BASE_DIR/gowe/pids"

# Build download dirs list: uploads + data + all worker workdirs
DOWNLOAD_DIRS="$UPLOAD_DIR,$BASE_DIR/data"
for i in $(seq 1 "$NUM_WORKERS"); do
    DOWNLOAD_DIRS="$DOWNLOAD_DIRS,$BASE_DIR/gowe/workdir/worker-${i}"
done

# Ensure directories exist
mkdir -p "$BASE_DIR/gowe" "$UPLOAD_DIR" "$LOG_DIR" "$PID_DIR" "$BASE_DIR/data"
for i in $(seq 1 "$NUM_WORKERS"); do
    mkdir -p "$BASE_DIR/gowe/workdir/worker-${i}"
done

cd "$PROJECT_DIR"

# Check binaries exist
if [[ ! -x ./bin/gowe-server ]] || [[ ! -x ./bin/gowe-worker ]]; then
    echo "ERROR: binaries not found. Run 'make build' first." >&2
    exit 1
fi

# Check nothing is already running
if pgrep -f "gowe-server.*:${GOWE_PORT}" > /dev/null 2>&1; then
    echo "ERROR: gowe-server already running on port ${GOWE_PORT}" >&2
    echo "  Run ./scripts/stop-server.sh first" >&2
    exit 1
fi

echo "Starting GoWe (port=${GOWE_PORT}, base=${BASE_DIR}, workers=${NUM_WORKERS})"

# --- Start server ---
./bin/gowe-server \
    --addr ":${GOWE_PORT}" \
    --db "$DB_PATH" \
    --default-executor worker \
    --allow-anonymous \
    --anonymous-executors "local,docker,worker,container" \
    --scheduler-poll 100ms \
    --upload-backend local \
    --upload-local-dir "$UPLOAD_DIR" \
    --upload-download-dirs "$DOWNLOAD_DIRS" \
    --log-level "$LOG_LEVEL" \
    --admins "$ADMINS" \
    > "$LOG_DIR/server.log" 2>&1 &
echo $! > "$PID_DIR/server.pid"
echo "  server  PID=$(cat "$PID_DIR/server.pid")  port=${GOWE_PORT}"

# Wait for server to be ready
for attempt in $(seq 1 20); do
    if curl -sf "http://localhost:${GOWE_PORT}/api/v1/health" > /dev/null 2>&1; then
        break
    fi
    sleep 0.5
done

if ! curl -sf "http://localhost:${GOWE_PORT}/api/v1/health" > /dev/null 2>&1; then
    echo "ERROR: server failed to start. Check $LOG_DIR/server.log" >&2
    exit 1
fi

# --- Start workers ---
# GPU IDs start at 1 (GPU 0 is typically reserved for other use)
for i in $(seq 1 "$NUM_WORKERS"); do
    ./bin/gowe-worker \
        --server "http://localhost:${GOWE_PORT}" \
        --runtime apptainer \
        --name "worker-${i}" \
        --workdir "$BASE_DIR/gowe/workdir/worker-${i}" \
        --stage-out "file://$BASE_DIR/data" \
        --poll 500ms \
        --log-level "$LOG_LEVEL" \
        --gpu --gpu-id "$i" \
        --image-dir "$IMAGE_DIR" \
        --pre-stage-dir "$PRE_STAGE_DIR" \
        --workspace-stager \
        > "$LOG_DIR/worker-${i}.log" 2>&1 &
    echo $! > "$PID_DIR/worker-${i}.pid"
    echo "  worker-${i}  PID=$(cat "$PID_DIR/worker-${i}.pid")  gpu=${i}"
done

# Wait for workers to register
sleep 2
ONLINE=$(curl -sf "http://localhost:${GOWE_PORT}/api/v1/workers" 2>/dev/null \
    | python3 -c "import sys,json; w=json.load(sys.stdin).get('data') or []; print(sum(1 for x in w if x['state']=='online'))" 2>/dev/null || echo 0)
echo ""
echo "GoWe started: server on :${GOWE_PORT}, ${ONLINE}/${NUM_WORKERS} workers online"
echo "Logs: $LOG_DIR/"
