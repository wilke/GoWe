# GoWe

A CWL v1.2 workflow engine for [BV-BRC](https://www.bv-brc.org/) bioinformatics pipelines. GoWe parses Common Workflow Language definitions, schedules task execution across local, container, and BV-BRC backends, and exposes a REST API for workflow management.

## Features

- **CWL v1.2 support** — parse, validate, and execute packed or modular CWL workflows
- **Multiple executor backends** — local processes, Docker containers, distributed workers, and BV-BRC remote jobs
- **Multi-provider authentication** — BV-BRC and MG-RAST tokens with per-task delegation
- **Distributed execution** — worker pools with group-based scheduling and shared secrets
- **Async scheduling** — tick-based scheduler with dependency resolution, retry logic, and state machine transitions
- **SQLite persistence** — lightweight embedded database with automatic migrations
- **REST API** — JSON endpoints for workflow CRUD, submission management, task monitoring, and BV-BRC app/workspace proxying
- **CLI client** — `gowe` command for login, submit, status, logs, and more

## Requirements

- Go 1.24+
- Docker or Apptainer (optional, for container executor — auto-detected from PATH)
- BV-BRC account (optional, for remote job submission)

## Installation

```bash
go install github.com/me/gowe/cmd/server@latest
go install github.com/me/gowe/cmd/cli@latest
```

Or build from source:

```bash
git clone https://github.com/wilke/GoWe.git
cd GoWe
mkdir -p bin
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
```

### Docker

```bash
docker build -t gowe .
docker run -p 8080:8080 gowe
```

## Quick Start

**1. Start the server**

```bash
gowe-server
```

The server listens on `:8080` by default with a SQLite database at `~/.gowe/gowe.db`.

```
gowe-server --addr :9090 --debug --db /tmp/gowe.db
```

**2. Register a workflow**

```bash
curl -X POST http://localhost:8080/api/v1/workflows \
  -H "Content-Type: application/json" \
  -d @testdata/packed/pipeline-packed.cwl
```

**3. Submit a run**

```bash
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "wf_...",
    "inputs": {
      "reads_r1": "/path/to/reads_R1.fastq",
      "reads_r2": "/path/to/reads_R2.fastq",
      "scientific_name": "Escherichia coli",
      "taxonomy_id": 562
    }
  }'
```

**4. Check status**

```bash
gowe status sub_...
```

## CLI

```
gowe — CWL workflow engine for BV-BRC

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

The `gowe run` command provides a cwl-runner compatible interface:

```bash
gowe run workflow.cwl job.yml
```

This bundles the CWL, submits to the server, waits for completion, and outputs CWL-formatted JSON to stdout.

**Flags:**
- `--outdir` — Output directory (default: temporary directory)
- `--no-upload` — Disable file upload; use `GOWE_PATH_MAP` for shared-filesystem mode
- `--timeout` — Execution timeout (default: 5m)
- `-q, --quiet` — Suppress progress messages

## API

All endpoints are prefixed with `/api/v1`.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | API discovery |
| `GET` | `/health` | Health check |
| `GET` | `/workflows` | List workflows |
| `POST` | `/workflows` | Create workflow |
| `GET` | `/workflows/{id}` | Get workflow |
| `PUT` | `/workflows/{id}` | Update workflow |
| `DELETE` | `/workflows/{id}` | Delete workflow |
| `POST` | `/workflows/{id}/validate` | Validate workflow |
| `GET` | `/submissions` | List submissions |
| `POST` | `/submissions` | Create submission |
| `GET` | `/submissions/{id}` | Get submission |
| `PUT` | `/submissions/{id}/cancel` | Cancel submission |
| `GET` | `/submissions/{id}/tasks` | List tasks |
| `GET` | `/submissions/{id}/tasks/{tid}` | Get task |
| `GET` | `/submissions/{id}/tasks/{tid}/logs` | Task logs |
| `GET` | `/apps` | List BV-BRC apps |
| `GET` | `/apps/{appID}` | Get app details |
| `GET` | `/workspace` | List workspace |

All responses use a standard envelope:

```json
{
  "status": "success",
  "request_id": "...",
  "timestamp": "...",
  "data": { ... }
}
```

## Executors

GoWe supports four execution backends, selected per-step via CWL hints:

| Type | Key | Description |
|------|-----|-------------|
| `local` | Default | Runs commands as local OS processes |
| `docker` | `DockerRequirement` or `goweHint.docker_image` | Runs commands inside Docker containers |
| `worker` | `goweHint.executor: worker` or `--default-executor=worker` | Delegates to remote workers for distributed execution |
| `bvbrc` | `goweHint.executor: bvbrc` | Submits jobs to BV-BRC via JSON-RPC 1.1 |

### Authentication

GoWe supports multi-provider authentication for API requests:

| Provider | Header | Token Format |
|----------|--------|--------------|
| BV-BRC | `Authorization` | `un=user@bvbrc\|tokenid=...\|expiry=...\|sig=...` |
| MG-RAST | `X-MG-RAST-Token` | Similar pipe-delimited format |
| Anonymous | (none) | Requires `--allow-anonymous` flag |

User tokens are delegated per-task to executors and workers, enabling jobs to run under the submitting user's identity.

**Server-side BV-BRC token** (for `/apps` and `/workspace` endpoints):

1. `BVBRC_TOKEN` environment variable
2. `~/.gowe/credentials.json` (via `gowe login`)
3. `~/.bvbrc_token`
4. `~/.patric_token`
5. `~/.p3_token`

If no server token is found, BV-BRC proxy endpoints are disabled.

### CWL Hints Example

```yaml
steps:
  annotate:
    run: tools/bvbrc-annotation.cwl
    hints:
      goweHint:
        executor: bvbrc
        bvbrc_app_id: GenomeAnnotation
    in:
      contigs: assemble/contigs
    out: [annotated_genome]

  local_step:
    run: tools/echo.cwl
    hints:
      DockerRequirement:
        dockerPull: ubuntu:22.04
    in:
      message: input_msg
    out: [output]
```

## Configuration

### Quick Setup

For development and testing, use the setup script to initialize your environment:

```bash
# Initialize environment (creates .env with auto-detected paths)
./scripts/setup-env.sh

# Setup and build binaries
./scripts/setup-env.sh -b

# Source environment before running
source .env
```

### Configuration Files

```
~/.gowe/                      # User configuration directory
├── gowe.db                   # SQLite database
└── worker-keys.json          # Worker authentication keys

$PROJECT/
├── .env                      # Local environment (gitignored)
├── .env.example              # Environment template
└── configs/                  # Configuration templates (reference only)
    ├── server.example.yaml
    ├── worker.example.yaml
    └── credentials.example.yaml
```

> **Note:** YAML config file loading (`--config` for full server/worker configuration) is not yet implemented.
> The example YAML files in `configs/` are design references. See [docs/planned-config.md](docs/planned-config.md) for the planned configuration system.

### Server Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8080` | Listen address |
| `--log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--log-format` | `text` | Log format (`text` or `json`) |
| `--db` | `~/.gowe/gowe.db` | SQLite database path |
| `--default-executor` | `""` | Default executor: `local`, `docker`, `worker` |
| `--debug` | `false` | Shorthand for `--log-level=debug` |
| `--allow-anonymous` | `false` | Allow unauthenticated requests |
| `--anonymous-executors` | `local,docker,worker` | Executors for anonymous users |
| `--worker-keys` | `""` | Path to worker keys JSON file |
| `--config` | `""` | Path to admin config file (JSON admin list only) |
| `--scheduler-poll` | `2s` | Scheduler poll interval |
| `--upload-backend` | `""` | File upload backend: `shock`, `s3`, `local` |
| `--upload-local-dir` | `""` | Local directory for file uploads |
| `--upload-download-dirs` | `""` | Directories allowed for file download |

### Worker Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://localhost:8080` | Server URL |
| `--name` | hostname | Worker name |
| `--group` | `default` | Worker group |
| `--worker-key` | `""` | Shared secret for authentication |
| `--runtime` | `none` | Runtime: `docker`, `apptainer`, `none` |
| `--workdir` | `$TMPDIR/gowe-worker` | Working directory |
| `--stage-out` | `local` | Output staging: `local`, `file://`, `s3://`, `shock://` |
| `--poll` | `5s` | Poll interval |
| `--stage-mode` | `copy` | File staging mode (`copy`, `symlink`, `reference`) |
| `--docker-host-path-map` | `""` | Path mapping for DinD (or `DOCKER_HOST_PATH_MAP` env) |
| `--docker-volume` | `""` | Named Docker volume shared with tool containers (or `DOCKER_VOLUME` env) |
| `--input-path-map` | `""` | Input path mapping (or `INPUT_PATH_MAP` env) |

### Environment Variables

These environment variables are actually read by the application code (not just test scripts):

| Variable | Used By | Description |
|----------|---------|-------------|
| `GOWE_ADMINS` | Server | Comma-separated admin usernames |
| `BVBRC_TOKEN` | Server | BV-BRC authentication token (also checks `~/.bvbrc_token` etc.) |
| `AWS_ACCESS_KEY_ID` | Server, Worker | S3 access key (fallback when flag is empty) |
| `AWS_SECRET_ACCESS_KEY` | Server, Worker | S3 secret key (fallback when flag is empty) |
| `DOCKER_HOST_PATH_MAP` | Worker | Path mapping for DinD — legacy approach, prefer `DOCKER_VOLUME` |
| `DOCKER_VOLUME` | Worker | Named Docker volume for tool containers (preferred for DinD) |
| `INPUT_PATH_MAP` | Worker | Input path translation (fallback when flag is empty) |
| `SHOCK_TOKEN` | Worker | Shock authentication token (fallback when flag is empty) |
| `GOWE_PATH_MAP` | CLI (run) | Path mapping for shared-filesystem distributed mode |
| `GOWE_OUTPUT_PATH_MAP` | CLI (run) | Output path translation for distributed mode |

Test script environment variables (set in `.env` via `setup-env.sh`):

| Variable | Description |
|----------|-------------|
| `GOWE_PROJECT_ROOT` | Project root directory |
| `GOWE_TESTDATA` | Test data directory |
| `GOWE_CONFORMANCE_DIR` | CWL conformance tests directory |

### Admin Configuration

Admins can be designated via (checked in order):
1. **Database** — `gowe-server grant-admin alice@bvbrc`
2. **Environment** — `GOWE_ADMINS=alice@bvbrc,bob@mgrast`
3. **Config file** — admin list in JSON file passed via `--config`

## Tools

GoWe includes several command-line tools in `cmd/`:

| Tool | Description | Documentation |
|------|-------------|---------------|
| `server` | Main API server with scheduler and executors | [docs/tools/server.md](docs/tools/server.md) |
| `cli` | CLI client for workflow submission and monitoring | [docs/tools/cli.md](docs/tools/cli.md) |
| `worker` | Remote worker for distributed task execution | [docs/tools/worker.md](docs/tools/worker.md) |
| `gen-cwl-tools` | Generates CWL tools from BV-BRC app definitions | [docs/tools/gen-cwl-tools.md](docs/tools/gen-cwl-tools.md) |
| `smoke-test` | End-to-end API integration test | [docs/tools/smoke-test.md](docs/tools/smoke-test.md) |
| `verify-bvbrc` | BV-BRC API connectivity verification | [docs/tools/verify-bvbrc.md](docs/tools/verify-bvbrc.md) |
| `scheduler` | Standalone scheduler (placeholder) | — |

Build all tools:

```bash
go build -o bin/ ./cmd/...
```

## Project Structure

```
cmd/
  cli/          CLI client entrypoint
  gen-cwl-tools/ CWL tool generator for BV-BRC apps
  scheduler/    Standalone scheduler (placeholder)
  server/       API server entrypoint
  smoke-test/   End-to-end API smoke test
  verify-bvbrc/ BV-BRC API verification tool
  worker/       Remote worker for distributed execution
internal/
  bvbrc/        BV-BRC auth + JSON-RPC 1.1 client
  bundle/       CWL file bundler
  config/       Server configuration
  executor/     Executor backends (local, docker, bvbrc)
  logging/      slog setup
  parser/       CWL parser + validator + DAG builder
  scheduler/    Tick-based scheduler loop
  server/       HTTP handlers + routing
  store/        SQLite persistence
  cli/          CLI command implementations
pkg/
  cwl/          CWL type definitions
  model/        Domain model (Workflow, Task, Submission, state machines)
testdata/       CWL workflow examples
docs/           Implementation plan + API reference
```

## Distributed Execution

GoWe supports distributed task execution across multiple worker nodes using Docker Compose:

```bash
# Start server and workers
docker-compose up -d --build

# Run a workflow against the distributed setup
./bin/gowe run --server http://localhost:8090 workflow.cwl job.yml

# Run the distributed test scripts
./scripts/test-distributed.sh           # Basic tests
./scripts/test-distributed-pipeline.sh  # 3-step pipeline with shared volume

# Stop the cluster
docker-compose down -v
```

The `docker-compose.yml` starts:
- 1 server with `--default-executor=worker`, `--allow-anonymous`, and `--upload-backend=local`
- 2 workers with `--runtime=none` (host execution)
- 1 worker with `--runtime=docker` (container execution, `DOCKER_VOLUME=gowe-workdir`)

All services share a named Docker volume (`gowe-workdir`) for working directories and outputs.

### Worker Groups

Workers can be organized into groups for targeted scheduling:

```bash
# Start a worker in a specific group
gowe-worker --server http://localhost:8080 --group gpu-workers --worker-key $SECRET

# Configure worker keys in server (maps keys to allowed groups)
cat > ~/.gowe/worker-keys.json << 'EOF'
{
  "keys": {
    "secret-key-1": {"groups": ["default", "gpu-workers"]},
    "secret-key-2": {"groups": ["cpu-workers"]}
  }
}
EOF
```

Tasks can target specific worker groups via `RuntimeHints.WorkerGroup`.

### Shared Volume

All services share a named Docker volume (`gowe-workdir`) mounted at `/workdir`. Output files are accessible at:

```
Volume:     gowe-workdir:/workdir/outputs/task_<id>/
Container:  /workdir/outputs/task_<id>/
```

The Docker worker uses `DOCKER_VOLUME=gowe-workdir` so that tool containers mount the same named volume — no host path translation is needed.

See [docs/tools/worker.md](docs/tools/worker.md) for worker configuration details.

## Testing

### Quick Start

```bash
# Setup test environment
./scripts/setup-env.sh -b
source .env

# Run all conformance tests (378 tests per mode)
./scripts/run-all-tests.sh

# Run required tests only (84 tests, faster for CI)
./scripts/run-all-tests.sh --required
```

### Test Commands

```bash
# Unit tests
go test ./...

# Tier 1 only (CI fast path)
./scripts/run-all-tests.sh -t 1

# Single mode
./scripts/run-all-tests.sh -m cwl-runner

# Skip Docker tests
./scripts/run-all-tests.sh --no-docker

# Staging backend tests
./scripts/run-staging-tests.sh

# With Docker integration tests
go test ./internal/executor/ -tags=integration

# With BV-BRC integration tests (requires valid token)
BVBRC_TOKEN=... go test ./internal/executor/ -tags=integration
```

### Test Tiers

| Tier | Tests | Description |
|------|-------|-------------|
| 1 | `unit`, `cwl-runner`, `cwl-runner-parallel` | Core execution (must pass) |
| 2 | `server-local`, `distributed-*` | Server modes |
| 3 | `staging-file`, `staging-s3`, `staging-shock` | Staging backends |

### CWL Conformance

GoWe passes 100% of CWL v1.2 conformance tests (378/378) in cwl-runner mode with Docker, and 377/378 with Apptainer. The container runtime is auto-detected from PATH when `DockerRequirement` is present.

| Mode | Result | Notes |
|------|--------|-------|
| cwl-runner (Docker) | 378/378 | |
| cwl-runner (Apptainer) | 377/378 | 1 known limitation¹ |
| cwl-runner-parallel | 378/378 | |
| distributed-none | 376/378 | |
| distributed-docker | 376/378 | |

¹ Test 227 (`networkaccess_disabled`): Apptainer shares the host network by default. Network isolation (`--net --network none`) requires root or admin config, which is unavailable on most HPC systems. See [docs/Execution-Modes.md](docs/Execution-Modes.md) for details.

See [scripts/README.md](scripts/README.md) for detailed test documentation.

## License

TBD
