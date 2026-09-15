# GoWe CWL Hints Reference

GoWe extends CWL with custom hints for executor routing, container selection, and reference data management. These are CWL-compliant extensions — other engines safely ignore them.

## Where to Place GoWe Hints

All GoWe-specific extensions should go in **`hints`**, not `requirements`. In CWL:

- **`requirements`** — the engine MUST support this or reject the document
- **`hints`** — the engine SHOULD support this but MAY ignore it

Using `hints` ensures your CWL workflows remain portable: cwltool, Toil, and other engines will skip unknown hints rather than failing.

> **Parser behavior**: GoWe accepts `DockerRequirement` and `gowe:ResourceData` from either `hints` or `requirements`. `gowe:Execution` is only recognized in `hints`. The parser also accepts legacy `goweHint` for backward compatibility, but new CWL files should use `gowe:Execution`.

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

## Data Flow

```
CWL hints
  ↓ parser/extractStepHints()
model.StepHints (BVBRCAppID, ExecutorType, DockerImage, WorkerGroup, RequiredDatasets)
  ↓ scheduler/createTaskFromStep()
model.Task.RuntimeHints
  ↓ store/CheckoutTask()
Worker matching: runtime, group, datasets (prestage=require, cache=prefer)
  ↓ worker execution
Container launched with bind mounts from --pre-stage-dir / --extra-bind
```

## Portability

| Hint | Other CWL engines | Portability |
|------|-------------------|-------------|
| `DockerRequirement` | Fully supported | Standard CWL |
| `gowe:Execution` | Safely ignored (namespaced) | GoWe-specific |
| `gowe:ResourceData` | Safely ignored (namespaced) | GoWe-specific |

To maximize portability, always place GoWe extensions in `hints` and use `$namespaces` for `gowe:` prefixed hints (both `gowe:Execution` and `gowe:ResourceData`).
