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

A scatter step produces ONE StepInstance owning N Tasks (one per combination, carrying ScatterIndex; outputs merge back in index order). The scheduler advances these through 6 phases each tick: (1) advance WAITING→READY when deps met, (2) dispatch READY→create Tasks, (3) retry FAILED tasks, (4) poll in-flight async tasks, (5) advance step instances, (6) finalize submissions.

Sub-workflow steps are never executed inline: each (scatter combination of a) sub-workflow step gets a persisted proxy task (`executor_type: subworkflow`, RUNNING from birth, `MaxRetries: 0`, `Job` = resolved child inputs) paired 1:1 with a child submission (`parent_task_id` = proxy id) that flows through the same tick machinery. pollInFlight advances the proxy from the child's state (child CANCELLED → proxy FAILED), repairs missing children after a crash, and reconciles cancels one nesting level per tick; the cancel handler also fans out to descendants synchronously. See SPECIFICATION.md §7.4 and ADR-0011.

### Executor Registry

Pluggable backends implementing `Submit()`, `Status()`, `Cancel()`, `Logs()`. Selection order:
1. Server `--default-executor` override
2. `gowe:Execution.executor` hint (`worker`, `bvbrc`, `local`)
3. `DockerRequirement` → auto-promoted to `worker` when workers online, else local
4. Default → `local`

### Worker Pull Model

Workers poll the server for tasks (no push). The checkout process matches on: container runtime capability, worker group, and dataset affinity (prestage=require, cache=prefer). Workers execute via `toolexec/`, stage outputs, and report results. Worker keys restrict which workers join a group; the worker group access policy (`internal/groupacl`, #261) restricts which submitters may target a group. It is stored in the database (`worker_group_policies`, `server_settings` key `worker_groups.default_deny`), changed at runtime by admins (`gowe admin worker-groups`, `/api/v1/admin/worker-groups`: write the store, then `Reload` the one `*groupacl.Policy` shared by server, UI and scheduler), and takes effect at once: checked early at submission (403), authoritatively in `dispatchStep` before any worker task is created (the step fails, no task), and again when a FAILED task is re-queued (scheduler retry, UI resume/recompute). Tasks already QUEUED or RUNNING are not re-checked; a direct SQL edit is only seen after a restart; an invalid stored rule stops start-up. The `--config` file may hold only `admins` and `database` (checked in `dbconfig.Select`; `worker_groups` there is a start-up error). A rule's `managers` (admin-appointed, never on `default`) may use the group and, through `/api/v1/worker-groups` / `gowe worker-groups` (`internal/server/handler_worker_group_managers.go`: right re-checked after Reload under `groupPolicyMu`), change its `users`, issue/revoke worker keys whose groups are exactly `[group]`, list the group's workers and remove offline ones (`internal/server/handler_group_workers.go`; removal only through `DeleteOfflineWorker`, which checks offline + silent for 3 x the worker timeout inside the delete transaction; `peer_addr` is recorded from the connection before `middleware.RealIP` and shown only to admins and the group's managers). The web UI pages `/admin/worker-groups` and `/worker-groups[/{group}]` call the same operations as those REST routes through the `ui.GroupAdmin` interface, implemented once on `*server.Server` in `internal/server/group_admin.go` (all checks live there; the REST handlers only decode/encode); worker auth stays on once a DB key has existed (`server_settings` `worker_auth.required`, set by `CreateWorkerKey` and at start-up). Programs (the model router) authenticate with GoWe-issued service tokens (`gowe_svc_…`, table `service_tokens`, hash only; admin routes `/api/v1/admin/service-tokens`, `gowe admin service-tokens`, CLI `--token-file`): `authenticateServiceToken` in `internal/server/auth.go` handles any credential containing the prefix (never the BV-BRC verifier/parser) and yields `<name>@service.gowe`, role user, `UserContext.IsService`, no provider token (like an anonymous submission: no Workspace staging, no injection). The realm `@service.gowe` is reserved (provider tokens and UI login claiming it are refused) and never admin: `model.User.IsAdmin` and `AdminConfig.IsAdmin` both refuse `model.IsServiceUsername`. A service identity never uses the BV-BRC executor (`model.ServiceBVBRCDenied`: 403 at submission/retry, step failed in `dispatchStep`, refused in `resubmitRetrying` and UI requeue) and `BVBRCExecutor.Submit` never falls back to the server's account (`defaultCaller` only for Status/Cancel/Logs); it is never a group manager (`groupacl.ValidateManagers` refuses, `Reload` ignores a stored entry with a WARN, `IsManager`/`mayManageGroup` say no).

**Token handling:** a worker holds the submitter's token only to stage data in/out and drops it when the task ends. It injects the token into a tool's environment only when the scheduler marked the task blessed (`TokenBlessingID`) AND the resolved image's sha256 is in `ImageSHA256s`; otherwise a blessed-less request is refused with a WARN and a digest mismatch fails the task pre-launch. See `internal/worker/tokenpolicy.go` and `docs/security/authentication.md` §6.

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
    gpu: true                  # Needs 1 GPU (--gpu worker)
    gpu_count: 2               # Needs N GPUs (wins over gpu); also cwltool:CUDARequirement.cudaDeviceCountMin, larger wins

  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        mode: prestage         # "prestage" (require) or "cache" (prefer)

  gowe:Service:                # Experimental long-running service: parsed, validated, access-checked only (runs as an ordinary task, MaxRetries 0)
    port_input: port           # int input that receives the port; also scheme, health{...}, stop_grace, weights_checksum (docs/cwl-hints.md)
```

A workflow with a `gowe:Service` tool is registered or replaced only if it has one step, no scatter, a container, a valid non-`default` worker group, an `int` `port_input` (else 400), and the caller is a server admin or a manager of that group (else 403; `model.ValidateServiceWorkflow` + `checkServiceWorkflow` in `internal/server/handler_workflows.go`). Replace, labels and delete of an existing one (REST and UI delete) go through `MayChangeWorkflow` (`internal/server/group_admin.go`, also on `ui.GroupAdmin`). A service anywhere inside a sub-workflow is refused by the parser and, authoritatively, by `createChildSubmission`; a child submission's task never carries `RuntimeHints.Service`.

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

PostgreSQL 16+ is a second backend of the same `*store.SQLStore` (dialect in `internal/store/dialect_postgres.go`, ADR-0014), selected by a `postgres://` URL in `--db` or `database.url` of `--config` (`internal/dbconfig`, shared with `cmd/db-copy`), also with one connection; store tests run on it when `GOWE_TEST_POSTGRES_URL` is set. `gowe-db-copy` (`cmd/db-copy`, `store.CopyDatabase` in `internal/store/copy.go`) copies a SQLite database into an empty PostgreSQL one; a new table must be added to `copyTables` there (a test fails otherwise). `task_attempts` keeps a task's earlier attempts (exit code, stdout/stderr, worker, times): every path that blanks a task record archives it first in the same transaction, through `ArchiveAndResetTask` (archive + compare-and-set full-row write, nothing archived when the CAS loses: scheduler retry, UI resume/recompute) or the shared `archiveTaskAttempts` statement (`ResetFailedTasks`, every requeue of RUNNING worker tasks). Scheduler retries, UI resume/recompute and worker requeues continue under the same task id; a submission retry (`ResetFailedTasks`) makes the scheduler dispatch the steps again as new tasks, so that attempt stays on the old task record. The scheduler retry's reset write keeps the old `retry_count` (the increment is persisted with the submit outcome). `archiveTaskAttempts` keeps the newest `model.MaxTaskAttempts` (20) per task; rows are deleted with the submission.

## Go Conventions

- Wrap errors with context: `fmt.Errorf("context: %w", err)`
- Use `internal/logging` (slog-based) with appropriate levels
- Table-driven tests with `t.Run()` subtests
- Integration tests use build tags (`-tags=integration`)
- `go.sum` is auto-generated; only edit `go.mod` manually

## Agent Workflow (Claude Code)

- Use **Fable** for planning and reviews; choose an appropriate (often cheaper) model
  for implementation.
- Use subagents where appropriate: fan out independent implementation pieces, and run
  adversarial review lenses in parallel before opening a PR (see the #164 plan on the
  issue tracker for the pattern: map → plan → review → implement → verify).
- **When work can be decomposed, delegate bounded implementation and test tasks to Sonnet
  subagents. Reserve the main loop for planning, integration and review. Use Opus
  subagents where necessary.**
- Go is not installed natively — build and test through the `build` / `test` skills
  (Apptainer `golang:1.24`).

## Branching, Commits & Releases

Trunk-based (GitHub Flow). Full guide: [`CONTRIBUTING.md`](CONTRIBUTING.md).

- **`main` is the only long-lived branch** and is always releasable. No `develop` branch.
- **Branch from `main`** (not from another PR's branch) with `<type>/<desc>` naming:
  `feat/`, `fix/`, `docs/`, `refactor/`, `test/`, `security/`, `ci/`, `chore/`. Keep branches
  short-lived; do **not** stack PRs.
- **Conventional Commits are required** — `feat:`, `fix:`, `docs:`, `security:`, etc.;
  `feat!:`/`BREAKING CHANGE:` for breaks. release-please derives the version and changelog
  from them, so the type is the release automation. The **squash-merge message** is what
  gets read — make it a valid Conventional Commit.
- **PRs target `main`**, require green `ci` (`go vet`, `go build`, `go test`), and are
  **squash-merged** (linear history; branch auto-deleted).
- **Releases are automated — do not tag by hand normally.** Merging the open **release-please**
  PR bumps `.release-please-manifest.json` + `CHANGELOG.md`, creates the `vX.Y.Z` tag, and
  **GoReleaser** builds the four binaries (`gowe`, `gowe-server`, `gowe-worker`, `cwl-runner`)
  for linux/darwin × amd64/arm64. Manual `git push origin vX.Y.Z` is the hotfix escape hatch
  (triggers the `release` workflow). Pre-releases use a `-rc.N` suffix, not a branch.
- Release config: `.goreleaser.yaml`, `release-please-config.json`,
  `.release-please-manifest.json`, `.github/workflows/{ci,release,release-please}.yml`.

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
| `--gpu` / `--gpu-id` | GPU passthrough. At concurrency 1 every task gets the whole config; above 1 each task gets exactly its own devices (count charged by the server as `charged_gpus`, UUIDs from the nvidia-smi inventory, which is then required; a task needing none gets no GPU access) |
| `--concurrency N` | Max tasks run at once (default 1 = one at a time; ≤ effective CPUs; with `--gpu` above 1 the GPU inventory is required). A task requesting no cores/RAM gets the share the server charged (`max(1, CPUs/N)` cores, `mem/N` MiB, or `--default-task-cores` / `--default-task-ram`); an older server (no `default_task_cores` echoed, or a task without `charged_cores`) drops the worker to 1 with a WARN. Shutdown cancels running tasks, waits ≤ 60 s, then deregisters (no drain) |
| `--host-id` | Host identity sent at registration, verbatim (1-64 chars `[A-Za-z0-9._-]`); default: first 16 hex of sha256 of `/etc/machine-id` → boot_id → hostname |
| `--worker-key-file` | Worker auth key, key file only (regular file, mode 0600 or stricter). `--worker-key` (argv) and `GOWE_WORKER_KEY` (environment) are **refused**: both are readable by same-uid host processes (`/proc/<pid>/cmdline`, `/proc/<pid>/environ`), and tools without DockerRequirement run on the host on every worker |
| `--inherit-host-env` | Degraded: host-run tools (`--runtime none`) inherit the worker env instead of the allow-list (WARN; label `inherit_host_env=true`) |
| `--apptainer-no-containall` | Degraded: Apptainer without `--containall`, `--cleanenv` kept (WARN; label `apptainer_containall=false`) |
| `--keep-task-dirs` | Keep task dirs after success (failed tasks always kept; `gowe submit --debug` keeps a single job's). Independent of this flag, a task that runs again starts in a fresh dir and the earlier attempt's is kept as `<task id>.attempt-N` (removed only by `--kept-task-dir-ttl`) |
| `--kept-task-dir-ttl` | Remove kept task dirs (failed, cancelled, debug, `--keep-task-dirs`, `.attempt-N`) whose `<workdir>/<base>.kept` marker (written next to the dir, `internal/worker/keptdirs.go`) is older than this; at start-up and hourly. Default 0 = never; positive below 1h is a start-up error. Dirs without a marker (in-place outputs, pre-marker dirs) are never touched. Kept dirs are removed only when the worker is the only worker process on its work directory (owner/sharer flocks, `internal/worker/workdirlock.go`) |

`cwl-runner` accepts the same bind/env flags as the worker (`--pre-stage-dir`, `--dataset`, `--extra-bind`, `--env`, `--env-file`; shared parsing in `internal/hostres`; see `docs/tools/cwl-runner.md`) — under Apptainer `--containall` they are the only way host databases and env reach a container.

## Running the Server

```bash
# Development
bin/gowe-server --addr :8080 --debug --db /tmp/gowe.db --allow-anonymous

# With workers
bin/gowe-server --addr :8080 --allow-anonymous --anonymous-executors local,docker,worker,container

# Worker
bin/gowe-worker --server http://localhost:8080 --runtime apptainer --image-dir /scout/containers/
```
