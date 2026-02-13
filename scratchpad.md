# GoWe Scratchpad

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
