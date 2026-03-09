# GoWe Test Scripts

This directory contains the test infrastructure for GoWe, providing comprehensive testing across all execution modes with cwl-runner as the gold standard baseline.

## Quick Start

```bash
# First time setup - initialize environment
./scripts/setup-env.sh -b

# Source environment variables
source .env

# Run all tests (378 conformance tests per mode)
./scripts/run-all-tests.sh

# Run required tests only (84 tests, faster for CI)
./scripts/run-all-tests.sh --required

# Run only Tier 1 tests (CI fast path)
./scripts/run-all-tests.sh -t 1

# Run unit tests only
./scripts/run-all-tests.sh unit

# Run staging backend tests
./scripts/run-staging-tests.sh
```

## Environment Setup

Before running tests, set up your environment using the setup script:

```bash
# Basic setup (creates .env with auto-detected paths)
./scripts/setup-env.sh

# Setup with build
./scripts/setup-env.sh -b

# Setup, build, and run validation tests
./scripts/setup-env.sh -b -t

# Force regenerate .env (overwrites existing)
./scripts/setup-env.sh -f
```

The setup script:
1. Creates `~/.gowe/` config directory
2. Generates `.env` file with auto-detected project paths
3. Creates working directories (local `tmp/` for testing; docker-compose uses named volume `gowe-workdir`)
4. Validates prerequisites (Go, Docker, cwltest)
5. Clones CWL conformance tests if needed
6. Optionally builds binaries and runs tests

### Environment Variables

Key variables set in `.env`:

| Variable | Description |
|----------|-------------|
| `GOWE_PROJECT_ROOT` | Project root directory |
| `GOWE_TESTDATA` | Test data directory |
| `GOWE_CONFORMANCE_DIR` | CWL v1.2 conformance tests |
| `GOWE_WORKDIR` | Shared working directory |
| `GOWE_TEST_SERVER_LOCAL_PORT` | Port for server-local tests (default: 8091) |
| `GOWE_TEST_DISTRIBUTED_PORT` | Port for distributed tests (default: 8090) |
| `DOCKER_HOST_PATH_MAP` | Path mapping for DinD (legacy — prefer `DOCKER_VOLUME`) |
| `DOCKER_VOLUME` | Named Docker volume for DinD tool containers (preferred) |

### Configuration Templates

Example configuration files are in the `configs/` directory:

```
configs/
├── server.example.yaml       # Server configuration
├── worker.example.yaml       # Worker configuration
├── credentials.example.yaml  # Staging backend credentials
└── README.md                 # Configuration documentation
```

Copy these to `~/.gowe/` and customize for your environment.

## Test Architecture

### Execution Modes

GoWe supports multiple execution modes, each tested for CWL v1.2 conformance:

```
                    ┌─────────────┐
                    │  cwl-runner │  Direct CLI execution (gold standard)
                    └─────────────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
         single       --parallel    (no server)

                    ┌─────────────┐
                    │   Server    │  HTTP API + Schedulers
                    └─────────────┘
                           │
              ┌────────────┴────────────┐
              ▼                         ▼
       server-local              distributed
    (LocalExecutor)           (Server + Workers)
                                       │
                    ┌──────────────────┼──────────────────┐
                    ▼                  ▼                  ▼
            distributed-docker  distributed-apptainer  distributed-none
```

| Mode | Description | Container Runtime |
|------|-------------|-------------------|
| `cwl-runner` | Direct CLI execution | Docker/Apptainer |
| `cwl-runner --parallel` | Parallel step execution | Docker/Apptainer |
| `server-local` | Server with LocalExecutor | Docker/Apptainer |
| `distributed-none` | Server + Workers | Host $PATH (no containers) |
| `distributed-docker` | Server + Workers | Docker-in-Docker |
| `distributed-apptainer` | Server + Workers | Apptainer |

### Test Tiers

Tests are organized into three tiers based on criticality:

#### Tier 1: Core Execution (Must Always Pass)

These tests verify the gold standard execution path and must pass for any PR to be merged.

| Test | Description | Expected |
|------|-------------|----------|
| `unit` | Go unit tests (`go test ./...`) | 100% pass |
| `cwl-runner` | Direct CLI conformance | 378/378 |
| `cwl-runner-parallel` | Parallel execution conformance | 378/378 |

#### Tier 2: Server Modes (Production Target)

These tests verify server-based execution modes. Progress is tracked toward 100% conformance.

| Test | Description | Current | Target |
|------|-------------|---------|--------|
| `server-local` | Server + LocalExecutor | 250/378 | 378/378 |
| `distributed-none` | Workers with host tools | 376/378 | 378/378 |
| `distributed-docker` | Workers with Docker | 376/378 | 378/378 |
| `distributed-apptainer` | Workers with Apptainer | TBD | 378/378 |

#### Tier 3: Staging Backends (Feature Tests)

These tests verify file staging backends used for distributed execution.

| Test | Description | Requires |
|------|-------------|----------|
| `staging-file` | Local filesystem (file://) | - |
| `staging-shared` | SharedFS (symlinks) | - |
| `staging-s3` | S3/MinIO | Docker |
| `staging-shock` | Shock (BV-BRC) | Docker |

## Scripts

### `setup-env.sh`

Initialize the development/testing environment.

```bash
Usage:
  ./scripts/setup-env.sh [options]

Options:
  -b, --build       Build all binaries after setup
  -f, --force       Overwrite existing .env file
  -t, --test        Run quick validation tests after setup
  -q, --quiet       Minimal output
  -h, --help        Show this help message
```

**Examples:**

```bash
# Basic setup
./scripts/setup-env.sh

# Setup and build binaries
./scripts/setup-env.sh -b

# Full setup with build and validation
./scripts/setup-env.sh -b -t

# Regenerate .env file
./scripts/setup-env.sh -f
```

### `run-all-tests.sh`

The main test runner that orchestrates all test types.

```bash
Usage:
  ./scripts/run-all-tests.sh [options] [test-type]

Test Types:
  all         Run all tests (default)
  conformance CWL conformance tests only
  unit        Go unit tests only
  staging     Staging backend tests only

Options:
  -m, --mode MODE     Run only specified execution mode
  -s, --skip MODE     Skip specified mode (can be used multiple times)
  -t, --tier N        Run only tier N tests (1, 2, or 3)
  --required          Run only required tests (84 tests, faster for CI)
  --no-docker         Skip tests requiring Docker
  --parallel          Use --parallel flag for cwl-runner
  -v, --verbose       Verbose output
  -r, --report        Generate markdown report
  -h, --help          Show this help message
```

**Examples:**

```bash
# Run all 378 conformance tests (default)
./scripts/run-all-tests.sh

# Run required tests only (84 tests, faster for CI)
./scripts/run-all-tests.sh --required

# Run only cwl-runner tests
./scripts/run-all-tests.sh -m cwl-runner

# Run Tier 1 tests only (fast CI)
./scripts/run-all-tests.sh -t 1

# Skip distributed tests
./scripts/run-all-tests.sh -s distributed-docker -s distributed-none

# Run without Docker dependencies
./scripts/run-all-tests.sh --no-docker

# Generate a markdown report
./scripts/run-all-tests.sh -r
```

### `run-staging-tests.sh`

Dedicated runner for staging backend tests.

```bash
Usage:
  ./scripts/run-staging-tests.sh [options] [backend...]

Backends:
  file      Local filesystem staging (no Docker required)
  shared    SharedFS staging (symlink mode, no Docker required)
  s3        S3/MinIO staging (requires Docker)
  shock     Shock staging (requires Docker)
  all       Run all backends (default)

Options:
  -u, --unit-only      Run only unit tests (no Docker)
  -i, --integration    Run integration tests (requires Docker)
  -k, --keep           Keep Docker containers running after tests
  -v, --verbose        Verbose output
  -h, --help           Show this help message
```

**Examples:**

```bash
# Run all staging tests
./scripts/run-staging-tests.sh

# Test only file and shared backends (no Docker)
./scripts/run-staging-tests.sh file shared

# Run S3 integration tests
./scripts/run-staging-tests.sh s3 -i

# Unit tests only
./scripts/run-staging-tests.sh -u
```

### `run-conformance.sh`

Run CWL conformance tests against cwl-runner directly.

```bash
./scripts/run-conformance.sh [tags]

# Examples
./scripts/run-conformance.sh              # Run required tests
./scripts/run-conformance.sh required     # Same as above
./scripts/run-conformance.sh workflow     # Run workflow tests only
```

### `run-conformance-server-local.sh`

Run conformance tests against server with local executor.

```bash
./scripts/run-conformance-server-local.sh [options] [tags]

Options:
  -p, --port PORT    Port for server (default: 8091)
  -k, --keep         Keep server running after tests
```

### `run-conformance-distributed.sh`

Run conformance tests against distributed docker-compose setup.

```bash
./scripts/run-conformance-distributed.sh [options] [tags]

Options:
  -p, --port PORT    Host port for server (default: 8090)
  -k, --keep         Keep containers running after tests
```

### `test-utils.sh`

Shared utilities sourced by other scripts. Provides:

- **Environment loading** - Auto-loads `.env` file if present
- **Path variables** - Exports `GOWE_*` environment variables
- **Color output** - Logging functions with terminal color support
- **Prerequisite checking** - Validates Go, Docker, cwltest
- **Result tracking** - With bash 3.x compatibility
- **Build helpers** - Functions to build Go binaries
- **Process management** - URL health checks, process cleanup
- **Report generation** - Markdown report creation

## Output and Reports

### Console Output

The test runner provides formatted output with pass/fail symbols:

```
=== GoWe Comprehensive Test Suite ===

[INFO] Test type: all
[INFO] Tags: required
[INFO] Modes: unit cwl-runner cwl-runner-parallel server-local

--- Prerequisites ---
  ✓ Go version: go1.21.0 darwin/arm64
  ✓ cwltest: installed
  ✓ Docker: available

=== [TIER 1] Core Execution Tests ===
  ✓ unit: All unit tests passed (2.5s)
  ✓ cwl-runner: 84/84 passed (45.2s)
  ✓ cwl-runner-parallel: 84/84 passed (32.1s)

=== [TIER 2] Server Mode Tests ===
  ✗ server-local: 72/84 passed (128 known failures)

=== Overall Status ===
Tier 1: PASSED (gold standard verified)
Tier 2: PARTIAL (known issues)
```

### Markdown Reports

Use `-r, --report` to generate reports saved to `reports/test-results-YYMMDD.md`.

### Result Files

Individual test runs save detailed output to:

- `conformance-results.txt` - cwl-runner results
- `conformance-server-local-results.txt` - server-local results
- `conformance-distributed-results.txt` - distributed results
- `test-unit-results.txt` - Go unit test results

## Baseline and Regression Detection

The file `testdata/expected-results.json` contains baseline test results:

```json
{
  "tier1": {
    "cwl-runner": { "expected": "378/378" },
    "cwl-runner-parallel": { "expected": "378/378" }
  },
  "tier2": {
    "server-local": { "expected": "250/378", "known_failures": 128 }
  }
}
```

### Regression Policy

- **Tier 1**: Must pass 100% - any failure blocks merge
- **Tier 2**: Track progress toward 100%, no regressions allowed
- **Tier 3**: Custom tests must pass when backends are available

## Timing Estimates

Use these estimates for CI timeout configuration and planning.

### Measured Durations (378 tests per mode)

| Mode | Duration | Target | Status |
|------|----------|--------|--------|
| unit | ~2s | ~2s | OK |
| cwl-runner | ~2 min | ~2 min | OK (baseline) |
| cwl-runner-parallel | ~2 min | ~2 min | OK (baseline) |
| server-local | ~19 min | ~2 min | **TOO SLOW** |
| distributed-none | ~37 min | ~2 min | **TOO SLOW** |
| distributed-docker | ~35 min | ~2 min | **TOO SLOW** |
| staging (all) | ~22s | ~22s | OK |
| **Total (all tiers)** | **~95 min** | **~10 min** | |

> **Note:** Server-local and distributed modes are 10-18x slower than cwl-runner.
> These modes execute the same CWL tests and should converge to similar runtimes.
> The overhead comes from HTTP round-trips, submission polling, and task scheduling
> latency — all of which are optimization targets, not inherent costs. Until resolved,
> the long runtimes are effectively a performance bug.

### Recommended Timeouts

These timeouts accommodate current (slow) performance. Reduce as server modes improve.

| CI Job | Timeout | Command |
|--------|---------|---------|
| PR checks (fast) | 10 min | `./scripts/run-all-tests.sh -t 1 --required` |
| PR checks (full) | 30 min | `./scripts/run-all-tests.sh --required` |
| Nightly | 120 min | `./scripts/run-all-tests.sh` |

## CI Integration

For continuous integration, use these commands:

```bash
# Fast CI check (Tier 1 only, required tests) - ~30s
./scripts/run-all-tests.sh -t 1 --required

# Full CI check (all tiers, required tests) - ~6 min
./scripts/run-all-tests.sh --required

# Nightly full conformance (all 378 tests) - ~95 min (target: ~10 min)
./scripts/run-all-tests.sh -r
```

## Prerequisites

### Required

- **Go 1.21+** - For building and running tests
- **cwltest** - CWL conformance test runner (`pip install cwltest`)

### Optional

- **Docker** - For container-based execution and staging tests
- **docker-compose** - For distributed mode tests
- **Apptainer** - For apptainer runtime tests

### Installing cwltest

```bash
pip install cwltest
```

## Troubleshooting

### Environment Not Set Up

If tests fail with path errors or missing variables:

```bash
# Run setup script
./scripts/setup-env.sh

# Source environment
source .env

# Verify paths are set
echo $GOWE_PROJECT_ROOT
echo $GOWE_CONFORMANCE_DIR
```

### Port Already in Use

If tests fail with "port in use" errors:

```bash
# Check what's using the port
lsof -i :8090
lsof -i :8091

# Kill existing processes or use different ports
./scripts/run-conformance-server-local.sh -p 9091

# Or set custom ports in .env
export GOWE_TEST_SERVER_LOCAL_PORT=9091
export GOWE_TEST_DISTRIBUTED_PORT=9090
```

### Docker Tests Failing

Ensure Docker daemon is running:

```bash
docker info
```

For Docker-in-Docker issues, check socket permissions:

```bash
ls -la /var/run/docker.sock
```

### Bash Version Issues

The scripts require bash 4+ for associative arrays. On macOS:

```bash
brew install bash
# Ensure /opt/homebrew/bin/bash is in your PATH
```

## Contributing

When adding new test modes or fixing tests:

1. Update `testdata/expected-results.json` with new baselines
2. Add documentation to this README
3. Ensure Tier 1 tests still pass
4. Update the GitHub issue tracking test progress
