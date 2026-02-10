# GoWe Scratchpad

## Session: 2026-02-10

### Current State
- **Phase 1**: Types + Logging — DONE (committed: f674576)
- **Phase 2**: Skeleton Server — DONE (uncommitted)
- **Phase 3**: CLI + Bundler — DONE (uncommitted)
- **Phase 4**: CWL Parser — not started
- Build: `go build ./...` clean
- Tests: `go test ./...` all pass

### Uncommitted Work
Phases 2-3 are fully built but not yet committed:
- `internal/config/` — server config loading
- `internal/server/` — all handlers (canned JSON), middleware, response envelope, router
- `internal/cli/` — cobra commands (submit, status, list, cancel, logs, apps)
- `internal/bundle/` — CWL $graph bundler (resolves run: refs)
- `pkg/cwl/` — CWL types for bundling
- `cmd/server/main.go` — updated server entry point
- `cmd/cli/main.go` — updated CLI entry point
- `testdata/` — sample CWL fixtures

### Key Decisions Made
- Outside-in build: skeleton API first, then fill in real implementations
- 7 dependencies total (chi, yaml.v3, sqlite, uuid, cobra, go-cmp, mcp-go)
- slog for logging, dependency-injected, never global
- Response envelope: {status, request_id, timestamp, data, pagination, error}
- ID prefixes: wf_, sub_, task_

### Next Steps
- Commit Phases 2-3
- Begin Phase 4: CWL Parser (replace skeleton workflow handlers with real parsing)
- Or address any remaining Phase 2-3 issues first

### Reference
- Plan: `/Users/me/.claude/plans/cryptic-bubbling-nest.md`
- Memory: `/Users/me/.claude/projects/-Users-me-Development-GoWe/memory/MEMORY.md`
- Docs: `docs/GoWe-Implementation-Plan.md`
