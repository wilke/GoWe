# GoWe Tutorial: From CWL to Results

This tutorial walks through the complete GoWe workflow lifecycle: writing a CWL workflow, registering it with the server, submitting a run, monitoring progress, and retrieving results.

## Prerequisites

- Go 1.24+ installed
- GoWe built from source:

```bash
mkdir -p bin
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
```

## 1. Start the Server

```bash
bin/gowe-server --debug
```

You should see:

```
level=INFO msg="database ready" path=/Users/you/.gowe/gowe.db
level=INFO msg="server starting" addr=:8080
```

Verify it's running:

```bash
curl -s http://localhost:8080/api/v1/health | jq .
```

```json
{
  "status": "ok",
  "request_id": "req_a1b2c3d4",
  "timestamp": "2026-02-11T10:00:00Z",
  "data": {
    "status": "healthy",
    "version": "0.1.0",
    "uptime": "3s",
    "executors": {
      "local": "available",
      "container": "unavailable",
      "bvbrc": "unavailable"
    }
  }
}
```

The `local` executor is always available. `container` requires Docker and `bvbrc` requires a BV-BRC token.

## 2. Write a CWL Workflow

GoWe uses CWL v1.2 in **packed format** — all tools and the workflow are combined in a single `$graph` document.

### Simple: Single-Step Workflow

Create `hello.cwl`:

```yaml
cwlVersion: v1.2
$graph:
  - id: echo-tool
    class: CommandLineTool
    baseCommand: ["echo"]
    inputs:
      message:
        type: string
    outputs:
      log:
        type: File
        outputBinding:
          glob: "*.txt"

  - id: main
    class: Workflow
    inputs:
      message: string
    steps:
      greet:
        run: "#echo-tool"
        in:
          message: message
        out: [log]
    outputs:
      result:
        type: File
        outputSource: greet/log
```

Key concepts:
- **`$graph`** contains an array of tools and one workflow (the entry with `class: Workflow`)
- **`run: "#echo-tool"`** references a tool by its `id` within the graph
- **`in:`** maps step inputs to workflow inputs or upstream step outputs
- **`baseCommand`** is the command the local executor will run

### Multi-Step: Pipeline with Dependencies

Create `pipeline.cwl`:

```yaml
cwlVersion: v1.2
$graph:
  - id: count-tool
    class: CommandLineTool
    baseCommand: ["wc", "-l"]
    inputs:
      file:
        type: string
    outputs:
      line_count:
        type: File
        outputBinding:
          glob: "*.txt"

  - id: report-tool
    class: CommandLineTool
    baseCommand: ["echo"]
    inputs:
      prefix:
        type: string
      data:
        type: string
    outputs:
      report:
        type: File
        outputBinding:
          glob: "*.txt"

  - id: main
    class: Workflow
    inputs:
      input_file: string
      report_title: string
    steps:
      count:
        run: "#count-tool"
        in:
          file: input_file
        out: [line_count]
      report:
        run: "#report-tool"
        in:
          prefix: report_title
          data: count/line_count
        out: [report]
    outputs:
      final_report:
        type: File
        outputSource: report/report
```

The `report` step depends on `count` — GoWe's scheduler resolves this automatically. The `count/line_count` syntax means "take the `line_count` output from the `count` step."

## 3. Register a Workflow

### Using curl

```bash
# Read the CWL file and POST it as JSON
CWL=$(cat hello.cwl)

curl -s -X POST http://localhost:8080/api/v1/workflows/ \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg cwl "$CWL" '{
    "name": "hello-world",
    "description": "A simple echo workflow",
    "cwl": $cwl
  }')" | jq .
```

Response (201 Created):

```json
{
  "status": "ok",
  "request_id": "req_e5f6a7b8",
  "timestamp": "2026-02-11T10:01:00Z",
  "data": {
    "id": "wf_3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "name": "hello-world",
    "description": "A simple echo workflow",
    "cwl_version": "v1.2",
    "inputs": [
      { "id": "message", "type": "string", "required": true }
    ],
    "outputs": [
      { "id": "result", "type": "File", "output_source": "greet/log" }
    ],
    "steps": [
      {
        "id": "greet",
        "tool_ref": "#echo-tool",
        "depends_on": [],
        "in": [{ "id": "message", "source": "message" }],
        "out": ["log"]
      }
    ],
    "created_at": "2026-02-11T10:01:00Z",
    "updated_at": "2026-02-11T10:01:00Z"
  }
}
```

Save the workflow ID:

```bash
WF_ID="wf_3fa85f64-5717-4562-b3fc-2c963f66afa6"
```

### Using the CLI

```bash
bin/gowe submit hello.cwl --dry-run
```

The CLI bundles separate CWL files into packed format automatically.

## 4. Validate the Workflow (Optional)

```bash
curl -s -X POST http://localhost:8080/api/v1/workflows/$WF_ID/validate | jq .
```

```json
{
  "status": "ok",
  "data": {
    "valid": true,
    "errors": [],
    "warnings": []
  }
}
```

If validation fails, the `errors` array contains details:

```json
{
  "valid": false,
  "errors": [
    { "field": "steps.assemble", "message": "cyclic dependency detected" }
  ]
}
```

## 5. Submit a Run

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d "{
    \"workflow_id\": \"$WF_ID\",
    \"inputs\": {
      \"message\": \"Hello GoWe!\"
    },
    \"labels\": {
      \"environment\": \"tutorial\"
    }
  }" | jq .
```

Response (201 Created):

```json
{
  "status": "ok",
  "data": {
    "id": "sub_7c9e6679-7425-40de-944b-e07fc1f90ae7",
    "workflow_id": "wf_3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "workflow_name": "hello-world",
    "state": "PENDING",
    "inputs": {
      "message": "Hello GoWe!"
    },
    "labels": {
      "environment": "tutorial"
    },
    "tasks": [
      {
        "id": "task_a1234567-89ab-cdef-0123-456789abcdef",
        "submission_id": "sub_7c9e6679-7425-40de-944b-e07fc1f90ae7",
        "step_id": "greet",
        "state": "PENDING",
        "executor_type": "local",
        "depends_on": [],
        "retry_count": 0,
        "max_retries": 3,
        "created_at": "2026-02-11T10:02:00Z"
      }
    ],
    "created_at": "2026-02-11T10:02:00Z",
    "completed_at": null
  }
}
```

Save the submission ID:

```bash
SUB_ID="sub_7c9e6679-7425-40de-944b-e07fc1f90ae7"
```

The submission starts in `PENDING` state. The scheduler picks it up within 2 seconds.

**Request fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `workflow_id` | Yes | ID from step 3 |
| `inputs` | No | Key-value map matching the workflow's declared inputs |
| `labels` | No | Arbitrary metadata for filtering/tracking |

## 6. Monitor Progress

### Poll the Submission

```bash
curl -s http://localhost:8080/api/v1/submissions/$SUB_ID/ | jq .
```

The `state` field progresses through:

```
PENDING → RUNNING → COMPLETED
                  → FAILED
                  → CANCELLED
```

Each task inside progresses through:

```
PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS
                                        → FAILED → RETRYING → QUEUED → ...
```

### Watch with a Loop

```bash
while true; do
  STATE=$(curl -s http://localhost:8080/api/v1/submissions/$SUB_ID/ | jq -r '.data.state')
  echo "State: $STATE"
  case $STATE in
    COMPLETED|FAILED|CANCELLED) break ;;
  esac
  sleep 2
done
```

For a simple local echo command this completes almost instantly.

### List All Tasks

```bash
curl -s http://localhost:8080/api/v1/submissions/$SUB_ID/tasks/ | jq .
```

```json
{
  "status": "ok",
  "data": [
    {
      "id": "task_a1234567-89ab-cdef-0123-456789abcdef",
      "step_id": "greet",
      "state": "SUCCESS",
      "executor_type": "local",
      "started_at": "2026-02-11T10:02:02Z",
      "completed_at": "2026-02-11T10:02:02Z"
    }
  ]
}
```

## 7. Retrieve Results

### Task Logs

```bash
TASK_ID="task_a1234567-89ab-cdef-0123-456789abcdef"

curl -s http://localhost:8080/api/v1/submissions/$SUB_ID/tasks/$TASK_ID/logs | jq .
```

```json
{
  "status": "ok",
  "data": {
    "task_id": "task_a1234567-89ab-cdef-0123-456789abcdef",
    "step_id": "greet",
    "stdout": "Hello GoWe!\n",
    "stderr": "",
    "exit_code": 0
  }
}
```

### Using the CLI

```bash
bin/gowe status $SUB_ID
bin/gowe logs $SUB_ID
```

## 8. Clean Up

Delete the workflow when you're done:

```bash
curl -s -X DELETE http://localhost:8080/api/v1/workflows/$WF_ID/ | jq .
```

```json
{
  "status": "ok",
  "data": { "deleted": true }
}
```

## 9. Multi-Step Pipeline Example

This example uses the BV-BRC assembly + annotation pipeline from `testdata/packed/pipeline-packed.cwl`:

```yaml
cwlVersion: v1.2
$graph:
  - id: bvbrc-assembly
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAssembly2
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      read1: { type: File }
      read2: { type: File }
      recipe: { type: string, default: "auto" }
    outputs:
      contigs: { type: File, outputBinding: { glob: "*.contigs.fasta" } }

  - id: bvbrc-annotation
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAnnotation
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      contigs: { type: File }
      scientific_name: { type: string }
      taxonomy_id: { type: int }
    outputs:
      annotated_genome: { type: File, outputBinding: { glob: "*.genome" } }

  - id: main
    class: Workflow
    inputs:
      reads_r1: File
      reads_r2: File
      scientific_name: string
      taxonomy_id: int
    steps:
      assemble:
        run: "#bvbrc-assembly"
        in:
          read1: reads_r1
          read2: reads_r2
        out: [contigs]
      annotate:
        run: "#bvbrc-annotation"
        in:
          contigs: assemble/contigs
          scientific_name: scientific_name
          taxonomy_id: taxonomy_id
        out: [annotated_genome]
    outputs:
      genome:
        type: File
        outputSource: annotate/annotated_genome
```

Key differences from the simple example:

- **`goweHint`** selects the `bvbrc` executor and maps to a BV-BRC application
- **`assemble/contigs`** in the `annotate` step's `in:` block means "wait for `assemble` to finish, then use its `contigs` output"
- The scheduler runs `assemble` first, then `annotate` — dependencies are resolved automatically from the `in:` mappings

### Submit (requires BV-BRC token)

```bash
# Authenticate first
bin/gowe login

# Submit
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d "{
    \"workflow_id\": \"$WF_ID\",
    \"inputs\": {
      \"reads_r1\": \"/user@bvbrc/home/sample_R1.fastq.gz\",
      \"reads_r2\": \"/user@bvbrc/home/sample_R2.fastq.gz\",
      \"scientific_name\": \"Escherichia coli\",
      \"taxonomy_id\": 562
    }
  }" | jq .
```

BV-BRC jobs are asynchronous — poll the submission until all tasks reach `SUCCESS` or `FAILED`.

## Executor Selection Reference

GoWe picks the executor for each step based on CWL hints:

| Hint | Executor | Use Case |
|------|----------|----------|
| *(none)* | `local` | Run as OS process |
| `DockerRequirement` | `container` | Run in Docker container |
| `goweHint.executor: bvbrc` | `bvbrc` | Submit to BV-BRC |
| `goweHint.docker_image` | `container` | Run in Docker container |

## API Response Envelope

All GoWe API responses use a standard envelope:

```json
{
  "status": "ok | error",
  "request_id": "req_...",
  "timestamp": "2026-02-11T10:00:00Z",
  "data": {},
  "pagination": {
    "total": 10,
    "limit": 20,
    "offset": 0,
    "has_more": false
  },
  "error": {
    "code": "validation_error",
    "message": "...",
    "details": [{ "field": "cwl", "message": "..." }]
  }
}
```

- `data` is present on success, `error` on failure
- `pagination` is included for list endpoints
- `request_id` can be used for debugging/support

## Task State Machine

```
PENDING ─── dependencies met ──→ SCHEDULED ──→ QUEUED ──→ RUNNING ──→ SUCCESS
   │                                                          │
   │         upstream failed                                  ├──→ FAILED
   └──────→ SKIPPED                                           │
                                                              └──→ RETRYING ──→ QUEUED
                                                                   (up to 3 retries)
```

## Quick Reference

| Action | curl | CLI |
|--------|------|-----|
| Health check | `GET /api/v1/health` | — |
| Create workflow | `POST /api/v1/workflows/` | `gowe submit file.cwl --dry-run` |
| List workflows | `GET /api/v1/workflows/` | — |
| Validate | `POST /api/v1/workflows/{id}/validate` | — |
| Submit run | `POST /api/v1/submissions/` | `gowe submit file.cwl` |
| Check status | `GET /api/v1/submissions/{id}/` | `gowe status {id}` |
| List tasks | `GET /api/v1/submissions/{id}/tasks/` | — |
| Get logs | `GET /api/v1/submissions/{id}/tasks/{tid}/logs` | `gowe logs {id}` |
| Cancel | `PUT /api/v1/submissions/{id}/cancel` | `gowe cancel {id}` |
| Delete workflow | `DELETE /api/v1/workflows/{id}/` | — |
