# GoWe Scratchpad

## Session: 2026-02-24 (Issue #52 - InitialWorkDirRequirement Conformance)

### Status: IN PROGRESS - 36/38 tests passing

Working on InitialWorkDirRequirement conformance (issue #52).

### Branch: `feature/InitialWorkDirRequirement`

### Progress

| Before | After | Change |
|--------|-------|--------|
| 30/38 | 36/38 | +6 tests |

### Fixes Applied

1. **Merged main** - Brought in metrics (#47), concurrency (#50), conditional (#48) changes
2. **Fixed trailing newlines in expressions** (aefb9fc) - Preserve trailing content after `${...}` code blocks
3. **Fixed JSON-stringified arrays in listing** (ef426e3) - Parse JSON when YAML `|` blocks cause array stringification
4. **Copy files for container execution** (d88e72e) - Copy files instead of symlink for Docker/Apptainer (symlinks don't work in containers)
5. **ShellCommandRequirement support** (ee7f7b7) - Wrap commands in `/bin/sh -c` when ShellCommandRequirement present
6. **Absolute entryname paths** (a0a786c) - Mount files at absolute paths inside containers
7. **dockerOutputDirectory** (a0a786c) - Collect outputs from custom output directory
8. **Validation for absolute paths** (a0a786c) - Ensure DockerRequirement is in requirements (not hints) for absolute entrynames

### Remaining Failures (2)

| Test | ID | Issue |
|------|----|-------|
| 1 | `initworkdir_expreng_requirements` | ExpressionEngineRequirement with `t()` template function not implemented |
| 2 | `initial_workdir_secondary_files_expr` | Complex secondaryFiles expressions with patterns in Docker |

### Required Conformance

84/84 tests still passing - no regressions.

### Next Steps

The remaining 2 tests require significant additional implementation:
- Test 1: Implement ExpressionEngineRequirement with engineConfig template functions
- Test 2: Fix secondaryFiles expression handling in Docker workflows

---

## Session: 2026-02-24 (Issue #47 - Per-Step Runtime and Memory Metrics)

### Status: MERGED - Issue #47 closed

Implemented per-step runtime and memory metrics for cwl-runner per issue #47, including per-iteration metrics for scatter steps.

### PR: #53 - MERGED (https://github.com/wilke/GoWe/pull/53)
### Merge commit: `322218d`
### Issue: #47 - auto-closed on merge

### Features Implemented

1. **Per-step duration** - Wall-clock time for each step on completion
2. **Peak memory usage** - Tracked via `getrusage` (`ru_maxrss`)
3. **Workflow summary** - Table printed to stderr with duration, peak memory, exit status
4. **JSON output** - Metrics included under `cwl:metrics` extension field when `--metrics` flag is enabled
5. **Per-iteration metrics** - Scatter steps capture per-iteration timing/memory
6. **Scatter summary stats** - Avg duration ± stddev, avg/max memory, success/fail counts

---

## Previous Sessions

See git history for earlier work on:
- Issue #50 - Global concurrency limit
- Conditional workflow support
- Step input conformance
- Parallel execution
- CWL conformance improvements
