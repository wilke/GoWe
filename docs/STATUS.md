# GoWe Project Status

Last updated: 2026-06-12

## Production Deployment

| Item | Value |
|------|-------|
| Host | `coconut` |
| Binary | `f216c5b` (dev tag `20260612-113835-ffc8f35`) |
| Branch | `feature/bvbrc-app-outputs` (30 commits ahead of main, 12 unpushed) |
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

**PR #115**: `feature/bvbrc-app-outputs` — 30 commits, not yet merged to main. Last 12 commits unpushed to origin (SSH key not in current session).

### Genome Analysis Pipeline (June 2026 work)

End-to-end CGA → CodonTree + cgMLST + WG-SNP pipeline. Verified working on H37Rv contigs (sub_3dbbe5c6, all 7 steps SUCCESS). Key files:
- `cwl/workflows/genome-analysis-pipeline.cwl` — scatter over contigs, parallel comparative analyses
- `cwl/tools/CodonTree.cwl` — replaces deprecated `PhylogeneticTree` (TheSEED/app_service stub; BV-BRC web UI maps `CodonTree → PhylogeneticTree`)
- `cwl/tools/bvbrc-wait-for-pgfams.cwl` — polls solr until freshly-annotated genome has PGFam-tagged CDS (gates CodonTree's `verify_genome_ids`)
- `cwl/tools/bvbrc-get-genome-id.cwl` — extracts genome_id from workspace autometadata
- `cwl/tools/bvbrc-create-genome-group.cwl` — creates genome group for cgMLST/SNP

### Scheduler/Executor Fixes (June 2026 work)

- **Token propagation (`f6deb8b`)**: `addUserToken` skipped embedding `sub.UserToken` whenever `--workspace-staging server` was on, unless the task had `inject_bvbrc_token`. BV-BRC executor tasks fell through and used the server's default token, making jobs run under the wrong identity. Fix: also embed when `task.ExecutorType == ExecutorTypeBVBRC`.
- **Scatter task inputs (`baa9903`)**: scatter dispatch only set `task.Job = combo`, leaving `task.Inputs` empty. BV-BRC executor reads from `Inputs`. Fix: set both in scatter and when-skipped paths.
- **`inject_bvbrc_token` hint**: new `gowe:Execution.inject_bvbrc_token: true` makes the worker inject `BVBRC_TOKEN` env var for worker-executed tools that call BV-BRC APIs (used by the 3 utility tools above).
- **Workflow naming (`baa9903`)**: packed `$graph` workflows got synthetic `id: "main"`. Fix: prefer `label:` field, then slugified `doc:` first line; always pass explicit `name` in registration JSON.

### Homology / blast-protein-search Fixes (June 2026 work)
- **`Homology.cwl`** (`884ceb4`, `03df2ec`): rewrote outputs to match actual `BV-BRC/homology_service` filenames (`blast_out.json`, `blast_out.raw.json`, `blast_out.txt`, `blast_headers.txt`, `blast_out.metadata.json`, `blast_out.archive`, plus `result_folder`). Added `blast_evalue_cutoff` (float), `blast_max_hits` (int), `blast_min_coverage` (int). Switched `output_path: Directory → string` to bypass `bundle.ResolveFilePaths` ws:// mangling. Verified end-to-end (`sub_03ea3d5b`).
- **`blast-protein-search.cwl`** (`ffc8f35`): replaces Clark's original pre-registered workflow which inlined a stale Homology with broken shell-style globs. Now a Workflow that references `gowe://Homology` so future Homology.cwl changes propagate automatically. Exposes all 7 typed outputs. Verified end-to-end (`sub_c9e5f137`).

### Validator Hardening (June 2026 work)
- **Reject shell-style `${...}` in outputBinding glob/outputEval** (`f216c5b`): `Validator.validateOutputBindings` walks every CommandLineTool output's glob (string or array) and outputEval expression for `${...}` substrings. CWL parameter references use `$(...)` syntax — `${...}` is shell-interpolation, passed through as literal at execution time, never matches a real file. Catches the exact bug pattern from Clark's blast-protein-search before the workflow is persisted. 4 new unit tests in `validator_test.go`. POST `/api/v1/workflows` now returns HTTP 400 with field-level error messages.

### 2-genome scatter pipeline (verified)
`sub_88b97faa`: H37Rv + CDC1551, 10 tasks total — all SUCCESS including final CodonTree (BV-BRC job 22519933). Required one zombie-task requeue mid-flight (see Known Issues / GH #118).

### Changes in PR #115 (earlier)

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

### BV-BRC Tools
- `ComprehensiveGenomeAnalysis`, `GenomeAssembly2`, `GenomeAnnotation`
- `Variation`, `RNASeq`, `MSA`, `MetagenomeBinning`
- `CodonTree` (replaces deprecated `PhylogeneticTree` stub)
- `CoreGenomeMLST`, `WholeGenomeSNPAnalysis`
- `Homology` (BLAST; `output_path: string`)

### Worker Utility Tools (with `inject_bvbrc_token: true`)
- `bvbrc-get-genome-id` — extract genome_id from workspace autometadata
- `bvbrc-wait-for-pgfams` — poll until PGFam features indexed in solr
- `bvbrc-create-genome-group` — programmatically create genome groups

### Pipelines
- `genome-analysis-pipeline` — CGA → CodonTree + cgMLST + WG-SNP (scatter-friendly)
- `protein-structure-prediction` — multi-tool structure prediction

### Protein Structure Tools
- `predict-structure` (worker, gpu: true)
- `protein-compare`, `select-structure`
- `boltz`, `chai`, `alphafold`, `esmfold`
- `predict-structure-app` (bvbrc executor variant)

## Known Issues

| Issue | Status | Notes |
|-------|--------|-------|
| #118 Zombie tasks after server restart | Open | Server restart leaves in-flight worker tasks RUNNING forever — worker reconnects but doesn't reclaim its prior `current_task`. Detection: `tasks.state='RUNNING'` AND owning worker's `current_task=''`. SQL workaround: `UPDATE tasks SET state='QUEUED', started_at=NULL, external_id='' WHERE id IN (...)`. Proper fix: scheduler reaper. See [[worker-task-zombies]] memory. |
| `bundle.ResolveFilePaths` ws:// mangling | Open | `internal/bundle/bundle.go:451` treats any non-http/https location as relative local path. `ws:///user@bvbrc/home` → `filepath.Join(jobDir, "ws:///...")` → `/tmp/ws:/user@bvbrc/home`, then `filepath.Base` extracts `"home"` as basename. Workaround: declare BV-BRC `output_path` as `string` not `Directory` (CGA/cgMLST/SNP/CodonTree still use Directory but their workflows wrap output_path as string). |
| Multiple registrations under same name | Open | `gowe submit --workflow <name>` resolves to one of N entries; explicit `name` doesn't enforce uniqueness. Affects e.g. `blast-protein-search` (Clark's original + later registrations). |
| `blast-protein-search` inlines broken Homology | Open | Pre-registered workflow inlines its own Homology CommandLineTool with the old broken `_output_globs`/Directory output_path. Re-registering the standalone `Homology` tool doesn't propagate. Owner should re-register with `gowe://Homology` reference. |
| `_preflight` echoed back to BV-BRC | Cosmetic | BV-BRC stderr warns about unspecified `_preflight` key. Non-fatal. Should be in `reservedKeys` in `bvbrc.go`. |
| #117 ws:// URI not recognized by CWL parser | Open | Documented, not fixed |
| #116 gowe:// in bundler (client-side) | Deferred | User declined fix for now |
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

Last run: 2026-04-28 on `609bfa3` (all pass). **Should be re-run on `03df2ec`** after the recent scheduler/Homology/CodonTree changes.

## To Resume After Reboot

1. **Start server**: `cd /scout/Experiments/GoWe && ./scripts/start-server.sh`
2. **Start extra workers**: Run the manual worker commands above
3. **Verify**: `curl -s http://localhost:8091/api/v1/health | python3 -m json.tool`
4. **Check orphaned tasks**: `sqlite3 /scout/wf/gowe/gowe.db "SELECT COUNT(*) FROM tasks WHERE state='RUNNING';"`
   - If tasks show RUNNING but no workers are executing them, requeue: `UPDATE tasks SET state='QUEUED', started_at=NULL WHERE state='RUNNING';`

## Build & Deploy

Go is **not natively installed** — build inside the apptainer golang container:

```bash
cd /scout/Experiments/GoWe
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "make dev"

# Update symlinks to the new versioned binaries:
DEV_TAG=$(ls -t bin/gowe-server-* | head -1 | sed 's|bin/gowe-server-||')
cd bin && for b in gowe gowe-server gowe-worker cwl-runner; do ln -sf "${b}-${DEV_TAG}" "$b"; done

# Restart server with the existing cmdline:
SERVER_PID=$(pgrep -f "gowe-server --addr :8091")
cat /proc/$SERVER_PID/cmdline | tr '\0' ' ' > /tmp/server_cmdline
kill -TERM $SERVER_PID && sleep 3
nohup $(cat /tmp/server_cmdline) > /scout/wf/gowe/server.log 2>&1 &
```

A project skill `/build` automates this — see `.claude/skills/build/SKILL.md`.

## Merge Plan

PR #115 should be merged to main. Run conformance tests first:
```bash
./scripts/run-conformance.sh
./scripts/run-conformance-server-local.sh -p 8095
./scripts/run-conformance-server-worker.sh -p 8097 -r none,apptainer
```
