#!/usr/bin/env node
/**
 * BV-BRC MCP Server
 *
 * Provides MCP tools for interacting with BV-BRC bioinformatics services:
 * - Workspace file management
 * - App discovery and job submission
 * - Job monitoring and management
 */

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  ListResourcesRequestSchema,
  ReadResourceRequestSchema,
  Tool,
  TextContent,
} from "@modelcontextprotocol/sdk/types.js";

import { BVBRCClient } from "./bvbrc-client.js";

const client = new BVBRCClient();

// --- Tool Definitions ---

const tools: Tool[] = [
  // Workspace Tools
  {
    name: "workspace_list",
    description: "List contents of a BV-BRC workspace directory",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path (e.g., /user@patricbrc.org/home/)" },
        recursive: { type: "boolean", description: "List recursively", default: false },
      },
      required: ["path"],
    },
  },
  {
    name: "workspace_get",
    description: "Get file metadata and optionally content from BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path to retrieve" },
        metadata_only: { type: "boolean", description: "Return only metadata", default: false },
      },
      required: ["path"],
    },
  },
  {
    name: "workspace_create_folder",
    description: "Create a new folder in the BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path for the new folder" },
      },
      required: ["path"],
    },
  },
  {
    name: "workspace_upload",
    description: "Upload file content to the BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Destination workspace path" },
        content: { type: "string", description: "File content to upload" },
        type: { type: "string", description: "Object type (txt, json, contigs, etc.)", default: "unspecified" },
      },
      required: ["path", "content"],
    },
  },
  {
    name: "workspace_delete",
    description: "Delete a file or folder from the BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path to delete" },
        force: { type: "boolean", description: "Force deletion of non-empty directories", default: false },
      },
      required: ["path"],
    },
  },
  {
    name: "workspace_copy",
    description: "Copy a file or folder in the BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        source: { type: "string", description: "Source workspace path" },
        destination: { type: "string", description: "Destination workspace path" },
      },
      required: ["source", "destination"],
    },
  },
  {
    name: "workspace_move",
    description: "Move or rename a file or folder in the BV-BRC workspace",
    inputSchema: {
      type: "object",
      properties: {
        source: { type: "string", description: "Source workspace path" },
        destination: { type: "string", description: "Destination workspace path" },
      },
      required: ["source", "destination"],
    },
  },
  {
    name: "workspace_share",
    description: "Set sharing permissions on a workspace object",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path to share" },
        user: { type: "string", description: "Username (e.g., user@patricbrc.org)" },
        permission: { type: "string", enum: ["r", "w", "n"], description: "Permission: r=read, w=write, n=revoke" },
      },
      required: ["path", "user", "permission"],
    },
  },
  {
    name: "workspace_download_url",
    description: "Get a download URL for a workspace file",
    inputSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace path" },
      },
      required: ["path"],
    },
  },

  // App Tools
  {
    name: "apps_list",
    description: "List all available BV-BRC bioinformatics applications",
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "app_schema",
    description: "Get the parameter schema for a specific BV-BRC application",
    inputSchema: {
      type: "object",
      properties: {
        app_id: { type: "string", description: "Application ID (e.g., GenomeAnnotation)" },
      },
      required: ["app_id"],
    },
  },

  // Job Tools
  {
    name: "job_submit",
    description: "Submit a new BV-BRC bioinformatics job",
    inputSchema: {
      type: "object",
      properties: {
        app_id: { type: "string", description: "Application ID" },
        params: { type: "object", description: "Application-specific parameters" },
        output_path: { type: "string", description: "Workspace path for job output" },
      },
      required: ["app_id", "params", "output_path"],
    },
  },
  {
    name: "job_status",
    description: "Check the status of one or more BV-BRC jobs",
    inputSchema: {
      type: "object",
      properties: {
        task_ids: { type: "array", items: { type: "string" }, description: "Task IDs to check" },
      },
      required: ["task_ids"],
    },
  },
  {
    name: "job_list",
    description: "List recent BV-BRC jobs for the authenticated user",
    inputSchema: {
      type: "object",
      properties: {
        offset: { type: "number", description: "Starting offset", default: 0 },
        limit: { type: "number", description: "Max jobs to return", default: 25 },
      },
    },
  },
  {
    name: "job_cancel",
    description: "Cancel a running or queued BV-BRC job",
    inputSchema: {
      type: "object",
      properties: {
        task_id: { type: "string", description: "Task ID to cancel" },
      },
      required: ["task_id"],
    },
  },
  {
    name: "job_logs",
    description: "Get execution logs for a BV-BRC job",
    inputSchema: {
      type: "object",
      properties: {
        task_id: { type: "string", description: "Task ID" },
      },
      required: ["task_id"],
    },
  },
];

// --- Tool Handlers ---

async function handleTool(name: string, args: Record<string, unknown>): Promise<string> {
  switch (name) {
    // Workspace
    case "workspace_list": {
      const result = await client.workspaceLs([args.path as string], args.recursive as boolean);
      return JSON.stringify(result, null, 2);
    }
    case "workspace_get": {
      const result = await client.workspaceGet([args.path as string], args.metadata_only as boolean);
      return JSON.stringify(result[0] || null, null, 2);
    }
    case "workspace_create_folder": {
      const result = await client.workspaceCreate(args.path as string, "folder");
      return JSON.stringify(result, null, 2);
    }
    case "workspace_upload": {
      const result = await client.workspaceCreate(
        args.path as string,
        (args.type as string) || "unspecified",
        args.content as string,
        true
      );
      return JSON.stringify(result, null, 2);
    }
    case "workspace_delete": {
      await client.workspaceDelete([args.path as string], args.force as boolean);
      return `Deleted: ${args.path}`;
    }
    case "workspace_copy": {
      const result = await client.workspaceCopy(args.source as string, args.destination as string);
      return JSON.stringify(result, null, 2);
    }
    case "workspace_move": {
      const result = await client.workspaceMove(args.source as string, args.destination as string);
      return JSON.stringify(result, null, 2);
    }
    case "workspace_share": {
      await client.workspaceSetPermissions(args.path as string, [
        { user: args.user as string, permission: args.permission as "r" | "w" | "n" },
      ]);
      return `Permission '${args.permission}' set for ${args.user} on ${args.path}`;
    }
    case "workspace_download_url": {
      const result = await client.workspaceGetDownloadUrl([args.path as string]);
      return JSON.stringify({ url: result[args.path as string] || null }, null, 2);
    }

    // Apps
    case "apps_list": {
      const apps = await client.enumerateApps();
      const summary = apps.map((a) => ({ id: a.id, label: a.label, description: a.description }));
      return JSON.stringify(summary, null, 2);
    }
    case "app_schema": {
      const app = await client.queryAppDescription(args.app_id as string);
      return JSON.stringify(app, null, 2);
    }

    // Jobs
    case "job_submit": {
      const task = await client.startApp(
        args.app_id as string,
        args.params as Record<string, unknown>,
        args.output_path as string
      );
      return JSON.stringify(task, null, 2);
    }
    case "job_status": {
      const result = await client.queryTasks(args.task_ids as string[]);
      return JSON.stringify(result, null, 2);
    }
    case "job_list": {
      const tasks = await client.enumerateTasks(
        (args.offset as number) || 0,
        Math.min((args.limit as number) || 25, 100)
      );
      return JSON.stringify(tasks, null, 2);
    }
    case "job_cancel": {
      const success = await client.killTask(args.task_id as string);
      return success ? `Cancelled: ${args.task_id}` : `Failed to cancel: ${args.task_id}`;
    }
    case "job_logs": {
      // queryAppLog is not available in the BV-BRC API
      // Get task info and suggest where logs can be found
      const taskId = args.task_id as string;
      const tasks = await client.queryTasks([taskId]);
      if (tasks[taskId]) {
        const task = tasks[taskId];
        return JSON.stringify({
          note: "Direct log retrieval is not available via API",
          task_id: taskId,
          status: task.status,
          output_path: task.output_path,
          suggestion: `Use workspace_list on '${task.output_path}' to find log files (.log, .err, .out)`
        }, null, 2);
      }
      return `Task ${taskId} not found`;
    }

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}

// --- MCP Server Setup ---

const server = new Server(
  { name: "bvbrc-mcp", version: "0.1.0" },
  { capabilities: { tools: {}, resources: {} } }
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools }));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;
  try {
    const result = await handleTool(name, args || {});
    return {
      content: [{ type: "text", text: result } as TextContent],
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return {
      content: [{ type: "text", text: `Error: ${message}` } as TextContent],
      isError: true,
    };
  }
});

server.setRequestHandler(ListResourcesRequestSchema, async () => ({
  resources: [
    {
      uri: "bvbrc://apps",
      name: "BV-BRC Application Catalog",
      description: "List of available bioinformatics applications",
      mimeType: "application/json",
    },
    {
      uri: "bvbrc://workspace-types",
      name: "Workspace Object Types",
      description: "Reference of valid workspace object types",
      mimeType: "application/json",
    },
  ],
}));

server.setRequestHandler(ReadResourceRequestSchema, async (request) => {
  const { uri } = request.params;

  if (uri === "bvbrc://apps") {
    const apps = await client.enumerateApps();
    return {
      contents: [{ uri, mimeType: "application/json", text: JSON.stringify(apps, null, 2) }],
    };
  }

  if (uri === "bvbrc://workspace-types") {
    const types = [
      { type: "folder", description: "Directory/container" },
      { type: "job_result", description: "Job output folder" },
      { type: "contigs", description: "FASTA contigs" },
      { type: "reads", description: "FASTQ reads" },
      { type: "feature_group", description: "Genomic features" },
      { type: "genome_group", description: "Genome collection" },
      { type: "unspecified", description: "General file" },
      { type: "txt", description: "Plain text" },
      { type: "json", description: "JSON data" },
      { type: "csv", description: "CSV data" },
      { type: "html", description: "HTML report" },
      { type: "pdf", description: "PDF document" },
      { type: "nwk", description: "Newick tree" },
      { type: "svg", description: "SVG graphic" },
    ];
    return {
      contents: [{ uri, mimeType: "application/json", text: JSON.stringify(types, null, 2) }],
    };
  }

  throw new Error(`Unknown resource: ${uri}`);
});

// --- Main ---

async function main() {
  console.error(`BV-BRC MCP Server v0.1.0`);
  console.error(`Authenticated: ${client.isAuthenticated ? `yes (${client.username})` : "no"}`);

  const transport = new StdioServerTransport();
  await server.connect(transport);
}

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
