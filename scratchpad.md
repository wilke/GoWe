# GoWe Scratchpad

## Session: 2026-02-10

### Current State
- **Phase 1**: Types + Logging — DONE (committed: f674576)
- **Phase 2**: Skeleton Server — DONE (committed: 77bcc6b)
- **Phase 3**: CLI + Bundler — DONE (committed: 77bcc6b)
- **Phase 4**: CWL Parser + Validation — DONE (committed: 655f912, v0.4.0)
- **Phase 5**: Store + Persistence (SQLite) — DONE (ready to commit)
- Build: `go build ./...` clean
- Tests: `go test ./...` all pass (9 test packages)
- Vet: `go vet ./...` clean

### Phase 5 — What was built
- `internal/config/config.go` — Added `DBPath` field to `ServerConfig`
- `internal/store/migrations.go` — Idempotent schema DDL (3 tables, 4 indexes)
- `internal/store/sqlite.go` — `SQLiteStore` implementing all 17 Store interface methods (WAL mode, JSON columns)
- `internal/store/sqlite_test.go` — 20 tests (CRUD, pagination, state queries, migration idempotency)
- `internal/server/server.go` — Replaced `workflows map + sync.RWMutex` with `store.Store`; `New()` now accepts store parameter
- `internal/server/handler_workflows.go` — All 6 handlers rewritten to use `s.store.*()` methods
- `internal/server/handler_submissions.go` — Replaced canned data with real store-backed CRUD (create with tasks, list, get, cancel)
- `internal/server/handler_tasks.go` — Replaced canned data with real store-backed CRUD (list by submission, get, get logs)
- `internal/server/server_test.go` — 34 tests (workflow CRUD + new submission/task tests with real data)
- `internal/cli/cli_test.go` — Updated all CLI tests to use in-memory SQLite store + real data
- `cmd/server/main.go` — Opens SQLite store at `~/.gowe/gowe.db` (or `--db` flag), runs migrations, passes to server

### Key Decisions Made
- Flat tables with JSON columns for inputs/outputs/steps (avoids complex JOINs, workflow is unit of consistency)
- RFC 3339 timestamps as TEXT (idiomatic for SQLite)
- WAL mode + foreign_keys ON
- `modernc.org/sqlite` — pure Go, no CGo
- `:memory:` DSN for testing (each test gets isolated store)
- GetWorkflow/GetSubmission/GetTask return nil (not error) for not-found
- GetSubmission auto-loads tasks via ListTasksBySubmission
- CreateSubmission handler creates tasks for each workflow step

### Next Steps
- Commit Phase 5 and tag v0.5.0
- Begin Phase 6: Scheduler + LocalExecutor

### Reference
- Plan: `/Users/me/.claude/plans/resilient-floating-finch.md`
- Memory: `/Users/me/.claude/projects/-Users-me-Development-GoWe/memory/MEMORY.md`
- Docs: `docs/GoWe-Implementation-Plan.md`
