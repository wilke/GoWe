# GoWe Vocabulary and Design Concepts

> **Status**: Living document
> **Date**: 2026-02-09
> **Purpose**: Establish a clear, unambiguous vocabulary for GoWe. Every contributor, document, and line of code should use these terms consistently.

---

## Part 1: CWL as Workflow Definition Format — Evaluation

### Verdict: Strong fit with targeted extensions

CWL is a **good foundation** for GoWe's workflow definition layer. Here's why:

### What CWL Gets Right for GoWe

| CWL Strength | GoWe Benefit |
|---|---|
| **Declarative, no embedded code** | Workflow definitions are data (YAML/JSON), parseable without a language runtime |
| **DAG via data-flow** | Steps execute when inputs are satisfied — matches how BV-BRC jobs depend on each other |
| **Type system** | Inputs/outputs are typed (`File`, `Directory`, `string`, `int`, records, arrays) — enables validation before submission |
| **Container specification** | `DockerRequirement` maps naturally to BV-BRC app containers and AWE's `Dockerimage`/`DockerPull` |
| **Resource requirements** | `ResourceRequirement` (CPU, RAM, disk) maps to BV-BRC job resource parameters |
| **Scatter/gather** | Run the same step across an array of inputs — perfect for batch sample processing |
| **Conditional execution** | `when` field (v1.2) — skip steps based on runtime conditions |
| **Vendor neutral** | Users can test workflows locally with cwltool, then submit to GoWe for BV-BRC execution |
| **Existing tooling** | Parsers, validators, editors, and test suites already exist |
| **Bioinformatics community** | CWL is widely used in genomics — BV-BRC users likely already know it |

### Where CWL Needs Extension for GoWe

| Gap | GoWe Extension |
|---|---|
| CWL describes **command-line tools**, not **service calls** | GoWe needs a `BVBRCAppRequirement` (or hint) to map a CWL step to a BV-BRC JSON-RPC `start_app` call instead of a local command execution |
| CWL `File` uses local paths / URIs | GoWe must map CWL `File.location` to BV-BRC workspace paths (e.g., `/user@bvbrc/home/...`) |
| CWL has no concept of remote job polling | GoWe's executor layer handles this — transparent to the CWL definition |
| CWL `baseCommand` assumes a local binary | For BV-BRC steps, `baseCommand` is replaced by `app_id` in the GoWe execution layer |
| No built-in scheduling hints | GoWe can use CWL `hints` for scheduling metadata (priority, queue, deadline) |

### Proposed Approach

**Use CWL v1.2 as the workflow definition format** with GoWe-specific hints:

```yaml
cwlVersion: v1.2
class: Workflow

hints:
  # GoWe extension: declare this is a BV-BRC workflow
  goweHint:
    executor: bvbrc
    workspace_base: /user@bvbrc/home/projects/my-analysis

inputs:
  reads_r1: File
  reads_r2: File
  scientific_name: string
  taxonomy_id: int

steps:
  assemble:
    run: bvbrc-assembly.cwl      # GoWe-provided CWL tool wrapper
    in:
      read1: reads_r1
      read2: reads_r2
    out: [contigs]

  annotate:
    run: bvbrc-annotation.cwl
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

Where `bvbrc-assembly.cwl` is a GoWe-provided tool wrapper:

```yaml
cwlVersion: v1.2
class: CommandLineTool

hints:
  goweHint:
    bvbrc_app_id: GenomeAssembly2
    executor: bvbrc

baseCommand: ["true"]   # placeholder — GoWe intercepts via bvbrc_app_id

inputs:
  read1:
    type: File
    doc: "Forward reads"
  read2:
    type: File
    doc: "Reverse reads"
  recipe:
    type: string
    default: "auto"

outputs:
  contigs:
    type: File
    outputBinding:
      glob: "*.contigs.fasta"
```

This means:
- Standard CWL tooling can **validate** the workflow structure.
- `cwltool` can run it **locally** for testing (if `baseCommand` points to real tools).
- GoWe recognizes `goweHint.bvbrc_app_id` and routes execution to BV-BRC instead.
- Users write CWL once, run anywhere.

---

## Part 2: GoWe Vocabulary

### Core Terms

The following terms are **normative** for GoWe. Use them precisely.

---

### Workflow

> A **Workflow** is a declarative description of a computation as a directed acyclic graph (DAG) of Steps connected by data flow. A Workflow has typed Inputs and Outputs.

- **Format**: CWL v1.2 YAML/JSON document with `class: Workflow`
- **Contains**: Steps, Inputs, Outputs, Requirements, Hints
- **Does NOT contain**: Execution logic, scheduling decisions, or credentials
- **Analogy**: A blueprint. It describes *what* to compute, never *how* or *where*.

```
Workflow
├── Inputs          (typed parameters provided at submission time)
├── Outputs         (typed results produced when all steps complete)
├── Steps           (the nodes of the DAG)
│   ├── Step A
│   │   ├── run → Tool or sub-Workflow
│   │   ├── in  → data links from Inputs or other Steps
│   │   └── out → named outputs
│   └── Step B
│       ├── run → Tool
│       ├── in  → data links (e.g., step_a/output1)
│       └── out → named outputs
└── Requirements    (features the engine MUST support)
```

---

### Step

> A **Step** is a single node in a Workflow's DAG. It binds a Tool (or sub-Workflow) to specific input sources and declares which outputs to expose.

- A Step is **not** executable on its own — it exists only within a Workflow.
- Steps define **data dependencies** via their `in` mappings (e.g., `input_bam: align/aligned_bam`).
- Steps may **scatter** over array inputs to create parallel Tasks.
- Steps may be **conditional** (`when` expression).

| Step property | Purpose |
|---|---|
| `id` | Unique name within the Workflow |
| `run` | Reference to a Tool or sub-Workflow |
| `in` | Input bindings (source links) |
| `out` | Output names to expose |
| `scatter` | Input(s) to scatter over |
| `scatterMethod` | How to combine scattered inputs (`dotproduct`, `flat_crossproduct`, `nested_crossproduct`) |
| `when` | Conditional expression (v1.2) |

---

### Tool

> A **Tool** is a reusable, self-contained description of a single computational operation. It declares its interface (inputs, outputs, types) and its execution method.

- **Format**: CWL v1.2 document with `class: CommandLineTool` or `class: ExpressionTool`
- A Tool is the **unit of reuse** — the same Tool can appear in multiple Steps across multiple Workflows.
- A Tool describes *what* command to run and *how* to build its arguments, but NOT *where* to run it.

**Two types:**
| Type | Description |
|---|---|
| **CommandLineTool** | Wraps a command-line invocation (`baseCommand`, `arguments`, input bindings) |
| **ExpressionTool** | Evaluates a JavaScript expression (pure data transformation, no side effects) |

**GoWe extension**: A Tool may carry a `goweHint.bvbrc_app_id` hint, telling the Executor to route it to BV-BRC as an app service call instead of a local command execution.

---

### Task

> A **Task** is a concrete, schedulable unit of work created when the Scheduler evaluates a Step at runtime. A Task binds a Tool to **resolved input values** and is assigned to an Executor.

- A Step produces **one Task** normally, or **N Tasks** when scattering.
- A Task is the entity that transitions through lifecycle states.
- Tasks are internal to the engine — users define Workflows and Steps, the engine creates Tasks.

**Relationship**: `Step (definition) → Task (runtime instance)`

This is analogous to:
- Class → Object (OOP)
- Docker Image → Container
- Airflow Operator → Task Instance

---

### Task Lifecycle States

```
                ┌──────────────────────────────────────────┐
                │           Task State Machine             │
                └──────────────────────────────────────────┘

   ┌─────────┐     ┌───────────┐     ┌────────┐     ┌─────────┐
   │ PENDING │────>│ SCHEDULED │────>│ QUEUED │────>│ RUNNING │
   └─────────┘     └───────────┘     └────────┘     └─────────┘
                                                        │
                                          ┌─────────────┼─────────────┐
                                          │             │             │
                                     ┌────v───┐   ┌────v────┐  ┌─────v───┐
                                     │SUCCESS │   │ FAILED  │  │SKIPPED  │
                                     └────────┘   └────┬────┘  └─────────┘
                                                       │
                                                  ┌────v────┐
                                                  │RETRYING │──> QUEUED
                                                  └─────────┘
```

| State | Meaning |
|---|---|
| **PENDING** | Task created, waiting for input dependencies to be satisfied |
| **SCHEDULED** | All inputs available, Scheduler has decided this Task should run |
| **QUEUED** | Submitted to an Executor's queue, waiting for resources |
| **RUNNING** | Actively executing (locally, in a container, or on BV-BRC) |
| **SUCCESS** | Completed successfully, outputs available |
| **FAILED** | Execution failed (non-zero exit, error, timeout) |
| **RETRYING** | Failed but eligible for retry per retry policy |
| **SKIPPED** | Conditional `when` evaluated to false — Task will not execute |

---

### Submission

> A **Submission** (or **Run**) is a specific execution of a Workflow with concrete input values. It is the top-level tracking entity.

- Created when a user submits a Workflow + input values via CLI or API.
- Contains the full resolved DAG of Tasks.
- Has its own lifecycle state derived from its Tasks' states.

| Submission property | Purpose |
|---|---|
| `id` | Unique identifier (UUID) |
| `workflow_id` | Reference to the Workflow definition |
| `inputs` | Resolved input values (files, parameters) |
| `tasks` | List of Tasks created from Steps |
| `state` | Aggregate state (`PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `CANCELLED`) |
| `submitted_at` | Timestamp |
| `completed_at` | Timestamp (when terminal state reached) |
| `submitted_by` | User identity |

**Submission states:**

| State | Meaning |
|---|---|
| **PENDING** | Submission accepted, Tasks being created |
| **RUNNING** | At least one Task is RUNNING or QUEUED |
| **COMPLETED** | All Tasks are SUCCESS or SKIPPED |
| **FAILED** | At least one Task FAILED with no retries remaining |
| **CANCELLED** | User cancelled the Submission |

---

### Executor

> An **Executor** is a pluggable backend that knows *how* to run a Task in a specific environment. The Scheduler assigns Tasks to Executors.

Executors are **not** visible in Workflow definitions (separation of concerns). The engine selects the appropriate Executor based on Tool hints, configuration, and policies.

| Executor | Runs Tasks by... |
|---|---|
| **LocalExecutor** | Spawning a local process (development/testing) |
| **ContainerExecutor** | Running in a Docker/Singularity container (like AWE worker) |
| **BVBRCExecutor** | Submitting to BV-BRC via JSON-RPC `AppService.start_app` and polling `query_tasks` |
| **HPCExecutor** | Submitting to SLURM/PBS (future) |

**Executor responsibilities:**
1. Translate Task inputs into the execution format (command-line args, BV-BRC params, SLURM script)
2. Stage input data (download files, mount volumes, set workspace paths)
3. Launch the computation
4. Monitor progress and report state transitions
5. Collect outputs (files, logs, metrics)
6. Report SUCCESS or FAILED back to the Scheduler

---

### Scheduler

> The **Scheduler** is the engine component that evaluates Task readiness, assigns Tasks to Executors, and enforces resource limits and retry policies.

The Scheduler runs as a continuous loop:

```
loop:
  1. Find Tasks in PENDING state whose input dependencies are all satisfied
  2. Transition them to SCHEDULED
  3. Select appropriate Executor for each (based on hints, config)
  4. Submit to Executor → transition to QUEUED
  5. Receive state updates from Executors → update Task states
  6. If FAILED + retries remaining → transition to RETRYING → re-QUEUE
  7. If all Tasks in a Submission are terminal → finalize Submission state
```

**Scheduler responsibilities:**
- Dependency resolution (DAG traversal)
- Executor selection
- Concurrency limits (max parallel Tasks, max BV-BRC jobs)
- Retry policies (max retries, backoff)
- Timeout enforcement
- Resource pool management

---

### ~~Operator~~ — Not Needed

> GoWe does **not** use a static Operator concept. BV-BRC apps are **self-describing** — the API provides full parameter schemas at runtime.

**Why Operators were considered and rejected:**

The Airflow model hardcodes a typed Operator class per service (e.g., `S3CopyOperator`, `BigQueryOperator`). This made sense for Airflow because AWS/GCP APIs don't self-describe their parameters in a machine-readable schema at a single endpoint.

BV-BRC **does** self-describe. The API provides:

- **`AppService.enumerate_apps`** → returns all apps with metadata
- **`AppService.query_app_description(app_id)`** → returns a specific app's full `AppParameter[]` schema including `id`, `type`, `required`, `default`, `enum` values, and descriptions

This means GoWe can:
1. Call `query_app_description("GenomeAssembly2")` at validation time
2. Receive the **live parameter schema** directly from BV-BRC
3. Validate user inputs against that schema dynamically
4. Pass validated params to `start_app`

Hardcoding per-app Operators would:
- **Duplicate** what BV-BRC already provides
- **Go stale** every time BV-BRC adds, removes, or changes an app or parameter
- **Require maintenance** of 20+ operator definitions by hand
- **Limit** users to only the apps we've wrapped

**Instead, GoWe uses a single generic BVBRCExecutor** that fetches and caches app schemas from the API:

```
BVBRCExecutor receives Task with goweHint.bvbrc_app_id = "GenomeAssembly2"
  │
  ├── 1. query_app_description("GenomeAssembly2") → AppParameter[] (cached)
  ├── 2. Validate Task inputs against AppParameter schema
  ├── 3. Map CWL File locations to BV-BRC workspace paths
  ├── 4. start_app("GenomeAssembly2", validated_params, workspace_path)
  └── 5. Poll query_tasks until terminal state
```

**Schema caching**: The Executor caches `AppDescription` results with a TTL (e.g., 1 hour) to avoid repeated API calls. Cache is invalidated on validation errors to handle schema changes.

---

## Part 3: Relationship Map

How all terms connect:

```
USER writes:
  Workflow (CWL YAML)                    ← definition time
    ├── Step A → run: Tool X (CWL)
    ├── Step B → run: Tool Y (CWL)
    └── Step C → run: sub-Workflow (CWL)

USER submits:
  Submission = Workflow + Input Values    ← submission time
    │
    ▼
ENGINE creates:
  Task A (from Step A)                   ← runtime
  Task B (from Step B)
  Task C₁, C₂, ... (from Step C, possibly scattered)
    │
    ▼
SCHEDULER evaluates:
  Task readiness (dependencies met?)
  Executor selection (local? BV-BRC? container?)
    │
    ▼
EXECUTOR runs:
  Task A → LocalExecutor (docker run ...)
  Task B → BVBRCExecutor (JSON-RPC start_app)
  Task C₁ → BVBRCExecutor (JSON-RPC start_app)
```

---

## Part 4: Term Disambiguation

These terms are often confused. GoWe uses them precisely:

| Often confused | GoWe meaning |
|---|---|
| "Job" (BV-BRC) | Maps to a **Task** — a single unit of work submitted to BV-BRC. In BV-BRC API docs, "job" and "task" are used interchangeably. In GoWe, we always say **Task**. |
| "Job" (AWE) | Maps to a **Submission** — AWE's "job" is a multi-task workflow with a DAG. |
| "Pipeline" | Informal synonym for **Workflow**. Acceptable in conversation, but code and docs use **Workflow**. |
| "Process" (CWL) | CWL's abstract base type. In GoWe code, use **Tool** (for CommandLineTool/ExpressionTool) or **Workflow**. |
| "Work unit" (AWE) | A partition of a Task for data-parallel execution. GoWe models this as **scattered Tasks**. |
| "App" (BV-BRC) | A BV-BRC application (e.g., GenomeAssembly2). In GoWe, referenced by `goweHint.bvbrc_app_id` on a Tool. The app's parameter schema is fetched dynamically from `query_app_description`. |
| "Run" | Synonym for **Submission**. Both are acceptable. |
| "DAG" | The dependency graph. A **Workflow** defines a DAG of Steps. A **Submission** instantiates a DAG of Tasks. |

---

## Part 5: Summary Table

| Term | Layer | Created by | Lifecycle |
|---|---|---|---|
| **Workflow** | Definition | User (CWL YAML) | Static (versioned, reusable) |
| **Tool** | Definition | User (CWL YAML) | Static (versioned, reusable) |
| **Step** | Definition | Part of a Workflow | Static |
| **Submission** | Runtime | User submits Workflow + inputs | PENDING → RUNNING → COMPLETED/FAILED/CANCELLED |
| **Task** | Runtime | Engine creates from Steps | PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS/FAILED/SKIPPED |
| **Scheduler** | Engine | GoWe server | Persistent loop |
| **Executor** | Engine | GoWe config | Pluggable backend |
| **App Schema** | Runtime (cached) | Fetched from BV-BRC `query_app_description` | Cached with TTL, used for input validation |
