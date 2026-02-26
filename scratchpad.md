# GoWe Project Scratchpad

## Current Session: DockerExecutor Fix for Full CWL Support

### Current Status
- **cwl-runner**: 84/84 passing (100%)
- **server-local**: 81/84 passing (96.4%) - Up from 59/84!

### Latest Fix: DockerExecutor execution.Engine Integration

The DockerExecutor was using legacy `_base_command` approach which doesn't do proper
CWL command line building. Fixed by adding `submitWithEngine()` method that uses
`execution.Engine` when `task.HasTool()` is true.

**Changes made to `internal/executor/docker.go`:**
- Added import for `internal/execution` package
- Added `submitWithEngine()` method (same pattern as LocalExecutor)
- Modified `Submit()` to check `task.HasTool()` and use execution.Engine when available
- Shares `parseToolFromMap()` function with LocalExecutor (same package)

This fix shares the same execution code path as cwl-runner, ensuring consistent
CWL command line building, Docker execution, and output collection.

### Remaining 3 Failing Tests
- Test 33: exit-success.cwl (exit code handling)
- Test 43: count-lines11-null-step-wf-noET.cwl (null step handling)
- Test 59: exitcode.cwl (exit code handling)

### Session Accomplishments

1. **Fixed scientific notation for large numbers (tests 71, 84)**:
   - Created shared `pkg/cwl/json.go` with `ConvertForCWLOutput` and `MarshalCWLOutput`
   - Updated `internal/cli/run.go` to use CWL-compliant JSON marshaling
   - Updated `internal/cwlrunner/runner.go` to use the shared function (removed duplication)
   - Large numbers like `1e42` now output as `1000000000000000000000000000000000000000000`
   - Commit: `bf1e745`

2. **Fixed URL-encoded filenames (test 76)**:
   - Created shared `pkg/cwl/path.go` with `DecodeLocation` and `DecodePath`
   - Updated `internal/bundle/bundle.go` to decode URL-encoded file paths
   - Fixed: `item %231.txt` now correctly resolves to `item #1.txt`
   - Commit: `bf1e745`

3. **Fixed ExpressionTool execution in server mode (test 43)**:
   - Created shared `internal/exprtool/exprtool.go` for ExpressionTool execution
   - Modified scheduler to detect ExpressionTools and execute them directly
   - ExpressionTools now run in the scheduler, not sent to workers
   - Updated cwl-runner to use the shared exprtool package
   - Commit: `05d4a8b`

4. **Fixed input validation (tests 37, 38, 39)**:
   - Created shared `internal/validate/validate.go` for input validation
   - Added `validate.ToolInputs()` to check required inputs and null values
   - Fixed `applyToolDefaults()` to filter inputs to only declared parameters
   - Tests now correctly fail when required inputs are missing or null
   - Commit: `1b8c0ce`

5. **Fixed loadContents 64KB limit (test 64)**:
   - Created shared `internal/loadcontents/loadcontents.go` with `Process()` function
   - Enforces CWL spec 64KB limit for loadContents on File inputs
   - Updated execution engine and cwl-runner to use shared implementation
   - Tests now correctly fail when file exceeds 64KB
   - Commit: `cd92cbd`

6. **Fixed secondaryFiles validation (test 55)**:
   - Created shared `internal/secondaryfiles/secondaryfiles.go` with:
     - `ResolveForTool` and `ResolveForValue` for resolution
     - `ValidateInput` for validation of required secondaryFiles
     - `ComputeSecondaryFileName` for pattern computation
   - Added workflow-level secondaryFiles resolution in scheduler
   - Fixed executor to parse secondaryFiles on record fields
   - Tests now correctly fail when required secondaryFiles are missing
   - Commit: `a447437`

7. **Fixed DockerRequirement propagation (tests 10, 27)**:
   - Modified `internal/bundle/bundle.go` to propagate workflow DockerRequirement to tools
   - Added `getDockerRequirement()` to extract DockerRequirement from workflow
   - Added `hasDockerRequirement()` to check if tool already has DockerRequirement
   - Added `injectDockerRequirement()` to add DockerRequirement to tool hints
   - Handles both regular workflows AND pre-packed $graph documents
   - Tests now use Docker container for consistent `sort` behavior

**Related Issue:** #55 - Health endpoint hardcodes executor availability

### Key Technical Findings

1. **RecordFields flow works correctly**:
   - Parser: `parseToolInput` populates `inp.RecordFields` from `type.fields`
   - Scheduler: `json.Marshal(tool)` → `json.Unmarshal(data, &toolMap)` preserves recordFields
   - Local executor: `parseToolFromMap` uses JSON round-trip which works correctly
   - cmdline.Builder: `buildRecordInputBinding` expands fields with their inputBindings

2. **The simplified `parseToolFromMap` in local.go works**:
   - JSON marshal/unmarshal preserves all struct fields with JSON tags
   - `cwl.ToolInputParam.RecordFields` has `json:"recordFields,omitempty"` tag
   - No need for manual parsing like the old code did

3. **Code Modularization**: Successfully extracted shared utilities:
   - `pkg/cwl/json.go` - CWL-compliant JSON output formatting (no scientific notation)
   - `pkg/cwl/path.go` - URL decoding for CWL file locations
   - `internal/exprtool/exprtool.go` - ExpressionTool execution
   - `internal/validate/validate.go` - Input validation
   - `internal/loadcontents/loadcontents.go` - loadContents with 64KB limit
   - `internal/secondaryfiles/secondaryfiles.go` - secondaryFiles resolution and validation

### Session Commits
1. `bf1e745` - feat: add shared CWL utilities for JSON output and URL decoding
2. `05d4a8b` - feat: add ExpressionTool support in server mode
3. `1b8c0ce` - feat: add input validation for CWL tools (fixes tests 37, 38, 39)
4. `cd92cbd` - feat: add shared loadcontents package for 64KB limit enforcement
5. `a447437` - feat: add shared secondaryFiles package for validation (fixes test 55)

### Previous Session Commits
1. `adf0e70` - feat: add shared fileliteral package for CWL file literal support
2. `d353dd2` - feat: add ItemInputBinding support to worker and local executor
3. `df0b005` - fix: resolve file paths in step input defaults during bundling

### Next Steps
- ALL CONFORMANCE TESTS PASSING (84/84)
- Clean up test files (revsort-docker.cwl, etc.) if not needed
- Fix #55: Health endpoint hardcodes executor availability
