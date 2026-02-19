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

Submit a CWL workflow for execution.

```bash
gowe submit <workflow.cwl> [flags]
```

**Flags:**
- `-i, --inputs` - Input values file (YAML or JSON)
- `--dry-run` - Validate without executing

**Examples:**

```bash
# Submit a workflow with no inputs
gowe submit my-workflow.cwl

# Submit with input values
gowe submit pipeline.cwl -i inputs.yaml

# Validate without running
gowe submit pipeline.cwl -i inputs.yaml --dry-run
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

```bash
# Check submission status
gowe status sub_abc123

# Check workflow details
gowe status wf_xyz789
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
