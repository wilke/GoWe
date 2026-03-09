#!/usr/bin/env bash
# stop-services.sh — Gracefully stop all GoWe Apptainer instances.
#
# Usage:
#   ./stop-services.sh              Stop all instances
#   ./stop-services.sh --env FILE   Use custom env file

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
ENV_FILE="$SCRIPT_DIR/gowe-stack.env"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --env)  ENV_FILE="$2"; shift ;;
        -h|--help)
            head -6 "$0" | tail -5 | sed 's/^# \?//'
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
fi

INSTANCE_PREFIX="${INSTANCE_PREFIX:-gowe}"

# ---------------------------------------------------------------------------
# Stop instances in reverse dependency order
# ---------------------------------------------------------------------------
INSTANCES=(
    "${INSTANCE_PREFIX}-server"
    "${INSTANCE_PREFIX}-shock"
    "${INSTANCE_PREFIX}-mongo"
)

echo "Stopping GoWe services..."

for instance in "${INSTANCES[@]}"; do
    if apptainer instance list 2>/dev/null | grep -q "$instance"; then
        echo "  Stopping $instance..."
        apptainer instance stop "$instance"
    else
        echo "  $instance is not running."
    fi
done

echo "All services stopped."
