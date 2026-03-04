# GoWe Configuration Templates

This directory contains example configuration files for GoWe server and worker components.

## Quick Start

```bash
# Run the setup script to initialize your environment
./scripts/setup-env.sh

# Or manually copy configs to ~/.gowe/
cp configs/server.example.yaml ~/.gowe/server.yaml
cp configs/worker.example.yaml ~/.gowe/worker.yaml
cp configs/credentials.example.yaml ~/.gowe/credentials.yaml
```

## Configuration Files

### server.example.yaml

Server configuration including:
- Listen address and database path
- Authentication settings (anonymous access, worker keys)
- Executor configuration
- File upload proxy settings (S3, Shock, local)

**Usage:**
```bash
./bin/server --config ~/.gowe/server.yaml
```

### worker.example.yaml

Worker configuration including:
- Server connection settings
- Container runtime (Docker, Apptainer, bare)
- Staging configuration (S3, Shock, HTTP, shared filesystem)
- Path mappings for distributed execution

**Usage:**
```bash
./bin/worker --config ~/.gowe/worker.yaml
```

### credentials.example.yaml

Credentials for staging backends:
- HTTP authentication (bearer, basic, header)
- S3/MinIO credentials
- Shock tokens
- BV-BRC authentication

**Security:** Keep `~/.gowe/credentials.yaml` secure and never commit it.

## Environment Variables

Configuration values can reference environment variables using `${VAR_NAME}` syntax:

```yaml
s3:
  access_key_id: "${AWS_ACCESS_KEY_ID}"
  secret_access_key: "${AWS_SECRET_ACCESS_KEY}"
```

## Configuration Precedence

1. CLI flags (highest priority)
2. Environment variables
3. Config file values
4. Default values (lowest priority)

## Directory Structure

```
~/.gowe/
├── server.yaml         # Server configuration
├── worker.yaml         # Worker configuration
├── credentials.yaml    # Staging backend credentials
├── worker-keys.json    # Worker authentication keys
└── gowe.db            # SQLite database

/etc/gowe/              # System-wide config (optional)
├── server.yaml
└── worker.yaml
```

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
