# GoWe Worker

The GoWe worker is a remote execution agent that polls the GoWe server for tasks and executes them locally. This enables distributed workflow execution across multiple machines.

## Installation

```bash
# From source
go build -o gowe-worker ./cmd/worker

# Or install globally
go install github.com/me/gowe/cmd/worker@latest
```

## Usage

```bash
gowe-worker [flags]
```

### Flags

#### Connection & Identity

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://localhost:8080` | GoWe server URL |
| `--name` | hostname | Worker name for identification |
| `--group` | `default` | Worker group for targeted scheduling |
| `--worker-key` | `""` | Shared secret for authentication |
| `--runtime` | `none` | Container runtime: docker, apptainer, none |
| `--workdir` | `$TMPDIR/gowe-worker` | Local working directory for task execution |
| `--stage-out` | `local` | Output staging mode (local, file://, http://, https://) |
| `--poll` | `5s` | Poll interval for checking new tasks |

#### TLS Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-cert` | (system) | Path to CA certificate PEM file for internal PKI |
| `--insecure` | `false` | Skip TLS verification (testing only) |

#### HTTP Stager

| Flag | Default | Description |
|------|---------|-------------|
| `--http-timeout` | `5m` | HTTP request timeout |
| `--http-retries` | `3` | Number of retry attempts |
| `--http-retry-delay` | `1s` | Initial retry delay (exponential backoff) |
| `--http-credentials` | | Path to credentials JSON file |
| `--http-upload-url` | | URL template for StageOut uploads |
| `--http-upload-method` | `PUT` | HTTP method for uploads (PUT or POST) |

#### Logging

| Flag | Default | Description |
|------|---------|-------------|
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `text` | Log format: text, json |
| `--debug` | `false` | Shorthand for `--log-level=debug` |

## Examples

### Basic worker

```bash
# Connect to local server with default settings
gowe-worker

# Connect to remote server
gowe-worker --server https://gowe.example.com:8080
```

### Named worker

```bash
# Give the worker a descriptive name
gowe-worker --name "compute-node-01"

# Useful for identifying workers in logs and monitoring
gowe-worker --name "gpu-worker-a100"
```

### Worker groups and authentication

Workers can be organized into groups for targeted task scheduling:

```bash
# Join a specific worker group with authentication
gowe-worker \
  --server http://gowe-server:8080 \
  --name "gpu-node-01" \
  --group "gpu-workers" \
  --worker-key "secret-key-1"

# Join the default group
gowe-worker \
  --server http://gowe-server:8080 \
  --worker-key "secret-key-1"
```

The server validates worker keys against its configuration (see server.md). Tasks can target specific groups, and workers only receive tasks matching their group.

### Container runtime

```bash
# Use Docker for containerized tasks
gowe-worker --runtime docker

# Use Apptainer (Singularity) for HPC environments
gowe-worker --runtime apptainer

# No containers - run directly on host
gowe-worker --runtime none
```

### Custom work directory

```bash
# Use a specific directory for task execution
gowe-worker --workdir /scratch/gowe

# Use fast local storage
gowe-worker --workdir /nvme/gowe-work
```

### Polling configuration

```bash
# Poll every 10 seconds (reduce server load)
gowe-worker --poll 10s

# Poll every second (faster response, higher load)
gowe-worker --poll 1s
```

### Production configuration

```bash
# Full production setup
gowe-worker \
  --server https://gowe.example.com:8080 \
  --name "worker-$(hostname)" \
  --runtime docker \
  --workdir /var/lib/gowe/work \
  --poll 5s \
  --log-format json \
  --log-level info
```

### HTTP staging with custom CA

```bash
# Internal infrastructure with private CA
gowe-worker \
  --server https://internal-gowe:8080 \
  --ca-cert /etc/pki/internal-ca.pem \
  --http-credentials /etc/gowe/creds.json \
  --http-upload-url "https://minio.internal:9000/outputs/{taskID}/{filename}" \
  --runtime docker
```

## Architecture

### Registration

When the worker starts, it registers with the server:

1. Sends registration request with name, hostname, group, and capabilities
2. Server validates worker key against allowed groups
3. Receives a worker ID from the server
4. Begins polling for tasks

If `--worker-key` is provided, the worker sends it in the `X-Worker-Key` header.

### Task Lifecycle

```
┌─────────┐     ┌─────────┐     ┌─────────┐
│ Server  │────>│ Worker  │────>│ Runtime │
│ (poll)  │<────│ (claim) │<────│ (exec)  │
└─────────┘     └─────────┘     └─────────┘
     │               │               │
     │   1. Poll     │               │
     │<──────────────│               │
     │               │               │
     │   2. Task     │               │
     │──────────────>│               │
     │               │   3. Execute  │
     │               │──────────────>│
     │               │               │
     │               │   4. Result   │
     │               │<──────────────│
     │   5. Report   │               │
     │<──────────────│               │
     └───────────────┴───────────────┘
```

### Per-Task Credentials

Tasks include the submitting user's token in `RuntimeHints.StagerOverrides.HTTPCredential`. This enables:

- Downloading inputs from authenticated data services (BV-BRC Workspace, MG-RAST, etc.)
- Uploading outputs using the user's identity
- Running external jobs under the user's account

The worker automatically applies these credentials during file staging.

### Working Directory Structure

```
$WORKDIR/
├── task_abc123/
│   ├── work/           # Execution directory
│   ├── inputs/         # Staged input files
│   ├── outputs/        # Output files
│   ├── stdout.txt      # Captured stdout
│   └── stderr.txt      # Captured stderr
└── task_def456/
    └── ...
```

## Runtime Modes

### none (default)

Executes commands directly on the host system. Suitable for:
- Trusted workflows
- Development and testing
- Environments without containers

```bash
gowe-worker --runtime none
```

### docker

Executes commands inside Docker containers. Requires:
- Docker daemon running
- User in docker group (or root)

```bash
gowe-worker --runtime docker
```

CWL workflows specify container images via `DockerRequirement`:

```yaml
hints:
  DockerRequirement:
    dockerPull: ubuntu:22.04
```

### apptainer

Executes commands inside Apptainer (Singularity) containers. Suitable for:
- HPC clusters
- Rootless container execution
- Shared multi-user systems

```bash
gowe-worker --runtime apptainer
```

## Output Staging

### local (default)

Outputs remain in the worker's work directory. The server can retrieve them via API.

```bash
gowe-worker --stage-out local
```

### file:// path

Copy outputs to a shared filesystem location:

```bash
gowe-worker --stage-out file:///shared/results
```

This is useful when:
- Workers share a network filesystem (NFS, Lustre, etc.)
- Results need to be accessible from multiple nodes

### http:// / https:// (HTTP Stager)

Stage inputs from HTTP/HTTPS URLs and upload outputs to an HTTP endpoint:

```bash
gowe-worker \
  --stage-out local \
  --http-upload-url "https://data.example.com/outputs/{taskID}/{filename}" \
  --http-credentials /etc/gowe/credentials.json
```

This enables:
- Downloading input files from HTTP/HTTPS URLs
- Uploading outputs to object storage or data servers
- Custom authentication per host

#### URL Templates

The `--http-upload-url` supports these placeholders:
- `{taskID}` - The task ID
- `{filename}` - Full filename with extension
- `{basename}` - Filename without extension

Example: `https://s3.example.com/bucket/{taskID}/{filename}`

#### Credentials File

Create a JSON file with per-host credentials:

```json
{
  "data.example.com": {
    "type": "bearer",
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
  },
  "*.internal.org": {
    "type": "basic",
    "username": "service-account",
    "password": "secret"
  },
  "upload.example.com": {
    "type": "header",
    "header_name": "X-API-Key",
    "header_value": "abc123"
  }
}
```

Supported authentication types:
- `bearer` - Authorization: Bearer {token}
- `basic` - HTTP Basic authentication
- `header` - Custom header name/value

Wildcard matching (e.g., `*.example.com`) is supported for host patterns.

#### Custom CA Certificate

For internal PKI with self-signed certificates:

```bash
gowe-worker \
  --server https://internal-server:8080 \
  --ca-cert /etc/gowe/internal-ca.pem \
  --http-upload-url "https://internal-data:9000/outputs/{taskID}/{filename}"
```

The CA certificate applies to both:
- Worker ↔ Server API communication
- HTTP stager HTTPS connections

## Tutorial: Setting Up a Worker Cluster

### 1. Start the server

On your head node:

```bash
gowe-server --addr 0.0.0.0:8080
```

### 2. Deploy workers

On each compute node:

```bash
# Install the worker binary
scp gowe-worker compute-01:/usr/local/bin/

# SSH to each node and start workers
ssh compute-01 'gowe-worker --server http://head-node:8080 --name compute-01 --runtime docker &'
ssh compute-02 'gowe-worker --server http://head-node:8080 --name compute-02 --runtime docker &'
ssh compute-03 'gowe-worker --server http://head-node:8080 --name compute-03 --runtime docker &'
```

### 3. Verify registration

Check the server logs or API:

```bash
curl http://head-node:8080/api/v1/workers/
```

### 4. Submit a workflow

Workflows with `goweHint.executor: worker` will be dispatched to registered workers:

```yaml
steps:
  process:
    run: tools/heavy-computation.cwl
    hints:
      goweHint:
        executor: worker
    in:
      data: input_data
    out: [result]
```

### 5. Monitor execution

```bash
# From any machine with access to the server
gowe status sub_abc123
gowe logs sub_abc123
```

## Systemd Service

Create `/etc/systemd/system/gowe-worker.service`:

```ini
[Unit]
Description=GoWe Worker
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=gowe
Group=gowe
ExecStart=/usr/local/bin/gowe-worker \
  --server https://gowe-server:8080 \
  --name %H \
  --runtime docker \
  --workdir /var/lib/gowe/work \
  --ca-cert /etc/gowe/ca.pem \
  --http-credentials /etc/gowe/credentials.json \
  --http-upload-url https://storage:9000/outputs/%H/{taskID}/{filename} \
  --log-format json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable gowe-worker
sudo systemctl start gowe-worker
```

## Troubleshooting

### Worker not receiving tasks

1. Check registration:
```bash
curl http://server:8080/api/v1/workers/
```

2. Verify connectivity:
```bash
curl http://server:8080/api/v1/health
```

3. Check worker logs:
```bash
gowe-worker --debug
```

### Docker permission denied

```
Error: Got permission denied while trying to connect to the Docker daemon
```

Add user to docker group:
```bash
sudo usermod -aG docker $USER
# Log out and back in
```

### Apptainer image not found

```
Error: unable to pull image
```

Pre-pull images or configure a local registry:
```bash
apptainer pull docker://ubuntu:22.04
```

### Work directory full

Clean up old task directories:
```bash
# Remove tasks older than 7 days
find /var/lib/gowe/work -maxdepth 1 -type d -mtime +7 -exec rm -rf {} \;
```

Or configure automatic cleanup via cron:
```cron
0 2 * * * find /var/lib/gowe/work -maxdepth 1 -type d -mtime +7 -exec rm -rf {} \;
```
