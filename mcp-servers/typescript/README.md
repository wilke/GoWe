# BV-BRC MCP Server (TypeScript)

MCP server for BV-BRC bioinformatics services. Enables LLMs to:
- Browse and manage BV-BRC workspace files
- Discover available bioinformatics applications
- Submit, monitor, and manage analysis jobs

## Installation

### Local
```bash
cd mcp-servers/typescript
npm install
npm run build
```

### Docker
```bash
docker build -t bvbrc-mcp-ts .
```

## Authentication

The server looks for a BV-BRC token in this order:
1. `BVBRC_TOKEN` environment variable
2. `P3_AUTH_TOKEN` environment variable
3. `~/.bvbrc_token` file
4. `~/.patric_token` file
5. `~/.p3_token` file

## Usage

### With Claude Code / Claude Desktop

Add to your MCP config:

**Direct:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "node",
      "args": ["/path/to/mcp-servers/typescript/dist/index.js"],
      "env": {
        "BVBRC_TOKEN": "your-token-here"
      }
    }
  }
}
```

**Docker:**
```json
{
  "mcpServers": {
    "bvbrc": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "BVBRC_TOKEN", "bvbrc-mcp-ts"],
      "env": {
        "BVBRC_TOKEN": "your-token-here"
      }
    }
  }
}
```

See [SETUP.md](../SETUP.md) for full configuration guide.

### Development Mode

```bash
npm run dev
```

## Available Tools

### Workspace Tools (9)
| Tool | Description |
|------|-------------|
| `workspace_list` | List directory contents |
| `workspace_get` | Get file metadata/content |
| `workspace_create_folder` | Create folder |
| `workspace_upload` | Upload file content |
| `workspace_delete` | Delete object |
| `workspace_copy` | Copy object |
| `workspace_move` | Move/rename object |
| `workspace_share` | Set permissions |
| `workspace_download_url` | Get download URL |

### App Tools (2)
| Tool | Description |
|------|-------------|
| `apps_list` | List all BV-BRC apps |
| `app_schema` | Get app parameter schema |

### Job Tools (5)
| Tool | Description |
|------|-------------|
| `job_submit` | Submit new job |
| `job_status` | Check job status |
| `job_list` | List recent jobs |
| `job_cancel` | Cancel job |
| `job_logs` | Get execution logs |

## Example Usage

```
User: What's in my workspace?
LLM: [calls workspace_list with path="/user@patricbrc.org/home/"]

User: What apps are available?
LLM: [calls apps_list]

User: Annotate my genome
LLM: [calls app_schema with app_id="GenomeAnnotation"]
     [calls job_submit with params]
```
