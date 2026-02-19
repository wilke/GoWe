# Remote Worker Architecture: Feasibility & Implementation Analysis

**Date:** 2026-02-16
**Context:** Evaluate AWE's worker model for GoWe remote execution with Docker + Apptainer support

---

## 1. AWE Worker Model Summary

AWE uses a **pull-based HTTP worker model** with these core concepts:

| Concept | AWE Implementation |
|---------|-------------------|
| **Communication** | HTTP REST (no websockets/gRPC) |
| **Work distribution** | Worker polls server (`GET /work?client={id}`) |
| **Registration** | Worker POST profile → server assigns UUID |
| **Heartbeat** | Worker PUT every 10s → server responds with instructions |
| **Job lifecycle** | Checkout → Download data → Execute → Upload results → Report |
| **Container support** | Docker via `go-dockerclient` library |
| **Queue model** | Server-side queue with FCFS policy |
| **Auth** | Client group tokens (optional bearer) |
| **Data staging** | Shock object store for input/output transfer |

**Worker pipeline (goroutines):**
```
workStealer → dataDownloader → processor → deliverer
```

**Key strengths:**
- Simple HTTP protocol — workers need no special infrastructure
- Server maintains all state — workers are stateless between heartbeats
- Disk-space-aware scheduling
- Graceful degradation (re-registration on server restart)

**Key weaknesses:**
- Polling introduces latency (up to 10s between job availability and checkout)
- No push mechanism — high worker counts create polling pressure
- Tight coupling to Shock object store for data staging
- Single-threaded work checkout (semaphore-based mutual exclusion)
- No native Apptainer/Singularity support

---

## 2. GoWe Current Architecture

GoWe's executor model is already well-suited for async remote execution:

```
Executor Interface:
  Type()   → ExecutorType
  Submit() → (externalID, error)     # Fire-and-forget for async
  Status() → (TaskState, error)      # Idempotent polling
  Cancel() → error
  Logs()   → (stdout, stderr, error)
```

**Scheduler tick phases (every 2s):**
```
1. advancePending    — deps check → SCHEDULED
2. dispatchScheduled — resolve inputs → Submit() → QUEUED/terminal
3. resubmitRetrying  — retry FAILED tasks
4. pollInFlight      — Status() polling for QUEUED/RUNNING
5. finalizeSubmissions — aggregate task states
6. markRetries       — FAILED → RETRYING if retries remain
```

**Existing executor types:**
| Type | Sync/Async | ExternalID | Notes |
|------|-----------|------------|-------|
| `local` | Sync | work dir path | Blocks in Submit(), completes immediately |
| `container` | Sync | container ID | Docker via CLI, blocks in Submit() |
| `bvbrc` | Async | job UUID | JSON-RPC, polls Status() each tick |

**Key insight:** The BV-BRC executor already implements the exact async pattern a remote worker needs. The scheduler's polling loop handles it transparently.

---

## 3. Proposed Remote Worker Architecture for GoWe

### 3.1 High-Level Design

```
┌─────────────────────────────────────────┐
│              GoWe Server                │
│                                         │
│  Scheduler ──→ WorkerExecutor           │
│       ↕              ↕                  │
│  Store (SQLite)  Worker Queue           │
│                      ↕                  │
│              Worker API Endpoints       │
│              POST /api/v1/workers       │
│              PUT  /api/v1/workers/{id}  │
│              GET  /api/v1/workers/work  │
│              PUT  /api/v1/tasks/{id}    │
└──────────────────────┬──────────────────┘
                       │ HTTP
        ┌──────────────┼──────────────────┐
        │              │                  │
   ┌────▼────┐   ┌────▼────┐   ┌────▼────┐
   │ Worker  │   │ Worker  │   │ Worker  │
   │ (Docker)│   │ (Appt.) │   │ (bare)  │
   └─────────┘   └─────────┘   └─────────┘
```

### 3.2 Adaptation from AWE

**Keep from AWE:**
- Pull-based HTTP polling (simple, firewall-friendly, no special infra)
- Worker registration with profile (capabilities, resources)
- Heartbeat for liveness detection
- Server-side queue and work assignment

**Simplify vs AWE:**
- No Shock object store — use task.Inputs/Outputs as JSON (BV-BRC pattern)
- No data staging phase — inputs are already resolved by scheduler
- No multi-goroutine pipeline — single execution loop per worker
- No client groups — use existing GoWe auth when needed

**Add beyond AWE:**
- Apptainer/Singularity support alongside Docker
- Container runtime selection per worker (capability-based matching)
- Worker labels/tags for affinity scheduling
- CWL-aware execution (use `_base_command`, `_output_globs`, `_docker_image`)

### 3.3 Component Design

#### A. Worker Registration & Heartbeat

```go
// pkg/model/worker.go
type Worker struct {
    ID           string            `json:"id"`           // wrk_<uuid>
    Name         string            `json:"name"`
    Hostname     string            `json:"hostname"`
    State        WorkerState       `json:"state"`        // online, offline, draining
    Runtime      ContainerRuntime  `json:"runtime"`      // docker, apptainer, none
    Labels       map[string]string `json:"labels"`       // for affinity matching
    LastSeen     time.Time         `json:"last_seen"`
    CurrentTask  string            `json:"current_task"` // task_id or ""
    RegisteredAt time.Time         `json:"registered_at"`
}

type ContainerRuntime string
const (
    RuntimeDocker    ContainerRuntime = "docker"
    RuntimeApptainer ContainerRuntime = "apptainer"
    RuntimeNone      ContainerRuntime = "none"  // bare process
)
```

#### B. Worker API Endpoints (server-side)

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/v1/workers` | Register worker |
| `PUT` | `/api/v1/workers/{id}/heartbeat` | Heartbeat + status |
| `GET` | `/api/v1/workers/{id}/work` | Check out next task |
| `PUT` | `/api/v1/workers/{id}/tasks/{tid}/status` | Report task progress |
| `PUT` | `/api/v1/workers/{id}/tasks/{tid}/complete` | Report task completion |
| `DELETE` | `/api/v1/workers/{id}` | Deregister worker |

#### C. WorkerExecutor (server-side, implements Executor interface)

```go
// internal/executor/worker.go
type WorkerExecutor struct {
    store  store.Store
    logger *slog.Logger
}

func (e *WorkerExecutor) Type() model.ExecutorType {
    return model.ExecutorTypeWorker // "worker"
}

func (e *WorkerExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
    // Enqueue task into worker queue (store).
    // Return task.ID as externalID — workers reference tasks by ID.
    // Does NOT assign to a specific worker — workers pull from queue.
    return task.ID, nil
}

func (e *WorkerExecutor) Status(ctx context.Context, task *model.Task) (model.TaskState, error) {
    // Read task state from store (workers update it via API).
    fresh, _ := e.store.GetTask(ctx, task.ID)
    return fresh.State, nil
}
```

**Key insight:** The WorkerExecutor is thin — it just enqueues. Workers update task state directly via the API. The scheduler's existing polling (Phase 3) picks up state changes automatically.

#### D. Worker Process (client-side binary)

```
gowe-worker --server http://host:8080 --runtime docker --name my-worker
```

**Worker loop:**
```
1. Register:  POST /workers → get worker_id
2. Loop:
   a. Heartbeat: PUT /workers/{id}/heartbeat
   b. If idle:   GET /workers/{id}/work → get task (or 204 No Content)
   c. If task:
      - Report RUNNING: PUT /workers/{id}/tasks/{tid}/status
      - Execute (Docker/Apptainer/bare)
      - Collect outputs via glob patterns from _output_globs
      - Report completion: PUT /workers/{id}/tasks/{tid}/complete
        Body: {state, stdout, stderr, exit_code, outputs}
   d. Sleep poll_interval
```

#### E. Data Staging Model

**Core question:** How does the worker know where to put outputs?

The tool defines WHAT files to collect (`_output_globs`). The WORKER CONFIG
defines WHERE to put them. This is because the storage topology is an
infrastructure concern, not a workflow concern.

**Key insight:** Only initial inputs and final outputs MUST be staged.
Intermediate outputs between steps only need staging if the next step runs
on a different worker (federated setup). This makes worker behavior
configurable:

##### Worker Stage-Out Modes

```
gowe worker --server http://host:8080 \
            --runtime docker \
            --workdir /data/gowe/tasks \
            --stage-out local                       # single-worker mode
            --stage-out file:///shared/gowe/results  # shared filesystem
            --stage-out ws:///user@bvbrc/home/gowe   # BV-BRC workspace
            --stage-out shock://                      # Shock server
```

| Mode | Output Location | Use Case |
|------|----------------|----------|
| `local` | `file://{workdir}/{task_id}/{file}` | Single worker runs all steps. Outputs stay in workdir. Fast, no copy. |
| `file://{path}` | `file://{path}/{sub_id}/{task_id}/{file}` | Shared filesystem (NFS). Any worker on the network can reach outputs. |
| `ws://{path}` | `ws://{path}/{sub_id}/{task_id}/{file}` | BV-BRC Workspace. Upload after execution. Federated / remote. |
| `shock://` | `shock://{node-uuid}` | Shock object store. Upload after execution. Federated / remote. |

**What flows through the system:**

```
Step A completes on Worker 1:
  task_a.Outputs["contigs"] = "file:///shared/gowe/sub_x/task_a/contigs.fa"
                                     ↑ determined by worker's --stage-out

Server stores output manifest in DB (resolve.go:38 reads this)

Step B dispatched to Worker 2:
  Resolver reads task_a.Outputs["contigs"] → "file:///shared/gowe/sub_x/task_a/contigs.fa"
  Worker 2 stages in from that URI → copies to its local workdir
```

With `--stage-out local`, the output URI is a worker-local path. This only
works if step B runs on the SAME worker (single-worker mode). The scheduler
could enforce this via worker affinity when stage-out=local.

##### Stage-In: Always Happens

Stage-in is unconditional. The worker always downloads inputs to its local
workdir before execution, regardless of mode:

```go
// internal/worker/stager.go
type Stager interface {
    StageIn(ctx context.Context, location string, destPath string) error
    StageOut(ctx context.Context, srcPath string, location string) error
}
```

Three implementations, selected by URI scheme of the source/dest:

```go
type FileStager struct{}      // file:// → cp or symlink
type ShockStager struct{}     // shock:// → HTTP GET/PUT to Shock node
type WorkspaceStager struct{}  // ws:// → BV-BRC Workspace download/upload
```

The worker resolves each input's scheme via `cwl.ParseLocationScheme()`,
picks the matching stager, and copies to `workdir/{task_id}/inputs/{name}`.

##### Stage-Out: Configurable

After execution, the worker:
1. Collects output files via `_output_globs` in the workdir
2. If `--stage-out=local`: reports workdir paths as output URIs (no copy)
3. If `--stage-out=file://...`: copies to shared path, reports shared URIs
4. If `--stage-out=ws://...`: uploads to workspace, reports ws:// URIs
5. Reports output manifest to server

##### Workdir Cleanup

| Mode | When to Clean |
|------|--------------|
| `local` | After ALL downstream tasks complete (server tells worker via heartbeat) |
| `file://`, `ws://`, `shock://` | Immediately after stage-out + report (outputs are in durable storage) |

Worker config flag: `--cleanup=auto|immediate|manual`

##### Final Workflow Outputs

For the workflow's final outputs (not intermediate), the submission can
specify an explicit destination that overrides the worker's default:

```json
{
  "workflow_id": "wf_123",
  "inputs": { ... },
  "output_destination": "ws:///user@bvbrc/home/my-analysis/"
}
```

If set, the worker stages final outputs to that destination regardless
of its `--stage-out` mode. This gives users control over where results
land without changing worker config.

#### F. Container Execution

```go
// internal/worker/runtime.go
type Runtime interface {
    Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

type RunSpec struct {
    Image      string            // _docker_image (empty = bare execution)
    Command    []string          // _base_command
    WorkDir    string            // task workdir (inputs already staged here)
    Env        map[string]string // optional environment variables
}

type RunResult struct {
    ExitCode int
    Stdout   string
    Stderr   string
}
```

**Docker runtime:**
```
docker run --rm -v workdir:/work -w /work image cmd...
```

**Apptainer runtime:**
```
apptainer exec --bind workdir:/work --pwd /work docker://image cmd...
```

**Bare runtime:**
```
cd workdir && cmd...
```

Outputs are collected from workdir via `_output_globs` AFTER execution,
then staged out to their destination locations by the Stager.

### 3.4 Task Assignment Strategy

**Simple approach (recommended for v1):**
- FIFO queue — workers get the next available task
- Filter by required runtime: if task has `_docker_image`, only assign to workers with matching runtime
- No affinity/anti-affinity (add in v2 via labels)

**Server-side checkout logic:**
```go
func (h *WorkerHandler) HandleWorkCheckout(w http.ResponseWriter, r *http.Request) {
    workerID := chi.URLParam(r, "id")
    worker, _ := h.store.GetWorker(ctx, workerID)

    // Find next QUEUED task with executor_type="worker"
    // that matches worker's runtime capabilities
    task, err := h.store.CheckoutTask(ctx, worker)
    if err != nil || task == nil {
        w.WriteHeader(http.StatusNoContent) // No work available
        return
    }

    task.State = model.TaskStateRunning
    task.ExternalID = workerID
    h.store.UpdateTask(ctx, task)

    // Return task with resolved inputs
    json.NewEncoder(w).Encode(task)
}
```

### 3.5 CWL Workflow Integration

Workers are specified via the existing `goweHint` mechanism:

```yaml
hints:
  goweHint:
    executor: worker              # routes to WorkerExecutor
    docker_image: ubuntu:22.04    # optional — passed as _docker_image
```

If `docker_image` is set, the worker uses its container runtime.
If not, the worker runs the command bare.

---

## 4. Feasibility Assessment

### 4.1 What Already Exists and Works

| Component | Status | Notes |
|-----------|--------|-------|
| Async executor pattern | Done | BV-BRC executor proves the model |
| Scheduler polling loop | Done | Phase 3 handles QUEUED/RUNNING automatically |
| Executor registry | Done | Just register `WorkerExecutor` at bootstrap |
| Task state machine | Done | States cover the full lifecycle |
| Reserved input keys | Done | `_base_command`, `_output_globs`, `_docker_image` |
| Input resolution | Done | Scheduler resolves before dispatch |
| CWL hint system | Done | `goweHint.executor` already routes to executors |
| Docker execution code | Done | DockerExecutor has container run/collect logic |
| HTTP API framework | Done | chi/v5 router, response envelope pattern |
| SQLite persistence | Done | Store interface with full task CRUD |

**Conclusion:** ~60% of the plumbing already exists. The WorkerExecutor is thinner than BVBRCExecutor because workers push state updates directly.

### 4.2 What Needs to Be Built

| Component | Effort | Risk |
|-----------|--------|------|
| Worker model + store tables | Small | Low — follows existing patterns |
| Worker API endpoints (6 routes) | Medium | Low — same pattern as submission API |
| WorkerExecutor (server-side) | Small | Low — thinnest executor yet |
| Worker binary (`cmd/worker/main.go`) | Medium | Medium — new process, needs testing |
| Docker runtime adapter | Small | Low — can reuse DockerExecutor logic |
| Apptainer runtime adapter | Medium | Medium — new runtime, needs research |
| Task queue query (store) | Small | Low — SQL with WHERE + LIMIT 1 |
| Worker liveness/timeout handling | Medium | Medium — needs TTL, re-queue logic |
| Output collection + upload | Medium | Medium — file transfer from worker |

### 4.3 Estimated Scope

| Metric | Estimate |
|--------|----------|
| New files | ~8-10 |
| Modified files | ~4-5 |
| New Go code | ~1500-2000 lines |
| New tests | ~500-800 lines |
| Database migrations | 1 (workers table) |

---

## 5. Risk Analysis (Revised)

### 5.1 Data Staging Architecture (was: Output File Transfer)

**Original risk:** How do workers transfer files? No object store exists.

**Resolution:** Workers are responsible for all data staging. The behavior is
**configurable per worker** via `--stage-out` mode (see Section 3.3E).
Three storage backends map to the existing CWL location scheme:

| Scheme | Stage-In | Stage-Out |
|--------|----------|-----------|
| `file://` | Copy/symlink to workdir | Copy from workdir to target path |
| `shock://` | HTTP GET from Shock server | HTTP PUT to Shock server |
| `ws://` | Download via Workspace API | Upload via Workspace API |

**Key design principles:**
1. Workdir is always local — inputs staged in, command runs locally, outputs staged out
2. Only initial inputs and final outputs MUST be staged — intermediates only if federated
3. Worker config controls WHERE outputs go — the tool only defines WHAT to collect
4. Stage-out mode determines whether multi-worker pipelines work

**What the worker reports back (no file upload to GoWe server):**
```json
{
  "state": "success",
  "exit_code": 0,
  "stdout": "...",
  "stderr": "...",
  "outputs": {
    "contigs": {"class": "File", "location": "file:///shared/results/contigs.fa"}
  }
}
```

The output location URI is determined by the worker's `--stage-out` config.
The server stores it as-is. The next step's worker stages in from that URI.

### 5.2 Concurrent Task Checkout

**Original risk:** Two workers request work simultaneously, both get the same task.

**Resolution:** Use a Go channel-based dispatcher (not SQL-level locking):

```go
type WorkQueue struct {
    checkoutCh chan checkoutRequest  // serialized by single goroutine
}

type checkoutRequest struct {
    worker   *model.Worker
    resultCh chan *model.Task  // nil = no work available
}
```

A single dispatcher goroutine reads from `checkoutCh`, finds the next matching
task, transitions it to RUNNING, and sends it back on `resultCh`. Natural
serialization — no mutex, no SQL race. Same pattern as Go's standard
`net/http` connection pool.

### 5.3 Container Runtime Abstraction

**Original risk:** Apptainer CLI differs from Docker.

**Resolution:** The CLIs are structurally equivalent for our use case:

```
Docker:    docker run --rm -v workdir:/work -w /work image cmd...
Apptainer: apptainer exec --bind workdir:/work --pwd /work image.sif cmd...
```

Both accept `--bind`/`-v` for mounts, both run a command in a container.
Apptainer can pull Docker images directly (`docker://ubuntu:22.04`), so no
separate image management is needed for v1.

The `Runtime` interface is a thin flag mapper:

```go
type Runtime interface {
    Run(ctx context.Context, spec RunSpec) (RunResult, error)
}
```

Three implementations: `DockerRuntime`, `ApptainerRuntime`, `BareRuntime`.
Each just builds the right CLI args from the same `RunSpec`. This is the same
abstraction as the existing `CommandRunner` interface in
`internal/executor/docker.go`.

### 5.4 Remaining Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| **Stale workers** — crash without deregister, tasks stuck RUNNING | Medium | TTL heartbeat timeout (3x interval) → mark offline, re-queue tasks |
| **Worker process lifecycle** — deployment on remote machines | Medium | Single binary `gowe worker`. Users deploy via systemd/docker-compose/k8s. No orchestrator. |
| **Shock/Workspace credentials on worker** — staging needs auth tokens | Medium | Worker config takes token paths. Same pattern as BV-BRC executor (`~/.patric_token`). |
| **Large file staging latency** — staging GB-scale inputs adds overhead | Low | Symlink for `file://` on shared FS. Parallel staging for multiple inputs. Accept latency for remote storage. |
| **Worker auth** — unauthenticated worker API | Low | Shared secret / bearer token in v2. LAN-only for v1. |

---

## 6. Implementation Phases

### Phase 1: Server-Side Foundation (Worker Model + API + Queue)
- Add `Worker` model to `pkg/model/`
- Add `WorkerState` state machine (online, offline, draining)
- Add `ExecutorTypeWorker` to `pkg/model/state.go`
- Add `workers` table + migration to SQLite store
- Add `WorkerExecutor` to `internal/executor/` (thin — just enqueues)
- Add channel-based `WorkQueue` dispatcher (serialized checkout)
- Add 6 worker API endpoints to `internal/server/`
- Register `WorkerExecutor` in bootstrap (`cmd/server/main.go`)

### Phase 2: Worker Binary (Docker + Bare Runtime)
- Create `cmd/worker/main.go` — CLI flags, registration, heartbeat, work loop
  (or `gowe worker` subcommand in existing `cmd/cli/`)
- Create `internal/worker/runtime.go` — Runtime interface + DockerRuntime + BareRuntime
- Create `internal/worker/stager.go` — Stager interface + FileStager
  (symlink/copy for `file://` locations)
- Output collection via `_output_globs` after execution
- Stage outputs back to `file://` destinations
- Report completion with output manifest to server

### Phase 3: Remote Storage Stagers + Apptainer
- Add `ShockStager` — HTTP GET/PUT for `shock://` locations
- Add `WorkspaceStager` — BV-BRC Workspace download/upload for `ws://` locations
- Add `ApptainerRuntime` — `apptainer exec --bind` flag mapping
  (same RunSpec, different CLI args; `docker://` prefix for OCI images)

### Phase 4: Hardening
- Stale worker detection: TTL heartbeat timeout → mark offline, re-queue tasks
- Worker draining: graceful shutdown finishes current task, stops pulling
- Heartbeat instructions: server can tell worker to discard/stop (AWE pattern)
- Task timeout: max execution time per task, killed if exceeded
- Worker auth: shared secret / bearer token for worker API endpoints

---

## 7. Comparison: AWE vs GoWe Worker Approach

| Aspect | AWE | GoWe (Proposed) |
|--------|-----|-----------------|
| Protocol | HTTP REST | HTTP REST (same) |
| Work distribution | Worker pull | Worker pull (same) |
| Data staging | Shock object store | HTTP upload / shared FS |
| Container support | Docker only | Docker + Apptainer |
| Queue implementation | In-memory (ServerMgr) | SQLite (persisted) |
| Worker pipeline | 4 goroutine stages | Single loop (simpler) |
| CWL integration | Full CWL runner | CWL hints + resolved inputs |
| Authentication | Client group tokens | Bearer tokens (v2) |
| Server restart | Workers must restart | Workers re-register (resilient) |
| Complexity | ~15k lines | ~2k lines (estimated) |

**GoWe advantage:** The existing async executor pattern means the scheduler needs zero changes. AWE built its own scheduler; GoWe's scheduler already handles async executors generically.

---

## 8. Decision Points

| # | Question | Resolution |
|---|----------|------------|
| 1 | Output transfer mechanism | Workers stage data themselves via URI scheme stagers. No upload to GoWe server. Workers report output location URIs. |
| 2 | Concurrent checkout | Channel-based dispatcher goroutine (natural serialization, no SQL locking). |
| 3 | Apptainer vs Docker | Same Runtime interface, just different CLI flags. Both supported from Phase 2/3. |

**Still open:**

4. **Worker binary** — `gowe worker` subcommand (single binary) or separate `gowe-worker` binary?
5. **Worker authentication** — None for v1 (LAN-only), or shared secret from day one?
6. **Task assignment** — Pure FIFO, or runtime-capability-based from day one?

---

## 9. Recommendation

**Feasibility: HIGH.** GoWe's architecture is already designed for this. The BV-BRC executor proves the async pattern works. The effort is moderate (~2000 lines) with low architectural risk.

**Recommended approach:**
- Start with Phase 1+2 (server-side + Docker worker)
- Use the existing `gowe` binary with a `worker` subcommand (`gowe worker --server ...`)
- FIFO queue with runtime capability matching
- HTTP upload for output files
- No auth for v1 (add in Phase 4)
- Apptainer in Phase 3 (after Docker is proven)
