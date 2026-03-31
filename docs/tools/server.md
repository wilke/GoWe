# GoWe Server

The GoWe server is the main API server that manages workflows, submissions, and task execution. It provides a REST API, runs the scheduler loop, and coordinates multiple executor backends.

## Installation

```bash
# From source
go build -o gowe-server ./cmd/server

# Or install globally
go install github.com/me/gowe/cmd/server@latest
```

## Usage

```bash
gowe-server [flags]
```

### Flags

#### Core

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8080` | Listen address (host:port) |
| `--db` | `~/.gowe/gowe.db` | SQLite database path |
| `--default-executor` | `""` | Default executor type: `local`, `container`, `worker`, `bvbrc` (empty = hint-based) |
| `--image-dir` | `""` | Base directory for resolving relative `.sif` image paths in `DockerRequirement` |
| `--config` | `""` | Path to server config file (for admins, worker keys) |
| `--scheduler-poll` | `2s` | Scheduler poll interval |
| `--workspace-staging` | `""` | Workspace staging mode: `server` (pre/post-stage `ws://` on server) or empty (passthrough to workers) |
| `--workspace-url` | `""` | BV-BRC Workspace service URL for server-side staging (default: production) |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `text` | Log format: text, json |
| `--debug` | `false` | Shorthand for `--log-level=debug` |

#### Authentication

| Flag | Default | Description |
|------|---------|-------------|
| `--allow-anonymous` | `false` | Allow unauthenticated API requests |
| `--anonymous-executors` | `local,container,worker` | Executors allowed for anonymous users |
| `--admins` | `""` | Comma-separated list of admin usernames (also: `GOWE_ADMINS` env) |
| `--worker-keys` | `""` | Path to worker keys JSON file |

Environment variables:
- `GOWE_ADMINS` — Comma-separated list of admin usernames (e.g., `alice@bvbrc,bob@mgrast`)

#### File Upload Proxy

| Flag | Default | Description |
|------|---------|-------------|
| `--upload-backend` | `""` | Enable file upload proxy: `shock`, `s3`, `local` |
| `--upload-max-size` | `1073741824` (1 GB) | Maximum upload size in bytes |
| `--upload-local-dir` | `""` | Local directory for file uploads |
| `--upload-download-dirs` | `""` | Comma-separated directories allowed for file download |

**S3 backend flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--upload-s3-endpoint` | `""` | S3 endpoint (empty = AWS) |
| `--upload-s3-region` | `us-east-1` | S3 region |
| `--upload-s3-bucket` | `""` | S3 bucket |
| `--upload-s3-prefix` | `uploads/` | S3 key prefix |
| `--upload-s3-access-key` | `""` | S3 access key (env: `AWS_ACCESS_KEY_ID`) |
| `--upload-s3-secret-key` | `""` | S3 secret key (env: `AWS_SECRET_ACCESS_KEY`) |
| `--upload-s3-path-style` | `false` | Use path-style S3 addressing (for MinIO) |
| `--upload-s3-disable-ssl` | `false` | Disable SSL for S3 |

**Shock backend flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--upload-shock-host` | `""` | Shock server host (e.g., `localhost:7445`) |
| `--upload-shock-http` | `false` | Use HTTP instead of HTTPS |
| `--upload-shock-token` | `""` | Shock authentication token |

The upload proxy enables workers to upload output files through the server. Workers POST files to `/api/v1/upload`, and the server forwards them to the configured backend.

**Example: local upload backend for docker-compose:**

```bash
gowe-server \
  --default-executor worker \
  --upload-backend local \
  --upload-local-dir /workdir/uploads \
  --upload-download-dirs /workdir/uploads,/workdir/outputs
```

#### Container Images

| Flag | Default | Description |
|------|---------|-------------|
| `--image-dir` | `""` | Base directory for resolving relative `.sif` image paths |

When `--image-dir` is set, the local and Apptainer executors resolve relative `.sif` paths in `DockerRequirement.dockerPull` against this directory. This is required for HPC deployments where pre-built SIF images are stored in a shared location.

```bash
# Resolve tool.sif → /scout/containers/tool.sif
gowe-server --image-dir /scout/containers/ --default-executor worker
```

## Examples

### Basic startup

```bash
# Start with defaults (port 8080, database in ~/.gowe/)
gowe-server
```

### Custom configuration

```bash
# Listen on a different port with debug logging
gowe-server --addr :9090 --debug

# Use a specific database file
gowe-server --db /var/lib/gowe/production.db

# JSON logging for production
gowe-server --log-format json --log-level info

# Force all tasks to use the worker executor (distributed mode)
gowe-server --default-executor worker --debug
```

### Multi-provider authentication

GoWe supports authentication via BV-BRC and MG-RAST tokens:

```bash
# Submit with BV-BRC token
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Authorization: $(cat ~/.bvbrc_token)" \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "wf_xxx", "inputs": {}}'

# Submit with MG-RAST token
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "X-MG-RAST-Token: $(cat ~/.mgrast_token)" \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "wf_xxx", "inputs": {}}'

# Anonymous submission (requires --allow-anonymous)
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "wf_xxx", "inputs": {}}'
```

User tokens are stored with submissions and delegated per-task to executors, enabling jobs to run under the submitting user's identity.

### Anonymous access

Enable anonymous access for local/demo use:

```bash
# Allow unauthenticated requests (limited to local, docker, worker executors)
gowe-server --allow-anonymous

# Restrict anonymous to specific executors
gowe-server --allow-anonymous --anonymous-executors local,docker
```

Anonymous users cannot submit jobs to BV-BRC or MG-RAST executors (require credentials).

### Distributed execution mode

When `--default-executor=worker` is set, all tasks are dispatched to registered worker nodes instead of being executed locally:

```bash
# Start server in distributed mode
gowe-server --default-executor worker --addr 0.0.0.0:8080

# Workers connect and register (see docs/tools/worker.md)
# Tasks are distributed across available workers
```

This is useful for:
- Scaling execution across multiple machines
- Isolating task execution from the API server
- Running tasks on specialized hardware (GPU nodes, high-memory nodes, etc.)

### Running in Docker

```bash
# Build the image
docker build -t gowe .

# Run with persistent storage
docker run -p 8080:8080 -v ~/.gowe:/root/.gowe gowe
```

## Architecture

The server initializes several components on startup:

### Database

SQLite database with automatic migrations. Default location: `~/.gowe/gowe.db`

The directory is created automatically if it doesn't exist.

### Executor Registry

The server registers multiple executor backends:

| Executor | Trigger | Description |
|----------|---------|-------------|
| `local` | Default | Runs commands as OS processes |
| `docker` | `DockerRequirement` hint | Runs in Docker containers |
| `worker` | `gowe:Execution.executor: worker` | Delegates to remote workers |
| `bvbrc` | `gowe:Execution.executor: bvbrc` | Submits to BV-BRC cloud |

### BV-BRC Integration

If a valid BV-BRC token is found, the server:
- Registers the `bvbrc` executor
- Enables `/api/v1/apps` endpoints for app discovery
- Enables `/api/v1/workspace` endpoints for file browsing

Token sources (checked in order):
1. `BVBRC_TOKEN` environment variable
2. `~/.gowe/credentials.json`
3. `~/.bvbrc_token`
4. `~/.patric_token`
5. `~/.p3_token`

### Worker Authentication

Workers authenticate using shared secrets and can belong to groups:

```json
// ~/.gowe/worker-keys.json
{
  "keys": {
    "secret-key-1": {
      "groups": ["default", "gpu-workers"],
      "description": "Production GPU cluster"
    },
    "secret-key-2": {
      "groups": ["cpu-workers"],
      "description": "CPU-only workers"
    }
  }
}
```

```bash
gowe-server --worker-keys ~/.gowe/worker-keys.json
```

Workers send `X-Worker-Key` header on registration. Tasks can target specific groups via `RuntimeHints.WorkerGroup`.

### Scheduler

A tick-based scheduler loop that:
- Polls for pending tasks
- Resolves dependencies
- Checks token expiry before dispatch
- Dispatches tasks to appropriate executors (with user credentials)
- Handles state transitions and retries

## API Reference

All endpoints are prefixed with `/api/v1`. All responses use a standard JSON envelope:

```json
{
  "status": "ok",
  "request_id": "req_abc123",
  "timestamp": "2026-03-28T12:34:56Z",
  "data": { ... },
  "pagination": { ... }
}
```

Error responses:

```json
{
  "status": "error",
  "request_id": "req_abc123",
  "timestamp": "2026-03-28T12:34:56Z",
  "error": {
    "code": "NOT_FOUND",
    "message": "Workflow not found"
  }
}
```

### Authentication

Protected endpoints require one of:
- `Authorization` header (BV-BRC token): `un=username|tokenid=...|expiry=...`
- `X-MG-RAST-Token` header (MG-RAST token)
- Anonymous access (if `--allow-anonymous` is set)

Worker endpoints use `X-Worker-Key` header (if worker keys configured).

### Error Codes

| Code | HTTP Status | Meaning |
|------|------------|---------|
| `ErrValidation` | 400 | Invalid request body or query parameters |
| `ErrUnauthorized` | 401 | No auth token or invalid token |
| `ErrForbidden` | 403 | Authenticated but insufficient permissions |
| `ErrNotFound` | 404 | Resource not found |
| `ErrConflict` | 409 | Invalid state transition or resource conflict |
| `ErrInternal` | 500 | Server error |

---

### Health & Discovery

#### `GET /api/v1/health`

Server health and version. No auth required.

```bash
curl http://localhost:8080/api/v1/health
```

```json
{
  "status": "healthy",
  "version": "0.1.0",
  "go_version": "go1.24",
  "uptime": "2h15m30s",
  "executors": {
    "local": "available",
    "worker": "available",
    "bvbrc": "unavailable"
  }
}
```

#### `GET /api/v1/`

API endpoint discovery. No auth required.

---

### Workflows

#### `POST /api/v1/workflows`

Create a workflow from CWL. If CWL content matches an existing workflow, returns the existing one (deduplication by content hash).

```bash
curl -X POST http://localhost:8080/api/v1/workflows \
  -H "Authorization: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-workflow",
    "description": "optional description",
    "cwl": "cwlVersion: v1.2\nclass: CommandLineTool\n..."
  }'
```

Response (201):

```json
{
  "id": "wf_abc123",
  "name": "my-workflow",
  "class": "Workflow",
  "cwl_version": "v1.2",
  "step_count": 3,
  "content_hash": "sha256...",
  "created_at": "2026-03-28T12:00:00Z"
}
```

#### `GET /api/v1/workflows`

List workflows (paginated).

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | 20 | Max results (max: 100) |
| `offset` | 0 | Skip N results |

```bash
curl http://localhost:8080/api/v1/workflows?limit=10
```

#### `GET /api/v1/workflows/{id}`

Get workflow details (accepts ID or name). Returns full CWL, steps, inputs, outputs.

```bash
curl http://localhost:8080/api/v1/workflows/wf_abc123
```

#### `PUT /api/v1/workflows/{id}`

Update workflow metadata or CWL content.

```bash
curl -X PUT http://localhost:8080/api/v1/workflows/wf_abc123 \
  -H "Content-Type: application/json" \
  -d '{"name": "new-name", "cwl": "cwlVersion: v1.2\n..."}'
```

If `cwl` is provided, the workflow is re-parsed and re-validated.

#### `DELETE /api/v1/workflows/{id}`

Delete a workflow.

```bash
curl -X DELETE http://localhost:8080/api/v1/workflows/wf_abc123
```

#### `POST /api/v1/workflows/{id}/validate`

Validate a workflow without creating a submission.

```bash
curl -X POST http://localhost:8080/api/v1/workflows/wf_abc123/validate
```

```json
{
  "valid": true,
  "errors": [],
  "warnings": []
}
```

---

### Submissions

#### `POST /api/v1/submissions`

Submit a workflow for execution. Auth required (user token stored for per-task credential delegation).

```bash
curl -X POST http://localhost:8080/api/v1/submissions \
  -H "Authorization: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "wf_abc123",
    "inputs": {
      "sequences": {"class": "File", "path": "test.fasta"},
      "num_recycles": 3
    },
    "labels": {"experiment": "pilot-1"}
  }'
```

Response (201):

```json
{
  "id": "sub_xyz789",
  "workflow_id": "wf_abc123",
  "workflow_name": "my-workflow",
  "state": "pending",
  "inputs": { ... },
  "outputs": {},
  "labels": { ... },
  "submitted_by": "user@example.com",
  "created_at": "2026-03-28T12:00:00Z",
  "tasks": [ ... ]
}
```

**Dry run** — validate inputs without executing:

```bash
curl -X POST 'http://localhost:8080/api/v1/submissions?dry_run=true' \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "wf_abc123", "inputs": { ... }}'
```

```json
{
  "dry_run": true,
  "valid": true,
  "workflow": {"id": "wf_abc123", "name": "my-workflow", "step_count": 3},
  "dag_acyclic": true,
  "execution_order": ["step1", "step2", "step3"],
  "executor_availability": {
    "local": "available",
    "worker": "available"
  }
}
```

**File literals** — files with `contents` but no `path` are auto-materialized:

```json
{
  "inputs": {
    "config": {"class": "File", "basename": "params.yaml", "contents": "key: value\n"}
  }
}
```

#### `GET /api/v1/submissions`

List submissions (paginated).

| Parameter | Default | Description |
|-----------|---------|-------------|
| `limit` | 20 | Max results (max: 100) |
| `offset` | 0 | Skip N results |
| `state` | | Filter: pending, running, completed, failed, cancelled |
| `workflow_id` | | Filter by workflow (ID or name) |

```bash
curl 'http://localhost:8080/api/v1/submissions?state=running&limit=5'
```

#### `GET /api/v1/submissions/{id}`

Get submission details including all tasks and step instances.

```bash
curl http://localhost:8080/api/v1/submissions/sub_xyz789
```

#### `PUT /api/v1/submissions/{id}/cancel`

Cancel a running submission. Marks all non-terminal tasks as SKIPPED.

```bash
curl -X PUT http://localhost:8080/api/v1/submissions/sub_xyz789/cancel
```

```json
{
  "id": "sub_xyz789",
  "state": "cancelled",
  "steps_cancelled": 2,
  "tasks_cancelled": 5,
  "tasks_already_completed": 3
}
```

---

### Tasks

#### `GET /api/v1/submissions/{sid}/tasks`

List tasks in a submission.

```bash
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks
```

#### `GET /api/v1/submissions/{sid}/tasks/{tid}`

Get task details (state, executor type, runtime hints, inputs, outputs).

```bash
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_123
```

#### `GET /api/v1/submissions/{sid}/tasks/{tid}/logs`

Get task stdout, stderr, and exit code.

```bash
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_123/logs
```

```json
{
  "task_id": "task_123",
  "step_id": "step_1",
  "stdout": "Processing...\nDone.",
  "stderr": "",
  "exit_code": 0
}
```

---

### Workers

Worker endpoints use `X-Worker-Key` auth (if configured).

#### `POST /api/v1/workers`

Register a worker.

```bash
curl -X POST http://localhost:8080/api/v1/workers \
  -H "X-Worker-Key: secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "gpu-worker-01",
    "hostname": "compute-01.example.com",
    "group": "esmfold",
    "runtime": "apptainer",
    "version": "abc1234",
    "gpu_enabled": true,
    "gpu_device": "cuda:0",
    "datasets": {
      "boltz": "/local_databases/boltz",
      "alphafold": "/local_databases/alphafold"
    }
  }'
```

Response (201):

```json
{
  "id": "wrk_abc123",
  "name": "gpu-worker-01",
  "hostname": "compute-01.example.com",
  "group": "esmfold",
  "state": "online",
  "runtime": "apptainer",
  "version": "abc1234",
  "gpu_enabled": true,
  "gpu_device": "cuda:0",
  "datasets": { ... },
  "last_seen": "2026-03-28T12:34:56Z",
  "registered_at": "2026-03-28T12:34:56Z"
}
```

#### `GET /api/v1/workers`

List all registered workers with state, group, runtime, GPU, and dataset info.

```bash
curl http://localhost:8080/api/v1/workers
```

#### `PUT /api/v1/workers/{id}/heartbeat`

Worker keep-alive. Updates `last_seen`. Workers are marked offline after 30s without heartbeat.

```bash
curl -X PUT http://localhost:8080/api/v1/workers/wrk_abc123/heartbeat \
  -H "X-Worker-Key: secret-key-1"
```

#### `GET /api/v1/workers/{id}/work`

Poll for a task. Returns 204 if no work available. Task matching considers: runtime capability, worker group, and dataset affinity (prestage=require, cache=prefer).

```bash
curl http://localhost:8080/api/v1/workers/wrk_abc123/work \
  -H "X-Worker-Key: secret-key-1"
```

Response (200): Full task object with CWL tool, inputs, runtime hints.

Response (204): No content — no work available.

#### `PUT /api/v1/workers/{id}/tasks/{tid}/status`

Report task progress.

```bash
curl -X PUT http://localhost:8080/api/v1/workers/wrk_abc123/tasks/task_456/status \
  -H "X-Worker-Key: secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{"state": "running"}'
```

#### `PUT /api/v1/workers/{id}/tasks/{tid}/complete`

Report task completion with results.

```bash
curl -X PUT http://localhost:8080/api/v1/workers/wrk_abc123/tasks/task_456/complete \
  -H "X-Worker-Key: secret-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "state": "success",
    "exit_code": 0,
    "stdout": "Prediction complete.",
    "stderr": "",
    "outputs": {
      "predictions": {
        "class": "Directory",
        "location": "file:///scratch/gowe/task_456/output"
      }
    }
  }'
```

#### `DELETE /api/v1/workers/{id}`

Deregister a worker.

```bash
curl -X DELETE http://localhost:8080/api/v1/workers/wrk_abc123 \
  -H "X-Worker-Key: secret-key-1"
```

---

### File Upload & Download

Requires `--upload-backend` to be configured. See [File Upload Proxy](#file-upload-proxy) flags.

#### `POST /api/v1/files`

Upload a file. Forwarded to configured backend (local, shock, s3).

```bash
curl -X POST http://localhost:8080/api/v1/files \
  -H "Authorization: $TOKEN" \
  -F "file=@input.fasta"
```

Response (201):

```json
{
  "location": "file:///workdir/uploads/input.fasta",
  "filename": "input.fasta",
  "size": 12345
}
```

The returned `location` URI can be used directly in submission inputs.

#### `GET /api/v1/files/download`

Download a file or list a directory. Only paths under `--upload-download-dirs` are allowed.

```bash
# Download a file
curl 'http://localhost:8080/api/v1/files/download?location=file:///workdir/uploads/input.fasta' \
  -H "Authorization: $TOKEN" -o input.fasta

# List a directory
curl 'http://localhost:8080/api/v1/files/download?location=file:///workdir/uploads/' \
  -H "Authorization: $TOKEN"
```

Directory listing response:

```json
[
  {"basename": "input.fasta", "location": "file:///workdir/uploads/input.fasta", "is_dir": false, "size": 12345},
  {"basename": "results/", "location": "file:///workdir/uploads/results", "is_dir": true, "size": 0}
]
```

---

### BV-BRC Integration

Requires a valid BV-BRC token (server-side or user-provided).

#### `GET /api/v1/apps`

List BV-BRC applications (cached 5 minutes).

```bash
curl http://localhost:8080/api/v1/apps -H "Authorization: $TOKEN"
```

#### `GET /api/v1/apps/{appID}`

Get BV-BRC app details.

```bash
curl http://localhost:8080/api/v1/apps/GenomeAssembly2 -H "Authorization: $TOKEN"
```

#### `GET /api/v1/apps/{appID}/cwl-tool`

Auto-generate a CWL CommandLineTool wrapper from the BV-BRC app schema.

```bash
curl http://localhost:8080/api/v1/apps/GenomeAssembly2/cwl-tool -H "Authorization: $TOKEN"
```

```json
{
  "app_id": "GenomeAssembly2",
  "cwl_tool": "cwlVersion: v1.2\nclass: CommandLineTool\nhints:\n  gowe:Execution:\n    executor: bvbrc\n    bvbrc_app_id: GenomeAssembly2\n..."
}
```

#### `GET /api/v1/workspace`

Browse BV-BRC workspace.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `path` | user home | Workspace path to list |

```bash
curl 'http://localhost:8080/api/v1/workspace?path=/awilke@bvbrc/home/' \
  -H "Authorization: $TOKEN"
```

---

### Server-Sent Events (SSE)

#### `GET /api/v1/sse/submissions/{id}`

Stream real-time submission updates. Connection auto-closes when submission reaches terminal state.

```bash
curl -N http://localhost:8080/api/v1/sse/submissions/sub_xyz789 \
  -H "Authorization: $TOKEN"
```

Events:

| Event | When | Data |
|-------|------|------|
| `init` | Immediately on connect | Full submission state |
| `update` | State changes | Updated submission state |
| `complete` | Terminal state reached | Final submission state |
| (heartbeat) | Every 2s when idle | Comment line (`: heartbeat`) |

```
event: init
data: {"id":"sub_xyz789","state":"pending",...}

event: update
data: {"id":"sub_xyz789","state":"running",...}

: heartbeat

event: complete
data: {"id":"sub_xyz789","state":"completed","outputs":{...}}
```

---

### Admin

Requires admin role (configured via `--admins`, `GOWE_ADMINS`, or config file).

#### `GET /api/v1/admin/users`

List all users.

```bash
curl http://localhost:8080/api/v1/admin/users -H "Authorization: $ADMIN_TOKEN"
```

```json
[
  {"username": "awilke@bvbrc", "provider": "bvbrc", "role": "admin", "created_at": "..."},
  {"username": "user@bvbrc", "provider": "bvbrc", "role": "user", "created_at": "..."}
]
```

#### `PUT /api/v1/admin/users/{username}/role`

Update a user's role.

```bash
curl -X PUT http://localhost:8080/api/v1/admin/users/user@bvbrc/role \
  -H "Authorization: $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"role": "admin"}'
```

## Graceful Shutdown

The server handles `SIGINT` and `SIGTERM` signals:

1. Stops accepting new requests
2. Waits for the scheduler to complete current tick
3. Drains in-flight HTTP requests (5 second timeout)
4. Closes database connection

## Tutorial: Running Your First Workflow

### 1. Start the server

```bash
gowe-server --debug
```

### 2. Create a simple workflow

Create `hello.cwl`:

```yaml
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
    inputBinding:
      position: 1
outputs:
  output:
    type: stdout
stdout: output.txt
```

### 3. Register the workflow

```bash
curl -X POST http://localhost:8080/api/v1/workflows/ \
  -H "Content-Type: application/json" \
  -d '{
    "name": "hello-world",
    "cwl": "cwlVersion: v1.2\nclass: CommandLineTool\nbaseCommand: echo\ninputs:\n  message:\n    type: string\n    inputBinding:\n      position: 1\noutputs:\n  output:\n    type: stdout\nstdout: output.txt"
  }'
```

Response:

```json
{
  "status": "success",
  "data": {
    "id": "wf_abc123",
    "name": "hello-world",
    "step_count": 1
  }
}
```

### 4. Submit a run

```bash
curl -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{
    "workflow_id": "wf_abc123",
    "inputs": {"message": "Hello, GoWe!"}
  }'
```

### 5. Monitor progress

```bash
# Check submission status
curl http://localhost:8080/api/v1/submissions/sub_xyz789/

# View task logs when complete
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_001/logs
```

## Troubleshooting

### Server won't start

```
open database: unable to open database file
```

Ensure the parent directory exists and is writable:

```bash
mkdir -p ~/.gowe
chmod 755 ~/.gowe
```

### BV-BRC executor not registered

```
bvbrc executor not registered (no token)
```

Set your BV-BRC token:

```bash
export BVBRC_TOKEN="un=user@patricbrc.org|tokenid=...|expiry=...|sig=..."
gowe-server
```

Or use the CLI to save it:

```bash
gowe login --token "..."
```

### Port already in use

```
listen tcp :8080: bind: address already in use
```

Use a different port:

```bash
gowe-server --addr :9090
```
