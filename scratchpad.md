# GoWe Scratchpad

## Session: 2026-02-11

### Current State
- **Phase 1**: Types + Logging — DONE (committed: f674576)
- **Phase 2**: Skeleton Server — DONE (committed: 77bcc6b)
- **Phase 3**: CLI + Bundler — DONE (committed: 77bcc6b)
- **Phase 4**: CWL Parser + Validation — DONE (committed: 655f912, v0.4.0)
- **Phase 5**: Store + Persistence (SQLite) — DONE (committed: 9108738, v0.5.0)
- **Phase 6**: Scheduler + LocalExecutor — DONE (committed: 2bdff9f, v0.6.0)
- **DockerExecutor**: DONE (committed: 5c99647)
- **Phase 7**: BVBRCExecutor — DONE (committed: ee14cc3, v0.7.0)
- **README.md**: Created (committed: 86a9439)
- Build: `go build ./...` clean
- Tests: `go test ./...` all 12 test packages pass (27 new tests in Phase 7)
- Vet: `go vet ./...` clean

### Phase 7 — What was built
- `internal/bvbrc/auth.go` — Token resolution (env → credentials.json → .bvbrc_token → .patric_token → .p3_token), parsing, expiry check
- `internal/bvbrc/auth_test.go` — 7 unit tests
- `internal/bvbrc/client.go` — RPCCaller interface, HTTPRPCCaller (JSON-RPC 1.1), RPCError type
- `internal/bvbrc/client_test.go` — 5 unit tests with httptest.Server
- `internal/executor/bvbrc.go` — BVBRCExecutor: Submit (start_app), Status (query_tasks), Cancel (kill_task), Logs (query_app_log)
- `internal/executor/bvbrc_test.go` — 15 unit tests with mock RPCCaller
- `internal/executor/bvbrc_integration_test.go` — 1 integration test (build-tag gated)
- `internal/scheduler/resolve.go` — Inject `_bvbrc_app_id` reserved key from step hints
- `internal/scheduler/resolve_test.go` — +2 tests for `_bvbrc_app_id` injection
- `cmd/server/main.go` — Conditional BVBRCExecutor registration (soft — server starts without token)

### Key Decisions Made (Phase 7)
- Async executor: Submit returns immediately with BV-BRC job UUID; scheduler polls via pollInFlight()
- RPCCaller interface abstracts JSON-RPC 1.1 for mock-based testing
- Soft registration: server starts fine without a BV-BRC token
- No new dependencies — uses only net/http and encoding/json
- Token resolution priority: BVBRC_TOKEN env → ~/.gowe/credentials.json → ~/.bvbrc_token → ~/.patric_token → ~/.p3_token
- BV-BRC state mapping: queued→QUEUED, in-progress→RUNNING, completed→SUCCESS, failed/deleted/suspended→FAILED

### GitHub Issues
- #1–#5: Phases 1–5 (CLOSED)
- #6: Phase 6 — Scheduler + LocalExecutor (CLOSED)
- #7: Phase 7 — BVBRCExecutor (CLOSED via ee14cc3)
- #8: Airflow provider (open, separate)
- #9: API verification — BV-BRC endpoints (open, PLANNED — see below)
- #10: Output registry (open)
- #11: Phase 8 — MCP Server + Tool Gen (open)

### Next Task: Issue #9 — BV-BRC API Verification (PLANNED)
Plan is at: `/Users/me/.claude/plans/tranquil-sparking-popcorn.md`

**What**: Issue #9 is about verifying `docs/BVBRC-API.md` against live BV-BRC endpoints. Three `[VERIFY]` tags in the doc mark uncertain sections.

**Approach**: Create `cmd/verify-bvbrc/main.go` — a one-shot CLI tool that:
1. Resolves a BV-BRC token (reuses `internal/bvbrc.ResolveToken`)
2. Checks service URL reachability (app_service, auth endpoint, workspace)
3. Calls AppService methods: `enumerate_apps`, `query_app_description`, `query_tasks`, `query_task_summary`
4. Calls Workspace.ls to verify response shape
5. Prints pass/fail report, exits 0/1
6. Skips `start_app` (would create a real job)

**Files to create/modify**:
- `cmd/verify-bvbrc/main.go` — Create: verification CLI tool
- `docs/BVBRC-API.md` — Modify: update `[VERIFY]` to `[VERIFIED <date>]` or `[FIXED]`

**Key details**:
- Workspace uses different URL: `https://p3.theseed.org/services/Workspace` (needs second RPCCaller)
- Auth endpoint check uses plain net/http (not RPC)
- No new dependencies
- Optional auth test if BVBRC_USERNAME/BVBRC_PASSWORD env vars are set

**Run**: `go run ./cmd/verify-bvbrc/` (requires BVBRC_TOKEN or ~/.bvbrc_token)

### Remaining Work After #9
- Phase 8: MCP Server + Tool Gen (#11) — final planned phase
- Output registry (#10)
- Airflow provider (#8, separate)

### Reference
- Phase 7 plan (overwritten with #9 plan): `/Users/me/.claude/plans/tranquil-sparking-popcorn.md`
- Memory: `/Users/me/.claude/projects/-Users-me-Development-GoWe/memory/MEMORY.md`
- Docs: `docs/GoWe-Implementation-Plan.md`, `docs/BVBRC-API.md`
- BV-BRC API `[VERIFY]` tags at lines 7, 74, 623 of `docs/BVBRC-API.md`
