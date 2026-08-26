package staging

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeWorkspaceService stands in for the Workspace JSON-RPC endpoint and the
// Shock node it hands out, so the whole stage-out path can be exercised over
// HTTP without touching a live service.
type fakeWorkspaceService struct {
	srv *httptest.Server

	mu sync.Mutex
	// Uploaded maps workspace destination path -> bytes received by Shock.
	Uploaded map[string][]byte
	// InlineContent maps destination path -> content sent inline via
	// Workspace.create (should stay empty for file stage-out).
	InlineContent map[string]string
	// Auth records the Authorization header of the last Shock PUT.
	Auth string
	// Method records the HTTP method of the last Shock PUT.
	Method string
	// FormField records the multipart field name of the last Shock PUT.
	FormField string

	nodes map[string]string // node id -> destination path
	seq   int
}

func newFakeWorkspaceService(t *testing.T) *fakeWorkspaceService {
	t.Helper()
	f := &fakeWorkspaceService{
		Uploaded:      map[string][]byte{},
		InlineContent: map[string]string{},
		nodes:         map[string]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWorkspaceService) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/shock_api/node/") {
		f.handleShock(w, r)
		return
	}

	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Method != "Workspace.create" || len(req.Params) == 0 {
		http.Error(w, "unexpected call "+req.Method, http.StatusNotImplemented)
		return
	}

	var params struct {
		Objects           [][]any `json:"objects"`
		CreateUploadNodes bool    `json:"createUploadNodes"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil || len(params.Objects) == 0 {
		http.Error(w, "bad params", http.StatusBadRequest)
		return
	}

	spec := params.Objects[0]
	destPath, _ := spec[0].(string)
	objType, _ := spec[1].(string)

	f.mu.Lock()
	if len(spec) > 3 {
		if s, ok := spec[3].(string); ok && s != "" {
			f.InlineContent[destPath] = s
		}
	}
	shockURL := ""
	if params.CreateUploadNodes {
		f.seq++
		id := "node-" + destPath
		f.nodes[id] = destPath
		shockURL = f.srv.URL + "/shock_api/node/" + id
	}
	f.mu.Unlock()

	dir, name := destPath, ""
	if i := strings.LastIndex(destPath, "/"); i >= 0 {
		dir, name = destPath[:i+1], destPath[i+1:]
	}

	// 12-slot ObjectMeta, as WorkspaceImpl.pm's _generate_object_meta emits it.
	meta := []any{
		name, objType, dir, "2026-08-20T12:00:00Z",
		"uuid-1", "tester@bvbrc", 0,
		map[string]any{}, map[string]any{},
		"o", "n", shockURL,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "1", "version": "1.1",
		"result": []any{[][]any{meta}},
	})
}

func (f *fakeWorkspaceService) handleShock(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/shock_api/node/")

	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(part)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.Uploaded[f.nodes[id]] = body
	f.Auth = r.Header.Get("Authorization")
	f.Method = r.Method
	f.FormField = part.FormName()
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":200}`))
}

func (f *fakeWorkspaceService) bytesAt(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Uploaded[path]
}

// TestWorkspaceStageOut_BinaryRoundTrip is the fix for #172: a stage-out of a
// payload containing every byte value must land byte-for-byte identical.
func TestWorkspaceStageOut_BinaryRoundTrip(t *testing.T) {
	payload := make([]byte, 0, 4096)
	for rep := 0; rep < 16; rep++ {
		for i := 0; i < 256; i++ {
			payload = append(payload, byte(i))
		}
	}
	want := sha256.Sum256(payload)

	src := filepath.Join(t.TempDir(), "chunks.jsonl.gz")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	f := newFakeWorkspaceService(t)
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.srv.URL + "/services/Workspace",
		Token:        "un=tester|sig=abc",
	}, nil)

	loc, err := stager.StageOut(context.Background(), src, "task-1", StageOptions{
		Metadata: map[string]string{"destination": "/tester@bvbrc/home/results/1"},
	})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	const dest = "/tester@bvbrc/home/results/1/chunks.jsonl.gz"
	if loc != "ws://"+dest {
		t.Errorf("StageOut location = %q, want %q", loc, "ws://"+dest)
	}

	stored := f.bytesAt(dest)
	if got := sha256.Sum256(stored); got != want {
		t.Fatalf("sha256 mismatch: stored %d bytes (%x), staged out %d bytes (%x)",
			len(stored), got, len(payload), want)
	}

	if len(f.InlineContent) != 0 {
		t.Errorf("file bytes were sent inline via Workspace.create for %v; they must go through Shock",
			mapKeys(f.InlineContent))
	}
	if f.Method != http.MethodPut {
		t.Errorf("Shock method = %s, want PUT", f.Method)
	}
	if f.FormField != "upload" {
		t.Errorf("Shock multipart field = %q, want \"upload\"", f.FormField)
	}
	if f.Auth != "OAuth un=tester|sig=abc" {
		t.Errorf("Shock Authorization = %q, want \"OAuth <token>\"", f.Auth)
	}
}

// TestWorkspaceUploadContent_GoesThroughShock covers the manifest path
// (_gowe_outputs.json), which is text but is routed identically so no caller is
// left on the JSON-inline path.
func TestWorkspaceUploadContent_GoesThroughShock(t *testing.T) {
	f := newFakeWorkspaceService(t)
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.srv.URL + "/services/Workspace",
		Token:        "un=tester",
	}, nil)

	const dest = "/tester@bvbrc/home/results/1/_gowe_outputs.json"
	content := `{"outputs":{"out":"ws:///tester@bvbrc/home/results/1/a.bin"}}`

	if _, err := stager.UploadContent(context.Background(), dest, content, StageOptions{}); err != nil {
		t.Fatalf("UploadContent: %v", err)
	}

	if got := string(f.bytesAt(dest)); got != content {
		t.Errorf("stored content = %q, want %q", got, content)
	}
	if len(f.InlineContent) != 0 {
		t.Errorf("content was sent inline via Workspace.create for %v", mapKeys(f.InlineContent))
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
