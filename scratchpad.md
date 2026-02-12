# GoWe Scratchpad

## Session: 2026-02-12 (E2E Test)

### End-to-End Test: PASSED

Successfully ran `test-pipeline.cwl` (Date -> Sleep) against live BV-BRC:

| Step | App | BV-BRC Job | State | Duration |
|------|-----|-----------|-------|----------|
| get_date | Date | 21463320 | SUCCESS | ~70s |
| wait | Sleep | 21463323 | SUCCESS | ~4s |

- Submission `sub_ff2295ed` reached COMPLETED
- Resolver correctly tolerated missing upstream outputs (nil for optional trigger)
- Both tasks transitioned: PENDING -> SCHEDULED -> QUEUED -> RUNNING -> SUCCESS

### Bug Fixed This Session

**Scientific notation job IDs** (`internal/executor/bvbrc.go:108`):
- `fmt.Sprintf("%v", float64(21463320))` produced `"2.1463317e+07"`
- `query_tasks` response keyed by `"21463320"` — lookup mismatch -> stuck at QUEUED
- Fix: `strconv.FormatInt(int64(id), 10)` -> `"21463320"`
- NOT YET COMMITTED

### Known Issues Found During Test

1. **Logs empty** — `BVBRCExecutor.Logs()` calls `query_app_log` but returns empty for Date/Sleep. Issue #34.
2. **task_summary counts zero** — `task_summary` in submission response shows all zeros despite tasks existing. Likely aggregation bug.
3. **Date/Sleep produce no workspace output** — Expected; they're test apps. Real apps (GenomeAnnotation etc.) would write files.

### Uncommitted Change

- `internal/executor/bvbrc.go` — float64-to-int job ID fix (strconv.FormatInt)

---

### Phase Completion Status

- **Phase 1-7**: ALL DONE (v0.3.0 through v0.7.0)
- **DockerExecutor**: DONE (5c99647)
- **Phase 8**: MCP Server + Tool Gen — IN PROGRESS
- **CWL Directory type**: DONE (8c57a81, #31)
- **Auto-wrap bare CLTs**: DONE (43ca8f6, #28)

### Recent Commits (main)

```
8c57a81 feat: map BV-BRC folder to CWL Directory with URI scheme support (#31)
43ca8f6 feat: auto-wrap bare CommandLineTools as single-step Workflows (#28)
07ee63a fix: handle numeric job IDs from BV-BRC start_app response
4ffd2b4 fix: tolerate missing outputs on completed BV-BRC tasks in resolver
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
- #10: Output registry
- #11: Phase 8 — MCP Server + Tool Gen
- #16: Add HTTP integration tests
- #17: Set up CI/CD with GitHub Actions

### p3 Tools

Location: `/Users/me/Development/bvbrc/BV-BRC-Go-SDK/dist/bin/`
Usage: `export PATH="/Users/me/Development/bvbrc/BV-BRC-Go-SDK/dist/bin:$PATH"`

### Key Files Reference

| File | Purpose |
|------|---------|
| `cmd/server/main.go` | Server entry point |
| `internal/executor/bvbrc.go` | BVBRCExecutor (Submit/Status/Cancel/Logs) |
| `internal/scheduler/resolve.go` | Input resolution + dependency checking |
| `internal/scheduler/loop.go` | Scheduler main loop (5 phases per tick) |
| `cwl/workflows/test-pipeline.cwl` | Date -> Sleep test workflow |
| `cwl/jobs/test-pipeline.yml` | Job inputs for test pipeline |

### YAML Gotcha

`?` is a YAML special character. Inside flow mappings `{ }`, optional types like `string?` must be quoted: `{ type: "string?" }`. Block style doesn't need quoting.
