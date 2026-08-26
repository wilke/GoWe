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

/**
 * A parsed Workspace ObjectMeta tuple.
 *
 * Layout per the Workspace module's own Workspace.spec:
 *
 *   [ObjectName, ObjectType, FullObjectPath, creation_time, ObjectID,
 *    object_owner, ObjectSize, UserMetadata, AutoMetadata, user_permission,
 *    global_permission, shockurl, error]
 *
 * The spec declares 13 slots; WorkspaceImpl.pm's _generate_object_meta emits
 * the first 12. shockurl is slot 11 (slots 9 and 10 are permission letters —
 * issue #171) and the full path is slot 2 (the containing directory, with a
 * trailing slash) joined with slot 0.
 */
export interface WorkspaceObject {
  /** Full path: directory in slot [2] joined with the name in slot [0]. */
  path: string;
  /** Object name without the directory prefix (slot [0]). */
  name: string;
  type: string;
  /** Owner username (slot [5]). */
  owner: string;
  creation_time: string;
  id: string;
  size: number;
  user_metadata: Record<string, unknown>;
  auto_metadata: Record<string, unknown>;
  /** Calling user's permission on the owning workspace: o, w, r, a, p, n (slot [9]). */
  user_permission?: string;
  /** Workspace's global permission (slot [10]). */
  global_permission?: string;
  /** Full URL of the backing Shock node, when the object is Shock-backed (slot [11]). */
  shock_url?: string;
  /** Per-object error (slot [12]); only present on 13-slot tuples. */
  error?: string;
  /**
   * Object content, only from Workspace.get without metadata_only. For an
   * inline object this is the text itself; for a Shock-backed object it is
   * the Shock node URL (the service never inlines Shock content).
   */
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

    // Workspace.spec: get(...) returns (list<tuple<ObjectMeta,ObjectData>>) —
    // each entry is a [meta, data] pair, not the metadata tuple itself.
    const result = await this.callWorkspace("Workspace.get", [params]);
    const raw = (result as unknown[][][])?.[0] || [];
    return raw.map(parseWorkspaceGetEntry);
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

  /**
   * Map each requested path to its download URL. Paths without a URL
   * (folders, missing objects) are omitted from the result.
   *
   * Side effect on the service: the caller's token is persisted server-side
   * for the lifetime of the download link.
   */
  async workspaceGetDownloadUrl(paths: string[]): Promise<Record<string, string>> {
    const params = { objects: paths };
    const result = await this.callWorkspace("Workspace.get_download_url", [params]);
    return parseDownloadUrls(paths, result);
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

/**
 * Parse one Workspace ObjectMeta tuple (see the WorkspaceObject doc comment
 * for the slot layout). Tolerates both the 12-slot tuples the service emits
 * and the 13-slot layout the spec declares.
 */
export function parseWorkspaceObject(tuple: unknown[]): WorkspaceObject {
  const slot = (i: number): unknown => (tuple.length > i ? tuple[i] : undefined);
  const str = (i: number): string | undefined => {
    const v = slot(i);
    return v === undefined || v === null || v === "" ? undefined : String(v);
  };

  const name = str(0) ?? "";
  const directory = str(2) ?? "";

  const obj: WorkspaceObject = {
    path: joinWorkspacePath(directory, name),
    name,
    type: str(1) ?? "",
    owner: str(5) ?? "",
    creation_time: str(3) ?? "",
    id: str(4) ?? "",
    size: Number(slot(6)) || 0,
    user_metadata: (slot(7) as Record<string, unknown>) || {},
    auto_metadata: (slot(8) as Record<string, unknown>) || {},
  };

  const userPermission = str(9);
  if (userPermission !== undefined) obj.user_permission = userPermission;
  const globalPermission = str(10);
  if (globalPermission !== undefined) obj.global_permission = globalPermission;
  const shockUrl = str(11);
  if (shockUrl !== undefined) obj.shock_url = shockUrl;
  const error = str(12);
  if (error !== undefined) obj.error = error;

  return obj;
}

/**
 * Parse one [ObjectMeta, ObjectData] pair returned by Workspace.get. A bare
 * metadata tuple (older deployments) is accepted as well. With metadata_only
 * the pair has no data half, so `data` stays undefined.
 */
export function parseWorkspaceGetEntry(entry: unknown[]): WorkspaceObject {
  if (!Array.isArray(entry) || entry.length === 0) {
    return parseWorkspaceObject([]);
  }
  if (!Array.isArray(entry[0])) {
    // Not a [meta, data] pair — the entry is the metadata tuple itself.
    return parseWorkspaceObject(entry);
  }
  const obj = parseWorkspaceObject(entry[0] as unknown[]);
  if (entry.length > 1 && typeof entry[1] === "string") {
    obj.data = entry[1];
  }
  return obj;
}

/**
 * Parse the Workspace.get_download_url result: the JSON-RPC result wraps one
 * flat list of URLs in input order, with null for folders and missing objects
 * (`[[url1, url2, null, ...]]`). Entries without a URL are omitted.
 */
export function parseDownloadUrls(paths: string[], result: unknown): Record<string, string> {
  const urls = Array.isArray(result) && Array.isArray(result[0]) ? (result[0] as unknown[]) : [];
  const out: Record<string, string> = {};
  paths.forEach((path, i) => {
    const url = urls[i];
    if (typeof url === "string" && url !== "") {
      out[path] = url;
    }
  });
  return out;
}

/**
 * Join the directory slot of an ObjectMeta tuple with the object name. The
 * service emits the directory with a trailing slash; tolerate its absence.
 */
export function joinWorkspacePath(directory: string, name: string): string {
  if (!name) return directory;
  if (!directory) return name;
  return directory.endsWith("/") ? directory + name : `${directory}/${name}`;
}
