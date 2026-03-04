# GoWe Configuration Templates

This directory contains **reference templates** for GoWe configuration. These YAML files document the planned configuration schema but are **not currently loaded** by the server or worker binaries.

> **Current state:** All server and worker settings are configured via CLI flags. See `--help` for each binary.
> **Planned:** Full YAML config loading via `--config`. See [docs/planned-config.md](../docs/planned-config.md).

## Files

### server.example.yaml

Reference template for server configuration including:
- Listen address and database path
- Authentication settings (anonymous access, worker keys)
- Executor configuration
- File upload proxy settings (S3, Shock, local)

**Current usage:** The server `--config` flag only reads an admin user list, not the full YAML schema shown here.

### worker.example.yaml

Reference template for worker configuration including:
- Server connection settings
- Container runtime (Docker, Apptainer, bare)
- Staging configuration (S3, Shock, HTTP, shared filesystem)
- Path mappings for distributed execution

**Current usage:** The worker has no `--config` flag. All settings must be passed as CLI flags.

### credentials.example.yaml

Reference template for staging backend credentials:
- HTTP authentication (bearer, basic, header)
- S3/MinIO credentials
- Shock tokens
- BV-BRC authentication

**Current usage:** The worker's `--http-credentials` flag loads credentials from a **JSON** file (not YAML). S3, Shock, and BV-BRC credentials are passed via flags or environment variables.

## Current Configuration

All configuration is done through CLI flags and a few environment variable fallbacks:

```bash
# Server
./bin/server --addr :8080 --db ~/.gowe/gowe.db --allow-anonymous

# Worker
./bin/worker --server http://localhost:8080 --runtime docker --stage-out local
```

See the main [README.md](../README.md) for complete flag documentation.

## Testing Configuration

For development and testing, use the `.env` file in the project root:

```bash
# Copy and customize
cp .env.example .env

# Source before running tests
source .env
./scripts/run-all-tests.sh
```

The `.env` file sets paths and environment variables for the test scripts.
