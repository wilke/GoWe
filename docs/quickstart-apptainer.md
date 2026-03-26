# Quickstart: Apptainer + Distributed Workers

Run CWL workflows in Apptainer containers with distributed worker execution. This is the recommended setup for HPC systems without Docker.

## Prerequisites

- GoWe binaries built (see [Local Quickstart](quickstart-local.md#building))
- Apptainer installed and in PATH
- SIF container images (see [Container Images](#container-images) below)

## Container Images

GoWe resolves `.sif` images from a local directory via `--image-dir`:

```bash
ls /scout/containers/
# alphafold.sif  boltz.sif  chai.sif  esmfold.sif  python.sif  ...
```

If you don't have SIF images, you can pull one for testing:

```bash
mkdir -p ~/containers
apptainer pull ~/containers/python.sif docker://python:3.12-slim
```

## 1. Start the Server

```bash
bin/gowe-server \
  --addr :8080 \
  --allow-anonymous \
  --anonymous-executors local,docker,worker,container \
  --image-dir /scout/containers/
```

## 2. Start a Worker

In a second terminal:

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/
```

You should see:
```
level=INFO msg="worker registered" id=wrk_... runtime=apptainer
level=INFO msg="polling for tasks"
```

The worker appears at http://localhost:8080/workers in the web UI.

## 3. Run the Echo Test

In a third terminal:

```bash
GOWE_SERVER=http://localhost:8080 bin/gowe run \
  test-esmfold/echo-test.cwl \
  --no-upload
```

This runs `echo "hello from GoWe"` inside an Alpine container via Apptainer. Expected output:

```
State: COMPLETED
{
  "result": {
    "basename": "output.txt",
    ...
  }
}
```

## 4. Run a Real Workflow (Boltz Protein Prediction)

This requires the `boltz.sif` image and Boltz model weights in `/local_databases/boltz`.

Start a worker with reference data and GPU:

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/ \
  --pre-stage-dir /local_databases \
  --gpu --gpu-id 0
```

The worker auto-discovers datasets in `/local_databases/` and reports them to the server.

Submit the Boltz workflow:

```bash
GOWE_SERVER=http://localhost:8080 bin/gowe run \
  test-esmfold/boltz-test.cwl \
  test-esmfold/boltz-sif-job.json \
  --no-upload --timeout 10m
```

Or submit to the already-registered workflow by name:

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "boltz-test",
    "inputs": {
      "input_yaml": {
        "class": "File",
        "location": "file:///scout/Experiments/GoWe/test-esmfold/boltz-input.yaml"
      }
    }
  }' | python3 -m json.tool
```

Monitor progress:

```bash
# Via CLI
GOWE_SERVER=http://localhost:8080 bin/gowe status sub_...

# Via API
curl -s http://localhost:8080/api/v1/submissions/sub_... | python3 -m json.tool

# Via web UI
open http://localhost:8080/submissions
```

## Architecture

```
┌──────────┐     HTTP      ┌──────────┐    poll     ┌──────────────┐
│ gowe CLI ├──────────────►│  Server  │◄────────────┤   Worker 1   │
└──────────┘               │  :8080   │             │  apptainer   │
                           │          │             │  --gpu       │
┌──────────┐     HTTP      │ scheduler│    poll     ├──────────────┤
│   curl   ├──────────────►│ SQLite   │◄────────────┤   Worker 2   │
└──────────┘               └──────────┘             │  apptainer   │
                                                    └──────────────┘
```

1. **CLI/API** registers workflows and submits runs
2. **Scheduler** creates tasks, resolves dependencies, matches workers by runtime/datasets
3. **Workers** poll for tasks, execute in Apptainer containers, report results

## Worker Flags Reference

| Flag | Description | Example |
|------|-------------|---------|
| `--server` | GoWe server URL | `http://localhost:8080` |
| `--runtime` | Container runtime | `apptainer` or `docker` |
| `--image-dir` | Directory with `.sif` images | `/scout/containers/` |
| `--pre-stage-dir` | Reference data directory (auto-scanned) | `/local_databases` |
| `--dataset` | Dataset alias (repeatable) | `boltz_weights=/local_databases/boltz` |
| `--extra-bind` | Extra bind mount (repeatable) | `/scratch` |
| `--gpu` | Enable GPU passthrough | |
| `--gpu-id` | Specific GPU device ID | `0` |
| `--group` | Worker group for routing | `gpu-workers` |

## Multi-Worker Setup

Run multiple workers with different capabilities:

```bash
# GPU worker with reference data
bin/gowe-worker --server http://localhost:8080 --runtime apptainer \
  --image-dir /scout/containers/ --pre-stage-dir /local_databases \
  --gpu --gpu-id 0 --group gpu

# CPU-only worker
bin/gowe-worker --server http://localhost:8080 --runtime apptainer \
  --image-dir /scout/containers/ --group cpu
```

The scheduler routes tasks based on container requirements and dataset affinity.

## Next Steps

- [Full Tutorial](tutorial.md) — writing CWL workflows, multi-step pipelines, BV-BRC integration
- [Worker Configuration](tools/worker.md) — detailed flag reference, resource limits
- [Execution Modes](Execution-Modes.md) — network isolation, executor selection
