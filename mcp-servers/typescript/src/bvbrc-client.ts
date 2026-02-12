/**
 * BV-BRC JSON-RPC 1.1 Client
 */

import { readFileSync, existsSync } from "fs";
import { homedir } from "os";
import { join } from "path";

const DEFAULT_APP_SERVICE_URL = "https://p3.theseed.org/services/app_service";
const DEFAULT_WORKSPACE_URL = "https://p3.theseed.org/services/Workspace";

export interface BVBRCConfig {
  appServiceUrl?: string;
  workspaceUrl?: string;
  token?: string;
}

export interface Task {
  id: string;
  app: string;
  owner: string;
  status: "queued" | "in-progress" | "completed" | "failed" | "deleted" | "suspended";
  submit_time?: string;
  start_time?: string;
  completed_time?: string;
  parameters?: Record<string, unknown>;
  output_path?: string;
}

export interface AppDescription {
  id: string;
  label: string;
  description: string;
  parameters?: AppParameter[];
}

export interface AppParameter {
  id: string;
  label: string;
  type: string;
  required: boolean;
  default?: unknown;
  desc?: string;
}

export interface WorkspaceObject {
  path: string;
  type: string;
  owner: string;
  creation_time: string;
  id: string;
  size: number;
  user_metadata: Record<string, string>;
  auto_metadata: Record<string, string>;
  shock_ref?: string;
  data?: string;
}

interface RPCRequest {
  id: string;
  method: string;
  version: "1.1";
  params: unknown[];
}

interface RPCResponse {
  id: string;
  version: string;
  result?: unknown;
  error?: { code: number; message: string; name?: string };
}

export class BVBRCClient {
  private appServiceUrl: string;
  private workspaceUrl: string;
  private token: string;
  private requestId = 0;

  constructor(config: BVBRCConfig = {}) {
    this.appServiceUrl = config.appServiceUrl || DEFAULT_APP_SERVICE_URL;
    this.workspaceUrl = config.workspaceUrl || DEFAULT_WORKSPACE_URL;
    this.token = config.token || loadToken();
  }

  get username(): string {
    return parseTokenUsername(this.token);
  }

  get isAuthenticated(): boolean {
    return !!this.token;
  }

  // --- App Service Methods ---

  async enumerateApps(): Promise<AppDescription[]> {
    const result = await this.callAppService("AppService.enumerate_apps", []);
    return (result as AppDescription[][])?.[0] || [];
  }

  async queryAppDescription(appId: string): Promise<AppDescription | null> {
    // enumerate_apps returns full details including parameters
    const apps = await this.enumerateApps();
    return apps.find(app => app.id === appId) || null;
  }

  async startApp(appId: string, params: Record<string, unknown>, outputPath: string): Promise<Task> {
    const result = await this.callAppService("AppService.start_app", [appId, params, outputPath]);
    return (result as Task[])[0];
  }

  async queryTasks(taskIds: string[]): Promise<Record<string, Task>> {
    const result = await this.callAppService("AppService.query_tasks", [taskIds]);
    return (result as Record<string, Task>[])?.[0] || {};
  }

  async enumerateTasks(offset: number, limit: number): Promise<Task[]> {
    const result = await this.callAppService("AppService.enumerate_tasks", [offset, limit]);
    return (result as Task[][])?.[0] || [];
  }

  async killTask(taskId: string): Promise<boolean> {
    const result = await this.callAppService("AppService.kill_task", [taskId]);
    return (result as number[])?.[0] === 1;
  }

  async queryAppLog(taskId: string): Promise<string> {
    const result = await this.callAppService("AppService.query_app_log", [taskId]);
    if (typeof result === "string") return result;
    if (Array.isArray(result)) return result[0] || "";
    return "";
  }

  // --- Workspace Methods ---

  async workspaceLs(paths: string[], recursive = false): Promise<Record<string, WorkspaceObject[]>> {
    const params: Record<string, unknown> = { paths };
    if (recursive) params.recursive = true;

    const result = await this.callWorkspace("Workspace.ls", [params]);
    const raw = (result as Record<string, unknown[][]>[])?.[0] || {};

    const parsed: Record<string, WorkspaceObject[]> = {};
    for (const [path, tuples] of Object.entries(raw)) {
      parsed[path] = tuples.map(parseWorkspaceObject);
    }
    return parsed;
  }

  async workspaceGet(paths: string[], metadataOnly = false): Promise<WorkspaceObject[]> {
    const params: Record<string, unknown> = { objects: paths };
    if (metadataOnly) params.metadata_only = true;

    const result = await this.callWorkspace("Workspace.get", [params]);
    const raw = (result as unknown[][][])?.[0] || [];
    return raw.map(parseWorkspaceObject);
  }

  async workspaceCreate(
    path: string,
    type: string,
    content?: string,
    overwrite = false
  ): Promise<WorkspaceObject> {
    const objSpec = [path, type, {}, content ?? null];
    const params: Record<string, unknown> = { objects: [objSpec] };
    if (overwrite) params.overwrite = true;

    const result = await this.callWorkspace("Workspace.create", [params]);
    const raw = (result as unknown[][][])?.[0]?.[0];
    return parseWorkspaceObject(raw);
  }

  async workspaceDelete(paths: string[], force = false): Promise<void> {
    const params: Record<string, unknown> = {
      objects: paths,
      deleteDirectories: true,
    };
    if (force) params.force = true;

    await this.callWorkspace("Workspace.delete", [params]);
  }

  async workspaceCopy(source: string, destination: string): Promise<WorkspaceObject[]> {
    const params = {
      objects: [[source, destination]],
      recursive: true,
    };
    const result = await this.callWorkspace("Workspace.copy", [params]);
    const raw = (result as unknown[][][])?.[0] || [];
    return raw.map(parseWorkspaceObject);
  }

  async workspaceMove(source: string, destination: string): Promise<WorkspaceObject[]> {
    // Workspace.move is not available in the BV-BRC API
    // Implement as copy followed by delete
    const copied = await this.workspaceCopy(source, destination);
    if (copied.length > 0) {
      await this.workspaceDelete([source]);
    }
    return copied;
  }

  async workspaceSetPermissions(
    path: string,
    permissions: Array<{ user: string; permission: "r" | "w" | "n" }>
  ): Promise<void> {
    const params = {
      path,
      permissions: permissions.map((p) => [p.user, p.permission]),
    };
    await this.callWorkspace("Workspace.set_permissions", [params]);
  }

  async workspaceGetDownloadUrl(paths: string[]): Promise<Record<string, string>> {
    const params = { objects: paths };
    const result = await this.callWorkspace("Workspace.get_download_url", [params]);
    return (result as Record<string, string>[])?.[0] || {};
  }

  // --- RPC Helpers ---

  private async callAppService(method: string, params: unknown[]): Promise<unknown> {
    return this.rpcCall(this.appServiceUrl, method, params);
  }

  private async callWorkspace(method: string, params: unknown[]): Promise<unknown> {
    return this.rpcCall(this.workspaceUrl, method, params);
  }

  private async rpcCall(url: string, method: string, params: unknown[]): Promise<unknown> {
    const request: RPCRequest = {
      id: String(++this.requestId),
      method,
      version: "1.1",
      params,
    };

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (this.token) {
      headers["Authorization"] = this.token;
    }

    const response = await fetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify(request),
    });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${await response.text()}`);
    }

    const rpcResponse: RPCResponse = await response.json();

    if (rpcResponse.error) {
      throw new Error(`RPC Error ${rpcResponse.error.code}: ${rpcResponse.error.message}`);
    }

    return rpcResponse.result;
  }
}

// --- Helpers ---

function loadToken(): string {
  // Check environment first
  const envToken = process.env.BVBRC_TOKEN || process.env.P3_AUTH_TOKEN;
  if (envToken) return envToken.trim();

  // Try token files
  const home = homedir();
  const tokenFiles = [".bvbrc_token", ".patric_token", ".p3_token"];

  for (const file of tokenFiles) {
    const path = join(home, file);
    if (existsSync(path)) {
      try {
        return readFileSync(path, "utf-8").trim();
      } catch {
        continue;
      }
    }
  }

  return "";
}

function parseTokenUsername(token: string): string {
  if (!token) return "";
  const match = token.match(/un=([^|]+)/);
  return match?.[1] || "";
}

function parseWorkspaceObject(tuple: unknown[]): WorkspaceObject {
  return {
    path: String(tuple[0] || ""),
    type: String(tuple[1] || ""),
    owner: String(tuple[2] || ""),
    creation_time: String(tuple[3] || ""),
    id: String(tuple[4] || ""),
    size: Number(tuple[6]) || 0,
    user_metadata: (tuple[7] as Record<string, string>) || {},
    auto_metadata: (tuple[8] as Record<string, string>) || {},
    shock_ref: tuple[9] ? String(tuple[9]) : undefined,
    data: tuple[11] ? String(tuple[11]) : undefined,
  };
}
