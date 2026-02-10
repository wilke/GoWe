# BV-BRC API Reference for Go Client Development

> **Document Version**: 1.0
> **Generated**: 2026-02-09
> **Source Repositories**: wilke/BV-BRC-Go-SDK, BV-BRC/BV-BRC-Web, BV-BRC/app_service, BV-BRC/Workspace
> **Note**: This document is compiled from BV-BRC source code analysis, API documentation, and the
> PATRIC/BV-BRC platform architecture. Sections marked with `[VERIFY]` should be confirmed against
> live endpoints. Clone https://github.com/wilke/BV-BRC-Go-SDK for the reference Go implementation.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Authentication](#2-authentication)
3. [Job Submission Endpoints](#3-job-submission-endpoints)
4. [Job Scheduling & Management](#4-job-scheduling--management)
5. [App Service Endpoints](#5-app-service-endpoints)
6. [Workspace API](#6-workspace-api)
7. [Data Structures (Go)](#7-data-structures-go)
8. [Go Implementation Examples](#8-go-implementation-examples)
9. [Configuration Reference](#9-configuration-reference)
10. [Endpoint Reference Table](#10-endpoint-reference-table)

---

## 1. Overview

### What is BV-BRC?

BV-BRC (Bacterial and Viral Bioinformatics Resource Center) is a NIAID-funded information system
that provides integrated data and analysis tools for bacterial and viral infectious disease research.
It is the successor to PATRIC (Pathosystems Resource Integration Center) and IRD/ViPR, merged into
a single unified platform.

- **Website**: https://www.bv-brc.org/
- **GitHub Organization**: https://github.com/BV-BRC/
- **Previous system**: PATRIC (https://patricbrc.org/, now redirects to BV-BRC)

### API Architecture

BV-BRC exposes multiple API layers:

| Layer | Protocol | Description |
|-------|----------|-------------|
| **Data API** | REST (Solr-backed) | Query genomic data, features, genomes, etc. |
| **App Service** | JSON-RPC 1.1 (JSONRPC11) | Submit and manage analysis jobs |
| **Workspace** | JSON-RPC 1.1 (JSONRPC11) | File and data management |
| **User Service** | REST + Token-based | Authentication and user management |
| **Shock** | REST | Large file storage (data nodes) |

The primary APIs for job submission and scheduling use **JSON-RPC 1.1** over HTTPS POST.

### JSON-RPC Transport Details

- **Version**: JSON-RPC 1.1 (not 2.0)
- **Transport**: HTTPS POST with `Content-Type: application/json`
- **Authentication**: Bearer token in `Authorization` header
- **Encoding**: UTF-8
- **Max payload**: Varies by endpoint (typically no hard limit for metadata; file uploads use Shock)

### Base URLs and Environments

| Environment | Base URL | Description |
|------------|----------|-------------|
| **Production** | `https://p3.theseed.org/services/` | Primary production services |
| **Production (alt)** | `https://www.bv-brc.org/api/` | Web-facing data API |
| **App Service** | `https://p3.theseed.org/services/app_service` | Job submission and management |
| **Workspace** | `https://p3.theseed.org/services/Workspace` | File/workspace management |
| **Data API** | `https://www.bv-brc.org/api/` | Data query (REST/Solr) |
| **User Service** | `https://user.patricbrc.org/` | Authentication (legacy URL still active) |
| **Shock** | `https://p3.theseed.org/services/shock_api` | Large file storage |

> `[VERIFY]` Some URLs may have been updated. Check `https://www.bv-brc.org/api/` and
> `https://p3.theseed.org/services/` for current availability.

---

## 2. Authentication

### Authentication Method

BV-BRC uses **token-based authentication** with a custom token format derived from the Globus Auth
system (originally PATRIC used Globus Nexus tokens, now uses its own auth service).

### How to Obtain Credentials

1. **Register** at https://www.bv-brc.org/ (or https://user.patricbrc.org/register)
2. **Login** via the authentication endpoint to receive a token
3. **Token** is passed in the `Authorization` header for all authenticated requests

### Authentication Endpoint

```
POST https://user.patricbrc.org/authenticate
Content-Type: application/x-www-form-urlencoded
```

**Parameters** (form-encoded):

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `username` | string | Yes | BV-BRC username |
| `password` | string | Yes | BV-BRC password |

**Response** (on success):

Returns a plain-text token string.

### Token Format

The BV-BRC authentication token is a pipe-delimited string containing claims:

```
un=<username>|tokenid=<uuid>|expiry=<unix_timestamp>|client_id=<client_id>|token_type=Bearer|this_is_globus=<realm>|sig=<signature>
```

Key fields:
- `un`: Username
- `tokenid`: Unique token identifier (UUID)
- `expiry`: Unix timestamp when the token expires
- `token_type`: Always `Bearer`
- `sig`: Cryptographic signature validating the token

### Token Lifecycle

- **Default expiry**: Tokens typically expire after **60 days** (may vary)
- **Storage**: Tokens are stored locally in `~/.patric_token` or `~/.bvbrc_token`
- **Refresh**: No automatic refresh; re-authenticate when expired
- **Validation**: Server validates signature and expiry on each request

### Using the Token

For all authenticated API calls, include:

```
Authorization: <token_string>
```

Note: Some implementations use `Authorization: OAuth <token>` format, but the raw token string
is most common.

### Example Auth Flow in Go

```go
func Authenticate(username, password string) (string, error) {
    data := url.Values{}
    data.Set("username", username)
    data.Set("password", password)

    resp, err := http.Post(
        "https://user.patricbrc.org/authenticate",
        "application/x-www-form-urlencoded",
        strings.NewReader(data.Encode()),
    )
    if err != nil {
        return "", fmt.Errorf("authentication request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("authentication failed (HTTP %d): %s", resp.StatusCode, string(body))
    }

    token, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", fmt.Errorf("failed to read token: %w", err)
    }

    return strings.TrimSpace(string(token)), nil
}
```

### Token File Locations

| File | Description |
|------|-------------|
| `~/.patric_token` | Legacy PATRIC token file |
| `~/.bvbrc_token` | Current BV-BRC token file |
| `~/.p3_token` | Alternative token file (some CLI tools) |

---

## 3. Job Submission Endpoints

### JSON-RPC Request Format

All App Service calls use this JSON-RPC 1.1 envelope:

```json
{
    "id": "unique-request-id",
    "method": "AppService.method_name",
    "version": "1.1",
    "params": [ ... ]
}
```

### Response Format

```json
{
    "id": "unique-request-id",
    "version": "1.1",
    "result": [ ... ]
}
```

Error response:

```json
{
    "id": "unique-request-id",
    "version": "1.1",
    "error": {
        "name": "JSONRPCError",
        "code": -32000,
        "message": "Error description"
    }
}
```

### Core Job Submission Methods

#### `AppService.start_app`

Submit a new job for execution.

**Endpoint**: `POST https://p3.theseed.org/services/app_service`

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `app_id` | string | Yes | Application identifier (e.g., `GenomeAssembly2`, `GenomeAnnotation`) |
| 1 | `params` | object | Yes | Application-specific parameters (JSON object) |
| 2 | `workspace_path` | string | Yes | Output workspace path (e.g., `/username@patricbrc.org/home/results`) |

**Example Request**:

```json
{
    "id": "1",
    "method": "AppService.start_app",
    "version": "1.1",
    "params": [
        "GenomeAnnotation",
        {
            "contigs": "/username@patricbrc.org/home/contigs.fasta",
            "scientific_name": "Escherichia coli",
            "taxonomy_id": 562,
            "code": 11,
            "domain": "Bacteria",
            "output_path": "/username@patricbrc.org/home/",
            "output_file": "annotation_result"
        },
        "/username@patricbrc.org/home/"
    ]
}
```

**Example Response**:

```json
{
    "id": "1",
    "version": "1.1",
    "result": [{
        "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
        "app": {
            "id": "GenomeAnnotation",
            "label": "Genome Annotation",
            "description": "Annotate a genome"
        },
        "submit_time": "2026-02-09T10:30:00Z",
        "start_time": null,
        "completed_time": null,
        "status": "queued",
        "parameters": { ... },
        "owner": "username@patricbrc.org"
    }]
}
```

#### `AppService.start_app2`

Enhanced version of `start_app` with additional options.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `app_id` | string | Yes | Application identifier |
| 1 | `params` | object | Yes | Application-specific parameters |
| 2 | `workspace_path` | string | Yes | Output workspace path |
| 3 | `options` | object | No | Additional options (reservation, container, etc.) |

The `options` object may include:

```json
{
    "reservation": "reservation-id",
    "container_id": "container-image-id",
    "base_url": "alternate-service-url"
}
```

#### `AppService.query_tasks`

Query the status of submitted jobs.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `task_ids` | array[string] | Yes | List of task/job IDs to query |

**Example Request**:

```json
{
    "id": "2",
    "method": "AppService.query_tasks",
    "version": "1.1",
    "params": [
        ["a1b2c3d4-e5f6-7890-abcd-ef1234567890"]
    ]
}
```

**Example Response**:

```json
{
    "id": "2",
    "version": "1.1",
    "result": [{
        "a1b2c3d4-e5f6-7890-abcd-ef1234567890": {
            "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
            "app": "GenomeAnnotation",
            "status": "completed",
            "submit_time": "2026-02-09T10:30:00Z",
            "start_time": "2026-02-09T10:31:00Z",
            "completed_time": "2026-02-09T10:45:00Z",
            "owner": "username@patricbrc.org",
            "output_path": "/username@patricbrc.org/home/annotation_result",
            "parameters": { ... }
        }
    }]
}
```

#### `AppService.query_task_summary`

Get a summary of all tasks for the authenticated user.

**Parameters**: None (empty array `[]`)

**Example Response**:

```json
{
    "id": "3",
    "version": "1.1",
    "result": [{
        "queued": 2,
        "in-progress": 1,
        "completed": 45,
        "failed": 3,
        "deleted": 0
    }]
}
```

#### `AppService.enumerate_tasks`

List tasks with pagination and filtering.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `offset` | integer | Yes | Starting offset (0-based) |
| 1 | `count` | integer | Yes | Number of tasks to return |

**Example Request**:

```json
{
    "id": "4",
    "method": "AppService.enumerate_tasks",
    "version": "1.1",
    "params": [0, 25]
}
```

#### `AppService.enumerate_tasks_filtered`

List tasks with filtering criteria.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `offset` | integer | Yes | Starting offset |
| 1 | `count` | integer | Yes | Number of results |
| 2 | `simple_filter` | object | No | Filter criteria |

Filter object keys:

```json
{
    "status": "completed",
    "app": "GenomeAnnotation",
    "submit_time_start": "2026-01-01T00:00:00Z",
    "submit_time_end": "2026-02-09T00:00:00Z"
}
```

---

## 4. Job Scheduling & Management

### Job Lifecycle States

Jobs progress through these states:

```
queued -> in-progress -> completed
                      -> failed
                      -> suspended
```

| State | Description |
|-------|-------------|
| `queued` | Job submitted and waiting in queue |
| `in-progress` | Job is actively executing |
| `completed` | Job finished successfully |
| `failed` | Job encountered an error |
| `deleted` | Job was cancelled/deleted by user |
| `suspended` | Job was suspended (admin action or resource limits) |

### Scheduling Methods

#### `AppService.query_task_details`

Get detailed information about a specific task including scheduling details.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `task_id` | string | Yes | The task/job ID |

**Response** includes:

```json
{
    "id": "task-uuid",
    "app": "GenomeAnnotation",
    "status": "in-progress",
    "submit_time": "2026-02-09T10:30:00Z",
    "start_time": "2026-02-09T10:31:00Z",
    "completed_time": null,
    "owner": "username@patricbrc.org",
    "parameters": { ... },
    "cluster_job_id": "slurm-12345",
    "hostname": "compute-node-01",
    "output_path": "/username@patricbrc.org/home/output",
    "stdout_url": "shock://p3.theseed.org/node/abc123",
    "stderr_url": "shock://p3.theseed.org/node/def456"
}
```

#### `AppService.kill_task`

Cancel a running or queued job.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `task_id` | string | Yes | The task/job ID to cancel |

**Example Request**:

```json
{
    "id": "5",
    "method": "AppService.kill_task",
    "version": "1.1",
    "params": ["a1b2c3d4-e5f6-7890-abcd-ef1234567890"]
}
```

**Response**:

```json
{
    "id": "5",
    "version": "1.1",
    "result": [1]
}
```

Returns `1` on success, error on failure.

#### `AppService.query_app_log`

Retrieve execution logs for a task.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `task_id` | string | Yes | The task/job ID |

#### `AppService.rerun_task`

Re-submit a previously completed or failed task with the same parameters.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `task_id` | string | Yes | The original task ID to rerun |

### Monitoring Job Progress

To monitor a job's progress, poll `query_tasks` or `query_task_details` at intervals:

```go
// Recommended polling strategy:
// - First 5 minutes: poll every 10 seconds
// - 5-30 minutes: poll every 30 seconds
// - After 30 minutes: poll every 60 seconds
```

### Scheduler Architecture

BV-BRC uses a multi-tier scheduling architecture:

1. **App Service** receives job submission via JSON-RPC
2. **Scheduler** queues jobs based on priority, resource availability, and fair-share policies
3. **Cluster Backend** (typically Slurm or AWE) executes the actual computation
4. **Status Updates** propagate back through the App Service to the user

---

## 5. App Service Endpoints

### Listing Available Applications

#### `AppService.enumerate_apps`

List all available bioinformatics applications.

**Parameters**: None (empty array `[]`)

**Example Response**:

```json
{
    "id": "6",
    "version": "1.1",
    "result": [[
        {
            "id": "GenomeAssembly2",
            "label": "Genome Assembly",
            "description": "Assemble reads into contigs",
            "parameters": [ ... ],
            "default_memory": "128G",
            "default_cpu": 8
        },
        {
            "id": "GenomeAnnotation",
            "label": "Genome Annotation",
            "description": "Annotate a genome using RASTtk",
            "parameters": [ ... ]
        }
    ]]
}
```

#### `AppService.query_app_description`

Get detailed description of a specific application.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `app_id` | string | Yes | Application identifier |

### Available Applications (Common)

| App ID | Label | Description |
|--------|-------|-------------|
| `GenomeAssembly2` | Genome Assembly | Assemble reads (SPAdes, MEGAHIT, etc.) |
| `GenomeAnnotation` | Genome Annotation | RASTtk annotation pipeline |
| `ComprehensiveGenomeAnalysis` | Comprehensive Genome Analysis | Assembly + Annotation + Analysis |
| `TaxonomicClassification` | Taxonomic Classification | Kraken2 / metagenomic classification |
| `MetagenomicBinning` | Metagenomic Binning | Bin metagenomic contigs |
| `PhylogeneticTree` | Phylogenetic Tree | Build phylogenetic trees |
| `GenomeAlignment` | Genome Alignment | Whole genome alignment |
| `Variation` | Variation Analysis | SNP/variant calling |
| `RNASeq` | RNA-Seq Analysis | Differential expression analysis |
| `TnSeq` | Tn-Seq Analysis | Transposon insertion sequencing |
| `DifferentialExpression` | Differential Expression | Gene expression comparison |
| `ProteinFamily` | Protein Family Sorter | Analyze protein families |
| `Proteome` | Proteome Comparison | Compare proteomes |
| `FastqUtils` | FASTQ Utilities | Read trimming, filtering, QC |
| `MetagenomicReadMapping` | Metagenomic Read Mapping | Map reads to reference genomes |
| `CodonTree` | Codon Tree | Build codon-based phylogenetic trees |
| `MSA` | Multiple Sequence Alignment | Align multiple sequences (MAFFT, MUSCLE) |
| `SubspeciesClassification` | Subspecies Classification | Classify bacterial subspecies |
| `Homology` | Homology Search | BLAST searches |
| `GenomeComparison` | Genome Comparison | Compare multiple genomes |
| `HASubtypeNumberingConversion` | HA Subtype Numbering | Influenza HA numbering |
| `PrimerDesign` | Primer Design | Design PCR primers |

> `[VERIFY]` The complete list of available apps may change. Use `enumerate_apps` for the current list.

### App-Specific Parameters

Each application has its own parameter schema. Common parameter patterns:

#### Genome Assembly Parameters

```json
{
    "paired_end_libs": [
        {
            "read1": "/user@patricbrc.org/home/reads_R1.fastq.gz",
            "read2": "/user@patricbrc.org/home/reads_R2.fastq.gz",
            "interleaved": false,
            "platform": "illumina"
        }
    ],
    "single_end_libs": [],
    "srr_ids": ["SRR12345678"],
    "recipe": "auto",
    "pipeline": "spades",
    "min_contig_len": 300,
    "min_contig_cov": 5.0,
    "trim": true,
    "output_path": "/user@patricbrc.org/home/",
    "output_file": "assembly_result"
}
```

#### Genome Annotation Parameters

```json
{
    "contigs": "/user@patricbrc.org/home/contigs.fasta",
    "scientific_name": "Escherichia coli K-12",
    "taxonomy_id": 83333,
    "code": 11,
    "domain": "Bacteria",
    "recipe": "default",
    "output_path": "/user@patricbrc.org/home/",
    "output_file": "annotation_result"
}
```

#### Comprehensive Genome Analysis Parameters

```json
{
    "paired_end_libs": [
        {
            "read1": "/user@patricbrc.org/home/reads_R1.fastq.gz",
            "read2": "/user@patricbrc.org/home/reads_R2.fastq.gz"
        }
    ],
    "scientific_name": "Escherichia coli",
    "taxonomy_id": 562,
    "code": 11,
    "domain": "Bacteria",
    "recipe": "default",
    "output_path": "/user@patricbrc.org/home/",
    "output_file": "cga_result"
}
```

---

## 6. Workspace API

### Overview

The Workspace service manages files and directories within user workspaces. Each user has a
workspace root identified by `/<username>@patricbrc.org/`.

**Endpoint**: `POST https://p3.theseed.org/services/Workspace`

### Workspace Path Convention

```
/<username>@patricbrc.org/<workspace_name>/<path>/<to>/<object>
```

Default workspaces:
- `/username@patricbrc.org/home/` - User's home workspace
- `/username@patricbrc.org/public/` - Publicly shared data
- `/PATRIC@patricbrc.org/PATRIC/` - System reference data (read-only)

### Workspace Object Types

| Type | Description |
|------|-------------|
| `folder` | Directory/container |
| `modelfolder` | Model-specific folder |
| `job_result` | Job output folder |
| `contigs` | Contig/FASTA file |
| `reads` | Sequencing reads |
| `feature_group` | Group of genomic features |
| `genome_group` | Group of genomes |
| `experiment_group` | Group of experiments |
| `unspecified` | General file |
| `diffexp_input_data` | Differential expression input |
| `diffexp_input_metadata` | Differential expression metadata |
| `html` | HTML report |
| `pdf` | PDF document |
| `txt` | Plain text |
| `json` | JSON data |
| `csv` | CSV data |
| `nwk` | Newick tree file |
| `svg` | SVG graphic |

### Workspace Methods

#### `Workspace.create`

Create a new workspace object (file or folder).

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `objects` | array | Yes | Array of object creation specifications |

Each object spec is an array:

```json
[
    "/user@patricbrc.org/home/path/to/object",  // destination path
    "folder",                                      // type
    {},                                            // metadata
    null                                           // content (null for folders, string for files)
]
```

**Example - Create a folder**:

```json
{
    "id": "7",
    "method": "Workspace.create",
    "version": "1.1",
    "params": [{
        "objects": [
            ["/user@patricbrc.org/home/my_analysis", "folder", {}, null]
        ]
    }]
}
```

**Example - Upload a small file**:

```json
{
    "id": "8",
    "method": "Workspace.create",
    "version": "1.1",
    "params": [{
        "objects": [
            [
                "/user@patricbrc.org/home/my_data.txt",
                "txt",
                {},
                "file content here as a string"
            ]
        ],
        "createUploadNodes": false,
        "overwrite": true
    }]
}
```

#### `Workspace.create` (for upload nodes - large files)

For large files, create an upload node first, then upload to Shock:

```json
{
    "id": "9",
    "method": "Workspace.create",
    "version": "1.1",
    "params": [{
        "objects": [
            [
                "/user@patricbrc.org/home/large_file.fastq.gz",
                "reads",
                {},
                null
            ]
        ],
        "createUploadNodes": true
    }]
}
```

**Response** includes a Shock node URL for upload:

```json
{
    "result": [[
        [
            "/user@patricbrc.org/home/large_file.fastq.gz",
            "reads",
            "user@patricbrc.org",
            "2026-02-09T10:00:00Z",
            "uuid-here",
            "user@patricbrc.org",
            12345,
            {},
            {},
            "shock",
            "shock-node-id-here"
        ]
    ]]
}
```

Then upload the file to Shock:

```
PUT https://p3.theseed.org/services/shock_api/node/<shock-node-id>
Authorization: <token>
Content-Type: multipart/form-data

[file data]
```

#### `Workspace.get`

Retrieve workspace objects.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `objects` | array[string] | Yes | Array of workspace paths to retrieve |
| 1 | `metadata_only` | boolean | No | If true, only return metadata (default: false) |

**Example Request**:

```json
{
    "id": "10",
    "method": "Workspace.get",
    "version": "1.1",
    "params": [{
        "objects": ["/user@patricbrc.org/home/my_data.txt"],
        "metadata_only": false
    }]
}
```

**Response format**:

Each object is returned as a tuple array:

```
[path, type, owner, creation_time, id, owner_id, size, user_metadata, auto_metadata, shock_ref, shock_node]
```

Index mapping:

| Index | Field | Type | Description |
|-------|-------|------|-------------|
| 0 | path | string | Full workspace path |
| 1 | type | string | Object type |
| 2 | owner | string | Object owner |
| 3 | creation_time | string | ISO 8601 creation timestamp |
| 4 | id | string | Object UUID |
| 5 | owner_id | string | Owner identifier |
| 6 | size | integer | File size in bytes |
| 7 | user_metadata | object | User-defined metadata |
| 8 | auto_metadata | object | Auto-generated metadata |
| 9 | shock_ref | string | Shock reference type ("shock" or "inline") |
| 10 | shock_node | string | Shock node ID (if applicable) |
| 11 | data | string | File content (if not metadata_only and inline) |

#### `Workspace.ls`

List contents of a workspace directory.

**Parameters**:

| Index | Name | Type | Required | Description |
|-------|------|------|----------|-------------|
| 0 | `paths` | array[string] | Yes | Workspace paths to list |
| 1 | `excludeDirectories` | boolean | No | Exclude subdirectories from listing |
| 2 | `excludeObjects` | boolean | No | Exclude objects (files) from listing |
| 3 | `recursive` | boolean | No | List recursively |

**Example Request**:

```json
{
    "id": "11",
    "method": "Workspace.ls",
    "version": "1.1",
    "params": [{
        "paths": ["/user@patricbrc.org/home/"]
    }]
}
```

**Example Response**:

```json
{
    "id": "11",
    "version": "1.1",
    "result": [{
        "/user@patricbrc.org/home/": [
            ["/user@patricbrc.org/home/assembly_results", "job_result", "user@patricbrc.org", "2026-02-08T15:00:00Z", "uuid1", "user@patricbrc.org", 0, {}, {}, null, null],
            ["/user@patricbrc.org/home/contigs.fasta", "contigs", "user@patricbrc.org", "2026-02-07T10:00:00Z", "uuid2", "user@patricbrc.org", 1048576, {}, {}, "shock", "shock-node-uuid"]
        ]
    }]
}
```

#### `Workspace.delete`

Delete workspace objects.

**Parameters**:

```json
{
    "id": "12",
    "method": "Workspace.delete",
    "version": "1.1",
    "params": [{
        "objects": ["/user@patricbrc.org/home/old_file.txt"],
        "force": false,
        "deleteDirectories": false
    }]
}
```

#### `Workspace.copy`

Copy workspace objects.

**Parameters**:

```json
{
    "id": "13",
    "method": "Workspace.copy",
    "version": "1.1",
    "params": [{
        "objects": [
            ["/user@patricbrc.org/home/source.txt", "/user@patricbrc.org/home/dest.txt"]
        ],
        "overwrite": false,
        "recursive": false
    }]
}
```

#### `Workspace.move`

Move/rename workspace objects.

**Parameters** (same format as copy):

```json
{
    "id": "14",
    "method": "Workspace.move",
    "version": "1.1",
    "params": [{
        "objects": [
            ["/user@patricbrc.org/home/old_name.txt", "/user@patricbrc.org/home/new_name.txt"]
        ],
        "overwrite": false
    }]
}
```

#### `Workspace.list_permissions`

List permissions on a workspace path.

**Parameters**:

```json
{
    "id": "15",
    "method": "Workspace.list_permissions",
    "version": "1.1",
    "params": [{
        "objects": ["/user@patricbrc.org/home/"]
    }]
}
```

#### `Workspace.set_permissions`

Set sharing permissions on workspace objects.

**Parameters**:

```json
{
    "id": "16",
    "method": "Workspace.set_permissions",
    "version": "1.1",
    "params": [{
        "path": "/user@patricbrc.org/home/shared_folder",
        "permissions": [
            ["collaborator@patricbrc.org", "r"],
            ["another_user@patricbrc.org", "w"]
        ]
    }]
}
```

Permission levels:
- `"r"` - Read only
- `"w"` - Read and write
- `"o"` - Owner (full control)
- `"n"` - No access (revoke)

#### `Workspace.get_download_url`

Get a download URL for a workspace object.

**Parameters**:

```json
{
    "id": "17",
    "method": "Workspace.get_download_url",
    "version": "1.1",
    "params": [{
        "objects": ["/user@patricbrc.org/home/results/output.txt"]
    }]
}
```

---

## 7. Data Structures (Go)

### JSON-RPC Envelope Types

```go
// RPCRequest represents a JSON-RPC 1.1 request envelope.
type RPCRequest struct {
    // ID is a unique identifier for the request, used to match responses.
    ID string `json:"id"`

    // Method is the fully-qualified JSON-RPC method name (e.g., "AppService.start_app").
    Method string `json:"method"`

    // Version is the JSON-RPC protocol version. Always "1.1" for BV-BRC.
    Version string `json:"version"`

    // Params contains the method parameters as a positional array.
    Params []interface{} `json:"params"`
}

// RPCResponse represents a JSON-RPC 1.1 response envelope.
type RPCResponse struct {
    // ID matches the request ID.
    ID string `json:"id"`

    // Version is the JSON-RPC protocol version.
    Version string `json:"version"`

    // Result contains the successful response data.
    // The structure varies by method.
    Result json.RawMessage `json:"result,omitempty"`

    // Error contains error information if the call failed.
    Error *RPCError `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
    // Name is the error class name (e.g., "JSONRPCError").
    Name string `json:"name"`

    // Code is the numeric error code. Standard JSON-RPC codes apply.
    // -32700: Parse error
    // -32600: Invalid request
    // -32601: Method not found
    // -32602: Invalid params
    // -32603: Internal error
    // -32000 to -32099: Server errors
    Code int `json:"code"`

    // Message is a human-readable error description.
    Message string `json:"message"`

    // Data contains additional error data (optional).
    Data interface{} `json:"data,omitempty"`
}
```

### Authentication Types

```go
// AuthToken represents a BV-BRC authentication token with parsed fields.
type AuthToken struct {
    // Raw is the complete token string as received from the auth service.
    Raw string `json:"-"`

    // Username is the authenticated user's login name (from "un" field).
    Username string `json:"username"`

    // TokenID is the unique identifier for this token (UUID).
    TokenID string `json:"token_id"`

    // Expiry is when the token expires.
    Expiry time.Time `json:"expiry"`

    // ClientID identifies the client application.
    ClientID string `json:"client_id"`

    // TokenType is the authorization scheme (typically "Bearer").
    TokenType string `json:"token_type"`

    // SignatureData is the cryptographic signature validating the token.
    Signature string `json:"signature"`
}

// AuthCredentials holds login credentials for authentication.
type AuthCredentials struct {
    // Username is the BV-BRC account username.
    Username string `json:"username"`

    // Password is the BV-BRC account password.
    Password string `json:"password"`
}
```

### Job/Task Types

```go
// TaskState represents the current state of a job/task.
type TaskState string

const (
    TaskStateQueued     TaskState = "queued"
    TaskStateInProgress TaskState = "in-progress"
    TaskStateCompleted  TaskState = "completed"
    TaskStateFailed     TaskState = "failed"
    TaskStateDeleted    TaskState = "deleted"
    TaskStateSuspended  TaskState = "suspended"
)

// Task represents a submitted job/task in the App Service.
type Task struct {
    // ID is the unique task identifier (UUID).
    ID string `json:"id"`

    // App is the application identifier that was run.
    App string `json:"app"`

    // Owner is the username of the task owner.
    Owner string `json:"owner"`

    // Status is the current execution state.
    Status TaskState `json:"status"`

    // SubmitTime is when the task was submitted.
    SubmitTime *time.Time `json:"submit_time,omitempty"`

    // StartTime is when execution began (null if not started).
    StartTime *time.Time `json:"start_time,omitempty"`

    // CompletedTime is when execution finished (null if not completed).
    CompletedTime *time.Time `json:"completed_time,omitempty"`

    // Parameters contains the application-specific parameters used.
    Parameters map[string]interface{} `json:"parameters,omitempty"`

    // OutputPath is the workspace path where results are stored.
    OutputPath string `json:"output_path,omitempty"`

    // ClusterJobID is the backend cluster job identifier (e.g., Slurm job ID).
    ClusterJobID string `json:"cluster_job_id,omitempty"`

    // Hostname is the compute node where the job ran.
    Hostname string `json:"hostname,omitempty"`

    // StdoutURL is the URL to the job's stdout log.
    StdoutURL string `json:"stdout_url,omitempty"`

    // StderrURL is the URL to the job's stderr log.
    StderrURL string `json:"stderr_url,omitempty"`
}

// TaskSummary holds aggregated task counts by status.
type TaskSummary struct {
    Queued     int `json:"queued"`
    InProgress int `json:"in-progress"`
    Completed  int `json:"completed"`
    Failed     int `json:"failed"`
    Deleted    int `json:"deleted"`
}

// JobSubmission represents the parameters needed to submit a new job.
type JobSubmission struct {
    // AppID is the identifier of the application to run.
    AppID string `json:"app_id"`

    // Params contains application-specific parameters.
    Params map[string]interface{} `json:"params"`

    // OutputPath is the workspace path for results.
    OutputPath string `json:"output_path"`

    // Options contains optional scheduling/execution options.
    Options *JobOptions `json:"options,omitempty"`
}

// JobOptions contains optional parameters for job submission.
type JobOptions struct {
    // Reservation specifies a cluster reservation to use.
    Reservation string `json:"reservation,omitempty"`

    // ContainerID specifies a specific container image.
    ContainerID string `json:"container_id,omitempty"`

    // BaseURL overrides the default service URL.
    BaseURL string `json:"base_url,omitempty"`
}
```

### App Service Types

```go
// AppDescription describes an available bioinformatics application.
type AppDescription struct {
    // ID is the unique application identifier.
    ID string `json:"id"`

    // Label is the human-readable application name.
    Label string `json:"label"`

    // Description provides a brief description of what the app does.
    Description string `json:"description"`

    // Parameters defines the app's parameter schema.
    Parameters []AppParameter `json:"parameters,omitempty"`

    // DefaultMemory is the default memory allocation.
    DefaultMemory string `json:"default_memory,omitempty"`

    // DefaultCPU is the default CPU allocation.
    DefaultCPU int `json:"default_cpu,omitempty"`
}

// AppParameter describes a single parameter for an application.
type AppParameter struct {
    // ID is the parameter identifier.
    ID string `json:"id"`

    // Label is the human-readable parameter name.
    Label string `json:"label"`

    // Type is the parameter data type (string, int, float, boolean, enum, etc.).
    Type string `json:"type"`

    // Required indicates whether the parameter is mandatory.
    Required bool `json:"required"`

    // Default is the default value if not specified.
    Default interface{} `json:"default,omitempty"`

    // Description explains the parameter.
    Description string `json:"desc,omitempty"`

    // EnumValues lists valid values for enum-type parameters.
    EnumValues []string `json:"enum,omitempty"`
}
```

### Workspace Types

```go
// WorkspaceObject represents a file or folder in the BV-BRC workspace.
type WorkspaceObject struct {
    // Path is the full workspace path.
    Path string `json:"path"`

    // Type is the object type (folder, contigs, reads, etc.).
    Type string `json:"type"`

    // Owner is the object owner's username.
    Owner string `json:"owner"`

    // CreationTime is when the object was created.
    CreationTime time.Time `json:"creation_time"`

    // ID is the unique object identifier (UUID).
    ID string `json:"id"`

    // OwnerID is the owner's identifier.
    OwnerID string `json:"owner_id"`

    // Size is the file size in bytes.
    Size int64 `json:"size"`

    // UserMetadata contains user-defined metadata key-value pairs.
    UserMetadata map[string]string `json:"user_metadata"`

    // AutoMetadata contains system-generated metadata.
    AutoMetadata map[string]string `json:"auto_metadata"`

    // ShockRef indicates storage type ("shock" for Shock-stored, "inline" for small files).
    ShockRef string `json:"shock_ref,omitempty"`

    // ShockNodeID is the Shock node identifier for large files.
    ShockNodeID string `json:"shock_node_id,omitempty"`

    // Data contains inline file content (for small files retrieved with metadata_only=false).
    Data string `json:"data,omitempty"`
}

// WorkspacePermission represents a permission entry for a workspace object.
type WorkspacePermission struct {
    // Username is the user being granted permission.
    Username string `json:"username"`

    // Permission is the permission level: "r" (read), "w" (write), "o" (owner), "n" (none).
    Permission string `json:"permission"`
}

// WorkspaceObjectMeta is the tuple representation returned by workspace API calls.
// The API returns arrays of arrays, not named objects.
// Index mapping:
//   [0] = path, [1] = type, [2] = owner, [3] = creation_time, [4] = id,
//   [5] = owner_id, [6] = size, [7] = user_meta, [8] = auto_meta,
//   [9] = shock_ref, [10] = shock_node
type WorkspaceObjectMeta [11]interface{}
```

### Client Configuration Types

```go
// ClientConfig holds all configuration for the BV-BRC API client.
type ClientConfig struct {
    // AppServiceURL is the URL for the App Service (job management).
    AppServiceURL string `json:"app_service_url"`

    // WorkspaceURL is the URL for the Workspace service.
    WorkspaceURL string `json:"workspace_url"`

    // DataAPIURL is the URL for the Data API (REST/Solr).
    DataAPIURL string `json:"data_api_url"`

    // AuthURL is the URL for the authentication service.
    AuthURL string `json:"auth_url"`

    // ShockURL is the URL for the Shock data storage service.
    ShockURL string `json:"shock_url"`

    // Token is the current authentication token.
    Token string `json:"token,omitempty"`

    // Username is the authenticated user's name.
    Username string `json:"username,omitempty"`

    // Timeout is the HTTP client timeout.
    Timeout time.Duration `json:"timeout"`

    // MaxRetries is the maximum number of retry attempts for failed requests.
    MaxRetries int `json:"max_retries"`

    // RetryDelay is the initial delay between retries (exponential backoff applied).
    RetryDelay time.Duration `json:"retry_delay"`
}

// DefaultConfig returns a ClientConfig with default production URLs.
func DefaultConfig() ClientConfig {
    return ClientConfig{
        AppServiceURL: "https://p3.theseed.org/services/app_service",
        WorkspaceURL:  "https://p3.theseed.org/services/Workspace",
        DataAPIURL:    "https://www.bv-brc.org/api/",
        AuthURL:       "https://user.patricbrc.org/authenticate",
        ShockURL:      "https://p3.theseed.org/services/shock_api",
        Timeout:       30 * time.Second,
        MaxRetries:    3,
        RetryDelay:    1 * time.Second,
    }
}
```

---

## 8. Go Implementation Examples

### Complete Example: Authenticate and Get Token

```go
package main

import (
    "fmt"
    "io"
    "log"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"
)

// AuthToken holds the parsed authentication token.
type AuthToken struct {
    Raw      string
    Username string
    TokenID  string
    Expiry   time.Time
}

// Authenticate logs in to BV-BRC and returns a token.
func Authenticate(authURL, username, password string) (*AuthToken, error) {
    data := url.Values{}
    data.Set("username", username)
    data.Set("password", password)

    resp, err := http.Post(
        authURL,
        "application/x-www-form-urlencoded",
        strings.NewReader(data.Encode()),
    )
    if err != nil {
        return nil, fmt.Errorf("auth request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("auth failed (HTTP %d): %s", resp.StatusCode, body)
    }

    tokenBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("reading token: %w", err)
    }

    raw := strings.TrimSpace(string(tokenBytes))
    token := &AuthToken{Raw: raw}

    // Parse token fields
    for _, part := range strings.Split(raw, "|") {
        kv := strings.SplitN(part, "=", 2)
        if len(kv) != 2 {
            continue
        }
        switch kv[0] {
        case "un":
            token.Username = kv[1]
        case "tokenid":
            token.TokenID = kv[1]
        case "expiry":
            var ts int64
            fmt.Sscanf(kv[1], "%d", &ts)
            token.Expiry = time.Unix(ts, 0)
        }
    }

    return token, nil
}

// SaveToken writes the token to the standard BV-BRC token file.
func SaveToken(token *AuthToken) error {
    home, err := os.UserHomeDir()
    if err != nil {
        return err
    }
    path := filepath.Join(home, ".bvbrc_token")
    return os.WriteFile(path, []byte(token.Raw), 0600)
}

// LoadToken reads a token from the standard file locations.
func LoadToken() (string, error) {
    home, err := os.UserHomeDir()
    if err != nil {
        return "", err
    }

    // Try BV-BRC token first, then PATRIC token
    for _, name := range []string{".bvbrc_token", ".patric_token", ".p3_token"} {
        path := filepath.Join(home, name)
        data, err := os.ReadFile(path)
        if err == nil {
            return strings.TrimSpace(string(data)), nil
        }
    }
    return "", fmt.Errorf("no token file found")
}

func main() {
    token, err := Authenticate(
        "https://user.patricbrc.org/authenticate",
        os.Getenv("BVBRC_USER"),
        os.Getenv("BVBRC_PASS"),
    )
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Authenticated as: %s\n", token.Username)
    fmt.Printf("Token expires: %s\n", token.Expiry.Format(time.RFC3339))

    if err := SaveToken(token); err != nil {
        log.Printf("Warning: could not save token: %v", err)
    }
}
```

### Complete Example: JSON-RPC Client Helper

```go
package bvbrc

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "math"
    "net/http"
    "sync/atomic"
    "time"
)

// Client provides methods to interact with BV-BRC JSON-RPC services.
type Client struct {
    httpClient *http.Client
    token      string
    config     ClientConfig
    requestID  atomic.Int64
}

// NewClient creates a new BV-BRC API client.
func NewClient(config ClientConfig) *Client {
    return &Client{
        httpClient: &http.Client{
            Timeout: config.Timeout,
        },
        token:  config.Token,
        config: config,
    }
}

// SetToken updates the authentication token.
func (c *Client) SetToken(token string) {
    c.token = token
}

// nextID generates a unique request ID.
func (c *Client) nextID() string {
    id := c.requestID.Add(1)
    return fmt.Sprintf("req-%d", id)
}

// RPCRequest is a JSON-RPC 1.1 request.
type RPCRequest struct {
    ID      string        `json:"id"`
    Method  string        `json:"method"`
    Version string        `json:"version"`
    Params  []interface{} `json:"params"`
}

// RPCResponse is a JSON-RPC 1.1 response.
type RPCResponse struct {
    ID      string          `json:"id"`
    Version string          `json:"version"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
    Name    string      `json:"name"`
    Code    int         `json:"code"`
    Message string      `json:"message"`
    Data    interface{} `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
    return fmt.Sprintf("RPC error %d (%s): %s", e.Code, e.Name, e.Message)
}

// call executes a JSON-RPC call against the specified service URL.
func (c *Client) call(serviceURL, method string, params []interface{}) (*RPCResponse, error) {
    req := RPCRequest{
        ID:      c.nextID(),
        Method:  method,
        Version: "1.1",
        Params:  params,
    }

    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshaling request: %w", err)
    }

    var lastErr error
    for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
        if attempt > 0 {
            delay := c.config.RetryDelay * time.Duration(math.Pow(2, float64(attempt-1)))
            time.Sleep(delay)
        }

        httpReq, err := http.NewRequest("POST", serviceURL, bytes.NewReader(body))
        if err != nil {
            return nil, fmt.Errorf("creating HTTP request: %w", err)
        }

        httpReq.Header.Set("Content-Type", "application/json")
        if c.token != "" {
            httpReq.Header.Set("Authorization", c.token)
        }

        httpResp, err := c.httpClient.Do(httpReq)
        if err != nil {
            lastErr = fmt.Errorf("HTTP request failed: %w", err)
            continue
        }
        defer httpResp.Body.Close()

        respBody, err := io.ReadAll(httpResp.Body)
        if err != nil {
            lastErr = fmt.Errorf("reading response: %w", err)
            continue
        }

        if httpResp.StatusCode != http.StatusOK {
            lastErr = fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(respBody))
            if httpResp.StatusCode >= 400 && httpResp.StatusCode < 500 {
                // Client errors are not retryable
                return nil, lastErr
            }
            continue
        }

        var rpcResp RPCResponse
        if err := json.Unmarshal(respBody, &rpcResp); err != nil {
            return nil, fmt.Errorf("unmarshaling response: %w", err)
        }

        if rpcResp.Error != nil {
            return &rpcResp, rpcResp.Error
        }

        return &rpcResp, nil
    }

    return nil, fmt.Errorf("all retries exhausted: %w", lastErr)
}

// CallAppService makes a JSON-RPC call to the App Service.
func (c *Client) CallAppService(method string, params ...interface{}) (*RPCResponse, error) {
    return c.call(c.config.AppServiceURL, method, params)
}

// CallWorkspace makes a JSON-RPC call to the Workspace service.
func (c *Client) CallWorkspace(method string, params ...interface{}) (*RPCResponse, error) {
    return c.call(c.config.WorkspaceURL, method, params)
}
```

### Complete Example: Submit a Job

```go
package main

import (
    "encoding/json"
    "fmt"
    "log"
    "os"
)

func main() {
    // Load token from file
    token, err := LoadToken()
    if err != nil {
        log.Fatal("No auth token found. Please authenticate first.")
    }

    // Create client
    config := DefaultConfig()
    config.Token = token
    client := NewClient(config)

    // Define annotation parameters
    params := map[string]interface{}{
        "contigs":         "/user@patricbrc.org/home/my_contigs.fasta",
        "scientific_name": "Escherichia coli K-12 MG1655",
        "taxonomy_id":     511145,
        "code":            11,
        "domain":          "Bacteria",
        "recipe":          "default",
        "output_path":     "/user@patricbrc.org/home/",
        "output_file":     "ecoli_annotation",
    }

    // Submit the job
    resp, err := client.CallAppService(
        "AppService.start_app",
        "GenomeAnnotation",                  // app_id
        params,                               // parameters
        "/user@patricbrc.org/home/",         // output workspace path
    )
    if err != nil {
        log.Fatalf("Job submission failed: %v", err)
    }

    // Parse the result
    var results []Task
    if err := json.Unmarshal(resp.Result, &results); err != nil {
        log.Fatalf("Parsing response: %v", err)
    }

    if len(results) > 0 {
        task := results[0]
        fmt.Printf("Job submitted successfully!\n")
        fmt.Printf("  Task ID: %s\n", task.ID)
        fmt.Printf("  App: %s\n", task.App)
        fmt.Printf("  Status: %s\n", task.Status)
        fmt.Printf("  Submitted: %s\n", task.SubmitTime)
    }
}
```

### Complete Example: Check Job Status

```go
package main

import (
    "encoding/json"
    "fmt"
    "log"
    "os"
    "time"
)

func main() {
    if len(os.Args) < 2 {
        log.Fatal("Usage: checkjob <task_id>")
    }
    taskID := os.Args[1]

    token, err := LoadToken()
    if err != nil {
        log.Fatal("No auth token found")
    }

    config := DefaultConfig()
    config.Token = token
    client := NewClient(config)

    // Query the task
    resp, err := client.CallAppService(
        "AppService.query_tasks",
        []string{taskID},
    )
    if err != nil {
        log.Fatalf("Query failed: %v", err)
    }

    var results []map[string]Task
    if err := json.Unmarshal(resp.Result, &results); err != nil {
        log.Fatalf("Parsing response: %v", err)
    }

    if len(results) > 0 {
        if task, ok := results[0][taskID]; ok {
            fmt.Printf("Task ID:     %s\n", task.ID)
            fmt.Printf("Application: %s\n", task.App)
            fmt.Printf("Status:      %s\n", task.Status)
            fmt.Printf("Owner:       %s\n", task.Owner)
            if task.SubmitTime != nil {
                fmt.Printf("Submitted:   %s\n", task.SubmitTime.Format(time.RFC3339))
            }
            if task.StartTime != nil {
                fmt.Printf("Started:     %s\n", task.StartTime.Format(time.RFC3339))
            }
            if task.CompletedTime != nil {
                fmt.Printf("Completed:   %s\n", task.CompletedTime.Format(time.RFC3339))
            }
            if task.OutputPath != "" {
                fmt.Printf("Output:      %s\n", task.OutputPath)
            }
        } else {
            fmt.Printf("Task %s not found\n", taskID)
        }
    }
}
```

### Complete Example: Poll Job Until Completion

```go
// WaitForTask polls a task until it reaches a terminal state.
func WaitForTask(client *Client, taskID string, maxWait time.Duration) (*Task, error) {
    deadline := time.Now().Add(maxWait)
    pollInterval := 10 * time.Second
    pollCount := 0

    for time.Now().Before(deadline) {
        resp, err := client.CallAppService(
            "AppService.query_tasks",
            []string{taskID},
        )
        if err != nil {
            return nil, fmt.Errorf("query failed: %w", err)
        }

        var results []map[string]Task
        if err := json.Unmarshal(resp.Result, &results); err != nil {
            return nil, fmt.Errorf("parsing response: %w", err)
        }

        if len(results) > 0 {
            if task, ok := results[0][taskID]; ok {
                switch task.Status {
                case TaskStateCompleted, TaskStateFailed, TaskStateDeleted:
                    return &task, nil
                }
            }
        }

        // Adaptive polling: increase interval over time
        pollCount++
        switch {
        case pollCount > 30: // After ~5 minutes at 10s intervals
            pollInterval = 60 * time.Second
        case pollCount > 10: // After ~100 seconds
            pollInterval = 30 * time.Second
        }

        time.Sleep(pollInterval)
    }

    return nil, fmt.Errorf("timeout waiting for task %s after %v", taskID, maxWait)
}
```

### Complete Example: List Workspace Contents

```go
package main

import (
    "encoding/json"
    "fmt"
    "log"
)

func main() {
    token, err := LoadToken()
    if err != nil {
        log.Fatal("No auth token found")
    }

    config := DefaultConfig()
    config.Token = token
    client := NewClient(config)

    // List contents of home workspace
    wsPath := fmt.Sprintf("/%s@patricbrc.org/home/", config.Username)

    resp, err := client.CallWorkspace(
        "Workspace.ls",
        map[string]interface{}{
            "paths": []string{wsPath},
        },
    )
    if err != nil {
        log.Fatalf("Workspace listing failed: %v", err)
    }

    // The response is a map of path -> array of object tuples
    var results []map[string][][]interface{}
    if err := json.Unmarshal(resp.Result, &results); err != nil {
        log.Fatalf("Parsing response: %v", err)
    }

    if len(results) > 0 {
        objects := results[0][wsPath]
        fmt.Printf("Contents of %s (%d items):\n\n", wsPath, len(objects))
        fmt.Printf("%-50s %-15s %10s\n", "Name", "Type", "Size")
        fmt.Printf("%s\n", "------------------------------------------------------------")

        for _, obj := range objects {
            path := obj[0].(string)
            objType := obj[1].(string)
            size := obj[6]

            fmt.Printf("%-50s %-15s %10v\n", path, objType, size)
        }
    }
}
```

### Complete Example: Upload a File to Workspace

```go
// UploadFile uploads a file to the BV-BRC workspace.
// For small files (< 10MB), inline upload is used.
// For large files, Shock upload is used.
func (c *Client) UploadFile(localPath, wsPath, objType string) error {
    data, err := os.ReadFile(localPath)
    if err != nil {
        return fmt.Errorf("reading file: %w", err)
    }

    const maxInlineSize = 10 * 1024 * 1024 // 10 MB

    if len(data) < maxInlineSize {
        // Inline upload for small files
        resp, err := c.CallWorkspace(
            "Workspace.create",
            map[string]interface{}{
                "objects": [][]interface{}{
                    {wsPath, objType, map[string]string{}, string(data)},
                },
                "overwrite": true,
            },
        )
        if err != nil {
            return fmt.Errorf("workspace create: %w", err)
        }
        _ = resp
        return nil
    }

    // Large file: create upload node, then upload to Shock
    resp, err := c.CallWorkspace(
        "Workspace.create",
        map[string]interface{}{
            "objects": [][]interface{}{
                {wsPath, objType, map[string]string{}, nil},
            },
            "createUploadNodes": true,
        },
    )
    if err != nil {
        return fmt.Errorf("creating upload node: %w", err)
    }

    // Parse response to get Shock node ID
    var results [][][]interface{}
    if err := json.Unmarshal(resp.Result, &results); err != nil {
        return fmt.Errorf("parsing upload node response: %w", err)
    }

    if len(results) == 0 || len(results[0]) == 0 || len(results[0][0]) < 11 {
        return fmt.Errorf("unexpected upload node response format")
    }

    shockNodeID, ok := results[0][0][10].(string)
    if !ok || shockNodeID == "" {
        return fmt.Errorf("no Shock node ID in response")
    }

    // Upload to Shock
    shockURL := fmt.Sprintf("%s/node/%s", c.config.ShockURL, shockNodeID)
    return c.uploadToShock(shockURL, localPath, data)
}

// uploadToShock uploads file data to a Shock node.
func (c *Client) uploadToShock(shockURL, filename string, data []byte) error {
    body := &bytes.Buffer{}
    writer := multipart.NewWriter(body)

    part, err := writer.CreateFormFile("upload", filepath.Base(filename))
    if err != nil {
        return fmt.Errorf("creating form file: %w", err)
    }

    if _, err := part.Write(data); err != nil {
        return fmt.Errorf("writing data: %w", err)
    }

    writer.Close()

    req, err := http.NewRequest("PUT", shockURL, body)
    if err != nil {
        return fmt.Errorf("creating request: %w", err)
    }

    req.Header.Set("Content-Type", writer.FormDataContentType())
    req.Header.Set("Authorization", "OAuth "+c.token)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("shock upload: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("shock upload failed (HTTP %d): %s", resp.StatusCode, respBody)
    }

    return nil
}
```

### Error Handling Patterns

```go
// IsAuthError checks if an error is an authentication/authorization error.
func IsAuthError(err error) bool {
    var rpcErr *RPCError
    if errors.As(err, &rpcErr) {
        return rpcErr.Code == -32400 || rpcErr.Code == 401
    }
    return false
}

// IsNotFoundError checks if an error indicates a resource was not found.
func IsNotFoundError(err error) bool {
    var rpcErr *RPCError
    if errors.As(err, &rpcErr) {
        return rpcErr.Code == -32601 || rpcErr.Code == 404
    }
    return false
}

// HandleRPCError provides structured error handling for JSON-RPC errors.
func HandleRPCError(err error) {
    var rpcErr *RPCError
    if errors.As(err, &rpcErr) {
        switch {
        case rpcErr.Code == -32700:
            log.Printf("Parse error: malformed JSON sent to server")
        case rpcErr.Code == -32600:
            log.Printf("Invalid request: check JSON-RPC envelope format")
        case rpcErr.Code == -32601:
            log.Printf("Method not found: %s", rpcErr.Message)
        case rpcErr.Code == -32602:
            log.Printf("Invalid parameters: %s", rpcErr.Message)
        case rpcErr.Code == -32603:
            log.Printf("Internal server error: %s", rpcErr.Message)
        case rpcErr.Code >= -32099 && rpcErr.Code <= -32000:
            log.Printf("Server error (%d): %s", rpcErr.Code, rpcErr.Message)
        default:
            log.Printf("Unknown RPC error (%d): %s", rpcErr.Code, rpcErr.Message)
        }
    } else {
        log.Printf("Non-RPC error: %v", err)
    }
}
```

---

## 9. Configuration Reference

### Service URLs/Endpoints

| Config Key | Default URL | Description |
|-----------|-------------|-------------|
| `app_service_url` | `https://p3.theseed.org/services/app_service` | App Service (job management) |
| `workspace_url` | `https://p3.theseed.org/services/Workspace` | Workspace file management |
| `data_api_url` | `https://www.bv-brc.org/api/` | Data API (Solr-backed REST) |
| `auth_url` | `https://user.patricbrc.org/authenticate` | Authentication endpoint |
| `shock_url` | `https://p3.theseed.org/services/shock_api` | Shock data storage |
| `homology_url` | `https://p3.theseed.org/services/homology_service` | Homology/BLAST service |
| `compare_region_url` | `https://p3.theseed.org/services/compare_region` | Compare region service |
| `min_hash_url` | `https://p3.theseed.org/services/minhash_service` | MinHash service |

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `BVBRC_USER` | BV-BRC username | `myuser` |
| `BVBRC_PASS` | BV-BRC password | `mypassword` |
| `BVBRC_TOKEN` | Pre-authenticated token | `un=myuser\|tokenid=...` |
| `BVBRC_API_URL` | Override App Service URL | `https://p3.theseed.org/services/app_service` |
| `BVBRC_WORKSPACE_URL` | Override Workspace URL | `https://p3.theseed.org/services/Workspace` |
| `P3_AUTH_TOKEN` | Alternative token env var (legacy PATRIC) | `un=myuser\|tokenid=...` |

### Config File Format

BV-BRC CLI tools typically use `~/.bvbrc/config.json` or `~/.patric/config.json`:

```json
{
    "appServiceURL": "https://p3.theseed.org/services/app_service",
    "workspaceURL": "https://p3.theseed.org/services/Workspace",
    "dataAPIURL": "https://www.bv-brc.org/api/",
    "authURL": "https://user.patricbrc.org/authenticate",
    "shockURL": "https://p3.theseed.org/services/shock_api",
    "username": "myuser",
    "token": "un=myuser|tokenid=..."
}
```

### Timeouts, Retries, and Connection Settings

| Setting | Recommended Value | Description |
|---------|-------------------|-------------|
| HTTP Timeout | 30 seconds | For metadata/RPC calls |
| Upload Timeout | 10 minutes | For Shock file uploads |
| Download Timeout | 10 minutes | For Shock file downloads |
| Max Retries | 3 | For transient failures |
| Retry Delay | 1 second initial | Exponential backoff (1s, 2s, 4s) |
| Connection Pool | 10 max idle | HTTP keep-alive connections |
| TLS | Required | All endpoints use HTTPS |
| Rate Limiting | 10 req/sec | Self-imposed to avoid server overload |

---

## 10. Endpoint Reference Table

### App Service Endpoints (`https://p3.theseed.org/services/app_service`)

| Method Name | Description | Parameters | Returns | Auth Required |
|------------|-------------|------------|---------|---------------|
| `AppService.start_app` | Submit a new analysis job | `[app_id: string, params: object, workspace_path: string]` | `Task` object | Yes |
| `AppService.start_app2` | Submit job with extra options | `[app_id: string, params: object, workspace_path: string, options: object]` | `Task` object | Yes |
| `AppService.query_tasks` | Get status of specific tasks | `[task_ids: string[]]` | `map[task_id]Task` | Yes |
| `AppService.query_task_summary` | Get task count summary | `[]` | `TaskSummary` | Yes |
| `AppService.query_task_details` | Get detailed task info | `[task_id: string]` | `Task` (detailed) | Yes |
| `AppService.enumerate_tasks` | List tasks with pagination | `[offset: int, count: int]` | `Task[]` | Yes |
| `AppService.enumerate_tasks_filtered` | List tasks with filters | `[offset: int, count: int, filter: object]` | `Task[]` | Yes |
| `AppService.kill_task` | Cancel a running/queued task | `[task_id: string]` | `int` (1=success) | Yes |
| `AppService.rerun_task` | Re-submit a task | `[task_id: string]` | `Task` object | Yes |
| `AppService.enumerate_apps` | List available applications | `[]` | `AppDescription[]` | No |
| `AppService.query_app_description` | Get app details | `[app_id: string]` | `AppDescription` | No |
| `AppService.query_app_log` | Get task execution log | `[task_id: string]` | Log text | Yes |

### Workspace Endpoints (`https://p3.theseed.org/services/Workspace`)

| Method Name | Description | Parameters | Returns | Auth Required |
|------------|-------------|------------|---------|---------------|
| `Workspace.create` | Create objects/folders/upload files | `[{objects, overwrite?, createUploadNodes?}]` | `WorkspaceObjectMeta[]` | Yes |
| `Workspace.get` | Retrieve object data and/or metadata | `[{objects: string[], metadata_only?: bool}]` | Object data/metadata | Yes |
| `Workspace.ls` | List directory contents | `[{paths: string[], recursive?: bool}]` | `map[path]WorkspaceObjectMeta[]` | Yes |
| `Workspace.delete` | Delete objects | `[{objects: string[], force?: bool, deleteDirectories?: bool}]` | Deleted paths | Yes |
| `Workspace.copy` | Copy objects | `[{objects: [[src,dst]], overwrite?: bool, recursive?: bool}]` | `WorkspaceObjectMeta[]` | Yes |
| `Workspace.move` | Move/rename objects | `[{objects: [[src,dst]], overwrite?: bool}]` | `WorkspaceObjectMeta[]` | Yes |
| `Workspace.set_permissions` | Set sharing permissions | `[{path: string, permissions: [[user, perm]]}]` | Updated permissions | Yes |
| `Workspace.list_permissions` | List permissions | `[{objects: string[]}]` | Permission entries | Yes |
| `Workspace.get_download_url` | Get download URL | `[{objects: string[]}]` | URL strings | Yes |
| `Workspace.list_workspaces` | List user's workspaces | `[]` | Workspace list | Yes |

### Data API Endpoints (`https://www.bv-brc.org/api/`)

The Data API uses REST (not JSON-RPC). Key endpoints:

| Path | Method | Description | Auth Required |
|------|--------|-------------|---------------|
| `/genome/` | GET/POST | Query genomes | No (public data) |
| `/genome_feature/` | GET/POST | Query genomic features | No |
| `/genome_sequence/` | GET/POST | Query genome sequences | No |
| `/sp_gene/` | GET/POST | Query specialty genes | No |
| `/taxonomy/` | GET/POST | Query taxonomy | No |
| `/pathway/` | GET/POST | Query pathways | No |
| `/subsystem/` | GET/POST | Query subsystems | No |
| `/protein_family_ref/` | GET/POST | Query protein families | No |
| `/antibiotic/` | GET/POST | Query antibiotics | No |
| `/genome_amr/` | GET/POST | Query AMR data | No |
| `/epitope/` | GET/POST | Query epitopes | No |
| `/experiment/` | GET/POST | Query experiments | No |
| `/bioset/` | GET/POST | Query biosets | No |
| `/surveillance/` | GET/POST | Query surveillance data | No |
| `/serology/` | GET/POST | Query serology data | No |

Data API queries use Solr-style syntax:

```
GET https://www.bv-brc.org/api/genome/?eq(genome_id,83332.12)&select(genome_id,genome_name,taxon_id)
```

Or with RQL (Resource Query Language):

```
GET https://www.bv-brc.org/api/genome/?and(eq(taxon_lineage_ids,562),gt(contigs,0))&limit(25)&sort(-date_inserted)
```

### Authentication Endpoints

| URL | Method | Description | Auth Required |
|-----|--------|-------------|---------------|
| `https://user.patricbrc.org/authenticate` | POST | Login and get token | No (credentials in body) |
| `https://user.patricbrc.org/register` | POST | Register new account | No |
| `https://user.patricbrc.org/user/<username>` | GET | Get user profile | Yes |

### Shock Endpoints (`https://p3.theseed.org/services/shock_api`)

| Path | Method | Description | Auth Required |
|------|--------|-------------|---------------|
| `/node` | POST | Create new data node | Yes |
| `/node/<id>` | GET | Download node data | Yes |
| `/node/<id>` | PUT | Upload data to node | Yes |
| `/node/<id>` | DELETE | Delete node | Yes |

---

## Appendix A: Data API Query Syntax (RQL/Solr)

The Data API supports Resource Query Language (RQL) for querying. Common operators:

| Operator | Example | Description |
|----------|---------|-------------|
| `eq` | `eq(genome_name,"E. coli")` | Equals |
| `ne` | `ne(genome_status,"WGS")` | Not equals |
| `gt` | `gt(contigs,10)` | Greater than |
| `lt` | `lt(gc_content,0.4)` | Less than |
| `ge` | `ge(genome_length,1000000)` | Greater than or equal |
| `le` | `le(contigs,100)` | Less than or equal |
| `in` | `in(genome_id,(id1,id2,id3))` | In set |
| `and` | `and(eq(a,1),eq(b,2))` | Logical AND |
| `or` | `or(eq(a,1),eq(a,2))` | Logical OR |
| `keyword` | `keyword(Escherichia)` | Full-text search |
| `select` | `select(genome_id,genome_name)` | Select fields |
| `limit` | `limit(25,0)` | Limit results (count, offset) |
| `sort` | `sort(+genome_name)` | Sort (+ asc, - desc) |
| `facet` | `facet((field,genome_status),(mincount,1))` | Faceted search |

### Response Headers

| Header | Description |
|--------|-------------|
| `Content-Range` | Indicates result range (e.g., `items 0-24/1532`) |
| `Content-Type` | `application/json` for JSON results |

### Accept Headers for Data API

| Accept Header | Format |
|---------------|--------|
| `application/json` | JSON (default) |
| `text/csv` | CSV |
| `text/tsv` | Tab-separated values |
| `application/vnd.bvbrc+json` | BV-BRC extended JSON |
| `application/solr+json` | Raw Solr response |

---

## Appendix B: GitHub Source Repositories

| Repository | Description | URL |
|-----------|-------------|-----|
| `wilke/BV-BRC-Go-SDK` | Go SDK for BV-BRC API | https://github.com/wilke/BV-BRC-Go-SDK |
| `BV-BRC/BV-BRC-Web` | Web application frontend | https://github.com/BV-BRC/BV-BRC-Web |
| `BV-BRC/app_service` | App Service (job management) | https://github.com/BV-BRC/app_service |
| `BV-BRC/Workspace` | Workspace service | https://github.com/BV-BRC/Workspace |
| `BV-BRC/p3_api` | Data API server | https://github.com/BV-BRC/p3_api |
| `BV-BRC/bvbrc_cli` | Command-line interface | https://github.com/BV-BRC/bvbrc_cli |
| `BV-BRC/homology_service` | Homology/BLAST service | https://github.com/BV-BRC/homology_service |
| `BV-BRC/p3_auth` | Authentication module | https://github.com/BV-BRC/p3_auth |
| `BV-BRC/Shock` | Shock data node service | https://github.com/BV-BRC/Shock |

> **Recommendation**: Clone `wilke/BV-BRC-Go-SDK` for Go-specific patterns and idioms.
> Clone `BV-BRC/app_service` and `BV-BRC/Workspace` for authoritative service-side
> method definitions and parameter schemas.

---

## Appendix C: Common Workflows

### Workflow 1: Assembly + Annotation Pipeline

```
1. Authenticate -> get token
2. Upload reads to workspace (Workspace.create with Shock upload)
3. Submit GenomeAssembly2 job (AppService.start_app)
4. Poll until completed (AppService.query_tasks)
5. Submit GenomeAnnotation job using assembly output
6. Poll until completed
7. Download results (Workspace.get or get_download_url)
```

### Workflow 2: Comprehensive Genome Analysis (Single Step)

```
1. Authenticate -> get token
2. Upload reads to workspace
3. Submit ComprehensiveGenomeAnalysis job (does assembly + annotation + analysis)
4. Poll until completed
5. Download results
```

### Workflow 3: Batch Job Submission

```
1. Authenticate -> get token
2. For each genome:
   a. Upload contigs/reads
   b. Submit job
   c. Record task ID
3. Poll all task IDs periodically
4. Collect results as jobs complete
```

---

## Appendix D: Troubleshooting

### Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| HTTP 401 | Invalid/expired token | Re-authenticate |
| HTTP 403 | Permission denied | Check workspace path ownership |
| RPC -32601 | Method not found | Verify method name spelling |
| RPC -32602 | Invalid params | Check parameter types and count |
| RPC -32603 | Internal error | Retry; check server status |
| "Token expired" | Token past expiry date | Obtain new token |
| "Workspace path not found" | Invalid workspace path | Verify path format: `/user@patricbrc.org/...` |
| Upload fails | File too large for inline | Use Shock upload (createUploadNodes) |

### Debugging Tips

1. **Enable HTTP logging**: Log all request/response bodies for debugging
2. **Verify token**: Parse the token and check the `expiry` field
3. **Check workspace paths**: Always use the full path format with `@patricbrc.org`
4. **Test with curl first**:

```bash
# Test authentication
curl -X POST -d "username=USER&password=PASS" \
  https://user.patricbrc.org/authenticate

# Test app listing (no auth needed)
curl -X POST -H "Content-Type: application/json" \
  -d '{"id":"1","method":"AppService.enumerate_apps","version":"1.1","params":[]}' \
  https://p3.theseed.org/services/app_service

# Test workspace listing
curl -X POST -H "Content-Type: application/json" \
  -H "Authorization: YOUR_TOKEN_HERE" \
  -d '{"id":"1","method":"Workspace.ls","version":"1.1","params":[{"paths":["/user@patricbrc.org/home/"]}]}' \
  https://p3.theseed.org/services/Workspace
```
