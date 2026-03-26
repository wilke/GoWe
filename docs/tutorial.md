# GoWe Tutorial: From CWL to Results

This tutorial walks through the complete GoWe workflow lifecycle: writing a CWL workflow, registering it with the server, submitting a run, monitoring progress, and retrieving results.

## Table of Contents

- [Prerequisites](#prerequisites)
- [1. Start the Server](#1-start-the-server)
- [2. Write a CWL Workflow](#2-write-a-cwl-workflow)
  - [Simple: Single-Step Workflow](#simple-single-step-workflow)
  - [Multi-Step: Pipeline with Dependencies](#multi-step-pipeline-with-dependencies)
- [3. Register a Workflow](#3-register-a-workflow)
- [4. Validate the Workflow (Optional)](#4-validate-the-workflow-optional)
- [5. Submit a Run](#5-submit-a-run)
- [6. Monitor Progress](#6-monitor-progress)
- [7. Retrieve Results](#7-retrieve-results)
- [8. Clean Up](#8-clean-up)
- [9. Multi-Step Pipeline Example](#9-multi-step-pipeline-example)
- [10. Distributed Execution with Workers](#10-distributed-execution-with-workers)
  - [Using Docker Compose](#using-docker-compose)
  - [Running Workflows](#running-workflows)
  - [Using goweHint for Worker Selection](#using-gowehint-for-worker-selection)
- [11. Distributed Execution with Apptainer (No Docker)](#11-distributed-execution-with-apptainer-no-docker)
  - [Architecture](#architecture)
  - [Build Binaries](#build-binaries)
  - [Start the Server](#start-the-server)
  - [Start Workers](#start-workers)
  - [GPU Workers](#gpu-workers)
  - [Multi-Node Setup](#multi-node-setup)
  - [Known Limitations](#known-limitations)
- [Executor Selection Reference](#executor-selection-reference)
- [API Response Envelope](#api-response-envelope)
- [Task State Machine](#task-state-machine)
- [Quick Reference](#quick-reference)

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
| `workflow_id` | Yes | Workflow ID (`wf_...`) or workflow name (e.g. `"boltz-test"`) |
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

## 10. Distributed Execution with Workers

GoWe supports distributed task execution across multiple worker nodes. This is useful for:
- Scaling execution across a cluster
- Running tasks on specialized hardware
- Isolating task execution from the API server

### Using Docker Compose

The quickest way to try distributed execution is with Docker Compose:

```bash
# Build and start the cluster
docker-compose up -d --build

# Verify the cluster is running
curl -s http://localhost:8090/api/v1/health | jq .
curl -s http://localhost:8090/api/v1/workers | jq '.data | length'
```

The default `docker-compose.yml` starts:
- 1 server with `--default-executor=worker`, `--upload-backend=local`
- 2 workers with `--runtime=none` (execute on host)
- 1 worker with `--runtime=docker` (execute in containers, `DOCKER_VOLUME=gowe-workdir`)

All services share a named Docker volume (`gowe-workdir`) mounted at `/workdir`.

### Running Workflows

Use the `gowe run` command for cwltest-compatible execution:

```bash
# Build the CLI
go build -o bin/gowe ./cmd/cli

# Run a workflow against the distributed cluster
./bin/gowe run --server http://localhost:8090 testdata/worker-test/simple-echo.cwl testdata/worker-test/simple-echo-job.yml
```

Output:

```json
{
  "output": {
    "class": "File",
    "location": "file:///tmp/gowe-output123/output.txt",
    "basename": "output.txt",
    "checksum": "sha1$...",
    "size": 19
  }
}
```

### Using goweHint for Worker Selection

You can explicitly request the worker executor using CWL hints:

```yaml
steps:
  heavy_computation:
    run: tools/compute.cwl
    hints:
      goweHint:
        executor: worker
    in:
      data: input_data
    out: [result]
```

Or set `--default-executor=worker` on the server to route all tasks to workers.

### Test Scripts

```bash
# Run the distributed integration test
./scripts/test-distributed.sh

# Clean up
docker-compose down -v
```

## 11. Distributed Execution with Apptainer (No Docker)

On HPC systems where Docker is unavailable, you can run the full distributed stack as native host processes using Apptainer as the container runtime.

### Architecture

All three process types run on the same machine (or across nodes via a shared filesystem):

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  gowe-server │◄───►│  worker-1    │     │  worker-2    │
│  (scheduler) │◄───►│  (apptainer) │     │  (apptainer) │
│  port 8080   │     │  GPU 0       │     │  GPU 1       │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       │              ┌─────┴────────────────────┘
       ▼              ▼
  ┌─────────┐   ┌──────────┐
  │ uploads │   │ data/    │    ◄── shared filesystem (file:// staging)
  └─────────┘   └──────────┘
```

- **Server**: Receives submissions, schedules tasks, dispatches to workers
- **Workers**: Poll server, execute CWL tools in Apptainer containers
- **CLI**: Bundles CWL, uploads inputs, submits, downloads outputs

File transfer uses **upload mode**: the CLI uploads input files to the server, workers stage outputs to a shared directory via `file://`, and the server serves those files back for CLI download.

### Build Binaries

Go is often not natively installed on HPC systems. Build via Apptainer:

```bash
mkdir -p bin /tmp/gomod
apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c \
  "cd $(pwd) && go build -o bin/ ./cmd/server ./cmd/worker && go build -o bin/gowe ./cmd/cli"
```

### Set Up Working Directories

```bash
BASE_DIR="/scratch/gowe"   # Use fast local storage
mkdir -p "$BASE_DIR/gowe/uploads" "$BASE_DIR/data" \
         "$BASE_DIR/gowe/workdir/worker-1" \
         "$BASE_DIR/gowe/workdir/worker-2"
```

| Directory | Purpose |
|-----------|---------|
| `gowe/uploads/` | Server stores files uploaded by CLI |
| `data/` | Workers copy outputs here (file:// stage-out) |
| `gowe/workdir/worker-N/` | Per-worker scratch space |

### Start the Server

```bash
./bin/server \
    --addr ":8080" \
    --db "$BASE_DIR/gowe/gowe.db" \
    --default-executor worker \
    --allow-anonymous \
    --anonymous-executors "local,docker,worker,container" \
    --scheduler-poll 100ms \
    --upload-backend local \
    --upload-local-dir "$BASE_DIR/gowe/uploads" \
    --upload-download-dirs "$BASE_DIR/gowe/uploads,$BASE_DIR/data,$BASE_DIR/gowe/workdir/worker-1,$BASE_DIR/gowe/workdir/worker-2" \
    --log-level info &
```

Key flags:
- `--default-executor worker` routes all tasks to workers
- `--upload-backend local` + `--upload-local-dir` enables file upload from CLI
- `--upload-download-dirs` lists all directories the server may serve files from (uploads, stage-out, and worker scratch dirs)

### Start Workers

```bash
for i in 1 2; do
    ./bin/worker \
        --server "http://localhost:8080" \
        --runtime apptainer \
        --name "worker-${i}" \
        --workdir "$BASE_DIR/gowe/workdir/worker-${i}" \
        --stage-out "file://$BASE_DIR/data" \
        --poll 500ms \
        --log-level info &
done
```

Key flags:
- `--runtime apptainer` uses Apptainer for CWL tools with `DockerRequirement` (tools without it run locally)
- `--stage-out "file://..."` copies outputs to the shared directory

### GPU Workers

For GPU workloads (e.g., protein structure prediction), bind each worker to a specific GPU:

```bash
for GPU_ID in 0 1 2 3 4 5 6 7; do
    ./bin/worker \
        --server "http://localhost:8080" \
        --runtime apptainer \
        --gpu \
        --gpu-id "$GPU_ID" \
        --name "gpu-worker-$GPU_ID" \
        --group "gpu-workers" \
        --workdir "$BASE_DIR/gowe/workdir/worker-$GPU_ID" \
        --stage-out "file://$BASE_DIR/data" \
        --poll 2s \
        --log-level info &
done
```

The `--gpu` flag passes `--nv` to Apptainer for NVIDIA GPU passthrough. The `--gpu-id` flag sets `CUDA_VISIBLE_DEVICES` to isolate each worker to one GPU.

### Verify the Cluster

```bash
# Check server health
curl -s http://localhost:8080/api/v1/health | jq .

# List registered workers
curl -s http://localhost:8080/api/v1/workers | jq '.data[] | {name, state, runtime}'

# Count workers
curl -s http://localhost:8080/api/v1/workers | jq '.data | length'
```

### Run a Workflow

```bash
export GOWE_SERVER="http://localhost:8080"

# Run a CWL tool (upload mode — CLI handles file transfer)
./bin/gowe run my-tool.cwl my-job.yml

# Submit a workflow
./bin/gowe submit my-workflow.cwl -i my-inputs.yml

# Check status
./bin/gowe list
./bin/gowe status sub_XXXXX
./bin/gowe logs sub_XXXXX
```

### Multi-Node Setup

For clusters with shared storage (NFS, Lustre, GPFS), run the server on a head node and workers on compute nodes:

```bash
# On the head node
./bin/server \
    --addr "0.0.0.0:8080" \
    --default-executor worker \
    --upload-backend local \
    --upload-local-dir "/shared/gowe/uploads" \
    --upload-download-dirs "/shared/gowe/uploads,/shared/gowe/data,/shared/gowe/workdir" \
    ...

# On each compute node (the server URL must be reachable)
./bin/worker \
    --server "http://head-node:8080" \
    --runtime apptainer \
    --gpu --gpu-id 0 \
    --name "$(hostname)-gpu0" \
    --workdir "/shared/gowe/workdir/$(hostname)-gpu0" \
    --stage-out "file:///shared/gowe/data" \
    --poll 2s
```

All paths under `--upload-download-dirs` must be accessible from both the server and workers. On a shared filesystem this happens naturally.

### Run CWL Conformance Tests

A dedicated script runs the full CWL v1.2 conformance suite in this mode:

```bash
# Run all 378 tests (375 pass, 3 known failures)
./scripts/run-conformance-distributed-apptainer.sh

# Required tests only (faster)
./scripts/run-conformance-distributed-apptainer.sh required

# Custom port, keep processes running after tests
./scripts/run-conformance-distributed-apptainer.sh -p 9092 -k
```

### Cleanup

```bash
# Kill workers and server
pkill -f "bin/worker"
pkill -f "bin/server"

# Remove working data
rm -rf "$BASE_DIR"
```

### Known Limitations

- **Network isolation**: Apptainer shares the host network. CWL's `NetworkAccess: false` cannot be enforced without root (test 227 fails).
- **SIF cache contention**: Multiple workers pulling the same image simultaneously can be slow. Pre-pull images or use a shared SIF cache (`APPTAINER_CACHEDIR`).
- **Rootless only**: Apptainer runs unprivileged by default. Features requiring `--fakeroot` (e.g., `--writable`) may not work on all HPC configurations.

## Executor Selection Reference

GoWe picks the executor for each step based on CWL hints:

| Hint | Executor | Use Case |
|------|----------|----------|
| *(none)* | `local` | Run as OS process |
| `DockerRequirement` | `docker` | Run in Docker container |
| `goweHint.executor: worker` | `worker` | Dispatch to remote workers |
| `goweHint.executor: bvbrc` | `bvbrc` | Submit to BV-BRC |
| `goweHint.docker_image` | `docker` | Run in Docker container |

When `--default-executor=worker` is set on the server, all tasks (regardless of hints) are routed to workers.

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
