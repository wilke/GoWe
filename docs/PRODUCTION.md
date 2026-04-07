# Production Deployment — coconut

GoWe production setup on `coconut`, an 8x NVIDIA H200 NVL workstation.

## Machine Specs

| Resource | Value |
|----------|-------|
| Hostname | `coconut` |
| CPUs | 384 cores |
| Memory | 1.5 TiB |
| GPUs | 8x NVIDIA H200 NVL (143 GB each) |
| OS | Ubuntu, kernel 6.8.0-94-generic |
| Container runtime | Apptainer 1.4.5 |
| Go | Not natively installed; compiled via `apptainer exec docker://golang:1.24` |

## Directory Layout

```
/scout/
├── Experiments/GoWe/          # Source code + binaries
│   ├── bin/                   # Compiled binaries (symlinked to versioned builds)
│   └── scripts/               # Start/stop/conformance scripts
├── containers/                # SIF container images
│   ├── folding_prod.sif       # Production protein folding tools
│   ├── folding_compare_prod.sif
│   ├── alphafold.sif
│   ├── boltz.sif
│   ├── chai.sif
│   ├── esmfold.sif
│   ├── hmmer.sif
│   ├── mmseqs2.sif
│   └── python.sif
└── wf/                        # Production runtime data
    ├── gowe/
    │   ├── gowe.db            # SQLite database (WAL mode)
    │   ├── logs/              # Server and worker logs
    │   ├── pids/              # PID files for start/stop scripts
    │   ├── uploads/           # File upload storage (local backend)
    │   ├── workdir/           # Per-worker working directories
    │   │   ├── worker-1/
    │   │   ├── worker-2/
    │   │   ├── worker-3/
    │   │   └── worker-gpu/
    │   └── secrets.env        # HuggingFace tokens (mode 600)
    └── data/                  # Staged output files

/local_databases/              # Pre-staged reference datasets
├── alphafold/                 # AlphaFold model weights + databases
├── boltz/                     # Boltz model weights
└── chai/                      # Chai model weights
```

## Starting the Server

### Quick Start

```bash
cd /scout/Experiments/GoWe
./scripts/start-server.sh
```

This starts 1 server + 2 workers with default settings.

### Configuration

The start script reads environment variables (all have defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `GOWE_PORT` | `8091` | Server listen port |
| `BASE_DIR` | `/scout/wf` | Root for database, logs, uploads, workdirs |
| `IMAGE_DIR` | `/scout/containers` | SIF image directory |
| `PRE_STAGE_DIR` | `/local_databases` | Pre-staged reference data |
| `NUM_WORKERS` | `2` | Number of workers to start |
| `LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `ADMINS` | `awilke,awilke@bvbrc,olson,olson@bvbrc` | Admin usernames |

Example with overrides:

```bash
NUM_WORKERS=4 LOG_LEVEL=debug ./scripts/start-server.sh
```

### What the Start Script Does

1. Creates required directories under `$BASE_DIR/gowe/`
2. Starts `gowe-server` on port `$GOWE_PORT` with:
   - `--default-executor worker` (routes all work through workers)
   - `--allow-anonymous` with executors `local,docker,worker,container`
   - `--scheduler-poll 100ms`
   - `--upload-backend local` with uploads in `$BASE_DIR/gowe/uploads`
   - `--workspace-staging server` (server-side ws:// staging)
3. Waits for health check to pass
4. Starts `$NUM_WORKERS` workers, each with:
   - `--runtime apptainer`
   - `--gpu --gpu-id $i` (GPU IDs start at 1; GPU 0 is reserved)
   - `--image-dir /scout/containers`
   - `--pre-stage-dir /local_databases`
   - `--workspace-stager` enabled
   - `--stage-out file:///scout/wf/data`
   - `--poll 500ms`
5. Writes PIDs to `$BASE_DIR/gowe/pids/`
6. Logs to `$BASE_DIR/gowe/logs/`

## Stopping the Server

```bash
./scripts/stop-server.sh
```

Sends SIGTERM to workers first (lets them finish current tasks), waits 2 seconds for deregistration, then stops the server. Force-kills after 10 seconds if processes don't exit.

## Health Check

```bash
curl -s http://localhost:8091/api/v1/health | python3 -m json.tool
```

Returns executor availability, worker summary (online/offline counts, runtimes, groups), and uptime.

## Secrets

Worker secrets live in `/scout/wf/gowe/secrets.env` (mode `600`, never committed):

```
# HuggingFace Hub authentication
HUGGING_FACE_HUB_TOKEN=<token>
HF_TOKEN=<token>
```

Workers load these via `--secret-file`. Secret values are injected into containers at runtime and never sent to the server, stored in task data, or exposed in API responses or logs.

## GPU Assignment

GPU 0 is reserved for interactive/other use. The start script assigns workers to GPUs starting at index 1:

| Worker | GPU ID | Device |
|--------|--------|--------|
| worker-1 | 1 | H200 NVL |
| worker-2 | 2 | H200 NVL |
| worker-3 | 3 | H200 NVL |
| ... | ... | ... |

Up to 7 GPU workers can run simultaneously (GPUs 1-7).

## Building Binaries

Go is not installed natively. All builds run through Apptainer:

```bash
# Build all binaries with version tags
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "make dev"

# Update symlinks to the new build
DEV_TAG=$(ls -t bin/gowe-server-* | head -1 | sed 's|bin/gowe-server-||')
cd bin
ln -sf gowe-server-$DEV_TAG gowe-server
ln -sf gowe-$DEV_TAG gowe
ln -sf gowe-worker-$DEV_TAG gowe-worker
ln -sf cwl-runner-$DEV_TAG cwl-runner
```

Or use the `make build` target which produces unversioned binaries directly:

```bash
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "make build"
```

## Log Locations

| Log | Path |
|-----|------|
| Server | `/scout/wf/gowe/logs/server.log` |
| Worker N | `/scout/wf/gowe/logs/worker-N.log` |

For debug-level logging, set `LOG_LEVEL=debug` before starting.

## Database

SQLite in WAL mode at `/scout/wf/gowe/gowe.db`. Single-writer, no external database server needed. Back up by copying the file while the server is stopped, or use `.backup` via the SQLite CLI.

## Troubleshooting

**Server won't start — port already in use:**
```bash
pgrep -af 'gowe-server'       # Find existing processes
./scripts/stop-server.sh       # Graceful stop
```

**Worker fails to register:**
Check the worker log for connectivity issues:
```bash
tail -50 /scout/wf/gowe/logs/worker-1.log
```

**Container image not found:**
Verify the SIF image exists in `/scout/containers/` and the `--image-dir` flag is set.

**GPU not available to worker:**
```bash
nvidia-smi                     # Verify GPU visibility
apptainer exec --nv docker://nvidia/cuda:12.0-base nvidia-smi  # Test inside container
```

**Database locked:**
GoWe uses `max_open_conns=1` with WAL mode. If the database reports "locked", ensure only one server instance is running against the same `.db` file.
