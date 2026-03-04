# GoWe Test Scripts

This directory contains the test infrastructure for GoWe, providing comprehensive testing across all execution modes with cwl-runner as the gold standard baseline.

## Quick Start

```bash
# Run all tests with required tags (84 tests, fastest)
./scripts/run-all-tests.sh

# Run full conformance suite (378 tests)
./scripts/run-all-tests.sh --full

# Run only Tier 1 tests (CI fast path)
./scripts/run-all-tests.sh -t 1

# Run unit tests only
./scripts/run-all-tests.sh unit

# Run staging backend tests
./scripts/run-staging-tests.sh
```

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
            distributed-docker  distributed-apptainer  distributed-bare
```

| Mode | Description | Container Runtime |
|------|-------------|-------------------|
| `cwl-runner` | Direct CLI execution | Docker/Apptainer |
| `cwl-runner --parallel` | Parallel step execution | Docker/Apptainer |
| `server-local` | Server with LocalExecutor | Docker/Apptainer |
| `distributed-bare` | Server + Workers | Host $PATH (no containers) |
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
| `distributed-bare` | Workers with host tools | TBD | 378/378 |
| `distributed-docker` | Workers with Docker | ~200/378 | 378/378 |
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
  -q, --quick         Quick mode: required tests only (84 tests)
  --full              Full mode: all conformance tests (378 tests)
  --no-docker         Skip tests requiring Docker
  --parallel          Use --parallel flag for cwl-runner
  -v, --verbose       Verbose output
  -r, --report        Generate markdown report
  -h, --help          Show this help message
```

**Examples:**

```bash
# Run required tests for all modes
./scripts/run-all-tests.sh

# Run full 378-test conformance suite
./scripts/run-all-tests.sh --full

# Run only cwl-runner tests
./scripts/run-all-tests.sh -m cwl-runner

# Run Tier 1 tests only (fast CI)
./scripts/run-all-tests.sh -t 1

# Skip distributed tests
./scripts/run-all-tests.sh -s distributed-docker -s distributed-bare

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

- Color output and logging functions
- Prerequisite checking (Go, Docker, cwltest)
- Result tracking with bash 3.x compatibility
- Build helpers
- Process management utilities
- Report generation

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

## CI Integration

For continuous integration, use these commands:

```bash
# Fast CI check (Tier 1 only, required tests)
./scripts/run-all-tests.sh -t 1 -q

# Full CI check (all tiers, required tests)
./scripts/run-all-tests.sh -q

# Nightly full conformance (all 378 tests)
./scripts/run-all-tests.sh --full -r
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

### Port Already in Use

If tests fail with "port in use" errors:

```bash
# Check what's using the port
lsof -i :8090
lsof -i :8091

# Kill existing processes or use different ports
./scripts/run-conformance-server-local.sh -p 9091
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
