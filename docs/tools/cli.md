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
```

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
