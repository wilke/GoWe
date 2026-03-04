# Planned Configuration Features

This document describes configuration features that are **not yet implemented** but are planned for future development. The YAML example files in `configs/` serve as a design reference for this work.

## Status: Not Implemented

None of the features below are currently functional. They represent the target configuration system design.

## Environment Variable Bindings for Flags

Currently, server and worker flags are **CLI-only** (using Go's `flag` package). The planned enhancement would allow each flag to also be set via an environment variable:

### Server

| Flag | Planned Env Var | Default |
|------|-----------------|---------|
| `--addr` | `GOWE_ADDR` | `:8080` |
| `--log-level` | `GOWE_LOG_LEVEL` | `info` |
| `--db` | `GOWE_DB_PATH` | `~/.gowe/gowe.db` |
| `--default-executor` | `GOWE_DEFAULT_EXECUTOR` | `""` |

### Worker

| Flag | Planned Env Var | Default |
|------|-----------------|---------|
| `--server` | `GOWE_SERVER_URL` | `http://localhost:8080` |
| `--name` | `GOWE_WORKER_NAME` | hostname |
| `--group` | `GOWE_WORKER_GROUP` | `default` |
| `--runtime` | `GOWE_WORKER_RUNTIME` | `none` |
| `--workdir` | `GOWE_WORKER_WORKDIR` | `$TMPDIR/gowe-worker` |
| `--stage-out` | `GOWE_STAGE_OUT` | `local` |
| `--poll` | `GOWE_WORKER_POLL` | `5s` |

## YAML Configuration File Loading

The `configs/` directory contains example YAML files that describe the target configuration format:

- `server.example.yaml` — full server configuration
- `worker.example.yaml` — full worker configuration with staging backends
- `credentials.example.yaml` — credential store for staging backends

### Planned Behavior

```bash
# Server loads full YAML config
./bin/server --config ~/.gowe/server.yaml

# Worker loads full YAML config
./bin/worker --config ~/.gowe/worker.yaml
```

### Current Behavior

- **Server** `--config` flag exists but only reads an admin user list (JSON format), not the full YAML schema
- **Worker** has no `--config` flag
- **Credentials** are loaded via `--http-credentials` flag in JSON format, not the YAML format shown in `credentials.example.yaml`

## Configuration Precedence

The planned precedence order (not yet implemented):

1. CLI flags (highest priority)
2. Environment variables
3. Config file values
4. Default values (lowest priority)

### Current Behavior

- CLI flags are the only configuration method for most settings
- A few settings fall back to environment variables when the flag is empty:
  - `DOCKER_HOST_PATH_MAP` (worker)
  - `INPUT_PATH_MAP` (worker)
  - `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (server and worker)
  - `SHOCK_TOKEN` (worker)
  - `GOWE_ADMINS` (server)
  - `BVBRC_TOKEN` (server, via token resolution chain)

## Environment Variable Interpolation in YAML

The planned system would support `${VAR_NAME}` syntax in YAML config files:

```yaml
s3:
  access_key_id: "${AWS_ACCESS_KEY_ID}"
  secret_access_key: "${AWS_SECRET_ACCESS_KEY}"
```

This is not implemented. Currently, environment variables must be read directly by the application code.

## System-Wide Configuration

Planned support for system-wide config at `/etc/gowe/`:

```
/etc/gowe/
├── server.yaml
└── worker.yaml
```

This is not implemented.

## Implementation Notes

To implement the full configuration system:

1. Add a config-loading library (e.g., `viper` or custom YAML loader) that merges flag → env → file → defaults
2. Wire `--config` on both server and worker to load the full YAML schema
3. Add `${VAR}` interpolation for YAML values
4. Convert credentials format from JSON to YAML (or support both)
5. Add `/etc/gowe/` as a fallback config directory
