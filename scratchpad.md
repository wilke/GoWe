# GoWe Scratchpad

## Session: 2026-02-10

### Current State
- **Phase 1**: Types + Logging — DONE (committed: f674576)
- **Phase 2**: Skeleton Server — DONE (committed: 77bcc6b)
- **Phase 3**: CLI + Bundler — DONE (committed: 77bcc6b)
- **Phase 4**: CWL Parser + Validation — DONE (ready to commit)
- **Tag**: v0.3.0 (pending v0.4.0 after Phase 4 commit)
- Build: `go build ./...` clean
- Tests: `go test ./...` all pass (7 test packages)
- Vet: `go vet ./...` clean

### Phase 4 — What was built
- `pkg/cwl/workflow.go` — Typed CWL structs (Workflow, InputParam, OutputParam, Step, StepInput, GoWeHint)
- `pkg/cwl/tool.go` — CommandLineTool, ToolInputParam, ToolOutputParam, OutputBinding
- `pkg/cwl/graph.go` — GraphDocument (parsed $graph container)
- `internal/parser/parser.go` — Parser with ParseGraph(), ToModel(), shorthand normalization
- `internal/parser/parser_test.go` — 22 tests
- `internal/parser/dag.go` — BuildDAG() using Kahn's algorithm + cycle/self-loop detection
- `internal/parser/dag_test.go` — 8 tests
- `internal/parser/validator.go` — Validator with 9 validation checks
- `internal/parser/validator_test.go` — 16 tests
- `internal/server/server.go` — Added parser, validator, workflows map, sync.RWMutex
- `internal/server/handler_workflows.go` — Rewrote all 6 handlers with real parse/validate/store
- `internal/server/server_test.go` — Updated workflow tests with real packed CWL

### Key Decisions Made
- Outside-in build: skeleton API first, then fill in real implementations
- 7 dependencies total (chi, yaml.v3, sqlite, uuid, cobra, go-cmp, mcp-go)
- slog for logging, dependency-injected, never global
- Response envelope: {status, request_id, timestamp, data, pagination, error}
- ID prefixes: wf_, sub_, task_
- Credentials stored at ~/.gowe/credentials.json
- CWL shorthand normalization: parser detects string vs map and normalizes to typed structs
- In-memory workflow store with sync.RWMutex (temporary until Phase 5 SQLite)

### Next Steps
- Commit Phase 4 and tag v0.4.0
- Begin Phase 5: Store + Persistence (SQLite)
  - Replace in-memory map with SQLite-backed store
  - `internal/store/` package

### Reference
- Plan: `/Users/me/.claude/plans/resilient-floating-finch.md`
- Memory: `/Users/me/.claude/projects/-Users-me-Development-GoWe/memory/MEMORY.md`
- Docs: `docs/GoWe-Implementation-Plan.md`
