# BV-BRC MCP Server Setup Guide

This guide covers how to build, run, and connect the BV-BRC MCP server to Claude Code.

## Quick Start

Choose either **TypeScript** or **Python** implementation:

### Option A: TypeScript (Node.js)

```bash
cd mcp-servers/typescript
npm install
npm run build
```

### Option B: Python

```bash
cd mcp-servers/python
pip install -e .
```

---

## Authentication

Get your BV-BRC token from https://www.bv-brc.org/ (login required).

### Option 1: Environment Variable (Recommended)

```bash
export BVBRC_TOKEN="un=youruser|tokenid=...|expiry=...|sig=..."
```

### Option 2: Token File

Save your token to one of these files:
- `~/.bvbrc_token`
- `~/.patric_token`
- `~/.p3_token`

```bash
echo "un=youruser|tokenid=...|expiry=...|sig=..." > ~/.bvbrc_token
chmod 600 ~/.bvbrc_token
```

---

## Docker Setup

### Build Images

```bash
# TypeScript
cd mcp-servers/typescript
docker build -t bvbrc-mcp-ts .

# Python
cd mcp-servers/python
docker build -t bvbrc-mcp-py .
```

### Test Docker Image

```bash
# Test TypeScript (should print server info to stderr)
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}' | \
  docker run -i -e BVBRC_TOKEN="$BVBRC_TOKEN" bvbrc-mcp-ts

# Test Python
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}' | \
  docker run -i -e BVBRC_TOKEN="$BVBRC_TOKEN" bvbrc-mcp-py
```

---

## Claude Code Integration

### Method 1: Direct (No Docker)

Add to your Claude Code MCP config (`~/.config/claude/claude_desktop_config.json` or via Claude Code settings):

**TypeScript:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "node",
      "args": ["/absolute/path/to/mcp-servers/typescript/dist/index.js"],
      "env": {
        "BVBRC_TOKEN": "un=youruser|tokenid=...|expiry=...|sig=..."
      }
    }
  }
}
```

**Python:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "python",
      "args": ["-m", "bvbrc_mcp"],
      "env": {
        "BVBRC_TOKEN": "un=youruser|tokenid=...|expiry=...|sig=...",
        "PYTHONPATH": "/absolute/path/to/mcp-servers/python/src"
      }
    }
  }
}
```

Or if installed via pip:
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "bvbrc-mcp",
      "env": {
        "BVBRC_TOKEN": "un=youruser|tokenid=...|expiry=...|sig=..."
      }
    }
  }
}
```

### Method 2: Docker

**TypeScript:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "BVBRC_TOKEN", "bvbrc-mcp-ts"],
      "env": {
        "BVBRC_TOKEN": "un=youruser|tokenid=...|expiry=...|sig=..."
      }
    }
  }
}
```

**Python:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "BVBRC_TOKEN", "bvbrc-mcp-py"],
      "env": {
        "BVBRC_TOKEN": "un=youruser|tokenid=...|expiry=...|sig=..."
      }
    }
  }
}
```

### Method 3: Using Token File with Docker

Mount your token file into the container:

```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-v", "${HOME}/.bvbrc_token:/home/mcp/.bvbrc_token:ro",
        "bvbrc-mcp-ts"
      ]
    }
  }
}
```

---

## Verify Connection

After configuring Claude Code, ask Claude:

> "What BV-BRC tools are available?"

Claude should respond with a list of 16 tools (workspace, app, and job tools).

Then try:

> "List available BV-BRC applications"

This will call the `apps_list` tool and return the bioinformatics apps.

---

## Troubleshooting

### "Not authenticated" error

- Verify your token: `echo $BVBRC_TOKEN | head -c 50`
- Check token expiry (tokens expire after ~60 days)
- Re-login at https://www.bv-brc.org/ to get a new token

### Docker permission issues

```bash
# Ensure token file is readable
chmod 644 ~/.bvbrc_token

# Or pass token via environment instead
docker run -i -e BVBRC_TOKEN="..." bvbrc-mcp-ts
```

### Claude Code doesn't see the server

1. Restart Claude Code after config changes
2. Check the config file path is correct for your OS:
   - macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
   - Linux: `~/.config/claude/claude_desktop_config.json`
   - Windows: `%APPDATA%\Claude\claude_desktop_config.json`

### Test MCP server manually

```bash
# Start server and send a tools/list request
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | node dist/index.js
```

---

## Available Tools

| Tool | Description |
|------|-------------|
| `workspace_list` | List workspace directory contents |
| `workspace_get` | Get file metadata/content |
| `workspace_create_folder` | Create a folder |
| `workspace_upload` | Upload file content |
| `workspace_delete` | Delete file/folder |
| `workspace_copy` | Copy file/folder |
| `workspace_move` | Move/rename file/folder |
| `workspace_share` | Set sharing permissions |
| `workspace_download_url` | Get download URL |
| `apps_list` | List BV-BRC applications |
| `app_schema` | Get app parameter schema |
| `job_submit` | Submit analysis job |
| `job_status` | Check job status |
| `job_list` | List recent jobs |
| `job_cancel` | Cancel a job |
| `job_logs` | Get job logs |
