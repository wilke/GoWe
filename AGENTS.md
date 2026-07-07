# AGENTS.md

Essential context for OpenCode agents working in this repository.

## Build & Test

**Go 1.24+** required (see `go.mod`). Single working directory; no monorepo.

```bash
# All binaries at once (standard workflow)
make build

# Individual binaries (with version stamping)
go build -o bin/gowe-server ./cmd/server
go build -ldflags "-X main.Version=$(git rev-parse HEAD)" -o bin/gowe-worker ./cmd/worker
go build -o bin/gowe ./cmd/cli
go build -o bin/cwl-runner ./cmd/cwl-runner

# Tests
make test              # Go unit tests only
make test-tier1        # Tier 1: cwl-runner + unit (fast CI path)
make test-all          # All execution modes (requires Docker)
make test-conformance  # 378 CWL v1.2 conformance tests (requires bin/cwl-runner)
```

**Important:** Worker version uses `git rev-parse HEAD`; must be a valid Git repository for reproducible builds. Unit tests run without Docker or integration tags; see `CLAUDE.md` for Docker/BV-BRC integration tests.

## Command → Binary Mapping

The Makefile's `bin_name` function (line 25) maps:
- `cli` → `gowe`
- `server` → `gowe-server`
- `worker` → `gowe-worker`
- Others unchanged

When reviewing or fixing build output, expect these names in `./bin/`.

## Code Organization

**Entry points:**
- `cmd/server/main.go` — HTTP API + scheduler + web UI
- `cmd/worker/main.go` — Task polling + execution
- `cmd/cli/main.go` — Client CLI
- `cmd/cwl-runner/main.go` — Standalone CWL runner (no server needed)

**Critical internals:**
- `internal/scheduler/` — Tick-based scheduling: (1) WAITING→READY, (1.5) pre-stage, (2) dispatch, (2.5) retry, (3) poll, (3.5) stuck detection, (4) advance steps, (5) finalize submissions
- `internal/executor/` — Registry: local, docker, apptainer, worker, bvbrc. Selection priority: (1) ForceExecutor flag (if set), (2) `gowe:Execution.executor` hint, (3) auto-promote DockerRequirement to worker (if workers online), (4) `--default-executor` flag, (5) default local
- `internal/parser/` — CWL v1.2 parsing, DAG construction, hint extraction
- `internal/store/` — SQLite with `modernc.org/sqlite` (pure Go, no CGO). WAL mode, `max_open_conns=1`. Migrations in `migrations.go`
- `internal/server/` — go-chi routing, middleware, auth (BV-BRC, MG-RAST, anonymous)
- `internal/worker/` — Pull-based task checkout + execution + output staging
- `internal/cwltool/` — Full CWL CommandLineTool executor (bindings, globbing, IWDR, JS eval)
- `pkg/model/` — Domain entities with three-level state hierarchy

**State hierarchy (critical for understanding execution):**
```
Submission (PENDING → RUNNING → COMPLETED/FAILED/CANCELLED)
  └─ StepInstance (WAITING → READY → DISPATCHED → RUNNING → COMPLETED/FAILED/SKIPPED)
       └─ Task (PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS/FAILED)
```

## CWL Extensions (GoWe-specific)

Safely ignored by other engines. Namespace: `gowe: https://github.com/wilke/GoWe#`

```yaml
hints:
  gowe:Execution:
    executor: worker|bvbrc|local        # Explicit executor routing
    worker_group: gpu-workers            # Target worker group
    bvbrc_app_id: GenomeAnnotation      # BV-BRC app link
    docker_image: override.sif           # Override DockerRequirement

  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        mode: prestage|cache             # prestage = require, cache = prefer
```

## Worker Flags Worth Knowing

- `--group` — Worker group for scheduling (default: `default`)
- `--runtime` — `docker`, `apptainer`, or `none` (default: none)
- `--image-dir` — Resolve relative `.sif` paths against this directory
- `--pre-stage-dir` — Auto-scan subdirectories as datasets, bind-mount into containers
- `--dataset id=path` — Explicit dataset alias (repeatable)
- `--extra-bind /path` — Generic bind mount (repeatable, not used for scheduling)
- `--secret NAME=value` — Secret env var (never sent to server)
- `--gpu` / `--gpu-id` — GPU passthrough (apptainer/docker only)

## Database Quirks

- SQLite only; embedded, zero external dependencies
- Pure Go driver (`modernc.org/sqlite`), no CGO
- WAL mode, single writer (`max_open_conns=1`)
- Idempotent migrations in `internal/store/migrations.go` use `addColumnIfNotExists()`
- For testing: use `--db :memory:` or temporary paths; do **not** commit `.db` files

## API Format

All endpoints under `/api/v1`. Standard envelope:
```json
{
  "status": "success|error",
  "request_id": "...",
  "timestamp": "...",
  "data": { ... }
}
```

## Logging

`internal/logging/` provides slog-based logger. Use appropriate levels (debug, info, warn, error). Server flags `--debug` → `debug` level, `--log-level` for fine control, `--log-format` for `text` or `json`.

## Releases & Commits

**Conventional Commits required** (`feat:`, `fix:`, `docs:`, `refactor:`, etc.). Type drives release-please automation (semver + changelog).

**Branches:** Trunk-based (GitHub Flow), `main` only. No `develop`. PR squash-merged; branch deleted. See `CONTRIBUTING.md` for full workflow.

**Releases:** Automated via release-please + GoReleaser. Do **not** tag manually. GoReleaser config: `.goreleaser.yaml`. Outputs: `gowe`, `gowe-server`, `gowe-worker`, `cwl-runner` for linux/darwin × amd64/arm64.

## Common Mistakes

1. **Forgetting to build before test:** `make test` uses Go's native test discovery; binaries are only needed for conformance tests. But `make test-tier1` requires `bin/cwl-runner` and `bin/gowe`.
2. **Modifying `go.sum`:** Never edit by hand. Run `go mod tidy` or let `go get` manage it.
3. **Breaking migrations:** Migrations are appended; never edit existing ones. Add new ones with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`.
4. **Docker volume path mapping:** Workers with `DOCKER_VOLUME=gowe-workdir` (named volume) handle path translation automatically. Legacy `DOCKER_HOST_PATH_MAP` requires explicit mapping.
5. **Worker authentication:** Use `--worker-key` (flag) + worker keys JSON (server config); secrets via `--secret` or `--secret-file` are never sent to server.
6. **Git commit hash in worker version:** Requires valid Git repo. Use `VERSION=$(git rev-parse HEAD)` in ldflags during build.

## See Also

- `CLAUDE.md` — Detailed architecture, packages, and conventions for Claude Code
- `CONTRIBUTING.md` — PR/release workflow
- `SPECIFICATION.md` — Normative CWL interpretation
- `docs/` — Guides, tutorials, ADRs, API reference
