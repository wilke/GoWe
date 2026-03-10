#!/bin/bash
# array-workers.sbatch — Launch multiple GoWe workers as a SLURM job array.
#
# Each array task starts an independent worker.  Useful for scaling out
# across multiple nodes or GPUs.
#
# Usage:
#   sbatch array-workers.sbatch                    # 4 workers (0-3)
#   sbatch --array=0-7 array-workers.sbatch        # 8 workers
#   sbatch --array=0-3 --gres=gpu:1 array-workers.sbatch  # 4 GPU workers
#
# Override defaults with environment variables:
#   DEPLOY_DIR=/opt/gowe/deploy/apptainer sbatch array-workers.sbatch

#SBATCH --job-name=gowe-workers
#SBATCH --output=gowe-worker-%A_%a.out
#SBATCH --error=gowe-worker-%A_%a.err
#SBATCH --time=24:00:00
#SBATCH --array=0-3

# ---------------------------------------------------------------------------
# Load Apptainer module (HPC-specific; no-op if unavailable)
# ---------------------------------------------------------------------------
module load apptainer 2>/dev/null || true

# ---------------------------------------------------------------------------
# Configuration — REQUIRED: set these for your cluster
# ---------------------------------------------------------------------------
# GOWE_SERVER_URL must point to the head node running the GoWe server.
# GOWE_SHOCK_HOST must point to the Shock server (if remote).
# Set via environment or edit the defaults below.
export GOWE_SERVER_URL="${GOWE_SERVER_URL:?ERROR: GOWE_SERVER_URL must be set (e.g. http://head-node:8080)}"
export GOWE_SHOCK_HOST="${GOWE_SHOCK_HOST:-${GOWE_SERVER_URL##http*://}}"

DEPLOY_DIR="${DEPLOY_DIR:-$(cd "$(dirname "$0")/.." && pwd)}"

# Unique name per array element
export GOWE_WORKER_NAME="slurm-${SLURM_ARRAY_JOB_ID}-task${SLURM_ARRAY_TASK_ID}-$(hostname -s)"

# Isolate workdir per task to avoid conflicts
export GOWE_WORKDIR="${GOWE_WORKDIR:-/tmp/gowe-worker-${SLURM_ARRAY_JOB_ID}-${SLURM_ARRAY_TASK_ID}}"

# If SLURM allocated GPUs, bind each array task to its GPU
if [ -n "${CUDA_VISIBLE_DEVICES:-}" ]; then
    export GOWE_GPU=true
    export GOWE_GPU_ID="$CUDA_VISIBLE_DEVICES"
elif [ "${GOWE_GPU:-false}" = true ]; then
    # Manual GPU assignment: one GPU per array task
    export GOWE_GPU_ID="${SLURM_ARRAY_TASK_ID}"
fi

# ---------------------------------------------------------------------------
# Launch worker
# ---------------------------------------------------------------------------
exec "$DEPLOY_DIR/start-worker.sh"
