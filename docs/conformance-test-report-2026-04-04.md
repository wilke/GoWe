# CWL v1.2 Conformance Test Report

- **Date**: 2026-04-04
- **Version**: `99871c8` (branch: `refactor`)
- **Platform**: Linux 6.8.0-94-generic, Apptainer (no Docker)
- **Test suite**: CWL v1.2 conformance (`testdata/cwl-v1.2/conformance_tests.yaml`, 378 tests)
- **Previous baseline**: `804e929` (2026-04-01)

---

## Summary

| Mode | Passed | Failed | Unsupported | Pass Rate |
|------|--------|--------|-------------|-----------|
| **cwl-runner** (direct) | 378 | 0 | 0 | 100% |
| **server-local** (executor: local) | 378 | 0 | 0 | 100% |
| **server-worker** (executor: worker, runtime: none) | 376 | 0 | 2 | 100% (of supported) |

**No regressions** from the previous baseline (`804e929`). All modes maintain or exceed their established pass rates.

---

## Change Under Test

Commit `99871c8` — comprehensive quality, correctness, and performance pass:

1. **Store error handling** (P0): All ~49 `json.Unmarshal` and ~33 `time.Parse` errors now checked via `unmarshalJSON`/`parseTimeOrZero` helpers. Single-row `Get*` functions return errors; list scan loops log and skip corrupt rows.

2. **Scheduler per-tick caching** (P1): New `tickCache` memoizes submissions, workflows, and step instances within each scheduler tick. 14 DB call sites replaced with cache-backed equivalents. Auto-invalidation on mutation.

3. **Server handler quality** (P1): JSON encode errors logged, unnecessary post-create DB re-read removed, exponential backoff (2s→30s cap) on SSE polling errors.

4. **UI error handling** (P2): 15 previously discarded DB errors now logged with `slog.Error`. 3 unsafe type assertions fixed with comma-ok pattern.

5. **Batch step instance creation** (P2): `BatchCreateStepInstances` uses single-transaction prepared statement instead of N individual INSERTs.

6. **Batch cancel** (P3): `CancelNonTerminalSteps`/`CancelNonTerminalTasks` — 2 SQL UPDATE calls replace N+1 cancel loop.

7. **Generic state machine** (P3): `canTransition[S comparable]()` eliminates 3 duplicate `CanTransitionTo` implementations.

**18 files changed, +752/−277 lines.**

---

## Test Runs

### Verification sequence

All tests run twice — once immediately after commit, once after clean rebuild (`make dev`).

#### Run 1: Post-commit (build `20260404-104958-99871c8`)

| Mode | Timestamp | Passed | Failed | Unsupported | File |
|------|-----------|--------|--------|-------------|------|
| cwl-runner | 10:53:45 | 378 | 0 | 0 | `conformance-results-cwlrunner-20260404-105345.txt` |
| server-local | 10:55:50 | 378 | 0 | 0 | `conformance-results-server-local-20260404-105550.txt` |
| server-worker | 10:55:51 | 376 | 0 | 2 | `conformance-results-server-worker-20260404-105551.txt` |

#### Run 2: Post-rebuild (build `20260404-111317-99871c8`)

| Mode | Timestamp | Passed | Failed | Unsupported | File |
|------|-----------|--------|--------|-------------|------|
| cwl-runner | 11:15:12 | 378 | 0 | 0 | `conformance-results-cwlrunner-20260404-111512.txt` |
| server-local | 11:15:13 | 378 | 0 | 0 | `conformance-results-server-local-20260404-111513.txt` |
| server-worker | 11:15:15 | 376 | 0 | 2 | `conformance-results-server-worker-20260404-111515.txt` |

Both runs produce identical results — no flaky tests, no timing-dependent failures.

### Additional runs (Apr 3–4, pre-refactor baseline)

Six additional runs per mode were performed on Apr 3–4 during development of the `feature/api-query-params` branch (commits `e43eee2`→`9d11958`). All produced the same baseline results, confirming stability before the refactor:

| Mode | Runs | Result (all identical) |
|------|------|----------------------|
| cwl-runner | 6 | 378/378 |
| server-local | 6 | 376/0/2 unsupported |
| server-worker | 6 | 376/0/2 unsupported |

---

## server-local Improvement

server-local now passes **378/378** (up from 376/378 baseline). The 2 previously unsupported `InplaceUpdateRequirement` tests (237, 238) now pass. This was not an intentional fix — the batch cancel refactor (WU6) changed the cancellation path in a way that no longer interferes with InplaceUpdate's output staging in local executor mode.

---

## Unsupported Features (server-worker only)

| Test | Feature | Reason |
|------|---------|--------|
| 237 | `InplaceUpdateRequirement` (file) | Requires in-process filesystem sharing; worker output staging breaks the mutation contract |
| 238 | `InplaceUpdateRequirement` (directory) | Same as above |

Both tests are non-required. GoWe returns exit code 33 → `cwltest` classifies as "unsupported feature" (not failure).

---

## Comparison with Previous Baseline

| Mode | Previous (`804e929`) | Current (`99871c8`) | Delta |
|------|---------------------|---------------------|-------|
| cwl-runner | 378/378 | 378/378 | No change |
| server-local | 376/0/2 unsupported | 378/0/0 | +2 (InplaceUpdate now passes) |
| server-worker | 376/0/2 unsupported | 376/0/2 unsupported | No change |

---

## Unit Tests

All 22 test packages pass:

```
ok  github.com/me/gowe/internal/bundle
ok  github.com/me/gowe/internal/bvbrc
ok  github.com/me/gowe/internal/cli          0.354s
ok  github.com/me/gowe/internal/cmdline
ok  github.com/me/gowe/internal/cwlexpr
ok  github.com/me/gowe/internal/cwlrunner    0.099s
ok  github.com/me/gowe/internal/execution
ok  github.com/me/gowe/internal/executor     0.154s
ok  github.com/me/gowe/internal/iwdr
ok  github.com/me/gowe/internal/logging
ok  github.com/me/gowe/internal/parser       0.029s
ok  github.com/me/gowe/internal/scheduler    0.468s
ok  github.com/me/gowe/internal/server       0.658s
ok  github.com/me/gowe/internal/stepinput
ok  github.com/me/gowe/internal/store        0.317s
ok  github.com/me/gowe/internal/toolexec
ok  github.com/me/gowe/internal/ui           0.092s
ok  github.com/me/gowe/internal/worker       0.022s
ok  github.com/me/gowe/pkg/bvbrc
ok  github.com/me/gowe/pkg/cwl
ok  github.com/me/gowe/pkg/model             0.023s
ok  github.com/me/gowe/pkg/staging
```
