package bvbrc_test

// Tests that replay RECORDED Workspace service responses through the exported
// client. The fixtures under testdata/workspace/ are the raw JSON-RPC `result`
// values captured from https://p3.theseed.org/services/Workspace (see the README
// there for the capture procedure); nothing in them is hand-written, so these
// tests pin the parser to what the production service actually emits — twelve
// ObjectMeta slots, directory-with-trailing-slash in [2], permission letters in
// [9]/[10], the Shock URL in [11], and [meta, data] pairs from Workspace.get.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
)

const (
	fixtureFolder = "/awilke@bvbrc/home/.gowe-fixtures/cd0bcb45-449e-4aa0-a8b0-88dff3c633a7"
	fixtureInline = fixtureFolder + "/inline.txt"
	fixtureShock  = fixtureFolder + "/shock.txt"
	fixtureNode   = "https://p3.theseed.org/services/shock_api/node/1c66310d-929b-4d5d-85dc-adfdf5c7007d"
	fixtureOwner  = "awilke@bvbrc"
)

// replayCall is what the replay server saw on the wire.
type replayCall struct {
	Method string
	Params []map[string]any
}

// newReplayClient serves one recorded fixture as the `result` of every JSON-RPC
// call and returns a client pointed at it plus a pointer to the last call seen.
func newReplayClient(t *testing.T, fixture string) (*bvbrc.Client, *replayCall) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "workspace", fixture))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", fixture, err)
	}
	if !json.Valid(raw) {
		t.Fatalf("fixture %s is not valid JSON", fixture)
	}

	last := &replayCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "fixture-token" {
			t.Errorf("Authorization header = %q, want the raw token", got)
		}
		var req struct {
			ID     string           `json:"id"`
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		last.Method = req.Method
		last.Params = req.Params

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      req.ID,
			"version": "1.1",
			"result":  json.RawMessage(raw),
		})
	}))
	t.Cleanup(srv.Close)

	cfg := bvbrc.Config{
		WorkspaceURL: srv.URL,
		Token:        "fixture-token",
		Timeout:      5 * time.Second,
	}
	return bvbrc.NewClient(cfg, nil), last
}

// assertInlineMeta checks the metadata fields shared by every fixture that
// carries the inline text object.
func assertInlineMeta(t *testing.T, obj bvbrc.WorkspaceObject) {
	t.Helper()
	if obj.Name != "inline.txt" {
		t.Errorf("Name = %q, want inline.txt", obj.Name)
	}
	if obj.Path != fixtureInline {
		t.Errorf("Path = %q, want [2]+[0] = %q", obj.Path, fixtureInline)
	}
	if obj.Type != bvbrc.WorkspaceObjectType("txt") {
		t.Errorf("Type = %q, want txt", obj.Type)
	}
	if obj.Owner != fixtureOwner {
		t.Errorf("Owner = %q, want [5] = %q", obj.Owner, fixtureOwner)
	}
	if obj.ID != "1161B900-A0EF-11F1-ACAA-E63D5F7A854C" {
		t.Errorf("ID = %q", obj.ID)
	}
	if obj.Size != 6 {
		t.Errorf("Size = %d, want 6 (len(\"hello\\n\"))", obj.Size)
	}
	if obj.UserPermission != "o" {
		t.Errorf("UserPermission = %q, want o", obj.UserPermission)
	}
	if obj.GlobalPermission != "n" {
		t.Errorf("GlobalPermission = %q, want n", obj.GlobalPermission)
	}
	if obj.ShockURL != "" {
		t.Errorf("ShockURL = %q, want empty for an inline object", obj.ShockURL)
	}
	if obj.Error != "" {
		t.Errorf("Error = %q, want empty (impl emits 12 slots)", obj.Error)
	}
	if obj.CreationTime.IsZero() {
		t.Error("CreationTime not parsed")
	}
	if obj.AutoMetadata["is_folder"] != "" {
		// is_folder is emitted as a JSON number; the string map drops it.
		t.Errorf("AutoMetadata[is_folder] = %q, want dropped (non-string)", obj.AutoMetadata["is_folder"])
	}
}

// assertShockMeta checks the metadata fields shared by every fixture that
// carries the Shock-backed object after update_auto_meta.
func assertShockMeta(t *testing.T, obj bvbrc.WorkspaceObject) {
	t.Helper()
	if obj.Name != "shock.txt" {
		t.Errorf("Name = %q, want shock.txt", obj.Name)
	}
	if obj.Path != fixtureShock {
		t.Errorf("Path = %q, want [2]+[0] = %q", obj.Path, fixtureShock)
	}
	if obj.Owner != fixtureOwner {
		t.Errorf("Owner = %q, want [5] = %q", obj.Owner, fixtureOwner)
	}
	if obj.Size != 12 {
		t.Errorf("Size = %d, want 12 (len(\"hello shock\\n\"))", obj.Size)
	}
	if obj.UserPermission != "o" || obj.GlobalPermission != "n" {
		t.Errorf("permissions = %q/%q, want o/n", obj.UserPermission, obj.GlobalPermission)
	}
	if obj.ShockURL != fixtureNode {
		t.Errorf("ShockURL = %q, want [11] = %q", obj.ShockURL, fixtureNode)
	}
}

func TestFixture_TwelveSlotTuples(t *testing.T) {
	// Every recorded ObjectMeta has exactly 12 elements: the implementation
	// omits the trailing `error` slot the spec declares.
	raw, err := os.ReadFile(filepath.Join("testdata", "workspace", "ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string][][]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	tuples := result[0][fixtureFolder]
	if len(tuples) != 2 {
		t.Fatalf("ls fixture has %d tuples, want 2", len(tuples))
	}
	for _, tuple := range tuples {
		if len(tuple) != 12 {
			t.Errorf("tuple %v has %d slots, want 12", tuple[0], len(tuple))
		}
		if _, ok := tuple[6].(float64); !ok {
			t.Errorf("slot [6] of %v is %T, want a JSON number", tuple[0], tuple[6])
		}
	}
}

func TestFixture_WorkspaceLs(t *testing.T) {
	client, call := newReplayClient(t, "ls.json")

	got, err := client.WorkspaceLs(context.Background(), bvbrc.WorkspaceLsInput{Paths: []string{fixtureFolder}})
	if err != nil {
		t.Fatalf("WorkspaceLs: %v", err)
	}
	if call.Method != "Workspace.ls" {
		t.Errorf("method = %q", call.Method)
	}

	objs := got[fixtureFolder]
	if len(objs) != 2 {
		t.Fatalf("got %d objects under %s, want 2 (keys: %v)", len(objs), fixtureFolder, keys(got))
	}
	assertInlineMeta(t, objs[0])
	assertShockMeta(t, objs[1])
	for _, o := range objs {
		if o.Data != "" {
			t.Errorf("%s: ls must not carry data, got %q", o.Name, o.Data)
		}
	}
}

func TestFixture_WorkspaceGet_PairsCarryData(t *testing.T) {
	client, call := newReplayClient(t, "get.json")

	got, err := client.WorkspaceGet(context.Background(), bvbrc.WorkspaceGetInput{Objects: []string{fixtureInline, fixtureShock}})
	if err != nil {
		t.Fatalf("WorkspaceGet: %v", err)
	}
	if call.Method != "Workspace.get" {
		t.Errorf("method = %q", call.Method)
	}
	if _, present := call.Params[0]["metadata_only"]; present {
		t.Error("metadata_only must not be sent for a default get")
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2", len(got))
	}

	// Inline object: the data half of the pair is the content itself.
	assertInlineMeta(t, got[0])
	if got[0].Data != "hello\n" {
		t.Errorf("inline Data = %q, want \"hello\\n\"", got[0].Data)
	}

	// Shock-backed object: the data half is the node URL, NOT the bytes. The
	// service never inlines Shock content; callers must fetch it themselves.
	assertShockMeta(t, got[1])
	if got[1].Data != fixtureNode {
		t.Errorf("shock Data = %q, want the Shock URL %q", got[1].Data, fixtureNode)
	}
}

func TestFixture_WorkspaceGet_MetadataOnly(t *testing.T) {
	client, call := newReplayClient(t, "get_metadata_only.json")

	got, err := client.WorkspaceGet(context.Background(), bvbrc.WorkspaceGetInput{
		Objects:      []string{fixtureInline, fixtureShock},
		MetadataOnly: true,
	})
	if err != nil {
		t.Fatalf("WorkspaceGet: %v", err)
	}
	if v, ok := call.Params[0]["metadata_only"].(bool); !ok || !v {
		t.Errorf("metadata_only = %v, want true", call.Params[0]["metadata_only"])
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2", len(got))
	}
	assertInlineMeta(t, got[0])
	assertShockMeta(t, got[1])
	for _, o := range got {
		if o.Data != "" {
			t.Errorf("%s: metadata_only must yield no data, got %q", o.Name, o.Data)
		}
	}
}

func TestFixture_CreateInline(t *testing.T) {
	client, _ := newReplayClient(t, "create_inline.json")

	content := "hello\n"
	obj, err := client.WorkspaceCreate(context.Background(), bvbrc.WorkspaceCreateInput{
		Path:    fixtureInline,
		Type:    bvbrc.WorkspaceObjectType("txt"),
		Content: &content,
	})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	assertInlineMeta(t, *obj)
}

func TestFixture_CreateUploadNode(t *testing.T) {
	// The create reply for an upload node carries the Shock URL in [11] and a
	// size of 0: nothing has been PUT yet.
	client, call := newReplayClient(t, "create_upload_node.json")

	obj, err := client.WorkspaceCreate(context.Background(), bvbrc.WorkspaceCreateInput{
		Path:              fixtureShock,
		Type:              bvbrc.WorkspaceObjectType("txt"),
		Overwrite:         true,
		CreateUploadNodes: true,
	})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	for _, flag := range []string{"overwrite", "createUploadNodes"} {
		if v, ok := call.Params[0][flag].(bool); !ok || !v {
			t.Errorf("%s = %v, want true", flag, call.Params[0][flag])
		}
	}
	if obj.Path != fixtureShock {
		t.Errorf("Path = %q, want %q", obj.Path, fixtureShock)
	}
	if obj.ShockURL != fixtureNode {
		t.Errorf("ShockURL = %q, want %q", obj.ShockURL, fixtureNode)
	}
	if obj.Size != 0 {
		t.Errorf("Size = %d, want 0 before the PUT", obj.Size)
	}
}

func TestFixture_UpdateAutoMeta_ReportsStoredSize(t *testing.T) {
	// update_auto_meta returns the refreshed ObjectMeta: [6] is the size Shock
	// recorded, which is the authoritative post-upload check. There is no typed
	// wrapper yet, so go through CallWorkspace and the same result shape as
	// create/copy/move: [[tuple, ...]].
	client, call := newReplayClient(t, "update_auto_meta.json")

	resp, err := client.CallWorkspace(context.Background(), "Workspace.update_auto_meta", map[string]any{
		"objects": []string{fixtureShock},
	})
	if err != nil {
		t.Fatalf("update_auto_meta: %v", err)
	}
	if call.Method != "Workspace.update_auto_meta" {
		t.Errorf("method = %q", call.Method)
	}

	var result [][][]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 || len(result[0]) != 1 {
		t.Fatalf("result shape %v, want [[tuple]]", result)
	}
	tuple := result[0][0]
	if len(tuple) != 12 {
		t.Errorf("tuple has %d slots, want 12", len(tuple))
	}
	if size, _ := tuple[6].(float64); size != 12 {
		t.Errorf("[6] = %v, want 12", tuple[6])
	}
	if tuple[11] != fixtureNode {
		t.Errorf("[11] = %v, want %q", tuple[11], fixtureNode)
	}
	auto, _ := tuple[8].(map[string]any)
	if _, ok := auto["inspection_started"]; !ok {
		t.Errorf("AutoMetadata after refresh = %v, want inspection_started", auto)
	}
}

func TestFixture_GetDownloadURL_Shape(t *testing.T) {
	// Pin the recorded wire shape: the JSON-RPC result wraps ONE flat list of
	// URLs in input order, with JSON null for a folder. Parsing through the
	// typed client is covered where WorkspaceGetDownloadURL is fixed; here we
	// only assert the shape callers must expect.
	//
	// TODO(rebase onto fix/workspace-upload-verify): add
	// TestFixture_WorkspaceGetDownloadURL replaying get_download_url.json through
	// client.WorkspaceGetDownloadURL and asserting
	// {inline: <url>, shock: <url>, folder: ""}. It cannot be pre-written here:
	// main's implementation maps raw[i][0] and would fail on this fixture.
	raw, err := os.ReadFile(filepath.Join("testdata", "workspace", "get_download_url.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result [][]*string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("get_download_url is not [[url|null, ...]]: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("outer list has %d entries, want exactly 1 (one wrap by JSON-RPC)", len(result))
	}
	urls := result[0]
	if len(urls) != 3 {
		t.Fatalf("got %d urls, want 3 (inline, shock, folder)", len(urls))
	}
	if urls[0] == nil || path.Base(*urls[0]) != "inline.txt" {
		t.Errorf("urls[0] = %v, want a download URL ending in inline.txt", deref(urls[0]))
	}
	if urls[1] == nil || path.Base(*urls[1]) != "shock.txt" {
		t.Errorf("urls[1] = %v, want a download URL ending in shock.txt", deref(urls[1]))
	}
	if urls[2] != nil {
		t.Errorf("urls[2] = %q, want null for a folder", *urls[2])
	}
}

func keys(m map[string][]bvbrc.WorkspaceObject) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
