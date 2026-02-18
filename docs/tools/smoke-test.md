# smoke-test

The `smoke-test` tool performs end-to-end integration testing of the GoWe API. It verifies that the server is functioning correctly by executing a complete workflow lifecycle.

## Installation

```bash
# From source
go build -o smoke-test ./cmd/smoke-test
```

## Usage

```bash
smoke-test [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `http://localhost:8080` | GoWe server address |
| `--timeout` | `2m` | Maximum time to wait for submission to complete |
| `--no-cleanup` | `false` | Skip cleanup (leave workflow/submission for inspection) |

## Examples

### Basic smoke test

```bash
# Test local server
smoke-test

# Test remote server
smoke-test --addr https://gowe.example.com:8080
```

### Extended timeout

For slower systems or when running BV-BRC jobs:

```bash
smoke-test --timeout 10m
```

### Debug mode (no cleanup)

Keep the test workflow and submission for inspection:

```bash
smoke-test --no-cleanup
```

Then inspect via API:
```bash
curl http://localhost:8080/api/v1/workflows/
curl http://localhost:8080/api/v1/submissions/
```

## Test Workflow

The smoke test uses an embedded CWL workflow:

```yaml
cwlVersion: v1.2
$graph:
  - id: echo-tool
    class: CommandLineTool
    baseCommand: ["echo", "smoke-test-ok"]
    inputs:
      tag: { type: string }
    outputs:
      out: { type: File, outputBinding: { glob: "*.txt" } }

  - id: main
    class: Workflow
    inputs:
      tag: string
    steps:
      echo:
        run: "#echo-tool"
        in:
          tag: tag
        out: [out]
    outputs:
      result:
        type: File
        outputSource: echo/out
```

This workflow:
1. Runs `echo smoke-test-ok` via the local executor
2. Captures output to a file
3. Returns the file as output

## Test Sequence

The smoke test performs these checks in order:

| # | Check | Description |
|---|-------|-------------|
| 1 | health | Verify server health endpoint |
| 2 | create workflow | POST workflow and verify response |
| 3 | get workflow | GET workflow and verify structure |
| 4 | validate workflow | POST validate and verify result |
| 5 | create submission | POST submission with inputs |
| 6 | poll submission | Poll until terminal state (COMPLETED/FAILED) |
| 7 | list tasks | GET tasks and verify states |
| 8 | task logs | GET logs for each task |
| 9 | cleanup | DELETE workflow (unless --no-cleanup) |

## Output Format

```
GoWe API Smoke Test Report
==========================
[PASS] health — healthy, version=dev, uptime=1h2m3s, executors: local=available, docker=available
[PASS] create workflow — id=wf_abc123, name=smoke-test-workflow, steps=1
[PASS] get workflow — name=smoke-test-workflow, steps=[echo]
[PASS] validate workflow — valid=true
[PASS] create submission — id=sub_xyz789, state=PENDING, tasks=[echo(PENDING)]
[PASS] poll submission — COMPLETED
[PASS] task echo (local) — id=task_001, state=SUCCESS
[PASS] task logs — exit_code=0, stdout="smoke-test-ok"
[PASS] cleanup workflow — HTTP 200

Summary: 9/9 passed
```

### Failed test output

```
GoWe API Smoke Test Report
==========================
[PASS] health — healthy, version=dev, uptime=1h2m3s
[PASS] create workflow — id=wf_abc123, name=smoke-test-workflow, steps=1
[PASS] get workflow — name=smoke-test-workflow, steps=[echo]
[PASS] validate workflow — valid=true
[PASS] create submission — id=sub_xyz789, state=PENDING, tasks=[echo(PENDING)]
[FAIL] poll submission — timeout after 2m0s, last state: RUNNING
[FAIL] task echo (local) — id=task_001, state=RUNNING

Summary: 5/7 passed, 2 failed
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | All tests passed |
| 1 | One or more tests failed |

## Tutorial: CI/CD Integration

### GitHub Actions

```yaml
name: Integration Tests

on: [push, pull_request]

jobs:
  smoke-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.24'

      - name: Build
        run: |
          go build -o bin/gowe-server ./cmd/server
          go build -o bin/smoke-test ./cmd/smoke-test

      - name: Start server
        run: |
          ./bin/gowe-server --debug &
          sleep 5  # Wait for server to start

      - name: Run smoke test
        run: ./bin/smoke-test --timeout 1m

      - name: Server logs
        if: failure()
        run: cat ~/.gowe/server.log || true
```

### Docker Compose

```yaml
version: '3.8'

services:
  server:
    build: .
    ports:
      - "8080:8080"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/api/v1/health"]
      interval: 5s
      timeout: 3s
      retries: 5

  smoke-test:
    build:
      context: .
      dockerfile: Dockerfile.test
    depends_on:
      server:
        condition: service_healthy
    command: smoke-test --addr http://server:8080 --timeout 2m
```

### Shell script wrapper

```bash
#!/bin/bash
# run-smoke-test.sh

set -e

# Start server in background
./bin/gowe-server --debug > /tmp/server.log 2>&1 &
SERVER_PID=$!

# Wait for server to be ready
for i in {1..30}; do
    if curl -s http://localhost:8080/api/v1/health > /dev/null; then
        break
    fi
    sleep 1
done

# Run smoke test
./bin/smoke-test --timeout 2m
RESULT=$?

# Cleanup
kill $SERVER_PID 2>/dev/null || true

# Show server logs on failure
if [ $RESULT -ne 0 ]; then
    echo "=== Server Logs ==="
    cat /tmp/server.log
fi

exit $RESULT
```

## Troubleshooting

### Server unreachable

```
[FAIL] health — unreachable: dial tcp 127.0.0.1:8080: connect: connection refused
Server unreachable at http://localhost:8080 — aborting.
```

Start the server first:
```bash
gowe-server &
sleep 2
smoke-test
```

### Timeout waiting for completion

```
[FAIL] poll submission — timeout after 2m0s, last state: RUNNING
```

1. Check server logs for errors
2. Increase timeout: `--timeout 5m`
3. Use `--no-cleanup` and inspect manually:
   ```bash
   curl http://localhost:8080/api/v1/submissions/sub_xyz789/
   curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/
   ```

### Task failed

```
[FAIL] task echo (local) — id=task_001, state=FAILED
```

Check task logs:
```bash
curl http://localhost:8080/api/v1/submissions/sub_xyz789/tasks/task_001/logs
```

### Local executor not available

If the smoke test fails because the local executor isn't registered, ensure the server is started without container-only mode.
