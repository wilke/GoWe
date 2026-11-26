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
| `--db` | `~/.gowe/gowe.db` | SQLite database path (`:memory:` for testing), or a `postgres://` / `postgresql://` URL without a password to use PostgreSQL (see [Database](#database)) |
| `--default-executor` | `""` | Default executor type: `local`, `container`, `worker`, `bvbrc` (empty = hint-based) |
| `--image-dir` | `""` | Base directory for resolving relative `.sif` image paths in `DockerRequirement` |
| `--config` | `""` | Path to the server config file (JSON): `admins`, the PostgreSQL `database` block (see [Database](#database)) and the `worker_groups` access policy (see [Worker Group Access](#worker-group-access)); any other top-level key is a start-up error |
| `--scheduler-poll` | `2s` | Scheduler poll interval |
| `--checkout-hold-after` | `10m` | After a queued task has waited this long for capacity on a worker that runs several tasks, that worker stops taking later tasks that need the same capacity (`0` disables; see [Capacity accounting at checkout](#capacity-accounting-at-checkout)) |
| `--workspace-staging` | `""` | Workspace staging mode: `server` (pre/post-stage `ws://` on server) or empty (passthrough to workers) |
| `--workspace-url` | `""` | BV-BRC Workspace service URL for server-side staging and the web UI workspace browser/uploads, which always run under the logged-in user's own token (default: production) |
| `--redeliver-source-dirs` | `""` | Comma-separated directories the admin re-delivery endpoint may read staged originals from (typically the shared stage-out directory; symlinks resolved). Empty refuses local re-upload |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `text` | Log format: text, json |
| `--debug` | `false` | Shorthand for `--log-level=debug` |

#### Metrics

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics-addr` | `""` | Listen address for a second, unauthenticated HTTP server exposing only `/metrics` (Prometheus text format). Empty disables metrics entirely — no registry is even constructed |
| `--metrics-workflow-label` | `true` | Include the (unbounded, user-authored) workflow name as a Prometheus label; `false` maps every observation to `workflow="_all"` instead |
| `--metrics-label-cap` | `200` | Per-label distinct-value cap for the user-authored `workflow`/`step` labels; values beyond the cap collapse into `_other` |

The metrics listener is deliberately separate from the main API/UI server: no auth middleware, no request logging, no routes other than `/metrics`. Bind it to `localhost` or a private interface — see [PRODUCTION.md](../PRODUCTION.md) for scrape and bind guidance.

```bash
# Enable Prometheus metrics on localhost:9090/metrics
gowe-server --metrics-addr localhost:9090
```

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
| `--upload-max-size` | `1073741824` (1 GB) | Maximum upload size in bytes, for the file proxy and for web UI workspace uploads (oversized bodies get 413) |
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

#### PostgreSQL

The server can keep its state in PostgreSQL 16 or newer instead (see
[ADR-0014](../adr/0014-postgresql-second-store-backend.md)). SQLite stays the default. The tables are the same,
and the server still uses a single database connection, so only one server process may use a database.

PostgreSQL is selected by a `postgres://` or `postgresql://` URL, either in `--db` or in `database.url` of the
`--config` file. A `--db` value given on the command line wins over `database.url`. Any other `--db` value is a
SQLite path.

The password goes in the `--config` file, never on the command line or in the environment (both can be read by
other processes of the same user through `/proc`):

```json
{
  "admins": ["alice"],
  "database": {
    "url": "postgres://gowe@127.0.0.1:5433/gowe",
    "password": "...",
    "schema": "gowe"
  }
}
```

```bash
chmod 600 /etc/gowe/server.json
gowe-server --config /etc/gowe/server.json
# or, with the URL on the command line (password still from the file):
gowe-server --db postgres://gowe@127.0.0.1:5433/gowe --config /etc/gowe/server.json
```

| Field | Description |
|-------|-------------|
| `url` | `postgres://` or `postgresql://` URL, without a password. Any other value is an error (SQLite is configured with `--db`) |
| `password` | The database password. With a non-empty password the file must be a regular file with mode `0600` or stricter |
| `schema` | Optional schema to work in (sets `search_path`). Empty: the role's default schema |

The `database` block is read strictly: when `--config` is given, the server does not start if the file cannot be
read, is not valid JSON, has a top-level key other than `admins`, `database` and `worker_groups`, or the `database`
block has an unknown field or a field of the wrong type. A config file without a `database` block is fine. (The
`admins` list is still read leniently.)

The server refuses to start, with an error that names the fix, when:
- the config file holds `database.password` and is not a regular file, or has any permission bit for group or
  others (`chmod 600 <file>`);
- the URL contains a password (`postgres://user:pw@host/db`) or a `password=` parameter, whether it comes from
  `--db` or from `database.url`;
- the URL has a `passfile=`, `service=`, `servicefile=` or `sslpassword=` parameter;
- `--db` looks like a PostgreSQL URL but does not start with exactly `postgres://` or `postgresql://`;
- `--config` names something that is not a regular file, or a file larger than 1 MiB;
- PostgreSQL is selected and any libpq-style `PG*` environment variable the driver reads is set (`PGHOST`,
  `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, `PGPASSFILE`, `PGAPPNAME`, `PGCONNECT_TIMEOUT`, `PGSSLMODE`,
  `PGSSLKEY`, `PGSSLCERT`, `PGSSLSNI`, `PGSSLROOTCERT`, `PGSSLPASSWORD`, `PGSSLNEGOTIATION`,
  `PGTARGETSESSIONATTRS`, `PGSERVICE`, `PGSERVICEFILE`, `PGTZ`, `PGOPTIONS`): the driver would read them on its
  own, and they could supply a password or change where and how the server connects. Give the whole connection
  (host, port, database, user, `sslmode`, certificate paths) in the URL. The error names the variables, never
  their values.

`--db` with a SQLite path wins over `database.url`; the server then logs one WARN line that the URL is ignored.
The driver also uses client certificates in `~/.postgresql/` (`postgresql.crt`, `postgresql.key`, `root.crt`)
when they exist, as libpq does.

The config file protects the password from other users of the machine, not from programs running as the
server's own user: a tool the server or a same-user worker runs on the host (no container) can read the file.
Run a PostgreSQL-backed server under a user that runs no tools if that matters.

The server also ignores `~/.pgpass`: the connection password is exactly `database.password` (empty for none, for
example with peer or trust authentication over a Unix socket). A `database.password` while the server uses SQLite
is not an error; the server logs one WARN line that it is unused.

The URL and the password are never logged. At start the store logs host, port, database and schema, then
`database ready` with `backend=postgres`.

Create the database with UTF8 encoding and the C collation; the server checks both at start:

```sql
CREATE ROLE gowe LOGIN;
\password gowe
CREATE DATABASE gowe OWNER gowe TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER libc LC_COLLATE 'C' LC_CTYPE 'C';
-- optional, with "schema": "gowe" in the config file:
\c gowe
CREATE SCHEMA gowe AUTHORIZATION gowe;
```

The tables are created on first start, as with SQLite.

To move an existing SQLite deployment to PostgreSQL with its history, copy the database once with
[`gowe-db-copy`](db-copy.md): it reads a copy of the SQLite file, writes everything into the empty PostgreSQL
database in one transaction, verifies every row before committing, and selects the target with the same rules and
the same `--config` file as the server. Start the server on PostgreSQL with the same token key file
(`--token-key-file`) as before, and keep the SQLite file as the way back; there is no tool for the reverse
direction.

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

Workers authenticate by sending an `X-Worker-Key` header. The server validates it against two sources; if **neither** has any key, worker endpoints are open (dev convenience — production SHOULD configure keys).

**1. Per-worker keys (recommended, hashed at rest).** Minted and revoked through the admin API (see [Admin](#admin) below). Only the SHA-256 hash of each key is stored, so the datastore never holds the raw secret, and one leaked worker's key can be revoked without disrupting the fleet.

**2. Static keys (file/env, for bootstrap or dev).** A key set can also be configured statically and mapped to allowed groups:

```json
// ~/.gowe/worker-keys.json
{
  "keys": {
    "secret-key-1": {
      "id": "gpu-cluster",
      "groups": ["default", "gpu-workers"],
      "description": "Production GPU cluster"
    },
    "sha256:9f86d0818...": {
      "id": "cpu-workers",
      "groups": ["cpu-workers"],
      "description": "Stored as a hash instead of the raw secret",
      "disabled": false,
      "expires_at": "2026-12-31T00:00:00Z"
    }
  }
}
```

```bash
gowe-server --worker-keys ~/.gowe/worker-keys.json
# or: GOWE_WORKER_KEYS='{"secret-key-1":["default"]}' gowe-server
```

Each static entry may carry an `id` (logged for attribution — the raw key is never logged), and optional `disabled` / `expires_at` lifecycle fields. A map key prefixed with `sha256:` is treated as an already-hashed key, so the raw secret need not live in the config file. Tasks target specific groups via `RuntimeHints.WorkerGroup`.

### Worker Group Access

Worker keys decide which **workers** may join a group. Which **submitters** may send tasks to a group (with
`gowe:Execution.worker_group` on a tool or step, or the `worker_group` label: `gowe submit --group`, the UI form) is
decided by the optional `worker_groups` block of the `--config` file:

```json
{
  "worker_groups": {
    "default_deny": false,
    "groups": {
      "model-router": {"users": ["awilke@bvbrc", "svc-router@bvbrc"], "admins": true}
    }
  }
}
```

- A group listed under `groups` is restricted: a submitter may use it only when their username is in `users`
  (exact, case-sensitive match), or when `admins` is `true` and they are a server admin. An empty `users` list with
  `admins: false` means nobody.
- A group that is not listed is open, unless `default_deny` is `true`: then every unlisted group except the default
  group is open to server admins only.
- The default group (`""` or `"default"`, the same group) is open unless it is listed itself.
- Anonymous submitters (unauthenticated REST submissions, stored as user `anonymous`) are never in a `users` list
  and never admins; `"anonymous"` in a `users` list is a start-up error.
- No block, or an empty one, means no restriction (the behaviour before this policy existed).

The block is read strictly at start-up: an unreadable file, invalid JSON, an unknown top-level key in the file
(anything other than `admins`, `database` and `worker_groups`), an unknown field in the block, a value of the wrong
type, a group name that is not letters, digits, `.`, `_` or `-` starting with a letter or digit (the rule tool
blessings use; no surrounding whitespace), or `"anonymous"` in a `users` list stops the server with an error naming
the problem. The policy is read only at start-up; a change needs a restart. At start-up the server logs one INFO
line: the restricted group names (never the user lists) and `default_deny`, or `no worker group policy: every user
may use every worker group`.

The policy is only as strong as the username it is given: with restrictions configured and
`--insecure-skip-token-verify` or `--allow-unverified-mgrast` set, a caller can claim a listed username, and the
server logs a WARN naming the flag.

The policy is applied when a task is created or re-queued:

- **At submission** (REST and UI): when the `worker_group` label or a step hint of the registered workflow names a
  group the submitter may not use, `POST /api/v1/submissions` answers `403` with code `FORBIDDEN` and the message
  `user "X" may not use worker group "Y"`, and nothing is stored; the UI shows the same message on the form.
- **At dispatch** (authoritative for new tasks): before a worker task is created, the scheduler checks the group the
  step will be routed to, for every step including scatter steps, those of nested sub-workflows and tool-level hints
  in `run:` documents. A denied step FAILS with `submitter "X" may not use worker group "Y"` (no task is created,
  nothing is retried) and the submission ends FAILED with that reason. `PUT /api/v1/submissions/{id}/retry`
  re-dispatches failed steps through this check.
- **At retry**: before the scheduler puts a FAILED worker task back in the queue, it checks the group stored on the
  task against the submitter. A denied task stays FAILED with that reason and its retries are used up, so it is
  not retried again.
- **At UI resume / recompute**: *Resume*, *Recompute Failed* and a single task's *Recompute* refuse to re-queue a
  worker task whose group the submitter may not use (`403` with the same message). When several tasks are
  re-queued at once, the denied ones are skipped, the others proceed. The check uses the submission's submitter,
  not the person clicking, so an admin cannot re-queue a denied user's task either. The reason is in the response
  body and the server log; the page itself does not display it.

Independently of any policy, *Recompute Failed* and a single task's *Recompute* are now limited to the submission's
owner and server admins, like *Resume* (before, any logged-in user could re-queue another user's failed tasks).

Only tasks that run on the `worker` executor are checked; tasks run by the server's own executors are not routed
by group.

The policy is not applied to tasks that are already QUEUED or RUNNING when the server restarts with a stricter
policy (a running task that is handed back to the queue because its worker was lost is not re-checked either): to
revoke access for work already admitted, cancel the user's submissions.

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
    },
    "host_id": "3f2a9c1d8e7b6a50",
    "host_cpus": 192,
    "host_mem_mb": 1547000,
    "slots": 1,
    "cgroup_cpu": true,
    "cgroup_memory": true,
    "gpus": [
      {"index": 0, "uuid": "GPU-1a2b3c4d-0000-1111-2222-333344445555", "name": "NVIDIA H200 NVL", "mem_total_mb": 143771}
    ]
  }'
```

`host_id`, `host_cpus`, `host_mem_mb`, `slots`, `cgroup_cpu`, `cgroup_memory` and `gpus` (#323) are optional and
informational; a worker that omits them registers as before and they are absent from the record. `host_id` must be 1-64
characters of `[A-Za-z0-9._-]` (400 otherwise); numbers `<= 0` count as not reported; `gpus` is capped at 64 entries
(extra and invalid entries — negative index or empty uuid — are dropped) with strings truncated to 128 bytes.

`default_task_cores` and `default_task_ram_mb` are optional too: the charge applied at checkout to a task that requests
no cores / no RAM (see [Capacity accounting at checkout](#capacity-accounting-at-checkout)). Numbers `<= 0` count as not
reported; a value above `max_cpus` / `max_mem_mb` (when those are set) is rejected with 400.

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
  "host_id": "3f2a9c1d8e7b6a50",
  "host_cpus": 192,
  "host_mem_mb": 1547000,
  "slots": 1,
  "cgroup_cpu": true,
  "cgroup_memory": true,
  "gpus": [ ... ],
  "last_seen": "2026-03-28T12:34:56Z",
  "registered_at": "2026-03-28T12:34:56Z"
}
```

#### `GET /api/v1/workers`

List all registered workers with state, group, runtime, GPU, and dataset info, plus the latest heartbeat usage
(`usage`, `usage_at`; see below).

```bash
curl http://localhost:8080/api/v1/workers
```

#### `PUT /api/v1/workers/{id}/heartbeat`

Worker keep-alive. Updates `last_seen`. Workers are marked offline after 30s without heartbeat.

```bash
curl -X PUT http://localhost:8080/api/v1/workers/wrk_abc123/heartbeat \
  -H "X-Worker-Key: secret-key-1"
```

The optional JSON body lists the tasks the worker is running and, from #323 on, its latest usage sample (taken every
15 s; `gpu_usage` only for a `--gpu` worker with a registered GPU inventory):

```json
{
  "running_tasks": ["task_456"],
  "host": {
    "load1": 12.5, "load5": 10.1,
    "mem_total_mb": 1547000, "mem_available_mb": 900000,
    "psi_cpu_some_avg10": 1.23, "psi_mem_some_avg10": 0.0,
    "sampled_at": "2026-11-24T10:00:00Z"
  },
  "gpu_usage": [
    {"uuid": "GPU-1a2b3c4d-0000-1111-2222-333344445555", "mem_used_mb": 40960, "util_pct": 87}
  ]
}
```

Every field is optional. The body is capped at 64 KiB; an oversized or malformed body is answered with 400 (`last_seen`
is still updated). Usage is telemetry and never fails a heartbeat: non-finite or negative numbers are dropped,
`util_pct` is clamped to 100, and `gpu_usage` entries for GPUs not in the worker's registered inventory are dropped.
The sanitised sample is stored and shown on the worker record as `usage` (`{"host": {...}, "gpus": [...]}`) with
`usage_at`, the server's receive time; a heartbeat without usage keeps the previous sample.

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
  -d '{"state": "RUNNING"}'
```

Returns `409 Conflict` (`error.code: CONFLICT`) instead of applying the report when:
- **already-terminal** — the task has already reached a terminal state (SUCCESS, FAILED, or SKIPPED), e.g. a concurrent cancel fan-out or a prior report beat this one.
- **not-owner** — the task's `external_id` no longer matches this worker's id (it was reaped/requeued and re-checked-out, or never checked out by this worker to begin with). This is the guard a stuck-task reap or a checkout race relies on: a late report from a worker that lost ownership is dropped rather than silently accepted.
- the requested state is itself terminal (terminal results must go through `/complete`, not `/status`).
- the task cannot transition from its current state to the requested state (`CanTransitionTo` rejects it).
- the state changed concurrently between the read and the compare-and-set write.

In every 409 case the worker's report is dropped; the worker logs and moves on rather than retrying (a 409 is a deliberate, not transient, refusal — see `/complete` below and the report retry behavior in `internal/worker/worker.go`).

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

Returns `409 Conflict` (`error.code: CONFLICT`) instead of applying the report when:
- **already-terminal** — the task has already reached a terminal state, e.g. a concurrent cancel fan-out already SKIPPED it, or the task was reaped as stuck and FAILED, or another report already landed.
- **not-owner** — the task's `external_id` no longer matches this worker's id, e.g. the task was reaped/requeued and re-checked-out by a different worker while this worker was still running it.
- the terminalizing compare-and-set write loses a race against a concurrent terminal write between the read and the write.

A worker that gets a 409 from `/complete` drops its result and keeps the task's working directory on disk (it does not retry and does not clean up) — see the worker protocol note above. Its retryable-vs-declined classification is in `reportComplete` (`internal/worker/worker.go`): network errors, 5xx, 429, and 408 are retried up to 3 attempts; a 409 is never retried.

#### `DELETE /api/v1/workers/{id}`

Deregister a worker.

```bash
curl -X DELETE http://localhost:8080/api/v1/workers/wrk_abc123 \
  -H "X-Worker-Key: secret-key-1"
```

---

### Hosts

#### `GET /api/v1/hosts`

Read-only per-host view of the fleet (#323): workers grouped by the `host_id` they reported at registration, sorted by
`host_id`. Same authentication as `GET /api/v1/fleet` (any signed-in user via the normal provider token; no worker key).
The numbers are the ones behind the `gowe_host_*` Prometheus gauges (label `host`); the charged totals are exported as
`gowe_host_charged_cpus` and `gowe_host_charged_mem_bytes`, next to `gowe_host_claimed_cpus` /
`gowe_host_claimed_mem_bytes`.

```bash
curl http://localhost:8080/api/v1/hosts -H "Authorization: $TOKEN"
```

Response (`data`):

```json
{
  "hosts": [
    {
      "host_id": "3f2a9c1d8e7b6a50",
      "hostnames": ["compute-01.example.com"],
      "cpus": 192,
      "mem_mb": 1547000,
      "claimed_cpus": 96,
      "claimed_mem_mb": 700000,
      "charged_cpus": 8,
      "charged_mem_mb": 32000,
      "usage": {"load1": 12.5, "load5": 10.1, "mem_total_mb": 1547000, "mem_available_mb": 900000, "sampled_at": "2026-11-24T10:00:00Z"},
      "usage_at": "2026-11-24T10:00:01Z",
      "gpus": [
        {"index": 0, "uuid": "GPU-1a2b3c4d-0000-1111-2222-333344445555", "name": "NVIDIA H200 NVL", "mem_total_mb": 143771, "mem_used_mb": 40960, "util_pct": 87, "usage_at": "2026-11-24T10:00:01Z"}
      ],
      "workers": [
        {"id": "wrk_abc123", "name": "gpu-worker-01", "group": "esmfold", "state": "online", "runtime": "apptainer",
         "slots": 1, "max_cpus": 64, "max_mem_mb": 500000, "gpu_enabled": true, "gpu_device": "0", "version": "abc1234", "last_seen": "2026-11-24T10:00:01Z", "running_tasks": 1},
        {"id": "wrk_def456", "name": "gpu-worker-02", "group": "esmfold", "state": "offline", "runtime": "apptainer",
         "last_seen": "2026-11-23T08:00:00Z", "running_tasks": 0}
      ],
      "workers_online": 1,
      "workers_total": 2,
      "running_tasks": 1
    }
  ]
}
```

- A host is listed while it has any worker row, but `cpus`/`mem_mb` (physical, max over workers; omitted when unknown),
  `claimed_cpus`/`claimed_mem_mb` (sum of `max_cpus`/`max_mem_mb`), `charged_cpus`/`charged_mem_mb` (sum of the
  charges of their running tasks, see [Capacity accounting at checkout](#capacity-accounting-at-checkout)), `usage` and
  `gpus` come from its **online** workers only; `workers` lists all of them, offline ones included.
- `usage` is the most recent host sample of an online worker, omitted when older than 2 minutes. `gpus` has one entry per
  distinct uuid (not deduplicated by index), sorted by `index` then `uuid`.
- An absent `mem_used_mb` / `util_pct` means **unknown** (no sample in the last 2 minutes), not zero. `usage_at` on a GPU
  is the server receive time of the worker report that supplied its `mem_used_mb` (or `util_pct` when there is no
  `mem_used_mb`); it is absent when the GPU has no fresh sample.
- `gpus`, `cpus`, `mem_mb` and the claimed totals are computed from **online** workers only, so a host whose GPU worker
  is offline or restarting lists no `gpus`.
- Each worker carries `gpu_enabled` and `gpu_device` (omitted when false/empty) as registered, to relate running work to
  GPUs.
- `running_tasks` counts RUNNING worker tasks checked out by each worker.
- Workers registered without a `host_id` (older workers) are grouped by hostname under `host_id` `hostname:<hostname>`
  with `"legacy": true`; they get no `gowe_host_*` gauges.
- The `hostnames` field is admin-only: for non-admin callers it is omitted and a legacy host's id becomes
  `legacy:<smallest worker id>`. Worker names default to the machine's hostname and are returned to every caller, exactly
  as on `/fleet`. Admins see everything (the admin fleet page shows the same view).
- The charged totals are summed by `host_id` only. The checkout host gate below also groups by worker key, so workers
  registered with different keys on one host show up together here but are gated separately.

#### Capacity accounting at checkout

When a worker checks out a task, the server charges the task against the worker and its host, per dimension (cores,
RAM): the **charge** is the task's request (`ResourceRequirement` cores / ram), else the worker's default share
(`default_task_cores` / `default_task_ram_mb`, optional at registration; a share above the worker's effective
capacity is rejected with 400), else 0. The charge is stored on the task (`charged_cores`, `charged_ram_mb`) and
cleared when the task is requeued (stale worker, heartbeat reconciliation, worker deletion).

A task is only handed to a worker when its charge fits two gates:
- **Worker gate:** `max_cpus` / `max_mem_mb` minus the charges of the worker's own RUNNING tasks.
- **Host gate:** the host capacity (`host_cpus` / `host_mem_mb`) minus the charges of the RUNNING tasks of all workers
  that are not offline with the same `host_id` **and** the same worker key. Workers without a `host_id`, or without a reported host
  capacity, are only worker-gated.

A request larger than the effective capacity (the smaller of `max_cpus` and `host_cpus`; same for memory) is never
offered to that worker, and the scheduler uses the same effective capacity when it decides whether any worker could ever
run a task. A task that charges nothing (no request, no default share) is never held back, even on an over-committed
host. Running tasks of offline workers are not counted (the reaper requeues them), except the checking-out worker's own; a RUNNING task checked out by an older server has no charge and counts as 0.

**GPUs** are a third dimension, counted per worker only (no host term, no default share). A task's GPU request is
`gpus` (from `gowe:Execution.gpu_count` or `CUDARequirement.cudaDeviceCountMin`), else 1 when it only carries
`requires_gpu` (`gpu: true`, and tasks and workflows from before GPU counts), else 0. A worker's GPU capacity is the
number of GPUs in the inventory it registered with `--gpu`:
- A CPU-only worker is never offered a task that needs a GPU.
- A GPU worker with an inventory of `c` GPUs is offered a task needing `k` GPUs only when `k <= c` and, for `k > 0`,
  `k` fits `c` minus the GPUs charged to the worker's own RUNNING tasks.
- A GPU worker that registered no inventory (older worker) or an empty one (`nvidia-smi` failed) has an unknown
  capacity: it is offered only tasks needing at most one GPU, with no free-GPU gate.

The GPU charge is stored on the task (`charged_gpus`, 0 for a task that needs no GPU) with the same lifecycle as
`charged_cores`; a multi-task GPU worker gives the task exactly that many of its devices. A task that needs more
GPUs than any online GPU worker of its group can give is not failed at dispatch: it stays queued, like a GPU task
while no GPU worker is online. GPU workers are still preferred for GPU tasks at checkout.

When a GPU worker registers with GPU UUIDs that another **online** worker with the same `host_id` also reports, the
server logs a WARN naming both workers and the shared UUIDs: the two would hand out the same devices (give each worker
its own GPUs with `--gpu-id`). The registration is not rejected, since a restarted worker's previous row stays online
until the reaper removes it.

**Holding capacity for a waiting task** (`--checkout-hold-after`, default `10m`, `0` disables). On a worker that runs
several tasks at once, a large task could otherwise wait forever: whenever one of the worker's tasks ends, smaller
tasks behind the large one take the freed capacity before enough is free for it. So when a worker that has running
tasks checks out, the server looks for the first queued task in queue order (priority, then age) that this worker
could otherwise take and that has waited longer than the hold-after duration (counted from its last dispatch, else its
creation), whose charge does not fit what is free on the **worker gate** (`max_cpus` / `max_mem_mb` minus the charges
of the worker's own running tasks) in some dimension, and that would fit the host gate once the worker's own running
tasks finished. If it finds one, the worker does not take tasks behind it that charge the dimension it is short of
(cores, RAM, GPUs, or several). GPUs count only on a worker with a GPU inventory; a task that needs no GPU is never
held for a GPU shortfall. It still takes tasks ahead of it, and tasks behind it that charge nothing in that dimension
(including tasks that charge nothing at all), as usual. Once enough capacity is free, tasks behind the one
the worker held for are not considered at that checkout, whatever their GPU or cache-dataset preference; tasks ahead
of it keep their place. The server keeps the held-for task per
worker in memory; after a server restart the next checkout re-establishes it.

- A worker that runs one task at a time never holds: it only asks for work when it is idle, so its own gate is never
  short.
- Only the worker gate triggers a hold. A task waiting only for host capacity used by other workers on the same host
  is not protected, because those workers may never be offered it and holding on this worker would not free anything
  for it.
- A dimension without `max_cpus` / `max_mem_mb` never triggers a hold.
- A hold has no time limit: it lasts until the worker's own running tasks have freed enough capacity.
- Every worker of a group that could run the waiting task holds for it at the same time, and only one of them will
  take it; the others release at their next checkout.
- If other workers on the host take the freed capacity before the waiting task is checked out, the task waits again
  and the hold starts over.
- A task requeued after its worker went away keeps its dispatch time, so it can count as waiting immediately.
- The search looks at the first 50 short, aged tasks in queue order; if none of them qualifies, nothing is held.
- The server logs at Info when a worker starts holding for a task (worker id, task id, the short dimensions, charge
  and free capacity).

Known undercounts:
- A cancelled task releases its charge as soon as the server marks it terminal, before the worker has actually stopped
  its process. For GPUs, a multi-task worker makes the next task wait (at most 60 s) until the device is released.
- Tasks already RUNNING when the server is upgraded have no charge until they are requeued and checked out again.

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

#### `POST /api/v1/admin/worker-keys`

Mint a per-worker key. The raw secret is returned **exactly once**; only its SHA-256 hash is persisted. `groups` scopes which worker groups the key may join (empty = any); `expires_at` is optional.

```bash
curl -X POST http://localhost:8080/api/v1/admin/worker-keys \
  -H "Authorization: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"label":"gpu-node-01","groups":["esmfold"],"description":"A100 box","expires_at":"2026-12-31T00:00:00Z"}'
```

Response (201) — save `key` now, it cannot be retrieved again:

```json
{
  "key": "gwk_o4Zk...redacted...",
  "worker_key": {
    "id": "wk_1f2e...",
    "label": "gpu-node-01",
    "key_prefix": "gwk_o4Zk8Q",
    "groups": ["esmfold"],
    "disabled": false,
    "created_by": "awilke@bvbrc",
    "created_at": "2026-07-06T12:00:00Z",
    "expires_at": "2026-12-31T00:00:00Z"
  },
  "warning": "store this key now; it cannot be retrieved again"
}
```

#### `GET /api/v1/admin/worker-keys`

List all worker keys. Key hashes and raw secrets are never returned — only metadata and the non-secret `key_prefix`.

```bash
curl http://localhost:8080/api/v1/admin/worker-keys -H "Authorization: $ADMIN_TOKEN"
```

#### `PATCH /api/v1/admin/worker-keys/{id}`

Enable/disable or edit a key. Disabling is the **reversible** form of revocation. Any subset of fields may be sent; `clear_expiry: true` removes an expiry.

```bash
# Disable a key without deleting it
curl -X PATCH http://localhost:8080/api/v1/admin/worker-keys/wk_1f2e... \
  -H "Authorization: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"disabled": true}'
```

#### `DELETE /api/v1/admin/worker-keys/{id}`

Permanently revoke (delete) a key. Use this for a compromised worker — other workers' keys are unaffected.

```bash
curl -X DELETE http://localhost:8080/api/v1/admin/worker-keys/wk_1f2e... \
  -H "Authorization: $ADMIN_TOKEN"
```

#### `POST /api/v1/admin/submissions/{id}/verify-outputs`

Download every `ws://` output of a delivered (or `upload_failed`) submission,
including sub-workflow children, and compare it to the SHA-1 checksum and size
the worker recorded before upload. Read-only; acts with the submission's stored
token. Answers `503` when Workspace staging is not available and `409` when the
submission is not `delivered`/`upload_failed`, not terminal, or its stored
token is missing or expired.

```bash
curl -X POST http://localhost:8080/api/v1/admin/submissions/sub_abc/verify-outputs \
  -H "Authorization: $ADMIN_TOKEN"
```

#### `POST /api/v1/admin/submissions/{id}/redeliver`

Re-upload every output that fails verification from its local original,
matched by checksum+size among the submission's task outputs and read only
from under `--redeliver-source-dirs`; then re-verify, rewrite the output
manifest, and mark the submission `delivered` (restoring `COMPLETED` for an
`OUTPUT_STAGING_FAILED` failure) once every output verifies. `?dry_run=true`
reports the plan without changing anything. One re-delivery per submission at
a time (`409`); writes are compare-and-set (`409` on a concurrent change).

```bash
curl -X POST "http://localhost:8080/api/v1/admin/submissions/sub_abc/redeliver?dry_run=true" \
  -H "Authorization: $ADMIN_TOKEN"
```

Request/response details: [API_GUIDE.md §10](../API_GUIDE.md#10-admin-verify-and-re-deliver-workspace-outputs).
CLI: `gowe admin verify-outputs`, `gowe admin redeliver`.

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
