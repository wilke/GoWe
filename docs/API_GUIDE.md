# GoWe API Guide for MCP Server Developers

How to interact with the GoWe REST API from an MCP server (or any HTTP client). Covers authentication, discovering workflows and BV-BRC apps, inspecting input schemas, and submitting jobs.

## Base URL

All endpoints live under `/api/v1`. The production instance on coconut runs on port 8091:

```
http://localhost:8091/api/v1
```

## Response Envelope

Every response uses the same JSON envelope:

```json
{
  "status": "ok",
  "request_id": "req_a1b2c3d4",
  "timestamp": "2026-04-07T12:00:00Z",
  "data": { },
  "pagination": { "total": 50, "limit": 20, "offset": 0, "has_more": true },
  "error": null
}
```

On error:

```json
{
  "status": "error",
  "request_id": "req_a1b2c3d4",
  "timestamp": "2026-04-07T12:00:00Z",
  "data": null,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "required input 'sequence' is missing",
    "details": [
      { "field": "inputs.sequence", "message": "required input 'sequence' is missing" }
    ]
  }
}
```

List endpoints include `pagination`. Single-resource endpoints omit it.

> **Note:** Response examples below show the `data` payload only, without the surrounding envelope, for brevity. All responses are wrapped in the `{status, request_id, timestamp, data, ...}` structure shown above.

---

## Authentication

### BV-BRC Token (Recommended)

Pass a BV-BRC token in the `Authorization` header:

```
Authorization: un=user@bvbrc|tokenid=xxx|expiry=1720000000|sig=yyy
```

The token is a pipe-delimited string obtained from [BV-BRC](https://www.bv-brc.org/) login. GoWe extracts the username and auto-creates an account on first use.

Token sources (checked in order by the BV-BRC client library):
1. `BVBRC_TOKEN` environment variable
2. `~/.gowe/credentials.json` (created by `gowe login`)
3. `~/.bvbrc_token` file
4. `~/.patric_token` file
5. `~/.p3_token` file

### Anonymous Access

When the server is started with `--allow-anonymous`, requests without a token are accepted as an anonymous user. Anonymous users may be restricted to specific executor types (e.g., `local,worker` only).

No header needed for anonymous access.

### Worker Authentication

Workers use a shared secret via the `X-Worker-Key` header. This is only relevant if you're building a worker client, not for job submission.

---

## 1. Discover Available Endpoints

```
GET /api/v1/
```

No auth required. Returns the full endpoint list:

```json
{
  "name": "GoWe API",
  "version": "v1",
  "description": "GoWe Workflow Engine ...",
  "endpoints": [
    { "path": "/api/v1/workflows", "methods": ["GET", "POST"], "description": "Workflow definition management" },
    { "path": "/api/v1/submissions", "methods": ["GET", "POST"], "description": "Job submission and management" },
    { "path": "/api/v1/apps", "methods": ["GET"], "description": "BV-BRC application catalog" }
  ]
}
```

---

## 2. List BV-BRC Apps

BV-BRC bioinformatics applications are exposed through the `/apps` endpoint. This proxies to the BV-BRC AppService and caches results for 5 minutes.

```
GET /api/v1/apps
```

```json
{
  "data": [
    {
      "id": "GenomeAssembly2",
      "label": "Genome Assembly",
      "description": "Assemble reads into contigs...",
      "parameters": [
        {
          "id": "paired_end_libs",
          "type": "group",
          "required": 0,
          "label": "Paired End Libraries"
        },
        {
          "id": "recipe",
          "type": "enum",
          "required": 1,
          "label": "Assembly Strategy",
          "desc": "Assembly recipe to use",
          "enum": ["auto", "unicycler", "spades", "canu"]
        }
      ]
    }
  ]
}
```

### Get a Single App's Schema

```
GET /api/v1/apps/{app_id}
```

Returns the full app definition with all parameters. Use this to build input forms or validate inputs before submission.

### Get Auto-Generated CWL Tool Wrapper

```
GET /api/v1/apps/{app_id}/cwl-tool
```

```json
{
  "app_id": "GenomeAssembly2",
  "cwl_tool": "cwlVersion: v1.2\nclass: CommandLineTool\nhints:\n  gowe:Execution:\n    bvbrc_app_id: GenomeAssembly2\n    executor: bvbrc\n..."
}
```

This generates a CWL CommandLineTool that wraps the BV-BRC app. Useful for incorporating BV-BRC apps into CWL workflows.

---

## 3. List Workflows

Workflows are pre-registered CWL definitions (Workflows or CommandLineTools).

```
GET /api/v1/workflows?limit=20&offset=0
```

Query parameters:

| Parameter | Description |
|-----------|-------------|
| `limit` | Page size (max 100, default 20) |
| `offset` | Pagination offset |
| `class` | Filter: `Workflow`, `CommandLineTool`, `ExpressionTool`, or `Tool` (both tool types) |
| `search` | Search by name or ID |
| `label` | Filter by label `key:value` (repeatable) |
| `sort` | Sort column |
| `dir` | `asc` or `desc` (default: `desc`) |

```json
{
  "data": [
    {
      "id": "wf_abc123",
      "name": "protein-structure-prediction",
      "description": "Run Boltz, Chai, and AlphaFold on input sequences",
      "class": "Workflow",
      "cwl_version": "v1.2",
      "content_hash": "sha256:...",
      "labels": { "category": "structural-biology" },
      "step_count": 5,
      "created_at": "2026-03-15T10:00:00Z"
    }
  ],
  "pagination": { "total": 3, "limit": 20, "offset": 0, "has_more": false }
}
```

### Register a Tool

Tools (CWL CommandLineTools) and Workflows are both registered through the same endpoint:

```
POST /api/v1/workflows
Content-Type: application/json
```

```json
{
  "name": "grep-count",
  "description": "Count lines matching a pattern",
  "cwl": "cwlVersion: v1.2\nclass: CommandLineTool\nbaseCommand: grep\n..."
}
```

The `cwl` field accepts raw CWL YAML. Both `CommandLineTool` and `Workflow` class documents can be registered. The server parses, validates, and stores the CWL.

### Compose a Workflow from Registered Tools

Use `gowe://` references in step `run` fields to reference tools already registered in the server:

```yaml
cwlVersion: v1.2
class: Workflow

inputs:
  input_file:
    type: File
  pattern:
    type: string

outputs:
  grep_result:
    type: File
    outputSource: grep_step/count

steps:
  grep_step:
    run: gowe://grep-count         # references the tool registered above
    in:
      pattern: pattern
      file: input_file
    out: [count]

  wc_step:
    run: gowe://word-count         # another registered tool (by name)
    in:
      file: input_file
    out: [count]
```

Reference formats:
- `gowe://tool-name` — lookup by name (most recent version)
- `gowe://wf_abc123` — lookup by exact ID

The server resolves `gowe://` references at registration time by inlining the referenced tool CWL into a packed `$graph` document. The workflow input must be a bare `Workflow` (not a pre-packed `$graph`).

See `examples/api/register_workflow.py` for a complete working example.

---

## 4. Get Workflow Input Definitions

To discover what inputs a workflow expects, fetch the full workflow:

```
GET /api/v1/workflows/{id_or_name}
```

The response includes an `inputs` array:

```json
{
  "data": {
    "id": "wf_abc123",
    "name": "protein-structure-prediction",
    "class": "Workflow",
    "inputs": [
      {
        "id": "sequence",
        "type": "File",
        "required": true,
        "default": null
      },
      {
        "id": "max_iterations",
        "type": "int",
        "required": false,
        "default": 3
      },
      {
        "id": "output_format",
        "type": "string?",
        "required": false,
        "default": "pdb"
      }
    ],
    "outputs": [
      { "id": "structures", "type": "Directory" },
      { "id": "metrics", "type": "File" }
    ],
    "steps": [
      {
        "id": "boltz_predict",
        "run": "#boltz-tool",
        "depends_on": []
      },
      {
        "id": "compare",
        "run": "#protein-compare",
        "depends_on": ["boltz_predict"]
      }
    ]
  }
}
```

### Input Type Reference

CWL input types map to JSON values:

| CWL Type | JSON Value | Example |
|----------|-----------|---------|
| `string` | string | `"MKTAYIAKQR..."` |
| `int` | number | `42` |
| `float` | number | `3.14` |
| `boolean` | boolean | `true` |
| `File` | object | `{"class": "File", "location": "/path/to/file.fasta"}` |
| `Directory` | object | `{"class": "Directory", "location": "/path/to/dir/"}` |
| `string?` | string or null | Optional — omit to use default |
| `File[]` | array of objects | `[{"class": "File", "location": "a.txt"}, ...]` |
| `record{...}` | object | `{"field1": "value", "field2": 5}` |

File inputs support workspace paths for BV-BRC integration:

```json
{
  "class": "File",
  "location": "ws:///user@bvbrc/home/data/sample.fastq.gz"
}
```

---

## 5. Validate Before Submitting (Dry Run)

Always validate first. The dry run checks inputs, resolves the DAG, and verifies executor availability without creating a submission:

```
POST /api/v1/submissions?dry_run=true
```

```json
{
  "workflow_id": "protein-structure-prediction",
  "inputs": {
    "sequence": {
      "class": "File",
      "location": "/scout/wf/data/my_protein.fasta"
    },
    "max_iterations": 5
  }
}
```

Response:

```json
{
  "data": {
    "dry_run": true,
    "valid": true,
    "workflow": {
      "id": "wf_abc123",
      "name": "protein-structure-prediction",
      "step_count": 5
    },
    "inputs_valid": true,
    "steps": [
      {
        "id": "boltz_predict",
        "executor_type": "worker",
        "depends_on": [],
        "executor_available": true
      },
      {
        "id": "compare",
        "executor_type": "worker",
        "depends_on": ["boltz_predict"],
        "executor_available": true
      }
    ],
    "dag_acyclic": true,
    "execution_order": ["boltz_predict", "compare"],
    "executor_availability": {
      "local": "available",
      "worker": "available"
    },
    "errors": [],
    "warnings": []
  }
}
```

If validation fails, `valid` is `false` and `errors` lists what's wrong:

```json
{
  "valid": false,
  "errors": [
    { "field": "inputs.sequence", "message": "required input 'sequence' is missing" }
  ]
}
```

---

## 6. Submit a Job

```
POST /api/v1/submissions
Authorization: un=user@bvbrc|tokenid=...|expiry=...|sig=...
Content-Type: application/json
```

```json
{
  "workflow_id": "protein-structure-prediction",
  "inputs": {
    "sequence": {
      "class": "File",
      "location": "/scout/wf/data/my_protein.fasta"
    },
    "max_iterations": 5
  },
  "labels": {
    "experiment": "batch-42",
    "target": "7KBF_A"
  },
  "output_destination": "ws:///user@bvbrc/home/results/batch-42/"
}
```

Fields:

| Field | Required | Description |
|-------|----------|-------------|
| `workflow_id` | yes | Workflow ID (`wf_abc123`) or name (`protein-structure-prediction`) |
| `inputs` | yes | Map of input values matching the workflow's input schema |
| `labels` | no | Key-value metadata for organizing submissions |
| `output_destination` | no | Where to upload outputs on completion (e.g., `ws://` for BV-BRC workspace) |

Response (HTTP 201):

```json
{
  "data": {
    "id": "sub_xyz789",
    "workflow_id": "wf_abc123",
    "workflow_name": "protein-structure-prediction",
    "state": "PENDING",
    "inputs": { "sequence": { "class": "File", "location": "..." }, "max_iterations": 5 },
    "outputs": {},
    "labels": { "experiment": "batch-42" },
    "submitted_by": "user@bvbrc",
    "task_summary": {
      "total": 0, "pending": 0, "scheduled": 0, "queued": 0,
      "running": 0, "success": 0, "failed": 0, "skipped": 0
    },
    "output_destination": "ws:///user@bvbrc/home/results/batch-42/",
    "created_at": "2026-04-07T12:00:00Z",
    "completed_at": null
  }
}
```

---

## 7. Monitor Job Status

### List Submissions

```
GET /api/v1/submissions?state=RUNNING&limit=20&offset=0
```

Standard list parameters (`limit`, `offset`, `state`, `search`, `workflow_id`,
`date_start`, `date_end`, `sort`, `dir`) plus:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `include_children` | `false` | Child submissions — those spawned by a parent's scatter/sub-workflow step (`parent_task_id` set) — are excluded from the rows and the pagination total by default. Pass `include_children=true` to list them too. Children remain individually reachable via `GET /api/v1/submissions/{id}` and via their parent regardless of this flag. |

Each returned submission carries `parent_task_id` (empty for top-level
submissions), so callers using `include_children=true` can tell parents and
children apart.

### Poll Submission State

```
GET /api/v1/submissions/{id}
```

```json
{
  "data": {
    "id": "sub_xyz789",
    "state": "RUNNING",
    "task_summary": {
      "total": 5,
      "pending": 0,
      "scheduled": 0,
      "queued": 1,
      "running": 2,
      "success": 2,
      "failed": 0,
      "skipped": 0
    },
    "outputs": {},
    "output_state": "pending",
    "created_at": "2026-04-07T12:00:00Z",
    "completed_at": null
  }
}
```

Submission states: `PENDING` -> `RUNNING` -> `COMPLETED` | `FAILED` | `CANCELLED`

### List Tasks Within a Submission

```
GET /api/v1/submissions/{id}/tasks?limit=50
```

```json
{
  "data": [
    {
      "id": "task_001",
      "step_id": "boltz_predict",
      "state": "SUCCESS",
      "executor_type": "worker",
      "exit_code": 0,
      "outputs": { "structure": { "class": "File", "location": "/scout/wf/data/..." } },
      "created_at": "2026-04-07T12:00:01Z",
      "completed_at": "2026-04-07T12:05:30Z"
    },
    {
      "id": "task_002",
      "step_id": "compare",
      "state": "RUNNING",
      "executor_type": "worker",
      "created_at": "2026-04-07T12:05:31Z",
      "completed_at": null
    }
  ]
}
```

Task states: `PENDING` -> `SCHEDULED` -> `QUEUED` -> `RUNNING` -> `SUCCESS` | `FAILED`

### Get Task Logs

```
GET /api/v1/submissions/{sub_id}/tasks/{task_id}/logs
```

```json
{
  "data": {
    "task_id": "task_001",
    "step_id": "boltz_predict",
    "stdout": "Processing sequence...\nPrediction complete.\n",
    "stderr": "",
    "exit_code": 0
  }
}
```

### Submission Timing

```
GET /api/v1/submissions/{id}/timing
GET /api/v1/submissions/{id}/timing?include_children=true
```

A timing-only projection of a submission: per-task queue/run durations, per-step
aggregates, and submission-level totals. The response carries timing fields and
raw timestamps only — task `tool`/`job` definitions and stdout/stderr never ship
through this endpoint. With `include_children=true` the report recurses into
sub-workflow child submissions (`children` array of nested reports).

```json
{
  "data": {
    "submission": {
      "id": "sub_xyz789",
      "state": "COMPLETED",
      "wall_s": 322.4,
      "scheduling_s": 0.8,
      "compute_s": 301.2,
      "queue_s": 12.5,
      "prestage_s": 1.9,
      "poststage_s": 3.4,
      "critical_path_s": 310.9,
      "counts": { "total": 5, "success": 5 },
      "created_at": "2026-04-07T12:00:00Z",
      "completed_at": "2026-04-07T12:05:22Z"
    },
    "steps": [
      { "step_id": "predict", "state": "COMPLETED", "wall_s": 290.1,
        "fan_in_s": 0.9, "max_run_s": 288.0, "tasks": 3,
        "created_at": "...", "completed_at": "..." }
    ],
    "tasks": [
      { "task_id": "task_001", "step_id": "predict", "scatter_index": 0,
        "executor": "worker", "state": "SUCCESS", "kind": "task",
        "queue_s": 4.1, "dispatch_s": 0.05, "checkout_wait_s": 3.6,
        "stage_in_s": 12.0, "compute_s": 270.5, "stage_out_s": 5.5,
        "run_s": 288.0, "retry_count": 0,
        "created_at": "...", "dispatched_at": "...", "started_at": "...",
        "completed_at": "..." }
    ]
  }
}
```

Semantics (per-state trust rules):

- `started_at` is trusted as a timestamp only in `RUNNING`/`SUCCESS`/`FAILED`.
  A `QUEUED` worker or bvbrc task's `started_at` is `null` (or, for a task that
  was `QUEUED` across a pre-0.15.x-next schema upgrade, a stale dispatch
  stamp) and is never used — its `queue_s` is "waiting so far" from
  `created_at`. `dispatched_at`, by contrast, needs no such gating: it is
  written once by the scheduler at submit time and never touched again, so it
  is safe to read (and `dispatch_s = dispatched_at − created_at` is safe to
  compute) in any state.
- `run_s` requires both timestamps and a `SUCCESS`/`FAILED` state; on
  `RETRYING`/`SCHEDULED` rows it is the last failed attempt's window, flagged
  `"retrying": true`, and `queue_s` is measured since first dispatch (it
  includes prior attempts' run time).
- `checkout_wait_s` (`started_at − dispatched_at`) is the gap between dispatch
  and actual execution start: for `worker` tasks, real time spent in the
  worker's own pull queue after becoming available; for `bvbrc`, real time on
  the platform's queue before it reported `RUNNING`. Near-zero for synchronous
  executors (`local`/`container`/`apptainer`), where dispatch and execution
  start are the same event. Subject to the same trust gating as `run_s`.
- `stage_in_s`/`stage_out_s` are worker-measured input/output staging
  durations, present only on `worker` tasks whose worker reported them (nil
  for every other executor, and for tasks run by a worker that predates this
  field). `compute_s` on a task row is `run_s` with staging subtracted
  (`run_s − stage_in_s − stage_out_s`, clamped to zero), populated only for
  plain `task` rows.
- `kind` classifies rows: `task`, `subworkflow` (proxy for a child submission;
  `run_s` is the child's wall time once the child is terminal, and may lag the
  proxy's own stamps by up to one scheduler tick; `child_submission_id` links
  the child), `skipped-iteration` (when-skip scatter synthetic — excluded from
  every aggregate), `cancelled` (`SKIPPED`; no `run_s`, `queue_s` only if it
  never started).
- Steps with zero tasks (inline ExpressionTool evaluation, when-skipped steps)
  are flagged `"inline": true`; their `wall_s` spans the step instance's own
  lifetime and therefore includes dependency wait. Because that interval
  starts at submission time rather than at the step's actual dispatch, an
  inline step sitting mid-chain can overlap its upstream dependency's
  interval; `critical_path_s` sums per-step intervals along dependency
  chains, so it can exceed `wall_s` when this happens.
- On the submission: `compute_s` sums task `run_s` (sub-workflow proxies
  contribute their child's wall time, unchanged by this section's per-task
  `compute_s`); `scheduling_s` is submission creation → first task creation
  (pre-dispatch work, including prestage); `prestage_s`/`poststage_s` are the
  ws:// input pre-staging and output delivery phase durations respectively
  (present only when a submission has ws:// inputs/outputs to stage, and only
  once each phase has both started and completed); `critical_path_s` is the
  longest path over the workflow step DAG.

---

## 8. Cancel a Job

```
PUT /api/v1/submissions/{id}/cancel
```

```json
{
  "data": {
    "id": "sub_xyz789",
    "state": "CANCELLED",
    "steps_cancelled": 2,
    "tasks_cancelled": 3,
    "tasks_already_completed": 2,
    "children_cancelled": 4
  }
}
```

`children_cancelled` counts descendant sub-workflow submissions (scatter
children, at any nesting depth) cancelled synchronously at cancel-accept time.

Deletion is separate from cancellation: `DELETE /api/v1/submissions/{id}`
permanently removes a submission and returns `409 Conflict` while any descendant
child submission is still active — cancel first and wait for the cascade to
finish. Deleting also requires an authenticated (non-anonymous) owner or admin.

---

## 9. Check System Health

```
GET /api/v1/health
```

No auth required:

```json
{
  "data": {
    "status": "healthy",
    "version": "0.1.0",
    "go_version": "go1.24.2",
    "uptime": "3h45m12s",
    "executors": {
      "local": "available",
      "container": "available",
      "bvbrc": "available",
      "worker": "available"
    },
    "workers": {
      "online": 2,
      "offline": 0,
      "runtimes": ["apptainer"],
      "groups": ["default"]
    }
  }
}
```

---

## 10. Admin: Verify and Re-deliver Workspace Outputs

Both endpoints require the **admin** role (`401` unauthenticated, `403` for a
non-admin) and a server with BV-BRC Workspace staging available
(`503 workspace staging not configured` otherwise). They act on behalf of the
submission's owner using the token stored with the submission — the admin's
own token is never used, because it has no write permission in another user's
workspace home. A missing or expired stored token yields `409 Conflict` with an
explanatory message; the owner must re-submit.

Only submissions whose delivery already ran to an outcome are accepted:
`output_state` must be `delivered` or `upload_failed` (`409` for anything else,
including `uploading` while the scheduler is still delivering), and the
submission must be terminal.

Every output `File` of the submission **and of its descendant sub-workflow
child submissions** is examined, at any nesting depth (arrays, records,
`Directory` listings). `secondaryFiles` are not examined — the scheduler's
stage-out never delivers them on their own, so they are neither expected in
the workspace nor reported.

### Verify outputs (read-only)

```
POST /api/v1/admin/submissions/{id}/verify-outputs
```

Downloads each `ws://` output through `Workspace.get_download_url` and streams
it through SHA-1, comparing the result to the `checksum` (`sha1$<hex>`) and
`size` the worker recorded **before** upload. The workspace's own `ObjectSize`
is never trusted. Outputs that still carry a `file://` location on a submission
with an output destination are reported as *not delivered*. Nothing is written.

```json
{
  "data": {
    "submission_id": "sub_abc",
    "state": "COMPLETED",
    "output_state": "delivered",
    "submissions": ["sub_abc", "sub_child1"],
    "files": [
      {
        "submission_id": "sub_abc",
        "location": "ws:///user@bvbrc/home/results/chunks.jsonl.gz",
        "expected_checksum": "sha1$3f786850e387550fdab836ed7e6dc881de23001b",
        "actual_checksum":   "sha1$89e6c98d92887913cadf06b2adb97f26cde4849b",
        "expected_size": 1048576,
        "actual_size": 1572864,
        "ok": false,
        "error": "checksum mismatch"
      }
    ],
    "summary": { "total": 1, "ok": 0, "mismatched": 1, "errors": 0 }
  }
}
```

`summary.mismatched` counts files that downloaded but differ; `summary.errors`
counts files that could not be compared (missing object, no recorded checksum,
not delivered, download failure) — see each file's `error`. A `total` of `0`
means no deliverable output was found at all (for example a submission with
no `File` outputs); such a submission is never marked delivered by re-delivery.

Optional fields (`expected_checksum`, `actual_checksum`, `error`, `action`,
`source`, `dry_run`, `updated`, `state_restored`, `manifest_uploaded`,
`manifest_error`, and the extra summary counters) are omitted when empty.

Each verification download waits at most 5 minutes for response headers and
1 hour end to end.

### Re-deliver outputs

```
POST /api/v1/admin/submissions/{id}/redeliver
POST /api/v1/admin/submissions/{id}/redeliver?dry_run=true
```

Runs the verification above, then for every file that does **not** verify:

1. locates the local original by **checksum + size** among the output `File`
   objects recorded on the submission's tasks (and its children's tasks) —
   `file://<stage-out-dir>/<task-id>/<name>`; basenames are not used because
   scatter shards share them;
2. admits the candidate only if it resolves (symlinks followed) under one of
   the server's `--redeliver-source-dirs`, is a regular file, and has the
   recorded size — FIFOs, devices, directories and wrong-size files are
   skipped without being opened; with no `--redeliver-source-dirs` configured
   local recovery is refused and reported per file;
3. checks the original's SHA-1 against the recorded checksum;
4. re-uploads it through the verified streaming Shock path to the **same**
   workspace path (overwrite) — or, for a never-uploaded `file://` output,
   under the submission's output destination. The target path must lie inside
   the submission's output destination;
5. downloads and re-verifies it, rewriting the output's `location` if it
   changed.

Only when **every** file verifies is `output_state` set to `delivered`; a
submission the scheduler had marked `FAILED` with error code
`OUTPUT_STAGING_FAILED` is then restored to `COMPLETED` (`state_restored:
true`). Whenever the submission row is written, the output manifest
`_gowe_outputs.json` in the destination is rewritten to match
(`manifest_uploaded: true`, or `manifest_error` if that non-fatal step failed).
Nothing is ever deleted.

Writes are compare-and-set against the `state`/`output_state` the request
loaded: if the submission changed meanwhile the response is `409 … changed
concurrently` and nothing is written. Only one re-delivery per submission runs
at a time (`409 re-delivery in progress`); the lock is in-memory, the row is
never marked `uploading`.

The response has the same shape as verification plus per-file `action` and
`source`, and extra summary counters:

| `action`            | Meaning |
|---------------------|---------|
| `verified`          | already matched; nothing done |
| `reuploaded`        | re-uploaded from `source` and re-verified OK |
| `would_reupload`    | dry run: original found at `source`, nothing changed |
| `original_missing`  | no admissible local file with that checksum+size (the `error` lists why each candidate was rejected) |
| `failed`            | not recoverable or the attempt failed — no recorded checksum, basename of the original differs from the workspace object, target path outside the destination, upload error, or the re-verification after upload did not match (`error` begins with `re-verify after upload`) |

```json
{
  "data": {
    "submission_id": "sub_abc",
    "state": "COMPLETED",
    "output_state": "delivered",
    "updated": true,
    "state_restored": true,
    "manifest_uploaded": true,
    "submissions": ["sub_abc"],
    "files": [
      {
        "submission_id": "sub_abc",
        "location": "ws:///user@bvbrc/home/results/chunks.jsonl.gz",
        "expected_checksum": "sha1$3f78…",
        "actual_checksum":   "sha1$3f78…",
        "expected_size": 1048576,
        "actual_size": 1048576,
        "ok": true,
        "action": "reuploaded",
        "source": "/shared/stage-out/task_7f3a/chunks.jsonl.gz"
      }
    ],
    "summary": { "total": 1, "ok": 1, "mismatched": 0, "errors": 0, "reuploaded": 1 }
  }
}
```

### Finding affected submissions

`GET /api/v1/submissions/` accepts `output_state=<state>[,<state>...]`
(lowercase: `delivered`, `upload_failed`, `skipped`, `uploading`), combinable
with `date_start=YYYY-MM-DD`; children are excluded by default.

### CLI

```bash
gowe admin verify-outputs sub_abc                       # one submission
gowe admin verify-outputs --all --since 2026-06-01      # every delivered/upload_failed submission
gowe admin verify-outputs --all --output-state delivered --json
gowe admin redeliver sub_abc --dry-run                  # plan only
gowe admin redeliver sub_abc                            # repair
```

The commands exit non-zero when any output fails verification or re-delivery.
The server is started with `--redeliver-source-dirs /path/to/stage-out` to
allow re-uploads from the shared stage-out directory.

---

## Complete MCP Server Example

Here's a minimal MCP tool that submits a job and polls for completion. This shows the typical flow an MCP server would implement:

```python
import httpx
import time


BASE_URL = "http://localhost:8091/api/v1"


def make_headers(token: str | None = None) -> dict:
    """Build request headers with optional BV-BRC auth."""
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = token
    return headers


def list_workflows(token: str | None = None) -> list[dict]:
    """List all registered workflows."""
    r = httpx.get(f"{BASE_URL}/workflows", headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]


def list_apps(token: str | None = None) -> list[dict]:
    """List available BV-BRC applications."""
    r = httpx.get(f"{BASE_URL}/apps", headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]


def get_app_schema(app_id: str, token: str | None = None) -> dict:
    """Get full parameter schema for a BV-BRC app."""
    r = httpx.get(f"{BASE_URL}/apps/{app_id}", headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]


def get_workflow_inputs(workflow_id: str, token: str | None = None) -> list[dict]:
    """Get input definitions for a workflow."""
    r = httpx.get(f"{BASE_URL}/workflows/{workflow_id}", headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]["inputs"]


def validate_submission(workflow_id: str, inputs: dict, token: str | None = None) -> dict:
    """Dry-run a submission to validate inputs and executor availability."""
    r = httpx.post(
        f"{BASE_URL}/submissions",
        params={"dry_run": "true"},
        json={"workflow_id": workflow_id, "inputs": inputs},
        headers=make_headers(token),
    )
    r.raise_for_status()
    return r.json()["data"]


def submit_job(
    workflow_id: str,
    inputs: dict,
    labels: dict | None = None,
    output_destination: str | None = None,
    token: str | None = None,
) -> dict:
    """Submit a workflow job. Returns the submission object."""
    body = {"workflow_id": workflow_id, "inputs": inputs}
    if labels:
        body["labels"] = labels
    if output_destination:
        body["output_destination"] = output_destination

    r = httpx.post(f"{BASE_URL}/submissions", json=body, headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]


def get_submission(submission_id: str, token: str | None = None) -> dict:
    """Get current submission state."""
    r = httpx.get(f"{BASE_URL}/submissions/{submission_id}", headers=make_headers(token))
    r.raise_for_status()
    return r.json()["data"]


def wait_for_completion(submission_id: str, token: str | None = None, poll_interval: int = 10) -> dict:
    """Poll until submission reaches a terminal state."""
    terminal_states = {"COMPLETED", "FAILED", "CANCELLED"}
    while True:
        sub = get_submission(submission_id, token)
        if sub["state"] in terminal_states:
            return sub
        time.sleep(poll_interval)


# --- Usage ---

token = "un=user@bvbrc|tokenid=xxx|expiry=999|sig=yyy"

# 1. Find a workflow
workflows = list_workflows(token)
wf = next(w for w in workflows if w["name"] == "protein-structure-prediction")

# 2. Check what inputs it needs
inputs_schema = get_workflow_inputs(wf["id"], token)
for inp in inputs_schema:
    print(f"  {inp['id']}: {inp['type']}  required={inp['required']}")

# 3. Validate
validation = validate_submission(wf["id"], {
    "sequence": {"class": "File", "location": "/data/protein.fasta"},
}, token)
assert validation["valid"], validation["errors"]

# 4. Submit
sub = submit_job(
    workflow_id=wf["id"],
    inputs={"sequence": {"class": "File", "location": "/data/protein.fasta"}},
    labels={"target": "7KBF_A"},
    output_destination="ws:///user@bvbrc/home/results/",
    token=token,
)
print(f"Submitted: {sub['id']}")

# 5. Wait for results
result = wait_for_completion(sub["id"], token)
print(f"Final state: {result['state']}")
print(f"Outputs: {result['outputs']}")
```

---

## Existing MCP Server Implementations

The `mcp-servers/` directory contains ready-to-use MCP servers in Python and TypeScript that wrap the BV-BRC workspace and app services (not the GoWe API directly). See `mcp-servers/SETUP.md` for setup instructions.

These provide 16 tools:

| Tool | Description |
|------|-------------|
| `workspace_list` | List workspace directory contents |
| `workspace_get` | Get file metadata/content |
| `workspace_upload` | Upload file content |
| `workspace_delete` | Delete file/folder |
| `workspace_copy` / `workspace_move` | Copy or move files |
| `workspace_share` | Set sharing permissions |
| `workspace_download_url` | Get download URL |
| `apps_list` | List BV-BRC applications |
| `app_schema` | Get app parameter schema |
| `job_submit` | Submit analysis job |
| `job_status` | Check job status |
| `job_list` | List recent jobs |
| `job_cancel` | Cancel a job |
| `job_logs` | Get job logs |

To build an MCP server that talks to the GoWe API (not BV-BRC directly), use the endpoint reference above with the same MCP SDK patterns shown in the existing implementations.
