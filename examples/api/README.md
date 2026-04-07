# GoWe API Examples

Standalone Python scripts demonstrating the GoWe REST API. No dependencies beyond Python 3.10+ standard library.

## Configuration

All scripts read these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `GOWE_URL` | `http://localhost:8091` | GoWe server base URL |
| `BVBRC_TOKEN` | (none) | BV-BRC authentication token |

```bash
export GOWE_URL=http://localhost:8091
export BVBRC_TOKEN="un=user@bvbrc|tokenid=...|expiry=...|sig=..."
```

## Scripts

### list_apps.py

List available BV-BRC applications.

```bash
python list_apps.py
python list_apps.py --search Assembly
```

### get_workflow_inputs.py

Show input definitions for a registered workflow.

```bash
python get_workflow_inputs.py protein-structure-prediction
python get_workflow_inputs.py wf_abc123
```

### dry_run.py

Validate a submission without running it. Checks inputs, DAG structure, and executor availability.

```bash
python dry_run.py protein-structure-prediction inputs.json
```

### submit_and_poll.py

Submit a job and poll until it reaches a terminal state. Prints task progress and final outputs (or failure logs).

```bash
python submit_and_poll.py protein-structure-prediction inputs.json
python submit_and_poll.py wf_abc123 inputs.json --output-dest "ws:///user@bvbrc/home/results/"
GOWE_POLL=5 python submit_and_poll.py wf_abc123 inputs.json
```

## Example Input Files

Create a JSON file matching the workflow's input schema:

```json
{
  "sequence": {
    "class": "File",
    "location": "/path/to/protein.fasta"
  },
  "max_iterations": 5
}
```

File and Directory inputs use CWL object syntax:

```json
{
  "reads": [
    { "class": "File", "location": "/data/sample_R1.fastq.gz" },
    { "class": "File", "location": "/data/sample_R2.fastq.gz" }
  ],
  "reference": {
    "class": "Directory",
    "location": "/data/ref_genome/"
  }
}
```

BV-BRC workspace paths work too:

```json
{
  "input_file": {
    "class": "File",
    "location": "ws:///user@bvbrc/home/data/sample.fastq.gz"
  }
}
```
