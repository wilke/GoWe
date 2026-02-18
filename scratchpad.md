# GoWe Scratchpad

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

### Current Status: 66/84 tests passing (79%)

### Remaining Failures (18 tests)
- **External tool file references** (tests 8, 10, 27, 36, 40-43, 54) - workflows referencing external .cwl files
- **Hints with $import** (test 25) - hints importing external files
- **Any type validation** (tests 38-39) - should-fail tests for Any type
- **SecondaryFiles handling** (tests 53, 54)
- **loadContents limit** (tests 63, 64) - 64KB limit enforcement
- **length on non-array** (test 66) - should fail when accessing .length on non-array
- **Colon in paths** (test 69) - path handling with colons
- **Record field inputBindings** (test 74) - record fields with individual inputBindings

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

### Next Steps
- Update gen-cwl-tools to use concrete output patterns instead of generic File[] glob
- Create CWL tools for 6 missing apps
- Resolve 4 unmatched existing CWL tools

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

**Impact on GoWe:**
- Current CWL glob `$(inputs.output_path.location)/$(inputs.output_file)*` matches the job_result metadata, not the actual files
- For real output resolution, GoWe should either:
  1. Read the `job_result` object and parse `output_files` (preferred)
  2. List `{output_path}/.{output_file}/` via Workspace.ls
- The `test-pipeline/` visible folder was empty; actual output was in `.test-pipeline/now`

### Created This Session

- `docs/BVBRC-App-Output-Convention.md` — full documentation of the output convention
- `scripts/test-e2e.sh` — shell script to run E2E test pipeline

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

### BV-BRC Source References

| File | Purpose |
|------|---------|
| `bvbrc_standalone_apps/service-scripts/App-Date.pl` | Date app implementation |
| `bvbrc_standalone_apps/app_specs/Date.json` | Date app spec |
| `dev_container/modules/app_service/lib/Bio/KBase/AppService/AppScript.pm` | App framework |
| `BV-BRC-Web/public/js/p3/widget/viewer/JobResult.js` | Web UI job result viewer |

### YAML Gotcha

`?` is a YAML special character. Inside flow mappings `{ }`, optional types like `string?` must be quoted: `{ type: "string?" }`. Block style doesn't need quoting.
