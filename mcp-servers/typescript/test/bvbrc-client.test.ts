/**
 * Parser tests against the RECORDED Workspace responses shared with the Go
 * client (pkg/bvbrc/testdata/workspace/). The fixtures are raw JSON-RPC
 * `result` values captured from the production service, so these tests pin the
 * TypeScript parser to what the service actually emits — the same files the Go
 * (workspace_fixtures_test.go) parser is tested against.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  BVBRCClient,
  parseWorkspaceObject,
  parseWorkspaceGetEntry,
  parseDownloadUrls,
  joinWorkspacePath,
} from "../src/bvbrc-client.js";

const FIXTURE_DIR = new URL("../../../pkg/bvbrc/testdata/workspace/", import.meta.url);

const FOLDER = "/awilke@bvbrc/home/.gowe-fixtures/cd0bcb45-449e-4aa0-a8b0-88dff3c633a7";
const INLINE = `${FOLDER}/inline.txt`;
const SHOCK = `${FOLDER}/shock.txt`;
const NODE = "https://p3.theseed.org/services/shock_api/node/1c66310d-929b-4d5d-85dc-adfdf5c7007d";
const OWNER = "awilke@bvbrc";

function fixture(name: string): unknown {
  return JSON.parse(readFileSync(new URL(name, FIXTURE_DIR), "utf-8"));
}

/**
 * Build a client whose fetch replays one fixture as the JSON-RPC result of
 * every call, recording the request for assertions.
 */
function replayClient(name: string): { client: BVBRCClient; calls: Array<{ method: string; params: unknown[] }> } {
  const result = fixture(name);
  const calls: Array<{ method: string; params: unknown[] }> = [];
  const client = new BVBRCClient({ workspaceUrl: "http://replay.invalid/ws", token: "fixture-token" });

  // Every replayClient() call installs its own fetch; tests never share one.
  globalThis.fetch = (async (_url: unknown, init?: RequestInit) => {
    const headers = (init?.headers ?? {}) as Record<string, string>;
    assert.equal(headers["Authorization"], "fixture-token", "JSON-RPC sends the raw token");
    const req = JSON.parse(String(init?.body));
    calls.push({ method: req.method, params: req.params });
    return new Response(JSON.stringify({ id: req.id, version: "1.1", result }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;

  return { client, calls };
}

function assertInlineMeta(obj: ReturnType<typeof parseWorkspaceObject>) {
  assert.equal(obj.name, "inline.txt", "name = [0]");
  assert.equal(obj.path, INLINE, "path = [2] + [0]");
  assert.equal(obj.type, "txt");
  assert.equal(obj.owner, OWNER, "owner = [5]");
  assert.equal(obj.id, "1161B900-A0EF-11F1-ACAA-E63D5F7A854C");
  assert.equal(obj.size, 6, 'size = len("hello\\n")');
  assert.equal(typeof obj.size, "number");
  assert.equal(obj.user_permission, "o", "[9] is a permission letter");
  assert.equal(obj.global_permission, "n", "[10] is a permission letter");
  assert.equal(obj.shock_url, undefined, "inline object has no Shock URL");
  assert.equal(obj.error, undefined, "impl emits 12 slots — no error slot");
  assert.deepEqual(obj.auto_metadata, { is_folder: 0 });
}

function assertShockMeta(obj: ReturnType<typeof parseWorkspaceObject>) {
  assert.equal(obj.name, "shock.txt");
  assert.equal(obj.path, SHOCK, "path = [2] + [0]");
  assert.equal(obj.owner, OWNER, "owner = [5]");
  assert.equal(obj.size, 12, 'size = len("hello shock\\n")');
  assert.equal(obj.user_permission, "o");
  assert.equal(obj.global_permission, "n");
  assert.equal(obj.shock_url, NODE, "shock_url = [11]");
  assert.equal(obj.error, undefined);
}

test("fixture tuples have 12 slots with a numeric size", () => {
  const ls = fixture("ls.json") as Array<Record<string, unknown[][]>>;
  const tuples = ls[0][FOLDER];
  assert.equal(tuples.length, 2);
  for (const tuple of tuples) {
    assert.equal(tuple.length, 12, `${tuple[0]} has 12 slots`);
    assert.equal(typeof tuple[6], "number");
  }
});

test("parseWorkspaceObject: recorded ls tuples", () => {
  const ls = fixture("ls.json") as Array<Record<string, unknown[][]>>;
  const [inline, shock] = ls[0][FOLDER].map(parseWorkspaceObject);
  assertInlineMeta(inline);
  assertShockMeta(shock);
  assert.equal(inline.data, undefined, "ls never carries data");
  assert.equal(shock.data, undefined);
  assert.equal("shock_ref" in inline, false, "the fictional shock_ref field is gone");
});

test("parseWorkspaceObject: 13-slot tuple carries error, 9-slot tuple leaves optionals undefined", () => {
  const thirteen = parseWorkspaceObject([
    "x.txt", "txt", "/u@bvbrc/home/", "2026-01-01T00:00:00Z", "ID", "u@bvbrc", 1, {}, {}, "w", "r", "", "boom",
  ]);
  assert.equal(thirteen.error, "boom");
  assert.equal(thirteen.user_permission, "w");
  assert.equal(thirteen.global_permission, "r");
  assert.equal(thirteen.shock_url, undefined, "empty [11] → undefined");

  const nine = parseWorkspaceObject(["y", "folder", "/u@bvbrc/home", "t", "ID", "u@bvbrc", 0, {}, {}]);
  assert.equal(nine.path, "/u@bvbrc/home/y", "missing trailing slash is tolerated");
  assert.equal(nine.user_permission, undefined);
  assert.equal(nine.shock_url, undefined);
});

test("joinWorkspacePath", () => {
  assert.equal(joinWorkspacePath("/a/b/", "c"), "/a/b/c");
  assert.equal(joinWorkspacePath("/a/b", "c"), "/a/b/c");
  assert.equal(joinWorkspacePath("/a/b/", ""), "/a/b/");
  assert.equal(joinWorkspacePath("", "c"), "c");
});

test("workspaceLs replays the recorded listing", async () => {
  const { client, calls } = replayClient("ls.json");
  const got = await client.workspaceLs([FOLDER]);
  assert.equal(calls[0].method, "Workspace.ls");
  assert.deepEqual(calls[0].params, [{ paths: [FOLDER] }]);
  assert.deepEqual(Object.keys(got), [FOLDER]);
  assert.equal(got[FOLDER].length, 2);
  assertInlineMeta(got[FOLDER][0]);
  assertShockMeta(got[FOLDER][1]);
});

test("workspaceGet: [meta, data] pairs — inline content vs. Shock URL", async () => {
  const { client, calls } = replayClient("get.json");
  const got = await client.workspaceGet([INLINE, SHOCK]);
  assert.equal(calls[0].method, "Workspace.get");
  assert.deepEqual(calls[0].params, [{ objects: [INLINE, SHOCK] }], "metadata_only not sent by default");
  assert.equal(got.length, 2);

  assertInlineMeta(got[0]);
  assert.equal(got[0].data, "hello\n", "inline object: data is the content");

  assertShockMeta(got[1]);
  assert.equal(got[1].data, NODE, "Shock-backed object: data is the node URL, not the bytes");
});

test("workspaceGet with metadata_only yields no data", async () => {
  const { client, calls } = replayClient("get_metadata_only.json");
  const got = await client.workspaceGet([INLINE, SHOCK], true);
  assert.deepEqual(calls[0].params, [{ objects: [INLINE, SHOCK], metadata_only: true }]);
  assert.equal(got.length, 2);
  assertInlineMeta(got[0]);
  assertShockMeta(got[1]);
  assert.equal(got[0].data, undefined);
  assert.equal(got[1].data, undefined);
});

test("parseWorkspaceGetEntry accepts a bare metadata tuple", () => {
  const ls = fixture("ls.json") as Array<Record<string, unknown[][]>>;
  const bare = parseWorkspaceGetEntry(ls[0][FOLDER][1]);
  assertShockMeta(bare);
  assert.equal(bare.data, undefined);
});

test("workspaceCreate: inline object and upload-node replies", async () => {
  {
    const { client, calls } = replayClient("create_inline.json");
    const obj = await client.workspaceCreate(INLINE, "txt", "hello\n");
    assert.deepEqual(calls[0].params, [{ objects: [[INLINE, "txt", {}, "hello\n"]] }]);
    assertInlineMeta(obj);
  }
  {
    const { client } = replayClient("create_upload_node.json");
    const obj = await client.workspaceCreate(SHOCK, "txt", undefined, true);
    assert.equal(obj.path, SHOCK);
    assert.equal(obj.shock_url, NODE, "upload-node create returns the Shock URL in [11]");
    assert.equal(obj.size, 0, "nothing PUT yet");
  }
});

test("update_auto_meta reply is a refreshed ObjectMeta with the stored size", () => {
  const reply = fixture("update_auto_meta.json") as unknown[][][];
  assert.equal(reply.length, 1);
  assert.equal(reply[0].length, 1);
  const obj = parseWorkspaceObject(reply[0][0]);
  assertShockMeta(obj);
  assert.ok("inspection_started" in obj.auto_metadata);
});

test("get_download_url: flat list in input order, null for a folder", async () => {
  const raw = fixture("get_download_url.json") as unknown[];
  assert.equal(raw.length, 1, "wrapped exactly once by JSON-RPC");
  assert.equal((raw[0] as unknown[]).length, 3);
  assert.equal((raw[0] as unknown[])[2], null, "folder → null");

  const parsed = parseDownloadUrls([INLINE, SHOCK, FOLDER], raw);
  assert.deepEqual(Object.keys(parsed), [INLINE, SHOCK], "folder omitted");
  assert.ok(parsed[INLINE].endsWith("/inline.txt"));
  assert.ok(parsed[SHOCK].endsWith("/shock.txt"));

  const { client } = replayClient("get_download_url.json");
  const got = await client.workspaceGetDownloadUrl([INLINE, SHOCK, FOLDER]);
  assert.deepEqual(got, parsed);
  assert.equal(got[FOLDER], undefined);
});

test("fixtures contain no token material", () => {
  for (const name of [
    "ls.json",
    "get.json",
    "get_metadata_only.json",
    "get_download_url.json",
    "update_auto_meta.json",
    "create_inline.json",
    "create_upload_node.json",
    "shock_put_reply.json",
  ]) {
    const text = readFileSync(new URL(name, FIXTURE_DIR), "utf-8");
    assert.doesNotMatch(text, /un=|tokenid=|expiry=|sig=/, `${name} carries no token fields`);
  }
});
