# GoWe CWL Hints Reference

GoWe extends CWL with custom hints for executor routing, container selection, and reference data management. These are CWL-compliant extensions — other engines safely ignore them.

## Where to Place GoWe Hints

All GoWe-specific extensions should go in **`hints`**, not `requirements`. In CWL:

- **`requirements`** — the engine MUST support this or reject the document
- **`hints`** — the engine SHOULD support this but MAY ignore it

Using `hints` ensures your CWL workflows remain portable: cwltool, Toil, and other engines will skip unknown hints rather than failing.

> **Parser behavior**: GoWe accepts `DockerRequirement`, `gowe:ResourceData` and `gowe:Service` from either `hints` or `requirements`. `gowe:Execution` is only recognized in `hints`. The parser also accepts legacy `goweHint` for backward compatibility, but new CWL files should use `gowe:Execution`.

---

## `gowe:Execution` — Executor Routing

Controls which execution backend GoWe uses for a tool.

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    executor: worker          # Execution backend
    bvbrc_app_id: GenomeAssembly2  # BV-BRC application ID
    docker_image: "myimage:latest" # Override container image
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `executor` | string | Execution backend: `local`, `worker`, `bvbrc` |
| `worker_group` | string | Target worker group for task routing (e.g., `gpu`, `esmfold`) |
| `bvbrc_app_id` | string | BV-BRC application ID (implies `executor: bvbrc`) |
| `docker_image` | string | Container image override (takes priority over `DockerRequirement.dockerPull`) |
| `gpu` | boolean | `true`: the tool needs one GPU (a `--gpu` worker) |
| `gpu_count` | integer >= 1 | Number of GPUs the tool needs; wins over `gpu`. Anything else (0, a string, an expression) fails registration |

### GPUs

A tool asks for GPUs with `gowe:Execution` (`gpu: true` = 1 GPU, `gpu_count: n` = n GPUs) or with cwltool's
`CUDARequirement`, in `hints` or `requirements`:

```yaml
$namespaces:
  cwltool: http://commonwl.org/cwltool#

requirements:
  cwltool:CUDARequirement:
    cudaDeviceCountMin: 2     # GPU count; 1 when absent
    cudaVersionMin: "11.4"    # accepted, ignored
```

- `CUDARequirement` is recognized as `cwltool:CUDARequirement`, as the full URI
  `http://commonwl.org/cwltool#CUDARequirement`, and under any prefix the document's `$namespaces` maps to
  `http://commonwl.org/cwltool#`. A declaration under `requirements` wins over one under `hints`.
- The count is `cudaDeviceCountMin`, 1 when absent. It must be an integer >= 1: a CWL expression or 0 fails
  registration. `cudaDeviceCountMax`, `cudaVersionMin` and `cudaComputeCapability` are accepted and ignored (no CUDA
  version or compute capability matching).
- With both a `gowe:Execution` GPU field and `CUDARequirement`, the larger count applies.
- A workflow step can set the count too (`gowe:Execution` under the step's `hints`, `CUDARequirement` under its
  `hints` or `requirements`); the larger of the step and tool counts is used. Only `CUDARequirement` is read from a step's
  `requirements`. Workflow-level declarations are not read.
- The scheduler routes the task to a `--gpu` worker that can give it that many GPUs (the server counts GPUs per worker,
  see the server's capacity accounting). On a worker running several tasks at once the task gets exactly that many
  devices; at `--concurrency 1` it gets the worker's whole GPU configuration.
- `runtime.cudaDeviceCount` is not provided to expressions.
- `cwl-runner` accepts these declarations but does not act on them (no GPU passthrough there).

### Executor Selection

GoWe selects the executor for each task in this order:

1. Server-wide `--default-executor` — if set, overrides all hints
2. `gowe:Execution.executor` — if set to `worker` or `bvbrc`, route there directly (`container` is ignored as a routing value — it describes *how* to run, not *where*)
3. `gowe:Execution.bvbrc_app_id` — implies `bvbrc` executor
4. `DockerRequirement` or `gowe:Execution.docker_image` — auto-promotes to `worker` when workers are online, otherwise runs locally
5. Default — `local` executor (runs as OS process)

### Examples

**Route to a distributed worker:**

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    executor: worker
  DockerRequirement:
    dockerPull: "boltz.sif"
```

**Submit to BV-BRC:**

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    executor: bvbrc
    bvbrc_app_id: GenomeAssembly2
```

**Route to a specific worker group:**

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    worker_group: esmfold
  DockerRequirement:
    dockerPull: "esmfold.sif"
```

Only workers started with `--group esmfold` will pick up this task. Workers in the `default` group will skip it. This can also be combined with `gowe:ResourceData` for dataset affinity.

The `--group` CLI flag on `gowe submit` sets a submission-level fallback: step-level `worker_group` hints take priority over the submission-level group.

**Override container image:**

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    docker_image: "custom-image:v2"
  DockerRequirement:
    dockerPull: "default-image:v1"  # ignored — gowe:Execution.docker_image wins
```

---

## `gowe:Execution.secret_env` / `inject_secrets` — Submission-time Secrets

Opts a step into seeing some or all of the submitter's [submission-time secrets](../security/authentication.md#8-submission-time-secrets-260) (`secrets: {NAME: value}` on `POST /api/v1/submissions`). A step with neither field sees no submission secrets at all — this is what makes it safe for unrelated steps (or unrelated submissions sharing a worker group) to run side by side.

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    secret_env: [HF_TOKEN, COLLECTION_STORE_DSN]   # this task's container env gets exactly these
```

```yaml
hints:
  gowe:Execution:
    inject_secrets: true   # this task's container env gets every submission secret
```

| Field | Type | Description |
|-------|------|--------------|
| `secret_env` | `[string]` | Names of submission secrets to inject as env vars into this task's container. A name absent from the submission's secrets fails the task **pre-dispatch**, naming the missing secret in the error — the task is never dispatched without it. |
| `inject_secrets` | `bool` | Injects every submission secret as an env var. A no-op (not an error) when the submission carries none. |

Both fields are additive with `gowe:ResourceData`/dataset hints and independent of `executor`/`worker_group` — but as of this release, only tasks actually dispatched to a **worker** reliably receive delivered secrets (the server-side local/docker executors do not yet deliver `secret_env`/`inject_secrets` — see the "Known limitations" note in the security doc linked above). Route a step that needs secrets to a worker explicitly if you're not already relying on `DockerRequirement` auto-promotion:

```yaml
hints:
  gowe:Execution:
    executor: worker
    secret_env: [REGISTRY_DSN]
```

### `cwltool:Secrets` — declaring a workflow input as secret

The (unstandardized, but widely adopted) `cwltool:Secrets` extension marks specific **workflow inputs** as secret. Declare it once, at the top-level `Workflow` document, naming the input IDs:

```yaml
$namespaces:
  cwltool: http://commonwl.org/cwltool#

class: Workflow
hints:
  "cwltool:Secrets":
    secrets: [registry_dsn]
inputs:
  registry_dsn: string   # submitted as an ordinary string input: "hunter2..."
outputs: [...]
steps:
  fetch:
    in:
      dsn: registry_dsn        # direct sourcing — re-injected automatically
    out: [...]
```

At submission, GoWe strips `registry_dsn`'s real value out of `inputs`/`submitted_inputs` (replacing it with the literal `<secret>` placeholder) and stores it under the derived key `INPUT_REGISTRY_DSN`. A step that sources the declared input **directly** (`in: dsn: registry_dsn`, no intermediate step output, no `valueFrom`) has the real value re-injected into its in-memory job before the tool runs — so `$(inputs.dsn)` in an argument or an `InitialWorkDirRequirement` file interpolation sees the real value, while the persisted record never does. Sourcing through a step output, `valueFrom`, or a nested sub-workflow's own `cwltool:Secrets` declaration is not (yet) re-injected — keep the declared input on a directly-consuming step for now.

`cwltool:Secrets` inputs are also validated: a declared input supplied as a non-string value is rejected with **400** at submission time.

---

## `DockerRequirement` — Container Image (CWL Standard)

This is a standard CWL hint, not GoWe-specific. Documented here because GoWe extends its behavior for Apptainer/SIF support.

```yaml
hints:
  DockerRequirement:
    dockerPull: "boltz.sif"
```

### Image Resolution

GoWe resolves `dockerPull` values as follows:

| Value | Behavior | Example |
|-------|----------|---------|
| Ends with `.sif` | Local SIF file, resolved from `--image-dir` | `boltz.sif` → `/scout/containers/boltz.sif` |
| Absolute `.sif` path | Used as-is | `/scout/containers/boltz.sif` |
| Registry name | Pulled via `docker://` or `apptainer pull` | `dxkb/boltz:latest` |

### Interaction with `gowe:Execution.docker_image`

If both are set, `gowe:Execution.docker_image` takes priority:

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Execution:
    docker_image: "override.sif"     # ← used
  DockerRequirement:
    dockerPull: "default.sif"        # ← ignored
```

---

## `gowe:ResourceData` — Reference Data Requirements

Declares large datasets (model weights, sequence databases) that must be available on the worker. The scheduler uses this for affinity-based task routing.

Requires the `gowe` namespace declaration:

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        size: 50GB
        mode: cache
```

### Dataset Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Dataset identifier — must match a worker's dataset ID |
| `path` | string | No | Expected mount path inside the container |
| `size` | string | No | Approximate size (informational, e.g. `50GB`, `2TB`) |
| `mode` | string | No | Scheduling mode: `prestage` or `cache` (default: `cache`) |
| `source` | string | No | Future: source URL for on-demand caching (`shock://...`, `s3://...`) |

### Scheduling Modes

| Mode | Behavior | Use Case |
|------|----------|----------|
| `prestage` | Scheduler **requires** a worker with this dataset. Task waits if no matching worker is available. | Large databases that can't be transferred (AlphaFold ~2TB) |
| `cache` | Scheduler **prefers** workers with this dataset but dispatches elsewhere if none available. | Model weights that could theoretically be pulled on-demand |

### Worker Side

Workers declare available datasets via:

- **`--pre-stage-dir /local_databases`** — auto-scans subdirectories (dirname = dataset ID)
- **`--dataset boltz_weights=/local_databases/boltz`** — explicit alias

The scheduler matches `gowe:ResourceData` dataset IDs against worker-reported datasets.

### Full Example

```yaml
cwlVersion: v1.2
class: CommandLineTool

$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  DockerRequirement:
    dockerPull: "boltz.sif"
  gowe:Execution:
    executor: worker
  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        size: 50GB
        mode: cache
      - id: alphafold
        path: /local_databases/alphafold
        size: 2TB
        mode: prestage

baseCommand: [boltz, predict]

inputs:
  input_yaml:
    type: File
    inputBinding:
      position: 1

outputs:
  result:
    type: File
    outputBinding:
      glob: "*.cif"
```

This tool:
1. Runs in the `boltz.sif` container on a distributed worker
2. **Requires** a worker with the `alphafold` dataset (prestage)
3. **Prefers** a worker with the `boltz` dataset (cache) but can run without it

---

## `gowe:Service` — Long-running Service (Experimental)

**Experimental. In this version the server side only: a submitted service workflow's task waits in the queue until a
service-mode worker of its group takes it, and no worker runs services yet** (a worker declares service mode at
registration; the `gowe-worker` of this version does not). Later versions will run the tool as a long-lived server
(for example a model server on a GPU worker) and check its health. See [Server behaviour](#server-behaviour) below.

Marks a `CommandLineTool` as a service: the worker chooses a port, passes it in the tool's `port_input` input and
checks the service's health over HTTP.

```yaml
$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  gowe:Service:
    port_input: port
    scheme: http
    health:
      path: /health
      ready_path: /v1/models
      ready_contains: my-model
      start_timeout: 600      # seconds
      interval: 10
      failure_threshold: 3
    stop_grace: 30            # seconds
    weights_checksum: sha256:...
```

| Field | Type | Default | Range / rule |
|-------|------|---------|--------------|
| `port_input` | string | — (required) | Id of an input of the tool whose type is exactly `int` (not `int?`, `long` or a union); the worker sets it to the chosen port |
| `scheme` | string | `http` | `http` or `https` |
| `health.path` | string | `/health` | Starts with `/` but not `//`; printable ASCII only (0x21-0x7e: no spaces, control or non-ASCII characters); no `#`; at most 256 bytes |
| `health.ready_path` | string | none | Optional second GET; same rules as `health.path` |
| `health.ready_contains` | string | none | The `ready_path` response body must contain it; any text without control characters; at most 256 bytes; requires `ready_path` |
| `health.start_timeout` | integer (seconds) | 600 | 1..3600 |
| `health.interval` | integer (seconds) | 10 | 1..300 |
| `health.failure_threshold` | integer | 3 | 1..100 |
| `stop_grace` | integer (seconds) | 30 | 1..600 |
| `weights_checksum` | string | none | Recorded only; printable ASCII only (0x21-0x7e); at most 256 bytes |

Lengths are counted in bytes (UTF-8), not characters.

- Only the literal key `gowe:Service` is recognized (unlike `gowe:Execution`, no full-URI or other-prefix spelling). A
  key that looks meant as it — equal to it ignoring case, ending in `#Service`, or `<prefix>:Service` where
  `$namespaces` maps `<prefix>` to `https://github.com/wilke/GoWe#` — fails registration (400), naming the key, instead
  of being ignored. This applies on tools, steps, the workflow and inside sub-workflows.
- The hint is read from `hints` or `requirements` of the tool, or from the `hints` or `requirements` of the step that
  runs it; declaring it in both `hints` and `requirements` of the same tool or step is an error. A step's hint
  replaces the tool's as a whole (like the other step-level routing fields). The tool's own hint is still validated
  in full (keys, values, `port_input`) even when the step replaces it.
- A **step's** `hints`/`requirements` are read in map form only (`hints: {gowe:Service: {...}}`). The list form
  (`hints: [{class: gowe:Service, ...}]`) is dropped on a workflow step, as it is today for every hint there
  (`DockerRequirement`, `gowe:Execution`, ...); a tool's and the workflow's `hints` accept both forms.
- A key that is given must have a value: an empty string for `port_input`, `scheme`, `health.path` or
  `health.ready_path` fails registration (400); omit the key to use the default.
- An unknown key (also inside `health`), a value of the wrong type, a value out of range, or a `port_input` that is not
  an `int` input of the tool fails registration (400). Integers are written as integers, as for `gpu_count`; a CWL
  expression is not accepted.
- The hint is accepted only on a `CommandLineTool` run by a step of the top-level workflow (or on that step): not on the
  workflow, an `ExpressionTool`, a tool no top-level step runs, or anywhere inside a sub-workflow (on the
  sub-workflow, one of its steps, or any tool it holds, at any depth). The scheduler enforces this too: a sub-workflow
  that turns out to contain a service (for example in a workflow registered before this check) fails its step with
  `a service (gowe:Service) cannot run inside a sub-workflow`, and a task of a sub-workflow never carries the hint.
- A top-level step must reach the tool by `run: "#<id>"` (inline or packed `$graph`) for the hint to count; an external
  `run: tool.cwl` reference on the server is refused for a service tool. Register packed documents (`gowe register`
  packs them).

### Registration rule

A workflow that contains a service tool may be registered (`POST /api/v1/workflows`) or replaced (`PUT
/api/v1/workflows/{id}`) only when all of these hold:

1. the workflow has exactly one step, and that step does not scatter;
2. the step runs in a container (`DockerRequirement.dockerPull` or `gowe:Execution.docker_image`);
3. the step's `gowe:Execution.executor`, if set, is `worker` or `container` (a service always runs on the worker
   executor; `local`, `bvbrc` or any other value is refused);
4. the step names a worker group other than `default` (`gowe:Execution.worker_group`);
5. `port_input` names an `int` input of the tool;
6. the caller is a server admin or a manager of that worker group.

1-5 are refused with 400, 6 with 403; an anonymous caller and a service identity (service token) are always refused.
The worker group must also be a valid group name (letters, digits, `.`, `_`, `-`, starting with a letter or digit;
else 400); names are case-sensitive, so `Default` is a group of its own, not `default`.

A change to an existing service workflow — `PUT` (new CWL or metadata only), `PATCH /api/v1/workflows/{id}/labels`,
`DELETE /api/v1/workflows/{id}`, and delete in the web UI — is refused with 403 unless the caller is a server admin or
a manager of that workflow's worker group (in addition to the usual ownership check of each route).

Registering content that is already stored returns the existing row (`deduplicated: true`) only when that row is
itself a valid service workflow; a row with the same text parsed without the service (by an older server) is not
reused, and a new row is stored.

### Server behaviour

A task created from a service step is a service task (`is_service`). The server never starts it a second time:

- **Always the worker executor.** A service step runs only on the worker executor, whatever the server's
  `--default-executor` and whether any worker is online. A server started with `--force-executor` other than `worker`,
  or a stored workflow whose step has an executor hint other than `worker` / `container`, fails the step at dispatch
  (no task is created); the store refuses a service task with any other executor. The worker-group access check
  applies as for every worker task.
- **One task per step.** If a service step is found READY while it already owns a task that is not finished (the step
  update after the task was created did not land), the scheduler adopts that task instead of creating a second one.
  The dispatch's own write of the submit outcome never puts a task a worker has already checked out back in the queue
  (compare-and-set on the state it wrote; this holds for every worker task).
- **Only for a live submission.** A service task is stored only while its submission exists and is not finished
  (COMPLETED, FAILED, CANCELLED), checked in the same transaction as the insert; a delete or cancel that lands while
  the scheduler is creating it leaves no task. A service task is handed out only while its submission exists and is
  not finished.
- **Queue and checkout.** It is offered only to a service-mode worker of its group, and a service-mode worker is offered
  only service tasks. It waits in the queue until such a worker takes it; it is never failed for lack of a capable
  worker (dispatch preflight, stuck-task detection), and a service-mode worker never counts as capacity for ordinary
  tasks. The checkout hold rule (`--checkout-hold-after`) is never applied to a service-mode worker: its running
  services do not finish, so a held service would wait forever.
- **Service-mode workers.** Service mode requires worker authentication: with it off (no static key, no database key
  ever created) a registration with `service_mode: true` is refused with 403 (`service mode requires worker
  authentication (register with a worker key that names the group)`). With it on, a worker registers with
  `service_mode: true` only with a worker key whose group list explicitly names the worker's group; a key with an
  empty group list (any group) is refused with 403, and such a key cannot adopt a keyless service-mode worker record
  either (403). `service_mode: true` also needs a valid `host_id` (an absent one, which the server groups by hostname,
  does not count): else 400 (`a service-mode worker must report a host id`).
- **GPUs.** A service-mode worker without a GPU inventory (GPU capacity unknown) is offered no task that needs a GPU.
  GPU charges follow the worker row: besides its own RUNNING tasks, a worker with an inventory and a host id has
  subtracted the GPUs charged to RUNNING services of other worker rows (online or offline) of the same `host_id` whose
  inventory shares at least one GPU UUID with its own; workers of the same host with disjoint inventories do not affect
  each other. Limit: a restarted worker registers under a new id, so its lost services stay charged to the old row
  (and are protected through this overlap rule) until they are force-released (see below); the protection is by
  inventory, not by the devices a service was actually given.
- **Never retried, requeued or re-run.** `max_retries` is 0, the scheduler never marks a FAILED service for retry
  (whatever its `max_retries`) and its retry phase refuses one before archiving or clearing anything. No requeue path
  (worker removal, reaper, worker heartbeat reconcile, submission retry reset) puts a service back in the queue. A
  submission retry (`PUT /api/v1/submissions/{id}/retry`) of a submission that has a service task, or of a service
  workflow, is refused with 409, and the web UI's resume and recompute skip a service task, both with
  `a service task is not re-run; submit the service again`.
- **Completion.** A worker reports a service's end with an explicit state, `FAILED` or `SKIPPED`: a model server
  that exits has failed, so `SUCCESS` (and an omitted state) is refused with 400 (`a service does not complete; report
  FAILED, or SKIPPED after a cancel`). `SKIPPED` stands only when the submission is cancelled (or the task has a stop
  request); otherwise it is stored as `FAILED` (stderr gets `service stopped by its worker without a cancel`), so the
  submission fails. For every worker task, `/complete` accepts only `SUCCESS`, `FAILED`, `SKIPPED` or no state (decided
  from the exit code); any other state is 400.
- **No image change at submission.** A submission of a service workflow whose inputs contain the key
  `cwl:requirements` is refused with 400 (`cwl:requirements is not accepted for a service workflow`); the scheduler
  never merges job requirements into a service's tool either.
- **Worker removal.** A worker that still holds a RUNNING service task cannot be removed (worker deregistration, admin,
  group-manager and web UI removal: 409 `worker still holds N running service(s); cancel their submissions, or
  force-release them if the worker is gone`; the UI's purge of offline workers skips it).
- **Silent worker.** When the worker of a RUNNING service goes silent, the reaper marks the worker offline as usual but
  the service stays RUNNING, keeps its charged cores, memory and GPUs, and is marked lost (`service_lost_at`); the mark
  is cleared when the worker heartbeats again.
- **Missing from the heartbeat (two strikes).** Only a heartbeat that carries `running_tasks` (an empty list included)
  is looked at for services; a heartbeat without the field (an older worker, an empty body) leaves them alone. A
  RUNNING service older than the grace period that such a heartbeat omits is marked missing (`service_missing_at`); a
  heartbeat that lists it again clears the mark; one that omits it once the mark is older than the grace period marks
  it FAILED (`service is no longer running on its worker (not in the worker's heartbeat)`), which fails its step and
  submission; a service with a stop request (see Cancel) is marked SKIPPED instead (`service stopped after a cancel
  (no longer in the worker's heartbeat)`): it stopped as asked. A worker must therefore send `running_tasks: []` when
  it runs nothing.
- **Stopping an ended service.** The heartbeat answer's `cancel_tasks` lists every task the worker reported running
  whose submission is cancelled and, for services, every one that is already finished in the database, has a stop
  request or (RUNNING) has lost its submission record, so the worker stops a service the server has ended or asked to
  stop. Independently of what the worker lists (also when `running_tasks` is absent), it lists every RUNNING service
  attributed to that worker that has a stop request or whose submission is cancelled or gone. Ordinary tasks follow
  the list only.
- **Cancel = stop requested.** Cancelling a submission makes it CANCELLED and skips its steps at once, and its tasks
  that are not running as before; a QUEUED service is SKIPPED at once. A RUNNING service is not: it stays RUNNING, its
  cores, memory and GPUs stay charged to its worker, and it gets a stop request (`service_stop_requested_at`, set
  once; a second cancel changes nothing). Every heartbeat that lists it gets it in `cancel_tasks` until its worker
  reports the end on `/complete` (`SKIPPED`, or `FAILED`, stored as sent) or the two-strike rule above ends it
  SKIPPED; only then are its charges freed. The scheduler leaves the cancelled submission alone meanwhile (no new
  task, no re-dispatch, the submission stays CANCELLED). The cancel answer counts such services in
  `services_stopping` (not in `tasks_already_completed`), and `gowe cancel` prints `Services stopping: N (their GPUs
  stay charged until the worker confirms)`.
- **Delete refused while running.** Deleting a submission (`DELETE /api/v1/submissions/{id}`, and delete in the web
  UI) while any task of it, or of a descendant child submission, is a RUNNING service is refused with 409 (`submission
  has a running service; cancel it and wait until it has stopped, or force-release it`) and changes nothing. The
  store refuses the delete too, for a service checked out after that check; the delete has then already cancelled
  the submission's steps and tasks, so it completes the cancel (the submission becomes CANCELLED, its service gets a
  stop request, descendants are cancelled) and answers 409 `submission has a running service, so it was not deleted:
  it was cancelled and its service is stopping; delete it once the service has stopped`. Cancel first, wait until
  the worker has reported the service stopped, then delete.
- **Force release.** `POST /api/v1/submissions/{id}/tasks/{tid}/force-release` (`gowe admin force-release-service
  <submission-id> <task-id>`) ends a RUNNING service whose worker cannot confirm that it stopped, or has not: its
  worker went silent (`service_lost_at`), its worker record is gone, or its stop request is older than 15 minutes
  (the worker online). Only a server admin or a manager of the task's worker group may do it (not the submission
  owner by that alone); anyone else (a service identity, an anonymous caller, a user who manages no group) is
  refused with 403 before anything is looked up, and a manager of another group gets the same 403 (`only a server
  admin or a manager of the service's worker group may force-release it`). An unknown submission or task, or a task
  of another submission, is 404; except that a server admin may release a task whose submission record is gone (the
  task's `submission_id` must be the one in the URL). A task that is not a service, not RUNNING, or whose worker is
  online with a younger or no stop request is refused with 409 (`the service's worker is online; cancel the
  submission, then wait 15 minutes or until the worker is marked lost`). The task becomes FAILED (SKIPPED when a stop
  was requested), its stderr gets `force-released by <user>: the worker did not confirm that the service stopped; its
  process and GPUs may still be in use` (followed by `; the worker will be told to stop it on its next heartbeat`
  when its worker record exists and is not lost), its secrets are removed, the server logs one WARN, the task
  metrics record it (a FAILED one with reason `force_release`), and its charges are freed. The answer holds only
  `id`, `state`, `external_id`, `completed_at` and `worker_notified` (when the worker is told to stop it: `on its
  next heartbeat`, `on its next heartbeat, if it comes back` for a lost worker, `never: its worker record no longer
  exists`). It is a bookkeeping act only: **the process may still run and hold the GPUs** on the worker's host, so
  check the host first. A worker that still lists the task is told to stop it on its heartbeat.

---

## Data Flow

```
CWL hints
  ↓ parser/extractStepHints()
model.StepHints (BVBRCAppID, ExecutorType, DockerImage, WorkerGroup, RequiredDatasets, RequiresGPU, GPUs)
  ↓ scheduler/createTaskFromStep()
model.Task.RuntimeHints
  ↓ store/CheckoutTask()
Worker matching: runtime, group, datasets (prestage=require, cache=prefer), GPU count
  ↓ worker execution
Container launched with bind mounts from --pre-stage-dir / --extra-bind
```

## Portability

| Hint | Other CWL engines | Portability |
|------|-------------------|-------------|
| `DockerRequirement` | Fully supported | Standard CWL |
| `gowe:Execution` | Safely ignored (namespaced) | GoWe-specific |
| `gowe:ResourceData` | Safely ignored (namespaced) | GoWe-specific |
| `gowe:Service` | Safely ignored (namespaced) | GoWe-specific, experimental |
| `cwltool:CUDARequirement` | Supported by cwltool | cwltool extension |

To maximize portability, always place GoWe extensions in `hints` and use `$namespaces` for `gowe:` prefixed hints (both `gowe:Execution` and `gowe:ResourceData`).
