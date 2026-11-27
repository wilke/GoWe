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

A `gowe:Service` step is the exception: always `worker` (see below).

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

  gowe:Service:                # Experimental long-running service: server side only (no worker runs it yet), see below
    port_input: port           # int input that receives the port; also scheme, health{...}, stop_grace, weights_checksum (docs/cwl-hints.md)
```

A workflow with a `gowe:Service` tool is registered or replaced only if it has one step, no scatter, a container, no executor hint other than `worker`/`container`, a valid non-`default` worker group, an `int` `port_input` (else 400), and the caller is a server admin or a manager of that group (else 403; `model.ValidateServiceWorkflow` + `checkServiceWorkflow` in `internal/server/handler_workflows.go`). Replace, labels and delete of an existing one (REST and UI delete) go through `MayChangeWorkflow` (`internal/server/group_admin.go`, also on `ui.GroupAdmin`). A service anywhere inside a sub-workflow is refused by the parser and, authoritatively, by `createChildSubmission`; a child submission's task never carries `RuntimeHints.Service`. A service task (`tasks.is_service`, derived by `insertTask` from `RuntimeHints.Service`) is never started twice: a service step always uses the worker executor (`dispatchStep` sets it, whatever the default executor or online workers; `--force-executor` other than worker or an executor hint other than worker/container fails the step, no task; `insertTask` refuses a non-worker service task, `store.ErrServiceNotWorker`); a READY service step that already owns a non-terminal task adopts it (`adoptServiceTask`); `persistSubmitOutcome` writes a non-terminal outcome of any worker task with `TerminalizeTaskFrom` the state the caller last wrote (QUEUED at dispatch, SCHEDULED at retry), so a checkout in between stands; a service task exists and is handed out only while its submission row exists and is not terminal (`insertTask` checks it in the insert's transaction, `store.ErrServiceSubmissionGone`, which `dispatchStep` and `failTaskPreDispatch` treat as nothing to do; `CheckoutTask`'s candidate SQL for a service-mode worker; `checkEligible` cannot, it sees only the task row); `CheckoutTask` matches it only with a `service_mode` worker (registration field; such a worker takes only services; service mode requires worker auth, else 403, and only a key whose groups explicitly name the worker's group may register one or adopt a keyless service-mode row in `authorizeWorker`, else 403), and `checkEligible` treats `IsService || RuntimeHints.Service != nil` as a service; preflight and `detectStuckTasks` never fail it, and service-mode workers are not batch capacity (`workerCapabilities`); MaxRetries 0, `markRetries` skips it whatever MaxRetries (and `advanceSteps` treats a FAILED service as final), `resubmitRetrying` fails it before archiving/blanking, `ArchiveAndResetTask` refuses it (`store.ErrServiceNotReset`); every requeue statement, `ResetFailedTasks` and their `archiveTaskAttempts` conditions carry `is_service = 0`; the reaper keeps a silent worker's services RUNNING with their charges (host sums count them even offline) and sets `service_lost_at` (cleared by `TouchWorker`); a service-mode worker's heartbeat `services` list (`model.ServiceReport`, more than 64 or not a list: whole list ignored with a WARN, the heartbeat still 200; ignored from other workers, invalid entries skipped with a WARN) is stored after the reconcile by `UpdateServiceStatuses` as `tasks.service_status` (only a RUNNING service of that worker, `updated_at` from the server, `ready_at` sticky and taken only with state `ready`, never before `started_at`, kept when the task ends) and shown as `Task.ServiceStatus`, whose state the store rewrites on every read (`Task.ApplyShownServiceState`: `stopped` when not RUNNING, `lost` with `service_lost_at`, `stopping` with a stop request); GPUs: a service-mode worker of unknown GPU capacity gets no GPU task, and a worker with an inventory and host id also subtracts the `charged_gpus` of RUNNING services of other rows of its `host_id` whose inventory shares a GPU UUID (`overlappingServiceGPUs`; charges follow the worker row, so a restarted worker's lost services stay charged to the old row until force-released; protection is by inventory, not assigned device); `ReconcileWorkerTasks` applies a two-strike rule only when the heartbeat carried `running_tasks` (nil = field absent: services untouched): first omission sets `service_missing_at`, listing clears it, an omission once it is older than the grace window fails it (`failMissingService`, CAS on state + `external_id`; SKIPPED when a stop was requested); the heartbeat's `cancel_tasks` (`CancelledTasksForWorker(workerID, listed)`) also lists reported services that are terminal in the database, have a stop request or (RUNNING) have no submission row, and, by `external_id` whatever was listed (`running_tasks` absent included), every RUNNING service of the worker with a stop request or a terminal (CANCELLED, FAILED, COMPLETED) or missing submission (batch tasks: the list rule only, CANCELLED only); `/complete` accepts only terminal states (or none) for every worker task, and for a service an explicit FAILED or SKIPPED (SUCCESS 400; SKIPPED stands only with a CANCELLED submission or a stop request); stopping: `CancelNonTerminalTasks` (the only bulk terminate on cancel: cancelseq, its descendant fan-out, the scheduler's child cascade, both delete handlers) never SKIPs a RUNNING worker service but sets `service_stop_requested_at` once, so it stays RUNNING and charged until its worker reports (the scheduler leaves the CANCELLED submission alone; the REST cancel answer counts them in `services_stopping` via `CountStoppingServices`, not in `tasks_already_completed`, and `gowe cancel` prints `Services stopping: N (...)`); a submission (or descendant) with a RUNNING service is not deleted (`HasRunningService`, 409 in REST and UI delete before anything changes, and `DeleteSubmission` itself returns `store.ErrSubmissionHasRunningService`; when that refusal comes after the handler's own step/task cancel, the handler completes the cancel with `cancelseq.Run`, re-reads the submission and answers 409 `store.DeleteRefusedMessage(state)`: `store.DeleteRefusedServiceStopping` when CANCELLED, else a text naming the real state); `POST /api/v1/submissions/{id}/tasks/{tid}/force-release` (`handler_service_release.go`, `gowe admin force-release-service`; admins and the task's group managers only, never service identities or anonymous; a caller managing no group gets 403 `ForceReleaseRefused` before any lookup, a manager of another group the same text, and so does a non-admin for every miss: unknown submission or task, task of another submission; admins get 404 for those) ends a RUNNING service whose worker is lost (`service_lost_at`), has no row, or has not confirmed a stop request older than `model.ServiceStopReleaseAfter` (15 min), or whose submission row is gone (admins only; the handler then needs a task with that id and that `submission_id`), FAILED (SKIPPED with a stop request), through `store.ForceReleaseService` (checks + CAS repeating them in one transaction, refusal `*store.ServiceReleaseError` = 409); the answer holds only `id`, `state`, `external_id`, `completed_at`, `worker_notified`, and the task metrics record it like `/complete` (reason `force_release`); the process may still run; deleting a worker that holds one returns `store.ErrWorkerHasServices` (409 on every removal route, UI purge skips it); submission retry (409 when any task of the submission is a service, or the workflow is one) and UI resume/recompute refuse it (`model.ServiceNotRerun`); `cwl:requirements` is refused for a service workflow at submission (`tokencheck.ServiceJobRequirements`, 400) and never merged into its tool (`jobRequirements`).

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
