# Conformance Test Error Analysis

**Date**: 2026-03-07
**Branch**: `feature/conformance-tests`
**Commit**: `8a54790`

## Test Results Summary

| Mode | Passed | Failed | Total |
|------|--------|--------|-------|
| **server-local** | 371 | 7 | 378 |
| **distributed-docker** | 376 | 2 | 378 |

---

## Grouped Failures

### Group 1: InplaceUpdateRequirement (tests 237, 238 — both modes)

| Test | Name | Mode |
|------|------|------|
| 237 | `modify_file_content` | server-local, distributed-docker |
| 238 | `modify_directory_content` | server-local, distributed-docker |

- **server-local**: Output mismatch — inplace updates don't propagate between steps (got `a=3` instead of `a=4`)
- **distributed-docker**: Rejected early (intentionally unsupported — no shared filesystem)
- **Root cause**: InplaceUpdateRequirement needs shared mutable state between steps. In server-local mode, each task gets its own working directory, so side effects don't propagate.

### Group 2: should_fail test passing (test 207 — server-local only)

| Test | Name | Mode |
|------|------|------|
| 207 | `secondary_files_missing` | server-local |

- **Error**: "Returned zero but it should be non-zero" — the test expects the runner to reject a workflow with missing required secondaryFiles on a record field, but the server-local path succeeds instead of failing.
- **Root cause**: SecondaryFiles validation on record fields not enforced in the local executor path.

### Group 3: Scatter valueFrom output collision (test 85 — server-local only)

| Test | Name | Mode |
|------|------|------|
| 85 | `wf_scatter_oneparam_valueFrom` | server-local |

- **Error**: Output is an array of 2 files but both have `location: .../cwl.stdout.txt` (same path). Expected: each scatter iteration produces a distinct output file.
- **Root cause**: All scatter tasks write stdout to the same output directory, so files overwrite each other. The output collector sees the same file path for both iterations instead of distinct files.

### Group 4: ExpressionTool directory output missing listing location (test 105 — server-local only)

| Test | Name | Mode |
|------|------|------|
| 105 | `exprtool_directory_literal` | server-local |

- **Error**: Directory output listing has `location: "a_directory"` expected but got files with absolute paths and no wrapping directory location `"a_directory"`. The listing files are directly in outdir instead of under a subdirectory.
- **Root cause**: ExpressionTool Directory literal materialization doesn't preserve the directory basename in the output path structure.

### Group 5: Writable directory output path leaking task dir (test 111 — server-local only)

| Test | Name | Mode |
|------|------|------|
| 111 | `input_dir_recurs_copy_writable` | server-local |

- **Error**: Expected `location: "work_dir"` and `location: "work_dir/c"` but got absolute task directory paths like `file:///...task_.../work_dir` and `file:///...task_.../work_dir/c`. Files inside the directory got outdir paths but the directory itself leaked the task working directory.
- **Root cause**: Directory output collection doesn't rebase the directory location to the outdir — it preserves the internal task working directory path.

### Group 6: nameroot/nameext not generated (test 115 — server-local only)

| Test | Name | Mode |
|------|------|------|
| 115 | `nameroot_nameext_generated` | server-local |

- **Error**: Expected 2 distinct output files (`rootFile` and `extFile`) with different checksums and sizes, but both have `basename: "out.txt"` and point to the same path. The tool writes `$(inputs.file1.nameroot)` and `$(inputs.file1.nameext)` to separate files.
- **Root cause**: `nameroot` and `nameext` fields are not being populated on File objects at execution time, so the expressions evaluate incorrectly.

---

## Summary by Root Cause Category

| Category | Tests | Fixable? |
|----------|-------|----------|
| InplaceUpdate (architectural) | 237, 238 | server-local: needs shared workdir; distributed: intentionally unsupported |
| SecondaryFiles validation | 207 | Yes — enforce required SF check on record fields in local executor |
| Scatter output isolation | 85 | Yes — each scatter task needs a unique output subdirectory |
| ExpressionTool dir materialization | 105 | Yes — preserve directory basename in output structure |
| Directory output path rebasing | 111 | Yes — rebase directory location to outdir on collection |
| nameroot/nameext population | 115 | Yes — populate these fields on File inputs before execution |

## Cross-Mode Comparison

- **distributed-docker** (376/378) is ahead of **server-local** (371/378) by 5 tests
- The 5 server-local-only failures (85, 105, 111, 115, 207) are all in the local executor path, not the upload/distributed pipeline
- Tests 237, 238 fail in both modes but for different reasons (output mismatch vs early rejection)
