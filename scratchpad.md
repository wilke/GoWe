# GoWe Scratchpad

## Session: 2026-02-10

### Current State
- **Phase 1**: Types + Logging — DONE (committed: f674576)
- **Phase 2**: Skeleton Server — DONE (committed: 77bcc6b)
- **Phase 3**: CLI + Bundler — DONE (committed: 77bcc6b)
- **Phase 4**: CWL Parser + Validation — DONE (committed: 655f912, v0.4.0)
- **Phase 5**: Store + Persistence (SQLite) — DONE (committed: 9108738, v0.5.0)
- **Phase 6**: Scheduler + LocalExecutor — DONE (committed: 2bdff9f, v0.6.0)
- Build: `go build ./...` clean
- Tests: `go test ./...` all pass (11 test packages)
- Vet: `go vet ./...` clean

### Phase 6 — What was built
- `internal/executor/registry.go` — Type→Executor lookup map
- `internal/executor/local.go` — LocalExecutor (os/exec), synchronous Submit
- `internal/executor/local_test.go` — 5 tests (echo, fail, missing cmd, glob, cancel)
- `internal/scheduler/resolve.go` — Input resolution + dependency checking (pure functions)
- `internal/scheduler/resolve_test.go` — 12 tests (workflow input, upstream output, missing, deps)
- `internal/scheduler/loop.go` — Scheduler loop (Start/Stop/Tick) with 5-phase tick algorithm
- `internal/scheduler/loop_test.go` — 8 tests (single task, pipeline, failed dep, retry, start/stop)
- `internal/scheduler/integration_test.go` — 2-step pipeline E2E test
- `pkg/model/workflow.go` — ToolInline JSON persistence fix (`json:"-"` → `json:"tool_inline,omitempty"`)
- `internal/server/server.go` — Added scheduler field + StartScheduler() method
- `internal/server/server_test.go` — Updated New() calls with nil scheduler
- `internal/cli/cli_test.go` — Updated New() calls with nil scheduler
- `cmd/server/main.go` — Wired executor registry, LocalExecutor, scheduler, graceful shutdown

### Current Work: DockerExecutor
- **Plan**: `/Users/me/.claude/plans/tranquil-sparking-popcorn.md`
- **Approach**: Docker CLI via os/exec (not SDK), synchronous, CommandRunner interface for testability
- **Key design**: `_docker_image` reserved key in task.Inputs, parsed from CWL DockerRequirement.dockerPull or goweHint.docker_image
- **Files to create**: `internal/executor/docker.go`, `docker_test.go`, `docker_integration_test.go`, `Dockerfile`
- **Files to modify**: `pkg/model/workflow.go`, `internal/parser/parser.go`, `internal/scheduler/resolve.go`, `cmd/server/main.go`

### Key Decisions Made (Phase 6)
- Scheduler embedded in server process (not separate binary)
- Tick-based polling with configurable interval (default 2s)
- LocalExecutor is synchronous (Submit blocks until process finishes)
- Reserved input keys: `_base_command`, `_output_globs` (and upcoming `_docker_image`)
- `ExecutorTypeContainer = "container"` already exists in model for DockerExecutor

### Reference
- Phase 6 plan: `/Users/me/.claude/plans/resilient-floating-finch.md`
- DockerExecutor plan: `/Users/me/.claude/plans/tranquil-sparking-popcorn.md`
- Memory: `/Users/me/.claude/projects/-Users-me-Development-GoWe/memory/MEMORY.md`
- Docs: `docs/GoWe-Implementation-Plan.md`
