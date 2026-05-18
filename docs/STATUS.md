# GoWe Project Status

Last updated: 2026-05-18

## Production Deployment

| Item | Value |
|------|-------|
| Host | `coconut` |
| Binary | `336de46` (2026-05-06) |
| Branch | `feature/bvbrc-app-outputs` (17 commits ahead of main) |
| Server | Port 8091, `--default-executor worker`, `--workspace-staging server` |
| Workers | 2 GPU (start script) + 2 GPU + 20 CPU (manual) = 24 total |
| GPUs | 4x H200 NVL assigned (IDs 1-4), GPU 0 reserved |
| URL | https://gowe.software-smithy.org |

### Start/Stop

```bash
cd /scout/Experiments/GoWe
./scripts/stop-server.sh
./scripts/start-server.sh   # starts server + 2 GPU workers

# Extra workers (must be started manually after script):
BASE_DIR=/scout/wf && LOG_DIR=$BASE_DIR/gowe/logs && PID_DIR=$BASE_DIR/gowe/pids
for i in 3 4; do
  ./bin/gowe-worker --server "http://localhost:8091" --runtime apptainer --name "worker-$i" \
    --workdir "$BASE_DIR/gowe/workdir/worker-$i" --stage-out "file://$BASE_DIR/data" \
    --poll 500ms --log-level info --gpu --gpu-id $i --image-dir /scout/containers \
    --pre-stage-dir /local_databases --extra-bind /scout/data \
    --secret-file "$BASE_DIR/gowe/secrets.env" --env-file "$BASE_DIR/gowe/worker-env.env" \
    --workspace-stager > "$LOG_DIR/worker-$i.log" 2>&1 &
  echo $! > "$PID_DIR/worker-$i.pid"
done
for i in $(seq 5 24); do
  ./bin/gowe-worker --server "http://localhost:8091" --runtime apptainer --name "worker-$i" \
    --workdir "$BASE_DIR/gowe/workdir/worker-$i" --stage-out "file://$BASE_DIR/data" \
    --poll 500ms --log-level info --image-dir /scout/containers \
    --pre-stage-dir /local_databases --extra-bind /scout/data \
    --secret-file "$BASE_DIR/gowe/secrets.env" --env-file "$BASE_DIR/gowe/worker-env.env" \
    --workspace-stager > "$LOG_DIR/worker-$i.log" 2>&1 &
  echo $! > "$PID_DIR/worker-$i.pid"
done
```

### Key Paths

| Path | Purpose |
|------|---------|
| `/scout/Experiments/GoWe/` | Source code + binaries |
| `/scout/Experiments/GoWe/bin/` | Versioned binaries (symlinked) |
| `/scout/containers/` | SIF container images |
| `/scout/wf/gowe/gowe.db` | SQLite database |
| `/scout/wf/gowe/logs/` | Server and worker logs |
| `/scout/wf/gowe/secrets.env` | HuggingFace tokens (mode 600) |
| `/scout/wf/gowe/worker-env.env` | Cache paths (HF_HOME, TORCH_HOME, etc.) |
| `/scout/wf/gowe/uploads/` | File upload storage |
| `/scout/wf/gowe/workdir/worker-N/` | Per-worker task working directories |
| `/scout/wf/data/` | Staged output files |
| `/local_databases/` | Pre-staged reference data (alphafold, boltz, chai) |
| `/local_databases/cache/` | HuggingFace/torch model cache |

## Open PR

**PR #115**: `feature/bvbrc-app-outputs` — 17 commits, not yet merged to main.

### Changes in PR #115

#### BV-BRC App Outputs (Phase 1-3)
- `cwl/app_outputs.yaml` — output catalog for 8 priority apps
- `cmd/gen-cwl-tools/main.go` — generator loads catalog, produces typed CWL outputs
- `cwl/tools/*.cwl` — regenerated with proper `outputBinding.glob` patterns
- `internal/executor/bvbrc.go` — executor parses `job_result.output_files`, maps to CWL output IDs

#### BV-BRC Executor Fixes
- File inputs resolved to path strings (not raw CWL objects)
- Null inputs stripped before `start_app`
- Logs fetched via `query_task_details` + `stderr_url`/`stdout_url` with OAuth auth
- Default executor is now fallback, not override (`--force-executor` for testing)

#### GPU Scheduling
- `RequiresGPU` field on RuntimeHints + StepHints
- `gowe:Execution.gpu: true` CWL hint parsed
- `CheckoutTask` filters by GPU capability
- GPU workers prefer GPU tasks (+100 score) over CPU tasks
- Composite index for priority-ordered checkout

#### Task Priority
- `priority` field on Task model (higher = checked out sooner)
- `PUT /api/v1/admin/tasks/{tid}/priority` admin endpoint
- `ORDER BY priority DESC, created_at` in checkout

#### Admin UI
- Active tasks page (`/admin/tasks`) — all PENDING/SCHEDULED/QUEUED/RUNNING tasks
- `GET /api/v1/admin/tasks/active` API endpoint
- Tasks tab in admin navigation

#### Workflow Labels
- `PATCH /api/v1/workflows/{id}/labels` — merge labels endpoint
- BV-BRC filter button in workflow list UI

#### UI Fixes
- Static task duration for completed tasks (was live clock)
- User token passed from UI submissions (was missing — broke workspace staging)
- `output_destination` form field wired through

#### Worker Config
- `--env-file` flag for non-secret container env vars (separate from `--secret-file`)
- `--workspace-upload` CLI flag for uploading inputs to BV-BRC workspace

## Registered Workflows

### Production Tools (with `executor:bvbrc` label)
- `GenomeAssembly2`, `GenomeAnnotation`, `ComprehensiveGenomeAnalysis`
- `Variation`, `RNASeq`, `MSA`, `CodonTree`, `MetagenomeBinning`

### Protein Structure Tools
- `predict-structure` (worker, gpu: true)
- `protein-compare`, `select-structure`
- `boltz`, `chai`, `alphafold`, `esmfold`
- `protein-structure-prediction` (workflow, uses gowe:// references)
- `predict-structure-app` (bvbrc executor variant)

## Known Issues

| Issue | Status | Notes |
|-------|--------|-------|
| #113 Worker cancellation | Open | Workers don't cancel running tasks when submission is cancelled |
| AlphaFold `--af2-data-dir` | Open | `predict-structure alphafold` needs to default data dir internally |
| `predict-structure-app` on BV-BRC | Open | `App-PredictStructure` not deployed on BV-BRC cluster yet |
| Anonymous shared visibility | Accepted | All anonymous users share submissions (session-scoped IDs deferred) |
| Token encryption at rest | Deferred | `user_token` plaintext in SQLite |

## Conformance Test Baselines

| Mode | Passed | Failed | Unsupported |
|------|--------|--------|-------------|
| cwl-runner | 378 | 0 | 0 |
| server-local | 376 | 0 | 2 |
| server-worker | 376 | 0 | 2 |

Last run: 2026-04-28 on `609bfa3` (all pass).

## To Resume After Reboot

1. **Start server**: `cd /scout/Experiments/GoWe && ./scripts/start-server.sh`
2. **Start extra workers**: Run the manual worker commands above
3. **Verify**: `curl -s http://localhost:8091/api/v1/health | python3 -m json.tool`
4. **Check orphaned tasks**: `sqlite3 /scout/wf/gowe/gowe.db "SELECT COUNT(*) FROM tasks WHERE state='RUNNING';"`
   - If tasks show RUNNING but no workers are executing them, requeue: `UPDATE tasks SET state='QUEUED', started_at=NULL WHERE state='RUNNING';`

## Build & Deploy

```bash
cd /scout/Experiments/GoWe
TAG="$(date +%Y%m%d-%H%M%S)-$(git rev-parse --short HEAD)"
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "\
  go build -o bin/gowe-server-${TAG} ./cmd/server && \
  go build -o bin/gowe-${TAG} ./cmd/cli && \
  go build -o bin/cwl-runner-${TAG} ./cmd/cwl-runner && \
  go build -ldflags \"-X main.Version=$(git rev-parse HEAD)\" -o bin/gowe-worker-${TAG} ./cmd/worker"
cd bin && for b in gowe gowe-server gowe-worker cwl-runner; do ln -sf "${b}-${TAG}" "$b"; done
# Then restart server + workers
```

## Merge Plan

PR #115 should be merged to main. Run conformance tests first:
```bash
./scripts/run-conformance.sh
./scripts/run-conformance-server-local.sh -p 8095
./scripts/run-conformance-server-worker.sh -p 8097 -r none,apptainer
```
