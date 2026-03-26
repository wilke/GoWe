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
| `--config` | `~/.gowe/config.yaml` | Server configuration file |
| `--scheduler-poll` | `2s` | Scheduler poll interval |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `text` | Log format: text, json |
| `--debug` | `false` | Shorthand for `--log-level=debug` |

#### Authentication

| Flag | Default | Description |
|------|---------|-------------|
| `--allow-anonymous` | `false` | Allow unauthenticated API requests |
| `--anonymous-executors` | `local,container,worker` | Executors allowed for anonymous users |
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

## API Endpoints

All endpoints are prefixed with `/api/v1`.

### Health & Discovery

```bash
# Health check
curl http://localhost:8080/api/v1/health

# API discovery
curl http://localhost:8080/api/v1/
```

### Workflows

```bash
# List workflows
curl http://localhost:8080/api/v1/workflows/

# Create workflow
curl -X POST http://localhost:8080/api/v1/workflows/ \
  -H "Content-Type: application/json" \
  -d '{"name": "my-workflow", "cwl": "..."}'

# Get workflow
curl http://localhost:8080/api/v1/workflows/wf_abc123/

# Validate workflow
curl -X POST http://localhost:8080/api/v1/workflows/wf_abc123/validate

# Delete workflow
curl -X DELETE http://localhost:8080/api/v1/workflows/wf_abc123/
```

### Submissions

```bash
# List submissions
curl http://localhost:8080/api/v1/submissions/

# Create submission
curl -X POST http://localhost:8080/api/v1/submissions/ \
  -H "Content-Type: application/json" \
  -d '{"workflow_id": "wf_abc123", "inputs": {"param1": "value1"}}'

# Get submission status
curl http://localhost:8080/api/v1/submissions/sub_xyz789/

# Cancel submission
curl -X PUT http://localhost:8080/api/v1/submissions/sub_xyz789/cancel
```

### Tasks

```bash
# List tasks for a submission
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/

# Get task details
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_123/

# Get task logs
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_123/logs
```

### BV-BRC Proxy (requires token)

```bash
# List BV-BRC apps
curl http://localhost:8080/api/v1/apps/

# Get app details
curl http://localhost:8080/api/v1/apps/GenomeAnnotation

# Browse workspace
curl http://localhost:8080/api/v1/workspace?path=/user@patricbrc.org/home/
```

## Response Format

All API responses use a standard envelope:

```json
{
  "status": "success",
  "request_id": "req_abc123",
  "timestamp": "2024-01-15T10:30:00Z",
  "data": { ... }
}
```

Error responses:

```json
{
  "status": "error",
  "request_id": "req_abc123",
  "timestamp": "2024-01-15T10:30:00Z",
  "error": {
    "code": "NOT_FOUND",
    "message": "Workflow not found"
  }
}
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
