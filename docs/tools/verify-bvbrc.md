# verify-bvbrc

The `verify-bvbrc` tool tests connectivity and functionality of BV-BRC external services. It validates your authentication token and verifies that all required API endpoints are accessible.

## Installation

```bash
# From source
go build -o verify-bvbrc ./cmd/verify-bvbrc
```

## Prerequisites

A valid BV-BRC authentication token is required. The tool checks these locations (in order):

1. `BVBRC_TOKEN` environment variable
2. `~/.gowe/credentials.json`
3. `~/.bvbrc_token`
4. `~/.patric_token`
5. `~/.p3_token`

## Usage

```bash
verify-bvbrc [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` | `false` | Enable debug logging (shows RPC request/response details) |

## Examples

### Basic verification

```bash
verify-bvbrc
```

### Debug mode

See full RPC request and response payloads:

```bash
verify-bvbrc --debug
```

### With environment token

```bash
BVBRC_TOKEN="un=user@patricbrc.org|tokenid=...|expiry=...|sig=..." verify-bvbrc
```

### Test authentication flow

Set username and password to test the full authentication flow:

```bash
BVBRC_USERNAME="myuser" BVBRC_PASSWORD="mypassword" verify-bvbrc
```

## Test Sequence

The tool performs these checks:

| # | Check | Description |
|---|-------|-------------|
| 1 | Service URL: app_service | Verify app_service endpoint is reachable |
| 2 | Service URL: Workspace | Verify Workspace endpoint is reachable |
| 3 | Auth endpoint | Verify authentication endpoint responds |
| 4 | Token format | Validate token structure (pipe-delimited with required fields) |
| 5 | Authenticate (optional) | Test username/password authentication if provided |
| 6 | service_status | Call AppService.service_status RPC |
| 7 | enumerate_apps | Call AppService.enumerate_apps RPC |
| 8 | query_tasks | Call AppService.query_tasks RPC |
| 9 | query_task_summary | Call AppService.query_task_summary RPC |
| 10 | Workspace.ls | List user's home workspace directory |

## Output Format

### Successful run

```
Token loaded for user "awilke@patricbrc.org" (expires 2024-02-15)

BV-BRC API Verification Report
===============================
[PASS] Service URL: app_service — reachable (HTTP 200)
[PASS] Service URL: Workspace — reachable (HTTP 200)
[PASS] Auth endpoint — reachable (HTTP 401)
[PASS] Token format — valid pipe-delimited, user=awilke@patricbrc.org, expires 2024-02-15
[PASS] service_status — enabled: Application services are available
[PASS] enumerate_apps — returned 45 apps (docs list 22): GenomeAnnotation, GenomeAssembly2, ...
[PASS] query_tasks — response: [{"queued":0,"in-progress":0,"completed":42,...}]
[PASS] query_task_summary — queued=0, in-progress=0, completed=42, failed=3, deleted=0
[PASS] Workspace.ls — 15 items in /awilke@patricbrc.org/home/

Summary: 10/10 passed
```

### Failed run

```
Token loaded for user "awilke@patricbrc.org" (expires 2024-01-01)

BV-BRC API Verification Report
===============================
[PASS] Service URL: app_service — reachable (HTTP 200)
[PASS] Service URL: Workspace — reachable (HTTP 200)
[PASS] Auth endpoint — reachable (HTTP 401)
[FAIL] Token format — missing fields: sig
[FAIL] service_status — RPC error: authentication required
[FAIL] enumerate_apps — RPC error: authentication required

Summary: 3/6 passed, 3 failed
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | All tests passed |
| 1 | One or more tests failed, or token expired/missing |

## BV-BRC Services Tested

### App Service

**URL:** `https://p3.theseed.org/services/app_service`

JSON-RPC 1.1 service for:
- Listing available bioinformatics apps
- Submitting and monitoring jobs
- Querying task status and history

### Workspace Service

**URL:** `https://p3.theseed.org/services/Workspace`

JSON-RPC 1.1 service for:
- Browsing workspace directories
- Managing files and folders
- Accessing job outputs

### Authentication Endpoint

**URL:** `https://user.patricbrc.org/authenticate`

REST endpoint for:
- Username/password authentication
- Token generation

## Token Format

BV-BRC tokens are pipe-delimited strings with these fields:

```
un=username@patricbrc.org|tokenid=ABC123|expiry=1234567890|client_id=...|token_type=...|SigningSubject=...|sig=...
```

Required fields:
- `un` - Username
- `tokenid` - Unique token identifier
- `expiry` - Unix timestamp expiration
- `sig` - Cryptographic signature

## Tutorial: Diagnosing BV-BRC Issues

### 1. Check token validity

```bash
verify-bvbrc
```

If token format fails, get a new token from [BV-BRC](https://www.bv-brc.org/).

### 2. Test network connectivity

```bash
# Test app_service
curl -X POST https://p3.theseed.org/services/app_service \
  -H "Content-Type: application/json" \
  -d '{"id":"1","method":"AppService.service_status","version":"1.1","params":[]}'

# Test workspace
curl -X POST https://p3.theseed.org/services/Workspace \
  -H "Content-Type: application/json" \
  -d '{"id":"1","method":"Workspace.list_workspaces","version":"1.1","params":[]}'
```

### 3. Debug RPC calls

```bash
verify-bvbrc --debug 2>&1 | less
```

Look for:
- Request payloads
- Response status codes
- Error messages

### 4. Test fresh authentication

```bash
BVBRC_USERNAME="myuser" BVBRC_PASSWORD="mypassword" verify-bvbrc
```

This tests:
- Password authentication works
- New tokens are generated correctly

### 5. Check task history

The `query_task_summary` check shows your job statistics:

```
[PASS] query_task_summary — queued=0, in-progress=2, completed=150, failed=5, deleted=10
```

If you have many failed jobs, investigate via the BV-BRC web interface.

## Troubleshooting

### Token expired

```
fatal: BV-BRC token is expired (expiry: 2024-01-01T00:00:00Z)
```

Get a new token:
1. Log in to [BV-BRC](https://www.bv-brc.org/)
2. Go to your profile
3. Copy the authentication token
4. Save it:
   ```bash
   gowe login --token "un=..."
   ```

### Missing token

```
fatal: no BV-BRC token found
```

Set one of:
```bash
# Environment variable
export BVBRC_TOKEN="un=..."

# Or save to file
gowe login --token "un=..."

# Or create token file directly
echo "un=..." > ~/.bvbrc_token
```

### Service unreachable

```
[FAIL] Service URL: app_service — unreachable: connection refused
```

Check:
1. Network connectivity: `ping p3.theseed.org`
2. Firewall rules for outbound HTTPS
3. Proxy settings: `export HTTPS_PROXY=...`

### RPC authentication error

```
[FAIL] enumerate_apps — RPC error: authentication required
```

Your token may be:
1. Expired (check expiry date)
2. Malformed (check token format)
3. Revoked (get a new token)

### Workspace path not found

```
[FAIL] Workspace.ls — home path key not found; got keys: []
```

The username in your token may not match your workspace path. Check:
```bash
# Your token username
grep "un=" ~/.bvbrc_token

# Expected workspace path format
# /username@patricbrc.org/home/
```

## Integration with GoWe

Run this tool before starting the GoWe server to ensure BV-BRC connectivity:

```bash
#!/bin/bash
# start-gowe.sh

# Verify BV-BRC access
if ! verify-bvbrc; then
    echo "BV-BRC verification failed. Server will start without BV-BRC executor."
    echo "Run 'gowe login' to configure authentication."
fi

# Start server
exec gowe-server "$@"
```
