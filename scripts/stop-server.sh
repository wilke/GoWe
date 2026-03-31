#!/usr/bin/env bash
#
# Stop GoWe server and workers gracefully.
#
# Usage:
#   ./scripts/stop-server.sh               # defaults: base=/scout/wf
#   BASE_DIR=/data/gowe ./scripts/stop-server.sh
#
set -euo pipefail

BASE_DIR="${BASE_DIR:-/scout/wf}"
PID_DIR="$BASE_DIR/gowe/pids"
TIMEOUT="${TIMEOUT:-10}"

if [[ ! -d "$PID_DIR" ]]; then
    echo "No PID directory found at $PID_DIR"
    echo "Trying to find processes by name..."
    pgrep -af 'gowe-(server|worker)' || echo "No GoWe processes found."
    exit 0
fi

stopped=0

# Stop workers first (let them finish current tasks gracefully)
for pidfile in "$PID_DIR"/worker-*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid=$(cat "$pidfile")
    name=$(basename "$pidfile" .pid)
    if kill -0 "$pid" 2>/dev/null; then
        echo "Stopping $name (PID $pid)..."
        kill "$pid"
        stopped=$((stopped + 1))
    else
        echo "  $name (PID $pid) already stopped"
    fi
    rm -f "$pidfile"
done

# Give workers time to deregister gracefully
if [[ $stopped -gt 0 ]]; then
    echo "Waiting for workers to deregister..."
    sleep 2
fi

# Stop server
if [[ -f "$PID_DIR/server.pid" ]]; then
    pid=$(cat "$PID_DIR/server.pid")
    if kill -0 "$pid" 2>/dev/null; then
        echo "Stopping server (PID $pid)..."
        kill "$pid"
        stopped=$((stopped + 1))
    else
        echo "  server (PID $pid) already stopped"
    fi
    rm -f "$PID_DIR/server.pid"
fi

# Wait for all processes to exit
if [[ $stopped -gt 0 ]]; then
    echo -n "Waiting for shutdown"
    for i in $(seq 1 "$TIMEOUT"); do
        if ! pgrep -f 'gowe-(server|worker)' > /dev/null 2>&1; then
            echo " done"
            break
        fi
        echo -n "."
        sleep 1
    done

    # Force kill if still running
    if pgrep -f 'gowe-(server|worker)' > /dev/null 2>&1; then
        echo " forcing..."
        pkill -9 -f 'gowe-(server|worker)' 2>/dev/null || true
        sleep 1
    fi
fi

# Clean up any leftover PID files
rm -f "$PID_DIR"/*.pid

echo "GoWe stopped."
