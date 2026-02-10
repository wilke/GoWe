# Common Workflow Language (CWL) Specification Reference

> Comprehensive reference for implementing a CWL engine in Go.
> Based on CWL v1.2 (current stable specification).
> Last updated: 2026-02-09

---

## Table of Contents

1. [CWL Overview](#1-cwl-overview)
2. [Core Concepts](#2-core-concepts)
3. [Specification Summary](#3-specification-summary)
4. [Best Practices for Implementation](#4-best-practices-for-implementation)
5. [Key Data Structures](#5-key-data-structures)
6. [Implementation Roadmap](#6-implementation-roadmap)

---

## 1. CWL Overview

### 1.1 What Is CWL?

The Common Workflow Language (CWL) is an open standard for describing command-line tool invocations and composing them into workflows. CWL documents are portable, declarative descriptions of computational pipelines that can be executed by any conformant engine regardless of the underlying platform.

CWL was designed for data-intensive scientific computing -- bioinformatics, medical imaging, astronomy, high-energy physics, and machine learning -- but is general-purpose enough for any command-line automation.

A CWL document does **not** contain executable code. It is a **description** of:
- What command-line tool to run and how to parameterize it.
- How multiple tools connect together via data flow (a directed acyclic graph).
- What computational resources, containers, and files are required.

### 1.2 Key Design Principles

| Principle | Description |
|-----------|-------------|
| **Portability** | Workflows run identically on laptops, clusters, cloud, and HPC. |
| **Reproducibility** | Deterministic execution via explicit inputs, outputs, and containers. |
| **Declarative** | Describes *what* to compute, not *how* to schedule or execute. |
| **Data-flow driven** | Steps execute when their input dependencies are satisfied (DAG). |
| **No side effects** | Tools read from designated inputs and write to designated outputs. |
| **Vendor neutral** | No lock-in; multiple independent engine implementations exist. |
| **Incremental complexity** | Simple tools need minimal boilerplate; advanced features are opt-in. |

### 1.3 Version History

| Version | Status | Key Changes |
|---------|--------|-------------|
| **v1.0** (v1.0.2) | Stable, legacy | Initial release. CommandLineTool, Workflow, ExpressionTool. |
| **v1.1** (v1.1.0) | Stable | Added `NetworkAccess`, `WorkReuse`, `InplaceUpdateRequirement`, `ToolTimeLimit`. Removed `$mixin`. |
| **v1.2** (v1.2.0 / v1.2.1) | **Current stable** | Added conditional execution (`when`), `pickValue`, fractional CPU cores, floating-point resource values, enhanced secondary files, `cwl.output.json` refinements. |

**This document focuses on CWL v1.2**, which is the authoritative specification for new engine implementations.

### 1.4 Specification Documents

The CWL v1.2 specification consists of three normative documents:

1. **CommandLineTool Description Standard** -- Schema and execution semantics for wrapping command-line tools.
2. **Workflow Description Standard** -- Schema and execution semantics for composing workflows from steps.
3. **Schema Salad Specification** -- The preprocessing and schema language used to load CWL documents.

### 1.5 Media Types

CWL documents use the following IANA media types:
- `application/cwl` (generic)
- `application/cwl+json` (JSON serialization)
- `application/cwl+yaml` (YAML serialization)

---

## 2. Core Concepts

### 2.1 Process Types (Classes)

CWL defines three process types, identified by the `class` field:

#### 2.1.1 CommandLineTool

Wraps a single command-line invocation. This is the fundamental building block.

```yaml
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [samtools, sort]

requirements:
  DockerRequirement:
    dockerPull: quay.io/biocontainers/samtools:1.17--h6fa5a28_1

inputs:
  input_bam:
    type: File
    inputBinding:
      position: 1
  output_name:
    type: string
    inputBinding:
      prefix: -o
      position: 2

outputs:
  sorted_bam:
    type: File
    outputBinding:
      glob: $(inputs.output_name)
```

**Execution flow:**
1. Validate inputs against declared types.
2. Check all requirements are satisfiable.
3. Stage input files into the working directory.
4. Build the command line from `baseCommand`, `arguments`, and input bindings.
5. Execute the command.
6. Capture stdout/stderr if configured.
7. Collect output files via `glob` patterns.
8. Validate outputs against declared types.

#### 2.1.2 Workflow

Composes multiple steps (each running a Process) into a directed acyclic graph (DAG) connected by data flow.

```yaml
cwlVersion: v1.2
class: Workflow

inputs:
  raw_reads:
    type: File
  reference_genome:
    type: File

steps:
  align:
    run: bwa-mem.cwl
    in:
      reads: raw_reads
      reference: reference_genome
    out: [aligned_bam]

  sort:
    run: samtools-sort.cwl
    in:
      input_bam: align/aligned_bam
      output_name:
        default: "sorted.bam"
    out: [sorted_bam]

  index:
    run: samtools-index.cwl
    in:
      input_bam: sort/sorted_bam
    out: [bam_index]

outputs:
  final_bam:
    type: File
    outputSource: sort/sorted_bam
  final_index:
    type: File
    outputSource: index/bam_index
```

**Key characteristics:**
- Steps execute when all inbound data links are satisfied.
- Independent steps MAY execute concurrently.
- The `source` field on step inputs references workflow inputs or other step outputs using `/` notation (e.g., `align/aligned_bam`).

#### 2.1.3 ExpressionTool

Executes a pure JavaScript expression (no command-line invocation). Requires `InlineJavascriptRequirement`. Useful for data transformation between steps.

```yaml
cwlVersion: v1.2
class: ExpressionTool

requirements:
  InlineJavascriptRequirement: {}

inputs:
  input_files:
    type: File[]
  prefix:
    type: string

expression: |
  ${
    var result = [];
    for (var i = 0; i < inputs.input_files.length; i++) {
      result.push({
        "class": "File",
        "location": inputs.input_files[i].location,
        "basename": inputs.prefix + "_" + inputs.input_files[i].basename
      });
    }
    return {"renamed_files": result};
  }

outputs:
  renamed_files:
    type: File[]
```

### 2.2 Type System

CWL has a rich type system. Every input and output parameter declares a type.

#### 2.2.1 Primitive Types (CWLType)

| Type | Description | Go Equivalent |
|------|-------------|---------------|
| `null` | No value / absence of value | `nil` |
| `boolean` | True or false | `bool` |
| `int` | 32-bit signed integer | `int32` |
| `long` | 64-bit signed integer | `int64` |
| `float` | Single-precision IEEE 754 | `float32` |
| `double` | Double-precision IEEE 754 | `float64` |
| `string` | Unicode string | `string` |

#### 2.2.2 Complex Types

| Type | Description |
|------|-------------|
| `File` | A file object with `location`, `path`, `basename`, `size`, `checksum`, `secondaryFiles`, `contents`, `format` |
| `Directory` | A directory object with `location`, `path`, `basename`, `listing` |
| `record` | A named struct with typed fields |
| `enum` | One of a fixed set of string symbols |
| `array` | An ordered list of items of a given type |
| `Any` | Any valid JSON/YAML value (used in hints) |

#### 2.2.3 Type DSL (Shorthand Notation)

CWL provides shorthand notation for common type patterns:

```yaml
# Optional type (union with null)
type: string?          # Expands to: [null, string]

# Array type
type: string[]         # Expands to: {type: array, items: string}

# Optional array
type: string[]?        # Expands to: [null, {type: array, items: string}]

# Union types (explicit)
type:
  - "null"
  - string
  - int
```

#### 2.2.4 File Object

The `File` type is central to CWL. Key fields:

```yaml
# File object structure
class: File
location: "file:///data/reads.fastq.gz"    # IRI (URI) to the file
path: "/data/reads.fastq.gz"               # Local filesystem path (computed at runtime)
basename: "reads.fastq.gz"                 # Filename portion
nameroot: "reads.fastq"                    # Basename without final extension
nameext: ".gz"                             # Final file extension
size: 1073741824                           # Size in bytes
checksum: "sha1$ab1c2d3e..."              # Checksum
format: "http://edamontology.org/format_1930"  # File format IRI
contents: null                             # File contents if loadContents is true (max 64 KiB)
secondaryFiles:                            # Associated files
  - class: File
    location: "file:///data/reads.fastq.gz.tbi"
```

**Secondary files** use pattern notation:
- `^` strips one file extension (e.g., `^.bai` turns `foo.bam` into `foo.bai`)
- Multiple `^` characters strip multiple extensions
- `?` suffix makes the secondary file optional (for inputs)

```yaml
inputs:
  indexed_bam:
    type: File
    secondaryFiles:
      - pattern: .bai    # Requires foo.bam.bai
      - pattern: ^.bai   # OR foo.bai (strips .bam, adds .bai)
        required: false   # This one is optional
```

#### 2.2.5 Directory Object

```yaml
class: Directory
location: "file:///data/reference/"
basename: "reference"
listing:                        # Contents (controlled by LoadListingRequirement)
  - class: File
    basename: "genome.fa"
  - class: Directory
    basename: "annotations"
```

#### 2.2.6 Record Types

Records are named struct types with typed fields:

```yaml
inputs:
  alignment_params:
    type:
      type: record
      name: alignment_params
      fields:
        threads:
          type: int
          inputBinding:
            prefix: -t
        min_quality:
          type: int
          inputBinding:
            prefix: -q
        output_format:
          type:
            type: enum
            name: output_format
            symbols: [sam, bam, cram]
          inputBinding:
            prefix: --output-fmt
```

#### 2.2.7 Enum Types

```yaml
inputs:
  compression_level:
    type:
      type: enum
      name: compression_level
      symbols:
        - none
        - fast
        - best
    inputBinding:
      prefix: --compression
```

### 2.3 Requirements and Hints

**Requirements** are features the engine MUST support for the process to execute. If the engine cannot satisfy a requirement, it MUST refuse to run the process.

**Hints** are identical in structure but are optional -- the engine SHOULD try to honor them but MAY ignore unsupported hints.

Requirements are inherited: a Workflow's requirements propagate to its steps. Step-level requirements override workflow-level requirements.

#### 2.3.1 Complete Requirements List (v1.2)

| Requirement | Purpose |
|-------------|---------|
| `InlineJavascriptRequirement` | Enables `$()` and `${}` JavaScript expressions |
| `SchemaDefRequirement` | Defines custom record/enum types inline |
| `DockerRequirement` | Specifies Docker/OCI container image |
| `SoftwareRequirement` | Declares software package dependencies |
| `InitialWorkDirRequirement` | Stages files/directories into the working directory |
| `EnvVarRequirement` | Sets environment variables |
| `ShellCommandRequirement` | Enables shell features (pipes, redirects) |
| `ResourceRequirement` | Declares CPU, RAM, disk requirements |
| `LoadListingRequirement` | Controls Directory listing depth |
| `WorkReuse` | Controls caching (v1.1+) |
| `NetworkAccess` | Allows network access during execution (v1.1+) |
| `InplaceUpdateRequirement` | Allows in-place file modification (v1.1+) |
| `ToolTimeLimit` | Sets execution time limit in seconds (v1.1+) |
| `SubworkflowFeatureRequirement` | Allows workflows as step `run` targets |
| `ScatterFeatureRequirement` | Enables scatter on workflow steps |
| `MultipleInputFeatureRequirement` | Enables `linkMerge` on step inputs |
| `StepInputExpressionRequirement` | Enables `valueFrom` on step inputs |

---

## 3. Specification Summary

### 3.1 Document Format

CWL documents are written in YAML (most common) or JSON. Before processing, CWL documents go through **Schema Salad preprocessing**:

1. **Field name resolution** -- Convert short names to full URIs.
2. **Identifier resolution** -- Expand relative IDs to absolute URIs.
3. **Link resolution** -- Resolve reference URIs.
4. **Vocabulary resolution** -- Map URIs to vocabulary terms.
5. **`$import` / `$include` processing** -- Load external documents/text.
6. **Identifier map transformation** -- Convert map notation to arrays.
7. **Type DSL expansion** -- Expand `?`, `[]` shorthand.
8. **Secondary files DSL expansion** -- Normalize secondary file patterns.

**Map notation** allows inputs, outputs, and steps to be written as maps (objects) instead of arrays:

```yaml
# Map notation (preferred, more readable):
inputs:
  message:
    type: string
  count:
    type: int

# Equivalent array notation:
inputs:
  - id: message
    type: string
  - id: count
    type: int
```

**Packed documents** use the `$graph` field to bundle multiple processes in a single file:

```yaml
$graph:
  - id: tool1
    class: CommandLineTool
    baseCommand: echo
    inputs:
      msg:
        type: string
        inputBinding:
          position: 1
    outputs: []

  - id: main
    class: Workflow
    inputs:
      message:
        type: string
    steps:
      step1:
        run: "#tool1"
        in:
          msg: message
        out: []
    outputs: []
```

The default entry point is `#main`. Fragment identifiers (e.g., `#tool1`) reference processes by `id`.

**External references**: The `$import` directive loads external CWL documents inline. The `$include` directive loads external text as a string.

```yaml
# Import a type definition
inputs:
  sample_info:
    type:
      $import: types/sample-record.yml

# Include a JavaScript library
requirements:
  InlineJavascriptRequirement:
    expressionLib:
      - $include: lib/helpers.js
```

### 3.2 Input/Output Binding

#### 3.2.1 CommandLineBinding

Controls how an input parameter translates to command-line arguments:

```yaml
baseCommand: [my-tool]

arguments:
  - position: 0
    prefix: --verbose
    # Arguments are always included (no associated input value)

inputs:
  threads:
    type: int
    inputBinding:
      position: 1
      prefix: -t              # Becomes: -t 4
      separate: true          # (default) space between prefix and value

  output_file:
    type: string
    inputBinding:
      position: 2
      prefix: --output=       # Becomes: --output=result.txt
      separate: false         # No space; concatenated

  input_files:
    type: string[]
    inputBinding:
      position: 3
      prefix: -i
      itemSeparator: ","     # Becomes: -i one,two,three
      separate: false

  enable_debug:
    type: boolean
    inputBinding:
      position: 4
      prefix: --debug         # Included only if true; omitted if false
```

**Binding rules by type:**

| Input Type | Binding Behavior |
|------------|-----------------|
| `string`, `int`, `long`, `float`, `double` | prefix + value (as string) |
| `boolean` | prefix only (if true); nothing (if false) |
| `File`, `Directory` | prefix + `self.path` |
| `array` | If `itemSeparator`: prefix + joined values. Otherwise: each element bound recursively. |
| `record` | Each field with `inputBinding` is bound recursively. |
| `null` | Nothing added to command line. |
| `enum` | prefix + symbol string |

**Position sorting**: Arguments are sorted by `position` (default 0). Ties are broken by field name alphabetically. `baseCommand` elements always come first.

#### 3.2.2 CommandOutputBinding

Controls how output files are discovered after execution:

```yaml
outputs:
  output_bam:
    type: File
    outputBinding:
      glob: "*.sorted.bam"    # Shell glob pattern in output directory

  stats_file:
    type: File
    outputBinding:
      glob: stats.txt
      loadContents: true       # Read first 64 KiB into .contents field

  line_count:
    type: int
    outputBinding:
      glob: stats.txt
      loadContents: true
      outputEval: $(parseInt(self[0].contents.trim()))
```

**Special output mechanism** -- `cwl.output.json`: If the tool creates a file named `cwl.output.json` in the output directory, the engine reads it and uses the JSON object as the output. Any `path` values are resolved relative to the output directory.

#### 3.2.3 Standard Streams

```yaml
class: CommandLineTool
baseCommand: wc
stdin: $(inputs.input_file.path)   # Pipe file to stdin
stdout: line_count.txt             # Capture stdout to file
stderr: errors.log                 # Capture stderr to file

inputs:
  input_file:
    type: File
  flags:
    type: string
    inputBinding:
      position: 1

outputs:
  count_output:
    type: File
    outputBinding:
      glob: line_count.txt
  error_log:
    type: File
    outputBinding:
      glob: errors.log
```

### 3.3 Steps and Connections

Workflow steps are connected via data flow. Each step input's `source` field references either a workflow input or another step's output.

```yaml
steps:
  step_a:
    run: tool_a.cwl
    in:
      input1: workflow_input_x           # From workflow input
    out: [output1, output2]

  step_b:
    run: tool_b.cwl
    in:
      input1: step_a/output1            # From step_a's output1
      input2: step_a/output2            # From step_a's output2
      param: workflow_input_y            # From workflow input
    out: [final_result]
```

#### 3.3.1 Multiple Input Sources and linkMerge

When a step input receives values from multiple sources, use `linkMerge`:

```yaml
requirements:
  MultipleInputFeatureRequirement: {}

steps:
  combine:
    run: merge-tool.cwl
    in:
      input_files:
        source:
          - step_a/output_file
          - step_b/output_file
          - step_c/output_file
        linkMerge: merge_flattened    # Flatten into single array
    out: [merged_output]
```

**linkMerge strategies:**

| Strategy | Behavior |
|----------|----------|
| `merge_nested` | Each source becomes an element in an outer array: `[[a], [b], [c]]` |
| `merge_flattened` | All sources flattened into a single array: `[a, b, c]` |

#### 3.3.2 pickValue (v1.2)

Filters null values from multiple sources (useful with conditional steps):

| Strategy | Behavior |
|----------|----------|
| `first_non_null` | Use the first non-null value |
| `the_only_non_null` | Require exactly one non-null value (error otherwise) |
| `all_non_null` | Collect all non-null values into an array |

```yaml
outputs:
  final_result:
    type: File
    outputSource:
      - conditional_step_a/result
      - conditional_step_b/result
    pickValue: first_non_null
```

#### 3.3.3 valueFrom on Step Inputs

Transforms input values using expressions (requires `StepInputExpressionRequirement`):

```yaml
requirements:
  StepInputExpressionRequirement: {}
  InlineJavascriptRequirement: {}

steps:
  process:
    run: tool.cwl
    in:
      output_name:
        source: input_file
        valueFrom: $(self.nameroot + ".processed.txt")
    out: [result]
```

### 3.4 Scatter/Gather

Scatter enables parallel execution of a step across elements of an array input.

#### 3.4.1 Simple Scatter (Single Input)

```yaml
requirements:
  ScatterFeatureRequirement: {}

steps:
  process_each:
    run: process-sample.cwl
    scatter: sample_file               # Scatter over this input
    in:
      sample_file: all_samples         # Array input
      reference: reference_genome      # Non-scattered (shared)
    out: [result]
    # If all_samples has 10 files, this step runs 10 times in parallel.
    # Output 'result' becomes an array of 10 results.
```

#### 3.4.2 Multi-Input Scatter

```yaml
requirements:
  ScatterFeatureRequirement: {}

steps:
  paired_process:
    run: paired-tool.cwl
    scatter: [read1, read2]
    scatterMethod: dotproduct          # Pair-wise: (read1[0], read2[0]), (read1[1], read2[1]), ...
    in:
      read1: forward_reads            # Array of N files
      read2: reverse_reads            # Array of N files (must match length)
    out: [aligned]
```

**scatterMethod options:**

| Method | Behavior | Output Shape |
|--------|----------|--------------|
| `dotproduct` | Element-wise pairing. Arrays must be same length. | Same length as inputs. |
| `nested_crossproduct` | Full Cartesian product. | Nested array: `[M][N]` for M x N inputs. |
| `flat_crossproduct` | Flattened Cartesian product. | Flat array of M*N elements. |

### 3.5 Conditional Execution (v1.2)

The `when` field on a step enables conditional execution based on a JavaScript expression:

```yaml
requirements:
  InlineJavascriptRequirement: {}

steps:
  maybe_compress:
    run: gzip.cwl
    when: $(inputs.should_compress)
    in:
      should_compress: do_compression   # boolean workflow input
      input_file: raw_output
    out: [compressed_file]
```

**Conditional execution rules:**
- The `when` expression MUST return a boolean.
- If `false`, the step is skipped and all outputs are `null`.
- Downstream steps receiving `null` from a skipped step should use `pickValue` to handle it.
- Available context: `inputs` (the step's resolved inputs).

```yaml
# Complete conditional workflow example
cwlVersion: v1.2
class: Workflow

requirements:
  InlineJavascriptRequirement: {}
  ScatterFeatureRequirement: {}
  MultipleInputFeatureRequirement: {}

inputs:
  input_file:
    type: File
  format:
    type:
      type: enum
      symbols: [gzip, bzip2, none]
      name: compression_format

steps:
  compress_gzip:
    run: gzip.cwl
    when: $(inputs.format_choice == "gzip")
    in:
      format_choice: format
      infile: input_file
    out: [outfile]

  compress_bzip2:
    run: bzip2.cwl
    when: $(inputs.format_choice == "bzip2")
    in:
      format_choice: format
      infile: input_file
    out: [outfile]

  passthrough:
    run: copy.cwl
    when: $(inputs.format_choice == "none")
    in:
      format_choice: format
      infile: input_file
    out: [outfile]

outputs:
  result:
    type: File
    outputSource:
      - compress_gzip/outfile
      - compress_bzip2/outfile
      - passthrough/outfile
    pickValue: first_non_null
```

### 3.6 Sub-Workflows

Steps can run entire workflows, not just CommandLineTools. Requires `SubworkflowFeatureRequirement`:

```yaml
requirements:
  SubworkflowFeatureRequirement: {}

steps:
  preprocessing:
    run: preprocess-workflow.cwl    # External workflow file
    in:
      raw_data: input_data
    out: [clean_data, qc_report]

  # Or inline:
  analysis:
    run:
      class: Workflow
      inputs:
        data:
          type: File
      steps:
        step1:
          run: tool1.cwl
          in:
            input: data
          out: [output]
      outputs:
        result:
          type: File
          outputSource: step1/output
    in:
      data: preprocessing/clean_data
    out: [result]
```

### 3.7 Docker / Container Support

The `DockerRequirement` specifies container images:

```yaml
requirements:
  DockerRequirement:
    dockerPull: ubuntu:22.04                    # Pull from registry
    # OR
    dockerLoad: path/to/image.tar.gz           # Load from archive
    # OR
    dockerFile: |                               # Build from Dockerfile
      FROM ubuntu:22.04
      RUN apt-get update && apt-get install -y samtools
    dockerImageId: my-samtools                  # Tag for built image
    dockerOutputDirectory: /data/output         # Override output directory path
```

**Container execution semantics:**
- Input files are mounted read-only into the container.
- The output directory is writable and mounted at a platform-specific path (or `dockerOutputDirectory`).
- `TMPDIR` points to a writable temporary directory.
- `HOME` is set to the output directory.
- Network access is disabled by default (enable with `NetworkAccess`).
- The engine chooses which container runtime to use (Docker, Singularity, Podman, etc.).

### 3.8 Resource Requirements

```yaml
requirements:
  ResourceRequirement:
    coresMin: 2           # Minimum CPU cores (can be fractional in v1.2: 0.5)
    coresMax: 8           # Maximum CPU cores
    ramMin: 4096          # Minimum RAM in MiB
    ramMax: 16384         # Maximum RAM in MiB
    tmpdirMin: 10240      # Minimum temp directory space in MiB
    tmpdirMax: 102400     # Maximum temp directory space in MiB
    outdirMin: 5120       # Minimum output directory space in MiB
    outdirMax: 51200      # Maximum output directory space in MiB
```

**Runtime object** (available in expressions):

```yaml
# In expressions, runtime provides actual allocated resources:
valueFrom: |
  ${
    // runtime.cores      - number of allocated CPU cores
    // runtime.ram        - allocated RAM in MiB
    // runtime.outdir     - path to output directory
    // runtime.tmpdir     - path to temp directory
    // runtime.outdirSize - allocated output dir space in MiB
    // runtime.tmpdirSize - allocated temp dir space in MiB
    return runtime.cores;
  }
```

### 3.9 JavaScript Expressions

CWL supports two forms of JavaScript, both requiring `InlineJavascriptRequirement`:

#### 3.9.1 Parameter References `$()`

Simple field access without full JavaScript:

```yaml
# Syntax: $(symbol.field[index]...)
outputBinding:
  glob: $(inputs.output_name)            # Simple field access
  glob: $(inputs.files[0].basename)      # Array index + field
  glob: $(runtime.outdir)                # Runtime context
```

Parameter reference grammar:
```
symbol    ::= [a-zA-Z_][a-zA-Z0-9_]*
segment   ::= .symbol | ["string"] | ['string'] | [integer]
paramref  ::= $( symbol segment* )
```

#### 3.9.2 JavaScript Expressions `$()`

Full JavaScript expressions (single expression, returns value):

```yaml
# Expression (returns a value)
valueFrom: $(inputs.count * 2 + 1)
```

#### 3.9.3 JavaScript Function Bodies `${}`

Multi-statement JavaScript (must explicitly `return`):

```yaml
# Function body (must return)
valueFrom: |
  ${
    var name = inputs.sample_name;
    var ext = inputs.format == "bam" ? ".bam" : ".sam";
    return name + ".sorted" + ext;
  }
```

#### 3.9.4 String Interpolation

When `$()` or `${}` appears within a string with other text, the result is converted to a string and interpolated:

```yaml
arguments:
  - valueFrom: "output_$(inputs.sample_id).bam"
    position: 2
```

**Escaping:**
- `\$(` produces literal `$(` (no evaluation)
- `\${` produces literal `${` (no evaluation)
- `\\` produces literal `\`

#### 3.9.5 Expression Context

All expressions have access to:

| Variable | Type | Description |
|----------|------|-------------|
| `inputs` | object | All resolved input values |
| `self` | varies | Context-dependent (current value being processed) |
| `runtime` | object | Runtime resources: `outdir`, `tmpdir`, `cores`, `ram`, `outdirSize`, `tmpdirSize` |
| `runtime.exitCode` | int | Exit code (only in `outputEval` expressions) |

#### 3.9.6 expressionLib

Pre-load JavaScript libraries available to all expressions in the document:

```yaml
requirements:
  InlineJavascriptRequirement:
    expressionLib:
      - |
        function resolveSecondary(base, suffix) {
          var root = base.replace(/\.[^.]+$/, '');
          return root + suffix;
        }
      - $include: lib/utils.js
```

### 3.10 File Handling and Staging

#### 3.10.1 InitialWorkDirRequirement

Stages files/directories into the working directory before execution:

```yaml
requirements:
  InitialWorkDirRequirement:
    listing:
      # Copy an input file into the working directory
      - entry: $(inputs.config_file)
        entryname: config.ini
        writable: false

      # Create a file from a string expression
      - entryname: run_params.txt
        entry: |
          THREADS=$(inputs.threads)
          MEMORY=$(inputs.memory)

      # Create a file using Dirent
      - entryname: script.sh
        entry: |
          #!/bin/bash
          echo "Processing $(inputs.sample_id)"
        writable: true

      # Stage an entire directory
      - entry: $(inputs.reference_dir)
        writable: false
```

**Key rules:**
- `writable: false` (default) -- files are read-only (may be symlinked/hardlinked).
- `writable: true` -- files must be actual copies (modifications don't affect originals).
- `entryname` can be an absolute path when running inside a container (v1.2).
- Existing files in the output directory are not overwritten unless explicitly staged.

#### 3.10.2 LoadListingRequirement

Controls how Directory objects load their listing:

```yaml
requirements:
  LoadListingRequirement:
    loadListing: shallow_listing   # or: no_listing, deep_listing
```

| Mode | Behavior |
|------|----------|
| `no_listing` | `listing` field is empty/absent. |
| `shallow_listing` | One level of files and subdirectories. |
| `deep_listing` | Recursive listing of all contents. |

#### 3.10.3 loadContents

On input parameters, `loadContents: true` reads the first 64 KiB of the file into the `contents` field, making it available in expressions:

```yaml
inputs:
  config:
    type: File
    loadContents: true
    inputBinding:
      valueFrom: $(JSON.parse(self.contents).parameter_x)
```

### 3.11 Environment Variables

```yaml
requirements:
  EnvVarRequirement:
    envDef:
      OMP_NUM_THREADS: $(runtime.cores)
      TEMP: $(runtime.tmpdir)
      CUSTOM_VAR: "some_value"
```

**Default environment:**
- `HOME` = output directory
- `TMPDIR` = temporary directory
- `PATH` = inherited from host or container default

### 3.12 Exit Codes

```yaml
class: CommandLineTool
baseCommand: my-tool

successCodes: [0, 1]          # These exit codes mean success
temporaryFailCodes: [2]       # Retryable failure
permanentFailCodes: [3, 4]    # Unrecoverable failure (default: any code not in successCodes)
```

Default behavior: exit code 0 = success, any other code = permanent failure.

### 3.13 Shell Command Requirement

Enables shell features (pipes, redirects, variables):

```yaml
requirements:
  ShellCommandRequirement: {}

arguments:
  - valueFrom: "samtools view -bS - | samtools sort -"
    shellQuote: false    # Don't escape shell metacharacters

inputs:
  input_sam:
    type: File
    inputBinding:
      position: 1
      # shellQuote defaults to true (safe for user inputs)
```

When `ShellCommandRequirement` is active:
- The command is executed via `/bin/sh -c "..."`.
- `shellQuote: true` (default) wraps the value in single quotes.
- `shellQuote: false` passes the value directly (allows pipes, redirects).

---

## 4. Best Practices for Implementation

### 4.1 Building a CWL Runner/Engine

A CWL engine must implement these core capabilities in roughly this order:

#### Phase 1: Document Loading

1. **Parse YAML/JSON** -- Read the raw document.
2. **Schema Salad preprocessing** -- Apply the preprocessing pipeline:
   - Resolve `$import` and `$include` directives.
   - Expand map notation (inputs/outputs/steps as objects to arrays with `id` fields).
   - Expand type DSL (`?`, `[]`).
   - Resolve identifiers and links.
3. **Determine document class** -- Read the `class` field to dispatch to the correct handler.
4. **Validate against schema** -- Check required fields, type correctness, and constraint satisfaction.
5. **Resolve `run` references** -- For workflow steps, load referenced CWL files.

**Implementation tip:** Build a `Loader` that handles Schema Salad preprocessing as a separate, reusable component. This is complex but foundational.

```go
// Pseudocode for document loading
func LoadCWLDocument(path string) (Process, error) {
    raw, err := readYAMLorJSON(path)
    if err != nil {
        return nil, err
    }

    // Schema Salad preprocessing
    processed, err := preprocess(raw, filepath.Dir(path))
    if err != nil {
        return nil, err
    }

    // Dispatch by class
    class, _ := processed["class"].(string)
    switch class {
    case "CommandLineTool":
        return parseCommandLineTool(processed)
    case "Workflow":
        return parseWorkflow(processed)
    case "ExpressionTool":
        return parseExpressionTool(processed)
    default:
        return nil, fmt.Errorf("unknown class: %s", class)
    }
}
```

#### Phase 2: Input Resolution

1. **Read input object** -- Parse the job/input YAML/JSON file.
2. **Type check** -- Validate each input against its declared type.
3. **Apply defaults** -- Fill in missing optional inputs with `default` values.
4. **Resolve File/Directory** -- Convert `location` URIs to local `path` values. Download remote files.
5. **Resolve secondary files** -- Apply `secondaryFiles` patterns to locate associated files.
6. **Load contents** -- If `loadContents: true`, read file contents (max 64 KiB).
7. **Load listings** -- For Directory inputs, populate `listing` per `LoadListingRequirement`.

#### Phase 3: Executing CommandLineTools

1. **Check requirements** -- Verify all requirements can be satisfied.
2. **Prepare execution environment:**
   - Create output directory and temp directory.
   - Stage files per `InitialWorkDirRequirement`.
   - Set environment variables per `EnvVarRequirement`.
3. **Build command line:**
   - Start with `baseCommand` elements.
   - Evaluate and sort `arguments` entries.
   - For each input with `inputBinding`, compute the argument(s).
   - Sort all arguments by `position`, then alphabetically by field name.
   - Apply `prefix`, `separate`, `itemSeparator`, `shellQuote`.
4. **Set up I/O streams** (stdin, stdout, stderr).
5. **Execute:**
   - Without container: `exec` or `os/exec` in Go.
   - With Docker: `docker run` with appropriate volume mounts.
   - With Shell: wrap in `/bin/sh -c "..."`.
6. **Check exit code** against `successCodes`, `temporaryFailCodes`, `permanentFailCodes`.
7. **Collect outputs:**
   - Check for `cwl.output.json`.
   - Apply `glob` patterns to find output files.
   - Apply `loadContents` and `outputEval` expressions.
   - Validate outputs against declared types.

**Command-line building algorithm:**

```go
// Pseudocode for command line construction
func BuildCommandLine(tool *CommandLineTool, inputs map[string]interface{}) []string {
    var args []CommandLineElement

    // 1. Base command
    for i, cmd := range tool.BaseCommand {
        args = append(args, CommandLineElement{
            Position: -1000000 + i,  // Always first
            Value:    cmd,
        })
    }

    // 2. Arguments
    for _, arg := range tool.Arguments {
        position := arg.Position  // default 0
        value := evaluateBinding(arg, nil, inputs)
        args = append(args, CommandLineElement{Position: position, Value: value})
    }

    // 3. Input bindings
    for _, input := range tool.Inputs {
        if input.InputBinding == nil {
            continue
        }
        value := inputs[input.ID]
        elements := bindInput(input, value)
        args = append(args, elements...)
    }

    // 4. Sort by position, then by field name
    sort.SliceStable(args, func(i, j int) bool {
        if args[i].Position != args[j].Position {
            return args[i].Position < args[j].Position
        }
        return args[i].FieldName < args[j].FieldName
    })

    // 5. Flatten to string slice
    return flatten(args)
}
```

#### Phase 4: Managing Workflow DAGs

1. **Parse workflow** -- Extract steps, inputs, outputs, and their connections.
2. **Build dependency graph** -- For each step, determine which other steps or workflow inputs it depends on.
3. **Topological sort** -- Validate the graph is a DAG (no cycles).
4. **Execute steps:**
   - Maintain a set of "ready" steps (all dependencies satisfied).
   - Execute ready steps (potentially in parallel).
   - When a step completes, propagate its outputs to downstream steps.
   - Check if new steps become ready.
   - Repeat until all steps complete or a failure occurs.
5. **Handle scatter** -- For scattered steps, expand into parallel sub-executions and gather results.
6. **Handle conditionals** -- Evaluate `when` expressions; skip steps that evaluate to false.
7. **Collect workflow outputs** -- Resolve `outputSource` references, apply `linkMerge` and `pickValue`.

```go
// Pseudocode for workflow execution
func ExecuteWorkflow(wf *Workflow, inputs map[string]interface{}) (map[string]interface{}, error) {
    state := NewWorkflowState(wf, inputs)

    for !state.AllStepsComplete() {
        readySteps := state.GetReadySteps()
        if len(readySteps) == 0 && !state.AllStepsComplete() {
            return nil, fmt.Errorf("deadlock: no steps ready but workflow not complete")
        }

        // Execute ready steps (potentially in parallel)
        var wg sync.WaitGroup
        for _, step := range readySteps {
            wg.Add(1)
            go func(s *WorkflowStep) {
                defer wg.Done()

                // Check conditional
                if s.When != "" {
                    shouldRun, err := evaluateWhen(s, state)
                    if err != nil {
                        state.SetStepFailed(s, err)
                        return
                    }
                    if !shouldRun {
                        state.SetStepSkipped(s) // All outputs become null
                        return
                    }
                }

                // Handle scatter
                if s.Scatter != nil {
                    results, err := executeScatter(s, state)
                    // ...
                } else {
                    result, err := executeStep(s, state)
                    // ...
                }
            }(step)
        }
        wg.Wait()
    }

    return state.CollectOutputs()
}
```

### 4.2 Container Integration

For Docker-based execution:

```go
// Pseudocode for Docker execution
func ExecuteInDocker(tool *CommandLineTool, req DockerRequirement,
    inputMappings []FileMapping, cmdLine []string) (int, error) {

    args := []string{"run", "--rm"}

    // Resource limits
    if tool.ResourceReq != nil {
        if tool.ResourceReq.CoresMax > 0 {
            args = append(args, fmt.Sprintf("--cpus=%g", tool.ResourceReq.CoresMax))
        }
        if tool.ResourceReq.RamMax > 0 {
            args = append(args, fmt.Sprintf("--memory=%dm", tool.ResourceReq.RamMax))
        }
    }

    // Network access
    if !hasNetworkAccess(tool) {
        args = append(args, "--network=none")
    }

    // Mount input files (read-only)
    for _, m := range inputMappings {
        args = append(args, "-v", fmt.Sprintf("%s:%s:ro", m.HostPath, m.ContainerPath))
    }

    // Mount output directory (read-write)
    args = append(args, "-v", fmt.Sprintf("%s:%s:rw", outputDir, containerOutputDir))

    // Mount temp directory
    args = append(args, "-v", fmt.Sprintf("%s:%s:rw", tmpDir, containerTmpDir))

    // Environment
    args = append(args, "-e", "HOME="+containerOutputDir)
    args = append(args, "-e", "TMPDIR="+containerTmpDir)

    // Working directory
    args = append(args, "-w", containerOutputDir)

    // Image
    args = append(args, req.DockerPull)

    // Command
    args = append(args, cmdLine...)

    cmd := exec.Command("docker", args...)
    // ... execute and capture output
}
```

### 4.3 JavaScript Expression Evaluation

For a Go engine, JavaScript evaluation requires an embedded JS runtime. Options:

1. **goja** -- Pure Go JavaScript engine (ECMAScript 5.1). Recommended for simplicity.
2. **v8go** -- V8 bindings for Go. Higher performance but CGo dependency.
3. **External process** -- Spawn a Node.js process. Slower but maximum compatibility.

```go
// Pseudocode using goja for expression evaluation
import "github.com/dop251/goja"

func EvaluateExpression(expr string, inputs map[string]interface{},
    self interface{}, runtime RuntimeContext) (interface{}, error) {

    vm := goja.New()

    // Set up context
    vm.Set("inputs", inputs)
    vm.Set("self", self)
    vm.Set("runtime", map[string]interface{}{
        "outdir":     runtime.Outdir,
        "tmpdir":     runtime.Tmpdir,
        "cores":      runtime.Cores,
        "ram":        runtime.Ram,
        "outdirSize": runtime.OutdirSize,
        "tmpdirSize": runtime.TmpdirSize,
    })

    // Load expression libraries
    for _, lib := range expressionLibs {
        if _, err := vm.RunString(lib); err != nil {
            return nil, fmt.Errorf("loading expression lib: %w", err)
        }
    }

    // Determine expression type
    if strings.HasPrefix(expr, "${") {
        // Function body -- wrap in IIFE
        script := "(function() {" + expr[2:len(expr)-1] + "})()"
        val, err := vm.RunString(script)
        return val.Export(), err
    } else if strings.HasPrefix(expr, "$(") {
        // Expression -- evaluate directly
        script := expr[2 : len(expr)-1]
        val, err := vm.RunString(script)
        return val.Export(), err
    }

    // String interpolation -- find and replace $() and ${} segments
    return interpolateString(vm, expr)
}
```

### 4.4 Error Handling

CWL defines three process states:

| State | Meaning | Engine Action |
|-------|---------|---------------|
| `success` | Process completed successfully | Continue workflow |
| `temporaryFailure` | Transient error (may retry) | Retry or propagate |
| `permanentFailure` | Unrecoverable error | Fail workflow |

**Workflow failure semantics:**
- If ANY step has `permanentFailure`, the workflow fails permanently.
- If one or more steps have `temporaryFailure` and all others succeed or are skipped, the workflow has `temporaryFailure`.
- Only if ALL steps succeed does the workflow succeed.

**Implementation considerations:**
- Exit codes determine step success/failure (configurable per tool).
- Expression evaluation errors are `permanentFailure`.
- Resource exhaustion (disk, memory) should be `temporaryFailure`.
- Missing required inputs should be caught during validation (before execution).

### 4.5 Conformance Testing

The CWL project provides an official conformance test suite. Use it to validate your engine:

```bash
# Install the conformance test runner
pip install cwltest

# Run conformance tests against your engine
cwltest --test conformance_tests.yaml --tool /path/to/your-engine

# Run specific test classes
cwltest --test conformance_tests.yaml --tool /path/to/your-engine \
    --tags command_line_tool docker scatter conditional
```

**Conformance test categories include:**
- `command_line_tool` -- Basic CommandLineTool execution
- `docker` -- Container support
- `expression_tool` -- ExpressionTool
- `workflow` -- Basic workflow execution
- `scatter` -- Scatter/gather
- `conditional` -- Conditional execution (when)
- `subworkflow` -- Sub-workflow support
- `shell_command` -- ShellCommandRequirement
- `initial_work_dir` -- InitialWorkDirRequirement
- `inline_javascript` -- JavaScript expressions
- `resource` -- ResourceRequirement
- `schema_def` -- SchemaDefRequirement
- `multiple_input` -- MultipleInputFeatureRequirement
- `step_input_expression` -- StepInputExpressionRequirement
- `env_var` -- EnvVarRequirement
- `secondary_files` -- Secondary files handling

---

## 5. Key Data Structures

The following Go-style struct definitions represent the main CWL schema objects. These are the data structures you need to implement for a CWL engine.

### 5.1 Top-Level Process Types

```go
// Process is the interface implemented by all CWL process types.
type Process interface {
    GetClass() string
    GetID() string
    GetInputs() []InputParameter
    GetOutputs() []OutputParameter
    GetRequirements() []Requirement
    GetHints() []Requirement
    GetCWLVersion() string
}

// CommandLineTool wraps a single command-line invocation.
type CommandLineTool struct {
    Class        string                  // Must be "CommandLineTool"
    ID           string                  // Optional unique identifier
    CWLVersion   string                  // e.g., "v1.2"
    Label        string                  // Human-readable label
    Doc          string                  // Documentation string or []string
    Intent       []string                // IRIs for tool purpose/intent

    BaseCommand  []string                // Base command (string or array of strings)
    Arguments    []CommandLineBinding     // Additional command-line arguments
    Inputs       []CommandInputParameter  // Input parameters
    Outputs      []CommandOutputParameter // Output parameters

    Requirements []Requirement           // Required features
    Hints        []Requirement           // Optional features

    Stdin        Expression              // Expression or string for stdin source
    Stdout       Expression              // Expression or string for stdout capture filename
    Stderr       Expression              // Expression or string for stderr capture filename

    SuccessCodes       []int             // Exit codes indicating success (default: [0])
    TemporaryFailCodes []int             // Exit codes indicating temporary failure
    PermanentFailCodes []int             // Exit codes indicating permanent failure
}

// Workflow composes multiple steps into a DAG.
type Workflow struct {
    Class        string                  // Must be "Workflow"
    ID           string                  // Optional unique identifier
    CWLVersion   string                  // e.g., "v1.2"
    Label        string                  // Human-readable label
    Doc          string                  // Documentation string

    Inputs       []WorkflowInputParameter  // Workflow-level inputs
    Outputs      []WorkflowOutputParameter // Workflow-level outputs
    Steps        []WorkflowStep            // Workflow steps

    Requirements []Requirement           // Required features
    Hints        []Requirement           // Optional features
}

// ExpressionTool executes a JavaScript expression without a command line.
type ExpressionTool struct {
    Class        string                  // Must be "ExpressionTool"
    ID           string                  // Optional unique identifier
    CWLVersion   string                  // e.g., "v1.2"
    Label        string                  // Human-readable label
    Doc          string                  // Documentation string

    Inputs       []InputParameter        // Input parameters
    Outputs      []OutputParameter       // Output parameters
    Expression   string                  // JavaScript expression

    Requirements []Requirement           // Required features
    Hints        []Requirement           // Optional features
}
```

### 5.2 Input/Output Parameters

```go
// CommandInputParameter defines a CommandLineTool input.
type CommandInputParameter struct {
    ID             string               // Parameter identifier (required)
    Label          string               // Human-readable label
    Doc            string               // Documentation
    Type           CWLType              // Parameter type (required)
    Default        interface{}          // Default value if not provided
    InputBinding   *CommandLineBinding  // How to bind to command line
    SecondaryFiles []SecondaryFileSchema // Associated files
    Format         Expression           // Expected file format (IRI or expression)
    Streamable     bool                 // Whether file can be streamed
    LoadContents   bool                 // Load file contents (max 64 KiB)
    LoadListing    LoadListingEnum      // Directory listing mode
}

// CommandOutputParameter defines a CommandLineTool output.
type CommandOutputParameter struct {
    ID             string               // Parameter identifier (required)
    Label          string               // Human-readable label
    Doc            string               // Documentation
    Type           CWLType              // Parameter type (required)
    OutputBinding  *CommandOutputBinding // How to discover output
    SecondaryFiles []SecondaryFileSchema // Associated output files
    Format         Expression           // Output file format
    Streamable     bool                 // Whether file can be streamed
}

// WorkflowInputParameter defines a Workflow input.
type WorkflowInputParameter struct {
    ID             string               // Parameter identifier (required)
    Label          string               // Human-readable label
    Doc            string               // Documentation
    Type           CWLType              // Parameter type (required)
    Default        interface{}          // Default value
    SecondaryFiles []SecondaryFileSchema // Associated files
    Format         Expression           // Expected file format
    Streamable     bool                 // Whether file can be streamed
    LoadContents   bool                 // Load file contents
    LoadListing    LoadListingEnum      // Directory listing mode
}

// WorkflowOutputParameter defines a Workflow output.
type WorkflowOutputParameter struct {
    ID             string               // Parameter identifier (required)
    Label          string               // Human-readable label
    Doc            string               // Documentation
    Type           CWLType              // Parameter type (required)
    OutputSource   StringOrSlice        // Source step/input reference(s)
    LinkMerge      LinkMergeMethod      // How to merge multiple sources
    PickValue      PickValueMethod      // How to pick from null/non-null values (v1.2)
    SecondaryFiles []SecondaryFileSchema // Associated files
    Format         Expression           // Output file format
    Streamable     bool                 // Whether file can be streamed
}
```

### 5.3 Workflow Steps

```go
// WorkflowStep defines a single step in a Workflow.
type WorkflowStep struct {
    ID             string               // Step identifier (required)
    Label          string               // Human-readable label
    Doc            string               // Documentation
    Run            ProcessOrRef         // Process to execute (inline or file reference)
    In             []WorkflowStepInput  // Step input bindings
    Out            []WorkflowStepOutput // Step output declarations
    When           Expression           // Conditional execution expression (v1.2)
    Scatter        StringOrSlice        // Input(s) to scatter over
    ScatterMethod  ScatterMethod        // How to combine scattered inputs
    Requirements   []Requirement        // Step-level requirements
    Hints          []Requirement        // Step-level hints
}

// WorkflowStepInput connects a workflow input or step output to a step input.
type WorkflowStepInput struct {
    ID        string            // Input parameter ID
    Source    StringOrSlice     // Source reference(s): workflow input or step/output
    LinkMerge LinkMergeMethod   // Merge strategy for multiple sources
    PickValue PickValueMethod   // Pick strategy for null values (v1.2)
    Default   interface{}       // Default value if source is missing
    ValueFrom Expression        // Expression to transform the value
}

// WorkflowStepOutput declares an output produced by a step.
type WorkflowStepOutput struct {
    ID string                   // Output parameter ID
}

// ScatterMethod determines how multiple scattered inputs combine.
type ScatterMethod string

const (
    ScatterDotProduct          ScatterMethod = "dotproduct"
    ScatterNestedCrossProduct  ScatterMethod = "nested_crossproduct"
    ScatterFlatCrossProduct    ScatterMethod = "flat_crossproduct"
)

// LinkMergeMethod determines how multiple input sources merge.
type LinkMergeMethod string

const (
    LinkMergeNested    LinkMergeMethod = "merge_nested"
    LinkMergeFlattened LinkMergeMethod = "merge_flattened"
)

// PickValueMethod determines how null values are handled (v1.2).
type PickValueMethod string

const (
    PickFirstNonNull   PickValueMethod = "first_non_null"
    PickTheOnlyNonNull PickValueMethod = "the_only_non_null"
    PickAllNonNull     PickValueMethod = "all_non_null"
)
```

### 5.4 Command Line Binding

```go
// CommandLineBinding controls how a value becomes command-line arguments.
type CommandLineBinding struct {
    Position      ExpressionOrInt      // Sort key for argument ordering (default: 0)
    Prefix        string               // String prepended to the value
    Separate      *bool                // Whether prefix and value are separate args (default: true)
    ItemSeparator string               // For arrays: join elements with this separator
    ValueFrom     Expression           // Expression or constant providing the value
    ShellQuote    *bool                // Shell quoting control (default: true)
    LoadContents  bool                 // Load file contents for use in valueFrom
}

// CommandOutputBinding controls how output files are discovered.
type CommandOutputBinding struct {
    Glob         ExpressionOrSlice    // Glob pattern(s) to match output files
    LoadContents bool                 // Read file contents into .contents (max 64 KiB)
    OutputEval   Expression           // Expression to compute final output value
}
```

### 5.5 Type System

```go
// CWLType represents any CWL type. It is a discriminated union.
// In practice, this is best represented as an interface or a variant type.
type CWLType struct {
    // Exactly one of these fields is set:
    Null       bool                  // null type
    Primitive  PrimitiveType         // boolean, int, long, float, double, string
    FileType   bool                  // File
    DirType    bool                  // Directory
    AnyType    bool                  // Any
    Record     *RecordSchema         // Record type definition
    Enum       *EnumSchema           // Enum type definition
    Array      *ArraySchema          // Array type definition
    Union      []CWLType             // Union of types (e.g., [null, string])
    Name       string                // Named type reference (from SchemaDefRequirement)
}

// PrimitiveType enumerates CWL primitive types.
type PrimitiveType string

const (
    TypeBoolean PrimitiveType = "boolean"
    TypeInt     PrimitiveType = "int"
    TypeLong    PrimitiveType = "long"
    TypeFloat   PrimitiveType = "float"
    TypeDouble  PrimitiveType = "double"
    TypeString  PrimitiveType = "string"
)

// RecordSchema defines a record (struct) type.
type RecordSchema struct {
    Name   string               // Type name
    Fields []RecordField        // Record fields
    Type   string               // Must be "record"
}

// RecordField defines a single field in a record type.
type RecordField struct {
    Name         string              // Field name
    Type         CWLType             // Field type
    Doc          string              // Documentation
    InputBinding *CommandLineBinding // How the field binds to command line
}

// EnumSchema defines an enumeration type.
type EnumSchema struct {
    Name    string               // Type name
    Symbols []string             // Allowed values
    Type    string               // Must be "enum"
    InputBinding *CommandLineBinding // Binding for the enum value
}

// ArraySchema defines an array type.
type ArraySchema struct {
    Items        CWLType             // Element type
    Type         string              // Must be "array"
    InputBinding *CommandLineBinding // Binding for array elements
}
```

### 5.6 File and Directory Objects

```go
// CWLFile represents a CWL File object.
type CWLFile struct {
    Class          string           // Must be "File"
    Location       string           // IRI/URI to the file (primary identifier)
    Path           string           // Local filesystem path (resolved at runtime)
    Basename       string           // Filename part of the path
    Nameroot       string           // Basename without final extension
    Nameext        string           // Final file extension (including dot)
    Dirname        string           // Directory part of the path
    Size           int64            // File size in bytes
    Checksum       string           // Hash: "sha1$..." format
    Contents       *string          // File content (if loadContents, max 64 KiB)
    Format         string           // File format IRI
    SecondaryFiles []FileOrDirectory // Associated secondary files
}

// CWLDirectory represents a CWL Directory object.
type CWLDirectory struct {
    Class    string           // Must be "Directory"
    Location string           // IRI/URI to the directory
    Path     string           // Local filesystem path (resolved at runtime)
    Basename string           // Directory name
    Listing  []FileOrDirectory // Directory contents (depth per LoadListingRequirement)
}

// FileOrDirectory is a union type for File or Directory.
type FileOrDirectory interface {
    IsFile() bool
    IsDirectory() bool
}

// SecondaryFileSchema defines how to find associated files.
type SecondaryFileSchema struct {
    Pattern  Expression       // Pattern or expression for secondary file path
    Required ExpressionOrBool // Whether the secondary file must exist (default: true for inputs)
}
```

### 5.7 Requirements

```go
// Requirement is a tagged union of all CWL requirement types.
// Each requirement type is identified by its "class" field.

type InlineJavascriptRequirement struct {
    Class         string     // "InlineJavascriptRequirement"
    ExpressionLib []string   // JavaScript libraries to preload
}

type DockerRequirement struct {
    Class                string // "DockerRequirement"
    DockerPull           string // Image name to pull (e.g., "ubuntu:22.04")
    DockerLoad           string // Path to Docker image archive
    DockerFile           string // Dockerfile content to build
    DockerImport         string // URL to import as Docker image
    DockerImageID        string // Image ID for built/imported images
    DockerOutputDirectory string // Override output directory path in container
}

type ResourceRequirement struct {
    Class      string          // "ResourceRequirement"
    CoresMin   ExpressionOrFloat // Min CPU cores (can be fractional in v1.2)
    CoresMax   ExpressionOrFloat // Max CPU cores
    RamMin     ExpressionOrFloat // Min RAM in MiB
    RamMax     ExpressionOrFloat // Max RAM in MiB
    TmpdirMin  ExpressionOrFloat // Min temp space in MiB
    TmpdirMax  ExpressionOrFloat // Max temp space in MiB
    OutdirMin  ExpressionOrFloat // Min output space in MiB
    OutdirMax  ExpressionOrFloat // Max output space in MiB
}

type InitialWorkDirRequirement struct {
    Class   string        // "InitialWorkDirRequirement"
    Listing []Dirent      // Files/directories to stage or create
    // Listing can also be an expression returning []Dirent
}

type Dirent struct {
    Entryname Expression   // Target filename (or expression)
    Entry     Expression   // Source file/content (File, Directory, string, or expression)
    Writable  bool         // If true, create a copy (not a symlink)
}

type EnvVarRequirement struct {
    Class  string           // "EnvVarRequirement"
    EnvDef []EnvironmentDef // Environment variable definitions
}

type EnvironmentDef struct {
    EnvName  string         // Variable name
    EnvValue Expression     // Variable value (string or expression)
}

type ShellCommandRequirement struct {
    Class string             // "ShellCommandRequirement"
}

type SchemaDefRequirement struct {
    Class string             // "SchemaDefRequirement"
    Types []CWLType          // Custom type definitions (record and enum only)
}

type SoftwareRequirement struct {
    Class    string            // "SoftwareRequirement"
    Packages []SoftwarePackage // Software package declarations
}

type SoftwarePackage struct {
    Package  string    // Package name
    Version  []string  // Acceptable version(s)
    Specs    []string  // IRIs for package specification
}

type LoadListingRequirement struct {
    Class       string          // "LoadListingRequirement"
    LoadListing LoadListingEnum // no_listing | shallow_listing | deep_listing
}

type LoadListingEnum string

const (
    NoListing      LoadListingEnum = "no_listing"
    ShallowListing LoadListingEnum = "shallow_listing"
    DeepListing    LoadListingEnum = "deep_listing"
)

type WorkReuse struct {
    Class         string         // "WorkReuse"
    EnableReuse   ExpressionOrBool // Whether to enable caching (default: true)
}

type NetworkAccess struct {
    Class         string         // "NetworkAccess"
    NetworkAccess ExpressionOrBool // Whether to allow network (default: false)
}

type InplaceUpdateRequirement struct {
    Class               string // "InplaceUpdateRequirement"
    InplaceUpdate       bool   // Whether to allow in-place file updates
}

type ToolTimeLimit struct {
    Class    string              // "ToolTimeLimit"
    Timelimit ExpressionOrInt   // Time limit in seconds
}

type SubworkflowFeatureRequirement struct {
    Class string // "SubworkflowFeatureRequirement"
}

type ScatterFeatureRequirement struct {
    Class string // "ScatterFeatureRequirement"
}

type MultipleInputFeatureRequirement struct {
    Class string // "MultipleInputFeatureRequirement"
}

type StepInputExpressionRequirement struct {
    Class string // "StepInputExpressionRequirement"
}
```

### 5.8 Runtime Context

```go
// RuntimeContext holds the runtime information available in expressions.
type RuntimeContext struct {
    Outdir     string  // Absolute path to output directory
    Tmpdir     string  // Absolute path to temp directory
    Cores      float64 // Allocated CPU cores
    Ram        float64 // Allocated RAM in MiB
    OutdirSize float64 // Allocated output dir space in MiB
    TmpdirSize float64 // Allocated temp dir space in MiB
    ExitCode   *int    // Exit code (only in outputEval context)
}
```

### 5.9 Utility / Helper Types

```go
// Expression can be a string literal, a parameter reference $(), or a JS expression ${}.
// During parsing, you may want to distinguish these:
type Expression string

// ExpressionOrInt is a field that accepts either an integer or an expression.
type ExpressionOrInt struct {
    IntValue *int
    Expr     Expression
}

// ExpressionOrFloat is a field that accepts either a float or an expression.
type ExpressionOrFloat struct {
    FloatValue *float64
    Expr       Expression
}

// ExpressionOrBool is a field that accepts either a boolean or an expression.
type ExpressionOrBool struct {
    BoolValue *bool
    Expr      Expression
}

// StringOrSlice is a field that accepts either a single string or an array of strings.
type StringOrSlice struct {
    Single   string
    Multiple []string
}

// ExpressionOrSlice is a field that accepts an expression, a string, or an array of strings.
type ExpressionOrSlice struct {
    Values []Expression // One or more glob patterns / expressions
}

// ProcessOrRef is a field that accepts either an inline Process or a string file path.
type ProcessOrRef struct {
    Inline  Process // Embedded process definition
    RefPath string  // Path to external CWL file
}
```

---

## 6. Implementation Roadmap

### 6.1 Feature Implementation Order

This roadmap organizes features from simplest (MVP) to most complex (full spec compliance). Each phase builds on the previous.

#### Phase 1: MVP -- Single CommandLineTool Execution

**Goal:** Execute a simple CommandLineTool with basic inputs and outputs.

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| YAML document parsing | P0 | Medium | Use a YAML library; handle map notation |
| Basic type system (string, int, boolean, File) | P0 | Medium | Primitive types and File |
| CommandLineTool class parsing | P0 | Medium | baseCommand, inputs, outputs |
| Input validation and binding | P0 | High | Position, prefix, separate |
| Command-line construction | P0 | High | Sorting, flattening |
| Process execution (no container) | P0 | Medium | os/exec |
| Output collection (glob) | P0 | Medium | Filepath.Glob |
| Exit code handling | P0 | Low | successCodes, permanentFailCodes |
| stdout/stderr capture | P0 | Low | stdout, stderr fields |
| stdin support | P1 | Low | Pipe file to process stdin |

**Milestone test:**
```yaml
# Should be able to run this:
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
    inputBinding:
      position: 1
outputs:
  out:
    type: stdout
stdout: output.txt
```

#### Phase 2: Extended Types and Requirements

**Goal:** Support all types, Docker, and essential requirements.

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| Full type system (long, float, double, null) | P0 | Low | |
| Optional types (null unions) | P0 | Medium | Type DSL: `string?` |
| Array types and binding | P0 | Medium | itemSeparator, nested binding |
| Record types | P1 | Medium | Nested field bindings |
| Enum types | P1 | Low | Symbol validation |
| Default values | P0 | Low | |
| DockerRequirement | P0 | High | dockerPull, volume mounts |
| ResourceRequirement | P1 | Medium | CPU/RAM limits |
| EnvVarRequirement | P1 | Low | Environment variable injection |
| ShellCommandRequirement | P1 | Medium | Shell execution mode |
| Secondary files | P1 | Medium | Pattern matching, `^` stripping |
| Directory type | P1 | Medium | |
| loadContents | P1 | Low | Read first 64 KiB |

**Milestone test:**
```yaml
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [samtools, sort]
requirements:
  DockerRequirement:
    dockerPull: biocontainers/samtools:v1.9-4-deb_cv1
inputs:
  input_bam:
    type: File
    inputBinding:
      position: 1
  threads:
    type: int?
    default: 4
    inputBinding:
      prefix: -@
outputs:
  sorted_bam:
    type: File
    outputBinding:
      glob: "*.sorted.bam"
```

#### Phase 3: JavaScript Expressions

**Goal:** Full expression support (parameter references and inline JavaScript).

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| Parameter references `$()` | P0 | High | Parser for `$(inputs.x.y[0])` syntax |
| String interpolation | P0 | Medium | Mixed text with `$(...)` |
| InlineJavascriptRequirement | P1 | High | Embed JS runtime (goja) |
| JavaScript expressions `$()` | P1 | Medium | Single expression evaluation |
| JavaScript function bodies `${}` | P1 | Medium | Multi-statement with return |
| expressionLib preloading | P2 | Low | Load JS libraries |
| Expression in valueFrom | P1 | Medium | Input transformation |
| Expression in outputEval | P1 | Medium | Output computation |
| Expression in glob | P1 | Low | Dynamic glob patterns |
| Runtime context object | P1 | Low | cores, ram, outdir, tmpdir |

#### Phase 4: Basic Workflows

**Goal:** Execute multi-step workflows with data flow.

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| Workflow class parsing | P0 | Medium | |
| Step parsing and `run` resolution | P0 | Medium | Load external CWL files |
| Data flow graph construction | P0 | High | Parse source references |
| DAG validation (cycle detection) | P0 | Medium | Topological sort |
| Sequential step execution | P0 | Medium | Execute in dependency order |
| Concurrent step execution | P1 | High | Goroutine-based parallelism |
| Workflow input/output resolution | P0 | Medium | outputSource mapping |
| SubworkflowFeatureRequirement | P2 | Medium | Recursive workflow execution |

**Milestone test:**
```yaml
cwlVersion: v1.2
class: Workflow
inputs:
  message:
    type: string
steps:
  step1:
    run: echo.cwl
    in:
      msg: message
    out: [output]
  step2:
    run: wc.cwl
    in:
      input_file: step1/output
    out: [count]
outputs:
  final_count:
    type: File
    outputSource: step2/count
```

#### Phase 5: Scatter, Conditionals, and Advanced Workflow Features

**Goal:** Full workflow execution capability.

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| Scatter (single input) | P0 | High | Array expansion and gathering |
| Scatter (dotproduct) | P1 | Medium | Parallel array iteration |
| Scatter (nested_crossproduct) | P2 | Medium | Cartesian product |
| Scatter (flat_crossproduct) | P2 | Medium | Flattened Cartesian product |
| Conditional execution (when) | P1 | Medium | JS expression returning boolean |
| pickValue | P1 | Medium | first_non_null, the_only_non_null, all_non_null |
| linkMerge | P1 | Medium | merge_nested, merge_flattened |
| MultipleInputFeatureRequirement | P1 | Low | Enable linkMerge |
| StepInputExpressionRequirement | P1 | Low | Enable valueFrom on step inputs |

#### Phase 6: File Staging and Advanced Features

**Goal:** Full spec compliance.

| Feature | Priority | Effort | Notes |
|---------|----------|--------|-------|
| InitialWorkDirRequirement | P1 | High | File staging, Dirent creation |
| LoadListingRequirement | P1 | Medium | Directory listing control |
| SchemaDefRequirement | P2 | Medium | Custom type definitions |
| SoftwareRequirement | P2 | Low | Metadata only (advisory) |
| ExpressionTool | P1 | Medium | Pure JS process execution |
| WorkReuse (caching) | P2 | High | Content-addressable caching |
| NetworkAccess | P2 | Low | Container networking control |
| InplaceUpdateRequirement | P2 | Medium | Writable file staging |
| ToolTimeLimit | P2 | Low | Process timeout |
| $import / $include | P1 | Medium | External document loading |
| $graph (packed documents) | P2 | Medium | Multi-process documents |
| Conformance test suite | P0 | Ongoing | Validate against official tests |

### 6.2 MVP Feature Summary

The absolute minimum to be useful:

1. Parse a CWL CommandLineTool YAML document.
2. Parse a job input YAML file.
3. Validate inputs against declared types (string, int, boolean, File).
4. Build the command line from baseCommand + input bindings.
5. Execute the command locally (no container).
6. Collect output files via glob patterns.
7. Return structured output.

This covers roughly the `command_line_tool` conformance test category.

### 6.3 Known Implementation Challenges

#### Challenge 1: Schema Salad Preprocessing

Schema Salad is the most complex part of document loading. It involves URI resolution, identifier expansion, map-to-array transformation, type DSL expansion, and import/include processing. **Recommendation:** Start with a simplified loader that handles the most common patterns (map notation, type DSL) and add full Schema Salad compliance incrementally.

#### Challenge 2: Type System

CWL's type system supports unions, optional types, nested records, arrays of records, and custom type definitions. The type checker must handle all combinations. **Recommendation:** Implement types as a recursive discriminated union (the `CWLType` struct above). Use recursive validation functions.

#### Challenge 3: JavaScript Evaluation

CWL expressions run in an ECMAScript 5.1 context with specific context variables (`inputs`, `self`, `runtime`). The engine must:
- Parse expressions from strings (detect `$()` vs `${}` vs plain text).
- Evaluate them in a sandboxed JS environment.
- Marshal Go values to/from JS values.
- Handle string interpolation (mixed text and expressions).

**Recommendation:** Use `goja` (pure Go, no CGo). Build a reusable expression evaluator that accepts context variables and returns Go values.

#### Challenge 4: File Staging

CWL requires careful management of input files (read-only), output directories, temporary directories, and staged files. The engine must:
- Create isolated execution directories.
- Symlink or copy input files into the working directory.
- Handle `InitialWorkDirRequirement` staging.
- Collect output files after execution.
- Clean up temporary files.

**Recommendation:** Create an abstraction layer (`StagingManager`) that handles all file operations. This simplifies both local and containerized execution.

#### Challenge 5: Container Abstraction

The spec requires Docker support but the engine should support multiple container runtimes (Docker, Podman, Singularity, etc.). **Recommendation:** Define a `ContainerRuntime` interface with methods like `Pull`, `Run`, `CopyFrom`. Implement Docker first, then add others.

#### Challenge 6: Workflow Scheduling

For workflow execution, the engine must:
- Track which steps are ready (all inputs available).
- Execute steps concurrently (respecting resource limits).
- Propagate outputs to downstream steps.
- Handle scatter expansion and result gathering.
- Handle conditional steps and null propagation.

**Recommendation:** Use Go channels and goroutines. Build a `Scheduler` that manages a pool of workers and a queue of ready steps.

### 6.4 Architecture Recommendations

```
cwl-engine/
  cmd/
    cwl-runner/          # CLI entry point
      main.go
  pkg/
    loader/              # Schema Salad preprocessing and CWL document loading
      loader.go
      salad.go           # Schema Salad transforms
      import.go          # $import / $include handling
    parser/              # CWL document parsing into Go structs
      parser.go
      types.go           # Type parsing and validation
      requirements.go    # Requirement parsing
    types/               # CWL type system and data structures
      cwl.go             # Core struct definitions
      file.go            # File/Directory types
      typing.go          # Type checking and coercion
    expression/          # JavaScript expression evaluation
      evaluator.go       # Expression engine (goja wrapper)
      parameter_ref.go   # Parameter reference parser
      interpolation.go   # String interpolation
    executor/            # Process execution
      executor.go        # Executor interface
      commandline.go     # Command-line construction
      local.go           # Local process execution
    container/           # Container runtime abstraction
      runtime.go         # ContainerRuntime interface
      docker.go          # Docker implementation
      podman.go          # Podman implementation
    staging/             # File staging and I/O management
      staging.go         # StagingManager
      glob.go            # Output glob matching
    workflow/            # Workflow DAG execution
      scheduler.go       # Step scheduling and parallelism
      dag.go             # DAG construction and validation
      scatter.go         # Scatter/gather logic
      conditional.go     # When/pickValue logic
    conformance/         # Conformance test helpers
      runner.go
```

### 6.5 Testing Strategy

1. **Unit tests** for each package (type checking, command-line construction, expression evaluation).
2. **Integration tests** using small CWL documents with known outputs.
3. **Conformance tests** using the official CWL conformance test suite (start with `command_line_tool` tag and expand).
4. **Real-world workflow tests** using published CWL workflows from repositories.

Target conformance test progression:
- Phase 1: `command_line_tool` basic tests (20-30 tests)
- Phase 2: `docker`, `env_var`, `shell_command` tests (10-20 tests)
- Phase 3: `inline_javascript`, `expression_tool` tests (15-25 tests)
- Phase 4: `workflow` basic tests (10-15 tests)
- Phase 5: `scatter`, `conditional`, `multiple_input`, `step_input_expression` (20-30 tests)
- Phase 6: Full conformance suite (150+ tests)

---

## Appendix A: Quick Reference -- CWL Document Skeleton

### CommandLineTool

```yaml
cwlVersion: v1.2
class: CommandLineTool

label: "Tool name"
doc: "Tool description"

requirements:
  - class: DockerRequirement
    dockerPull: image:tag
  - class: ResourceRequirement
    coresMin: 1
    ramMin: 1024

hints:
  - class: SoftwareRequirement
    packages:
      - package: toolname
        version: ["1.0"]

baseCommand: [tool, subcommand]

arguments:
  - position: 0
    prefix: --verbose

stdin: $(inputs.input_file.path)
stdout: output.txt
stderr: errors.log

successCodes: [0]
temporaryFailCodes: []
permanentFailCodes: []

inputs:
  input_file:
    type: File
    inputBinding:
      position: 1
    secondaryFiles:
      - .bai

  param:
    type: string?
    default: "value"
    inputBinding:
      prefix: --param

outputs:
  result:
    type: File
    outputBinding:
      glob: "*.result"
    secondaryFiles:
      - .idx
```

### Workflow

```yaml
cwlVersion: v1.2
class: Workflow

label: "Workflow name"
doc: "Workflow description"

requirements:
  ScatterFeatureRequirement: {}
  SubworkflowFeatureRequirement: {}
  InlineJavascriptRequirement: {}
  MultipleInputFeatureRequirement: {}
  StepInputExpressionRequirement: {}

inputs:
  input_files:
    type: File[]
  reference:
    type: File

steps:
  process:
    run: tool.cwl
    scatter: input_file
    in:
      input_file: input_files
      ref: reference
    out: [result]
    when: $(inputs.input_file.size > 0)

  merge:
    run: merge-tool.cwl
    in:
      files: process/result
    out: [merged]

outputs:
  final_output:
    type: File
    outputSource: merge/merged
```

### ExpressionTool

```yaml
cwlVersion: v1.2
class: ExpressionTool

requirements:
  InlineJavascriptRequirement: {}

inputs:
  files:
    type: File[]

expression: |
  ${
    var largest = null;
    var maxSize = 0;
    for (var i = 0; i < inputs.files.length; i++) {
      if (inputs.files[i].size > maxSize) {
        maxSize = inputs.files[i].size;
        largest = inputs.files[i];
      }
    }
    return {"largest_file": largest, "max_size": maxSize};
  }

outputs:
  largest_file:
    type: File
  max_size:
    type: long
```

---

## Appendix B: CWL v1.2 vs v1.1 -- Key Differences

| Feature | v1.1 | v1.2 |
|---------|------|------|
| Conditional execution (`when`) | Not available | Supported on WorkflowStep |
| `pickValue` | Not available | `first_non_null`, `the_only_non_null`, `all_non_null` |
| CPU cores | Integer only | Fractional (float) supported |
| Resource values | Integer only | Floating-point supported |
| `InitialWorkDirRequirement` `entryname` | Relative paths only | Absolute paths in containers |
| Secondary files `required` field | Not available | `required` can be expression or boolean |
| `cwl.output.json` | Basic support | Refined path resolution |
| `runtime.exitCode` | Not available | Available in `outputEval` |

---

## Appendix C: Useful References

- **CWL Specification v1.2**: https://www.commonwl.org/v1.2/
- **CWL User Guide**: https://www.commonwl.org/user_guide/
- **CWL Conformance Tests**: https://github.com/common-workflow-language/cwl-v1.2/tree/main/tests
- **Schema Salad**: https://www.commonwl.org/v1.2/SchemaSalad.html
- **Reference Implementation (cwltool)**: https://github.com/common-workflow-language/cwltool
- **CWL Viewers and Editors**: https://view.commonwl.org/
- **Existing Go Implementation (cwl-go)**: https://github.com/otiai10/cwl (partial, for reference)
- **goja (Go JS runtime)**: https://github.com/dop251/goja
