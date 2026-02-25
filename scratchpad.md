# GoWe Scratchpad

## Session: 2026-02-24 (Workflow Orchestration Consolidation)

### Status: COMPLETE - Shared stepinput package created

Consolidated step input resolution logic into shared `internal/stepinput/` package for use by both cwl-runner and scheduler. Expanded `model.StepInput` to preserve full CWL semantics.

### Branch: `feature/InitialWorkDirRequirement`

### Changes Made

1. **Expanded `pkg/model/workflow.go` StepInput struct**:
   - Added `Sources []string` - Primary array of sources
   - Added `Default any` - Fallback value if sources resolve to nil
   - Added `LoadContents bool` - Read file contents before valueFrom
   - Kept `Source string` for backwards compatibility (comma-joined)

2. **Updated `internal/parser/parser.go`**:
   - `ToModel()` now preserves all CWL StepInput fields (Sources, Default, LoadContents)
   - Maintains backwards compat by also setting comma-joined Source

3. **Created `internal/stepinput/` package**:
   - `resolve.go` - Shared resolution functions:
     - `ResolveInputs()` - Full CWL semantic resolution
     - `ResolveSource()` - Resolve workflow input or step output references
     - `ResolveDefaultValue()` - Handle File/Directory defaults with path resolution
     - `ApplyLoadContents()` - Read file contents into contents field
     - `InputDefFromModel()` - Convert model.StepInput to InputDef
   - `resolve_test.go` - Comprehensive unit tests

4. **Updated `internal/scheduler/resolve.go`**:
   - Refactored `ResolveTaskInputs()` to use `stepinput.ResolveInputs()`
   - Builds stepOutputs map from completed upstream tasks
   - Converts model.StepInput to stepinput.InputDef
   - Preserves post-processing for reserved keys and normalization

### Test Results

- All stepinput tests: 12/12 passing
- All scheduler tests: 36/36 passing
- All parser tests: passing
- Full test suite: all packages passing

### Conformance Test Results

| Mode | Before | After | Change |
|------|--------|-------|--------|
| cwl-runner | 84/84 | 84/84 | - |
| server-local | 9/84 | 22/84 | +13 tests |

**Fixes applied:**
1. Enabled Tool/Job population for ALL executors (not just worker)
2. Updated LocalExecutor to use execution.Engine for full CWL support
3. Updated populateToolAndJob to use stepinput package for proper defaults

**Remaining server-local failures:**
- 31 tests: `submission failed` - task execution errors
- 22 tests: `got: null` or wrong values - output collection edge cases
- 4 tests: bundle/parsing errors (packed workflows, passthrough)

### Polling Interval Changes

Added `-scheduler-poll` flag to server (default 2s):
- `cmd/server/main.go` - new flag
- `scripts/run-conformance-server-local.sh` - uses 100ms for fast tests
- `docker-compose.yml` - scheduler 500ms, workers 500ms

### Next Steps

- Investigate server mode output collection (separate issue)
- Consider refactoring cwl-runner to use stepinput package (optional)

---

## Session: 2026-02-24 (Integration Testing Scripts)

### Status: COMPLETE - Integration test scripts created

Added conformance test scripts for all execution modes.

### Branch: `feature/InitialWorkDirRequirement`

### Changes Made

1. **Created `scripts/run-conformance-server-local.sh`**:
   - Runs conformance tests with server using LocalExecutor
   - Starts server with `-default-executor local -allow-anonymous`
   - Uses different port (8091) to avoid conflicts with distributed tests
   - Supports same options as other scripts (-p, -k, -h, tags)

2. **Created `scripts/run-all-tests.sh`**:
   - Unified runner for all three execution modes
   - Modes: cwl-runner, server-local, distributed
   - Supports `-m MODE` to run specific mode
   - Supports `-s MODE` to skip specific mode
   - Shows summary with pass/fail status for each mode

### Test Modes

| Mode | Script | Description |
|------|--------|-------------|
| cwl-runner | `run-conformance.sh` | Standalone CLI |
| server-local | `run-conformance-server-local.sh` | Server with LocalExecutor |
| distributed | `run-conformance-distributed.sh` | Docker-compose with workers |

### Usage

```bash
# Run all modes with required tests
./scripts/run-all-tests.sh required

# Run only cwl-runner mode
./scripts/run-all-tests.sh -m cwl-runner required

# Run server-local mode with IWDR tests
./scripts/run-conformance-server-local.sh initial_work_dir

# Skip distributed mode
./scripts/run-all-tests.sh -s distributed required
```

### Test Results

| Mode | Result | Notes |
|------|--------|-------|
| cwl-runner | 84/84 | Full conformance |
| server-local | 9/84 | Server API path has gaps |
| distributed | 15/84 | Same server API issues |

### Gap Analysis: Server vs cwl-runner

The server execution path (`gowe run --server`) uses a different code path:
- `cwl-runner` → directly uses `internal/cwlrunner` package
- `server` → API submission → scheduler → executor

Server-specific issues identified:
1. **Input resolution**: "workflow input X not found for input Y" errors
2. **Workflow decomposition**: Task graph creation differs from cwl-runner
3. **Output collection**: Some outputs returning null

These are pre-existing issues, not related to the IWDR package extraction.

---

## Previous Session: 2026-02-24 (Execution Engine Consolidation)

### Status: COMPLETE - Execution consolidated into shared engine

Consolidated CWL execution into `internal/execution/Engine` for use by both cwl-runner and distributed workers. Extracted InitialWorkDirRequirement into `internal/iwdr/` package.

### Branch: `feature/InitialWorkDirRequirement`

### Changes Made

1. **Created `internal/iwdr/` package** with:
   - `iwdr.go` - Public API: `Stage()`, `UpdateInputPaths()`, `HasDockerRequirement()`, types
   - `stage.go` - Core staging logic extracted from runner.go (~500 lines)
   - `file_ops.go` - File operations: `copyFile()`, `copyDir()`, `CopyDirContents()`
   - `iwdr_test.go` - Unit tests for the package

2. **Enhanced `internal/execution/` engine**:
   - `engine.go` - Added IWD staging, container runtime detection, CWLDir config
   - `docker.go` - Full support: container mounts, dockerOutputDirectory, ShellCommandRequirement
   - `apptainer.go` (NEW) - Full Apptainer support mirroring Docker capabilities
   - `local.go` - Temp file handling for stdout/stderr (prevents find command race conditions)
   - `outputs.go` - Enhanced secondary files with expression evaluation and directory support

3. **Updated `internal/cwlrunner/runner.go`**:
   - Removed ~700 lines of IWD functions
   - Uses `iwdr` package for staging
   - Uses `execution.Engine` for tool execution (pending full integration)

4. **Updated supporting files**:
   - `internal/worker/worker.go` - Passes CWLDir to Engine
   - `pkg/model/task.go` - Added CWLDir to RuntimeHints

### Test Results

- InitialWorkDirRequirement: 38/38 passing
- Required conformance: 84/84 passing
- All Go unit tests passing

### Key Decisions

- BV-BRC API submission remains separate (fundamentally different - external API)
- Execution engine supports both Docker and Apptainer container runtimes
- Resource metrics (PeakMemoryKB, Duration, StartTime) captured in RunResult

---

## Previous Session: 2026-02-24 (Shared IWDR Package Refactoring)

### Status: COMPLETE - Shared iwdr package extracted

Extracted InitialWorkDirRequirement logic into shared package `internal/iwdr/` for use by both cwl-runner and server/executor.

### Branch: `feature/InitialWorkDirRequirement`

### Changes Made

1. **Created `internal/iwdr/` package** with:
   - `iwdr.go` - Public API: `Stage()`, `UpdateInputPaths()`, `HasDockerRequirement()`, types
   - `stage.go` - Core staging logic extracted from runner.go (~500 lines)
   - `file_ops.go` - File operations: `copyFile()`, `copyDir()`, `CopyDirContents()`
   - `iwdr_test.go` - Unit tests for the package

2. **Updated `internal/cwlrunner/`**:
   - `runner.go` - Removed ~700 lines of IWD functions, now imports and uses `iwdr` package
   - `execute.go` - Updated to use `iwdr.ContainerMount` and `iwdr.CopyDirContents()`

3. **Updated `internal/execution/`**:
   - `engine.go` - Added `CWLDir` to Config, calls `iwdr.Stage()` in `ExecuteTool()`
   - `docker.go` - Added `containerMounts` parameter to `executeDocker()`, mounts IWD files

4. **Updated `internal/worker/worker.go`**:
   - Extracts `CWLDir` from `task.RuntimeHints` and passes to Engine

5. **Updated `pkg/model/task.go`**:
   - Added `CWLDir` field to `RuntimeHints` struct

### Test Results

- InitialWorkDirRequirement: 38/38 passing
- Required conformance: 84/84 passing
- All Go unit tests passing

### Next Steps

- Consider adding CWLDir to task serialization when creating tasks on the server side
- Workers can now handle InitialWorkDirRequirement workflows (pending CWLDir propagation from scheduler)

---

## Previous Session: 2026-02-24 (Issue #52 - InitialWorkDirRequirement Conformance)

### Status: COMPLETE - 38/38 tests passing

All InitialWorkDirRequirement conformance tests now pass (issue #52).

### Branch: `feature/InitialWorkDirRequirement`

### Progress

| Before | After | Change |
|--------|-------|--------|
| 36/38 | 38/38 | +2 tests |

### Fixes Applied (This Session)

1. **Expression evaluation in output secondaryFiles** - Updated `addSecondaryFiles` in execute.go to evaluate JavaScript expressions in secondaryFiles patterns using the expression evaluator
2. **Array handling in secondaryFiles expressions** - When patterns like `${ return [self.basename+".idx7"]; }` return arrays, process each element separately with `extractSecondaryPaths` helper
3. **Directory support in secondaryFiles** - Handle directories (not just files) as secondary files by checking `IsDir()` and using `createDirectoryObject`
4. **Input context for expressions** - Pass `inputs` to the expression evaluator so patterns like `inputs.secondfile` work correctly
5. **Mount secondary files in Docker** - Updated `collectInputMountsValue` to recursively process `secondaryFiles` arrays and `listing` arrays for directories

### Test Results

- InitialWorkDirRequirement: 38/38 passing
- Required conformance: 84/84 passing

### Files Modified

- `internal/cwlrunner/execute.go`:
  - Updated `addSecondaryFiles` to handle expressions, arrays, and directories
  - Added `extractSecondaryPaths` helper function
  - Updated `addSecondaryFilesToOutput` to pass `inputs` parameter
  - Updated `collectInputMountsValue` to mount secondary files and directory listings

---

## Previous Sessions

### Session: 2026-02-24 (Issue #47 - Per-Step Runtime and Memory Metrics)

Status: MERGED - Issue #47 closed

### Session: Earlier (Issue #52 work)

Fixes applied:
- `$include` directive support in InlineJavascriptRequirement
- Expression evaluation in `computeSecondaryFileName` for input validation
- Trailing newlines in expressions
- JSON-stringified arrays in listing
- Copy files for container execution
- ShellCommandRequirement support
- Absolute entryname paths
- dockerOutputDirectory support

See git history for complete details.
