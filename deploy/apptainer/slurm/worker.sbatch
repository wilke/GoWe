#!/bin/bash
# worker.sbatch — Launch a single GoWe worker via SLURM.
#
# Usage:
#   sbatch worker.sbatch
#   sbatch --partition=gpu --gres=gpu:1 worker.sbatch
#
# Override defaults with environment variables:
#   DEPLOY_DIR=/opt/gowe/deploy/apptainer sbatch worker.sbatch

#SBATCH --job-name=gowe-worker
#SBATCH --output=gowe-worker-%j.out
#SBATCH --error=gowe-worker-%j.err
#SBATCH --time=24:00:00
#SBATCH --nodes=1
#SBATCH --ntasks=1

# ---------------------------------------------------------------------------
# Load Apptainer module (HPC-specific; no-op if unavailable)
# ---------------------------------------------------------------------------
module load apptainer 2>/dev/null || true

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
DEPLOY_DIR="${DEPLOY_DIR:-$(cd "$(dirname "$0")/.." && pwd)}"

# Give the worker a unique name from SLURM metadata
export GOWE_WORKER_NAME="slurm-${SLURM_JOB_ID}-$(hostname -s)"

# Isolate workdir per job to avoid conflicts
export GOWE_WORKDIR="${GOWE_WORKDIR:-/tmp/gowe-worker-${SLURM_JOB_ID}}"

# If SLURM allocated a GPU, enable it
if [ -n "${CUDA_VISIBLE_DEVICES:-}" ]; then
    export GOWE_GPU=true
    export GOWE_GPU_ID="$CUDA_VISIBLE_DEVICES"
fi

# ---------------------------------------------------------------------------
# Launch worker
# ---------------------------------------------------------------------------
exec "$DEPLOY_DIR/start-worker.sh"
