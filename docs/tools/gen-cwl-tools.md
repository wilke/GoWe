# gen-cwl-tools

The `gen-cwl-tools` utility generates CWL (Common Workflow Language) CommandLineTool definitions from BV-BRC app specifications. This enables using BV-BRC bioinformatics apps in standard CWL workflows.

## Installation

```bash
# From source
go build -o gen-cwl-tools ./cmd/gen-cwl-tools
```

## Prerequisites

A valid BV-BRC authentication token is required. The tool checks these locations (in order):

1. `BVBRC_TOKEN` environment variable
2. `~/.gowe/credentials.json`
3. `~/.bvbrc_token`
4. `~/.patric_token`
5. `~/.p3_token`

## Usage

```bash
gen-cwl-tools [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output-dir` | `cwl` | Root output directory for generated files |
| `--debug` | `false` | Enable debug logging |

## Examples

### Generate all tools

```bash
# Generate with default output directory
gen-cwl-tools

# Specify custom output directory
gen-cwl-tools --output-dir /path/to/cwl-tools

# With debug logging
gen-cwl-tools --debug
```

### Output structure

```
cwl/
├── tools/
│   ├── GenomeAnnotation.cwl
│   ├── GenomeAssembly2.cwl
│   ├── ComprehensiveGenomeAnalysis.cwl
│   ├── RNASeq.cwl
│   └── ...
├── workflows/           # (placeholder for future use)
└── REPORT.md           # Detailed generation report
```

### Sample output

```
Token loaded for user "awilke@patricbrc.org" (expires 2024-02-15)
Fetched 45 apps from BV-BRC

  OK    GenomeAnnotation — 12 inputs
  OK    GenomeAssembly2 — 15 inputs
  OK    ComprehensiveGenomeAnalysis — 18 inputs
  SKIP  DeprecatedApp — missing app id
  OK    RNASeq — 14 inputs
  ...

Done: 44/45 tools generated in cwl/tools/
Report: cwl/REPORT.md
```

## Generated CWL Format

Each generated tool follows this structure:

```yaml
cwlVersion: v1.2
class: CommandLineTool

doc: "Genome Annotation — Annotate a genome using RASTtk"

hints:
  goweHint:
    bvbrc_app_id: GenomeAnnotation
    executor: bvbrc

baseCommand: [GenomeAnnotation]

inputs:
  contigs:
    type: File
    doc: "Input contigs file [bvbrc:wstype]"
  scientific_name:
    type: string
    doc: "Scientific name of the organism"
  taxonomy_id:
    type: int
    doc: "NCBI Taxonomy ID"
  output_path:
    type: Directory?
    doc: "Workspace folder for results (framework parameter) [bvbrc:folder]"
  output_file:
    type: string?
    doc: "Prefix for output file names (framework parameter)"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
```

## Type Mapping

BV-BRC parameter types are mapped to CWL types:

| BV-BRC Type | CWL Type | Notes |
|-------------|----------|-------|
| `string` | `string` | |
| `int`, `integer` | `int` | |
| `float`, `number` | `float` | |
| `boolean`, `bool` | `boolean` | |
| `folder` | `Directory` | Workspace path |
| `wstype` | `File` | Workspace file |
| `list`, `array` | `string[]` | |
| `group` | record array | Complex nested type |

### Group Parameters

Some BV-BRC apps use "group" parameters for complex inputs like paired-end read libraries. The generator recognizes these patterns and emits proper CWL record arrays:

```yaml
paired_end_libs:
  type:
    - "null"
    - type: array
      items:
        type: record
        name: paired_end_lib
        fields:
          - name: read1
            type: File
            doc: "Forward reads"
          - name: read2
            type: File?
            doc: "Reverse reads"
          - name: platform
            type: string?
            doc: "Sequencing platform"
            default: "infer"
          - name: interleaved
            type: boolean
            default: false
```

Supported group schemas:
- `paired_end_libs` / `paired_end_lib`
- `single_end_libs` / `single_end_lib`
- `srr_libs` / `srr_ids`
- `fasta_files`
- `sequences`

## Report Format

The generated `REPORT.md` provides detailed information for each tool:

```markdown
# BV-BRC CWL Tools Report

Generated: 2024-01-15

## Summary

- Total apps: 45
- Tools generated: 44

---

## GenomeAnnotation

**Label**: Genome Annotation
**Description**: Annotate a genome using RASTtk
**File**: `tools/GenomeAnnotation.cwl`

### Inputs (12 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| contigs | File | wstype | yes | — | Input contigs file |
| scientific_name | string | string | yes | — | Scientific name |
| taxonomy_id | int | int | yes | — | NCBI Taxonomy ID |
| domain | string | string | no | "Bacteria" | Domain [enum: Bacteria, Archaea] |
| ...

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | File[] | `$(inputs.output_path.location)/$(inputs.output_file)*` |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---
```

## Tutorial: Using Generated Tools in a Workflow

### 1. Generate the CWL tools

```bash
gen-cwl-tools --output-dir my-cwl-lib
```

### 2. Review the report

```bash
cat my-cwl-lib/REPORT.md
```

### 3. Create a workflow using the tools

Create `annotation-pipeline.cwl`:

```yaml
cwlVersion: v1.2
class: Workflow

inputs:
  contigs:
    type: File
    doc: Input assembly contigs
  scientific_name:
    type: string
  taxonomy_id:
    type: int
  output_folder:
    type: string
    default: "/user@patricbrc.org/home/results"

steps:
  annotate:
    run: my-cwl-lib/tools/GenomeAnnotation.cwl
    in:
      contigs: contigs
      scientific_name: scientific_name
      taxonomy_id: taxonomy_id
      output_path:
        valueFrom: |
          ${
            return {"class": "Directory", "location": inputs.output_folder};
          }
      output_file:
        valueFrom: "annotation_result"
    out: [result]

outputs:
  annotation_files:
    type: File[]
    outputSource: annotate/result
```

### 4. Submit via GoWe

```bash
gowe submit annotation-pipeline.cwl -i inputs.yaml
```

## Customizing Generated Tools

The generated tools provide a starting point. Common customizations:

### Add specific output files

Replace the generic glob with specific outputs:

```yaml
outputs:
  genome_gff:
    type: File
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file).gff
  genome_faa:
    type: File
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file).faa
```

### Add resource hints

```yaml
hints:
  goweHint:
    bvbrc_app_id: GenomeAnnotation
    executor: bvbrc
  ResourceRequirement:
    coresMin: 4
    ramMin: 8000
```

### Set default values

```yaml
inputs:
  domain:
    type: string
    default: "Bacteria"
    doc: "Domain [enum: Bacteria, Archaea]"
```

## Troubleshooting

### Token expired

```
fatal: BV-BRC token is expired (expiry: 2024-01-01T00:00:00Z)
```

Refresh your BV-BRC token:
```bash
gowe login
```

### Missing app parameters

Some apps may have undocumented or dynamic parameters. Check the BV-BRC web interface for complete parameter lists and update the generated tool manually.

### Network errors

```
fatal: enumerate_apps: connection refused
```

Check your network connectivity to BV-BRC services:
```bash
curl https://p3.theseed.org/services/app_service
```

### Enum values not recognized

Enum constraints are documented in the `doc` field but not enforced by CWL validation. Add explicit enum types if needed:

```yaml
inputs:
  domain:
    type:
      type: enum
      symbols: [Bacteria, Archaea]
    doc: "Domain"
```
