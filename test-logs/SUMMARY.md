# Conformance Test Log Summary

## cwl-runner (standalone)

| Date | File | Result | Notes |
|------|------|--------|-------|
| 2026-03-30 16:26 | `conformance-results-cwlrunner-20260330-162603.txt` | 376/378 (2 failures) | First scripted run |
| 2026-03-30 16:42 | `conformance-results-cwlrunner-20260330-164240.txt` | 366/378 (12 failures) | Regression during development |
| 2026-03-30 22:06 | `conformance-results-cwlrunner-20260330-220645.txt` | 378/378 | First full pass |
| 2026-03-31 09:15 | `conformance-results-cwlrunner-20260331-091545.txt` | 378/378 | Confirmed after scheduler changes |
| 2026-04-01 13:05 | `conformance-results-cwlrunner-20260401-130521.txt` | 373/378 (5 failures) | ValidateFileInputs regression |
| 2026-04-01 13:38 | `conformance-results-cwlrunner-20260401-133803.txt` | 373/378 (5 failures) | First fix attempt (insufficient) |
| 2026-04-01 13:43 | `conformance-results-cwlrunner-20260401-134306.txt` | 378/378 | Regression fixed (804e929) |
| 2026-04-03 15:39 | `conformance-results-cwlrunner-20260403-153926.txt` | 378/378 | Pre-refactor baseline (api-query-params) |
| 2026-04-03 16:11 | `conformance-results-cwlrunner-20260403-161152.txt` | 378/378 | |
| 2026-04-03 22:15 | `conformance-results-cwlrunner-20260403-221526.txt` | 378/378 | |
| 2026-04-04 00:03 | `conformance-results-cwlrunner-20260404-000347.txt` | 378/378 | |
| 2026-04-04 00:37 | `conformance-results-cwlrunner-20260404-003718.txt` | 378/378 | |
| 2026-04-04 01:27 | `conformance-results-cwlrunner-20260404-012721.txt` | 378/378 | |
| 2026-04-04 10:50 | `conformance-results-cwlrunner-20260404-105027.txt` | 378/378 | Required-only, post-refactor (99871c8) |
| 2026-04-04 10:53 | `conformance-results-cwlrunner-20260404-105345.txt` | 378/378 | Post-refactor full suite (99871c8) |
| 2026-04-04 11:15 | `conformance-results-cwlrunner-20260404-111512.txt` | 378/378 | Post-rebuild verification (99871c8) |

## server-local

| Date | File | Result | Notes |
|------|------|--------|-------|
| 2026-03-30 16:29 | `conformance-results-server-local-20260330-162916.txt` | 369/378 (9 failures) | First server-local run |
| 2026-03-30 21:55 | `conformance-results-server-local-20260330-215555.txt` | 368/378 (10 failures) | |
| 2026-03-30 22:06 | `conformance-results-server-local-20260330-220647.txt` | 375/378 (3 failures) | |
| 2026-03-31 09:12 | `conformance-results-server-local-20260331-091235.txt` | 376/378 (2 failures) | InplaceUpdate counted as failure |
| 2026-03-31 09:21 | `conformance-results-server-local-20260331-092116.txt` | 376/378 (2 failures) | |
| 2026-03-31 09:37 | `conformance-results-server-local-20260331-093753.txt` | 377/378 (1 failure) | Best pre-exit33 |
| 2026-04-01 13:56 | `conformance-results-server-local-20260401-135621.txt` | 376/0/2 unsupported | Baseline achieved (804e929) |
| 2026-04-03 15:39 | `conformance-results-server-local-20260403-153929.txt` | 376/0/2 unsupported | Pre-refactor baseline |
| 2026-04-03 16:21 | `conformance-results-server-local-20260403-162120.txt` | 376/0/2 unsupported | |
| 2026-04-03 22:15 | `conformance-results-server-local-20260403-221536.txt` | 376/0/2 unsupported | |
| 2026-04-04 00:03 | `conformance-results-server-local-20260404-000352.txt` | 376/0/2 unsupported | |
| 2026-04-04 00:37 | `conformance-results-server-local-20260404-003719.txt` | 376/0/2 unsupported | |
| 2026-04-04 01:27 | `conformance-results-server-local-20260404-012722.txt` | 376/0/2 unsupported | |
| 2026-04-04 10:55 | `conformance-results-server-local-20260404-105550.txt` | 378/0/0 | Post-refactor (99871c8), InplaceUpdate now passes |
| 2026-04-04 11:15 | `conformance-results-server-local-20260404-111513.txt` | 378/0/0 | Post-rebuild verification (99871c8) |

## server-worker

| Date | File | Result | Notes |
|------|------|--------|-------|
| 2026-03-30 10:27 | `conformance-results-server-worker-20260330-102703.txt` | 368/378 (10 failures) | First full run |
| 2026-03-30 13:15 | `conformance-results-server-worker-20260330-131523.txt` | Broken | Required-only, immediate failure |
| 2026-03-30 13:26 | `conformance-results-server-worker-20260330-132646.txt` | 7/84 (77 failures) | Required-only, broken build |
| 2026-03-30 14:58 | `conformance-results-server-worker-20260330-145828.txt` | 7/84 (77 failures) | Required-only, still broken |
| 2026-03-30 16:12 | `conformance-results-server-worker-20260330-161245.txt` | 82/84 (2 failures) | Required-only, nearly fixed |
| 2026-03-30 16:35 | `conformance-results-server-worker-20260330-163525.txt` | 373/378 (5 failures) | First full suite |
| 2026-03-30 22:06 | `conformance-results-server-worker-20260330-220649.txt` | 375/378 (3 failures) | |
| 2026-03-31 09:11 | `conformance-results-server-worker-20260331-091136.txt` | Broken | Partial run, error on test 207 |
| 2026-03-31 09:12 | `conformance-results-server-worker-20260331-091248.txt` | 376/378 (2 failures) | InplaceUpdate counted as failure |
| 2026-03-31 09:15 | `conformance-results-server-worker-20260331-091554.txt` | 376/378 (2 failures) | |
| 2026-03-31 09:20 | `conformance-results-server-worker-20260331-092020.txt` | Partial | Only tests 237,238 — "All passed" |
| 2026-03-31 09:29 | `conformance-results-server-worker-20260331-092925.txt` | Partial | Only tests 237,238 — "All passed" |
| 2026-03-31 09:43 | `conformance-results-server-worker-20260331-094337.txt` | 376/378 (2 failures) | Full suite, InplaceUpdate as failure |
| 2026-03-31 12:15 | `conformance-results-server-worker-20260331-121550.txt` | 0/0/2 unsupported | Only tests 237,238 (exit code 33 verification) |
| 2026-03-31 12:17 | `conformance-results-server-worker-20260331-121703.txt` | 376/0/2 unsupported | Baseline achieved (exit code 33) |
| 2026-04-01 14:02 | `conformance-results-server-worker-20260401-140224.txt` | 376/0/2 unsupported | Confirmed after regression fix (804e929) |
| 2026-04-03 15:39 | `conformance-results-server-worker-20260403-153952.txt` | 376/0/2 unsupported | Pre-refactor baseline |
| 2026-04-03 18:01 | `conformance-results-server-worker-20260403-180159.txt` | 376/0/2 unsupported | |
| 2026-04-03 22:15 | `conformance-results-server-worker-20260403-221541.txt` | 376/0/2 unsupported | |
| 2026-04-04 00:03 | `conformance-results-server-worker-20260404-000357.txt` | 376/0/2 unsupported | |
| 2026-04-04 00:37 | `conformance-results-server-worker-20260404-003721.txt` | 376/0/2 unsupported | |
| 2026-04-04 01:27 | `conformance-results-server-worker-20260404-012724.txt` | 376/0/2 unsupported | |
| 2026-04-04 10:53 | `conformance-results-server-worker-20260404-105347.txt` | 376/0/2 unsupported | Post-refactor (99871c8) |
| 2026-04-04 10:55 | `conformance-results-server-worker-20260404-105551.txt` | 376/0/2 unsupported | Post-refactor (99871c8) |
| 2026-04-04 11:15 | `conformance-results-server-worker-20260404-111515.txt` | 376/0/2 unsupported | Post-rebuild verification (99871c8) |

---

## Deleted Files

Ad-hoc named files that didn't follow the `conformance-results-{mode}-{YYYYMMDD-HHMMSS}.txt` convention were removed:

| File | Reason |
|------|--------|
| `conformance-cwlrunner.log` | Ad-hoc name, duplicate of `20260330-162603` |
| `conformance-cwlrunner-all.log` | Incomplete run, no result summary |
| `conformance-cwlrunner-fixed.log` | Incomplete run, development scratch |
| `conformance-cwlrunner-v2.log` | Ad-hoc name, superseded by `20260330-220645` |
| `conformance-cwlrunner-v3.log` | Ad-hoc name, superseded by `20260330-220645` |
| `conformance-cwlrunner-final.log` | Ad-hoc name, superseded by `20260330-220645` |
| `conformance-server-local-all.log` | No result summary, raw log only |
| `conformance-server-local-final.log` | No result summary, raw log only |
| `conformance-server-worker-all.log` | No result summary, raw log only |
| `conformance-results-full.txt` | Pre-feature baseline (2026-03-09), old naming |
| `conformance-distributed-apptainer-results.txt` | Old naming convention (2026-03-10) |
| `20260329-211910-0e80f8b/` | Empty directory from early tagged build |
