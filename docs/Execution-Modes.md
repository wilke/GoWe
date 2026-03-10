# GoWe Execution Modes and Storage Backends

This document describes the available execution modes and storage backends in GoWe, along with test coverage status.

## Overview

GoWe supports multiple execution modes (compute) and storage backends (data). The `scripts/run-all-tests.sh` script runs CWL conformance tests across the primary modes.

## Execution Modes (Compute)

| Executor | Tested | Script | Description |
|----------|--------|--------|-------------|
| **local** | ✅ | `run-conformance-server-local.sh` | Direct process execution on host |
| **docker** | ✅ | `run-conformance.sh`* | Docker containers (auto-detected) |
| **worker** | ✅ | `run-conformance-distributed.sh` | Remote workers via docker-compose |
| **worker (apptainer)** | ✅ | `run-conformance-distributed-apptainer.sh` | Native workers with Apptainer runtime |
| **apptainer** | ✅ | `run-conformance.sh`* | Apptainer/Singularity containers (auto-detected) |
| **bvbrc** | ❌ | - | BV-BRC remote execution |
| **container** | ❌ | - | Generic container executor |

*`cwl-runner` auto-detects Docker/Apptainer when `DockerRequirement` is present.

### Executor Descriptions

- **local** (`internal/executor/local.go`): Executes commands directly on the host. Uses `execution.Engine` for full CWL support including command line building, output collection, and exit code handling.

- **docker** (`internal/executor/docker.go`): Executes commands in Docker containers. Supports volume mounts, GPU passthrough, and resource limits.

- **worker** (`internal/executor/worker.go`): Dispatches tasks to remote workers. Workers poll the server for tasks and execute them locally or in containers.

- **apptainer** (`internal/executor/apptainer.go`): Executes commands in Apptainer (formerly Singularity) containers. Useful for HPC environments where Docker is not available.

- **bvbrc** (`internal/executor/bvbrc.go`): Submits jobs to BV-BRC (Bacterial and Viral Bioinformatics Resource Center) for remote execution.

- **container** (`internal/executor/executor.go`): Generic container executor interface.

## Storage Backends (Data)

| Backend | Tested | Description |
|---------|--------|-------------|
| **local** | ✅ | In-place, no copy (cwl-runner default) |
| **file://** | ✅ | Shared filesystem (NFS, bind mounts, etc.) |
| **http(s)://** | ❌ | HTTP PUT/POST upload to any HTTP server |
| **shock://** | ❌ | Shock data service (BV-BRC integration) |
| **s3://** | ❌ | S3-compatible object storage |
| **ws://** | ❌ | Workspace service (BV-BRC integration) |

### Storage Backend Descriptions

- **local**: Files remain in place. Used by `cwl-runner` for standalone execution.

- **file://**: Copies outputs to a shared filesystem path. Workers use this for distributed execution with a shared volume.

- **http(s)://**: Uploads outputs via HTTP PUT/POST. Configurable with custom headers, authentication, and retry logic.

- **shock://**: Uploads to Shock data service. Used with BV-BRC for large file storage.

- **ws://**: Uploads to BV-BRC Workspace service. This is the default storage backend for BV-BRC execution.

- **s3://**: Uploads to S3-compatible object storage (planned).

## Test Matrix

```
                    │ local │ file:// │ http:// │ shock:// │ ws:// │ s3:// │
────────────────────┼───────┼─────────┼─────────┼──────────┼───────┼───────┤
cwl-runner          │  ✅   │    -    │    -    │    -     │   -   │   -   │
server-local        │  ✅   │    -    │    -    │    -     │   -   │   -   │
server-distributed  │   -   │   ✅    │   ❌    │   ❌     │   -   │  ❌   │
server-docker       │  ❌   │   ❌    │   ❌    │   ❌     │   -   │  ❌   │
server-apptainer    │  ❌   │   ✅    │   ❌    │   ❌     │   -   │  ❌   │
server-bvbrc        │   -   │    -    │    -    │   ❌     │  ❌   │   -   │
```

Legend:
- ✅ Tested in conformance suite
- ❌ Not tested (supported but no automated tests)
- `-` Not applicable for this combination

## Running Conformance Tests

### All Modes

```bash
./scripts/run-all-tests.sh                    # Run all modes
./scripts/run-all-tests.sh required           # Run required tests only
./scripts/run-all-tests.sh -m cwl-runner      # Run only cwl-runner mode
./scripts/run-all-tests.sh -s distributed     # Skip distributed mode
```

### Individual Modes

```bash
# Mode 1: cwl-runner (standalone CLI)
./scripts/run-conformance.sh

# Mode 2: Server with local execution
./scripts/run-conformance-server-local.sh

# Mode 3: Distributed workers (docker-compose)
./scripts/run-conformance-distributed.sh

# Mode 4: Distributed workers with Apptainer (native processes, no Docker)
./scripts/run-conformance-distributed-apptainer.sh
```

## Test Results

As of 2026-03-09:

| Mode | Passing | Total | Percentage | Notes |
|------|---------|-------|------------|-------|
| cwl-runner (Docker) | 378 | 378 | 100% | ✓ |
| cwl-runner (Apptainer) | 377 | 378 | 99.7% | 1 known limitation (see below) |
| cwl-runner-parallel | 378 | 378 | 100% | ✓ |
| distributed-none | 376 | 378 | 99.5% | 2 known failures |
| distributed-docker | 376 | 378 | 99.5% | 2 known failures |
| distributed-apptainer | 375 | 378 | 99.2% | 3 known failures (see below) |

### Apptainer Known Limitation

Test 227 (`networkaccess_disabled`) fails on Apptainer. The CWL spec requires network isolation by default (no `NetworkAccess` requirement = no network), which Docker enforces via `--network none`. Apptainer shares the host network by default and `--net --network none` requires root or admin configuration (`allow net users`/`allow net networks` in `apptainer.conf`). This cannot be enforced for unprivileged users on HPC systems.

### Distributed-Apptainer Known Failures

The distributed-apptainer mode (375/378) has 3 failures:
- Test 227 (`networkaccess_disabled`): Apptainer network isolation limitation (see above)
- Tests 237, 238 (`inp_update_wf`, `inpdir_update_wf`): Known distributed-mode failures, also present in distributed-none and distributed-docker

### Apptainer Compatibility Fixes

The following adaptations were made for Apptainer compatibility:

| Issue | Docker Behavior | Apptainer Fix |
|-------|----------------|---------------|
| HOME override | `--env HOME=...` | `--home src:dest` (Apptainer blocks HOME via `--env`) |
| ENTRYPOINT | Always honored | Use `apptainer run` when command starts with a flag (e.g. `-c`) |
| Colons in paths | `--bind src:dest` works | `--mount type=bind,source=...,destination=...` (colons in paths break `--bind`) |
| Network isolation | `--network none` | Not enforceable without root (known limitation) |

## Adding New Test Coverage

To add conformance testing for a new mode:

1. Create a script in `scripts/` following the pattern of existing scripts
2. Add the mode to `run-all-tests.sh`
3. Update this document with the new mode

### Example: Adding server-docker mode

```bash
# scripts/run-conformance-server-docker.sh
./bin/server -default-executor docker ...
```

Then add to `run-all-tests.sh`:
```bash
run_server_docker() {
    log_header "Mode: Server with Docker Execution"
    "$SCRIPT_DIR/run-conformance-server-docker.sh" "$TAGS"
}
```
