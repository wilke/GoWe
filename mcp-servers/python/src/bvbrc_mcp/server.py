"""BV-BRC MCP Server."""

import json
import sys
from dataclasses import asdict
from typing import Any

from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp.types import (
    TextContent,
    Tool,
    Resource,
)

from .client import BVBRCClient

# Initialize client and server
client = BVBRCClient()
server = Server("bvbrc-mcp")


# --- Tool Definitions ---

TOOLS = [
    # Workspace Tools
    Tool(
        name="workspace_list",
        description="List contents of a BV-BRC workspace directory",
        inputSchema={
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Workspace path"},
                "recursive": {"type": "boolean", "default": False},
            },
            "required": ["path"],
        },
    ),
    Tool(
        name="workspace_get",
        description="Get file metadata and optionally content from BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "metadata_only": {"type": "boolean", "default": False},
            },
            "required": ["path"],
        },
    ),
    Tool(
        name="workspace_create_folder",
        description="Create a new folder in the BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"],
        },
    ),
    Tool(
        name="workspace_upload",
        description="Upload file content to the BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "content": {"type": "string"},
                "type": {"type": "string", "default": "unspecified"},
            },
            "required": ["path", "content"],
        },
    ),
    Tool(
        name="workspace_delete",
        description="Delete a file or folder from the BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "force": {"type": "boolean", "default": False},
            },
            "required": ["path"],
        },
    ),
    Tool(
        name="workspace_copy",
        description="Copy a file or folder in the BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {
                "source": {"type": "string"},
                "destination": {"type": "string"},
            },
            "required": ["source", "destination"],
        },
    ),
    Tool(
        name="workspace_move",
        description="Move or rename a file or folder in the BV-BRC workspace",
        inputSchema={
            "type": "object",
            "properties": {
                "source": {"type": "string"},
                "destination": {"type": "string"},
            },
            "required": ["source", "destination"],
        },
    ),
    Tool(
        name="workspace_share",
        description="Set sharing permissions on a workspace object",
        inputSchema={
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "user": {"type": "string"},
                "permission": {"type": "string", "enum": ["r", "w", "n"]},
            },
            "required": ["path", "user", "permission"],
        },
    ),
    Tool(
        name="workspace_download_url",
        description="Get a download URL for a workspace file",
        inputSchema={
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"],
        },
    ),
    # App Tools
    Tool(
        name="apps_list",
        description="List all available BV-BRC bioinformatics applications",
        inputSchema={"type": "object", "properties": {}},
    ),
    Tool(
        name="app_schema",
        description="Get the parameter schema for a specific BV-BRC application",
        inputSchema={
            "type": "object",
            "properties": {"app_id": {"type": "string"}},
            "required": ["app_id"],
        },
    ),
    # Job Tools
    Tool(
        name="job_submit",
        description="Submit a new BV-BRC bioinformatics job",
        inputSchema={
            "type": "object",
            "properties": {
                "app_id": {"type": "string"},
                "params": {"type": "object"},
                "output_path": {"type": "string"},
            },
            "required": ["app_id", "params", "output_path"],
        },
    ),
    Tool(
        name="job_status",
        description="Check the status of one or more BV-BRC jobs",
        inputSchema={
            "type": "object",
            "properties": {
                "task_ids": {"type": "array", "items": {"type": "string"}},
            },
            "required": ["task_ids"],
        },
    ),
    Tool(
        name="job_list",
        description="List recent BV-BRC jobs for the authenticated user",
        inputSchema={
            "type": "object",
            "properties": {
                "offset": {"type": "number", "default": 0},
                "limit": {"type": "number", "default": 25},
            },
        },
    ),
    Tool(
        name="job_cancel",
        description="Cancel a running or queued BV-BRC job",
        inputSchema={
            "type": "object",
            "properties": {"task_id": {"type": "string"}},
            "required": ["task_id"],
        },
    ),
    Tool(
        name="job_logs",
        description="Get execution logs for a BV-BRC job",
        inputSchema={
            "type": "object",
            "properties": {"task_id": {"type": "string"}},
            "required": ["task_id"],
        },
    ),
]


# --- Handlers ---


@server.list_tools()
async def list_tools() -> list[Tool]:
    """Return available tools."""
    return TOOLS


@server.call_tool()
async def call_tool(name: str, arguments: dict[str, Any]) -> list[TextContent]:
    """Handle tool calls."""
    try:
        result = await handle_tool(name, arguments)
        return [TextContent(type="text", text=result)]
    except Exception as e:
        return [TextContent(type="text", text=f"Error: {e}")]


async def handle_tool(name: str, args: dict[str, Any]) -> str:
    """Route tool calls to handlers."""
    match name:
        # Workspace
        case "workspace_list":
            result = client.workspace_ls([args["path"]], args.get("recursive", False))
            return _to_json({p: [asdict(o) for o in objs] for p, objs in result.items()})

        case "workspace_get":
            result = client.workspace_get([args["path"]], args.get("metadata_only", False))
            return _to_json(asdict(result[0]) if result else None)

        case "workspace_create_folder":
            result = client.workspace_create(args["path"], "folder")
            return _to_json(asdict(result))

        case "workspace_upload":
            result = client.workspace_create(
                args["path"],
                args.get("type", "unspecified"),
                args["content"],
                overwrite=True,
            )
            return _to_json(asdict(result))

        case "workspace_delete":
            client.workspace_delete([args["path"]], args.get("force", False))
            return f"Deleted: {args['path']}"

        case "workspace_copy":
            result = client.workspace_copy(args["source"], args["destination"])
            return _to_json([asdict(o) for o in result])

        case "workspace_move":
            result = client.workspace_move(args["source"], args["destination"])
            return _to_json([asdict(o) for o in result])

        case "workspace_share":
            client.workspace_set_permissions(
                args["path"], [(args["user"], args["permission"])]
            )
            return f"Permission '{args['permission']}' set for {args['user']} on {args['path']}"

        case "workspace_download_url":
            result = client.workspace_get_download_url([args["path"]])
            return _to_json({"url": result.get(args["path"])})

        # Apps
        case "apps_list":
            apps = client.enumerate_apps()
            summary = [{"id": a.id, "label": a.label, "description": a.description} for a in apps]
            return _to_json(summary)

        case "app_schema":
            app = client.query_app_description(args["app_id"])
            return _to_json(asdict(app) if app else None)

        # Jobs
        case "job_submit":
            task = client.start_app(args["app_id"], args["params"], args["output_path"])
            return _to_json(asdict(task))

        case "job_status":
            result = client.query_tasks(args["task_ids"])
            return _to_json({tid: asdict(t) for tid, t in result.items()})

        case "job_list":
            offset = args.get("offset", 0)
            limit = min(args.get("limit", 25), 100)
            tasks = client.enumerate_tasks(offset, limit)
            return _to_json([asdict(t) for t in tasks])

        case "job_cancel":
            success = client.kill_task(args["task_id"])
            return f"Cancelled: {args['task_id']}" if success else f"Failed to cancel: {args['task_id']}"

        case "job_logs":
            # query_app_log is not available in the BV-BRC API
            # Get task info and suggest where logs can be found
            task_id = args["task_id"]
            tasks = client.query_tasks([task_id])
            if task_id in tasks:
                task = tasks[task_id]
                return _to_json({
                    "note": "Direct log retrieval is not available via API",
                    "task_id": task_id,
                    "status": task.status,
                    "output_path": task.output_path,
                    "suggestion": f"Use workspace_list on '{task.output_path}' to find log files (.log, .err, .out)"
                })
            return f"Task {task_id} not found"

        case _:
            raise ValueError(f"Unknown tool: {name}")


@server.list_resources()
async def list_resources() -> list[Resource]:
    """Return available resources."""
    return [
        Resource(
            uri="bvbrc://apps",
            name="BV-BRC Application Catalog",
            description="List of available bioinformatics applications",
            mimeType="application/json",
        ),
        Resource(
            uri="bvbrc://workspace-types",
            name="Workspace Object Types",
            description="Reference of valid workspace object types",
            mimeType="application/json",
        ),
    ]


@server.read_resource()
async def read_resource(uri: str) -> str:
    """Read resource content."""
    if uri == "bvbrc://apps":
        apps = client.enumerate_apps()
        return _to_json([asdict(a) for a in apps])

    if uri == "bvbrc://workspace-types":
        types = [
            {"type": "folder", "description": "Directory/container"},
            {"type": "job_result", "description": "Job output folder"},
            {"type": "contigs", "description": "FASTA contigs"},
            {"type": "reads", "description": "FASTQ reads"},
            {"type": "feature_group", "description": "Genomic features"},
            {"type": "genome_group", "description": "Genome collection"},
            {"type": "unspecified", "description": "General file"},
            {"type": "txt", "description": "Plain text"},
            {"type": "json", "description": "JSON data"},
            {"type": "csv", "description": "CSV data"},
            {"type": "html", "description": "HTML report"},
            {"type": "pdf", "description": "PDF document"},
            {"type": "nwk", "description": "Newick tree"},
            {"type": "svg", "description": "SVG graphic"},
        ]
        return _to_json(types)

    raise ValueError(f"Unknown resource: {uri}")


def _to_json(data: Any) -> str:
    """Convert data to formatted JSON."""
    return json.dumps(data, indent=2, default=str)


def main():
    """Run the MCP server."""
    print(f"BV-BRC MCP Server v0.1.0", file=sys.stderr)
    auth_status = f"yes ({client.username})" if client.is_authenticated else "no"
    print(f"Authenticated: {auth_status}", file=sys.stderr)

    import asyncio
    asyncio.run(run_server())


async def run_server():
    """Async server runner."""
    async with stdio_server() as (read_stream, write_stream):
        await server.run(read_stream, write_stream, server.create_initialization_options())


if __name__ == "__main__":
    main()
