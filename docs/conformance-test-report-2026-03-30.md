# CWL v1.2 Conformance Test Report

- **Date**: 2026-03-30
- **Version**: `0e80f8b` → `b12e015+fixes` (branch: `feature/worker`)
- **Platform**: Linux 6.8.0-94-generic, Apptainer (no Docker)
- **Test suite**: CWL v1.2 conformance (`testdata/cwl-v1.2/conformance_tests.yaml`, 378 tests)
- **Parallelism**: `-j4`, timeout 60s per test

---

## Summary

| Mode | Passed | Failed | Pass Rate |
|------|--------|--------|-----------|
| **cwl-runner** (direct) | 377 | 1 | 99.7% |
| **server-local** (executor: local) | 360 | 18 | 95.2% |
| **server-worker** (executor: worker, runtime: none) | 368 | 10 | 97.4% |

---

## How to Run Conformance Tests

**IMPORTANT**: Always use `--verbose` — cwltest doesn't show which tests failed without it.
Always capture output to a timestamped file so you don't have to rerun after failures.

### Prerequisites

- `cwltest` Python package (installed in conda env)
- Binaries built via Apptainer (Go not natively installed):

```bash
mkdir -p /tmp/gomod bin && \
  COMMIT=$(git rev-parse HEAD) && \
  apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c \
    "go build -o bin/cwl-runner ./cmd/cwl-runner && \
     go build -o bin/gowe-server ./cmd/server && \
     go build -o bin/gowe ./cmd/cli && \
     go build -ldflags \"-X main.Version=$COMMIT\" -o bin/gowe-worker ./cmd/worker"
```

### Mode 1: cwl-runner (direct, no server)

```bash
REPORT=conformance-results-cwlrunner-$(date +%Y%m%d-%H%M%S).txt
cwltest \
  --test testdata/cwl-v1.2/conformance_tests.yaml \
  --tool bin/cwl-runner \
  -j4 --timeout=60 --verbose \
  2>&1 | tee "$REPORT"

echo "Results saved to $REPORT"
```

To run only required tests:
```bash
cwltest \
  --test testdata/cwl-v1.2/conformance_tests.yaml \
  --tool bin/cwl-runner \
  --tags required -j4 --timeout=60 --verbose \
  2>&1 | tee conformance-results-required-$(date +%Y%m%d-%H%M%S).txt
```

### Mode 2: server-local (server with local executor)

Start the server:
```bash
mkdir -p /tmp/gowe-test-local/uploads
bin/gowe-server \
  --addr :8093 \
  --db /tmp/gowe-test-local/gowe.db \
  --default-executor local \
  --allow-anonymous --anonymous-executors local \
  --scheduler-poll 100ms \
  --upload-backend local \
  --upload-local-dir /tmp/gowe-test-local/uploads \
  --upload-download-dirs /tmp/gowe-test-local/uploads \
  --log-level debug \
  > /tmp/gowe-test-local/server.log 2>&1 &
```

Create the wrapper script:
```bash
cat > /tmp/gowe-test-local-wrapper.sh << 'EOF'
#!/bin/bash
exec /scout/Experiments/GoWe/bin/gowe run --server http://localhost:8093 --quiet "$@"
EOF
chmod +x /tmp/gowe-test-local-wrapper.sh
```

Run tests:
```bash
REPORT=conformance-results-server-local-$(date +%Y%m%d-%H%M%S).txt
cwltest \
  --test testdata/cwl-v1.2/conformance_tests.yaml \
  --tool /tmp/gowe-test-local-wrapper.sh \
  -j4 --timeout=60 --verbose \
  2>&1 | tee "$REPORT"
```

### Mode 3: server-worker (server with worker executor)

Start the server:
```bash
mkdir -p /tmp/gowe-test-worker/{uploads,workdir}
bin/gowe-server \
  --addr :8094 \
  --db /tmp/gowe-test-worker/gowe.db \
  --default-executor worker \
  --allow-anonymous --anonymous-executors local,worker,container \
  --scheduler-poll 100ms \
  --upload-backend local \
  --upload-local-dir /tmp/gowe-test-worker/uploads \
  --upload-download-dirs /tmp/gowe-test-worker/uploads,/tmp/gowe-test-worker/workdir \
  --log-level debug \
  > /tmp/gowe-test-worker/server.log 2>&1 &
```

Start the worker:
```bash
bin/gowe-worker \
  --server http://localhost:8094 \
  --runtime none \
  --name test-worker \
  --workdir /tmp/gowe-test-worker/workdir \
  --stage-out file:///tmp/gowe-test-worker/uploads \
  --poll 200ms \
  --log-level debug \
  > /tmp/gowe-test-worker/worker.log 2>&1 &
```

Create the wrapper script:
```bash
cat > /tmp/gowe-test-worker-wrapper.sh << 'EOF'
#!/bin/bash
exec /scout/Experiments/GoWe/bin/gowe run --server http://localhost:8094 --quiet "$@"
EOF
chmod +x /tmp/gowe-test-worker-wrapper.sh
```

Run tests:
```bash
REPORT=conformance-results-server-worker-$(date +%Y%m%d-%H%M%S).txt
cwltest \
  --test testdata/cwl-v1.2/conformance_tests.yaml \
  --tool /tmp/gowe-test-worker-wrapper.sh \
  -j4 --timeout=60 --verbose \
  2>&1 | tee "$REPORT"
```

### Running specific tests

```bash
# By test number(s):
cwltest --test testdata/cwl-v1.2/conformance_tests.yaml --tool bin/cwl-runner -n 87,239 --timeout=60 --verbose 2>&1

# By tag:
cwltest --test testdata/cwl-v1.2/conformance_tests.yaml --tool bin/cwl-runner --tags required --timeout=60 --verbose 2>&1
```

**Note**: `-n` takes test numbers (e.g., `-n 1,3-6`), NOT tag names. Use `--tags` for filtering by tag.

### After a test run

Extract failures from a saved report:
```bash
grep "^Test [0-9]* failed:" "$REPORT" | sort -t' ' -k2 -n
```

Count results:
```bash
tail -1 "$REPORT"
```

---

## Failure Analysis

### cwl-runner (direct): 1 failure

| # | Test | Category | Error |
|---|------|----------|-------|
| 227 | `networkaccess_disabled` | Network isolation | Requires `unshare --net` (not available without root/user-ns on this machine) |

This is a known platform limitation — not a code bug.

### server-local: 18 failures

#### Category 1: Directory listing not populated (10 tests)

Tests 87, 88, 90, 96, 140, 141, 239, 244, 247, 248, 371

| # | Test | Error |
|---|------|-------|
| 87 | `directory_input_param_ref` | `$(inputs.indir.path)` returns undefined — Directory object missing `path` property in expression context |
| 88 | `directory_input_docker` | Same as 87: Directory `path` not set in expression context |
| 90 | `directory_secondaryfiles` | Directory secondaryFiles not resolved |
| 96 | `input_dir_inputbinding` | Directory inputBinding not producing correct command line |
| 140 | `job_input_secondary_subdirs` | secondaryFiles in subdirectories not resolved |
| 141 | `job_input_subdir_primary_and_secondary_subdirs` | secondaryFiles in same subdirectory not resolved |
| 239 | `outputbinding_glob_directory` | Output glob returns Directory without `listing` field (**required tag**) |
| 244 | `listing_default_none` | `listing` field missing from Directory output |
| 247 | `listing_requirement_shallow` | `LoadListingRequirement: shallow_listing` not applied |
| 248 | `listing_loadListing_shallow` | `loadListing: shallow_listing` on input not applied |
| 371 | `capture-files-and-dirs` | Glob with `[Directory, File]` type not capturing directories |

**Root cause**: The server-local executor does not fully populate Directory objects with `path`, `listing`, and `basename` fields when staging inputs through the server pipeline. The cwl-runner handles this directly in `internal/cwlrunner/` but the server scheduler + local executor path skips some of these steps.

#### Category 2: Sub-workflow / scatter (2 tests)

| # | Test | Error |
|---|------|-------|
| 51 | `step_input_default_value_nosource` | Step input default with empty source in sub-workflow |
| 85 | `wf_scatter_oneparam_valueFrom` | Scatter with valueFrom on input parameter |

**Root cause**: Sub-workflow step input wiring — default values and valueFrom on scattered inputs not fully propagated through the scheduler's child submission mechanism.

#### Category 3: Expression context (1 test)

| # | Test | Error |
|---|------|-------|
| 115 | `nameroot_nameext_generated` | `nameroot`/`nameext` not generated from `basename` at execution time |

**Root cause**: Server pipeline doesn't compute `nameroot`/`nameext` fields on File objects before passing to expression evaluator.

#### Category 4: Record secondaryFiles (1 test)

| # | Test | Error |
|---|------|-------|
| 207 | `secondary_files_missing` | Validation of missing secondaryFiles in record type (**required tag**) |

**Root cause**: Server doesn't validate secondaryFiles presence for record-typed inputs.

#### Category 5: Inplace update (2 tests)

| # | Test | Error |
|---|------|-------|
| 237 | `modify_file_content` | `InplaceUpdateRequirement` on file |
| 238 | `modify_directory_content` | `InplaceUpdateRequirement` on directory |

**Root cause**: `InplaceUpdateRequirement` (writable inputs) not implemented in server executor path.

#### Category 6: Network isolation (1 test)

| # | Test | Error |
|---|------|-------|
| 227 | `networkaccess_disabled` | Same platform limitation as cwl-runner |

### server-worker (runtime: none): 10 failures

| # | Test | Category | Error |
|---|------|----------|-------|
| 54 | `revsort` | Sub-workflow | Multi-step sub-workflow timed out (scheduler blocks during child execution) |
| 107 | `docker_output_dir` | Docker-specific | Requires container with `dockerOutputDirectory` — impossible with runtime=none |
| 114 | `revsort-packed` | Sub-workflow | Same as 54 (packed format) |
| 177 | `docker_run_cmd` | Docker-specific | Requires `CMD` from Docker image |
| 207 | `secondary_files_missing` | Record secondaryFiles | Same as server-local (**required tag**) |
| 227 | `networkaccess_disabled` | Network isolation | Same platform limitation as cwl-runner |
| 235 | `symlink_illegal` | Symlink handling | Illegal symlink detection not enforced in worker staging |
| 237 | `modify_file_content` | Inplace update | Same as server-local |
| 238 | `modify_directory_content` | Inplace update | Same as server-local |
| 335 | `iwd_container_entryname1` | IWDR container | InitialWorkDirRequirement with container entryname |

**Notes**:
- Tests 107, 177, 335 require a real container runtime — expected failures with `--runtime none`
- Tests 54, 114 (sub-workflows) fail because `waitForStepCompletion` blocks the scheduler tick during child execution; with 2 workers, throughput is limited and tests timeout
- The remaining failures (207, 227, 235, 237, 238) are shared with server-local or cwl-runner

**Fixes applied** (vs initial run):
- Removed `requiresDocker` filter in `CheckoutTask` that prevented runtime=none workers from picking up tasks with DockerImage hints (was causing 356/378 failures)
- Added 500ms minimum poll interval and 10-minute timeout to `waitForStepCompletion` to prevent infinite spin loop

---

## Required Test Failures

Two of the 18 server-local failures are tagged `required`:

| # | Test | Status |
|---|------|--------|
| 207 | `secondary_files_missing` | Record secondaryFiles validation not implemented |
| 239 | `outputbinding_glob_directory` | Glob output missing `listing` field on Directory |

These should be prioritized for server-mode compliance.

---

## Cross-Mode Comparison

| # | Test | cwl-runner | srv-local | srv-worker |
|---|------|:----------:|:---------:|:----------:|
| 51 | step_input_default_value_nosource | PASS | FAIL | PASS |
| 54 | revsort (sub-workflow) | PASS | PASS | FAIL |
| 85 | scatter_valuefrom | PASS | FAIL | PASS |
| 87-96 | directory_input (6 tests) | PASS | FAIL | PASS |
| 107 | docker_output_dir | PASS | PASS | FAIL |
| 114 | revsort-packed | PASS | PASS | FAIL |
| 115 | nameroot_nameext | PASS | FAIL | PASS |
| 140-141 | secondary_subdirs (2 tests) | PASS | FAIL | PASS |
| 177 | docker_run_cmd | PASS | PASS | FAIL |
| 207 | secondary_files_missing | PASS | FAIL | FAIL |
| 227 | networkaccess_disabled | FAIL | FAIL | FAIL |
| 235 | symlink_illegal | PASS | PASS | FAIL |
| 237-238 | inplace_update (2 tests) | PASS | FAIL | FAIL |
| 239 | glob_directory | PASS | FAIL | PASS |
| 244,247,248 | listing (3 tests) | PASS | FAIL | PASS |
| 335 | iwd_container_entryname | PASS | PASS | FAIL |
| 371 | capture_files_and_dirs | PASS | FAIL | PASS |

Key observations:
- 11 server-local failures (directory/listing) **pass** in server-worker — worker path handles Directory objects better
- 5 server-worker failures (54,107,114,177,335) are either Docker-specific or sub-workflow timeouts — not shared with server-local
- 3 failures shared across server modes: 207 (secondaryFiles), 237-238 (inplace update)
- 1 failure shared across all modes: 227 (network isolation, platform limitation)

## Known Issues

1. **Synchronous child submission execution** (server-worker): `waitForStepCompletion` blocks the scheduler tick during sub-workflow execution. Mitigated with 500ms poll interval and 10-minute timeout, but reduces throughput. Proper fix: make child submissions asynchronous (process through normal scheduler phases).

2. **Directory object population gap** (server-local): The local executor path doesn't populate `path`, `listing`, `basename`, `nameroot`, `nameext` on File/Directory objects as completely as the cwl-runner or worker paths.

3. **InplaceUpdateRequirement** not implemented in distributed execution modes.

4. **Network isolation** (`networkaccess2`): Requires `unshare --net` which needs root or user namespaces — platform limitation.

---

## Comparison with Previous Baseline

| Mode | Previous | Current | Delta |
|------|----------|---------|-------|
| cwl-runner | 377/378 | 377/378 | No regression |
| server-local | — | 360/378 | First full run |
| server-worker | Blocked (22/378) | 368/378 | Fixed: CheckoutTask filter + spin loop timeout |
