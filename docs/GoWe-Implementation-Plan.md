# GoWe Implementation Plan

## Current Version: v0.9.0 (2026-02-18)

**Status**: 84/84 CWL v1.2 conformance tests passing (100%). Distributed worker execution operational.

## Context

GoWe is a Go-based workflow engine that uses CWL v1.2 YAML as its workflow definition format. It supports multiple execution backends: local process execution, Docker containers, distributed workers, and BV-BRC bioinformatics platform integration. The project evolved from an 8-phase outside-in development approach, with significant additions beyond the original plan including a standalone CWL runner and distributed worker system.

---

## Implementation Status Summary

### Phase Completion

| Phase | Description | Status | Notes |
|-------|-------------|--------|-------|
| **Phase 1** | Types + Logging | ✅ Complete | `pkg/model/`, `internal/logging/` |
| **Phase 2** | Server + API | ✅ Complete | Full REST API, simplified envelope |
| **Phase 3** | CLI + Bundler | ✅ Complete + Extended | Added `gowe run`, `gowe apps` |
| **Phase 4** | CWL Parser | ✅ Complete + Extended | Full CWL v1.2, expressions, 84/84 conformance |
| **Phase 5** | Store + Persistence | ✅ Complete | SQLite with workers table |
| **Phase 6** | Scheduler + LocalExecutor | ✅ Complete + Extended | Added Docker, Worker executors |
| **Phase 7** | BVBRCExecutor | ✅ Complete | JSON-RPC integration |
| **Phase 8** | MCP Server + Tool Gen | ⚠️ Partial (40%) | Tool gen exists, MCP not implemented |

### Beyond-Plan Additions

| Component | Description | Value |
|-----------|-------------|-------|
| `cmd/cwl-runner/` | Standalone CWL v1.2 reference runner | cwltest compatibility |
| `internal/cwlrunner/` | Full CWL execution engine (1500+ LOC) | 100% conformance |
| `internal/cwlexpr/` | JavaScript expression evaluator | CWL expressions |
| `internal/execution/` | Shared execution engine (2000+ LOC) | Code reuse |
| `cmd/worker/` | Remote worker daemon | Distributed execution |
| `internal/worker/` | Worker implementation | Pull-based tasks |
| Docker Compose | Multi-container test setup | Distributed testing |

---

## Architecture Overview

### Current Package Structure

```
GoWe/
├── cmd/                          # Executable binaries
│   ├── cli/                      # gowe CLI (user-facing)
│   ├── server/                   # HTTP server + scheduler
│   ├── worker/                   # Remote worker daemon
│   ├── cwl-runner/               # Standalone CWL runner
│   ├── gen-cwl-tools/            # CWL tool generator
│   ├── smoke-test/               # Integration test
│   └── verify-bvbrc/             # Token verification
│
├── pkg/                          # Public packages (importable)
│   ├── model/                    # Domain types (Task, Submission, Worker)
│   ├── cwl/                      # CWL v1.2 data structures
│   └── bvbrc/                    # BV-BRC API client
│
├── internal/                     # Private packages
│   ├── execution/                # Shared execution engine [NEW]
│   ├── worker/                   # Worker implementation [NEW]
│   ├── cwlrunner/                # CWL runner core [NEW]
│   ├── cwlexpr/                  # Expression evaluator [NEW]
│   ├── cmdline/                  # Command line builder [NEW]
│   ├── scheduler/                # Task scheduling
│   ├── executor/                 # Executor implementations
│   ├── parser/                   # CWL parsing + validation
│   ├── bundle/                   # CWL $graph bundler
│   ├── store/                    # SQLite persistence
│   ├── server/                   # HTTP handlers
│   ├── cli/                      # CLI commands
│   ├── config/                   # Configuration
│   ├── logging/                  # slog setup
│   ├── bvbrc/                    # BV-BRC integration
│   └── ui/                       # Web UI (optional)
│
├── Dockerfile                    # Server container
├── Dockerfile.worker             # Worker container
├── docker-compose.yml            # Distributed test setup
└── scripts/                      # Test and utility scripts
```

### System Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              USER INTERFACES                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐   ┌──────────────┐ │
│  │   gowe CLI   │   │  cwl-runner  │   │   Web UI     │   │  MCP Server  │ │
│  │              │   │  (standalone)│   │  (optional)  │   │  (planned)   │ │
│  └──────┬───────┘   └──────┬───────┘   └──────┬───────┘   └──────┬───────┘ │
│         │                  │                  │                  │          │
└─────────┼──────────────────┼──────────────────┼──────────────────┼──────────┘
          │                  │                  │                  │
          ▼                  ▼                  ▼                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              GOWE SERVER                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                         HTTP API (chi router)                           ││
│  │  /api/v1/workflows  /api/v1/submissions  /api/v1/workers  /api/v1/apps  ││
│  └───────────────────────────────┬─────────────────────────────────────────┘│
│                                  │                                           │
│  ┌───────────────┐    ┌──────────┴──────────┐    ┌───────────────────────┐  │
│  │    Parser     │    │      Handlers       │    │    Store (SQLite)     │  │
│  │  ┌─────────┐  │    │  ┌──────────────┐   │    │  ┌─────────────────┐  │  │
│  │  │validator│  │◄───│  │ submissions  │   │───►│  │   submissions   │  │  │
│  │  │  dag    │  │    │  │  workflows   │   │    │  │     tasks       │  │  │
│  │  │ parser  │  │    │  │   workers    │   │    │  │    workers      │  │  │
│  │  └─────────┘  │    │  └──────────────┘   │    │  │   workflows     │  │  │
│  └───────────────┘    └─────────────────────┘    │  └─────────────────┘  │  │
│                                                   └───────────────────────┘  │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           SCHEDULER                                    │  │
│  │                                                                        │  │
│  │   Phase 1: Advance Pending    ─►  Phase 2: Dispatch    ─►  Phase 3:   │  │
│  │   (resolve dependencies)          (submit to executor)     Poll Status │  │
│  │                                                                        │  │
│  │   Phase 4: Finalize           ◄─  Phase 5: Mark Retries               │  │
│  │   (complete submissions)          (handle failures)                    │  │
│  │                                                                        │  │
│  └───────────────────────────────────┬───────────────────────────────────┘  │
│                                      │                                       │
│  ┌───────────────────────────────────┴───────────────────────────────────┐  │
│  │                        EXECUTOR REGISTRY                               │  │
│  │                                                                        │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐   │  │
│  │  │    Local    │  │   Docker    │  │   Worker    │  │   BV-BRC    │   │  │
│  │  │  Executor   │  │  Executor   │  │  Executor   │  │  Executor   │   │  │
│  │  │  (os/exec)  │  │  (docker)   │  │  (proxy)    │  │  (JSON-RPC) │   │  │
│  │  └─────────────┘  └─────────────┘  └──────┬──────┘  └─────────────┘   │  │
│  │                                           │                            │  │
│  └───────────────────────────────────────────┼────────────────────────────┘  │
│                                              │                               │
└──────────────────────────────────────────────┼───────────────────────────────┘
                                               │
                                               ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           REMOTE WORKERS                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────────┐│
│  │                          Worker Daemon                                   ││
│  │                                                                          ││
│  │   ┌─────────────┐    ┌─────────────┐    ┌─────────────────────────────┐ ││
│  │   │   Client    │    │  Task Loop  │    │     Execution Engine        │ ││
│  │   │  (HTTP)     │◄──►│  (poll/     │───►│  ┌─────────┐  ┌──────────┐  │ ││
│  │   │             │    │   execute)  │    │  │ Stager  │  │ Runtime  │  │ ││
│  │   │ - register  │    │             │    │  │ (files) │  │(local/   │  │ ││
│  │   │ - heartbeat │    │             │    │  │         │  │ docker)  │  │ ││
│  │   │ - checkout  │    │             │    │  └─────────┘  └──────────┘  │ ││
│  │   │ - complete  │    │             │    │                             │ ││
│  │   └─────────────┘    └─────────────┘    └─────────────────────────────┘ ││
│  │                                                                          ││
│  └─────────────────────────────────────────────────────────────────────────┘│
│                                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   Worker 1   │  │   Worker 2   │  │   Worker 3   │  │   Worker N   │     │
│  │  (runtime:   │  │  (runtime:   │  │  (runtime:   │  │  (runtime:   │     │
│  │   docker)    │  │   local)     │  │   apptainer) │  │   ...)       │     │
│  └──────────────┘  └──────────────┘  └──────────────┘  └──────────────┘     │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Module Documentation

### 1. `pkg/model` - Domain Types

**Purpose**: Core data structures shared across all components.

**Files**: `task.go`, `submission.go`, `workflow.go`, `worker.go`, `state.go`, `api.go`

#### Key Types

```go
// Task represents a single unit of work (one CWL step execution)
type Task struct {
    ID            string                 // Unique identifier
    SubmissionID  string                 // Parent submission
    StepID        string                 // CWL step name
    State         TaskState              // PENDING, SCHEDULED, QUEUED, RUNNING, SUCCESS, FAILED, SKIPPED
    ExecutorType  ExecutorType           // local, docker, worker, bvbrc
    ExternalID    string                 // External job ID (BV-BRC job ID, etc.)

    // Execution data
    Tool          map[string]any         // CWL CommandLineTool definition
    Job           map[string]any         // Resolved input values
    Inputs        map[string]any         // Legacy: raw inputs
    Outputs       map[string]any         // Collected outputs
    RuntimeHints  *RuntimeHints          // Expression library, namespaces

    // Dependencies
    DependsOn     []string               // Task IDs this depends on

    // Lifecycle
    ExitCode      int
    Stdout        string
    Stderr        string
    RetryCount    int
    MaxRetries    int
    CreatedAt     time.Time
    StartedAt     *time.Time
    CompletedAt   *time.Time
}

// TaskState enumeration
const (
    TaskStatePending   TaskState = "PENDING"    // Waiting for dependencies
    TaskStateScheduled TaskState = "SCHEDULED"  // Dependencies met, ready to dispatch
    TaskStateQueued    TaskState = "QUEUED"     // Sent to executor, awaiting start
    TaskStateRunning   TaskState = "RUNNING"    // Actively executing
    TaskStateSuccess   TaskState = "SUCCESS"    // Completed successfully
    TaskStateFailed    TaskState = "FAILED"     // Execution failed
    TaskStateSkipped   TaskState = "SKIPPED"    // Skipped (dependency failed)
    TaskStateRetrying  TaskState = "RETRYING"   // Marked for retry
)
```

#### State Machine

```
                    ┌──────────────────────────────────────────┐
                    │                                          │
                    ▼                                          │
┌─────────┐    ┌───────────┐    ┌────────┐    ┌─────────┐    │
│ PENDING │───►│ SCHEDULED │───►│ QUEUED │───►│ RUNNING │────┤
└────┬────┘    └───────────┘    └────────┘    └────┬────┘    │
     │                                              │         │
     │ (dependency                                  │         │
     │  failed)                                     │         │
     ▼                                              ▼         │
┌─────────┐                    ┌─────────┐    ┌─────────┐    │
│ SKIPPED │                    │ SUCCESS │    │ FAILED  │────┘
└─────────┘                    └─────────┘    └────┬────┘
                                                   │
                                                   │ (retries
                                                   │  remaining)
                                                   ▼
                                              ┌──────────┐
                                              │ RETRYING │──► PENDING
                                              └──────────┘
```

---

### 2. `pkg/cwl` - CWL Data Structures

**Purpose**: Complete CWL v1.2 type system representation.

**Files**: `tool.go`, `workflow.go`, `graph.go`, `binding.go`, `requirements.go`, `types.go`, `location.go`

#### Key Types

```go
// CommandLineTool represents a CWL CommandLineTool
type CommandLineTool struct {
    ID           string
    Class        string                    // "CommandLineTool"
    CWLVersion   string
    Doc          string
    BaseCommand  []string                  // e.g., ["echo"] or ["python", "-c"]
    Arguments    []Argument                // Static arguments
    Inputs       map[string]ToolInputParam
    Outputs      map[string]ToolOutputParam
    Stdin        string                    // Expression for stdin
    Stdout       string                    // Filename for stdout capture
    Stderr       string                    // Filename for stderr capture
    Requirements map[string]any
    Hints        map[string]any
    SuccessCodes []int                     // Exit codes considered success
}

// Workflow represents a CWL Workflow
type Workflow struct {
    ID           string
    Class        string                    // "Workflow"
    CWLVersion   string
    Doc          string
    Inputs       map[string]InputParam
    Outputs      map[string]OutputParam
    Steps        map[string]Step
    Hints        map[string]any            // Workflow-level hints (inherited by steps)
    Requirements map[string]any
}

// Step represents a workflow step
type Step struct {
    Run           string                   // Tool reference ("#tool-id" or path)
    In            map[string]StepInput     // Input mappings
    Out           []string                 // Output IDs
    Scatter       []string                 // Inputs to scatter over
    ScatterMethod string                   // dotproduct, flat_crossproduct, nested_crossproduct
    When          string                   // Conditional expression
    Hints         map[string]any
    Requirements  map[string]any
}

// GraphDocument represents a parsed CWL $graph document
type GraphDocument struct {
    CWLVersion  string
    Tools       map[string]*CommandLineTool
    Workflows   map[string]*Workflow
    Expressions map[string]*ExpressionTool
    Namespaces  map[string]string          // e.g., edam → http://edamontology.org/
    MainID      string                     // Primary process ID
}
```

#### Parsing Flow

```
┌──────────────────┐
│   CWL YAML/JSON  │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐     ┌───────────────────┐
│  Detect Format   │────►│  Single Document  │
│  ($graph or not) │     │  (bare tool/wf)   │
└────────┬─────────┘     └─────────┬─────────┘
         │                         │
         ▼                         │
┌──────────────────┐               │
│  Parse $graph    │               │
│  ├─ Tools        │               │
│  ├─ Workflows    │               │
│  └─ Expressions  │               │
└────────┬─────────┘               │
         │                         │
         ▼                         ▼
┌─────────────────────────────────────────┐
│           GraphDocument                  │
│  ┌─────────────────────────────────────┐│
│  │ Tools: map[string]*CommandLineTool  ││
│  │ Workflows: map[string]*Workflow     ││
│  │ MainID: "main" or first process     ││
│  └─────────────────────────────────────┘│
└─────────────────────────────────────────┘
```

---

### 3. `internal/parser` - CWL Parsing & Validation

**Purpose**: Parse YAML/JSON CWL documents, validate structure, build DAG.

**Files**: `parser.go`, `validator.go`, `dag.go`, `normalize.go`, `import.go`, `types.go`

#### Pseudo Code

```
FUNCTION ParseGraph(data []byte, baseDir string) -> GraphDocument:
    // Step 1: YAML unmarshal
    raw := yaml.Unmarshal(data)

    // Step 2: Resolve $import directives
    raw = resolveImports(raw, baseDir)

    // Step 3: Detect document type
    IF raw has "$graph":
        graph = parseGraphDocument(raw["$graph"])
    ELSE IF raw["class"] == "CommandLineTool":
        tool = parseCommandLineTool(raw)
        graph = wrapToolInGraph(tool)
    ELSE IF raw["class"] == "Workflow":
        workflow = parseWorkflow(raw)
        graph = wrapWorkflowInGraph(workflow)
    ELSE IF raw["class"] == "ExpressionTool":
        expr = parseExpressionTool(raw)
        graph = wrapExpressionInGraph(expr)

    // Step 4: Resolve step.run references
    FOR each workflow in graph.Workflows:
        FOR each step in workflow.Steps:
            IF step.Run starts with "#":
                toolID = step.Run[1:]  // Remove # prefix
                step.ResolvedTool = graph.Tools[toolID]

    RETURN graph

FUNCTION BuildDAG(workflow *Workflow) -> DAG:
    dag = new DAG()

    // Build dependency graph
    FOR each stepID, step in workflow.Steps:
        node = dag.AddNode(stepID)

        FOR each inputID, input in step.In:
            source = input.Source
            IF source contains "/":  // e.g., "step1/output"
                upstreamStep = source.split("/")[0]
                dag.AddEdge(upstreamStep -> stepID)

    // Topological sort
    dag.Order = topologicalSort(dag)

    // Cycle detection
    IF hasCycle(dag):
        RETURN error("workflow contains cycle")

    RETURN dag
```

#### Validation Flow

```
┌─────────────────────────────────────────────────────────────┐
│                      VALIDATION PHASES                       │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Phase 1: Structure Validation                               │
│  ├─ Required fields present (cwlVersion, class, inputs...)  │
│  ├─ Type syntax correct (string, File, int?, string[])      │
│  └─ No unknown fields in strict mode                         │
│                                                              │
│  Phase 2: Reference Validation                               │
│  ├─ All step.run references resolve to tools                │
│  ├─ All step.in sources exist (workflow inputs or step/out) │
│  └─ All outputSource references are valid                   │
│                                                              │
│  Phase 3: Type Compatibility                                 │
│  ├─ Input types match expected types                         │
│  ├─ Output types compatible with downstream inputs          │
│  └─ Optional vs required checking                           │
│                                                              │
│  Phase 4: DAG Validation                                     │
│  ├─ No cycles in step dependencies                          │
│  └─ Compute topological execution order                     │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

### 4. `internal/cwlexpr` - Expression Evaluator

**Purpose**: Evaluate CWL JavaScript expressions and parameter references.

**Files**: `evaluator.go`, `context.go`, `evaluator_test.go`

#### Expression Types

```
CWL Expression Types:
1. Parameter Reference: $(inputs.message)
2. Expression: ${ return inputs.x + inputs.y; }
3. Interpolation: "Hello $(inputs.name)!"
4. Object Literal: $({'key': inputs.value})
```

#### Pseudo Code

```
FUNCTION Evaluate(expr string, ctx *Context) -> (any, error):
    // Step 1: Detect expression type
    IF expr starts with "$(" and ends with ")":
        // Parameter reference or expression
        inner = expr[2:len(expr)-1]
        RETURN evaluateJS(inner, ctx)

    ELSE IF expr starts with "${" and ends with "}":
        // Code block
        code = expr[2:len(expr)-1]
        RETURN evaluateJS("(function(){" + code + "})()", ctx)

    ELSE IF expr contains "$(":
        // String interpolation
        RETURN interpolateString(expr, ctx)

    ELSE:
        // Literal value
        RETURN expr, nil

FUNCTION evaluateJS(code string, ctx *Context) -> any:
    // Create JavaScript VM (goja)
    vm = goja.New()

    // Inject context variables
    vm.Set("inputs", ctx.Inputs)
    vm.Set("self", ctx.Self)
    vm.Set("runtime", ctx.Runtime)

    // Evaluate
    result = vm.RunString(code)
    RETURN gojaToGo(result)

STRUCT Context:
    Inputs   map[string]any   // CWL inputs
    Self     any              // Current value (for valueFrom)
    Runtime  RuntimeContext   // runtime.cores, runtime.ram, etc.
```

#### Runtime Context

```
runtime = {
    cores:      int,        // Available CPU cores
    ram:        int,        // Available RAM in MB
    outdir:     string,     // Output directory path
    tmpdir:     string,     // Temporary directory path
    outdirSize: int,        // Minimum outdir space in MB
    tmpdirSize: int,        // Minimum tmpdir space in MB
    exitCode:   int         // Exit code (in outputEval only)
}
```

---

### 5. `internal/cmdline` - Command Line Builder

**Purpose**: Construct command line from CWL tool definition and inputs.

**Files**: `builder.go`, `builder_test.go`

#### Pseudo Code

```
FUNCTION BuildCommandLine(tool *CommandLineTool, inputs map[string]any, ctx *Context) -> []string:
    parts = []CommandPart{}

    // Step 1: Add base command
    FOR i, cmd in tool.BaseCommand:
        parts.append(CommandPart{position: 0, sortKey: i, value: cmd})

    // Step 2: Process arguments
    FOR i, arg in tool.Arguments:
        position = evaluatePosition(arg.Position, ctx)
        value = evaluateValueFrom(arg.ValueFrom, ctx)

        IF arg.Prefix != "":
            IF arg.Separate:
                parts.append(CommandPart{position: position, sortKey: i, value: arg.Prefix})
                parts.append(CommandPart{position: position, sortKey: i+0.5, value: value})
            ELSE:
                parts.append(CommandPart{position: position, sortKey: i, value: arg.Prefix + value})
        ELSE:
            parts.append(CommandPart{position: position, sortKey: i, value: value})

    // Step 3: Process inputs with inputBinding
    FOR inputID, param in tool.Inputs:
        IF param.InputBinding == nil:
            CONTINUE

        value = inputs[inputID]
        IF value == nil AND param.Default != nil:
            value = param.Default
        IF value == nil:
            CONTINUE

        binding = param.InputBinding
        position = evaluatePosition(binding.Position, ctx)

        // Handle different value types
        SWITCH type(value):
            CASE bool:
                IF value == true:
                    parts.append(CommandPart{position: position, value: binding.Prefix})

            CASE []any:  // Array
                FOR i, item in value:
                    IF binding.ItemSeparator != "":
                        // Join all items
                        joined = strings.Join(items, binding.ItemSeparator)
                        addWithPrefix(parts, position, binding.Prefix, joined)
                    ELSE:
                        // Each item separately
                        addWithPrefix(parts, position + i*0.001, binding.Prefix, item)

            CASE map[string]any:  // File/Directory
                path = value["path"]
                addWithPrefix(parts, position, binding.Prefix, path)

            DEFAULT:  // string, int, float
                addWithPrefix(parts, position, binding.Prefix, value)

    // Step 4: Sort by position, then by sort key
    sort(parts, by: (position, sortKey, isArgument))

    // Step 5: Extract values
    result = []string{}
    FOR part in parts:
        result.append(part.value)

    RETURN result
```

#### Position Sorting

```
Sorting Rules:
1. Position (ascending) - arguments/inputs sorted by position field
2. Arguments before inputs at same position
3. Sort key (insertion order) as tie-breaker

Example:
  baseCommand: [cmd]
  arguments:
    - position: 1, value: "-a"
    - position: 2, value: "-b"
  inputs:
    x: position: 1, prefix: "-x", value: "foo"
    y: position: 3, prefix: "-y", value: "bar"

Result: cmd -a -x foo -b -y bar
             │     │   │     │
        pos:1(arg) │ pos:2  pos:3
              pos:1(input)
```

---

### 6. `internal/execution` - Shared Execution Engine

**Purpose**: Unified tool execution logic shared between cwl-runner and workers.

**Files**: `engine.go`, `stager.go`, `runtime.go`, `local.go`, `docker.go`, `outputs.go`

#### Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          Engine                                      │
│                                                                      │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │                    ExecuteTool()                                │ │
│  │                                                                 │ │
│  │  1. Build RuntimeContext (cores, ram, outdir, tmpdir)          │ │
│  │  2. Create working directory                                    │ │
│  │  3. Stage input files via Stager                                │ │
│  │  4. Build command line via cmdline.Builder                      │ │
│  │  5. Execute via Runtime (local or docker)                       │ │
│  │  6. Validate exit code against successCodes                     │ │
│  │  7. Collect outputs via glob patterns                           │ │
│  │  8. Stage outputs to destination                                │ │
│  │                                                                 │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                      │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐  │
│  │    Stager    │    │   Runtime    │    │   Output Collector   │  │
│  │              │    │              │    │                      │  │
│  │  StageIn()   │    │  Run()       │    │  CollectOutputs()    │  │
│  │  StageOut()  │    │              │    │  ProcessCwlOutput()  │  │
│  │              │    │              │    │                      │  │
│  └──────────────┘    └──────────────┘    └──────────────────────┘  │
│         │                   │                       │               │
│         ▼                   ▼                       ▼               │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐  │
│  │ FileStager   │    │LocalRuntime  │    │  Glob + Checksum     │  │
│  │ (local fs)   │    │(os/exec)     │    │  computation         │  │
│  └──────────────┘    ├──────────────┤    └──────────────────────┘  │
│                      │DockerRuntime │                               │
│                      │(docker exec) │                               │
│                      └──────────────┘                               │
└─────────────────────────────────────────────────────────────────────┘
```

#### Pseudo Code

```
FUNCTION ExecuteTool(ctx, tool, inputs, workDir) -> ExecuteResult:
    // Step 1: Build runtime context
    runtime = buildRuntimeContext(tool)
    runtime.outdir = workDir + "/outputs"
    runtime.tmpdir = workDir + "/tmp"
    mkdir(runtime.outdir)
    mkdir(runtime.tmpdir)

    // Step 2: Stage input files
    stagedInputs = {}
    FOR inputID, value in inputs:
        IF isFileOrDirectory(value):
            localPath = stager.StageIn(ctx, value.location, workDir)
            stagedInputs[inputID] = copyWithPath(value, localPath)
        ELSE:
            stagedInputs[inputID] = value

    // Step 3: Build command line
    exprCtx = Context{Inputs: stagedInputs, Runtime: runtime}
    cmdLine = cmdline.Build(tool, stagedInputs, exprCtx)

    // Step 4: Determine execution mode
    IF hasDockerRequirement(tool):
        runtime := DockerRuntime{Image: getDockerImage(tool)}
    ELSE:
        runtime := LocalRuntime{}

    // Step 5: Build run spec
    spec = RunSpec{
        Command:  cmdLine,
        WorkDir:  workDir,
        Env:      buildEnvVars(tool),
        Stdin:    evaluateStdin(tool, exprCtx),
        Stdout:   evaluateStdout(tool, exprCtx),
        Stderr:   evaluateStderr(tool, exprCtx),
        Mounts:   collectInputMounts(stagedInputs),
    }

    // Step 6: Execute
    result = runtime.Run(ctx, spec)

    // Step 7: Validate exit code
    IF result.ExitCode NOT IN tool.SuccessCodes:
        RETURN error("exit code " + result.ExitCode)

    // Step 8: Collect outputs
    outputs = {}
    FOR outputID, param in tool.Outputs:
        IF param.Type == "stdout":
            outputs[outputID] = readStdoutFile(spec.Stdout, workDir)
        ELSE IF param.Type == "stderr":
            outputs[outputID] = readStderrFile(spec.Stderr, workDir)
        ELSE:
            globPattern = evaluateGlob(param.OutputBinding.Glob, exprCtx)
            files = glob(workDir, globPattern)
            outputs[outputID] = processFiles(files, param)

    // Step 9: Handle cwl.output.json
    IF exists(workDir + "/cwl.output.json"):
        cwlOutput = readJSON(workDir + "/cwl.output.json")
        outputs = mergeOutputs(outputs, cwlOutput)

    RETURN ExecuteResult{
        Outputs:  outputs,
        ExitCode: result.ExitCode,
        Stdout:   result.Stdout,
        Stderr:   result.Stderr,
    }
```

#### Stager Interface

```go
type Stager interface {
    // StageIn downloads a file from location to local path
    StageIn(ctx context.Context, location string, destPath string) error

    // StageOut uploads a local file and returns the new location URI
    StageOut(ctx context.Context, srcPath string, taskID string) (string, error)
}

// FileStager implements local filesystem staging
type FileStager struct {
    OutputDir string  // Base directory for outputs
}

// Future implementations:
// - ShockStager: Upload/download from Shock data store
// - S3Stager: Upload/download from S3
// - WorkspaceStager: BV-BRC Workspace integration
```

---

### 7. `internal/cwlrunner` - Standalone CWL Runner

**Purpose**: cwltest-compatible CWL v1.2 reference runner.

**Files**: `runner.go`, `execute.go`, `scatter.go`, `runner_test.go`

#### Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                            Runner                                    │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌────────────────┐                                                  │
│  │  LoadDocument  │──► Parse CWL YAML/JSON ──► GraphDocument        │
│  └────────────────┘                                                  │
│                                                                      │
│  ┌────────────────┐                                                  │
│  │   LoadInputs   │──► Parse job YAML/JSON ──► map[string]any       │
│  └────────────────┘                                                  │
│                                                                      │
│  ┌────────────────┐                                                  │
│  │    Execute     │                                                  │
│  │                │                                                  │
│  │  ├─ Workflow? ─┼──► executeWorkflow()                            │
│  │  │             │     ├─ Build DAG                                │
│  │  │             │     ├─ Topological sort                         │
│  │  │             │     └─ Execute steps in order                   │
│  │  │             │                                                  │
│  │  ├─ Tool? ────┼──► executeTool() via execution.Engine           │
│  │  │             │                                                  │
│  │  └─ ExprTool? ┼──► evaluateExpression()                          │
│  │                │                                                  │
│  └────────────────┘                                                  │
│                                                                      │
│  ┌────────────────┐                                                  │
│  │    Scatter     │──► Handle array inputs                          │
│  │                │     ├─ dotproduct                               │
│  │                │     ├─ flat_crossproduct                        │
│  │                │     └─ nested_crossproduct                      │
│  └────────────────┘                                                  │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

#### Workflow Execution Flow

```
FUNCTION executeWorkflow(ctx, workflow, inputs) -> outputs:
    // Step 1: Resolve workflow input defaults
    inputs = mergeDefaults(inputs, workflow.Inputs)

    // Step 2: Build DAG and get execution order
    dag = BuildDAG(workflow)
    order = dag.TopologicalOrder()

    // Step 3: Initialize step outputs map
    stepOutputs = {}

    // Step 4: Execute steps in order
    FOR stepID in order:
        step = workflow.Steps[stepID]

        // Resolve step inputs
        stepInputs = {}
        FOR inputID, input in step.In:
            IF input.Source contains "/":
                // Reference to upstream step output
                parts = input.Source.split("/")
                stepOutputs[parts[0]][parts[1]]
            ELSE:
                // Reference to workflow input
                stepInputs[inputID] = inputs[input.Source]

            // Apply default if missing
            IF stepInputs[inputID] == nil AND input.Default != nil:
                stepInputs[inputID] = input.Default

        // Check conditional
        IF step.When != "":
            whenResult = evaluate(step.When, stepInputs)
            IF whenResult == false:
                stepOutputs[stepID] = nil  // Step skipped
                CONTINUE

        // Handle scatter
        IF len(step.Scatter) > 0:
            stepOutputs[stepID] = executeScatter(ctx, step, stepInputs)
        ELSE:
            stepOutputs[stepID] = executeSingleStep(ctx, step, stepInputs)

    // Step 5: Collect workflow outputs
    outputs = {}
    FOR outputID, output in workflow.Outputs:
        source = output.OutputSource
        IF source contains "/":
            parts = source.split("/")
            outputs[outputID] = stepOutputs[parts[0]][parts[1]]
        ELSE:
            outputs[outputID] = inputs[source]  // Passthrough

    RETURN outputs
```

---

### 8. `internal/scheduler` - Task Scheduling

**Purpose**: Manage task lifecycle, resolve dependencies, dispatch to executors.

**Files**: `scheduler.go`, `loop.go`, `resolve.go`

#### Scheduler Loop

```
┌─────────────────────────────────────────────────────────────────────┐
│                        SCHEDULER TICK                                │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 1: Advance Pending Tasks                                │   │
│  │                                                                │   │
│  │  FOR each task WHERE state = PENDING:                         │   │
│  │    deps = store.GetTasks(task.DependsOn)                      │   │
│  │    IF all deps are SUCCESS:                                   │   │
│  │      resolveInputs(task, deps)                                │   │
│  │      task.State = SCHEDULED                                   │   │
│  │    ELSE IF any dep is FAILED or SKIPPED:                      │   │
│  │      task.State = SKIPPED                                     │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 2: Dispatch Scheduled Tasks                             │   │
│  │                                                                │   │
│  │  FOR each task WHERE state = SCHEDULED:                       │   │
│  │    executor = registry.Get(task.ExecutorType)                 │   │
│  │    externalID = executor.Submit(task)                         │   │
│  │    task.ExternalID = externalID                               │   │
│  │    task.State = QUEUED (async) or RUNNING (sync)              │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 2.5: Re-submit Retrying Tasks                           │   │
│  │                                                                │   │
│  │  FOR each task WHERE state = RETRYING:                        │   │
│  │    task.State = PENDING                                       │   │
│  │    task.RetryCount++                                          │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 3: Poll In-Flight Tasks                                 │   │
│  │                                                                │   │
│  │  FOR each task WHERE state IN (QUEUED, RUNNING):              │   │
│  │    executor = registry.Get(task.ExecutorType)                 │   │
│  │    newState = executor.Status(task)                           │   │
│  │    IF newState is terminal (SUCCESS, FAILED):                 │   │
│  │      task.State = newState                                    │   │
│  │      task.Outputs = collectOutputs(task)                      │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 4: Finalize Submissions                                 │   │
│  │                                                                │   │
│  │  FOR each submission WHERE state = RUNNING:                   │   │
│  │    tasks = store.GetTasksBySubmission(submission.ID)          │   │
│  │    IF all tasks are terminal:                                 │   │
│  │      IF any task FAILED:                                      │   │
│  │        submission.State = FAILED                              │   │
│  │      ELSE:                                                    │   │
│  │        submission.State = COMPLETED                           │   │
│  │        collectSubmissionOutputs(submission, tasks)            │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                              │                                       │
│                              ▼                                       │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ PHASE 5: Mark Retries                                         │   │
│  │                                                                │   │
│  │  FOR each task WHERE state = FAILED:                          │   │
│  │    IF task.RetryCount < task.MaxRetries:                      │   │
│  │      task.State = RETRYING                                    │   │
│  │                                                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

---

### 9. `internal/executor` - Execution Backends

**Purpose**: Pluggable executor implementations.

**Files**: `executor.go`, `registry.go`, `local.go`, `docker.go`, `worker.go`, `bvbrc.go`

#### Executor Interface

```go
type Executor interface {
    // Type returns the executor identifier
    Type() model.ExecutorType

    // Submit starts task execution, returns external ID
    Submit(ctx context.Context, task *model.Task) (string, error)

    // Status returns current task state
    Status(ctx context.Context, task *model.Task) (model.TaskState, error)

    // Cancel requests task cancellation
    Cancel(ctx context.Context, task *model.Task) error

    // Logs returns stdout/stderr
    Logs(ctx context.Context, task *model.Task) (string, string, error)
}
```

#### Executor Implementations

```
┌─────────────────────────────────────────────────────────────────────┐
│                       EXECUTOR REGISTRY                              │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌─────────────────┐                                                │
│  │  LocalExecutor  │  Runs tasks as local processes                 │
│  │                 │  - Uses os/exec                                │
│  │                 │  - Synchronous execution                       │
│  │                 │  - Legacy _base_command format                 │
│  └─────────────────┘                                                │
│                                                                      │
│  ┌─────────────────┐                                                │
│  │  DockerExecutor │  Runs tasks in Docker containers              │
│  │                 │  - Mounts input files as volumes               │
│  │                 │  - Uses docker exec                            │
│  │                 │  - Handles path translation                    │
│  └─────────────────┘                                                │
│                                                                      │
│  ┌─────────────────┐                                                │
│  │  WorkerExecutor │  Proxy for distributed workers                │
│  │                 │  - Transitions task to QUEUED                  │
│  │                 │  - Workers pull via API                        │
│  │                 │  - Status read from store                      │
│  └─────────────────┘                                                │
│                                                                      │
│  ┌─────────────────┐                                                │
│  │  BVBRCExecutor  │  BV-BRC platform integration                  │
│  │                 │  - JSON-RPC to AppService                      │
│  │                 │  - Polls query_tasks for status                │
│  │                 │  - Maps BV-BRC states to GoWe states          │
│  └─────────────────┘                                                │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

---

### 10. `internal/worker` - Remote Worker

**Purpose**: Pull tasks from server, execute locally, report results.

**Files**: `worker.go`, `client.go`, `runtime.go`, `stager.go`

#### Worker Loop

```
FUNCTION Run(ctx context.Context, cfg Config):
    // Step 1: Register with server
    workerID = client.Register(ctx, cfg.Name, hostname, cfg.Runtime)
    logger.Info("registered", "id", workerID)

    // Step 2: Start heartbeat + poll loop
    ticker = time.NewTicker(cfg.PollInterval)

    LOOP:
        SELECT:
            CASE <-ctx.Done():
                // Graceful shutdown
                client.Deregister(ctx)
                RETURN

            CASE <-ticker.C:
                // Heartbeat
                client.Heartbeat(ctx)

                // Check for work
                task = client.Checkout(ctx)
                IF task == nil:
                    CONTINUE

                // Execute task
                result = executeTask(ctx, task)

                // Report completion
                client.ReportComplete(ctx, task.ID, result)

FUNCTION executeTask(ctx context.Context, task *Task) -> Result:
    // Create working directory
    workDir = createTempDir()
    DEFER: cleanup(workDir)

    IF task.HasTool():
        // New path: use execution.Engine
        tool = parseToolFromMap(task.Tool)

        // Build engine with appropriate runtime
        engine = execution.NewEngine(stager, selectRuntime(cfg.Runtime))

        // Execute
        result = engine.ExecuteTool(ctx, tool, task.Job, workDir)

        RETURN Result{
            State:    SUCCESS if result.Error == nil else FAILED,
            Outputs:  result.Outputs,
            ExitCode: result.ExitCode,
            Stdout:   result.Stdout,
            Stderr:   result.Stderr,
        }
    ELSE:
        // Legacy path: extract _base_command
        cmd = task.Inputs["_base_command"]
        result = runtime.Run(ctx, RunSpec{Command: cmd, WorkDir: workDir})

        RETURN Result{
            State:    SUCCESS if result.ExitCode == 0 else FAILED,
            ExitCode: result.ExitCode,
            Stdout:   result.Stdout,
            Stderr:   result.Stderr,
        }
```

#### Worker API Endpoints

```
┌─────────────────────────────────────────────────────────────────────┐
│                        WORKER API                                    │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  POST /api/v1/workers/register                                      │
│  ├─ Request:  {name, hostname, runtime, cores, memory}              │
│  └─ Response: {id, ...}                                             │
│                                                                      │
│  PUT /api/v1/workers/{id}/heartbeat                                 │
│  ├─ Request:  {}                                                    │
│  └─ Response: 200 OK                                                │
│                                                                      │
│  GET /api/v1/workers/{id}/checkout                                  │
│  ├─ Request:  -                                                     │
│  └─ Response: Task (200) or 204 No Content                          │
│                                                                      │
│  PUT /api/v1/tasks/{id}/complete                                    │
│  ├─ Request:  {state, outputs, exit_code, stdout, stderr}           │
│  └─ Response: 200 OK                                                │
│                                                                      │
│  DELETE /api/v1/workers/{id}                                        │
│  ├─ Request:  -                                                     │
│  └─ Response: 200 OK (deregistered)                                 │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

---

### 11. `internal/store` - Persistence

**Purpose**: SQLite-based persistence for workflows, submissions, tasks, workers.

**Files**: `store.go`, `sqlite.go`, `migrations.go`

#### Schema

```sql
-- Workflows table
CREATE TABLE workflows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    cwl TEXT NOT NULL,          -- Raw CWL YAML/JSON
    cwl_version TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Submissions table
CREATE TABLE submissions (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id),
    state TEXT NOT NULL DEFAULT 'PENDING',
    inputs TEXT,                 -- JSON
    outputs TEXT,                -- JSON
    labels TEXT,                 -- JSON
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

-- Tasks table
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id),
    step_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'PENDING',
    executor_type TEXT,
    external_id TEXT,
    inputs TEXT,                 -- JSON (legacy)
    outputs TEXT,                -- JSON
    tool TEXT,                   -- JSON (CWL tool definition)
    job TEXT,                    -- JSON (resolved inputs)
    runtime_hints TEXT,          -- JSON
    depends_on TEXT,             -- JSON array of task IDs
    exit_code INTEGER,
    stdout TEXT,
    stderr TEXT,
    retry_count INTEGER DEFAULT 0,
    max_retries INTEGER DEFAULT 3,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP
);

-- Workers table
CREATE TABLE workers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    hostname TEXT,
    runtime TEXT,                -- docker, apptainer, none
    cores INTEGER,
    memory INTEGER,
    state TEXT DEFAULT 'ACTIVE',
    last_heartbeat TIMESTAMP,
    registered_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Indexes
CREATE INDEX idx_tasks_state ON tasks(state);
CREATE INDEX idx_tasks_submission ON tasks(submission_id);
CREATE INDEX idx_tasks_state_executor ON tasks(state, executor_type);
CREATE INDEX idx_submissions_state ON submissions(state);
CREATE INDEX idx_workers_state ON workers(state);
```

---

### 12. `internal/cli` - CLI Commands

**Purpose**: User-facing CLI commands.

**Files**: `root.go`, `run.go`, `submit.go`, `status.go`, `list.go`, `cancel.go`, `logs.go`, `login.go`, `apps.go`

#### Command Tree

```
gowe
├── run <cwl-file> [job-file]     # Submit + poll + output (cwltest compatible)
│   ├── --outdir <dir>            # Output directory
│   ├── --quiet                   # Suppress progress
│   └── --timeout <duration>      # Execution timeout
│
├── submit <cwl-file>             # Submit workflow
│   ├── --inputs <job-file>       # Input values
│   └── --dry-run                 # Validate only
│
├── status <submission-id>        # Query submission status
│
├── list                          # List submissions
│   ├── --state <state>           # Filter by state
│   └── --limit <n>               # Limit results
│
├── cancel <submission-id>        # Cancel submission
│
├── logs <submission-id>          # View task logs
│   └── --task <task-id>          # Specific task
│
├── login                         # BV-BRC authentication
│
└── apps                          # List BV-BRC apps
```

#### `gowe run` Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                         gowe run                                     │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  1. Bundle CWL Files                                                │
│     ├─ Read workflow.cwl                                            │
│     ├─ Resolve run: references                                      │
│     └─ Pack into $graph format                                      │
│                                                                      │
│  2. Read Job Inputs                                                  │
│     └─ Parse job.yml → map[string]any                               │
│                                                                      │
│  3. Create Workflow                                                  │
│     └─ POST /api/v1/workflows {name, cwl: packed}                   │
│                                                                      │
│  4. Create Submission                                                │
│     └─ POST /api/v1/submissions {workflow_id, inputs}               │
│                                                                      │
│  5. Poll Until Complete                                              │
│     └─ GET /api/v1/submissions/{id} (loop until terminal)           │
│                                                                      │
│  6. Collect Outputs                                                  │
│     ├─ Parse submission.Outputs                                     │
│     ├─ Stage files to --outdir                                      │
│     └─ Add checksum, size, basename                                 │
│                                                                      │
│  7. Output CWL JSON                                                  │
│     └─ Print JSON to stdout (cwltest format)                        │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Analysis & Recommendations

### Software Engineering Assessment

#### Strengths

| Aspect | Rating | Evidence |
|--------|--------|----------|
| Separation of Concerns | ⭐⭐⭐⭐⭐ | Clean pkg/internal split, interface-driven design |
| Code Reuse | ⭐⭐⭐⭐⭐ | `internal/execution` shared by cwl-runner and worker |
| Extensibility | ⭐⭐⭐⭐ | Executor registry, pluggable stagers and runtimes |
| Testability | ⭐⭐⭐⭐ | Interface-based, mock-friendly, 84/84 conformance |
| Error Handling | ⭐⭐⭐ | Wrapped errors, but some edge cases missing |

#### Areas for Improvement

| Issue | Location | Recommendation |
|-------|----------|----------------|
| Store interface too large | `internal/store/store.go` | Split into TaskStore, WorkerStore, SubmissionStore |
| Duplicate execution paths | `internal/cwlrunner` vs `internal/execution` | Consider consolidating |
| Task model overloaded | `pkg/model/task.go` | Separate Tool/Job from legacy Inputs |

### Performance Engineering Assessment

#### Critical Issues (Must Fix)

| Issue | File:Line | Impact | Fix |
|-------|-----------|--------|-----|
| HTTP connection pool | `worker/client.go:23-29` | Connection churn | Configure `http.Transport` |
| Worker blocks heartbeat | `worker/worker.go:126` | Worker marked dead | Separate heartbeat goroutine |
| SQLite pool not configured | `store/sqlite.go:24-45` | "database locked" | `SetMaxOpenConns(1)` |
| Defer cancel in select | `worker/worker.go:92-98` | Goroutine leak | Call `cancel()` explicitly |

#### Medium Priority

| Issue | File | Impact |
|-------|------|--------|
| N+1 queries in scheduler | `scheduler/loop.go:131-137` | Database load |
| Missing compound index | `store/migrations.go` | Slow checkout |
| JSON round-trip parsing | `worker/worker.go:346-357` | CPU overhead |
| Stop channel race | `scheduler/loop.go:75-78` | Panic on double stop |

#### Recommended Fixes

```go
// Fix 1: HTTP connection pool (worker/client.go)
httpClient: &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}

// Fix 2: SQLite connection pool (store/sqlite.go)
db.SetMaxOpenConns(1)       // SQLite handles one writer
db.SetMaxIdleConns(1)
db.SetConnMaxLifetime(time.Hour)

// Fix 3: Separate heartbeat goroutine (worker/worker.go)
func (w *Worker) Run(ctx context.Context, cfg Config) error {
    // Heartbeat in background
    go w.heartbeatLoop(ctx)
    // Task polling in main goroutine
    return w.taskLoop(ctx)
}

// Fix 4: Compound index (store/migrations.go)
CREATE INDEX idx_tasks_state_executor ON tasks(state, executor_type);
```

---

## Remaining Work

### Phase 8 Completion (MCP Server)

| Task | Priority | Effort |
|------|----------|--------|
| Create `internal/mcp/server.go` | High | 4 hours |
| Create `internal/mcp/tools.go` | High | 4 hours |
| Create `cmd/mcp/main.go` | High | 1 hour |
| Add `/api/v1/apps/{id}/cwl-tool` endpoint | High | 2 hours |
| Add `/api/v1/workspace` proxy endpoint | Medium | 2 hours |

### Production Readiness

| Task | Priority | Effort |
|------|----------|--------|
| Fix performance issues (above) | Critical | 2 hours |
| Output staging (#42) | High | 8 hours |
| Linux conformance testing (#41) | Medium | 2 hours |
| API response envelope standardization | Low | 4 hours |

---

## Verification Commands

```bash
# Build all binaries
go build ./...

# Run all tests
go test ./...

# Run CWL conformance tests
./scripts/run-conformance.sh

# Run distributed worker tests
./scripts/test-distributed.sh

# Start server with debug logging
./bin/gowe-server --debug

# Start worker
./bin/gowe-worker -server http://localhost:8080 -runtime docker

# Run single CWL tool
./bin/cwl-runner testdata/cwl-conformance/echo.cwl testdata/cwl-conformance/echo-job.yml

# Submit via CLI
./bin/gowe run workflow.cwl job.yml --outdir ./output
```

---

## Key Reference Files

- [GoWe-Vocabulary.md](GoWe-Vocabulary.md) — Domain terminology and state machines
- [BVBRC-API.md](BVBRC-API.md) — JSON-RPC methods, Go structs, auth flow
- [CWL-Specs.md](CWL-Specs.md) — CWL v1.2 types, parsing rules
- [tools/cli.md](tools/cli.md) — CLI command documentation
- [tools/server.md](tools/server.md) — Server documentation
- [tools/worker.md](tools/worker.md) — Worker documentation
- [tutorial.md](tutorial.md) — Getting started guide
