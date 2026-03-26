# Quickstart: Local Execution

Run a CWL workflow locally in under 5 minutes. No containers needed.

## Prerequisites

- GoWe binaries built (see [Building](#building) below)

## Building

```bash
cd /scout/Experiments/GoWe
mkdir -p bin

# If Go is installed natively:
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
go build -o bin/gowe-worker ./cmd/worker
go build -o bin/cwl-runner ./cmd/cwl-runner

# If using Apptainer (Go not installed):
mkdir -p /tmp/gomod
apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c \
  "cd /scout/Experiments/GoWe && \
   go build -o bin/gowe-server ./cmd/server && \
   go build -o bin/gowe ./cmd/cli && \
   go build -o bin/gowe-worker ./cmd/worker && \
   go build -o bin/cwl-runner ./cmd/cwl-runner"
```

## 1. Start the Server

```bash
bin/gowe-server --addr :8080 --allow-anonymous --anonymous-executors local
```

You should see:
```
level=INFO msg="GoWe server listening" addr=:8080
level=INFO msg="scheduler started" tick=2s
```

Open http://localhost:8080 in a browser to see the web UI.

## 2. Run a Simple Echo Workflow

In a second terminal:

```bash
cd /scout/Experiments/GoWe

GOWE_SERVER=http://localhost:8080 bin/gowe run \
  testdata/worker-test/simple-echo.cwl \
  testdata/worker-test/simple-echo-job.yml \
  --no-upload
```

Expected output:
```
Bundling testdata/worker-test/simple-echo.cwl...
Creating workflow simple-echo...
Submitting with workflow ID wf_...
Submission created: sub_...
State: RUNNING
State: COMPLETED
{
  "output": {
    "basename": "message.txt",
    ...
  }
}
```

The workflow ran `echo "Hello from worker!"` and captured the output in `message.txt`.

## 3. Verify via the API

List registered workflows:
```bash
curl -s http://localhost:8080/api/v1/workflows | python3 -m json.tool
```

List submissions:
```bash
curl -s http://localhost:8080/api/v1/submissions | python3 -m json.tool
```

## 4. Submit Again by Name

Now that `simple-echo` is registered, you can submit directly by name:

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "simple-echo", "inputs": {"message": "Hello again!"}}' \
  | python3 -m json.tool
```

## What Happened

1. **`gowe run`** bundled the CWL file, registered it as a workflow, and submitted a run
2. The **scheduler** picked up the submission and created a task
3. The **local executor** ran `echo "Hello from worker!"` as an OS process
4. Results were collected and returned as CWL output JSON

## Next Steps

- [Quickstart: Apptainer + Workers](quickstart-apptainer.md) — distributed execution with containers
- [Full Tutorial](tutorial.md) — writing CWL workflows, multi-step pipelines, monitoring
- [Worker Configuration](tools/worker.md) — GPU support, reference data, bind mounts
