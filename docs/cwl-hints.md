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

**Experimental. In this version the hint is parsed, validated and access-checked only; a service workflow that is
submitted runs like an ordinary task.** Later versions will run the tool as a long-lived server (for example a model
server on a GPU worker) and check its health.

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
3. the step names a worker group other than `default` (`gowe:Execution.worker_group`);
4. `port_input` names an `int` input of the tool;
5. the caller is a server admin or a manager of that worker group.

1-4 are refused with 400, 5 with 403; an anonymous caller and a service identity (service token) are always refused.
The worker group must also be a valid group name (letters, digits, `.`, `_`, `-`, starting with a letter or digit;
else 400); names are case-sensitive, so `Default` is a group of its own, not `default`.

A change to an existing service workflow — `PUT` (new CWL or metadata only), `PATCH /api/v1/workflows/{id}/labels`,
`DELETE /api/v1/workflows/{id}`, and delete in the web UI — is refused with 403 unless the caller is a server admin or
a manager of that workflow's worker group (in addition to the usual ownership check of each route).

Registering content that is already stored returns the existing row (`deduplicated: true`) only when that row is
itself a valid service workflow; a row with the same text parsed without the service (by an older server) is not
reused, and a new row is stored.

A task created from a service step is not retried by the scheduler (`max_retries` 0).

**Known gap (closed in a later version):** the container rule holds at registration only. A submission can still
change the image the step runs through `cwl:requirements` in its inputs (job-level `DockerRequirement`).

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
