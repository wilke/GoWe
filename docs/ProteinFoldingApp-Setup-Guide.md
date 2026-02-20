# ProteinFoldingApp Setup Guide

This guide walks through setting up GoWe and running the ProteinFoldingApp experiments on an 8-GPU machine with Apptainer.

## Prerequisites

- 8 NVIDIA GPUs with CUDA support
- Apptainer (Singularity) installed with `--nv` support
- ~3TB storage for genetic databases
- ~100GB storage for container images
- ~500GB scratch space for working files

Verify GPU access:
```bash
nvidia-smi
# Should show 8 GPUs

apptainer exec --nv docker://nvidia/cuda:12.0-base nvidia-smi
# Should show GPUs inside container
```

## Step 1: Install GoWe

```bash
# Clone and build
git clone https://github.com/wilke/GoWe.git
cd GoWe
git checkout v0.10.1

# Build binaries
mkdir -p bin
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe-worker ./cmd/worker
go build -o bin/gowe ./cmd/cli

# Add to PATH
export PATH="$PWD/bin:$PATH"

# Verify
gowe-server --help
gowe-worker --help
```

## Step 2: Clone ProteinFoldingApp

```bash
cd ~
git clone https://github.com/wilke/ProteinFoldingApp.git
cd ProteinFoldingApp
```

## Step 3: Pull Container Images

Convert Docker images to Apptainer SIF format (faster startup, offline use):

```bash
mkdir -p ~/containers
cd ~/containers

# Prediction tools (GPU required)
apptainer pull alphafold.sif docker://wilke/alphafold:latest
apptainer pull boltz.sif docker://dxkb/boltz:latest
apptainer pull chai.sif docker://dxkb/chai:latest
apptainer pull esmfold.sif docker://dxkb/esmfold:latest

# MSA tools (CPU only)
apptainer pull mmseqs2.sif docker://staphb/mmseqs2:latest
apptainer pull hmmer.sif docker://staphb/hmmer:latest

# Utility
apptainer pull python.sif docker://python:3.11-slim

# Build protein-compare (if not available)
# cd ~/ProteinFoldingApp/docker/protein-compare
# apptainer build protein-compare.sif Singularity.def
```

## Step 4: Stage Genetic Databases

AlphaFold2 requires large genetic databases (~2.5TB):

```bash
# Create database directory
sudo mkdir -p /data/alphafold-databases
sudo chown $USER:$USER /data/alphafold-databases

# Download databases (this takes hours)
# Option 1: Use AlphaFold's download script
# Option 2: Rsync from existing installation
rsync -av --progress source:/path/to/alphafold-db/ /data/alphafold-databases/

# ColabFold database for Boltz/Chai (~500GB)
sudo mkdir -p /data/colabfold-db
# Download from https://colabfold.mmseqs.com/
```

Verify database structure:
```bash
ls /data/alphafold-databases/
# Should contain: bfd/, mgnify/, params/, pdb70/, uniclust30/, uniref90/
```

## Step 5: Create Directory Structure

```bash
# Working directories
sudo mkdir -p /scratch/gowe
sudo chown -R $USER:$USER /scratch/gowe

# Results directory
sudo mkdir -p /results/protein-folding
sudo chown -R $USER:$USER /results

# GoWe database
mkdir -p ~/.gowe

# Log directory
sudo mkdir -p /var/log/gowe
sudo chown $USER:$USER /var/log/gowe
```

## Step 6: Start GoWe Cluster

### Option A: Manual Start

```bash
# Terminal 1: Start server
gowe-server \
  --addr 0.0.0.0:8080 \
  --allow-anonymous \
  --default-executor worker \
  --db ~/.gowe/gowe.db \
  --debug

# Terminal 2: Start workers (run this script)
for GPU_ID in {0..7}; do
  gowe-worker \
    --server http://localhost:8080 \
    --runtime apptainer \
    --gpu \
    --gpu-id "$GPU_ID" \
    --name "gpu-worker-$GPU_ID" \
    --group "gpu-workers" \
    --workdir "/scratch/gowe/worker-$GPU_ID" \
    --stage-out "file:///results/protein-folding" \
    --poll 2s \
    --debug &
  echo "Started worker $GPU_ID"
done
```

### Option B: Startup Script

Save as `~/start-gowe-cluster.sh`:

```bash
#!/bin/bash
set -e

SERVER_URL="http://localhost:8080"
WORKDIR="/scratch/gowe"
RESULTS="/results/protein-folding"
LOG_DIR="/var/log/gowe"
GOWE_BIN="$HOME/GoWe/bin"

export PATH="$GOWE_BIN:$PATH"

# Clean up old workers
pkill -f gowe-worker || true
pkill -f gowe-server || true
sleep 2

echo "Starting GoWe server..."
$GOWE_BIN/gowe-server \
  --addr 0.0.0.0:8080 \
  --allow-anonymous \
  --default-executor worker \
  --db ~/.gowe/gowe.db \
  > "$LOG_DIR/server.log" 2>&1 &

sleep 3

# Verify server is up
curl -sf "$SERVER_URL/api/v1/health" > /dev/null || {
  echo "Server failed to start. Check $LOG_DIR/server.log"
  exit 1
}
echo "Server started"

echo "Starting 8 GPU workers..."
for GPU_ID in {0..7}; do
  mkdir -p "$WORKDIR/worker-$GPU_ID"

  $GOWE_BIN/gowe-worker \
    --server "$SERVER_URL" \
    --runtime apptainer \
    --gpu \
    --gpu-id "$GPU_ID" \
    --name "gpu-worker-$GPU_ID" \
    --group "gpu-workers" \
    --workdir "$WORKDIR/worker-$GPU_ID" \
    --stage-out "file://$RESULTS" \
    --poll 2s \
    > "$LOG_DIR/worker-$GPU_ID.log" 2>&1 &

  echo "  Started gpu-worker-$GPU_ID on GPU $GPU_ID"
done

sleep 5

echo ""
echo "Cluster status:"
curl -s "$SERVER_URL/api/v1/workers" | jq -r '.data[] | "  \(.name): \(.state)"'
echo ""
echo "Server URL: $SERVER_URL"
echo "Logs: $LOG_DIR/"
echo ""
echo "Ready to submit workflows!"
```

Make executable and run:
```bash
chmod +x ~/start-gowe-cluster.sh
~/start-gowe-cluster.sh
```

## Step 7: Verify Cluster

```bash
# Check server health
curl http://localhost:8080/api/v1/health | jq

# List workers (should show 8)
curl http://localhost:8080/api/v1/workers | jq '.data | length'

# Show worker details
curl http://localhost:8080/api/v1/workers | jq '.data[] | {name, state, runtime}'
```

## Step 8: Prepare Experiment Data

```bash
cd ~/ProteinFoldingApp

# Download target sequences and experimental structures
python scripts/prepare_targets.py --config configs/targets_pilot.yaml

# Generate MSAs (optional, can be done on-demand)
python scripts/generate_msas.py \
  --sequences data/sequences/ \
  --output data/alignments/ \
  --database /data/colabfold-db
```

## Step 9: Run Experiments

### Test with Single Prediction

```bash
cd ~/ProteinFoldingApp

# Run AlphaFold2 on one target
gowe run \
  --server http://localhost:8080 \
  cwl/tools/alphafold-predict.cwl \
  testdata/single-target-job.yml
```

### Run Experiment 1 (Within-Tool Quality)

```bash
# Submit the workflow
gowe submit \
  --server http://localhost:8080 \
  cwl/workflows/experiment1-within-tool.cwl \
  inputs/experiment1-inputs.yml

# Check status
gowe list --server http://localhost:8080

# Monitor a specific submission
gowe status --server http://localhost:8080 sub_XXXXX
```

### Run All Experiments

```bash
# Experiment 1: Within-tool quality assessment
gowe submit cwl/workflows/experiment1-within-tool.cwl inputs/exp1.yml

# Experiment 2: Cross-tool comparison
gowe submit cwl/workflows/experiment2-across-tools.cwl inputs/exp2.yml

# Experiment 3: MSA impact analysis
gowe submit cwl/workflows/experiment3-msa-impact.cwl inputs/exp3.yml

# Experiment 4: MSA depth sensitivity
gowe submit cwl/workflows/experiment4-msa-depth.cwl inputs/exp4.yml
```

## Step 10: Monitor Progress

```bash
# Watch server logs
tail -f /var/log/gowe/server.log

# Watch a specific worker
tail -f /var/log/gowe/worker-0.log

# List all submissions
curl http://localhost:8080/api/v1/submissions | jq '.data[] | {id, state, workflow_id}'

# Get task details for a submission
curl http://localhost:8080/api/v1/submissions/sub_XXXXX/tasks | jq
```

## Step 11: Collect Results

```bash
cd ~/ProteinFoldingApp

# Results are staged to /results/protein-folding/
ls /results/protein-folding/

# Aggregate metrics
python scripts/collect_metrics.py \
  --results /results/protein-folding \
  --output results/metrics.csv

# Generate plots
python scripts/plot_results.py \
  --metrics results/metrics.csv \
  --output results/figures/
```

## Troubleshooting

### Workers not receiving tasks

```bash
# Check worker registration
curl http://localhost:8080/api/v1/workers | jq

# Check worker logs
tail -100 /var/log/gowe/worker-0.log

# Verify GPU access
apptainer exec --nv docker://nvidia/cuda:12.0-base nvidia-smi
```

### GPU out of memory

```bash
# Check GPU memory usage
nvidia-smi

# Clear GPU memory (kills all GPU processes)
sudo fuser -v /dev/nvidia* 2>/dev/null | xargs -r sudo kill -9

# Reduce batch size in CWL tool inputs
```

### Container not found

```bash
# Pull missing container
apptainer pull docker://IMAGE_NAME

# Or use pre-built SIF
export APPTAINER_CACHEDIR=~/containers
```

### Database mount issues

```bash
# Verify database path exists
ls /data/alphafold-databases/

# Test mount in container
apptainer exec --nv \
  --bind /data/alphafold-databases:/data \
  docker://wilke/alphafold:latest \
  ls /data/
```

## Stopping the Cluster

```bash
# Graceful shutdown
pkill -SIGTERM -f gowe-worker
pkill -SIGTERM -f gowe-server

# Force kill if needed
pkill -9 -f gowe-worker
pkill -9 -f gowe-server
```

## Quick Reference

| Command | Description |
|---------|-------------|
| `~/start-gowe-cluster.sh` | Start server + 8 workers |
| `curl localhost:8080/api/v1/health` | Check server health |
| `curl localhost:8080/api/v1/workers \| jq` | List workers |
| `gowe submit workflow.cwl job.yml` | Submit workflow |
| `gowe status sub_XXX` | Check submission status |
| `gowe list` | List all submissions |
| `tail -f /var/log/gowe/server.log` | Watch server logs |
| `nvidia-smi` | Check GPU status |

## Environment Variables

Add to `~/.bashrc`:

```bash
export PATH="$HOME/GoWe/bin:$PATH"
export GOWE_SERVER="http://localhost:8080"

# Apptainer settings
export APPTAINER_CACHEDIR="$HOME/containers"
export APPTAINER_TMPDIR="/scratch/apptainer-tmp"

# CUDA (if needed)
export CUDA_HOME="/usr/local/cuda"
export PATH="$CUDA_HOME/bin:$PATH"
export LD_LIBRARY_PATH="$CUDA_HOME/lib64:$LD_LIBRARY_PATH"
```
