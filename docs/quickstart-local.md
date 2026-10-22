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

> **Building a separate browser client?** The built-in UI above is server-rendered and
> same-origin, so it needs no configuration. A standalone browser client calling `/api/v1`
> directly is different: it should sit behind a same-origin reverse proxy that injects the
> bearer token server-side (never ship the token to the browser). `--cors-origins` exists for
> deliberate cross-origin deployments and is off by default. See
> [`docs/PRODUCTION.md`](PRODUCTION.md#browser-clients--cors) for the full story.

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

> **Submit-by-name is sharper than it looks.** Workflow registration is append-only: each
> `POST /api/v1/workflows` under an existing name creates a new version rather than replacing
> it, and submitting by name always resolves to the *newest* version registered under that
> name — not a pinned one. That's fine for a quickstart where you're the only one registering
> `simple-echo`. Once a name might be re-registered by someone (or something) else — CI
> re-publishing the same workflow, another user reusing the name — submit by the concrete
> `wf_...` ID returned at registration instead, so you know exactly which version ran.

## What Happened

1. **`gowe run`** bundled the CWL file, registered it as a workflow, and submitted a run
2. The **scheduler** picked up the submission and created a task
3. The **local executor** ran `echo "Hello from worker!"` as an OS process
4. Results were collected and returned as CWL output JSON

## Next Steps

- [Quickstart: Apptainer + Workers](quickstart-apptainer.md) — distributed execution with containers
- [Full Tutorial](tutorial.md) — writing CWL workflows, multi-step pipelines, monitoring
- [Worker Configuration](tools/worker.md) — GPU support, reference data, bind mounts
