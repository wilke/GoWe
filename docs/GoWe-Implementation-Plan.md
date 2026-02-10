# GoWe Implementation Plan

## Context

GoWe is a Go-based workflow engine that uses CWL v1.2 YAML as its workflow definition format, submits bioinformatics jobs to BV-BRC via JSON-RPC, and provides scheduling, monitoring, and management. The project has comprehensive design docs (7,300 lines) but zero implementation code — only three stub `main.go` files. This plan uses an outside-in approach across 8 phases: skeleton API + CLI first (demoable UX in Phase 3), then fill in real implementations incrementally.

## Major Components

```
cmd/cli/main.go            → User-facing CLI (submit, status, cancel, logs)
cmd/server/main.go         → REST API server (persists state, receives submissions)
cmd/scheduler/main.go      → Scheduling loop (evaluates readiness, dispatches to executors)

pkg/model/                 → Public domain types (Workflow, Step, Tool, Task, Submission, errors)
pkg/cwl/                   → CWL v1.2 parsing types (CommandLineTool, Workflow, requirements)
pkg/bvbrc/                 → BV-BRC JSON-RPC client (auth, AppService, Workspace)

internal/logging/          → Logger setup (slog level/format), debug mode
internal/config/           → Server/scheduler config loading
internal/parser/           → CWL YAML parser, validator, DAG builder (server-side)
internal/bundle/           → CWL $graph bundler: resolves run: refs, packs into single document
internal/store/            → Persistence (Store interface + SQLite implementation)
internal/scheduler/        → Scheduler loop, dispatch, input resolution, retry logic
internal/executor/         → Executor interface + implementations (Local, BV-BRC)
internal/server/           → HTTP handlers, middleware, routing
internal/cli/              → CLI command implementations (uses bundle/ for submit)
internal/mcp/              → MCP server: exposes GoWe API as LLM tool-use interface
internal/toolgen/           → Auto-generate CWL tool wrappers from BV-BRC app schemas

cmd/mcp/main.go            → MCP server entry point (stdio transport)
```

**Why pkg/ vs internal/**: `pkg/` holds types that external tools could import (domain model, CWL types, BV-BRC client). `internal/` holds implementation details (handlers, scheduler loop, store queries).

## CWL-to-Submission Data Flow

### User's files

```
my-project/
├── pipeline.cwl              ← Workflow (references tools via "run:")
├── tools/
│   ├── bvbrc-assembly.cwl    ← Tool definition (CommandLineTool)
│   └── bvbrc-annotation.cwl  ← Tool definition (CommandLineTool)
└── job.yml                   ← Input values for this run
```

### Phase 1: CLI Bundling (user's machine)

The CLI reads all local files and produces a **self-contained packed CWL document**. The server never touches the user's filesystem.

```
gowe submit pipeline.cwl --inputs job.yml [--dry-run] [--debug]

CLI actions:
1. Read pipeline.cwl
2. Walk all "run:" references → resolve relative paths
   ├── run: tools/bvbrc-assembly.cwl → read file
   └── run: tools/bvbrc-annotation.cwl → read file
3. Bundle into CWL $graph packed format:
   - Each referenced Tool gets an "id" and goes into $graph[]
   - Workflow's run: fields become fragment refs (run: "#bvbrc-assembly")
   - Result: single YAML document with all Tools + Workflow inlined
4. Read job.yml → parse as input values map
5. POST /api/v1/workflows  body: {name, cwl: <packed document>}
   → receives workflow_id
6. POST /api/v1/submissions body: {workflow_id, inputs: <from job.yml>}
   → receives submission_id
```

**Packed $graph format** (what the server receives):

```yaml
cwlVersion: v1.2
$graph:
  - id: bvbrc-assembly
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAssembly2
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      read1: { type: File }
      read2: { type: File }
      recipe: { type: string, default: "auto" }
    outputs:
      contigs: { type: File, outputBinding: { glob: "*.contigs.fasta" } }

  - id: bvbrc-annotation
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAnnotation
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      contigs: { type: File }
      scientific_name: { type: string }
      taxonomy_id: { type: int }
    outputs:
      annotated_genome: { type: File, outputBinding: { glob: "*.genome" } }

  - id: main
    class: Workflow
    inputs:
      reads_r1: File
      reads_r2: File
      scientific_name: string
      taxonomy_id: int
    steps:
      assemble:
        run: "#bvbrc-assembly"
        in:
          read1: reads_r1
          read2: reads_r2
        out: [contigs]
      annotate:
        run: "#bvbrc-annotation"
        in:
          contigs: assemble/contigs
          scientific_name: scientific_name
          taxonomy_id: taxonomy_id
        out: [annotated_genome]
    outputs:
      genome:
        type: File
        outputSource: annotate/annotated_genome
```

### Phase 2: Server Parsing (POST /workflows)

```
Server receives packed CWL document
│
├── internal/parser/parser.go: ParseWorkflow()
│   ├── YAML unmarshal → detect $graph vs single document
│   ├── Extract all Process entries from $graph
│   │   ├── #bvbrc-assembly  → cwl.CommandLineTool
│   │   ├── #bvbrc-annotation → cwl.CommandLineTool
│   │   └── #main            → cwl.Workflow
│   ├── Resolve "run:" fragment refs → attach Tool inline to each Step
│   │   step "assemble".run = "#bvbrc-assembly" → resolved to cwl.CommandLineTool
│   │   step "annotate".run = "#bvbrc-annotation" → resolved to cwl.CommandLineTool
│   └── Return *cwl.Workflow with all Tools resolved inline
│
├── internal/parser/validator.go: Validate()
│   ├── Required fields present (cwlVersion, class, inputs, outputs, steps)
│   ├── All step inputs have valid sources
│   │   "reads_r1" → exists in Workflow.Inputs ✓
│   │   "assemble/contigs" → step "assemble" has output "contigs" ✓
│   ├── Input/output types are compatible
│   └── Return []ValidationError (empty = valid)
│
├── internal/parser/dag.go: BuildDAG()
│   ├── Walk step.in[].source references
│   │   step "assemble": sources = [reads_r1, reads_r2] → depends_on = [] (workflow inputs only)
│   │   step "annotate": sources = [assemble/contigs, ...] → depends_on = ["assemble"]
│   ├── Topological sort → execution order: [assemble, annotate]
│   └── Cycle detection → error if cycle found
│
├── internal/parser/parser.go: ToModel()
│   ├── cwl.Workflow → model.Workflow
│   ├── Each cwl.WorkflowStep → model.Step
│   │   ├── step.ToolInline = resolved Tool (with all inputs/outputs/hints)
│   │   ├── step.DependsOn = ["assemble"] (computed from DAG)
│   │   └── step.Hints.goweHint extracted → bvbrc_app_id, executor hint
│   ├── model.Workflow.Inputs[] with types
│   └── model.Workflow.Outputs[] with outputSource
│
└── internal/store: CreateWorkflow()
    ├── Persists raw CWL (for re-download/display)
    └── Persists parsed model.Workflow (for Submission creation)
```

### Phase 3: Submission Creation (POST /submissions)

```
Server receives {workflow_id, inputs}
│
├── store.GetWorkflow(workflow_id) → model.Workflow
│
├── Validate inputs against Workflow.Inputs[]
│   ├── "reads_r1": required=true, type=File → present, is File ✓
│   ├── "reads_r2": required=true, type=File → present, is File ✓
│   ├── "scientific_name": required=true, type=string → present ✓
│   └── "taxonomy_id": required=true, type=int → present, is int ✓
│
├── Create Submission
│   ├── submission.ID = uuid
│   ├── submission.WorkflowID = wf_...
│   ├── submission.State = PENDING
│   └── submission.Inputs = validated input values
│
├── Create Tasks from Steps (one Task per Step)
│   ├── Task A (from step "assemble"):
│   │   ├── task.ID = uuid
│   │   ├── task.StepID = "assemble"
│   │   ├── task.State = PENDING
│   │   ├── task.ExecutorType = "bvbrc" (from goweHint)
│   │   ├── task.BVBRCAppID = "GenomeAssembly2" (from goweHint)
│   │   ├── task.Tool = resolved CommandLineTool (inline)
│   │   ├── task.DependsOn = [] (no upstream tasks)
│   │   └── task.Inputs = NOT YET RESOLVED (happens at scheduling time)
│   │
│   └── Task B (from step "annotate"):
│       ├── task.ID = uuid
│       ├── task.StepID = "annotate"
│       ├── task.State = PENDING
│       ├── task.ExecutorType = "bvbrc" (from goweHint)
│       ├── task.BVBRCAppID = "GenomeAnnotation" (from goweHint)
│       ├── task.Tool = resolved CommandLineTool (inline)
│       ├── task.DependsOn = [task_A.ID]
│       └── task.Inputs = NOT YET RESOLVED (waiting for Task A outputs)
│
└── store.CreateSubmission() + store.CreateTask() × 2
```

### Phase 4: Scheduling and Execution

```
Scheduler loop (runs continuously)
│
├── Tick 1: Find PENDING Tasks with all dependencies satisfied
│   ├── Task A: DependsOn = [] → ALL SATISFIED ✓
│   └── Task B: DependsOn = [Task A] → Task A is PENDING ✗ (skip)
│
├── Resolve Task A inputs (now, at scheduling time):
│   ├── step.in "read1" source="reads_r1" → Submission.Inputs["reads_r1"]
│   │   = {class: File, location: "/user@bvbrc/home/reads/sample1_R1.fastq.gz"}
│   ├── step.in "read2" source="reads_r2" → Submission.Inputs["reads_r2"]
│   │   = {class: File, location: "/user@bvbrc/home/reads/sample1_R2.fastq.gz"}
│   └── task.Inputs = resolved values
│
├── Task A → SCHEDULED → Select BVBRCExecutor
│
├── BVBRCExecutor.Submit(Task A):
│   ├── query_app_description("GenomeAssembly2") → AppParameter[] (cached)
│   ├── Map task.Inputs → BV-BRC app params
│   │   read1 → paired_end_libs[0].read1 (workspace path)
│   │   read2 → paired_end_libs[0].read2 (workspace path)
│   ├── Validate against app schema
│   ├── start_app("GenomeAssembly2", params, workspace) → bvbrc_job_id
│   └── task.ExternalID = bvbrc_job_id
│
├── Task A → QUEUED → RUNNING (BV-BRC is processing)
│
├── ... Scheduler polls Executor.Status(Task A) ...
│
├── BV-BRC reports "completed"
│   ├── Task A → SUCCESS
│   └── Task A.Outputs = {contigs: {class: File,
│       location: "/user@bvbrc/home/assemblies/sample1/assembly.contigs.fasta"}}
│
├── Tick N: Re-evaluate PENDING Tasks
│   └── Task B: DependsOn = [Task A] → Task A is SUCCESS ✓ → SATISFIED
│
├── Resolve Task B inputs:
│   ├── step.in "contigs" source="assemble/contigs"
│   │   → Task A.Outputs["contigs"]  ← OUTPUT FROM UPSTREAM TASK
│   │   = {class: File, location: "/user@bvbrc/home/assemblies/sample1/assembly.contigs.fasta"}
│   ├── step.in "scientific_name" source="scientific_name" → Submission.Inputs
│   ├── step.in "taxonomy_id" source="taxonomy_id" → Submission.Inputs
│   └── task.Inputs = resolved values
│
├── Task B → SCHEDULED → BVBRCExecutor.Submit(Task B)
│   ├── start_app("GenomeAnnotation", {contigs: "/.../assembly.contigs.fasta", ...})
│   └── ... poll until completed ...
│
├── Task B → SUCCESS
│   └── Task B.Outputs = {annotated_genome: {class: File, location: "/.../annotation.genome"}}
│
└── All Tasks terminal → Submission → COMPLETED
    └── Submission.Outputs["genome"] = Task B.Outputs["annotated_genome"]
```

### Responsibility Summary

| Concern | Component | File |
|---------|-----------|------|
| Read .cwl files from disk | CLI | `internal/cli/submit.go` |
| Resolve `run:` references | CLI | `internal/cli/bundle.go` |
| Bundle into packed $graph | CLI | `internal/cli/bundle.go` |
| Read job.yml as inputs | CLI | `internal/cli/submit.go` |
| Parse packed CWL → cwl types | Server | `internal/parser/parser.go` |
| Validate CWL structure | Server | `internal/parser/validator.go` |
| Build DAG, detect cycles | Server | `internal/parser/dag.go` |
| Extract goweHint | Server | `internal/parser/parser.go` |
| Convert cwl → model.Workflow | Server | `internal/parser/parser.go` |
| Validate inputs vs Workflow schema | Server | `internal/server/handler_submissions.go` |
| Create Tasks from Steps | Server | `internal/server/handler_submissions.go` |
| Resolve Task inputs at runtime | Scheduler | `internal/scheduler/dispatch.go` |
| Select Executor per Task | Scheduler | `internal/scheduler/dispatch.go` |
| Validate against BV-BRC app schema | Executor | `internal/executor/bvbrc.go` |
| Submit to BV-BRC (start_app) | Executor | `internal/executor/bvbrc.go` |
| Poll status, collect outputs | Executor | `internal/executor/bvbrc.go` |
| Finalize Submission state | Scheduler | `internal/scheduler/loop.go` |
| Generate CWL tool from app schema | Server | `internal/toolgen/toolgen.go` |
| Map app output patterns | Server | `internal/toolgen/outputs.go` |
| Proxy workspace listing | Server | `internal/server/handler_workspace.go` |
| Expose tools/resources via MCP | MCP Server | `internal/mcp/server.go` |
| Translate MCP tool calls → REST | MCP Server | `internal/mcp/tools.go` |
| Logger setup (level, format, debug) | All binaries | `internal/logging/logging.go` |
| Dry-run validation report | Server | `internal/server/handler_submissions.go` |
| CLI dry-run output formatting | CLI | `internal/cli/submit.go` |

## LLM Integration via MCP

### Why This Works

Three properties of the GoWe + BV-BRC stack create an ideal surface for LLM-driven workflow composition:

1. **Self-describing apps.** `enumerate_apps` returns 22+ apps. `query_app_description(app_id)` returns full `AppParameter[]` with types, required flags, defaults, and enum values. The LLM works from authoritative schemas, not hallucinated APIs.

2. **Structured output format.** CWL is declarative YAML with a well-defined schema. LLMs reliably generate valid structured YAML when given examples and constraints.

3. **Validation feedback loop.** GoWe's validate endpoint returns structured errors (`"source 'assemble/contig' does not match any step output (did you mean 'assemble/contigs'?)"`) that the LLM can fix iteratively — agentic self-correction with ground truth.

### Architecture: GoWe as MCP Server

GoWe exposes its REST API as an MCP (Model Context Protocol) server over stdio. Each API endpoint becomes a tool the LLM can call:

```
┌─────────────────────────────────────────────────────────────┐
│  LLM (Claude, GPT, etc.) via MCP                           │
│                                                             │
│  Tools (function calls):                                    │
│  ┌─────────────────┬──────────────────────────────────────┐ │
│  │ list_apps        │ GET /api/v1/apps                    │ │
│  │ get_app_schema   │ GET /api/v1/apps/{id}               │ │
│  │ generate_tool    │ GET /api/v1/apps/{id}/cwl-tool      │ │
│  │ list_workspace   │ BV-BRC Workspace.ls (proxied)       │ │
│  │ submit_workflow  │ POST /api/v1/workflows + submissions│ │
│  │ validate_workflow│ POST /api/v1/workflows/{id}/validate│ │
│  │ check_status     │ GET /api/v1/submissions/{id}        │ │
│  │ get_task_logs    │ GET /api/v1/.../tasks/{tid}/logs    │ │
│  │ cancel           │ PUT /api/v1/submissions/{id}/cancel │ │
│  └─────────────────┴──────────────────────────────────────┘ │
│                                                             │
│  Resources (context):                                       │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ App catalog with descriptions and parameter schemas     ││
│  │ CWL tool templates (auto-generated from app schemas)    ││
│  │ User's workspace file listing                           ││
│  │ GoWe vocabulary and CWL format reference                ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
            │ stdio (JSON-RPC 2.0)
            ▼
┌─────────────────────────────────────────────────────────────┐
│  cmd/mcp/main.go — MCP Server                              │
│  ├── internal/mcp/server.go    — MCP protocol, tool defs   │
│  ├── internal/mcp/tools.go     — Tool handlers → GoWe API  │
│  └── internal/mcp/resources.go — Resource providers         │
│            │ HTTP client                                    │
│            ▼                                                │
│  GoWe Server (cmd/server)                                   │
│  └── REST API (all existing endpoints)                      │
└─────────────────────────────────────────────────────────────┘
```

### Conversational Flow

```
User: "I have paired-end Illumina reads for E. coli.
       Assemble the genome, then annotate it."
       │
       ▼
LLM calls: list_apps()
  → sees GenomeAssembly2, GenomeAnnotation among 22 apps
       │
       ▼
LLM calls: get_app_schema("GenomeAssembly2")
  → paired_end_libs: array | recipe: enum [auto,spades,...] | trim: boolean
LLM calls: get_app_schema("GenomeAnnotation")
  → contigs: string (workspace path) | scientific_name: string | taxonomy_id: int
       │
       ▼
LLM calls: generate_tool("GenomeAssembly2")
  → returns pre-built CWL CommandLineTool with goweHint
LLM calls: generate_tool("GenomeAnnotation")
  → returns pre-built CWL CommandLineTool with goweHint
       │
       ▼
LLM calls: list_workspace("/user@bvbrc/home/reads/")
  → sees sample1_R1.fastq.gz, sample1_R2.fastq.gz
       │
       ▼
LLM generates: CWL Workflow YAML (wires tools via data-flow)
LLM generates: Job inputs YAML (using real workspace paths)
       │
       ▼
LLM calls: submit_workflow(cwl, inputs)
  → validates → if errors, fix and retry
  → submission_id returned
       │
       ▼
LLM calls: check_status(submission_id)  [periodic]
  → reports progress: "Assembly step completed (42 contigs, N50=245kb).
     Annotation step running..."
       │
       ▼
LLM calls: check_status(submission_id)
  → "Workflow completed. Annotated genome at
     /user@bvbrc/home/annotations/sample1/annotation.genome"
```

### Auto-Generated CWL Tool Wrappers

The key insight: **the LLM doesn't need to write CWL tool definitions from scratch**. GoWe auto-generates them from BV-BRC app schemas.

`GET /api/v1/apps/{app_id}/cwl-tool` returns a valid CWL CommandLineTool with:
- Inputs derived from `AppParameter[]` (types mapped: BV-BRC `enum` → CWL `string` with docs, BV-BRC `array` → CWL `File[]`, etc.)
- `goweHint` with the correct `bvbrc_app_id`
- `baseCommand: ["true"]` (placeholder — GoWe intercepts via goweHint)
- Documented inputs with descriptions from the app schema

This reduces the LLM's job to **pure composition** — wiring pre-built tools into a Workflow DAG based on user intent — rather than generating tool definitions.

```
internal/toolgen/toolgen.go:
  func GenerateCWLTool(schema AppDescription) ([]byte, error)
    │
    ├── Map each AppParameter to a CWL input:
    │   ├── type:"string"  → { type: string }
    │   ├── type:"int"     → { type: int }
    │   ├── type:"boolean" → { type: boolean }
    │   ├── type:"enum"    → { type: string, doc: "One of: auto, spades, ..." }
    │   ├── type:"array"   → { type: "File[]" } (for read libraries)
    │   └── required:false  → add "?" suffix (CWL optional type)
    │
    ├── Add goweHint:
    │   hints:
    │     goweHint:
    │       bvbrc_app_id: <schema.ID>
    │       executor: bvbrc
    │
    ├── Add outputs (from known output registry or generic):
    │   outputs:
    │     result: { type: Directory, outputBinding: { glob: "." } }
    │
    └── Marshal to YAML, return
```

### Output Binding Registry

CWL `outputBinding.glob` patterns require knowing what files each BV-BRC app produces. This isn't in `AppParameter[]`. GoWe maintains a registry:

```go
// internal/toolgen/outputs.go
var appOutputs = map[string][]CWLOutput{
    "GenomeAssembly2": {
        {ID: "contigs", Type: "File", Glob: "*.contigs.fasta"},
        {ID: "report", Type: "File", Glob: "*_assembly_report.html"},
    },
    "GenomeAnnotation": {
        {ID: "annotated_genome", Type: "File", Glob: "*.genome"},
        {ID: "annotation_report", Type: "File", Glob: "*_annotation_report.html"},
    },
    // ... other apps
}
```

For apps without a registry entry, the generated tool uses a generic `Directory` output. The registry is maintained manually (small — one entry per app) and updated when output patterns change.

### Workspace Browsing

For the LLM to pick real file paths (not hallucinated ones), GoWe proxies BV-BRC workspace operations:

| MCP Tool | GoWe Endpoint | Purpose |
|----------|---------------|---------|
| `list_workspace` | `GET /api/v1/workspace?path=/user@bvbrc/home/` | List user's files and folders |
| `workspace_info` | `GET /api/v1/workspace/info?path=...` | Get metadata for a workspace object |

These are thin proxies to `Workspace.ls` and `Workspace.get`, authenticated with the user's BV-BRC token.

### Authentication Flow

The MCP server receives the user's BV-BRC credentials via MCP configuration (environment variables or config file). All tool calls pass the auth token through to GoWe, which forwards it to BV-BRC.

```
MCP config:
{
  "mcpServers": {
    "gowe": {
      "command": "gowe-mcp",
      "env": {
        "GOWE_SERVER_URL": "http://localhost:8080",
        "BVBRC_TOKEN": "<user's BV-BRC auth token>"
      }
    }
  }
}
```

### What the LLM Generates vs What GoWe Provides

| Artifact | Who creates it | How |
|----------|---------------|-----|
| CWL Tool wrappers | **GoWe** (auto-generated) | `GET /api/v1/apps/{id}/cwl-tool` from live app schema |
| CWL Workflow | **LLM** (composed) | Wires tools via data-flow based on user intent |
| Job inputs | **LLM** (from context) | Uses workspace listing for real paths, app schema for valid values |
| Packed $graph | **GoWe server** (or CLI) | Bundles workflow + tools into single document |
| Validation | **GoWe** | Structured errors the LLM can fix |
| Execution | **GoWe** | Scheduler + BVBRCExecutor, fully automated |

---

## REST API Endpoints

All prefixed with `/api/v1`. JSON request/response bodies.

### Response Envelope

Every response uses a standard envelope:

```json
{
  "status": "ok",
  "request_id": "req_abc123",
  "timestamp": "2026-02-09T17:30:00Z",
  "data": { ... },
  "pagination": { ... },
  "error": null
}
```

| Field | Type | Present | Description |
|-------|------|---------|-------------|
| `status` | string | Always | `"ok"` or `"error"` |
| `request_id` | string | Always | Unique ID per request (for tracing/debugging) |
| `timestamp` | string | Always | ISO 8601 server time |
| `data` | object/array | On success | The response payload |
| `pagination` | object | On list endpoints | Pagination metadata |
| `error` | object | On error | `{"code": "NOT_FOUND", "message": "...", "details": [...]}` |

**Error codes**: `VALIDATION_ERROR`, `NOT_FOUND`, `CONFLICT`, `UNAUTHORIZED`, `INTERNAL_ERROR`

### Pagination

All list endpoints accept `?limit=N&offset=N` and return:

```json
{
  "pagination": {
    "total": 42,
    "limit": 20,
    "offset": 0,
    "has_more": true
  }
}
```

Defaults: `limit=20`, `offset=0`. Max limit: `100`.

---

### Self-Discovery (`GET /api/v1`)

Returns all available endpoints, their methods, and descriptions.

**Response:**

```json
{
  "status": "ok",
  "request_id": "req_001",
  "timestamp": "2026-02-09T17:30:00Z",
  "data": {
    "name": "GoWe API",
    "version": "v1",
    "description": "GoWe Workflow Engine — CWL-based workflow submission, scheduling, and management",
    "endpoints": [
      {
        "path": "/api/v1/workflows",
        "methods": ["GET", "POST"],
        "description": "Workflow definition management"
      },
      {
        "path": "/api/v1/workflows/{id}",
        "methods": ["GET", "PUT", "DELETE"],
        "description": "Single Workflow operations"
      },
      {
        "path": "/api/v1/workflows/{id}/validate",
        "methods": ["POST"],
        "description": "Validate a Workflow without persisting"
      },
      {
        "path": "/api/v1/submissions",
        "methods": ["GET", "POST"],
        "description": "Submission (run) management. POST accepts ?dry_run=true for validation without execution"
      },
      {
        "path": "/api/v1/submissions/{id}",
        "methods": ["GET"],
        "description": "Single Submission detail with Tasks"
      },
      {
        "path": "/api/v1/submissions/{id}/cancel",
        "methods": ["PUT"],
        "description": "Cancel a running Submission"
      },
      {
        "path": "/api/v1/submissions/{sid}/tasks",
        "methods": ["GET"],
        "description": "List Tasks in a Submission"
      },
      {
        "path": "/api/v1/submissions/{sid}/tasks/{tid}",
        "methods": ["GET"],
        "description": "Single Task detail"
      },
      {
        "path": "/api/v1/submissions/{sid}/tasks/{tid}/logs",
        "methods": ["GET"],
        "description": "Task stdout/stderr logs"
      },
      {
        "path": "/api/v1/apps",
        "methods": ["GET"],
        "description": "List available BV-BRC applications (cached)"
      },
      {
        "path": "/api/v1/apps/{app_id}",
        "methods": ["GET"],
        "description": "Get BV-BRC app parameter schema"
      },
      {
        "path": "/api/v1/apps/{app_id}/cwl-tool",
        "methods": ["GET"],
        "description": "Auto-generated CWL tool wrapper from app schema (for LLM composition)"
      },
      {
        "path": "/api/v1/workspace",
        "methods": ["GET"],
        "description": "Browse BV-BRC workspace contents (proxy)"
      },
      {
        "path": "/api/v1/health",
        "methods": ["GET"],
        "description": "Server health and version"
      }
    ]
  }
}
```

---

### Health (`GET /api/v1/health`)

```json
{
  "status": "ok",
  "request_id": "req_002",
  "timestamp": "2026-02-09T17:30:00Z",
  "data": {
    "status": "healthy",
    "version": "0.1.0",
    "go_version": "go1.21.4",
    "uptime": "2h15m30s",
    "scheduler": "running",
    "store": "connected",
    "executors": {
      "local": "available",
      "bvbrc": "available",
      "container": "unavailable"
    }
  }
}
```

---

### Workflows

#### `POST /api/v1/workflows` — Register a Workflow

**Request:**

```json
{
  "name": "assembly-annotation-pipeline",
  "description": "Assemble reads then annotate the genome",
  "cwl": "cwlVersion: v1.2\nclass: Workflow\n\ninputs:\n  reads_r1:\n    type: File\n  reads_r2:\n    type: File\n  scientific_name:\n    type: string\n  taxonomy_id:\n    type: int\n\nsteps:\n  assemble:\n    run: bvbrc-assembly.cwl\n    in:\n      read1: reads_r1\n      read2: reads_r2\n    out: [contigs]\n\n  annotate:\n    run: bvbrc-annotation.cwl\n    in:\n      contigs: assemble/contigs\n      scientific_name: scientific_name\n      taxonomy_id: taxonomy_id\n    out: [annotated_genome]\n\noutputs:\n  genome:\n    type: File\n    outputSource: annotate/annotated_genome"
}
```

**Response (201 Created):**

```json
{
  "status": "ok",
  "request_id": "req_010",
  "timestamp": "2026-02-09T17:31:00Z",
  "data": {
    "id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "name": "assembly-annotation-pipeline",
    "description": "Assemble reads then annotate the genome",
    "cwl_version": "v1.2",
    "inputs": [
      {"id": "reads_r1", "type": "File", "required": true},
      {"id": "reads_r2", "type": "File", "required": true},
      {"id": "scientific_name", "type": "string", "required": true},
      {"id": "taxonomy_id", "type": "int", "required": true}
    ],
    "outputs": [
      {"id": "genome", "type": "File", "output_source": "annotate/annotated_genome"}
    ],
    "steps": [
      {
        "id": "assemble",
        "tool_ref": "bvbrc-assembly.cwl",
        "depends_on": [],
        "in": [
          {"id": "read1", "source": "reads_r1"},
          {"id": "read2", "source": "reads_r2"}
        ],
        "out": ["contigs"]
      },
      {
        "id": "annotate",
        "tool_ref": "bvbrc-annotation.cwl",
        "depends_on": ["assemble"],
        "in": [
          {"id": "contigs", "source": "assemble/contigs"},
          {"id": "scientific_name", "source": "scientific_name"},
          {"id": "taxonomy_id", "source": "taxonomy_id"}
        ],
        "out": ["annotated_genome"]
      }
    ],
    "created_at": "2026-02-09T17:31:00Z",
    "updated_at": "2026-02-09T17:31:00Z"
  }
}
```

#### `GET /api/v1/workflows?limit=2&offset=0` — List Workflows

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_011",
  "timestamp": "2026-02-09T17:32:00Z",
  "data": [
    {
      "id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "name": "assembly-annotation-pipeline",
      "description": "Assemble reads then annotate the genome",
      "cwl_version": "v1.2",
      "step_count": 2,
      "created_at": "2026-02-09T17:31:00Z"
    },
    {
      "id": "wf_b2c3d4e5-f6a7-8901-bcde-f12345678901",
      "name": "taxonomic-classification",
      "description": "Classify metagenomic reads",
      "cwl_version": "v1.2",
      "step_count": 1,
      "created_at": "2026-02-09T16:00:00Z"
    }
  ],
  "pagination": {
    "total": 5,
    "limit": 2,
    "offset": 0,
    "has_more": true
  }
}
```

#### `POST /api/v1/workflows/{id}/validate` — Validate a Workflow

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_012",
  "timestamp": "2026-02-09T17:33:00Z",
  "data": {
    "valid": false,
    "errors": [
      {"path": "steps.annotate.in.contigs", "message": "source 'assemble/contig' does not match any step output (did you mean 'assemble/contigs'?)"}
    ],
    "warnings": [
      {"path": "steps.assemble.hints.goweHint", "message": "bvbrc_app_id 'GenomeAssembly2' not verified (BV-BRC unreachable)"}
    ]
  }
}
```

---

### Submissions

#### `POST /api/v1/submissions` — Create a Submission

**Request:**

```json
{
  "workflow_id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "inputs": {
    "reads_r1": {
      "class": "File",
      "location": "/user@bvbrc/home/reads/sample1_R1.fastq.gz"
    },
    "reads_r2": {
      "class": "File",
      "location": "/user@bvbrc/home/reads/sample1_R2.fastq.gz"
    },
    "scientific_name": "Escherichia coli K-12",
    "taxonomy_id": 83333
  },
  "labels": {
    "project": "ecoli-analysis",
    "sample": "sample1"
  }
}
```

**Response (201 Created):**

```json
{
  "status": "ok",
  "request_id": "req_020",
  "timestamp": "2026-02-09T17:35:00Z",
  "data": {
    "id": "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
    "workflow_id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "workflow_name": "assembly-annotation-pipeline",
    "state": "PENDING",
    "inputs": {
      "reads_r1": {"class": "File", "location": "/user@bvbrc/home/reads/sample1_R1.fastq.gz"},
      "reads_r2": {"class": "File", "location": "/user@bvbrc/home/reads/sample1_R2.fastq.gz"},
      "scientific_name": "Escherichia coli K-12",
      "taxonomy_id": 83333
    },
    "labels": {"project": "ecoli-analysis", "sample": "sample1"},
    "submitted_by": "user@bvbrc",
    "task_summary": {
      "total": 2,
      "pending": 2,
      "scheduled": 0,
      "queued": 0,
      "running": 0,
      "success": 0,
      "failed": 0,
      "skipped": 0
    },
    "created_at": "2026-02-09T17:35:00Z",
    "completed_at": null
  }
}
```

#### `GET /api/v1/submissions?state=RUNNING&limit=20&offset=0` — List Submissions

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_021",
  "timestamp": "2026-02-09T17:36:00Z",
  "data": [
    {
      "id": "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
      "workflow_id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "workflow_name": "assembly-annotation-pipeline",
      "state": "RUNNING",
      "labels": {"project": "ecoli-analysis", "sample": "sample1"},
      "submitted_by": "user@bvbrc",
      "task_summary": {"total": 2, "pending": 0, "running": 1, "success": 1, "failed": 0},
      "created_at": "2026-02-09T17:35:00Z",
      "completed_at": null
    }
  ],
  "pagination": {
    "total": 1,
    "limit": 20,
    "offset": 0,
    "has_more": false
  }
}
```

#### `GET /api/v1/submissions/{id}` — Get Submission Detail

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_022",
  "timestamp": "2026-02-09T17:40:00Z",
  "data": {
    "id": "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
    "workflow_id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "workflow_name": "assembly-annotation-pipeline",
    "state": "COMPLETED",
    "inputs": {
      "reads_r1": {"class": "File", "location": "/user@bvbrc/home/reads/sample1_R1.fastq.gz"},
      "reads_r2": {"class": "File", "location": "/user@bvbrc/home/reads/sample1_R2.fastq.gz"},
      "scientific_name": "Escherichia coli K-12",
      "taxonomy_id": 83333
    },
    "outputs": {
      "genome": {
        "class": "File",
        "location": "/user@bvbrc/home/annotations/sample1/annotation.genome"
      }
    },
    "labels": {"project": "ecoli-analysis", "sample": "sample1"},
    "submitted_by": "user@bvbrc",
    "task_summary": {"total": 2, "pending": 0, "running": 0, "success": 2, "failed": 0},
    "tasks": [
      {
        "id": "task_d4e5f6a7-b8c9-0123-def0-34567890abcd",
        "step_id": "assemble",
        "state": "SUCCESS",
        "executor_type": "bvbrc",
        "external_id": "bvbrc-job-uuid-001",
        "bvbrc_app_id": "GenomeAssembly2",
        "outputs": {
          "contigs": {
            "class": "File",
            "location": "/user@bvbrc/home/assemblies/sample1/sample1_assembly.contigs.fasta"
          }
        },
        "retry_count": 0,
        "created_at": "2026-02-09T17:35:00Z",
        "started_at": "2026-02-09T17:35:05Z",
        "completed_at": "2026-02-09T17:38:30Z"
      },
      {
        "id": "task_e5f6a7b8-c9d0-1234-ef01-4567890abcde",
        "step_id": "annotate",
        "state": "SUCCESS",
        "executor_type": "bvbrc",
        "external_id": "bvbrc-job-uuid-002",
        "bvbrc_app_id": "GenomeAnnotation",
        "outputs": {
          "annotated_genome": {
            "class": "File",
            "location": "/user@bvbrc/home/annotations/sample1/annotation.genome"
          }
        },
        "retry_count": 0,
        "created_at": "2026-02-09T17:38:31Z",
        "started_at": "2026-02-09T17:38:35Z",
        "completed_at": "2026-02-09T17:40:00Z"
      }
    ],
    "created_at": "2026-02-09T17:35:00Z",
    "completed_at": "2026-02-09T17:40:00Z"
  }
}
```

#### `PUT /api/v1/submissions/{id}/cancel` — Cancel Submission

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_023",
  "timestamp": "2026-02-09T17:41:00Z",
  "data": {
    "id": "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
    "state": "CANCELLED",
    "tasks_cancelled": 1,
    "tasks_already_completed": 1
  }
}
```

---

### Tasks

#### `GET /api/v1/submissions/{sid}/tasks` — List Tasks

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_030",
  "timestamp": "2026-02-09T17:40:00Z",
  "data": [
    {
      "id": "task_d4e5f6a7-b8c9-0123-def0-34567890abcd",
      "step_id": "assemble",
      "state": "SUCCESS",
      "executor_type": "bvbrc",
      "external_id": "bvbrc-job-uuid-001",
      "bvbrc_app_id": "GenomeAssembly2",
      "retry_count": 0,
      "created_at": "2026-02-09T17:35:00Z",
      "started_at": "2026-02-09T17:35:05Z",
      "completed_at": "2026-02-09T17:38:30Z"
    },
    {
      "id": "task_e5f6a7b8-c9d0-1234-ef01-4567890abcde",
      "step_id": "annotate",
      "state": "SUCCESS",
      "executor_type": "bvbrc",
      "external_id": "bvbrc-job-uuid-002",
      "bvbrc_app_id": "GenomeAnnotation",
      "retry_count": 0,
      "created_at": "2026-02-09T17:38:31Z",
      "started_at": "2026-02-09T17:38:35Z",
      "completed_at": "2026-02-09T17:40:00Z"
    }
  ],
  "pagination": {
    "total": 2,
    "limit": 20,
    "offset": 0,
    "has_more": false
  }
}
```

#### `GET /api/v1/submissions/{sid}/tasks/{tid}/logs` — Task Logs

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_031",
  "timestamp": "2026-02-09T17:41:00Z",
  "data": {
    "task_id": "task_d4e5f6a7-b8c9-0123-def0-34567890abcd",
    "step_id": "assemble",
    "stdout": "SPAdes v3.15.5\nAssembling reads...\n=== Assembly complete ===\nContigs: 42\nTotal length: 4,641,652 bp\nN50: 245,312\n",
    "stderr": "",
    "exit_code": 0
  }
}
```

---

### Apps (BV-BRC Proxy)

#### `GET /api/v1/apps` — List Available Apps

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_040",
  "timestamp": "2026-02-09T17:42:00Z",
  "data": [
    {
      "id": "GenomeAssembly2",
      "label": "Genome Assembly",
      "description": "Assemble reads into contigs using SPAdes, MEGAHIT, or other assemblers",
      "default_cpu": 8,
      "default_memory": "128G"
    },
    {
      "id": "GenomeAnnotation",
      "label": "Genome Annotation",
      "description": "Annotate a genome using RASTtk",
      "default_cpu": 4,
      "default_memory": "32G"
    },
    {
      "id": "ComprehensiveGenomeAnalysis",
      "label": "Comprehensive Genome Analysis",
      "description": "Assembly + Annotation + Analysis pipeline",
      "default_cpu": 8,
      "default_memory": "128G"
    }
  ],
  "pagination": {
    "total": 22,
    "limit": 20,
    "offset": 0,
    "has_more": true
  }
}
```

#### `GET /api/v1/apps/GenomeAssembly2` — App Schema Detail

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_041",
  "timestamp": "2026-02-09T17:42:00Z",
  "data": {
    "id": "GenomeAssembly2",
    "label": "Genome Assembly",
    "description": "Assemble reads into contigs using SPAdes, MEGAHIT, or other assemblers",
    "default_cpu": 8,
    "default_memory": "128G",
    "parameters": [
      {
        "id": "paired_end_libs",
        "label": "Paired End Libraries",
        "type": "array",
        "required": false,
        "description": "List of paired-end read library objects"
      },
      {
        "id": "recipe",
        "label": "Assembly Recipe",
        "type": "enum",
        "required": true,
        "default": "auto",
        "enum": ["auto", "unicycler", "spades", "megahit", "velvet", "miniasm", "canu"],
        "description": "Assembly algorithm to use"
      },
      {
        "id": "trim",
        "label": "Trim Reads",
        "type": "boolean",
        "required": false,
        "default": true,
        "description": "Trim adapters and low-quality bases before assembly"
      },
      {
        "id": "min_contig_len",
        "label": "Minimum Contig Length",
        "type": "int",
        "required": false,
        "default": 300,
        "description": "Minimum contig length to report"
      },
      {
        "id": "output_path",
        "label": "Output Path",
        "type": "string",
        "required": true,
        "description": "Workspace path for results"
      },
      {
        "id": "output_file",
        "label": "Output File Prefix",
        "type": "string",
        "required": true,
        "description": "Prefix for output file names"
      }
    ]
  }
}
```

#### `GET /api/v1/apps/{app_id}/cwl-tool` — Auto-Generated CWL Tool Wrapper

Returns a valid CWL CommandLineTool document generated from the BV-BRC app's parameter schema. Used by LLMs to compose workflows without writing tool definitions.

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_042",
  "timestamp": "2026-02-09T17:42:30Z",
  "data": {
    "app_id": "GenomeAssembly2",
    "cwl_tool": "cwlVersion: v1.2\nclass: CommandLineTool\n\nhints:\n  goweHint:\n    bvbrc_app_id: GenomeAssembly2\n    executor: bvbrc\n\nbaseCommand: [\"true\"]\n\ndoc: \"Genome Assembly — Assemble reads into contigs using SPAdes, MEGAHIT, or other assemblers\"\n\ninputs:\n  paired_end_libs:\n    type: File[]?\n    doc: \"Paired-end read library files\"\n  recipe:\n    type: string\n    default: \"auto\"\n    doc: \"Assembly algorithm. One of: auto, unicycler, spades, megahit, velvet, miniasm, canu\"\n  trim:\n    type: boolean?\n    default: true\n    doc: \"Trim adapters and low-quality bases before assembly\"\n  min_contig_len:\n    type: int?\n    default: 300\n    doc: \"Minimum contig length to report\"\n  output_path:\n    type: string\n    doc: \"Workspace path for results\"\n  output_file:\n    type: string\n    doc: \"Prefix for output file names\"\n\noutputs:\n  contigs:\n    type: File\n    outputBinding:\n      glob: \"*.contigs.fasta\"\n  report:\n    type: File\n    outputBinding:\n      glob: \"*_assembly_report.html\"\n",
    "generated_from_schema_version": "2026-02-09T12:00:00Z",
    "output_registry_hit": true
  }
}
```

**404 Response** (unknown app):

```json
{
  "status": "error",
  "request_id": "req_043",
  "timestamp": "2026-02-09T17:42:30Z",
  "data": null,
  "error": {
    "code": "NOT_FOUND",
    "message": "App 'UnknownApp' not found in BV-BRC"
  }
}
```

---

### Workspace (BV-BRC Proxy)

#### `GET /api/v1/workspace?path={workspace_path}` — List Workspace Contents

Proxies `Workspace.ls` to let LLMs and clients browse the user's BV-BRC workspace. Requires BV-BRC auth token in `Authorization` header.

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_050",
  "timestamp": "2026-02-09T17:43:00Z",
  "data": {
    "path": "/user@bvbrc/home/reads/",
    "objects": [
      {
        "name": "sample1_R1.fastq.gz",
        "type": "reads",
        "size": 1048576000,
        "created": "2026-02-01T10:00:00Z"
      },
      {
        "name": "sample1_R2.fastq.gz",
        "type": "reads",
        "size": 1073741824,
        "created": "2026-02-01T10:00:00Z"
      },
      {
        "name": "sample2/",
        "type": "folder",
        "size": 0,
        "created": "2026-02-05T14:30:00Z"
      }
    ]
  }
}
```

---

### Error Responses

**404 Not Found:**

```json
{
  "status": "error",
  "request_id": "req_099",
  "timestamp": "2026-02-09T17:45:00Z",
  "data": null,
  "error": {
    "code": "NOT_FOUND",
    "message": "Submission 'sub_nonexistent' not found"
  }
}
```

**400 Validation Error:**

```json
{
  "status": "error",
  "request_id": "req_098",
  "timestamp": "2026-02-09T17:45:00Z",
  "data": null,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Invalid submission request",
    "details": [
      {"field": "inputs.reads_r1", "message": "required input 'reads_r1' is missing"},
      {"field": "inputs.taxonomy_id", "message": "expected int, got string"}
    ]
  }
}
```

**401 Unauthorized:**

```json
{
  "status": "error",
  "request_id": "req_097",
  "timestamp": "2026-02-09T17:45:00Z",
  "data": null,
  "error": {
    "code": "UNAUTHORIZED",
    "message": "BV-BRC authentication required. Provide token via Authorization header."
  }
}
```

## Logging

All GoWe components use stdlib `log/slog` (Go 1.21+) — no third-party logging library.

### Log Levels

| Level | When | Example |
|-------|------|---------|
| `DEBUG` | Internal state transitions, SQL queries, JSON-RPC payloads, input resolution | `"resolving task input" step=annotate source=assemble/contigs` |
| `INFO` | Normal operations visible to operators | `"task submitted" task_id=task_abc executor=bvbrc app=GenomeAssembly2` |
| `WARN` | Recoverable issues, retries, degraded state | `"BV-BRC poll failed, will retry" task_id=task_abc err="timeout" attempt=2` |
| `ERROR` | Unrecoverable failures | `"task failed permanently" task_id=task_abc err="BV-BRC returned 500"` |

### Logger Setup

```go
// internal/logging/logging.go
func NewLogger(level slog.Level, format string) *slog.Logger
```

- **`--log-level`** flag on all binaries: `debug`, `info` (default), `warn`, `error`
- **`--log-format`** flag: `text` (default, human-readable) or `json` (structured, for log aggregation)
- Logger is created in `main()` and passed via dependency injection (constructor args), never a global
- Every component receives `*slog.Logger` and creates child loggers with component context:

```go
// Server handler
logger = logger.With("component", "server")

// Scheduler
logger = logger.With("component", "scheduler")

// Executor
logger = logger.With("component", "executor", "type", "bvbrc")
```

### Debug Mode

Enabled via `--log-level=debug` on any binary, or `--debug` shorthand flag. Effects:

| Component | Debug output |
|-----------|-------------|
| **Server** | Full request/response bodies, parsed CWL structure, DAG edges, SQL queries |
| **Scheduler** | Every tick: tasks evaluated, dependency check results, input resolution steps |
| **Executor (local)** | Command line constructed, env vars set, stdout/stderr streaming |
| **Executor (BV-BRC)** | Full JSON-RPC request/response bodies, auth token refresh, app schema cache hits/misses |
| **CLI** | Bundled CWL document (packed $graph), HTTP request/response, resolved file paths |
| **Parser** | YAML unmarshal results, type resolution, validator checks, DAG construction |
| **MCP Server** | All MCP JSON-RPC messages (stdin/stdout), tool call parameters, resource responses |

```
# Example debug output (text format)
time=2026-02-09T17:35:05Z level=DEBUG component=scheduler msg="evaluating task readiness" task_id=task_abc step=annotate depends_on=[task_xyz]
time=2026-02-09T17:35:05Z level=DEBUG component=scheduler msg="dependency not satisfied" task_id=task_abc dep=task_xyz dep_state=RUNNING
time=2026-02-09T17:35:10Z level=DEBUG component=executor type=bvbrc msg="JSON-RPC call" method=AppService.query_tasks params=["task_xyz_external"]
time=2026-02-09T17:35:10Z level=DEBUG component=executor type=bvbrc msg="JSON-RPC response" method=AppService.query_tasks status=completed elapsed=245ms
```

### Request Tracing

Every HTTP request generates a `request_id` (already in the response envelope). This ID propagates through all log entries for that request via `slog.With("request_id", reqID)`, enabling full request tracing through server → store → executor.

---

## Dry-Run Mode

Dry-run validates the entire submission pipeline without executing any tasks. Available in both CLI and API.

### CLI: `gowe submit --dry-run`

```
gowe submit pipeline.cwl --inputs job.yml --dry-run
```

**What happens:**

```
CLI dry-run:
1. Read and bundle CWL files (same as normal)     ✓ runs
2. POST /api/v1/workflows                         ✓ runs (registers workflow)
3. POST /api/v1/submissions?dry_run=true           ✓ runs (validates, does NOT persist)
4. Print validation result                         ✓ shows what would happen
```

**Output:**

```
Dry-run: assembly-annotation-pipeline
  Workflow: valid ✓
  Inputs:  4/4 provided ✓
  Steps:
    1. assemble → GenomeAssembly2 (bvbrc)
       Inputs: read1, read2, recipe=auto, trim=true
       Depends on: (none)
    2. annotate → GenomeAnnotation (bvbrc)
       Inputs: contigs ← assemble/contigs, scientific_name, taxonomy_id
       Depends on: assemble
  DAG: acyclic ✓
  Executor availability: bvbrc ✓
  BV-BRC app validation:
    GenomeAssembly2: schema fetched ✓, inputs compatible ✓
    GenomeAnnotation: schema fetched ✓, inputs compatible ✓

No submission created. Use without --dry-run to execute.
```

### API: `POST /api/v1/submissions?dry_run=true`

Same request body as a normal submission. Returns a validation report instead of creating a Submission.

**Response (200):**

```json
{
  "status": "ok",
  "request_id": "req_060",
  "timestamp": "2026-02-09T17:50:00Z",
  "data": {
    "dry_run": true,
    "valid": true,
    "workflow": {
      "id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "name": "assembly-annotation-pipeline"
    },
    "inputs_valid": true,
    "steps": [
      {
        "id": "assemble",
        "executor_type": "bvbrc",
        "bvbrc_app_id": "GenomeAssembly2",
        "depends_on": [],
        "app_schema_valid": true,
        "inputs_compatible": true
      },
      {
        "id": "annotate",
        "executor_type": "bvbrc",
        "bvbrc_app_id": "GenomeAnnotation",
        "depends_on": ["assemble"],
        "app_schema_valid": true,
        "inputs_compatible": true
      }
    ],
    "dag_acyclic": true,
    "execution_order": ["assemble", "annotate"],
    "executor_availability": {
      "bvbrc": "available"
    },
    "errors": [],
    "warnings": []
  }
}
```

**Dry-run with errors (200):**

```json
{
  "status": "ok",
  "request_id": "req_061",
  "data": {
    "dry_run": true,
    "valid": false,
    "errors": [
      {"path": "inputs.reads_r1", "message": "required input 'reads_r1' is missing"},
      {"path": "steps.annotate.bvbrc_app", "message": "GenomeAnnotation parameter 'taxonomy_id' expects int, got string"}
    ],
    "warnings": [
      {"path": "steps.assemble.hints.goweHint", "message": "BV-BRC app schema cached, last verified 2h ago"}
    ]
  }
}
```

### What Dry-Run Validates

| Check | Normal submit | Dry-run |
|-------|:---:|:---:|
| CWL syntax and structure | yes | yes |
| Input completeness and types | yes | yes |
| DAG cycle detection | yes | yes |
| Executor availability | yes | yes |
| BV-BRC app schema fetch + input compatibility | no (deferred to scheduling) | **yes** (eagerly) |
| BV-BRC workspace path existence | no | **yes** (checks paths exist) |
| Persist Submission + Tasks | yes | **no** |
| Dispatch to Scheduler | yes | **no** |

The key difference: dry-run **eagerly validates** things that normal submit defers to scheduling time (app schema checks, workspace path verification). This gives users confidence before committing to a run.

### Implementation

Dry-run logic lives in `internal/server/handler_submissions.go`:

```go
func (h *Handler) CreateSubmission(w http.ResponseWriter, r *http.Request) {
    dryRun := r.URL.Query().Get("dry_run") == "true"
    // ... parse request, validate inputs, build tasks ...
    if dryRun {
        report := h.buildDryRunReport(ctx, workflow, tasks)
        respond(w, http.StatusOK, report)
        return
    }
    // ... normal persist + enqueue ...
}
```

---

## Core Interfaces

```go
// Executor — pluggable backend that runs Tasks
type Executor interface {
    Type() model.ExecutorType
    Submit(ctx context.Context, task *model.Task) (externalID string, err error)
    Status(ctx context.Context, task *model.Task) (model.TaskState, error)
    Cancel(ctx context.Context, task *model.Task) error
    Logs(ctx context.Context, task *model.Task) (stdout, stderr string, err error)
}

// Store — persistence layer
type Store interface {
    // Workflow CRUD
    CreateWorkflow(ctx, *model.Workflow) error
    GetWorkflow(ctx, id) (*model.Workflow, error)
    ListWorkflows(ctx, ListOptions) ([]*model.Workflow, int, error)
    UpdateWorkflow(ctx, *model.Workflow) error
    DeleteWorkflow(ctx, id) error
    // Submission CRUD
    CreateSubmission(ctx, *model.Submission) error
    GetSubmission(ctx, id) (*model.Submission, error)
    ListSubmissions(ctx, ListOptions) ([]*model.Submission, int, error)
    UpdateSubmission(ctx, *model.Submission) error
    // Task operations
    CreateTask(ctx, *model.Task) error
    GetTask(ctx, id) (*model.Task, error)
    ListTasksBySubmission(ctx, submissionID) ([]*model.Task, error)
    UpdateTask(ctx, *model.Task) error
    GetTasksByState(ctx, TaskState) ([]*model.Task, error)
    // Lifecycle
    Close() error
    Migrate(ctx) error
}

// Scheduler — evaluates readiness, dispatches Tasks
type Scheduler interface {
    Start(ctx context.Context) error   // Blocks until ctx cancelled
    Stop() error
    Tick(ctx context.Context) error    // Single iteration (for testing)
}

// Parser — loads and validates CWL documents
type Parser interface {
    ParseWorkflow(ctx, data []byte) (*cwl.Workflow, error)
    ParseTool(ctx, data []byte) (cwl.Process, error)
    ToModel(ctx, *cwl.Workflow) (*model.Workflow, error)
}
```

**Reasoning**: Interfaces defined first enables mock-based testing from day one. Every component receives dependencies via constructor (no globals). Every method takes `context.Context` for cancellation.

## Dependencies (7 total)

| Library | Purpose | Why |
|---------|---------|-----|
| `github.com/go-chi/chi/v5` | HTTP router | Lightweight, stdlib-compatible, middleware chaining |
| `gopkg.in/yaml.v3` | YAML parsing | Standard Go YAML lib, handles CWL documents |
| `modernc.org/sqlite` | SQLite database | Pure-Go, no CGo, cross-compiles cleanly |
| `github.com/google/uuid` | UUID generation | Entity IDs |
| `github.com/spf13/cobra` | CLI framework | Subcommands, flags, help, completion |
| `github.com/google/go-cmp` | Test comparison | Readable struct diffs (test only) |
| `github.com/mark3labs/mcp-go` | MCP server | Go MCP SDK — stdio transport, tool/resource registration |

**Reasoning**: Minimal dependency footprint. No third-party logging library — stdlib `log/slog` (Go 1.21+) provides structured logging with levels (DEBUG/INFO/WARN/ERROR), text and JSON handlers, child loggers via `With()`, and `context.Context` integration. stdlib `net/http` for HTTP, stdlib `testing` for tests. Custom JSON-RPC 1.1 client (~60 lines) rather than importing a library (BV-BRC uses 1.1, not 2.0). MCP uses JSON-RPC 2.0 (different from BV-BRC's 1.1) — the `mcp-go` library handles protocol details, tool registration, and stdio transport.

## Build Phases (Outside-In: Skeleton → CLI → Fill In)

**Strategy**: Build the user-facing surface first (skeleton API + CLI), then fill in real implementations one handler at a time. This lets us demo the full workflow UX early and use the skeleton server as an API contract test.

### Phase 1: Types + Logging (Foundation)

**Build**: `internal/logging/logging.go` (slog setup: `NewLogger(level, format)`, `--debug` shorthand), `pkg/model/` (all domain structs, state enums, errors), core interfaces (`internal/store/store.go`, `internal/executor/executor.go`, `internal/scheduler/scheduler.go`).

**What this gives us**: The vocabulary both server and CLI compile against. No database, no HTTP — just types, enums, and the logger.

**Test**: Table-driven state transition tests. Logger creation with all level/format combinations.

**Done when**: `go build ./...` and `go test ./pkg/model/... ./internal/logging/...` pass. All domain types defined. Logger produces correct text/JSON output at all levels.

### Phase 2: Skeleton Server (API Contract)

**Build**: `internal/config/`, `internal/server/` (all handlers return canned JSON matching the plan's API examples), `cmd/server/main.go` (wire config → chi router → listen). No store, no parser, no scheduler — handlers return hardcoded responses using `pkg/model/` types.

Every endpoint from the API spec is implemented:
- `GET /api/v1` — self-discovery (static)
- `GET /api/v1/health` — health (static)
- `POST /api/v1/workflows` — accepts body, returns canned workflow with generated ID
- `GET /api/v1/workflows` — returns canned list
- `GET /api/v1/workflows/{id}` — returns canned workflow
- `POST /api/v1/workflows/{id}/validate` — returns canned validation result
- `POST /api/v1/submissions` — returns canned submission (supports `?dry_run=true`)
- `GET /api/v1/submissions` — returns canned list
- `GET /api/v1/submissions/{id}` — returns canned detail with tasks
- `PUT /api/v1/submissions/{id}/cancel` — returns canned cancel response
- `GET /api/v1/submissions/{sid}/tasks` — returns canned task list
- `GET /api/v1/submissions/{sid}/tasks/{tid}` — returns canned task detail
- `GET /api/v1/submissions/{sid}/tasks/{tid}/logs` — returns canned logs
- `GET /api/v1/apps` — returns canned app list
- `GET /api/v1/apps/{app_id}` — returns canned app schema
- `GET /api/v1/apps/{app_id}/cwl-tool` — returns canned CWL tool
- `GET /api/v1/workspace` — returns canned workspace listing

Includes: request logging middleware (logs method, path, status, duration at INFO; full bodies at DEBUG), request_id generation, response envelope, error responses (400/404/401).

**Test**: HTTP handler tests (`httptest`). Every endpoint returns valid JSON matching the response envelope. Error codes work. Request logging captured. `--debug` produces verbose output.

**Done when**: `go run ./cmd/server --debug` starts. Can curl every endpoint and get well-formed JSON responses. The API contract is locked down. Response shapes match the plan exactly.

### Phase 3: CLI + Bundler

**Build**: `pkg/cwl/` (CWL types needed for bundling), `internal/bundle/` (resolve `run:` refs, produce packed `$graph`), `internal/cli/` (all commands), `cmd/cli/main.go` (cobra root). `testdata/` (sample .cwl fixtures — both separate files and pre-packed).

**Commands**:
- `gowe login` — prompt for BV-BRC credentials, store token
- `gowe submit pipeline.cwl --inputs job.yml` — bundle CWL, POST to server, POST submission
- `gowe submit --dry-run` — bundle, POST with `?dry_run=true`, print validation report
- `gowe status <id>` — GET submission detail, print formatted status
- `gowe list` — GET submissions list, print table
- `gowe cancel <id>` — PUT cancel
- `gowe logs <id> [--task <tid>]` — GET task logs

**Global flags**: `--server` (default `http://localhost:8080`), `--debug`, `--log-level`, `--log-format`.

**Bundler**:
- Read workflow.cwl, walk `run:` references, resolve relative paths
- Pack into CWL `$graph` format with `#fragment` references
- Handle missing files gracefully with clear error messages

**Test**:
- Bundler: given `testdata/separate/workflow.cwl` + `testdata/separate/tools/*.cwl` → valid packed `$graph`.
- CLI commands: test against skeleton server (or `httptest` mock). Verify correct HTTP method, path, body for each command.
- `--dry-run` prints formatted validation report.
- `--debug` shows packed $graph, HTTP request/response.
- Missing file errors, server connection errors handled gracefully.

**Done when**: Full CLI UX works against the skeleton server. `gowe submit pipeline.cwl --inputs job.yml` → bundles files → posts to server → prints submission ID. `gowe status <id>` → prints formatted output. `gowe submit --dry-run --debug` → shows packed CWL and verbose HTTP trace. The complete user workflow is demoable.

### Phase 4: CWL Parser + Validation (Replace Skeleton Handlers)

**Build**: `internal/parser/` (parser, validator, DAG builder). Replace skeleton workflow handlers with real CWL parsing.

**What changes in the server**:
- `POST /api/v1/workflows` — now actually parses the CWL, validates structure, builds DAG, returns real parsed result (still no persistence — stores in memory)
- `POST /api/v1/workflows/{id}/validate` — real validation with structured errors and "did you mean?" suggestions
- `POST /api/v1/submissions?dry_run=true` — real input validation against workflow schema

**Test**:
- Parse packed $graph, resolve `#fragment` refs to inline Tools.
- Parse echo CommandLineTool. Parse 2-step Workflow, verify DAG edges.
- Parse goweHint, extract `bvbrc_app_id`. Reject cycles. Reject missing required fields.
- Validation errors include helpful messages (typo suggestions, type mismatches).
- CLI `--dry-run` now returns real validation against parsed CWL.

**Done when**: Submit real CWL via CLI → server parses it → returns real parsed workflow structure with steps, dependencies, inputs, outputs. Validation catches real errors. DAG cycle detection works.

### Phase 5: Store + Persistence (Replace In-Memory)

**Build**: `internal/store/sqlite.go` + `migrations.go` (SQLite implementation of Store interface).

**What changes in the server**:
- All handlers now persist to SQLite instead of returning canned data
- `POST /api/v1/workflows` — persists parsed workflow + raw CWL
- `GET /api/v1/workflows` — reads from database with pagination
- `POST /api/v1/submissions` — creates real Submission + Tasks in database
- All GET endpoints return real persisted data

Store logs SQL operations at DEBUG level, state transitions at INFO.

**Test**: SQLite CRUD with `:memory:` database. Migration idempotency. Pagination. Concurrent access. Handler tests with real Store replacing mocks.

**Done when**: CLI submit → server persists workflow + submission → CLI status reads back real data from database. Data survives server restart. `--debug` shows SQL queries.

### Phase 6: Scheduler + LocalExecutor

**Build**: `internal/executor/local.go` (os/exec), `internal/executor/registry.go`, `internal/scheduler/loop.go`, `internal/scheduler/dispatch.go`, `internal/scheduler/retry.go`, `cmd/scheduler/main.go`. Scheduler logs every tick at DEBUG (tasks evaluated, dependencies checked, dispatch decisions) and state transitions at INFO.

**Test**: LocalExecutor runs `echo hello`, captures stdout. Scheduler Tick() advances states correctly. Retry logic. Submission finalization. Debug log output verified for tick/dispatch cycle.

**Done when**: Submit a 2-step local CWL Workflow via CLI → Server persists → Scheduler runs both steps in order → Submission reaches COMPLETED → `gowe status` shows COMPLETED. With `--debug`, full tick-by-tick trace visible.

### Phase 7: BVBRCExecutor

**Build**: `pkg/bvbrc/` (client, auth, types, config), `internal/executor/bvbrc.go`, `internal/executor/poller.go`.

**Test**: Mock BV-BRC JSON-RPC server. App schema fetch + caching. Submit → poll → complete. BV-BRC state → GoWe state mapping. Debug logging of JSON-RPC payloads.

**Done when**: goweHint Workflow submits via CLI → server persists → scheduler dispatches to BVBRCExecutor → executor fetches schema, validates, calls start_app, polls to completion. Full end-to-end with mock BV-BRC.

### Phase 8: MCP Server + CWL Tool Generation

**Build**: `internal/toolgen/` (app schema → CWL tool generator, output registry), `internal/mcp/` (MCP protocol server, tool definitions, resource providers), `cmd/mcp/main.go` (stdio entry point), workspace proxy handler in `internal/server/`.

**Components**:

- **toolgen**: `GenerateCWLTool(AppDescription) → []byte` maps `AppParameter[]` to CWL inputs, injects `goweHint`, attaches output bindings from registry. Exposed via `GET /api/v1/apps/{id}/cwl-tool`.

- **output registry**: `map[string][]CWLOutput` — known output patterns per BV-BRC app. Falls back to generic `Directory` output for unknown apps.

- **workspace proxy**: `GET /api/v1/workspace?path=...` proxies `Workspace.ls` through GoWe's auth layer. Lets LLMs discover available data files.

- **MCP server**: JSON-RPC 2.0 over stdio. Defines tools (`list_apps`, `get_app_schema`, `generate_tool`, `list_workspace`, `submit_workflow`, `validate_workflow`, `check_status`, `get_task_logs`, `cancel`). Each tool handler calls the GoWe REST API via HTTP client. Provides resources (app catalog, CWL format reference, vocabulary).

**Test**:
- Tool generation: given a mock `AppDescription`, verify generated CWL parses correctly, has correct goweHint, correct types.
- Output registry: apps with entries get specific glob patterns; unknown apps get generic Directory output.
- Workspace proxy: mock BV-BRC Workspace.ls, verify GoWe returns correct listing.
- MCP server: mock GoWe API, send MCP tool calls via stdin, verify correct JSON-RPC responses on stdout.
- Integration: LLM-simulated flow — list_apps → get_schema → generate_tool → compose workflow → validate → submit → check_status.

**Done when**: `gowe-mcp` starts over stdio, an MCP client can list tools, call `generate_tool("GenomeAssembly2")` → get valid CWL, call `submit_workflow` → get a submission_id, call `check_status` → get status. Workspace browsing returns real (mocked) file listings.

### Phase Summary

```
Phase 1: Types + Logging          ─── foundation, everything compiles against this
Phase 2: Skeleton Server           ─── API contract, all endpoints return canned JSON
Phase 3: CLI + Bundler             ─── full UX works against skeleton (demoable)
  ── outside-in complete, now fill in real implementations ──
Phase 4: CWL Parser                ─── replace skeleton workflow handlers with real parsing
Phase 5: Store + Persistence       ─── replace in-memory with SQLite
Phase 6: Scheduler + LocalExecutor ─── real execution pipeline
Phase 7: BVBRCExecutor             ─── real BV-BRC integration
Phase 8: MCP Server + Tool Gen     ─── LLM integration
```

## Test Strategy Summary

| Layer | Approach | Mocks |
|-------|----------|-------|
| `internal/logging/` | Level/format combos, child logger context | None |
| `pkg/model/` | Table-driven, pure data | None |
| `pkg/cwl/` | Table-driven with YAML fixtures | None |
| `pkg/bvbrc/` | `httptest.NewServer` mock | Mock HTTP responses |
| `internal/parser/` | Table-driven with `testdata/*.cwl` | None |
| `internal/store/` | In-memory SQLite (`:memory:`) | None (real DB) |
| `internal/scheduler/` | Mock Store + Mock Executor | Both mocked |
| `internal/executor/local` | Real os/exec (`echo`, `cat`) | None (integration) |
| `internal/executor/bvbrc` | `httptest.NewServer` mock | Mock BV-BRC API |
| `internal/server/` (skeleton) | `httptest.NewRecorder` | None (canned responses) |
| `internal/server/` (real) | `httptest.NewRecorder` | Mock Store (then real Store) |
| `internal/cli/` | Mock HTTP server (skeleton) | Mock API |
| `internal/bundle/` | Table-driven with `testdata/*.cwl` | None |
| `internal/toolgen/` | Table-driven with mock `AppDescription` | None |
| `internal/mcp/` | Stdin/stdout pipe with mock GoWe API | Mock HTTP |
| Integration | Full stack, real SQLite, real HTTP | Only external services |

## Verification

After each phase:
1. `go build ./...` — compiles cleanly
2. `go test ./...` — all tests pass
3. `go vet ./...` — no issues
4. Manual smoke test per phase's "done when" criteria

End-to-end after Phase 3: CLI bundles CWL and submits to skeleton server — full UX flow demoable.
End-to-end after Phase 6: Submit a 2-step CWL workflow via CLI, server persists, scheduler runs both steps, submission completes — verified via `gowe status`.

## Key Reference Files

- [GoWe-Vocabulary.md](docs/GoWe-Vocabulary.md) — Authoritative domain terminology and state machines
- [BVBRC-API.md](docs/BVBRC-API.md) — JSON-RPC methods, Go structs, auth flow
- [CWL-Specs.md](docs/CWL-Specs.md) — CWL v1.2 types, parsing rules, Go struct pseudocode
- [Workflow-Engines-Comparison.md](docs/Workflow-Engines-Comparison.md) — Architecture diagram, scheduler design
- BV-BRC Go SDK at `../bvbrc/BV-BRC-Go-SDK/appservice/client.go` — Reference JSON-RPC 1.1 patterns
