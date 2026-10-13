# cwl-runner

Standalone CWL v1.2 runner (no server). Usage: `cwl-runner [flags] <cwl-file> [job-file]`.

This page documents the host-resource flags. For the rest (`--outdir`, `--runtime`, `--parallel`, `--jobs`,
`--image-dir`, `--preserve-environment`, ...) run `cwl-runner --help`.

## Host resources: bind mounts and environment

Apptainer tools run with `--containall --cleanenv`: a container sees only its task directory, its inputs, the image
and explicit bind mounts, and no host environment variables. To let a standalone run reach host model weights,
databases or run folders (for example boltz `--cache /local_databases/boltz`, or chai with
`CHAI_DOWNLOADS_DIR=/local_databases/chai`), cwl-runner accepts the same flags as `gowe-worker`, with the same
parsing (shared code in `internal/hostres`).

| Flag | Container tools (Apptainer and Docker) | Host tools (`--runtime none` / `--no-container`) |
|------|----------------------------------------|---------------------------------------------------|
| `--pre-stage-dir DIR` | Bind-mount `DIR` (read-write) at the same path | No-op: the tool sees the host filesystem |
| `--dataset id=path` | Bind-mount `path` at the same path (repeatable, or comma-separated) | No-op |
| `--extra-bind PATH` | Bind-mount `PATH` at the same path (repeatable, or comma-separated) | No-op |
| `--env NAME=value` | Set `NAME=value` in the container (repeatable) | Set `NAME=value` in the tool environment |
| `--env-file FILE` | As `--env`, one `NAME=value` per line, `#` comments and blank lines ignored | Same |

Notes:

- `--dataset` differs slightly from the worker: the worker registers the id for scheduler affinity only (the bind
  comes from `--pre-stage-dir`/`--extra-bind`). A standalone run has no scheduler, so cwl-runner also bind-mounts the
  `--dataset` path (skipped when already under another bind) and the id is informational.
- `--pre-stage-dir` binds the whole directory; its subdirectories are the dataset ids, as for the worker.
- Binds are applied to every tool of the run: single tools, workflow steps, scatter, sub-workflows and `--parallel`.
- Env precedence is the same as the worker: `--env-file` is loaded first and `--env` overrides entries of the same
  name. The result is applied after the tool's `EnvVarRequirement`, so `--env`/`--env-file` win over a same-named
  `EnvVarRequirement` entry. `HOME` and `TMPDIR` are refused (the CWL spec fixes them). Values are never logged
  (names only, at debug level).
- `--env` is not `--preserve-environment NAME`: that copies a variable from the host environment to host-mode tools
  only (it does not reach containers); `--env` sets an explicit value for all tools.

## Example: boltz / chai from a worker's setup

```bash
cwl-runner --runtime apptainer --image-dir /scout/containers \
  --pre-stage-dir /local_databases --env-file worker-env.env tool.cwl job.yml
```

with `worker-env.env`:

```
# model locations and tokens, as passed to gowe-worker --env-file
CHAI_DOWNLOADS_DIR=/local_databases/chai
HF_TOKEN=hf_abc123
```

Single paths: `--extra-bind /data/run42 --env FOO=bar`.
