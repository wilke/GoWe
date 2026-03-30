# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test

```bash
# Build all binaries (embeds git commit hash in worker version)
make build

# Build individual binaries
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
go build -ldflags "-X main.Version=$(git rev-parse HEAD)" -o bin/gowe-worker ./cmd/worker
go build -o bin/cwl-runner ./cmd/cwl-runner

# Unit tests
go test ./...

# Single package (verbose)
go test -v ./internal/parser/...

# Integration tests (require Docker or BV-BRC token)
go test ./internal/executor/ -tags=integration
BVBRC_TOKEN=... go test ./... -tags=integration

# Lint
go vet ./...
go fmt ./...

# CWL conformance (requires bin/cwl-runner + cwltest Python package)
./scripts/run-conformance.sh required    # 84 required tests
./scripts/run-conformance.sh             # All 378 tests

# Full test suite across all execution modes
./scripts/run-all-tests.sh               # All tiers
./scripts/run-all-tests.sh -t 1          # Tier 1 only (fast CI)
./scripts/run-all-tests.sh --required    # Required tests only
```

Binary naming: `cli` → `gowe`, `server` → `gowe-server`, `worker` → `gowe-worker`. The Makefile handles this automatically.

## Architecture

### Core Data Flow

```
CWL file → parser/ → model.Workflow (DAG of Steps)
                          ↓
            Submit with inputs → model.Submission
                          ↓
         scheduler/ tick loop (6 phases per tick)
                          ↓
     StepInstance (per step per submission, handles scatter)
                          ↓
              Task (concrete work unit)
                          ↓
        executor/ registry dispatches to backend
                          ↓
    local | docker | apptainer | worker | bvbrc
```

### Three-Level State Hierarchy

```
Submission  (PENDING → RUNNING → COMPLETED/FAILED/CANCELLED)
  └─ StepInstance  (WAITING → READY → DISPATCHED → RUNNING → COMPLETED/FAILED/SKIPPED)
       └─ Task  (PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS/FAILED)
```

Scatter steps produce N StepInstances, each with its own Tasks. The scheduler advances these through 6 phases each tick: (1) advance WAITING→READY when deps met, (2) dispatch READY→create Tasks, (3) retry FAILED tasks, (4) poll in-flight async tasks, (5) advance step instances, (6) finalize submissions.

### Executor Registry

Pluggable backends implementing `Submit()`, `Status()`, `Cancel()`, `Logs()`. Selection order:
1. Server `--default-executor` override
2. `gowe:Execution.executor` hint (`worker`, `bvbrc`, `local`)
3. `DockerRequirement` → auto-promoted to `worker` when workers online, else local
4. Default → `local`

### Worker Pull Model

Workers poll the server for tasks (no push). The checkout process matches on: container runtime capability, worker group, and dataset affinity (prestage=require, cache=prefer). Workers execute via `toolexec/`, stage outputs, and report results.

### CWL Hint Extensions

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    executor: worker           # Routing: local, worker, bvbrc
    worker_group: esmfold      # Target worker group
    bvbrc_app_id: GenomeAssembly2
    docker_image: override.sif # Override DockerRequirement

  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        mode: prestage         # "prestage" (require) or "cache" (prefer)
```

### Key Packages

| Package | Role |
|---------|------|
| `internal/scheduler/` | Tick-based scheduling loop, dependency resolution, retry logic |
| `internal/executor/` | Registry + 5 backends (local, docker, apptainer, bvbrc, worker) |
| `internal/store/` | SQLite persistence (pure Go, WAL mode, single writer) |
| `internal/parser/` | CWL v1.2 parsing, validation, DAG construction, hints extraction |
| `internal/server/` | HTTP handlers (go-chi), middleware, auth, SSE |
| `internal/worker/` | Remote worker loop: poll, execute, stage outputs, heartbeat |
| `internal/cwltool/` | Full CWL CommandLineTool executor (bindings, globbing, JS, IWDR) |
| `internal/toolexec/` | Shared execution logic: command building, mounts, GPU, secrets |
| `internal/cwlrunner/` | Standalone runner (no server), used by `bin/cwl-runner` |
| `internal/cwlexpr/` | CWL expression evaluation via goja JavaScript engine |
| `pkg/model/` | Domain entities with state machines and valid transitions |
| `pkg/cwl/` | CWL v1.2 type definitions (loose for bundler, strict in parser) |
| `pkg/staging/` | File staging abstraction: copy, symlink, reference modes |

### API Format

All endpoints under `/api/v1`. JSON envelope: `{status, request_id, timestamp, data}`.

### Database

SQLite with `modernc.org/sqlite` (pure Go, no CGO). Schema in `internal/store/migrations.go`. Migrations use idempotent `ALTER TABLE ADD COLUMN` with `addColumnIfNotExists()`. WAL mode, `max_open_conns=1`.

## Go Conventions

- Wrap errors with context: `fmt.Errorf("context: %w", err)`
- Use `internal/logging` (slog-based) with appropriate levels
- Table-driven tests with `t.Run()` subtests
- Integration tests use build tags (`-tags=integration`)
- `go.sum` is auto-generated; only edit `go.mod` manually

## Worker Flags (Key Ones)

| Flag | Purpose |
|------|---------|
| `--group` | Worker group for task isolation (default: `default`) |
| `--pre-stage-dir` | Auto-scan datasets directory, bind-mount into containers |
| `--dataset id=path` | Explicit dataset alias (repeatable) |
| `--extra-bind /path` | Generic bind mount (repeatable, not used for scheduling) |
| `--secret NAME=value` | Secret env var injected into containers (never sent to server) |
| `--secret-file` | Load secrets from file (`NAME=value` per line) |
| `--image-dir` | Base directory for resolving relative `.sif` image paths |
| `--gpu` / `--gpu-id` | GPU passthrough |

## Running the Server

```bash
# Development
bin/gowe-server --addr :8080 --debug --db /tmp/gowe.db --allow-anonymous

# With workers
bin/gowe-server --addr :8080 --allow-anonymous --anonymous-executors local,docker,worker,container

# Worker
bin/gowe-worker --server http://localhost:8080 --runtime apptainer --image-dir /scout/containers/
```
