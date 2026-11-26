# GoWe CLI

The GoWe CLI (`gowe`) is a command-line client for interacting with the GoWe server. It provides commands for authentication, workflow submission, monitoring, and management.

## Installation

```bash
# From source
go build -o gowe ./cmd/cli

# Or install globally
go install github.com/me/gowe/cmd/cli@latest
```

## Usage

```bash
gowe [command] [flags]
```

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `http://localhost:8080` | GoWe server URL |
| `--debug` | `false` | Enable debug logging |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-format` | `text` | Log format: text, json |
| `--token-file` | `""` | Read the API token from this file instead of `~/.gowe/credentials.json` |

`--token-file FILE` is how a program uses a [service token](#service-tokens) (or any token):
the file must be a regular file (a symbolic link is refused, never followed), owned by the
user running `gowe`, with mode 0600 or stricter and at most 4096 bytes (otherwise the command
fails before any request); its content is the token, surrounding white space is trimmed. It takes
precedence over the stored credentials of `gowe login`. There is deliberately no environment
variable or command-line argument for the token itself. `submit --workspace-upload` refuses a
service token (it is not a BV-BRC token and is never sent to the Workspace).

## Commands

### login

Authenticate with BV-BRC and store credentials.

```bash
gowe login [flags]
```

**Flags:**
- `--token` - BV-BRC authentication token (prompted if omitted)

**Examples:**

```bash
# Interactive login (prompts for token)
gowe login

# Provide token directly
gowe login --token "un=user@patricbrc.org|tokenid=...|expiry=...|sig=..."
```

The token is stored in `~/.gowe/credentials.json` with secure permissions (0600).

---

### submit

Submit a CWL workflow for execution. You can provide a local CWL file (which will be bundled and registered) or reference an already-registered workflow by ID or name.

```bash
gowe submit <workflow.cwl> [flags]
gowe submit --workflow <id-or-name> [flags]
```

**Flags:**
- `-i, --inputs` - Input values file (YAML or JSON)
- `--workflow` - Submit using an already-registered workflow (by ID or name)
- `--output-destination` - Target URI for uploading outputs (e.g., `ws:///user@bvbrc/home/results/`)
- `--group` - Target worker group for task scheduling
- `--no-upload` - Disable file upload; assume files are accessible on workers
- `--workspace-upload` - Upload input files to the BV-BRC Workspace instead of the server (for the `bvbrc` executor)
- `--workspace-url` - Workspace service URL for `--workspace-upload` (or `GOWE_WORKSPACE_URL`; default: production)
- `--debug` - Debug mode for this submission: workers keep its task working directories instead of deleting them after success (distinct from the global `--debug` logging flag)
- `--dry-run` - Validate without executing

**Examples:**

```bash
# Submit a local CWL file (bundles and registers automatically)
gowe submit my-workflow.cwl

# Submit with input values
gowe submit pipeline.cwl -i inputs.yaml

# Submit an already-registered workflow by name
gowe submit --workflow protein-structure-prediction -i inputs.yaml

# Submit by workflow ID
gowe submit --workflow wf_f8975ed7-0ea8-48a9-bbcb-f6ebad1305b9 -i inputs.yaml

# Upload outputs to BV-BRC Workspace after completion
gowe submit pipeline.cwl -i inputs.yaml --output-destination "ws:///user@bvbrc/home/results/"

# Target a specific worker group
gowe submit pipeline.cwl -i inputs.yaml --group gpu-workers

# Validate without running
gowe submit pipeline.cwl -i inputs.yaml --dry-run

# Keep every task's working directory on the workers for troubleshooting
gowe submit pipeline.cwl -i inputs.yaml --debug
```

**Input file format (YAML):**

```yaml
reads_r1: /path/to/reads_R1.fastq
reads_r2: /path/to/reads_R2.fastq
reference_genome: /path/to/reference.fasta
threads: 8
```

**Input file format (JSON):**

```json
{
  "reads_r1": "/path/to/reads_R1.fastq",
  "reads_r2": "/path/to/reads_R2.fastq",
  "reference_genome": "/path/to/reference.fasta",
  "threads": 8
}
```

**Dry-run output:**

```
Dry-run: my-pipeline
  Workflow: valid
  Steps:
    1. assemble -> GenomeAssembly2 (bvbrc)
    2. annotate -> GenomeAnnotation (bvbrc)
  DAG: acyclic
  Executors:
    local: available
    bvbrc: available

No submission created. Use without --dry-run to execute.
```

---

### run

Execute a CWL workflow with cwltest-compatible output.

```bash
gowe run <cwl-file> [job-file] [flags]
```

**Flags:**
- `--outdir` - Output directory for result files (default: temporary directory)
- `-q, --quiet` - Suppress progress messages (required for cwltest)
- `--timeout` - Execution timeout (default: 5m)
- `--no-upload` - Disable file upload; use `GOWE_PATH_MAP` for shared-filesystem mode

**Examples:**

```bash
# Run a workflow with inputs
gowe run pipeline.cwl inputs.yaml

# Run with quiet mode for cwltest compatibility
gowe run --quiet pipeline.cwl inputs.yaml

# Run against a remote server
gowe run --server http://gowe-server:8080 pipeline.cwl inputs.yaml

# Custom output directory
gowe run --outdir ./results pipeline.cwl inputs.yaml
```

**Output format (CWL-compatible JSON):**

```json
{
  "output": {
    "class": "File",
    "location": "file:///tmp/cwl-output-123/output.txt",
    "basename": "output.txt",
    "checksum": "sha1$a94a8fe5ccb19ba61c4c0873d391e987982fbbd3",
    "size": 42
  }
}
```

This command is designed to work with [cwltest](https://github.com/common-workflow-language/cwltest), the CWL conformance testing tool. It follows the same interface as `cwl-runner`:

1. Bundles the CWL file (resolving external references)
2. Creates a workflow on the server
3. Submits a run with the provided inputs
4. Polls until completion
5. Outputs results as CWL-formatted JSON to stdout

**Note:** Progress messages go to stderr, results go to stdout. Use `--quiet` to suppress progress messages entirely.

---

### status

Check workflow or submission status.

```bash
gowe status <id> [flags]
```

**Examples:**

**Flags:**

| Flag | Description |
|------|-------------|
| `--timing` | Show the timing breakdown instead of the plain status: submission totals (wall, scheduling, compute, queue, prestage, poststage, critical path), per-step wall/fan-in/max-run, and per-task queue/dispatch/checkout-wait/stage-in/compute/stage-out/run durations. Sub-workflow children are included recursively. |
| `--json` | Print the raw timing JSON (requires `--timing`) |

**Examples:**

```bash
# Check submission status
gowe status sub_abc123

# Check workflow details
gowe status wf_xyz789

# Timing breakdown (per step and per task)
gowe status sub_abc123 --timing

# Raw timing JSON for scripting
gowe status sub_abc123 --timing --json
```

**Output:**

```
Submission: sub_abc123
State: RUNNING
Workflow: wf_xyz789 (my-pipeline)
Created: 2024-01-15 10:30:00

Tasks:
  task_001  assemble   RUNNING   bvbrc
  task_002  annotate   PENDING   bvbrc
```

**Timing output (`--timing`):**

```
Submission sub_abc123 [COMPLETED]  wall=322.4s scheduling=0.8s compute=301.2s queue=12.5s prestage=1.9s poststage=3.4s critical-path=310.9s
  STEP      STATE      WALL    FAN-IN  MAX-RUN  TASKS
  assemble  COMPLETED  290.1s  0.9s    288.0s   3
  TASK      STEP      IDX  EXECUTOR  STATE    KIND  QUEUE  DISPATCH  CHECKOUT  STAGE-IN  COMPUTE  STAGE-OUT  RUN     RETRIES
  task_001  assemble  0    worker    SUCCESS  task  4.1s   0.1s      3.6s      12.0s     270.5s   5.5s       288.0s  0
```

Queue/run durations follow the timing trust rules: a `QUEUED` worker or bvbrc
task shows waiting time from its creation (its `started_at` is untrustworthy —
`null` as of this endpoint's dispatch/staging attribution, or a stale dispatch
stamp for a row still `QUEUED` from before that upgrade), and `RETRYING` rows
show the last failed attempt's window. DISPATCH/CHECKOUT/STAGE-IN/STAGE-OUT
render `-` when the executor or attempt has no such data (only `worker` tasks
report stage timings; synchronous executors have a near-zero CHECKOUT).

---

### list

List workflows or submissions.

```bash
gowe list [workflows|submissions] [flags]
```

**Examples:**

```bash
# List all workflows
gowe list workflows

# List all submissions
gowe list submissions

# Default: list submissions
gowe list
```

**Output:**

```
ID              STATE       WORKFLOW            CREATED
sub_abc123      COMPLETED   my-pipeline         2024-01-14 09:00:00
sub_def456      RUNNING     genome-annotation   2024-01-15 10:30:00
sub_ghi789      FAILED      assembly-pipeline   2024-01-15 11:00:00
```

---

### cancel

Cancel a running submission.

```bash
gowe cancel <submission_id>
```

**Examples:**

```bash
gowe cancel sub_abc123
```

**Output:**

```
Submission sub_abc123 cancelled
```

---

### logs

Fetch task or submission logs.

```bash
gowe logs <submission_id> [task_id] [flags]
```

**Examples:**

```bash
# Get logs for a specific task
gowe logs sub_abc123 task_001

# List all task logs for a submission
gowe logs sub_abc123

# Also print each task's earlier attempts (attempt, reason, exit code,
# worker, times, stderr; see server.md "Task attempt history")
gowe logs sub_abc123 --task task_001 --attempts
```

**Output:**

```
Task: task_001 (assemble)
State: SUCCESS
Exit Code: 0

--- stdout ---
Assembly completed successfully
Contigs: 42
N50: 125000

--- stderr ---
[INFO] Starting assembly...
[INFO] Processing reads...
```

---

### apps

List or query BV-BRC apps.

```bash
gowe apps [app_id] [flags]
```

**Examples:**

```bash
# List all available apps
gowe apps

# Get details for a specific app
gowe apps GenomeAnnotation
```

**Output (list):**

```
APP ID                  LABEL
GenomeAnnotation        Genome Annotation
GenomeAssembly2         Genome Assembly
ComprehensiveGenome...  Comprehensive Genome Analysis
RNASeq                  RNA-Seq Analysis
...
```

**Output (detail):**

```
App: GenomeAnnotation
Label: Genome Annotation
Description: Annotate a genome using RASTtk

Parameters:
  contigs            File      Required   Input contigs file
  scientific_name    string    Required   Scientific name
  taxonomy_id        int       Required   NCBI Taxonomy ID
  output_path        folder    Required   Output folder
  output_file        string    Required   Output filename prefix
```

### admin

Administrative operations; the caller must hold the **admin** role.

```bash
gowe admin verify-outputs <submission_id> | --all [--output-state delivered,upload_failed] [--since YYYY-MM-DD] [--json]
gowe admin redeliver <submission_id> [--dry-run] [--json]
gowe admin worker-groups list|set|delete|default-deny ...   # see below
gowe admin remove-worker <worker-id>
gowe admin service-tokens create|list|revoke ...            # see below
```

**remove-worker** removes a worker of any group from the worker list. Only a worker that is
offline and has sent no heartbeat for 3 x the server's worker timeout (90 s) is removed (the
server answers `409` otherwise and the command prints its reason); its running tasks, if any
are still attributed to it, are requeued. See [Retiring a worker](#retiring-a-worker).

**verify-outputs** (read-only) downloads every `ws://` output of a submission — including
sub-workflow child submissions — and compares it to the SHA-1 checksum and size the worker
recorded before upload. `--all` verifies every submission whose `output_state` matches
`--output-state` (default `delivered,upload_failed`), optionally created on/after `--since`.

**redeliver** re-uploads each output that fails verification from its local original (found
by checksum+size among the task outputs, read only from the server's
`--redeliver-source-dirs`), re-verifies it, rewrites the output manifest, and marks the
submission `delivered` once every output verifies. `--dry-run` reports the plan
(`would_reupload` / `original_missing`) without changing anything. Nothing is ever deleted.

Both act with the submission's **stored** token (the admin's own token has no write access
to another user's workspace) and exit non-zero when any output fails. Server-side errors:
`503` when workspace staging is not configured, `409` when the submission is not terminal,
its `output_state` is not `delivered`/`upload_failed`, its stored token is missing or
expired, or another re-delivery of it is in progress.

```bash
gowe admin verify-outputs sub_abc
gowe admin verify-outputs --all --since 2026-06-01 --json > verify.json
gowe admin redeliver sub_abc --dry-run
gowe admin redeliver sub_abc
```

See [API_GUIDE.md §10](../API_GUIDE.md#10-admin-verify-and-re-deliver-workspace-outputs) and
[upgrading.md](../upgrading.md) for the post-0.15.0 recovery procedure.

#### worker-groups

Shows and changes which submitters may use which worker groups (the worker group access
policy, stored in the server's database; see
[server.md, Worker Group Access](server.md#worker-group-access)).

```bash
gowe admin worker-groups list [--json]
gowe admin worker-groups set <group> [--user <username>]... [--admins] [--manager <username>]... [--no-managers]
gowe admin worker-groups delete <group>
gowe admin worker-groups default-deny <true|false>
```

**set** creates or replaces the rule of a group: only the `--user` usernames (repeatable, exact
and case-sensitive) and the group's managers may use it, and server admins too with `--admins`;
with none of them, nobody may. **set replaces the users and the admins switch**, it does not edit
them: omitting `--admins` turns the admins switch off, and omitting `--user` leaves nobody in the
users list. Managers are different: `--manager` (repeatable) replaces them, **without `--manager`
the stored managers are kept**, and `--no-managers` removes them all. Managers manage the
group's users and its group-only worker keys with [`gowe worker-groups`](#worker-groups-1); the
default group cannot have managers. When **set** removes managers (or **delete** removes a rule
that had managers or keys), the output lists the worker keys of that group alone: they stay
valid, revoke them if the former manager should no longer add workers. **list** shows the
managers of every rule. The default group is written
`default`. **delete** removes the rule: the group becomes open, or admin-only while
**default-deny** is `true` (which makes every group without a rule, except `default`,
admin-only). Group names (for **set**) and usernames are checked before any request is sent;
`anonymous` cannot be listed. **delete** only refuses an empty name, so it also removes a stored
rule with an invalid name.

A change takes effect at once for new submissions, dispatch and re-queued tasks; tasks already
QUEUED or RUNNING are not checked again. **list** first reloads the stored policy into the one
the server enforces, so it shows what is in force; after a change answered "stored but ... could
not be reloaded" (the stored and the enforced policy then differ), running **list** once the
cause is fixed re-syncs them. A direct SQL edit of the tables is seen after the next **list**,
change or server restart.

```bash
gowe admin worker-groups set gpu --user alice@bvbrc --user bob@bvbrc --admins --manager lead@bvbrc
gowe admin worker-groups default-deny true
gowe admin worker-groups list
```

#### service-tokens

Service tokens give a program (such as the model router) an identity of its own: a request
made with a token of name `N` is the user `N@service.gowe`, role user, **never an admin** and
never a worker group manager, with no BV-BRC token (so no Workspace staging) and **no BV-BRC
executor** (a workflow with a step on it is refused with `403`). What a token holder can do is
listed in the trust statement of
[authentication.md, Service tokens](../security/authentication.md#service-tokens).

```bash
gowe admin service-tokens create <name> [--description TEXT] [--expires DURATION] --out FILE
gowe admin service-tokens list [--json]
gowe admin service-tokens revoke <id>
```

**create** issues a token for `<name>` (1-64 characters: a lower-case letter or digit, then
lower-case letters, digits, `.`, `_` or `-`; several tokens may share a name, for rotation).
`--out` is **required**: the token is written only to that file, which is created with mode
0600 and must not exist (it is never overwritten); the file is created before the request, so
a file that cannot be created costs no token. The token is never printed, also not with
`--debug` (the answer holding it is not logged). The command prints the token id, the username
to put into a worker group rule, the prefix, the expiry and the file. `--expires` is a duration
from now between `1h` and `8760h` (default: no expiry). **list** shows id, name, username,
prefix, created, expires and last used (written at most once a minute), never the token.
**revoke** deletes a token; the next request made with it is refused.

```bash
gowe admin service-tokens create model-router --expires 2160h --out /etc/model-router/gowe.token
gowe admin worker-groups set model-router --user model-router@service.gowe
# on the router's host:
gowe --token-file /etc/model-router/gowe.token submit pipeline.cwl -i job.yaml --group model-router
gowe admin service-tokens revoke svt_6d1c...
```

### worker-groups

For the **managers** of a worker group (appointed by a server admin with
`gowe admin worker-groups set <group> --manager <username>`) and for server admins: the users,
the worker keys and the workers of the groups they manage (see
[server.md, Worker group managers](server.md#worker-group-managers)).

```bash
gowe worker-groups list [--json]                    # the groups you manage (admins: all)
gowe worker-groups show <group>
gowe worker-groups add-user <group> <username>
gowe worker-groups remove-user <group> <username>
gowe worker-groups keys <group> [--json]            # keys valid for exactly this group
gowe worker-groups create-key <group> --label L [--description D] [--expires 2027-01-01T00:00:00Z|720h] [--out FILE]
gowe worker-groups revoke-key <group> <key-id>
gowe worker-groups workers <group> [--json]         # the group's workers, offline ones included
gowe worker-groups remove-worker <group> <worker-id>
```

**add-user** / **remove-user** change who may submit to the group; the change takes effect at
once (tasks already QUEUED or RUNNING are not stopped). **create-key** issues a worker key whose
groups are exactly `[<group>]`; a worker started with it (`gowe-worker --group <group>
--worker-key-file FILE`) joins the group. The raw key is shown once: printed to stdout (with a
note on stderr that it is not shown again), or with `--out FILE` written to a new file with mode
0600 (an existing file is never overwritten; the file is created before the key is requested)
and not printed. The answer holding the key is never logged, not even with `--debug`. When the
answer cannot be read or the key cannot be written to `--out`, the file is removed and the error
says how to find (`keys`) and revoke (`revoke-key`) the key that may have been created.
`--expires` takes an RFC 3339 time or a duration from now; `--label` may be at most 200 bytes
and `--description` at most 1000. It fails with `409` until a server admin has created the
first worker key, and while the group has 100 keys of its own. **revoke-key** deletes a key of this group
only (a key with any other groups is `404`); a task already running with it is not stopped.

**workers** lists the workers of the group (whatever key they registered with), sorted by name:
worker id (what **remove-worker** takes), name, state, hostname, peer address, last seen and
version. Non-printable characters in these worker-supplied values (control characters such as
tab, newline and ESC) are shown as `?`. `--json` prints the full answer unchanged (JSON escapes
control characters), including `key_id` (the key's id when its groups are exactly `[<group>]`,
`other` for any other key). The peer address is the address the server saw the worker's last
heartbeat come from; behind a reverse proxy it is the proxy's address. A worker that stops
heartbeating is shown `offline` and stays listed until it is removed. **remove-worker**
removes a worker of the group that is offline and has sent no heartbeat for 3 x the server's
worker timeout (90 s); otherwise the server refuses (`409`) and says why.

Managers cannot change the managers, the admins switch or default-deny, nor delete the rule
(`gowe admin worker-groups`). Whoever holds a key of a group can run a worker that receives
every task sent to that group, including the submitter's data and the token used for staging.

```bash
gowe worker-groups add-user gpu carol@bvbrc
gowe worker-groups create-key gpu --label gpu-node-07 --expires 2160h --out ~/gpu-node-07.key
gowe worker-groups keys gpu
gowe worker-groups revoke-key gpu wk_1f2e...
gowe worker-groups workers gpu
gowe worker-groups remove-worker gpu wrk_3c4d...
```

#### Retiring a worker

1. Stop the worker on its host (or revoke its key).
2. Wait until it is shown `offline` (30 s without heartbeat, then up to 10 s), and until it
   has sent no heartbeat for 90 s (3 x the worker timeout) in all.
3. Remove it: `gowe worker-groups remove-worker <group> <worker-id>` (server admins, any
   group: `gowe admin remove-worker <worker-id>`).

A worker process that is still running when it is removed (it had not reached the server for
90 s) does **not** register again by itself: its requests are answered `404` from then on, and
it must be restarted. A removed worker that is started again with a valid key registers again,
as a new worker. Revoking a key does not stop a task already running on the worker.

---

## Tutorial: Complete Workflow Submission

### 1. Authenticate (if using BV-BRC)

```bash
# Get your token from BV-BRC website or use existing token file
gowe login
# Enter your token when prompted
```

### 2. Create a workflow file

Create `assembly-workflow.cwl`:

```yaml
cwlVersion: v1.2
class: Workflow

inputs:
  reads_r1:
    type: File
    doc: Forward reads (FASTQ)
  reads_r2:
    type: File
    doc: Reverse reads (FASTQ)
  scientific_name:
    type: string
    doc: Scientific name of organism
  taxonomy_id:
    type: int
    doc: NCBI Taxonomy ID

steps:
  assemble:
    run: tools/assembly.cwl
    in:
      read1: reads_r1
      read2: reads_r2
    out: [contigs]

outputs:
  contigs:
    type: File
    outputSource: assemble/contigs
```

### 3. Create an inputs file

Create `inputs.yaml`:

```yaml
reads_r1:
  class: File
  path: /data/sample_R1.fastq.gz
reads_r2:
  class: File
  path: /data/sample_R2.fastq.gz
scientific_name: "Escherichia coli"
taxonomy_id: 562
```

### 4. Validate with dry-run

```bash
gowe submit assembly-workflow.cwl -i inputs.yaml --dry-run
```

### 5. Submit for execution

```bash
gowe submit assembly-workflow.cwl -i inputs.yaml
```

Output:

```
Workflow registered: wf_abc123
Submission created: sub_xyz789 (state: PENDING)
```

### 6. Monitor progress

```bash
# Check status
gowe status sub_xyz789

# Poll until complete
watch -n 10 gowe status sub_xyz789
```

### 7. View results

```bash
# Get task logs
gowe logs sub_xyz789

# View specific task output
gowe logs sub_xyz789 task_001
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GOWE_SERVER` | Default server URL (overrides `--server` default) |
| `BVBRC_TOKEN` | BV-BRC authentication token |
| `GOWE_PATH_MAP` | Path mapping for shared-filesystem distributed mode (format: `src1=dst1:src2=dst2`) |
| `GOWE_OUTPUT_PATH_MAP` | Output path translation for distributed mode (translates container paths to host paths) |

## Configuration Files

| File | Description |
|------|-------------|
| `~/.gowe/credentials.json` | Stored BV-BRC token from `gowe login` |
| `--token-file FILE` | A token (e.g. a service token) in a file of your own, mode 0600 or stricter, not a symlink, at most 4096 bytes; takes precedence over the stored token |

## Troubleshooting

### Connection refused

```
Error: Post "http://localhost:8080/api/v1/workflows/": dial tcp 127.0.0.1:8080: connect: connection refused
```

The server isn't running. Start it with:

```bash
gowe-server
```

### Workflow validation failed

```
Error: create workflow: API error VALIDATION_ERROR: CWL validation failed
```

Check your CWL syntax. Use `--debug` for details:

```bash
gowe submit workflow.cwl --debug
```

### BV-BRC app not found

```
Error: executor 'bvbrc' not available
```

Ensure you're logged in:

```bash
gowe login
```

And the server has a valid token (restart server after login).
