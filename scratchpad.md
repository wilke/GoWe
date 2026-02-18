# GoWe Scratchpad

## Session: 2026-02-18 Morning (CWL Conformance - 96.4%)

### Status: 81/84 TESTS PASSING (96.4%) on macOS | 84/84 ON LINUX

Improved CWL conformance test pass rate from 79/84 to **81/84 (96.4%)** on macOS.
**All 84 tests pass on Linux** (verified in Ubuntu container).

### Key Improvements (79/84 → 81/84)

53. **SecondaryFiles for record fields** - Added `SecondaryFiles` field to `RecordField` type
54. **Workflow input secondaryFiles** - Parse and resolve secondaryFiles from workflow input declarations
55. **Tool input secondaryFiles** - Resolve secondaryFiles for direct tool execution
56. **SecondaryFiles validation** - Validate required secondary files exist in File objects
57. **Secondary file resolution context** - Distinguish between direct tool execution and workflow steps
58. **Packed document validator fix** - Handle tool IDs with .cwl suffix in $graph documents

### Platform Difference Verified

Confirmed macOS vs Linux `rev`/`sort` output differs:
```
macOS:  sha1$c67d838c10ff86680366bf168d7bae7f11ba3b20
Linux:  sha1$b9214658cc453331b62c2282b772a5c063dbd284 (expected)
```
Tests 10 and 27 pass on Linux but fail on macOS due to this difference.

### Files Modified
- `pkg/cwl/binding.go` - Added SecondaryFiles to RecordField
- `pkg/cwl/workflow.go` - Added RecordFields and SecondaryFiles to InputParam
- `internal/parser/parser.go` - Parse secondaryFiles for record fields and workflow inputs
- `internal/cwlrunner/runner.go` - Added secondaryFiles validation and resolution
- `internal/cwlrunner/scatter.go` - Updated executeTool call signature
- `internal/parser/validator.go` - Fixed tool reference validation for packed documents

### Current Status
- **macOS**: 81/84 tests passing (96.4%)
- **Linux**: 84/84 tests passing (100%)

### Remaining macOS Failures (3 tests)
- **Test 10, 27** - Platform difference: macOS `rev`/`sort` differ from Linux (not a bug)
- **Test 69** - Docker limitation with colons in paths (Docker uses : as volume separator)

### Git Status
- v0.8.8: `e435472 feat: improve CWL conformance from 79/84 to 81/84 (96.4%)`
- Latest: `9073047 fix: validator now handles packed document tool IDs with .cwl suffix`

---

## Session: 2026-02-18 (Documentation Update)

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
