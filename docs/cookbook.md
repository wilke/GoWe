# GoWe Cookbook

Practical recipes for common tasks. Each recipe is self-contained — copy, paste, adapt.

---

## Server

### Start a minimal test server

```bash
bin/gowe-server --addr :8080 --allow-anonymous --anonymous-executors local
```

Local executor only, no auth required. Good for development.

### Start a production server with all executors

```bash
bin/gowe-server \
  --addr :8091 \
  --db /data/gowe.db \
  --allow-anonymous \
  --anonymous-executors local,docker,worker,container \
  --image-dir /scout/containers/ \
  --upload-backend local \
  --upload-dir /data/uploads
```

### Start fresh (reset database)

```bash
rm ~/.gowe/gowe.db        # default location
bin/gowe-server            # creates a new empty database on startup
```

Or specify a new path:

```bash
bin/gowe-server --db /tmp/fresh.db
```

### Find which port the server is on

```bash
ss -tlnp | grep gowe
```

---

## Workflows

### Register a workflow from a CWL file

```bash
GOWE_SERVER=http://localhost:8080 bin/gowe run my-tool.cwl job.json --no-upload
```

This bundles, registers, submits, and waits for completion in one step.

To register without running:

```bash
CWL=$(cat my-tool.cwl)
curl -s -X POST http://localhost:8080/api/v1/workflows/ \
  -H "Content-Type: application/json" \
  -d "{\"name\": \"my-tool\", \"cwl\": $(echo "$CWL" | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')}" \
  | python3 -m json.tool
```

### List registered workflows

```bash
curl -s http://localhost:8080/api/v1/workflows | python3 -m json.tool
```

Or visit http://localhost:8080/workflows in the browser.

### Delete a workflow

```bash
curl -s -X DELETE http://localhost:8080/api/v1/workflows/wf_... | python3 -m json.tool
```

### Check if a workflow is already registered (deduplication)

GoWe computes a SHA-256 hash of the CWL content. Submitting identical CWL returns the existing workflow (HTTP 200) instead of creating a duplicate (HTTP 201).

```bash
# Submit twice — second call returns the same workflow ID
GOWE_SERVER=http://localhost:8080 bin/gowe run my-tool.cwl job.json --no-upload
GOWE_SERVER=http://localhost:8080 bin/gowe run my-tool.cwl job.json --no-upload
# Same wf_... ID both times
```

---

## Submissions

### Submit a job to a registered workflow by name

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "boltz-test",
    "inputs": {
      "input_yaml": {
        "class": "File",
        "location": "file:///path/to/input.yaml"
      }
    }
  }' | python3 -m json.tool
```

`workflow_id` accepts either an ID (`wf_...`) or a workflow name.

### Submit with a string input

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "simple-echo",
    "inputs": {"message": "Hello world"}
  }' | python3 -m json.tool
```

### Pass a File input via the API

File inputs need `class: File` and a `location`:

```json
{
  "workflow_id": "my-tool",
  "inputs": {
    "reads": {
      "class": "File",
      "location": "file:///data/reads.fastq"
    }
  }
}
```

For remote files (requires upload backend):

```json
{
  "class": "File",
  "location": "http://example.com/data/reads.fastq"
}
```

### Pass a Directory input

```json
{
  "database": {
    "class": "Directory",
    "location": "file:///data/reference_db"
  }
}
```

### Submit using the CLI with an already-registered workflow

Use `--workflow` to skip CWL bundling and reference a workflow by ID or name:

```bash
# By name
bin/gowe submit --workflow protein-structure-prediction -i inputs.yaml

# By ID
bin/gowe submit --workflow wf_f8975ed7-0ea8-48a9-bbcb-f6ebad1305b9 -i inputs.yaml

# With workspace output staging
bin/gowe submit --workflow protein-structure-prediction -i inputs.yaml \
  --output-destination "ws:///user@bvbrc/home/results/"
```

### Re-run a workflow with different inputs

Just submit again with new inputs — same workflow, new submission:

```bash
curl -s -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "boltz-test", "inputs": {"input_yaml": {"class": "File", "location": "file:///path/to/different-input.yaml"}}}'
```

### Dry-run (validate without executing)

```bash
curl -s -X POST "http://localhost:8080/api/v1/submissions/?dry_run=true" \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "my-tool", "inputs": {"message": "test"}}' \
  | python3 -m json.tool
```

### List submissions for a workflow

Filter by workflow name:

```bash
curl -s "http://localhost:8080/api/v1/submissions?workflow_id=boltz-test" | python3 -m json.tool
```

Or by workflow ID:

```bash
curl -s "http://localhost:8080/api/v1/submissions?workflow_id=wf_c6fab32b-..." | python3 -m json.tool
```

Combine filters — e.g. completed runs of boltz-test:

```bash
curl -s "http://localhost:8080/api/v1/submissions?workflow_id=boltz-test&state=COMPLETED" | python3 -m json.tool
```

### Cancel a running submission

```bash
curl -s -X PUT http://localhost:8080/api/v1/submissions/sub_.../cancel | python3 -m json.tool
```

Or via CLI:

```bash
GOWE_SERVER=http://localhost:8080 bin/gowe cancel sub_...
```

---

## Monitoring

### Check submission status

```bash
curl -s http://localhost:8080/api/v1/submissions/sub_... | python3 -m json.tool
```

Or via CLI:

```bash
GOWE_SERVER=http://localhost:8080 bin/gowe status sub_...
```

### Poll until completion

```bash
SUB_ID=sub_...
while true; do
  STATE=$(curl -s http://localhost:8080/api/v1/submissions/$SUB_ID | python3 -c "import sys,json; print(json.load(sys.stdin)['data']['state'])")
  echo "State: $STATE"
  case $STATE in COMPLETED|FAILED|CANCELLED) break ;; esac
  sleep 5
done
```

### List tasks for a submission

```bash
curl -s http://localhost:8080/api/v1/submissions/sub_.../tasks | python3 -m json.tool
```

### Get task logs

```bash
curl -s http://localhost:8080/api/v1/submissions/sub_.../tasks/task_.../logs | python3 -m json.tool
```

### Check which worker ran a task

```bash
curl -s http://localhost:8080/api/v1/submissions/sub_.../tasks/task_... \
  | python3 -c "import sys,json; t=json.load(sys.stdin)['data']; print(f'worker={t.get(\"worker_id\",\"local\")}, state={t[\"state\"]}')"
```

---

## Workers

### Start an Apptainer worker

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/
```

### Start a GPU worker with reference data

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/ \
  --pre-stage-dir /local_databases \
  --gpu --gpu-id 0
```

`--pre-stage-dir` scans subdirectories and registers them as datasets. For `/local_databases/` with `boltz/`, `alphafold/`, `chai/` inside, the worker reports datasets `[boltz, alphafold, chai]`.

### Add dataset aliases

When CWL hints use IDs that don't match directory names:

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/ \
  --pre-stage-dir /local_databases \
  --dataset boltz_weights=/local_databases/boltz \
  --dataset alphafold_databases=/local_databases/alphafold
```

### Bind-mount extra directories

For scratch space, licenses, or other paths that don't need scheduler awareness:

```bash
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/ \
  --extra-bind /scratch \
  --extra-bind /opt/licenses
```

### Inject secrets into containers

For tools that need authentication tokens (e.g., HuggingFace):

```bash
# Create a secrets file (chmod 600)
cat > /path/to/secrets.env << 'EOF'
# HuggingFace authentication
HF_TOKEN=hf_abc123
HUGGING_FACE_HUB_TOKEN=hf_abc123
EOF
chmod 600 /path/to/secrets.env

# Start worker with secrets
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/ \
  --secret-file /path/to/secrets.env
```

Secrets are injected into every container but never sent to the server or stored in task data.

### Route tasks to a specific worker group

Use `gowe:Execution.worker_group` in CWL to target a group:

```yaml
hints:
  gowe:Execution:
    worker_group: esmfold
```

Then start a worker in that group:

```bash
bin/gowe-worker --server http://localhost:8080 --runtime apptainer \
  --image-dir /scout/containers/ --group esmfold --gpu --gpu-id 3
```

Or override the group at submit time:

```bash
bin/gowe submit workflow.cwl -i job.json --group esmfold
```

### Run multiple workers on one machine

```bash
# GPU worker for protein prediction
bin/gowe-worker --server http://localhost:8080 --runtime apptainer \
  --image-dir /scout/containers/ --pre-stage-dir /local_databases \
  --gpu --gpu-id 0 --group gpu &

# CPU worker for general tasks
bin/gowe-worker --server http://localhost:8080 --runtime apptainer \
  --image-dir /scout/containers/ --group cpu &
```

### Check registered workers

```bash
curl -s http://localhost:8080/api/v1/workers | python3 -m json.tool
```

Or visit http://localhost:8080/workers.

### Worker went offline — what happened?

Workers send heartbeats every 10 seconds. If the server doesn't hear from a worker for 30 seconds, it marks it offline and requeues its tasks.

Common causes:
- Worker process crashed or was killed
- Network issue between worker and server
- Worker machine went down

Check server logs for `worker_offline` messages.

---

## CWL Recipes

### Minimal CommandLineTool

```yaml
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [echo]

inputs:
  message:
    type: string
    inputBinding:
      position: 1

outputs:
  output:
    type: stdout
```

### Tool with Apptainer SIF image

```yaml
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [python3, script.py]

hints:
  DockerRequirement:
    dockerPull: "python.sif"    # resolved from --image-dir

inputs:
  data:
    type: File
    inputBinding:
      position: 1

outputs:
  result:
    type: File
    outputBinding:
      glob: "output.csv"
```

`.sif` names are resolved against the worker's `--image-dir`. Non-`.sif` names (e.g. `python:3.12`) are pulled from a registry via `docker://` or `apptainer pull`.

### Declare reference data requirements

```yaml
cwlVersion: v1.2
class: CommandLineTool

$namespaces:
  gowe: https://github.com/wilke/GoWe#

hints:
  DockerRequirement:
    dockerPull: "boltz.sif"
  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        size: 50GB
        mode: cache          # prefer workers that have it
      - id: alphafold
        path: /local_databases/alphafold
        size: 2TB
        mode: prestage       # require workers that have it

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

**Modes:**
- `prestage` — scheduler **requires** a worker with this dataset (task waits if none available)
- `cache` — scheduler **prefers** workers with this dataset but dispatches elsewhere if needed

### Multi-step workflow

```yaml
cwlVersion: v1.2
class: Workflow

inputs:
  sequence:
    type: File

steps:
  predict:
    run: boltz-predict.cwl
    in:
      input_yaml: sequence
    out: [predicted_cif]

  analyze:
    run: protein-compare.cwl
    in:
      structure: predict/predicted_cif
    out: [metrics]

outputs:
  metrics:
    type: File
    outputSource: analyze/metrics
```

---

## Troubleshooting

### Task stuck in PENDING

The scheduler creates tasks in PENDING, then moves them to READY when dependencies are met, then dispatches to workers.

Check:
1. **Is a worker online?** `curl -s http://localhost:8080/api/v1/workers | python3 -m json.tool`
2. **Does the task need a dataset the worker doesn't have?** Check task's `runtime_hints.required_datasets` vs worker's `datasets`
3. **Is the worker's runtime compatible?** Docker tasks need Docker workers, Apptainer tasks need Apptainer workers
4. **Check server logs** for scheduler messages

### File not found inside container

Container paths must match what the CWL expects. Common fixes:

- **Input files**: Use `file:///absolute/path` in job inputs
- **Reference data**: Use `--pre-stage-dir` or `--extra-bind` on the worker so paths are bind-mounted
- **SIF images**: Use `--image-dir` on the worker pointing to the directory with `.sif` files

### "Unknown workflow" when submitting by name

Workflow names are case-sensitive. Check the exact name:

```bash
curl -s http://localhost:8080/api/v1/workflows \
  | python3 -c "import sys,json; [print(w['name']) for w in json.load(sys.stdin)['data']]"
```

### Worker can't reach server

```bash
# Test connectivity from worker machine
curl -s http://server-host:8080/api/v1/workers | head -1
```

The worker needs HTTP access to the server's `/api/v1/` endpoints.

### Submission completed but outputs are empty

Check task logs for stderr:

```bash
curl -s http://localhost:8080/api/v1/submissions/sub_.../tasks \
  | python3 -c "import sys,json; [print(t['id'], t['state']) for t in json.load(sys.stdin)['data']]"

# Then for each task:
curl -s http://localhost:8080/api/v1/submissions/sub_.../tasks/task_.../logs | python3 -m json.tool
```

Common causes:
- Output glob pattern doesn't match actual filenames
- Tool wrote to wrong directory (use `$(runtime.outdir)` in CWL)
- Container exited with error but GoWe captured partial outputs

---

## BV-BRC Workspace Staging

GoWe supports staging files to/from BV-BRC workspaces using `ws://` URIs. Two deployment modes are available.

### Mode A: Worker-side staging (cloud/distributed workers)

Workers download `ws://` inputs and upload outputs directly. Best when workers have internet access.

```bash
# Server (normal, no workspace flags needed)
bin/gowe-server --addr :8080 --default-executor worker --allow-anonymous

# Worker with workspace staging enabled
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --workspace-stager \
  --image-dir /scout/containers/
```

Submit with workspace inputs — the user's BV-BRC token is passed via the Authorization header:

```bash
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Authorization: $(cat ~/.bvbrc_token)" \
  -d '{
    "workflow_id": "wf_...",
    "inputs": {
      "seq": {"class": "File", "location": "ws:///awilke@bvbrc/home/test.fasta"}
    }
  }'
```

### Mode B: Server-side staging (HPC/isolated compute)

The server pre-stages `ws://` inputs to local files before dispatching, and uploads outputs after completion. Best when workers run on isolated HPC nodes without internet.

```bash
# Server pre/post-stages workspace files
bin/gowe-server --addr :8080 --workspace-staging server --default-executor worker

# Worker (no workspace flags needed — never sees ws:// URIs)
bin/gowe-worker \
  --server http://localhost:8080 \
  --runtime apptainer \
  --image-dir /scout/containers/
```

Submit with an output destination to get results uploaded back to the workspace:

```bash
# Via API
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Authorization: $(cat ~/.bvbrc_token)" \
  -d '{
    "workflow_id": "wf_...",
    "inputs": {
      "seq": {"class": "File", "location": "ws:///awilke@bvbrc/home/test.fasta"}
    },
    "output_destination": "ws:///awilke@bvbrc/home/results/"
  }'

# Via CLI (uses token from ~/.gowe/credentials.json)
bin/gowe submit --workflow protein-structure-prediction -i inputs.yaml \
  --output-destination "ws:///awilke@bvbrc/home/results/"
```

Missing workspace directories are created automatically during output upload.

**Important:** The destination path must match the authenticated user. If your token authenticates as `awilke@bvbrc`, the destination must be under `/awilke@bvbrc/home/...`. Mismatched paths will fail with a permission error.

Check output delivery status:

```bash
curl http://localhost:8080/api/v1/submissions/sub_... | jq '.data | {state, output_state}'
```

**Output states:**
- `""` (empty) — not yet processed
- `"uploading"` — upload in progress
- `"delivered"` — all outputs uploaded successfully
- `"upload_failed"` — upload failed; submission transitions to FAILED with error code `OUTPUT_STAGING_FAILED`
