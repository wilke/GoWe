# GoWe

A CWL v1.2 workflow engine written in Go. GoWe parses Common Workflow Language definitions, schedules task execution across local, container, and remote backends, and exposes a REST API with a web UI for workflow management.

## Features

- **CWL v1.2 compliance** — 378/378 conformance tests passing
- **Multiple executor backends** — local processes, Docker/Apptainer containers, distributed workers, BV-BRC remote jobs
- **Distributed execution** — pull-based worker pools with group routing, dataset affinity, and GPU scheduling
- **Async scheduler** — tick-based with dependency resolution, scatter/gather, retry logic, and three-level state machines
- **Web UI** — dashboard, submission management, real-time SSE updates, workflow browsing
- **REST API** — JSON endpoints for workflows, submissions, tasks, workers, and file management
- **CLI client** — `gowe` command for login, submit, status, logs, cancel, and more
- **Multi-provider auth** — BV-BRC and MG-RAST tokens with per-task delegation; anonymous mode for development
- **SQLite persistence** — embedded database, zero external dependencies, automatic migrations
- **Container runtimes** — Docker and Apptainer/Singularity with SIF image support and GPU passthrough

## Requirements

- **Go 1.24+** (for building from source)
- **Docker** (for container execution and docker-compose deployment)
- **Apptainer** (optional, alternative container runtime for HPC)
- **BV-BRC account** (optional, for remote BV-BRC job submission)

## Installation

### From Source

```bash
git clone https://github.com/wilke/GoWe.git
cd GoWe
make build
```

This builds all binaries into `./bin/`:

| Binary | Description |
|--------|-------------|
| `gowe-server` | REST API server with scheduler, web UI, and executors |
| `gowe` | CLI client |
| `gowe-worker` | Distributed worker |
| `cwl-runner` | Standalone CWL runner (cwltest-compatible) |

To install to `$GOPATH/bin`:

```bash
make install
```

Or build individual binaries:

```bash
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
go build -ldflags "-X main.Version=$(git rev-parse HEAD)" -o bin/gowe-worker ./cmd/worker
go build -o bin/cwl-runner ./cmd/cwl-runner
```

### Docker Images

Build all three images:

```bash
make docker
```

Or individually:

```bash
docker build -t gowe-server -f Dockerfile .
docker build -t gowe-worker -f Dockerfile.worker .
docker build -t gowe-cwl-runner -f Dockerfile.cwl-runner .
```

Run the server standalone:

```bash
docker run -p 8080:8080 gowe-server --allow-anonymous --debug
```

With a persistent database:

```bash
docker run -p 8080:8080 -v gowe-data:/data gowe-server \
  --db /data/gowe.db --allow-anonymous
```

## Quick Start

> **Guides:**
> - [Quickstart: Local Execution](docs/quickstart-local.md) — no containers, 5 minutes
> - [Quickstart: Apptainer + Workers](docs/quickstart-apptainer.md) — distributed execution with SIF images
> - [Cookbook](docs/cookbook.md) — copy-paste recipes for common tasks
> - [CWL Hints Reference](docs/cwl-hints.md) — gowe:Execution, gowe:ResourceData, DockerRequirement
> - [Full Tutorial](docs/tutorial.md) — writing CWL, multi-step pipelines, monitoring

### 1. Start the server

```bash
gowe-server
```

Listens on `:8080` with a SQLite database at `~/.gowe/gowe.db`.

```bash
gowe-server --addr :9090 --debug --db /tmp/gowe.db --allow-anonymous
```

### 2. Register a workflow

```bash
curl -X POST http://localhost:8080/api/v1/workflows \
  -H "Content-Type: application/json" \
  -d @workflow.cwl.json
```

Or via the CLI:

```bash
gowe submit workflow.cwl -i job.json
```

### 3. Submit a run

```bash
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "my-workflow-name",
    "inputs": {
      "message": "hello world"
    }
  }'
```

`workflow_id` accepts either a workflow ID (`wf_...`) or a workflow name.

### 4. Check status

```bash
gowe status sub_...
```

Or open the web UI at `http://localhost:8080/`.

## Running with Docker Compose

Docker Compose provides a complete distributed setup with a server and multiple workers sharing a volume for inputs and outputs.

### Start the Stack

```bash
docker compose up -d --build
```

This starts:
- **gowe-server** — API server on port `8090` (maps to internal `8080`) with `--default-executor=worker`
- **worker-1, worker-2** — Host-execution workers (`--runtime=none`) for non-container tasks
- **worker-docker** — Docker-enabled worker (`--runtime=docker`) with access to the Docker socket

All services share a named volume (`gowe-workdir`) for working directories and outputs.

### Submit a Workflow

```bash
# Register and submit via the CLI
./bin/gowe submit --server http://localhost:8090 workflow.cwl -i job.json

# Or register via API
curl -X POST http://localhost:8090/api/v1/workflows \
  -H "Content-Type: application/json" \
  -d @workflow.cwl.json

# Submit
curl -X POST http://localhost:8090/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "my-workflow", "inputs": {...}}'
```

The web UI is available at `http://localhost:8090/`.

### Check Worker Status

```bash
curl -s http://localhost:8090/api/v1/workers | jq '.data'
```

### View Logs

```bash
docker compose logs -f gowe-server     # Server logs
docker compose logs -f worker-docker   # Docker worker logs
```

### Stop the Stack

```bash
docker compose down -v
```

### Customization

Create a `docker-compose.override.yml` to customize for your environment:

```yaml
services:
  gowe-server:
    command:
      - "-addr"
      - ":8080"
      - "-db"
      - "/data/gowe.db"
      - "-default-executor"
      - "worker"
      - "-allow-anonymous"
      - "-scheduler-poll"
      - "2s"
    ports:
      - "8080:8080"

  worker-docker:
    environment:
      - DOCKER_VOLUME=gowe-workdir
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - gowe-workdir:/workdir
      - /path/to/your/data:/data:ro
```

Key environment variables for the Docker worker:
- `DOCKER_VOLUME=gowe-workdir` — named volume shared between the worker and tool containers (no host path translation needed)

### Production Considerations

The default `docker-compose.yml` uses fast poll intervals (`500ms`) for testing. For production:

- Increase `--scheduler-poll` to `2s` or higher
- Increase worker `--poll` to `5s` or higher
- Remove `--debug` flags
- Set `--worker-keys` for worker authentication
- Configure `--upload-backend` for file management (`s3` or `shock`)

## Architecture

### Data Flow

```
CWL file → parser → model.Workflow (DAG of Steps)
                          ↓
            Submit with inputs → model.Submission
                          ↓
         scheduler tick loop (6 phases per tick)
                          ↓
     StepInstance (per step per submission, handles scatter)
                          ↓
              Task (concrete work unit)
                          ↓
        executor registry dispatches to backend
                          ↓
    local | docker | apptainer | worker | bvbrc
```

### Three-Level State Hierarchy

```
Submission  (PENDING → RUNNING → COMPLETED / FAILED / CANCELLED)
  └─ StepInstance  (WAITING → READY → DISPATCHED → RUNNING → COMPLETED / FAILED / SKIPPED)
       └─ Task  (PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS / FAILED)
```

Scatter steps produce N StepInstances, each with its own Tasks. The scheduler advances these through 6 phases each tick: (1) advance WAITING→READY when deps met, (2) dispatch READY→create Tasks, (3) retry FAILED tasks, (4) poll in-flight async tasks, (5) advance step instances, (6) finalize submissions.

### Executor Selection

| Priority | Condition | Executor |
|----------|-----------|----------|
| 1 | `--default-executor` server flag | As configured |
| 2 | `gowe:Execution.executor` CWL hint | `worker`, `bvbrc`, or `local` |
| 3 | `DockerRequirement` present + workers online | `worker` (auto-promoted) |
| 4 | `DockerRequirement` present, no workers | `local` (with container) |
| 5 | Default | `local` |

### Worker Pull Model

Workers poll the server for tasks (no push). Checkout matches on: container runtime capability, worker group, and dataset affinity (`prestage` = require, `cache` = prefer). Workers execute via `toolexec`, stage outputs, and report results.

## CLI

```
gowe — CWL workflow engine CLI

Commands:
  login     Authenticate with BV-BRC
  submit    Submit a CWL workflow
  run       Execute a CWL workflow (cwltest-compatible)
  status    Check workflow/submission status
  list      List workflows or submissions
  cancel    Cancel a submission
  logs      Fetch task/submission logs
  apps      List/query BV-BRC apps

Flags:
  --server      Server URL (default http://localhost:8080)
  --debug       Enable debug logging
  --log-level   Log level: debug, info, warn, error
  --log-format  Log format: text, json
```

### cwltest-Compatible Runner

```bash
# Standalone (no server needed)
cwl-runner workflow.cwl job.json

# Via server
gowe run --server http://localhost:8080 workflow.cwl job.json
```

Flags: `--outdir`, `--no-upload`, `--timeout` (default 5m), `-q/--quiet`.

## API

All endpoints are prefixed with `/api/v1`. Responses use a standard envelope:

```json
{
  "status": "success",
  "request_id": "...",
  "timestamp": "...",
  "data": { ... }
}
```

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | API discovery |
| `GET` | `/health` | Health check |
| **Workflows** | | |
| `GET` | `/workflows` | List workflows |
| `POST` | `/workflows` | Create workflow |
| `GET` | `/workflows/{id}` | Get workflow |
| `PUT` | `/workflows/{id}` | Update workflow |
| `DELETE` | `/workflows/{id}` | Delete workflow |
| `POST` | `/workflows/{id}/validate` | Validate workflow |
| **Submissions** | | |
| `GET` | `/submissions` | List submissions (`?workflow_id=&state=&search=&sort=&limit=&offset=`) |
| `POST` | `/submissions` | Create submission (`workflow_id` accepts ID or name; `?dry_run=true` for validation) |
| `GET` | `/submissions/{id}` | Get submission |
| `PUT` | `/submissions/{id}/cancel` | Cancel submission |
| `GET` | `/submissions/{id}/tasks` | List tasks |
| `GET` | `/submissions/{id}/tasks/{tid}` | Get task |
| `GET` | `/submissions/{id}/tasks/{tid}/logs` | Task logs |
| `GET` | `/sse/submissions/{id}` | SSE stream for real-time updates |
| **Workers** | | |
| `GET` | `/workers` | List workers |
| `POST` | `/workers` | Register worker |
| `PUT` | `/workers/{id}/heartbeat` | Worker heartbeat |
| `GET` | `/workers/{id}/work` | Checkout task |
| `DELETE` | `/workers/{id}` | Deregister worker |
| **Apps & Workspace** | | |
| `GET` | `/apps` | List BV-BRC apps |
| `GET` | `/apps/{appID}` | Get app details |
| `GET` | `/apps/{appID}/cwl-tool` | Generate CWL tool from app |
| `GET` | `/workspace` | List BV-BRC workspace |
| **Files** | | |
| `POST` | `/files` | Upload file |
| `GET` | `/files/download` | Download file |
| **Admin** | | |
| `GET` | `/admin/users` | List users |
| `PUT` | `/admin/users/{username}/role` | Set user role |

## Configuration

### Server Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8080` | Listen address |
| `--db` | `~/.gowe/gowe.db` | SQLite database path (`:memory:` for testing) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--log-format` | `text` | `text` or `json` |
| `--debug` | `false` | Shorthand for `--log-level=debug` |
| `--default-executor` | `""` | `local`, `docker`, `worker` |
| `--scheduler-poll` | `2s` | Scheduler tick interval |
| `--allow-anonymous` | `false` | Allow unauthenticated requests |
| `--anonymous-executors` | `local,docker,worker` | Executors allowed for anonymous users |
| `--worker-keys` | `""` | Path to worker keys JSON |
| `--image-dir` | `""` | Base directory for `.sif` image resolution |
| `--upload-backend` | `""` | `shock`, `s3`, or `local` |
| `--upload-local-dir` | `""` | Local directory for file uploads |
| `--upload-download-dirs` | `""` | Directories allowed for downloads |

### Worker Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://localhost:8080` | Server URL |
| `--name` | hostname | Worker name |
| `--group` | `default` | Worker group for task routing |
| `--worker-key` | `""` | Shared secret for server authentication |
| `--runtime` | `none` | `docker`, `apptainer`, `none` |
| `--workdir` | `$TMPDIR/gowe-worker` | Working directory |
| `--stage-out` | `local` | Output staging: `local`, `file://`, `s3://`, `shock://` |
| `--poll` | `5s` | Poll interval |
| `--image-dir` | `""` | Base directory for `.sif` images |
| `--pre-stage-dir` | `""` | Auto-scan datasets directory (bind-mounted into containers) |
| `--dataset` | `""` | Dataset alias `id=path` (repeatable) |
| `--extra-bind` | `""` | Extra bind mount (repeatable) |
| `--gpu` | `false` | Enable GPU passthrough |
| `--gpu-id` | `""` | Specific GPU device IDs |
| `--secret` | `""` | Secret env var `NAME=value` (repeatable, never sent to server) |
| `--secret-file` | `""` | Load secrets from file |

### Environment Variables

| Variable | Used By | Description |
|----------|---------|-------------|
| `GOWE_ADMINS` | Server | Comma-separated admin usernames |
| `BVBRC_TOKEN` | Server | BV-BRC authentication token |
| `AWS_ACCESS_KEY_ID` | Server/Worker | S3 access key |
| `AWS_SECRET_ACCESS_KEY` | Server/Worker | S3 secret key |
| `DOCKER_VOLUME` | Worker | Named Docker volume for tool containers (preferred for DinD) |
| `DOCKER_HOST_PATH_MAP` | Worker | Path mapping for DinD (legacy, prefer `DOCKER_VOLUME`) |
| `INPUT_PATH_MAP` | Worker | Input path translation |
| `GOWE_PATH_MAP` | CLI | Path mapping for shared-filesystem mode |

### Authentication

| Provider | Header | Token Format |
|----------|--------|--------------|
| BV-BRC | `Authorization` | `un=user@bvbrc\|tokenid=...\|sig=...` |
| MG-RAST | `X-MG-RAST-Token` | Similar pipe-delimited format |
| Anonymous | (none) | Requires `--allow-anonymous` server flag |

User tokens are delegated per-task to executors, enabling jobs to run under the submitting user's identity.

## CWL Extensions

GoWe defines namespaced CWL hints that are safely ignored by other CWL engines:

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  # Executor routing
  gowe:Execution:
    executor: worker           # local, worker, bvbrc
    worker_group: gpu-workers  # Target worker group
    bvbrc_app_id: GenomeAnnotation
    docker_image: override.sif # Override DockerRequirement

  # Dataset affinity scheduling
  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        size: 50GB
        mode: prestage         # "prestage" (require) or "cache" (prefer)
```

## Distributed Workers

### Worker Groups

Workers can be organized into groups for targeted scheduling:

```bash
gowe-worker --server http://localhost:8080 --group gpu-workers --worker-key $SECRET
```

Configure allowed groups per key:

```json
{
  "keys": {
    "secret-key-1": {"groups": ["default", "gpu-workers"]},
    "secret-key-2": {"groups": ["cpu-workers"]}
  }
}
```

### Reference Data Pre-staging

Large datasets (model weights, sequence databases) are declared by workers and matched by the scheduler:

```bash
gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --pre-stage-dir /local_databases \
  --dataset boltz_weights=/local_databases/boltz \
  --extra-bind /scratch \
  --image-dir /scout/containers/ \
  --gpu --gpu-id 3
```

- `--pre-stage-dir` — auto-scans subdirectories as datasets, bind-mounts into containers
- `--dataset` — explicit dataset aliases (additive with auto-discovered)
- `--extra-bind` — generic bind mounts (not used for scheduling)

### Secret Environment Variables

Workers inject secrets into containers without exposing them to the server or API:

```bash
gowe-worker --secret HF_TOKEN=hf_abc123 --secret-file /path/to/secrets.env
```

### Apptainer / SIF Images

```bash
gowe-worker --runtime apptainer --image-dir /path/to/sif/
```

Image resolution for `DockerRequirement.dockerPull`:
- `boltz.sif` — resolved against `--image-dir`
- `/absolute/path/boltz.sif` — used as-is
- `dxkb/boltz:latest` — prefixed with `docker://` for registry pull

## Apptainer HPC Deployment

For HPC clusters without Docker, see `deploy/apptainer/`:

```bash
# Build SIF images
deploy/apptainer/build-sif.sh

# Start services (MongoDB + Shock + GoWe server as Apptainer instances)
deploy/apptainer/start-services.sh

# Launch workers on compute nodes
deploy/apptainer/start-worker.sh

# SLURM job arrays
sbatch deploy/apptainer/slurm/array-workers.sbatch
```

## Testing

```bash
# Unit tests
make test

# CWL conformance (84 required tests)
make test-conformance

# Full suite (all tiers, all modes)
make test-all

# Tier 1 only (CI fast path: unit + cwl-runner)
make test-tier1

# Distributed mode via docker-compose
make test-distributed

# Verbose
make test-all V=1
```

### Test Tiers

| Tier | Tests | Description |
|------|-------|-------------|
| 1 | `unit`, `cwl-runner`, `cwl-runner-parallel` | Core execution (must pass) |
| 2 | `server-local`, `distributed-*` | Server modes |
| 3 | `staging-file`, `staging-s3`, `staging-shock` | Staging backends |

### CWL Conformance Results

| Mode | Result | Notes |
|------|--------|-------|
| cwl-runner (Docker) | 378/378 | Full compliance |
| cwl-runner (Apptainer) | 377/378 | 1 known limitation (network isolation requires root) |
| server-local | 376/378 | 2 unsupported (InplaceUpdate) |
| server-worker | 376/378 | 2 unsupported (InplaceUpdate) |

## Project Structure

```
cmd/
  cli/            CLI client (→ gowe)
  server/         API server (→ gowe-server)
  worker/         Distributed worker (→ gowe-worker)
  cwl-runner/     Standalone CWL runner
  gen-cwl-tools/  BV-BRC CWL tool generator
  smoke-test/     End-to-end integration test
  verify-bvbrc/   BV-BRC API verification
internal/
  parser/         CWL v1.2 parsing, validation, DAG construction
  scheduler/      Tick-based scheduler with dependency resolution
  executor/       Executor backends (local, docker, apptainer, bvbrc, worker)
  server/         HTTP handlers, routing (go-chi), middleware, auth
  store/          SQLite persistence with migrations
  worker/         Remote worker loop: poll, execute, stage, heartbeat
  cwltool/        Full CWL CommandLineTool executor
  toolexec/       Shared execution: command building, mounts, GPU, secrets
  cwlrunner/      Standalone runner (no server)
  cwlexpr/        CWL expression evaluation (goja JS engine)
  bvbrc/          BV-BRC auth and JSON-RPC 1.1 client
  bundle/         CWL file bundler
  ui/             Web UI handlers and templates
pkg/
  cwl/            CWL v1.2 type definitions
  model/          Domain models with state machines
  staging/        File staging: copy, symlink, reference modes
ui/
  assets/         Static assets (CSS, JS)
  templates/      HTML templates
deploy/
  apptainer/      Apptainer/HPC deployment scripts and SLURM jobs
docs/             Guides, tutorials, API reference
scripts/          Test runners, setup, conformance
testdata/         CWL workflow examples and conformance tests
```

## License

MIT
