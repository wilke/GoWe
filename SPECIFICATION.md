# GoWe Specification

> **Status**: Living specification · **Version**: draft-3 (2026-07-06)
> **Applies to**: GoWe server, worker, CLI, and `cwl-runner`
> **Companion documents**: [`docs/adr/`](docs/adr/) (why), [`docs/cwl-hints.md`](docs/cwl-hints.md)
> (hint reference), [`docs/GoWe-Vocabulary.md`](docs/GoWe-Vocabulary.md) (concepts),
> [`CONFORMANCE.md`](CONFORMANCE.md) (CWL conformance)

This document is the normative reference for how GoWe interprets workflows and executes
them. It consolidates the vocabulary, the CWL hint extensions, the execution model, and the
worker/API contracts into one place. Where this specification and prose docs disagree, this
document governs; where this document and the running code disagree, that is a bug in one of
them — file it.

---

## 1. Conformance language

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as
defined in RFC 2119. They apply to GoWe implementations (server, worker, runner), not to
workflow authors, except where a statement is explicitly about a valid workflow document.

---

## 2. Scope

GoWe is a workflow engine that:

- accepts workflows and tools written in **CWL v1.2** (§4);
- runs them through a **three-level execution model** (§6) driven by a **tick-based
  scheduler** (§7);
- dispatches concrete work to **five interchangeable executor backends** (§8);
- distributes work to remote machines through a **pull-based worker protocol** (§9);
- persists all state in an embedded **SQLite** store (§11) and exposes an **HTTP API** (§12).

Out of scope for this document: the internal CWL expression engine, the BV-BRC JSON-RPC wire
format (see [`docs/BVBRC-API.md`](docs/BVBRC-API.md)), and the web UI.

---

## 3. Terminology (normative)

These terms are normative. Code, docs, and API responses MUST use them consistently.

| Term | Definition |
|------|------------|
| **Workflow** | A declarative DAG of Steps with typed Inputs and Outputs. CWL `class: Workflow`. Describes *what* to compute, never *how* or *where*. |
| **Tool** | A reusable single operation. CWL `class: CommandLineTool` or `ExpressionTool`. The unit of reuse. |
| **Step** | One node in a Workflow's DAG; binds a Tool (or sub-Workflow) to input sources. Not executable alone. |
| **Submission** (a.k.a. **Run**) | One execution of a Workflow with concrete input values. Top-level tracking entity. |
| **StepInstance** | One runtime instance of a Step within a Submission. A scatter Step yields **N** StepInstances. |
| **Task** | A concrete, schedulable unit of work bound to resolved inputs and assigned to an Executor. |
| **Executor** | A pluggable backend that knows *how* to run a Task in one environment. |
| **Scheduler** | The engine loop that advances the model, resolves dependencies, dispatches, retries, and finalizes. |
| **Worker** | A remote process that pulls Tasks from the server and executes them. |

Disambiguation: "job" (BV-BRC) ≈ **Task**; "job" (AWE) ≈ **Submission**; "pipeline" is an
informal synonym for **Workflow**. Code and docs MUST use the normative term.

---

## 4. Workflow definition

Rationale for adopting CWL as the sole definition format:
[ADR-0001](docs/adr/0001-adopt-cwl-v1.2-as-workflow-definition-format.md).

### 4.1 Format

A workflow or tool definition MUST be a CWL v1.2 YAML or JSON document. GoWe supports:

- `class: Workflow`, `class: CommandLineTool`, `class: ExpressionTool`;
- `$graph` bundles (resolved by the bundler);
- typed inputs/outputs (`File`, `Directory`, `string`, `int`, `long`, `float`, `double`,
  `boolean`, records, arrays, enums, and `null`/optional unions);
- `scatter` with `scatterMethod` ∈ {`dotproduct`, `flat_crossproduct`, `nested_crossproduct`};
- conditional steps via `when` (v1.2);
- standard requirements/hints including `DockerRequirement`, `ResourceRequirement`,
  `InlineJavascriptRequirement`, `InitialWorkDirRequirement`, `EnvVarRequirement`,
  `SecondaryFiles`, and `loadContents`.

### 4.2 Portability requirement

A conforming workflow MUST remain valid CWL that other engines can parse. All GoWe-specific
metadata MUST be expressed as namespaced hints (§5) so that engines which do not understand
them ignore them rather than failing.

### 4.3 Conformance

GoWe tracks the upstream CWL v1.2 conformance suite (378 tests). Supported execution modes
and their current pass rates are recorded in [`docs/Execution-Modes.md`](docs/Execution-Modes.md)
and [`CONFORMANCE.md`](CONFORMANCE.md). Known deviations (e.g. Apptainer network isolation,
test 227) MUST be documented there.

---

## 5. GoWe hint extensions (normative)

All extensions live under the namespace:

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#
```

Extensions MUST be placed in `hints` unless noted. Rationale: [ADR-0002](docs/adr/0002-extend-cwl-via-namespaced-hints.md).

### 5.1 `gowe:Execution`

Controls placement of a Tool/Step. Recognized **only in `hints`**. A legacy `goweHint`
alias is accepted for backward compatibility; new documents SHOULD use `gowe:Execution`.

| Field | Type | Meaning |
|-------|------|---------|
| `executor` | string | Backend: `local`, `worker`, or `bvbrc`. `container` is not a routing value. |
| `worker_group` | string | Target worker group (e.g. `esmfold`, `gpu`). |
| `bvbrc_app_id` | string | BV-BRC application id; implies `executor: bvbrc`. |
| `docker_image` | string | Container image override. Takes priority over `DockerRequirement.dockerPull`. Feeds both Docker and Apptainer (§8.3). |
| `gpu` | boolean | Request GPU passthrough. |
| `inject_bvbrc_token` | boolean | Inject the caller's BV-BRC token into the task environment. |

### 5.2 `gowe:ResourceData`

Declares large reference datasets a Tool depends on. Accepted in `hints` or `requirements`.
Rationale: [ADR-0008](docs/adr/0008-dataset-affinity-and-staging-modes.md).

```yaml
gowe:ResourceData:
  datasets:
    - id: alphafold        # MUST match a worker-advertised dataset id
      path: /local_databases/alphafold
      size: 2TB            # informational
      mode: prestage       # prestage | cache (default: cache)
      source: shock://...  # optional; reserved for on-demand caching
```

| `mode` | Scheduler behavior |
|--------|--------------------|
| `prestage` | Task MUST be dispatched only to a worker advertising this dataset id; it waits otherwise. |
| `cache` | Task SHOULD prefer such a worker but MAY run elsewhere. |

A dataset entry without an `id` MUST be ignored. An unknown/empty `mode` MUST default to
`cache`.

### 5.3 `DockerRequirement` (standard CWL, extended resolution)

`DockerRequirement.dockerPull` is honored when present. GoWe resolves the image value by
pattern:

| Value | Resolution |
|-------|------------|
| ends with `.sif` (relative) | local Apptainer image, resolved against `--image-dir` |
| absolute `.sif` path | used as-is |
| anything else | pulled as `docker://<value>` |

If both `gowe:Execution.docker_image` and `DockerRequirement.dockerPull` are set, the
former wins.

---

## 6. Execution model (normative)

Execution is a three-level hierarchy; each level has its own state machine and illegal
transitions MUST be rejected (`pkg/model`). Rationale: [ADR-0003](docs/adr/0003-three-level-state-hierarchy.md).

```
Submission                       one run of a workflow
  └── StepInstance               one per step; scatter ⇒ N
        └── Task                 concrete work unit
```

### 6.1 Submission states

`PENDING → RUNNING → COMPLETED | FAILED | CANCELLED`

| State | Meaning |
|-------|---------|
| PENDING | Accepted; StepInstances/Tasks being created. |
| RUNNING | At least one StepInstance is active. |
| COMPLETED | All StepInstances terminal and none failed. |
| FAILED | A StepInstance failed with no retries remaining. |
| CANCELLED | Cancelled by the user. |

### 6.2 StepInstance states

`WAITING → READY → DISPATCHED → RUNNING → COMPLETED | FAILED | SKIPPED`

| State | Meaning |
|-------|---------|
| WAITING | Upstream dependencies not yet satisfied. |
| READY | All inputs available; eligible for dispatch. |
| DISPATCHED | Tasks created and submitted to executors. |
| RUNNING | At least one Task running. |
| COMPLETED / FAILED | All Tasks terminal, success/failure aggregated. |
| SKIPPED | `when` evaluated false, **or** the StepInstance was non-terminal when its Submission was cancelled. |

### 6.3 Task states

`PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS | FAILED | SKIPPED`,
with `FAILED → RETRYING → QUEUED` while retries remain.

| State | Meaning |
|-------|---------|
| PENDING | Created; awaiting input resolution. |
| SCHEDULED | Inputs resolved; scheduler decided it runs. |
| QUEUED | Handed to an executor / awaiting worker checkout. |
| RUNNING | Executing. |
| SUCCESS / FAILED | Terminal outcome. |
| RETRYING | Failed but eligible for retry; re-enters QUEUED. |
| SKIPPED | Conditional skip (`when` false), **or** cancellation of a non-terminal Task when its Submission is cancelled. |

> Worker-bound tasks MAY transition directly `PENDING → QUEUED`, bypassing `SCHEDULED`, so
> they are immediately available for checkout.

### 6.4 State propagation

State MUST propagate upward only: Task terminality drives StepInstance advancement (§7 phase
4), and StepInstance terminality drives Submission finalization (§7 phase 5).

---

## 7. Scheduler semantics (normative)

The scheduler runs a periodic **tick** (default ~2 s). Each tick MUST execute the following
phases in order; a phase that errors MUST NOT silently skip later ticks. Reference:
`internal/scheduler/loop.go`.

| Phase | Action |
|-------|--------|
| 1 | Advance `WAITING → READY` StepInstances whose dependencies are all met. |
| 1.5 | (Server mode) Pre-stage workspace inputs for PENDING submissions. |
| 2 | Dispatch `READY` StepInstances: resolve inputs, create Tasks, submit to executors. |
| 2.5 | Re-submit `RETRYING` tasks. |
| 3 | Poll `QUEUED`/`RUNNING` tasks on async executors for status. |
| 3.5 | Detect stuck `QUEUED` worker tasks (progress-based) and recover them. |
| 4 | Advance `DISPATCHED`/`RUNNING` StepInstances when all their Tasks are terminal. |
| 5 | Finalize submissions whose StepInstances are all terminal. |
| 5.5 | (Server mode) Post-stage outputs to the workspace for completed submissions. |
| 6 | Transition newly `FAILED` tasks to `RETRYING` while attempts remain. |

### 7.1 Dependency resolution

A StepInstance MUST NOT advance out of `WAITING` until every input source it references
(workflow inputs or upstream step outputs) is available. Resolution operates at the
StepInstance level.

### 7.2 Scatter

A scatter Step MUST expand into N StepInstances according to its `scatterMethod`. Each
StepInstance owns its own Tasks and advances independently.

### 7.3 Retry

On Task `FAILED`, the scheduler MUST transition it to `RETRYING` and re-queue it if and only
if retry attempts remain per the task's retry policy; otherwise the failure propagates to the
StepInstance and Submission.

---

## 8. Executor model (normative)

### 8.1 Interface

Every backend MUST implement `Submit`, `Status`, `Cancel`, and `Logs`
(`internal/executor/executor.go`) and register in the registry
(`internal/executor/registry.go`). Rationale: [ADR-0005](docs/adr/0005-pluggable-executor-registry.md).

| Backend | Kind | Runs a Task by… |
|---------|------|-----------------|
| `local` | sync | direct host process (`os/exec`). |
| `docker` | sync | Docker container. |
| `apptainer` | sync | Apptainer/Singularity container (`.sif`, `--nv`, binds). |
| `worker` | async | queuing for a remote pull-worker (§9). |
| `bvbrc` | async | BV-BRC JSON-RPC `start_app` + `query_tasks` polling ([ADR-0007](docs/adr/0007-generic-bvbrc-executor-over-operators.md)). |

### 8.2 Selection order (first match wins)

The scheduler MUST select a Task's backend in this order:

1. Server `--force-executor`, if set — forces every Task to this backend, ignoring all hints
   and `--default-executor`. Intended for testing only.
2. Server `--default-executor`, if set — overrides all hints.
3. `gowe:Execution.executor` (`worker` | `bvbrc` | `local`).
4. `gowe:Execution.bvbrc_app_id` present ⇒ `bvbrc`.
5. `DockerRequirement` or `gowe:Execution.docker_image` present ⇒ `worker` when workers are
   online, else `local`.
6. Default ⇒ `local`.

### 8.3 Container image resolution

Both `docker` and `apptainer` backends consume a single image string (from
`gowe:Execution.docker_image` or `DockerRequirement.dockerPull`), resolved per §5.3. The
field name `docker_image` is historical; it governs whichever container runtime executes the
task.

---

## 9. Worker protocol (normative)

Workers **pull**; the server MUST NOT push work. Rationale: [ADR-0004](docs/adr/0004-pull-based-worker-model.md).
Workers authenticate with the `X-Worker-Key` header.

### 9.1 Lifecycle

1. **Register** — `POST /api/v1/workers` advertising capabilities: container runtime
   (`docker`/`apptainer`/`none`), worker group, GPU, and available dataset ids.
2. **Poll** — `GET /api/v1/workers/{id}/work` (default ~5 s). Returns the next matching Task,
   or HTTP 204 if none.
3. **Execute** — via `internal/toolexec`: stage inputs, launch the container/process, apply
   mounts, GPU, and injected secrets.
4. **Report** — `PUT /api/v1/workers/{id}/tasks/{tid}/status` for progress, then
   `PUT …/tasks/{tid}/complete` with the result.
5. **Heartbeat** — `PUT /api/v1/workers/{id}/heartbeat` periodically.
6. **Deregister** — `DELETE /api/v1/workers/{id}`.

### 9.2 Checkout matching

The server MUST claim a Task atomically (`store.CheckoutTask`) such that at most one worker
receives it. A Task is eligible for a worker only if:

- the worker's container runtime satisfies the Task's requirement; **and**
- the worker's group matches the Task's `worker_group` (step-level overrides the
  submission-level `--group` fallback); **and**
- dataset affinity is satisfied: every `prestage` dataset id is advertised by the worker;
  `cache` dataset matches are preferred but not required.

### 9.3 Liveness and recovery

The scheduler MUST treat a worker as dead after missed heartbeats and MUST return its
in-flight and stuck `QUEUED` tasks for re-dispatch (§7 phase 3.5). Secrets injected into a
task MUST NOT be transmitted to the server.

---

## 10. Inputs & storage (normative)

This section consolidates how a run's **inputs** are supplied and how **File/Directory data**
is moved in and out across the supported storage backends. Rationale:
[ADR-0008](docs/adr/0008-dataset-affinity-and-staging-modes.md). Operational detail lives in
[`docs/Execution-Modes.md`](docs/Execution-Modes.md) and [`docs/tools/worker.md`](docs/tools/worker.md).

### 10.1 Job inputs

A **Submission** is a workflow plus a set of concrete input values (the *job*). Inputs are
supplied at submit time, not configured as a backend:

- **CLI** — `gowe submit <workflow> --inputs <file>`, where the file is a YAML/JSON job
  document. Relative `File`/`Directory` paths MUST be resolved against the job file's own
  directory before submission (`internal/cli/submit.go`, `bundle.ResolveFilePaths`).
- **API** — `POST /api/v1/submissions` with the input values in the request body.

Each `File`/`Directory` input carries a `location`; its URI scheme selects the storage
backend in §10.2. A location with no scheme (or `file://`) is a local/shared-filesystem path.

### 10.2 Storage backends

Staging is a pluggable backend (`pkg/staging`) selected by the `location` URI scheme via the
`Stager` interface (`pkg/staging/staging.go`, `ParseLocation`); `composite.go` dispatches by
scheme. Each backend handles both stage-in (inputs) and stage-out (outputs).

| Scheme | Backend file | Behavior | Status |
|--------|--------------|----------|--------|
| `local` / *(none)* | `file.go` | in place, no copy (`cwl-runner` default) | supported |
| `file://` | `shared.go` | copy to a shared filesystem path (distributed workers) | supported |
| `http(s)://` | *(http stager)* | HTTP PUT/POST upload, custom headers/auth/retry | defined |
| `shock://` | `shock.go` | Shock data service (`shock://host/node/{id}`) | defined |
| `ws://` | `workspace.go` | BV-BRC Workspace service (default for BV-BRC runs) | defined |
| `s3://` | `s3.go` | S3-compatible object storage | planned |

Backends marked *defined*/*planned* are implemented to varying degrees but not fully covered
by the conformance suite; see the test matrix in [`docs/Execution-Modes.md`](docs/Execution-Modes.md).
Staging supports **copy**, **symlink**, and **reference** modes.

> **`ws://` (BV-BRC Workspace)** is *defined* — implemented end to end (submission, output
> mapping, the stager, and server-side pre/post-staging) but not CI-verified, since it requires
> a live BV-BRC service and a real user token. See
> [`docs/BVBRC-Workspace-Deep-Dive.md`](docs/BVBRC-Workspace-Deep-Dive.md) for the full
> submission/result round-trip and the specific gaps (wildcard-glob resolution, large-file
> streaming, recursive directory download).

### 10.3 Server-side staging and upload proxy

When the server stages on behalf of a run (`cmd/server`):

- `--workspace-staging server` with `--workspace-url <url>` — pre-stage `ws://` inputs and
  post-stage outputs to a BV-BRC Workspace on the server (§7 phases 1.5 / 5.5). Empty =
  passthrough to workers.
- `--upload-backend shock|s3|local` selects the file-upload proxy backend, configured by:
  - Shock — `--upload-shock-host`, `--upload-shock-http`, `--upload-shock-token`.
  - S3 — `--upload-s3-endpoint`, `--upload-s3-region`, `--upload-s3-bucket`, `--upload-s3-prefix`,
    `--upload-s3-access-key`, `--upload-s3-secret-key`, `--upload-s3-path-style`, `--upload-s3-disable-ssl`.
  - Local — `--upload-local-dir`; `--upload-download-dirs` allow-lists download roots.

### 10.4 Worker-side stage-in

Workers resolve and stage a task's inputs before execution using their configured stagers
(`internal/worker/stager_config.go`, `stagein.go`), then stage outputs back to the run's
target scheme. `--extra-bind` injects arbitrary host paths into containers for plumbing and
MUST NOT influence scheduling (only `gowe:ResourceData` does).

### 10.5 Reference data (large read-only inputs)

Large, pre-positioned datasets (databases, model weights) are **not** carried as `File`
inputs. They are declared with the `gowe:ResourceData` hint (§5.2) and matched to workers by
affinity (§9.2): `prestage` requires a worker advertising the dataset, `cache` prefers one.
Workers advertise datasets via `--pre-stage-dir` or `--dataset id=path`.

---

## 11. Persistence (normative)

State MUST be persisted to SQLite via `modernc.org/sqlite` (pure Go, no CGO), with
`max_open_conns = 1` (single writer), WAL mode, and foreign keys enforced. Rationale:
[ADR-0006](docs/adr/0006-sqlite-single-writer-persistence.md). Core tables: `workflows`
(deduped by content hash), `submissions`, `step_instances`, `tasks`, `workers`, plus `users`,
`sessions`, and label vocabulary. Schema migrations MUST be idempotent
(`addColumnIfNotExists`; `internal/store/migrations.go`). Task checkout MUST be a single
atomic statement backed by the `(state, executor_type)` index.

---

## 12. HTTP API (normative surface)

All endpoints live under `/api/v1`. Responses use the envelope
`{ status, request_id, timestamp, data }`. Auth: user endpoints via the API auth
middleware; worker endpoints via `X-Worker-Key`; admin endpoints require the admin role.

| Group | Representative endpoints |
|-------|--------------------------|
| Health | `GET /health` |
| Workflows | `GET/POST /workflows`, `GET/PUT/DELETE /workflows/{id}`, `GET …/inputs`, `GET …/outputs`, `POST …/validate` |
| Submissions | `GET/POST /submissions`, `GET /submissions/{id}`, `PUT …/cancel`, `PUT …/retry`, `GET …/tasks`, `GET …/tasks/{tid}/logs` |
| Workers | `POST /workers`, `GET /workers`, `GET /workers/{id}/work`, `PUT /workers/{id}/heartbeat`, `PUT …/tasks/{tid}/status`, `PUT …/tasks/{tid}/complete`, `DELETE /workers/{id}` |
| BV-BRC proxy | `GET /apps`, `GET /apps/{id}`, `GET /apps/{id}/cwl-tool`, `GET /workspace`, file up/download |
| Streaming | `GET /sse/submissions/{id}` (server-sent events) |
| Admin | `GET /admin/tasks/active`, `PUT /admin/tasks/{tid}/priority`, user/role and label management |

The full route table is defined in `internal/server/`.

---

## 13. Security (normative)

GoWe separates credentials into four planes and holds them together with one invariant:
**execution secrets MUST NOT travel back to the server** (§13.2). Identity is delegated to
external providers — GoWe stores no user passwords. Rationale for the auth model:
[ADR-0009](docs/adr/0009-delegated-identity-and-optional-worker-keys.md).

### 13.1 Credential planes

| Plane | Boundary | Carrier | Rule |
|-------|----------|---------|------|
| **User / API token** | client → server | `Authorization` (BV-BRC) or `X-MG-RAST-Token` (MG-RAST) header | Verified per request (§13.3). |
| **Worker key** | worker → server | `X-Worker-Key` header | Authenticates and scopes a worker to groups (§13.3). |
| **Worker secrets** | worker-local | `--secret`, `--secret-file` | Injected into the container env on the worker only; MUST NOT reach the server (§13.5). |
| **Delegated BV-BRC token** | server → container | task `RuntimeHints` → `BVBRC_TOKEN` env | The submitter's own token, injected only when opted in (§13.5). |

### 13.2 Trust boundary invariant

- Credentials MUST NOT appear in workflow or tool definitions — they are inert data (§3).
- Worker secrets and any token injected into a task's container environment MUST NOT be
  transmitted to, logged by, or persisted by the server. The worker→server return path
  carries task status and results only.
- A worker MUST treat injected secrets as process-local: held in memory, written only into
  the container environment at execution time, never echoed to the server or to logs.

### 13.3 Authentication schemes

| Scheme | Presented as | The server verifies | Applies to |
|--------|--------------|---------------------|------------|
| User token | `Authorization: Bearer <token>` or `X-MG-RAST-Token` | Provider-issued pipe-delimited token: non-empty username and unexpired `expiry` | User/API endpoints |
| Anonymous | *(no token)* + server `--allow-anonymous` | Nothing; request runs as the anonymous user | User/API endpoints, if enabled |
| Worker key | `X-Worker-Key` | Key present in the configured key set; yields allowed groups | Worker endpoints |

- When a token is presented, the server MUST reject it if the username is empty or the token
  is expired (`401`).
- On first valid use of a provider token, the server MUST look up or create the corresponding
  user account keyed by `(username, provider)`.
- When no user keys/tokens apply and anonymous access is disabled, protected endpoints MUST
  return `401`.
- Worker-key enforcement is **optional**: if no keys are configured, worker endpoints are
  open. Operators SHOULD configure keys in any multi-tenant or networked deployment.

### 13.4 Authorization and roles

- Roles are `user`, `admin`, and `anonymous` (`pkg/model`).
- Admin membership is resolved from (highest priority first) the stored user role, the
  `--admins` flag, the `GOWE_ADMINS` env var, and the config file. A user matching admin
  config MUST be promoted on authentication.
- `/api/v1/admin/*` endpoints MUST require the `admin` role (`403` otherwise).
- Anonymous submissions MUST be restricted to the executors allowed by
  `--anonymous-executors` (default `local,docker,worker`).

### 13.5 Secrets and token handling

- Worker secrets are loaded from `--secret`/`--secret-file` into worker memory. Secret
  **names** MAY be logged; secret **values** MUST NOT be logged.
- The `X-Worker-Key` MUST NOT be logged in the clear; only a hash of it MAY be logged.
- A BV-BRC token MAY be injected into a task's container as `BVBRC_TOKEN` when, and only when,
  `gowe:Execution.inject_bvbrc_token` is set (or a workspace stager requires it); the injected
  token is the submitter's own, so the job runs under the user's identity. This opt-in gate is
  a deliberate least-privilege boundary: a token MUST NOT be exposed to a tool that did not
  request it. (A proposed change, PR #132 / issue #133, injects the token into every worker
  task unconditionally and adds a `KB_AUTH_TOKEN` alias; if adopted, this clause and §13.1
  MUST be revised, and the least-privilege trade-off recorded in an ADR.)
- Response bodies MUST NOT expose stored tokens: submission token fields are serialized with
  `json:"-"`.

### 13.6 Transport security

- Worker→server transport MAY be TLS; workers support a custom CA (`--ca-cert`) and, for
  testing only, `--insecure` to skip verification. `--insecure` MUST NOT be used in
  production.
- The server does not currently terminate TLS itself (see §13.7); production deployments
  SHOULD run it behind a TLS-terminating proxy.

### 13.7 Known limitations (informative)

These are current-state gaps, not normative requirements. They are documented so operators
can compensate and so the project can track hardening. Each SHOULD be addressed before a
security-sensitive production deployment:

- **Tokens at rest are plaintext.** The submitter's BV-BRC token is persisted unencrypted in
  `submissions.user_token` (SQLite). Protect the database file accordingly.
- **Worker keys are shared secrets.** Keys are stored plaintext in config/env with no
  per-worker identity or rotation; a leaked key affects every worker sharing it.
- **Server TLS is not enforced.** Cookie `Secure` is hardcoded off (a standing TODO) and the
  server serves plain HTTP; front it with a TLS terminator.
- **Anonymous mode widens exposure.** If `--allow-anonymous` is enabled, always scope it with
  `--anonymous-executors`.

---

## 14. Entry points

| Binary | Role |
|--------|------|
| `gowe` (`cmd/cli`) | User CLI: submit and inspect workflows via the API. |
| `gowe-server` (`cmd/server`) | Control plane: HTTP API + scheduler + store. |
| `gowe-worker` (`cmd/worker`) | Remote pull-worker daemon. |
| `cwl-runner` (`cmd/cwl-runner`) | Standalone, serverless CWL runner (conformance/local). |

---

## 15. Versioning of this specification

This specification is versioned independently of the software. A change that alters required
behavior MUST bump the version line at the top and SHOULD be accompanied by a new or updated
ADR. The specification describes the intended contract; divergence in code is a defect to
reconcile against this document or a prompt to amend it.
