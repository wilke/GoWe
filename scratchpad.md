# GoWe Scratchpad

## Session: 2026-02-24 (Conditional Conformance - 46/46 COMPLETE)

### Status: COMPLETE - All 46 conditional tests passing

Improved CWL conditional conformance tests from **0/46 to 46/46 (100%)**.

### Branch: `conditional`

### Key Fixes

| Issue | Tests | Fix |
|-------|-------|-----|
| Early `when` check with scatter vars | 19, 20, 22 | Skip early `when` check if expression references scattered variables |
| Step input array sources | 45, 46 | Handle `[]any` case in step input parsing (multiple sources shorthand) |
| `all_non_null` validation | 11, 33 | Validate that `pickValue: all_non_null` requires array output type |
| Complex output types | 20, 42 | Use `serializeCWLType` for workflow output types to handle nested array types |

### Files Modified

| File | Changes |
|------|---------|
| `internal/cwlrunner/scatter.go` | Added `whenReferencesScatterVars()` helper, skip early `when` check when condition references scatter variables |
| `internal/parser/parser.go` | Handle `[]any` step input sources, use `serializeCWLType` for output types |
| `internal/parser/validator.go` | Added `isArrayType()` helper, validate `all_non_null` requires array output type |

### Verification

```bash
./scripts/run-conformance.sh conditional
# All tests passed
# === All conditional tests passed! ===
```

---

## Session: 2026-02-24 (step_input Conformance - 20/20 COMPLETE)

### Status: COMPLETE - All 20 step_input tests passing

Improved CWL step_input conformance tests from 10/20 to **20/20 (100%)**.

### Branch: `step_input`

### Commits This Session

1. `9a1b9fb` - fix: handle NaN and Inf values in JSON output serialization
2. `4736e97` - fix: preserve source-resolved values in valueFrom inputs context
3. `dcaa31c` - feat: support MultipleInputFeatureRequirement and inline ExpressionTools
4. `e64b4d6` - chore: remove debug test
5. `e1f3006` - feat: implement loadContents and fix nested_crossproduct scatter

### Key Fixes

| Issue | Tests | Fix |
|-------|-------|-----|
| NaN serialization | Test 1 | `math.IsNaN(val) || math.IsInf(val, 0)` → return nil |
| valueFrom inputs context | Multiple | Don't update inputsCtx during valueFrom (preserves source values) |
| Multiple sources | Tests 11,15 | StepInput.Source → Sources []string |
| Inline ExpressionTools | Tests 11,15 | Parse `run: {class: ExpressionTool, ...}` inline |
| YAML literal whitespace | Multiple | `strings.TrimSpace(expr)` before checking `${...}` |
| loadContents | Tests 1,16-19 | Apply loadContents for workflow inputs, step inputs, ExpressionTool inputs |
| nested_crossproduct | Test 5 | `mergeScatterOutputsNested` with proper nesting structure |

### Files Modified

| File | Changes |
|------|---------|
| `pkg/cwl/workflow.go` | Added LoadContents to InputParam and StepInput; Source → Sources |
| `internal/parser/parser.go` | Parse loadContents field, multiple sources, inline ExpressionTools |
| `internal/parser/validator.go` | Updated for Sources []string |
| `internal/parser/dag.go` | Updated for Sources []string dependency tracking |
| `internal/cwlrunner/runner.go` | NaN fix, applyLoadContents, loadContents for workflow/step/ExpressionTool inputs |
| `internal/cwlrunner/scatter.go` | mergeScatterOutputsNested, nestResults for nested_crossproduct |
| `internal/cwlexpr/evaluator.go` | TrimSpace for YAML literal blocks |

### Verification

```bash
./scripts/run-conformance.sh step_input
# All tests passed
# === All step_input tests passed! ===
```

---

## Session: 2026-02-24 (StepInputExpressionRequirement)

### Status: COMPLETE

Implemented StepInputExpressionRequirement support for both cwl-runner and gowe server.

**StepInputExpressionRequirement** allows workflow step inputs to use `valueFrom` expressions that transform the source value before passing it to the tool.

**Files Modified:**

| File | Changes |
|------|---------|
| `pkg/cwl/workflow.go` | Added `ValueFrom` field to `StepInput` struct |
| `pkg/model/workflow.go` | Added `ValueFrom` field to `StepInput` struct |
| `internal/parser/parser.go` | Parse `valueFrom` from step inputs (lines 636-641), convert to model.StepInput with ValueFrom (line 1169) |
| `internal/cwlrunner/runner.go` | Updated `resolveStepInputs` to evaluate valueFrom expressions using cwlexpr |
| `internal/cwlrunner/parallel.go` | Added evaluator to parallelExecutor, use shared evaluator for valueFrom and when expressions |
| `internal/scheduler/resolve.go` | Added expressionLib parameter, evaluate valueFrom expressions in ResolveTaskInputs |
| `internal/scheduler/loop.go` | Updated call to ResolveTaskInputs with nil expressionLib |
| `internal/cwlrunner/runner_test.go` | Added `TestRunner_Execute_ValueFrom` test |
| `internal/scheduler/resolve_test.go` | Added `TestResolveTaskInputs_ValueFrom` and `TestResolveTaskInputs_ValueFrom_NoSource` tests |

**Example Usage:**
```yaml
cwlVersion: v1.2
class: Workflow
requirements:
  StepInputExpressionRequirement: {}
inputs:
  prefix: string
  name: string
outputs:
  result:
    type: File
    outputSource: greet/output
steps:
  greet:
    run: echo.cwl
    in:
      message:
        source: name
        valueFrom: $(inputs.prefix + " " + self)  # "Hello World"
    out: [output]
```

**Key Implementation Details:**
- In cwl-runner: `resolveStepInputs` now takes an evaluator and evaluates valueFrom with `inputs` = workflow inputs, `self` = resolved source value
- In gowe server: `ResolveTaskInputs` takes expressionLib parameter, creates evaluator, evaluates valueFrom similarly
- Shared evaluators for efficiency (one per workflow execution instead of per-expression)
- Works with or without source (valueFrom can generate value from scratch if source is empty)

**Tests:**
- `TestRunner_Execute_ValueFrom` - cwl-runner workflow with valueFrom expression
- `TestResolveTaskInputs_ValueFrom` - scheduler with source + valueFrom
- `TestResolveTaskInputs_ValueFrom_NoSource` - scheduler with no source, only valueFrom

All tests pass (77 unit test packages).

---

## Session: 2026-02-24 (Parallel Execution for cwl-runner)

### Status: COMPLETE

Implemented parallel execution for the cwl-runner, allowing independent workflow steps and scatter iterations to run concurrently.

**New Files:**
- `internal/cwlrunner/parallel.go` - Parallel executor with worker pool and DAG-based scheduling

**Modified Files:**
- `internal/cwlrunner/runner.go` - Added ParallelConfig, integrated parallel execution
- `internal/cwlrunner/scatter.go` - Added executeScatterParallel with bounded goroutines
- `internal/cwlrunner/runner_test.go` - Added parallel execution tests
- `internal/parser/parser.go` - Fixed stringSlice to handle single string values (scatter bug fix)
- `cmd/cwl-runner/main.go` - Added --parallel, --jobs, --no-fail-fast flags

**Architecture:**
```
┌─────────────────────────────────────────────────────────────────────┐
│                      Parallel Workflow Executor                      │
│                                                                      │
│  DAG Tracker → Ready Queue (channel) → Worker Pool (N workers)      │
│       │                                        │                     │
│       └──────────── Results Collector ◀────────┘                     │
└─────────────────────────────────────────────────────────────────────┘
```

**CLI Usage:**
```bash
cwl-runner --parallel workflow.cwl job.yml           # Use all CPUs
cwl-runner --parallel --jobs 8 workflow.cwl job.yml  # Limit to 8 concurrent
cwl-runner --parallel --no-fail-fast workflow.cwl    # Continue on errors
```

**Key Features:**
- Worker pool pattern with bounded concurrency
- DAG-based ready-queue for workflow steps
- Semaphore-bounded parallel scatter iterations
- Fail-fast with context cancellation (default)
- Thread-safe stepCount with mutex for unique work directories

**Bug Fix:**
- Fixed `stringSlice` in parser to handle single string values (e.g., `scatter: message`)
- Previously, scatter with single input name returned nil, causing scatter to not execute

**Tests:**
- TestRunner_Execute_Parallel - Two independent steps
- TestRunner_Execute_Parallel_Scatter - 4-way scatter with parallel iterations
- TestRunner_Execute_Sequential_Scatter - Verify sequential scatter still works
- TestParallelConfig_Defaults - Config defaults

---

## Session: 2026-02-20 (Work Summary Generation)

### Status: COMPLETE

Generated work summary report for the GPU support and ProteinFoldingApp integration work.

**Report Created:**
- `reports/work-260220.gowe.md` - Comprehensive work summary

**Session Context (from compacted conversation):**
- GPU support implementation (Apptainer `--nv`, Docker `--gpus`)
- CUDA_VISIBLE_DEVICES for GPU isolation in multi-GPU deployments
- ProteinFoldingApp feasibility analysis
- 8-GPU deployment guide for running AlphaFold2, Boltz, Chai, ESMFold experiments
- Tagged as v0.10.1

**Key Commits:**
- `37b6281` - feat: add GPU support for Apptainer and Docker runtimes
- `ab7882e` - feat: set CUDA_VISIBLE_DEVICES for GPU isolation
- `ee693aa` - docs: add ProteinFoldingApp setup guide for 8-GPU deployment

---

## Session: 2026-02-19 (Distributed Pipeline Test with Shared Volume)

### Status: COMPLETE

Created and verified a 3-step distributed pipeline test demonstrating shared volume access:

**Pipeline Steps:**
1. `generate-numbers.cwl` - Creates file with numbers 1-100
2. `count-lines.cwl` - Runs `wc -l` on the file
3. `check-exists.cwl` - Checks if file exists, returns boolean

**Test Files Created:**
- `testdata/distributed-test/pipeline.cwl`
- `testdata/distributed-test/generate-numbers.cwl`
- `testdata/distributed-test/count-lines.cwl`
- `testdata/distributed-test/check-exists.cwl`
- `testdata/distributed-test/job.yml`
- `scripts/test-distributed-pipeline.sh`

**Shared Volume Configuration:**
- Host path: `./tmp/workdir/`
- Container path: `/workdir/`
- Outputs stored in: `./tmp/workdir/outputs/task_<id>/`

**Result Files (visible on host):**
```
tmp/workdir/outputs/task_9defa6c7.../numbers.txt      # 100 lines (1-100)
tmp/workdir/outputs/task_781508ba.../line_count.txt   # "100"
tmp/workdir/outputs/task_3cd32444.../exists_result.txt # "true"
```

---

## Session: 2026-02-19 (Multi-Provider Auth + ArgumentEntry Fix)

### Status: COMPLETE

---

### Bug Fix: CWL ArgumentEntry Deserialization (Issue #45)

**Problem:** When CWL tools with structured `arguments` were sent to distributed workers, the arguments failed to deserialize. Go's `encoding/json` deserialized `[]any` elements as `map[string]interface{}` instead of `cwl.Argument` structs.

**Solution:** Implemented CWL-compliant typed `ArgumentEntry` with custom JSON unmarshaling:

```go
type ArgumentEntry struct {
    StringValue string     // For string literals or expressions
    Binding     *Argument  // For CommandLineBinding objects
    IsString    bool       // Discriminator
}
```

**Files Changed:**
- `pkg/cwl/binding.go` - Added `ArgumentEntry` type with `UnmarshalJSON`/`MarshalJSON`
- `pkg/cwl/tool.go` - Changed `Arguments []any` to `Arguments []ArgumentEntry`
- `internal/cmdline/builder.go` - Updated `buildArgument()` for typed entry
- `internal/parser/parser.go` - Create `ArgumentEntry` objects during parsing
- `internal/cmdline/builder_test.go` - Updated tests

**Verification:**
- ✅ 84/84 CWL conformance tests pass
- ✅ All unit tests pass
- ✅ Distributed `bwa-mem` test now passes (was failing)

**GitHub:** Issue #45 created and closed

---

## Multi-Provider Authentication Implementation

### Status: COMPLETE

**Implementation of multi-provider authentication with role separation:**

### Features Implemented

1. **User Model** (`pkg/model/user.go`)
   - UserRole: user, admin, anonymous
   - AuthProvider: bvbrc, mgrast, local
   - User struct with linked providers support

2. **Auth Infrastructure**
   - `internal/server/auth.go` - Multi-provider auth middleware
   - `internal/server/admin_config.go` - Admin role from env/cli/config
   - `internal/server/worker_auth.go` - Worker key authentication

3. **Database Changes** (`internal/store/migrations.go`)
   - Users table with id, username, provider, role, created_at, last_login
   - Linked providers table for multi-provider linking
   - Submission token columns: user_token, token_expiry, auth_provider
   - Workers table: worker_group column

4. **Per-Task Token Delegation**
   - BVBRCExecutor refactored for per-task callers
   - Scheduler populates RuntimeHints.StagerOverrides.HTTPCredential
   - Token expiry check before task dispatch

5. **Worker Groups**
   - Workers register with a group membership
   - X-Worker-Key header authentication
   - CheckoutTask filters by worker group

6. **Server Configuration**
   - `--allow-anonymous` flag for unauthenticated access
   - `--anonymous-executors` to restrict executors for anonymous users
   - `--worker-keys` for worker key configuration
   - `--config` for config file loading

### Files Created

| File | Purpose |
|------|---------|
| `pkg/model/user.go` | User, UserRole, AuthProvider types |
| `internal/server/auth.go` | Multi-provider auth middleware |
| `internal/server/admin_config.go` | Admin role management |
| `internal/server/worker_auth.go` | Worker key authentication |
| `internal/server/handler_admin.go` | Admin API endpoints |

### Files Modified

| File | Changes |
|------|---------|
| `pkg/model/submission.go` | Added UserToken, TokenExpiry, AuthProvider |
| `pkg/model/worker.go` | Added Group field |
| `pkg/model/task.go` | Added WorkerGroup to RuntimeHints |
| `pkg/model/errors.go` | Added ErrForbidden |
| `pkg/model/session.go` | Updated to use UserRole from user.go |
| `internal/store/migrations.go` | Users, linked_providers tables; submission/worker columns |
| `internal/store/store.go` | User CRUD ops; CheckoutTask with workerGroup |
| `internal/store/sqlite.go` | Implemented all new operations |
| `internal/executor/bvbrc.go` | Per-task caller with user token |
| `internal/scheduler/loop.go` | Token expiry check; HTTPCredential population |
| `internal/server/server.go` | Auth config fields; middleware application |
| `internal/server/handler_submissions.go` | UserContext usage |
| `internal/server/handler_workers.go` | Worker auth; group support |
| `internal/worker/worker.go` | Group, WorkerKey config |
| `internal/worker/client.go` | X-Worker-Key header; group param |
| `cmd/worker/main.go` | --group, --worker-key flags |
| `cmd/server/main.go` | Auth config loading |

### Test Updates

- Store tests: CheckoutTask now takes workerGroup parameter
- UI tests: Use string(model.RoleUser) casts
- Server/CLI tests: Enable anonymous access by default for testing

### All Tests Pass

```
go test ./...  # 19 packages, all pass
```

---

## Session: 2026-02-19 (HTTP Stager + Test Script Improvements)

### Status: COMPLETE

**Commits this session:**
1. `deec4ad` - feat: add HTTP/HTTPS staging support for workers
2. `f72a98a` - fix: use correct port 8090 in distributed conformance script
3. `d931331` - feat: add port config and conflict detection to distributed test scripts

### HTTP/HTTPS Stager Implementation

Added multi-scheme stager with full HTTP/HTTPS support:
- HTTPStager for downloading (GET) and uploading (PUT/POST) files
- Per-host credentials (bearer, basic, custom header) with wildcard matching
- Per-task stager overrides via `RuntimeHints.StagerOverrides`
- Custom CA certificate support for internal PKI
- Unified TLS config for worker↔server and data staging
- Retry logic with exponential backoff

**New CLI flags for worker:**
```
--ca-cert           CA certificate PEM file
--insecure          Skip TLS verification
--http-timeout      Request timeout (default: 5m)
--http-retries      Retry attempts (default: 3)
--http-credentials  Credentials JSON file
--http-upload-url   URL template with {taskID}, {filename}
--http-upload-method PUT or POST
```

### Test Script Improvements

Updated `scripts/test-distributed.sh` and `scripts/run-conformance-distributed.sh`:
- `-p, --port PORT` - Configure server port (default: 8090)
- `-k, --keep` - Keep containers running after tests
- Detects running containers and offers to reuse
- Checks for port conflicts before starting
- Dynamic port mapping via `docker-compose.override.yml`

### Test Results

- **Unit tests**: 19 packages pass
- **CWL conformance (standalone)**: 84/84 pass
- **Distributed worker tests**: All pass
- **HTTPStager tests**: 14/14 pass

### Files Changed

| File | Change |
|------|--------|
| `internal/worker/stager_config.go` | NEW - Config types |
| `internal/execution/http_stager.go` | NEW - HTTPStager impl |
| `internal/execution/http_stager_test.go` | NEW - Tests |
| `internal/worker/client.go` | TLS config support |
| `internal/worker/worker.go` | CompositeStager wiring |
| `pkg/model/task.go` | StagerOverrides types |
| `cmd/worker/main.go` | New CLI flags |
| `docs/tools/worker.md` | Documentation |
| `scripts/test-distributed.sh` | Port config, conflict detection |
| `scripts/run-conformance-distributed.sh` | Port config, conflict detection |

---

## Session: 2026-02-18 Night (Multi-Scheme Stager with HTTP/HTTPS)

### Status: COMPLETE - HTTP/HTTPS staging support added

**Major accomplishments:**
1. Added HTTPStager for HTTP/HTTPS file staging (downloads and uploads)
2. Configurable credentials with per-host authentication (bearer, basic, custom header)
3. Per-task stager overrides via RuntimeHints.StagerOverrides
4. Custom CA certificate support for internal PKI
5. Unified TLS config shared between worker↔server API and data staging

### Files Created

| File | Purpose |
|------|---------|
| `internal/worker/stager_config.go` | StagerConfig, TLSConfig, HTTPStagerConfig, CredentialSet types |
| `internal/execution/http_stager.go` | HTTPStager with StageIn (GET) and StageOut (PUT/POST), retries, credentials |
| `internal/execution/http_stager_test.go` | Comprehensive tests for HTTPStager |

### Files Modified

| File | Changes |
|------|---------|
| `internal/worker/client.go` | Accept TLS config for server communication |
| `internal/worker/worker.go` | CompositeStager wiring, HTTPStager integration, per-task overrides |
| `pkg/model/task.go` | Added StagerOverrides and HTTPCredential to RuntimeHints |
| `cmd/worker/main.go` | CLI flags: --ca-cert, --http-timeout, --http-credentials, etc. |

### Files Removed

| File | Reason |
|------|--------|
| `internal/worker/stager.go` | Duplicate of execution.FileStager, consolidated |

### New CLI Flags

```
# TLS (applies to server API + all HTTPS staging)
--ca-cert          Path to CA certificate PEM file for internal PKI
--insecure         Skip TLS verification (testing only)

# HTTP Stager
--http-timeout      HTTP request timeout (default: 5m)
--http-retries      Retry attempts (default: 3)
--http-retry-delay  Initial retry delay (default: 1s)
--http-credentials  Path to credentials JSON file
--http-upload-url   URL template for StageOut uploads
--http-upload-method PUT or POST (default: PUT)
```

### Credentials File Format

```json
{
  "data.example.com": {"type": "bearer", "token": "eyJhbGc..."},
  "*.internal.org": {"type": "basic", "username": "svc", "password": "secret"},
  "upload.example.com": {"type": "header", "header_name": "X-Upload-Token", "header_value": "abc123"}
}
```

### Architecture

```
CompositeStager
├─ file:// → FileStager
├─ http:// → HTTPStager (StageIn: GET, StageOut: PUT/POST)
└─ https://→ HTTPStager
```

### Tests

All 14 HTTPStager tests pass:
- StageIn: download, retry, 4xx no-retry
- StageOut: PUT, POST, URL template expansion
- Credentials: bearer, basic, header, wildcard
- Overrides: per-task headers and credentials

### Next Steps

- Future schemes: `s3://`, `shock://`, `ws://` (BV-BRC Workspace)
- Integration test with real HTTP server

---

## Session: 2026-02-18 Evening (Distributed Execution & 100% Conformance)

### Status: 84/84 TESTS PASSING (100%) + DISTRIBUTED WORKERS WORKING

**Major accomplishments:**
1. Fixed Docker executor file path handling for macOS symlinks
2. Implemented workflow-level hint inheritance (DockerRequirement)
3. All 84 CWL conformance tests now pass on macOS
4. Distributed worker execution verified working

### CWL Conformance: 84/84 (100%)

Fixed the remaining 2 failing tests (10 and 27) by:
1. **Docker file path fix** - Mount files with resolved host path but original container target
   - On macOS, `/tmp` resolves to `/private/tmp`, but commands use `/tmp` paths
   - Fixed in both `internal/cwlrunner` and `internal/execution` packages

2. **Workflow-level hints** - Inherit DockerRequirement from workflow to steps
   - Added `Hints` and `Requirements` fields to `cwl.Workflow` struct
   - Parser now extracts workflow-level hints
   - `hasDockerRequirement` and `getDockerImage` check both tool and workflow hints

### Distributed Worker Testing: VERIFIED

```bash
./scripts/test-distributed.sh  # All tests pass
```

- 3 workers registered (worker-1, worker-2, worker-docker)
- Simple echo test: ✅ Passed
- Echo pipeline test: ✅ Passed

### Files Modified

| File | Changes |
|------|---------|
| `pkg/cwl/workflow.go` | Added Hints, Requirements fields |
| `internal/parser/parser.go` | Parse workflow-level hints |
| `internal/cwlrunner/runner.go` | Check workflow hints in hasDockerRequirement/getDockerImage |
| `internal/cwlrunner/execute.go` | Fixed mount path: resolved→original target |
| `internal/execution/stager.go` | Fixed mount path: resolved→original target |

### Recent Commits

```
914a8ae fix: Docker executor file path handling and workflow hint inheritance
8660bb3 docs: add remote worker architecture analysis
8d02899 feat: support bundling bare CommandLineTools
6eeca67 docs: add distributed execution and gowe run command documentation
f7cdfab feat: add shared execution package and distributed worker testing
```

### Documentation Updates

Updated README and docs for:
- `gowe run` command (cwltest-compatible runner)
- `--default-executor` server flag
- Worker executor type
- Docker Compose distributed execution setup
- Tutorial section on distributed execution

---

## Session: 2026-02-18 Afternoon (Distributed Worker Implementation)

### Distributed Worker Testing Infrastructure: COMPLETE

Implemented complete distributed worker execution testing:

**New files created:**
- `internal/execution/` - Shared execution package (engine, docker, local, stager)
- `internal/cli/run.go` - `gowe run` cwltest-compatible command
- `Dockerfile.worker` - Worker container image
- `docker-compose.yml` - Multi-container test setup
- `testdata/worker-test/` - Test workflows
- `scripts/test-distributed.sh` - Distributed test script

**Key features:**
- `--default-executor=worker` flag for server
- `gowe run` command for cwltest compatibility
- Docker Compose with 1 server + 3 workers
- Shared execution engine for cwl-runner and worker

---

## Session: 2026-02-18 Morning (CWL Conformance - 96.4%)

### Tool Documentation: COMPLETE

Updated README.md and created comprehensive documentation for all cmd/ tools.

**Files modified (1):**
- `README.md` — Added Tools section with table linking to docs, updated Project Structure

**Files created (6):**
- `docs/tools/server.md` — Server documentation with examples, API reference, tutorial
- `docs/tools/cli.md` — CLI documentation with all commands, flags, tutorial
- `docs/tools/worker.md` — Worker documentation with runtime modes, clustering tutorial
- `docs/tools/gen-cwl-tools.md` — CWL tool generator docs with type mapping, customization
- `docs/tools/smoke-test.md` — Smoke test docs with CI/CD integration examples
- `docs/tools/verify-bvbrc.md` — BV-BRC verification docs with troubleshooting guide

**Tools documented (7):**
| Tool | Description |
|------|-------------|
| server | Main API server with scheduler and executors |
| cli | CLI client (login, submit, status, list, cancel, logs, apps) |
| worker | Remote worker for distributed task execution |
| gen-cwl-tools | CWL tool generator from BV-BRC app specs |
| smoke-test | End-to-end API integration test |
| verify-bvbrc | BV-BRC API connectivity verification |
| scheduler | Placeholder (stub) |

---

## Session: 2026-02-17 Late Night (CWL Conformance - 94%)

### Status: 79/84 TESTS PASSING (94%)

Improved CWL conformance test pass rate from 66/84 to **79/84 (94%)**.

### Key Improvements (66/84 → 79/84)

41. **Record field inputBindings** - Added support for record types with field-level inputBindings
42. **Union type parsing fix** - Fixed `["null", "boolean"]` → `boolean?` conversion
43. **External tool file loading** - Workflows can now reference external .cwl files
44. **ExpressionTool support** - Parse and execute ExpressionTools in $graph documents
45. **EnvVarRequirement** - Environment variables now set for tool execution
46. **Step input defaults** - Properly resolve defaults including File objects
47. **File objects in arguments** - Extract path from File objects instead of JSON
48. **JavaScript object literals** - Fixed `$({'key': value})` expression parsing
49. **.length validation** - Properly fail when accessing .length on non-array values
50. **Undeclared input validation** - Accessing undeclared inputs now fails (v0.8.6 → 79/84)
51. **Step input filtering** - Only declared tool inputs passed per CWL v1.2 spec
52. **loadContents 64KB limit** - Input files >64KB now correctly fail loadContents

### Git Status
- v0.8.6: `74addc5 feat: improve CWL conformance from 66/84 to 77/84 (92%)`
- v0.8.7: `d20a787 feat: improve CWL conformance from 77/84 to 79/84 (94%)`

---

## Session: 2026-02-17 Late Evening (CWL Conformance Continued)

---

## Session: 2026-02-17 Evening (CWL Conformance Continued)

### Status: IMPROVED

Improved CWL conformance test pass rate from 62/84 to 66/84 (79%).

### Changes Made This Session

37. **Format field for outputs** - Added format field support for File outputs, including:
    - Namespace resolution (e.g., `edam:format_2330` -> `http://edamontology.org/format_2330`)
    - Expression evaluation (e.g., `$(inputs.input.format)`)
    - Added Namespaces field to GraphDocument
38. **ShellCommandRequirement** - Run commands through shell when ShellCommandRequirement is present
39. **runtime.exitCode** - Capture exit code and make available in outputEval expressions
    - Added ExitCode to RuntimeContext
    - Added InOutputEval flag to Context to conditionally include exitCode
40. **Packed format ID normalization** - Handle fully-qualified IDs in packed JSON format:
    - Normalize IDs like `#main/input` -> `input`
    - Normalize source references like `#main/rev/output` -> `rev/output`
    - Preserve local references like `echo_1/fileout`

### Previous Status: 66/84 tests passing (79%)

---

## Session: 2026-02-17 Continued (CWL Conformance Test Improvements)

### Status: IMPROVED

Improved CWL conformance test pass rate from 54/84 to 61/84 (73%).

### Changes Made This Session (Latest)

33. **$import support** - Resolve `$import` directives in CWL documents (e.g., `outputs: {"$import": params_inc.yml}`)
34. **Argument ordering** - Arguments come before inputs at the same position (fixed via isArgument flag)
35. **Float formatting in JSON** - Output floats without scientific notation in both command line and JSON output
36. **Float formatting in expressions** - Format floats without e-notation when converting to strings in expressions

### Previous Changes This Session
1. **Array-style parsing** - Added support for array-style inputs/outputs/hints/requirements
2. **Bare Workflow support** - Parser now handles bare Workflow class without $graph
3. **Stdin redirection** - Fixed Docker stdin piping with `-i` flag
4. **File path resolution** - Fixed File objects to always have path/basename/dirname/nameroot/nameext
5. **Checksum computation** - Added SHA1 checksum to output File objects
6. **Tool input defaults** - Merge tool defaults with job inputs
7. **Shorthand output types** - Handle `stdout_file: stdout` format
8. **Stdout/stderr capture** - Automatic capture for type outputs
9. **Passthrough workflows** - Workflows can pass inputs directly to outputs
10. **Docker symlink resolution** - Fixed /tmp symlink issue on macOS for Docker mounts
11. **cwl.output.json handling** - Use as complete output when present (per CWL spec)
12. **ResourceRequirement** - Extract coresMin/ramMin from hints/requirements for runtime
13. **Nested array bindings** - Support item-level inputBinding for arrays (e.g., `-XXX -YYY file1 -YYY file2`)
14. **Stdout expression evaluation** - Evaluate expressions in stdout filename for output collection
15. **Boolean empty inputBinding** - Boolean with `inputBinding: {}` is omitted from command line
16. **ItemInputBinding parsing** - Parse inputBinding from inside array type definitions
17. **File literal support** - Materialize Files with `contents` field as temp files with symlink resolution for Docker
18. **cwl.output.json processing** - Process File/Directory objects to resolve relative paths and add metadata (checksum, size, etc.)
19. **Directory type outputs** - Support Directory type in outputBinding with listing of contents
20. **Directory input listing** - Recursively resolve File/Directory objects in Directory listing
21. **Packed tool-only files** - Support `$graph` files containing only CommandLineTools (no Workflow), creating synthetic workflows
22. **Tool ID hash handling** - Strip `#` prefix from tool IDs for consistent lookup in `$graph` files
23. **Map/array JSON serialization** - Convert maps and arrays to JSON format in expressions (not Go's default format)
24. **Position expressions** - Support expressions in inputBinding.position (e.g., `position: $(self)`)
25. **OutputEval without glob** - Evaluate outputEval when glob is null
26. **Runtime in glob** - Pass runtime context to glob expression evaluation (e.g., `glob: $(runtime.outdir)`)
27. **Glob path handling** - Handle absolute paths and workDir-prefixed paths correctly
28. **Workflow input defaults** - Merge workflow input defaults with provided inputs
29. **CWL version validation** - Accept v1.0, v1.1, v1.2, and draft-3
30. **Workflows without outputs** - Allow empty outputs array in workflows
31. **Null in expressions** - Output "null" for nil values in string interpolation (for JSON)
32. **Docker path resolution** - Always resolve to absolute paths for Docker mounts

### Key Remaining Issues (23 failing tests)
- **External tool file references** (tests 8, 10, 36, 40-43, 54) - workflows referencing external .cwl files
- **EnvVarRequirement** (test 25) - environment variable setup not implemented
- **Format checking** (tests 14-16) - output format field not supported
- **Record field inputBindings** (test 74) - record fields with individual inputBindings
- **ShellCommandRequirement** (test 59) - shell command execution for `exit` command
- **loadContents limit** (tests 63-64) - 64KB limit enforcement
- **length on non-array** (test 66) - should fail when accessing .length on non-array
- **Colon in paths** (test 69) - Docker mount issues with colons
- Record type with field-level inputBindings
- Format checking tests (format field on outputs)
- ShellCommandRequirement (for shell builtins like exit)
- loadContents limit (64KB)
- Capture type validation (files vs directories)

---

## Session: 2026-02-17 (CWL Full Parser Implementation - Issue #36)

### Status: COMPLETED

Successfully implemented a complete CWL v1.2 parser and cwl-runner for the GoWe project.

### Files Created

1. **pkg/cwl/binding.go** - InputBinding, OutputBinding, Argument, SecondaryFileSchema, Dirent, EnvironmentDef types
2. **pkg/cwl/requirements.go** - All CWL requirement types (Docker, Resource, InitialWorkDir, EnvVar, Shell, InlineJavascript, etc.)
3. **internal/cwlexpr/evaluator.go** - JavaScript expression evaluator using goja
4. **internal/cwlexpr/context.go** - Expression context (inputs, self, runtime)
5. **internal/cwlexpr/evaluator_test.go** - Unit tests for expression evaluation
6. **internal/cmdline/builder.go** - Command line construction from CommandLineTool
7. **internal/cmdline/builder_test.go** - Unit tests for command line builder
8. **internal/cwlrunner/runner.go** - Main CWL runner orchestration
9. **internal/cwlrunner/execute.go** - Local and Docker execution engines
10. **internal/cwlrunner/scatter.go** - Scatter method implementations
11. **internal/cwlrunner/runner_test.go** - Unit tests for runner
12. **cmd/cwl-runner/main.go** - CLI entry point
13. **scripts/run-conformance.sh** - Conformance test runner script
14. **testdata/cwl-conformance/** - Test fixtures

### Files Modified

1. **pkg/cwl/tool.go** - Enhanced CommandLineTool with Arguments, Stdin, Stdout, Stderr, exit codes; enhanced ToolInputParam with InputBinding; added ExpressionTool type
2. **pkg/cwl/workflow.go** - Added ScatterMethod, Requirements to Step
3. **internal/parser/parser.go** - Enhanced parsing for all new binding/requirement fields
4. **go.mod** - Added goja dependency

### Key Features

1. **CWL Expression Evaluator** - Parameter references, expressions, code blocks, interpolation
2. **Command Line Builder** - Position sorting, prefix handling, valueFrom evaluation
3. **cwl-runner CLI** - validate, dag, print-command, execute modes
4. **Execution** - Local and Docker modes with output collection
5. **Scatter** - dotproduct, nested_crossproduct, flat_crossproduct

### Verification

```bash
go test ./...  # All tests pass
./bin/cwl-runner validate testdata/cwl-conformance/echo.cwl
./bin/cwl-runner --no-container testdata/cwl-conformance/echo.cwl testdata/cwl-conformance/echo-job.yml
```

---

## Session: 2026-02-16 (Remote Worker Implementation)

### Remote Worker Phase 1 + 2: COMPLETE

Implemented remote worker support — workers are separate processes that pull tasks from the server via HTTP, execute locally (Docker/Apptainer/bare), and report results back.

**New files created (8):**
- `pkg/model/worker.go` — Worker, WorkerState, ContainerRuntime types
- `internal/executor/worker.go` — WorkerExecutor (thin, reads state from store)
- `internal/server/handler_workers.go` — 7 worker API endpoints
- `internal/worker/runtime.go` — Runtime interface + Docker/Apptainer/Bare impls
- `internal/worker/stager.go` — Stager interface + FileStager
- `internal/worker/client.go` — HTTP client for server API
- `internal/worker/worker.go` — Worker main loop + task execution
- `cmd/worker/main.go` — Worker binary entry point

**Files modified (5):**
- `pkg/model/state.go` — Added `ExecutorTypeWorker`
- `internal/store/migrations.go` — Added workers table
- `internal/store/store.go` — Added Worker CRUD + CheckoutTask to interface
- `internal/store/sqlite.go` — Implemented Worker CRUD + CheckoutTask
- `internal/server/server.go` — Registered worker routes
- `cmd/server/main.go` — Registered WorkerExecutor at bootstrap

**Test files created (4):**
- `internal/executor/worker_test.go`
- `internal/worker/runtime_test.go`
- `internal/worker/stager_test.go`
- `internal/server/handler_workers_test.go`
- `internal/store/sqlite_test.go` (modified — added Worker + CheckoutTask tests)

**API endpoints (7):**
| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/workers` | GET | List workers |
| `/api/v1/workers` | POST | Register worker |
| `/api/v1/workers/{id}/heartbeat` | PUT | Heartbeat |
| `/api/v1/workers/{id}/work` | GET | Checkout task (204 if none) |
| `/api/v1/workers/{id}` | DELETE | Deregister worker |
| `/api/v1/workers/{id}/tasks/{tid}/status` | PUT | Report task status |
| `/api/v1/workers/{id}/tasks/{tid}/complete` | PUT | Report task completion |

**Verification:** `go build ./...` and `go test ./...` — all pass, zero errors.

### Next Steps
- Not yet committed — awaiting user direction
- E2E manual test: start server, submit worker-hinted workflow, start worker binary
- Phase 3 (future): ShockStager, WorkspaceStager for remote data staging

---

## Session: 2026-02-16 (CWL Tool Fixes)

### CWL Tool Audit & Fixes: COMPLETE

Fixed all 39 existing CWL tools and created 6 new ones based on `docs/BVBRC-App-Specs-Summary.md`.

**Files modified (26 CWL tools):**

| Tool | Changes |
|------|---------|
| GenomeAssembly | srr_ids→string[]?, added insert_size_mean/stdev to PE libs |
| GenomeAssembly2 | srr_ids→string[]?, expanded recipe enum, added normalize/filtlong/target_depth/max_bases, fixed genome_size to int |
| SARS2Assembly | srr_ids→string[]?, added primers/primer_version |
| ComprehensiveSARS2Analysis | srr_ids→string[]?, added primers/primer_version, fixed recipe enum |
| ComprehensiveGenomeAnalysis | srr_ids→string[]?, code→int?, domain enum expanded, added normalize/filtlong/target_depth, genome_size→int, added insert_size_mean/stdev |
| GenomeAnnotation | code→int?, domain enum expanded, added assembly_output |
| GenomeAnnotationGenbank | added raw_import_only, skip_contigs |
| GapfillModel | string→string[] for lists, added record arrays (uptake_limit, custom_bounds, objective) |
| FluxBalanceAnalysis | same as GapfillModel |
| FastqUtils | PE/SE libs→singular records (not arrays) |
| MetagenomeBinning | PE/SE libs→singular records |
| MetagenomicReadMapping | PE/SE libs→singular records |
| Variation | removed platform/orientation from PE, added insert_size_mean/stdev, simplified SE, srr_ids→string[]?, expanded mapper/caller enums |
| TaxonomicClassification | srr_ids→string[]?, added insert_size_mean/stdev to PE |
| FunctionalClassification | srr_ids→string[]? |
| RNASeq | experimental_conditions→string[]?, added sample_id/condition/insert_size to records |
| RNASeq2 | same as RNASeq |
| TnSeq | reference_genome_id→required, added gumbel to recipe enum |
| GeneTree | sequences→string[], feature/genome_metadata_fields→string[]? |
| CodonTree | genome_ids→optional, added genome_groups/genome_metadata_fields, fixed int defaults |
| MSA | added input_status/input_type/select_genomegroup/feature_list/genome_list/ref_type/strategy/ref_string, feature_groups→File[]?, expanded aligner enum |
| Homology | expanded input_source/db_source enums, added input_feature_group/input_genome_group/db_id_list/db_feature_group/db_genome_group/blast params |
| SequenceSubmission | metadata→required, added 12 personal metadata fields |
| MetaCATS | alignment_file/group_file→optional, added year_ranges/metadata_group/input_type/alphabet/groups/auto_groups |
| GenomeComparison | genome_ids/user_genomes/user_feature_groups→arrays |
| HASubtypeNumberingConversion | expanded input_source enum, added input_feature_list |
| PrimerDesign | added input_type/sequence_input, removed SEQUENCE_TEMPLATE, added PRIMER_PICK_INTERNAL_OLIGO |
| Sleep | removed output_path/output_file, outputs→empty |
| RunProbModelSEEDJob | removed output_path/output_file, outputs→empty |

**Files verified (no changes needed):**
- DifferentialExpression.cwl
- GenomeAlignment.cwl
- ModelReconstruction.cwl

**New CWL tools created (6):**
- CEIRRDataSubmission.cwl
- CoreGenomeMLST.cwl
- SARS2Wastewater.cwl
- TreeSort.cwl
- ViralAssembly.cwl
- WholeGenomeSNPAnalysis.cwl

### Next Steps
- Consider updating `cmd/gen-cwl-tools/main.go` to generate these corrected CWL patterns
- Update concrete output patterns (currently using generic File[] glob)
- Run tests to validate CWL tool parsing

---

## Session: 2026-02-13 (BV-BRC App Specs Research)

### BV-BRC App Specs Summary: COMPLETE

Researched all 34 BV-BRC template-based repos (42 distinct apps):
- Fetched app_spec JSON files from all repos
- Fetched README/md documentation from all repos
- Analyzed service-scripts for concrete output file patterns
- Compiled into `docs/BVBRC-App-Specs-Summary.md`

**Key findings:**
- 39 existing CWL tools, 6 apps need new CWL tools: CEIRRDataSubmission, CoreGenomeMLST, SARS2Wastewater, TreeSort, ViralAssembly, WholeGenomeSNPAnalysis
- 4 existing CWL tools not covered by research (FluxBalanceAnalysis, FunctionalClassification, PhylogeneticTree, RNASeq2)
- Special behaviors documented: donot_create_result_folder (Sleep, GapfillModel, ModelReconstruction, RunProbModelSEEDJob), singular lib params (ViralAssembly, FastqUtils, MetagenomicReadMapping), wrapper apps (ComprehensiveGenomeAnalysis, ComprehensiveSARS2Analysis)
- All inputs mapped to CWL types with correct group/record array handling
- Concrete output files identified from service-script analysis (save_file_to_file, p3-cp, write_dir patterns)

---

## Session: 2026-02-12 (E2E Test + BV-BRC Output Investigation)

### E2E Test: PASSED (run 2)

Successfully ran `test-pipeline.cwl` (Date -> Sleep) against live BV-BRC:

| Step | App | BV-BRC Job | State |
|------|-----|-----------|-------|
| get_date | Date | 21463335 | SUCCESS |
| wait | Sleep | 21463336 | SUCCESS |

- Submission `sub_45f030ee` reached COMPLETED
- **Date app produced output**: `/awilke@bvbrc/home/gowe-test/.test-pipeline/now`
- Content: `Thu Feb 12 17:01:36 CST 2026`

### BV-BRC Output Convention (NEW FINDING)

Documented in `docs/BVBRC-App-Output-Convention.md`. Key points:

**Framework creates two workspace objects per job:**
```
{output_path}/{output_file}      <-- job_result JSON metadata
{output_path}/.{output_file}/    <-- hidden folder with actual output files
```

- `result_folder = output_path + "/." + output_file` (dot prefix!)
- Apps write to `$app->result_folder()`, never use output_path/output_file directly
- `write_results()` enumerates the hidden folder and creates the job_result manifest
- The job_result JSON contains `output_files: [[path, uuid], ...]` — authoritative manifest

### Previous Bug Fix (committed earlier)

**Scientific notation job IDs** (`internal/executor/bvbrc.go:108`):
- Fixed with `strconv.FormatInt(int64(id), 10)` — committed as `8cc0a7a`

### Known Issues

1. **Logs empty** — `BVBRCExecutor.Logs()` returns empty for Date/Sleep. Issue #34.
2. **task_summary counts zero** — aggregation bug in submission response.
3. **CWL glob pattern incorrect** — matches job_result metadata, not actual output files. Needs output resolver rework.

---

### Phase Completion Status

- **Phase 1-7**: ALL DONE (v0.3.0 through v0.7.0)
- **DockerExecutor**: DONE (5c99647)
- **Phase 8**: MCP Server + Tool Gen — IN PROGRESS
- **CWL Directory type**: DONE (8c57a81, #31)
- **Auto-wrap bare CLTs**: DONE (43ca8f6, #28)
- **CWL Tool Generation**: v0.8.0 (42df7cb) — 39 tools with File[] output type
- **CWL Tool Fixes**: 2026-02-16 — all 45 tools corrected to match app specs

### Recent Commits (main)

```
42df7cb feat: regenerate CWL tools with File[] output type and path-derived glob (v0.8.0)
8cc0a7a fix: format BV-BRC job IDs as integers to prevent scientific notation
8c57a81 feat: map BV-BRC folder to CWL Directory with URI scheme support (#31)
43ca8f6 feat: auto-wrap bare CommandLineTools as single-step Workflows (#28)
```

### Open Issues (as of 2026-02-12)

**Gen-CWL-Tools improvements:**
- #30: Semantic types for genome/feature IDs
- #32: Enum values from allowed_values
- #33: Framework-injected outputs

**BV-BRC executor:**
- #34: Fix BVBRCExecutor.Logs
- #35: Document query_task_details API

**Infrastructure:**
- #9: API verification (PLANNED)
- #10: Output registry — now informed by BV-BRC output convention findings
- #11: Phase 8 — MCP Server + Tool Gen
- #16: Add HTTP integration tests
- #17: Set up CI/CD with GitHub Actions

### Key Files Reference

| File | Purpose |
|------|---------|
| `cmd/server/main.go` | Server entry point |
| `internal/executor/bvbrc.go` | BVBRCExecutor (Submit/Status/Cancel/Logs) |
| `internal/scheduler/resolve.go` | Input resolution + dependency checking |
| `internal/scheduler/loop.go` | Scheduler main loop (5 phases per tick) |
| `cwl/workflows/test-pipeline.cwl` | Date -> Sleep test workflow |
| `cwl/jobs/test-pipeline.yml` | Job inputs for test pipeline |
| `scripts/test-e2e.sh` | E2E test runner script |
| `docs/BVBRC-App-Output-Convention.md` | BV-BRC output convention docs |

### YAML Gotcha

`?` is a YAML special character. Inside flow mappings `{ }`, optional types like `string?` must be quoted: `{ type: "string?" }`. Block style doesn't need quoting.
