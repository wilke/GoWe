# Recorded Workspace service responses

Every `*.json` file here is the raw JSON-RPC `result` (or, for the Shock PUT,
the raw HTTP reply body) captured from the production BV-BRC Workspace service
at `https://p3.theseed.org/services/Workspace`. Nothing is hand-written: they
pin the Go (`../../workspace_fixtures_test.go`), TypeScript
(`mcp-servers/typescript/test/bvbrc-client.test.ts`) and Python parsers to
what the service actually emits. Do not edit them by hand — re-record instead.

Recorded 2026-08-26 as user `awilke@bvbrc` in a scratch folder
`/awilke@bvbrc/home/.gowe-fixtures/<uuid>/` that was deleted afterwards. The
owner name, object UUIDs, Shock node id and download-link keys are real but
inert: the objects no longer exist, and the download links are bound to a token
that is not in the files.

| File | Call | What it shows |
|------|------|---------------|
| `create_inline.json` | `Workspace.create` of `inline.txt` with inline content `"hello\n"` | 12-slot ObjectMeta, size 6, empty `[11]` |
| `create_upload_node.json` | `Workspace.create` of `shock.txt` with `createUploadNodes:1, overwrite:1` | `[11]` is the Shock node URL, size 0 (nothing PUT yet) |
| `shock_put_reply.json` | Shock reply to the multipart `PUT` of 12 bytes to that node | `{status, data:{file:{size, checksum.md5}}, error}` envelope |
| `update_auto_meta.json` | `Workspace.update_auto_meta` `{objects:[shock.txt]}` | refreshed ObjectMeta with the stored size in `[6]` |
| `ls.json` | `Workspace.ls` `{paths:[folder]}` | `[{folder: [tuple, tuple]}]`, `[2]` is the directory with a trailing slash |
| `get.json` | `Workspace.get` `{objects:[inline.txt, shock.txt]}` | `[[[meta, data], ...]]` pairs; data is the text for the inline object and the **Shock URL** for the Shock-backed one |
| `get_metadata_only.json` | same with `metadata_only:1` | pairs with no data half |
| `get_download_url.json` | `Workspace.get_download_url` `{objects:[inline.txt, shock.txt, folder]}` | one flat list in input order, `null` for the folder |

## Capture procedure

Requirements: `curl`, `jq`, a BV-BRC token in `~/.patric_token`. JSON-RPC calls
send the raw token as the `Authorization` header; the Shock PUT sends
`Authorization: OAuth <token>`.

```bash
TOKEN="$(cat ~/.patric_token)"
USER="$(sed -n 's/^un=\([^|]*\).*/\1/p' ~/.patric_token)"
WS=https://p3.theseed.org/services/Workspace
FOLDER="/$USER/home/.gowe-fixtures/$(uuidgen)"

rpc() {  # rpc <method> <params-json>  → prints the raw `result`
  curl -sS -f -X POST "$WS" -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
    --data "{\"id\":\"1\",\"method\":\"$1\",\"version\":\"1.1\",\"params\":[$2]}" \
  | jq -e 'if .error then error(.error.message) else .result end'
}

# Folders, one level at a time (the service does not create intermediates).
rpc Workspace.create "{\"objects\":[[\"/$USER/home/.gowe-fixtures\",\"folder\",{},null]]}" >/dev/null || true
rpc Workspace.create "{\"objects\":[[\"$FOLDER\",\"folder\",{},null]]}" >/dev/null

# Inline text object.
rpc Workspace.create "{\"objects\":[[\"$FOLDER/inline.txt\",\"txt\",{},\"hello\\n\"]]}" > create_inline.json

# Shock-backed object: allocate the node, PUT the bytes, refresh the metadata.
rpc Workspace.create "{\"objects\":[[\"$FOLDER/shock.txt\",\"txt\",{},null]],\"createUploadNodes\":1,\"overwrite\":1}" > create_upload_node.json
NODE_URL="$(jq -r '.[0][0][11]' create_upload_node.json)"
printf 'hello shock\n' > shock.txt
curl -sS -f -X PUT "$NODE_URL" -H "Authorization: OAuth $TOKEN" \
  -F "upload=@shock.txt;filename=shock.txt" | jq . > shock_put_reply.json
rpc Workspace.update_auto_meta "{\"objects\":[\"$FOLDER/shock.txt\"]}" > update_auto_meta.json

# Captures.
rpc Workspace.ls "{\"paths\":[\"$FOLDER\"]}" > ls.json
rpc Workspace.get "{\"objects\":[\"$FOLDER/inline.txt\",\"$FOLDER/shock.txt\"]}" > get.json
rpc Workspace.get "{\"objects\":[\"$FOLDER/inline.txt\",\"$FOLDER/shock.txt\"],\"metadata_only\":1}" > get_metadata_only.json
rpc Workspace.get_download_url "{\"objects\":[\"$FOLDER/inline.txt\",\"$FOLDER/shock.txt\",\"$FOLDER\"]}" > get_download_url.json

# Cleanup: delete the LEAF folder first. Deleting only the top-level folder
# with force+deleteDirectories removed the parent row but left the uuid folder
# and its objects reachable by path when this was recorded.
rpc Workspace.delete "{\"objects\":[\"$FOLDER\"],\"force\":1,\"deleteDirectories\":1}"
rpc Workspace.delete "{\"objects\":[\"/$USER/home/.gowe-fixtures\"],\"force\":1,\"deleteDirectories\":1}"
rpc Workspace.ls "{\"paths\":[\"/$USER/home/.gowe-fixtures\"]}"   # expect [{}]
```

Notes:

- The multipart part must carry a non-empty `filename=`; without it the
  Workspace never records the size (`ObjectSize` stays 0).
- Call `update_auto_meta` **before** the `ls`/`get` captures, otherwise `[6]`
  of the Shock object can still read 0.
- Sanity-check before committing: `grep -l "un=\|tokenid=\|expiry=\|sig=" *.json` must
  print nothing. The download URLs carry an opaque key, not the token, but the
  service keeps the caller's token server-side for the link's lifetime.
- The Shock node created here is never deleted by the Workspace service
  (deleting the object orphans the node); that is a property of the service.
