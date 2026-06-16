# GoWe Project Status

Last updated: 2026-06-16

## Production Deployment

| Item | Value |
|------|-------|
| Host | `coconut` |
| Binary | `ba3bc5e` (dev tag `ba3bc5e-prod-20260616-131431`) — `main` tip |
| Branch | `main` (PR #115 and this session's PRs #119/#121/#122/#124/#125/#126 all merged) |
| Server | Port 8091, `--default-executor worker`, `--workspace-staging server` |
| Workers | 4 total: `cpu-worker-1`, `cpu-worker-2` (no GPU) + `worker-1` (GPU 1), `worker-2` (GPU 2) |
| GPUs | H200 NVL, IDs 1-2 used by `worker-1`/`worker-2`; GPU 0 reserved |
| URL | https://gowe.software-smithy.org |

> Redeployed 2026-06-16 from `ba3bc5e` (all 5 processes + 4 symlinks consistent). Rollback binary `gowe-server-772f1ab-prod-*` retained in `bin/`. Exact launch commands captured from `/proc` at deploy time (server flags above; workers use `--runtime apptainer --poll 500ms --image-dir /scout/containers --pre-stage-dir /local_databases --extra-bind /scout/data --secret-file ... --env-file ... --workspace-stager`, GPU workers add `--gpu --gpu-id N`).

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

## Session 2026-06-13 → 06-16: Issue Triage + ws:// Fix Chain

All work below is merged to `main` and deployed (`ba3bc5e`).

### Issue triage
Reviewed all 23 open issues against the codebase. **Closed 9 as fixed/superseded**: #10 (output registry → `cwl/app_outputs.yaml`), #17 (health executor map), #29 (CWL tool rework), #34 (`Logs()` → `query_task_details`), #39 (cwl-runner integration superseded by `cwltool`/`toolexec`), #52 (IWDR conformance), #57 (stager relocated to `pkg/staging`), #72 (server-local upload), #75 (scatter outdir collision). **Filed #123** (unify the byte-identical `cwltool.ResolveFileObject` / `cwlrunner.resolveFileObject`).

### Worker task-state binding — #118 + #113 (PR #119)
Both stem from a one-directional, fire-and-forget heartbeat. Made it **bidirectional and reconciling**: worker reports its in-flight task IDs; server returns `cancel_tasks`.
- **#118 zombie reaper**: `store.ReconcileWorkerTasks` requeues RUNNING worker-tasks the worker no longer reports, past a 30s grace window (catches the alive-but-forgetful worker after a server restart, which the heartbeat-timeout reaper missed).
- **#113 worker cancel**: per-task `context.WithCancel` map (`internal/worker/active_tasks.go`); the heartbeat loop kills the specific task's process within one beat. Reports `SKIPPED` on explicit cancel; leaves the task for requeue on worker shutdown. Result reporting detached from the per-task ctx so a killed task can still report.

### Validator regression — #120 (PR #121)
`f216c5b` rejected **any** `${...}` in outputBinding glob/outputEval, but a whole-string `${ ... }` block with a `return` is a valid CWL JS expression body. Regressed 7 conformance tests. Fix: `isJSExpressionBody` accepts a single brace-balanced `${...}` containing `return`; still rejects embedded shell-style `${var}` (the blast-workflow bug).

### ws:// URI resolution — #117 (PR #122) + follow-ups #124/#125/#126
`ws://`/`shock://` File/Directory locations were mangled by three resolvers (and more, found during real-submission verification):
- **PR #122 (#117)**: added `cwl.IsURI` (single source of truth via `ParseLocationScheme`); `cwltool.isURI`, `cwlrunner.isURI`, and `bundle.ResolveFilePaths` now preserve any non-file scheme. **Retires the `output_path: string` workaround** — `File`/`Directory` types work again.
- **PR #124**: `uploadDirectoryInput` preserves a remote-URI Directory's location instead of stripping it.
- **PR #125**: `resolveBVBRCInput` recurses into group/record params so File/Directory nested inside `[bvbrc:group]` (e.g. `paired_end_libs[].read1`) flatten to workspace path strings (previously sent as CWL objects → BV-BRC `File HASH(0x...) does not exist`).
- **PR #126**: fixed a #124 regression — `cwl.IsURI` is true for `file://` too, so local directories were wrongly skipped from upload (11 server-worker conformance failures); now checks the scheme explicitly.

**Verified against real BV-BRC**: `GenomeAssembly2` submission with ws:// reads + ws:// output_path reached `start_app` (authorized, preflight ran) — no path mangling; the nested `read1`/`read2` flattened to clean workspace paths. (Full success blocked only by the mock `handler_workspace.go` browser returning non-existent files.)

### Open PR
None — PR #115 (`feature/bvbrc-app-outputs`) merged (commit `11d918b`).

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
| #118 Zombie tasks after server restart | **Fixed** | PR #119 — `store.ReconcileWorkerTasks` heartbeat reaper (deployed `ba3bc5e`). |
| `bundle.ResolveFilePaths` ws:// mangling | **Fixed** | PR #122 (#117) — routes through `cwl.ParseLocationScheme`; preserves any non-file scheme. `output_path: string` workaround no longer required. |
| Nested group File/Directory not flattened for BV-BRC | **Fixed** | PR #125 — `resolveBVBRCInput` recurses into group/record params. |
| Multiple registrations under same name | Open | `gowe submit --workflow <name>` resolves to one of N entries; explicit `name` doesn't enforce uniqueness. Affects e.g. `blast-protein-search` (Clark's original + later registrations). |
| `blast-protein-search` inlines broken Homology | Open | Pre-registered workflow inlines its own Homology CommandLineTool with the old broken `_output_globs`/Directory output_path. Re-registering the standalone `Homology` tool doesn't propagate. Owner should re-register with `gowe://Homology` reference. |
| `_preflight` echoed back to BV-BRC | Cosmetic | BV-BRC stderr warns about unspecified `_preflight` key. Non-fatal. Should be in `reservedKeys` in `bvbrc.go`. |
| #117 ws:// URI not recognized | **Fixed** | PRs #122/#124/#125/#126 — full chain, verified against real BV-BRC. |
| #116 gowe:// in bundler (client-side) | Deferred | User declined fix for now |
| #113 Worker cancellation | **Fixed** | PR #119 — cancel via heartbeat `cancel_tasks`. |
| #123 Unify duplicated File resolver | Open | Refactor follow-up to #117: `cwltool.ResolveFileObject` == `cwlrunner.resolveFileObject` (+ `splitBasenameExt`). |
| `handler_workspace.go` returns mock data | Open | `/api/v1/workspace` serves hardcoded sample files; not wired to real `Workspace.ls`. Surfaced during ws:// real-submission verification. |
| Server pre-stages ws:// `output_path` | Minor | `--workspace-staging server` tries to download an output-destination Directory ("no download URL"); didn't block `start_app`. |
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

Last run: **2026-06-16 on `ba3bc5e`** — all three modes at baseline (378 / 376 / 376). The server-worker run during this session caught a #124 regression (365/11/2) which PR #126 fixed; re-run is clean.

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

## Next Up

- **#123** — unify the duplicated File resolver (`cwltool.ResolveFileObject` / `cwlrunner.resolveFileObject`). Self-contained issue with full detail for a fresh session.
- Wire `/api/v1/workspace` to the real `Workspace.ls` (currently mock — `handler_workspace.go`).
- Skip pre-staging ws:// `output_path` (output destinations shouldn't be downloaded as inputs).

Conformance gate before any merge (always run all three):
```bash
./scripts/run-conformance.sh
./scripts/run-conformance-server-local.sh -p 8095
./scripts/run-conformance-server-worker.sh -p 8097 -r none,apptainer
```
