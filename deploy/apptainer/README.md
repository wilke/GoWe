# GoWe Apptainer Deployment

Deploy GoWe with Shock file staging using [Apptainer](https://apptainer.org/)
(formerly Singularity). Designed for HPC clusters and bare-metal servers where
Docker is unavailable.

## Architecture

```
 Apptainer Instances (head node)         Bare Metal (compute nodes)
┌──────────────┐ ┌──────────────┐       ┌──────────────────────────┐
│ MongoDB 5.0  │ │ Shock Server │       │ gowe-worker binary       │
│ :27017       │←│ :7445        │       │ --runtime apptainer      │
└──────────────┘ └──────┬───────┘       │ --stage-out shock://...  │
                        │               │ uses `apptainer exec`    │
                 ┌──────┴───────┐       │ for CWL tool containers  │
                 │ GoWe Server  │←──────│                          │
                 │ :8080        │ poll  └──────────────────────────┘
                 └──────────────┘
                        ↑
                   CLI (bare metal)
```

- **Server, Shock, MongoDB** run as Apptainer instances (long-running daemons)
- **Workers** run on bare metal and use `apptainer exec` to run CWL tools
- **CLI** runs on bare metal (no container needed)
- All services use **host networking** (Apptainer default)
- Persistent data via **bind mounts**

## Prerequisites

- [Apptainer](https://apptainer.org/) ≥ 1.1 (`module load apptainer` on HPC)
- [Go](https://go.dev/) ≥ 1.24 (for building binaries)
- `curl` (for health checks)
- Root or fakeroot access (for building SIF images)

## Quick Start

```bash
# 1. Configure
cp gowe-stack.env.example gowe-stack.env
vi gowe-stack.env   # Adjust DATA_DIR, ports, etc.

# 2. Build SIF images and Go binaries
./build-sif.sh

# 3. Start the stack (MongoDB → Shock → GoWe Server)
./start-services.sh

# 4. Start a worker on a compute node
./start-worker.sh
```

## File Overview

```
deploy/apptainer/
├── gowe-server.def          # Apptainer definition: GoWe server
├── mongodb.def              # Apptainer definition: MongoDB 5.0
├── shock-server.def         # Apptainer definition: Shock data service
├── build-sif.sh             # Build all SIF images
├── start-services.sh        # Start MongoDB + Shock + GoWe server
├── stop-services.sh         # Graceful shutdown
├── start-worker.sh          # Launch a worker on a compute node
├── gowe-stack.env.example   # Configuration template
├── README.md                # This file
└── slurm/
    ├── worker.sbatch        # SLURM job: single worker
    └── array-workers.sbatch # SLURM job array: multiple workers
```

## Building SIF Images

```bash
# Build everything (compiles Go binaries + builds SIF files)
./build-sif.sh

# Build server SIF only (skips Shock/MongoDB)
./build-sif.sh --server

# Build Shock + MongoDB SIFs only
./build-sif.sh --shock

# Skip Go compilation (use pre-built binaries in bin/)
./build-sif.sh --no-build
```

Output goes to `./sif/` (override with `SIF_DIR`):

```
sif/
├── gowe-server.sif
├── mongodb.sif
└── shock-server.sif
```

### Shock Image Sources

`build-sif.sh` tries three strategies in order:

1. Convert from local Docker daemon: `docker-daemon://shock-shock-server:latest`
2. Pull from Docker Hub: `docker://mgrast/shock-server:latest`
3. Build from definition file: `shock-server.def`

If none work, build the Shock Docker image first from
[MG-RAST/Shock](https://github.com/MG-RAST/Shock).

## Configuration

Copy and edit the environment file:

```bash
cp gowe-stack.env.example gowe-stack.env
```

Key settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `SIF_DIR` | `./sif` | Built SIF images location |
| `DATA_DIR` | `/data/gowe` | Persistent data root |
| `SERVER_PORT` | `8080` | GoWe API port |
| `SHOCK_PORT` | `7445` | Shock API port |
| `MONGO_PORT` | `27017` | MongoDB port |
| `GOWE_RUNTIME` | `apptainer` | Worker container runtime |
| `GOWE_WORKDIR` | `/tmp/gowe-worker` | Worker scratch directory |
| `SHOCK_TOKEN` | _(empty)_ | Shock auth token |
| `GOWE_GPU` | `false` | Enable GPU passthrough |
| `GOWE_GPU_ID` | _(empty)_ | Specific GPU device(s) |

## Managing Services

### Start

```bash
# Full stack
./start-services.sh

# Server only (Shock/MongoDB are external)
./start-services.sh --server-only

# Custom env file
./start-services.sh --env /etc/gowe/production.env
```

### Stop

```bash
./stop-services.sh
```

### Status

```bash
apptainer instance list
```

## Running Workers

Workers run on bare metal and use Apptainer to execute CWL tool containers.

### Direct Launch

```bash
# Basic
./start-worker.sh

# With GPU
./start-worker.sh --gpu --gpu-id 0

# Remote server
./start-worker.sh --server http://head-node:8080 --name compute-01
```

### Multiple Workers (Manual)

```bash
# One worker per GPU on a multi-GPU node
for gpu in 0 1 2 3; do
    GOWE_WORKER_NAME="gpu-$gpu" \
    GOWE_WORKDIR="/scratch/gowe/worker-$gpu" \
        ./start-worker.sh --gpu --gpu-id "$gpu" &
done
```

### SLURM

```bash
# Single worker
sbatch slurm/worker.sbatch

# 4 workers (job array)
sbatch slurm/array-workers.sbatch

# 8 GPU workers
sbatch --array=0-7 --gres=gpu:1 slurm/array-workers.sbatch
```

Override settings via environment:

```bash
DEPLOY_DIR=/opt/gowe/deploy/apptainer \
GOWE_SERVER_URL=http://head-node:8080 \
    sbatch slurm/array-workers.sbatch
```

## Persistent Data Layout

All data lives under `$DATA_DIR` (default: `/data/gowe`):

```
/data/gowe/
├── mongo/       # MongoDB data files
├── shock/
│   ├── data/    # Shock node storage
│   └── logs/    # Shock server logs
└── server/
    └── gowe.db  # SQLite database
```

## GPU Support

Workers pass `--nv` to `apptainer exec` for NVIDIA GPU passthrough:

```bash
# All GPUs
./start-worker.sh --gpu

# Specific GPU
./start-worker.sh --gpu --gpu-id 0

# Multiple GPUs
./start-worker.sh --gpu --gpu-id 0,1
```

On SLURM, `CUDA_VISIBLE_DEVICES` is automatically detected from the
allocation and passed through.

## Port Conflicts

All ports are configurable. When multiple users share a login node,
choose unique ports:

```bash
# In gowe-stack.env
MONGO_PORT=28017
SHOCK_PORT=8445
SERVER_PORT=9080
INSTANCE_PREFIX=myuser-gowe   # Unique instance names
```

## Makefile Integration

From the project root:

```bash
make apptainer        # Build SIF images
make apptainer-up     # Start stack
make apptainer-down   # Stop stack
```

## Troubleshooting

### Check instance logs

```bash
# GoWe server
apptainer instance list
apptainer exec instance://gowe-server cat /data/gowe.log

# Shock
curl -s http://localhost:7445/ | python3 -m json.tool
```

### Verify worker registration

```bash
curl -s http://localhost:8080/api/v1/workers/ | python3 -m json.tool
```

### MongoDB health

```bash
apptainer exec instance://gowe-mongo mongo --eval "db.adminCommand('ping')"
```

### Worker can't find apptainer

On HPC, load the module before starting:

```bash
module load apptainer
./start-worker.sh
```

The SLURM scripts do this automatically.
