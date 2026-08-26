package bvbrc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWorkspace is an httptest-backed stand-in for the Workspace JSON-RPC
// endpoint plus the Shock node endpoint it hands out. It records exactly what
// each side received so tests can assert on the wire protocol, not just on the
// happy path.
type fakeWorkspace struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex

	// CreateCalls holds the decoded "objects" entry of every Workspace.create.
	CreateCalls []createCall

	// ShockPuts holds the body of every multipart PUT, keyed by node id.
	ShockPuts map[string][]byte
	// ShockMeta records the request shape of the last Shock upload.
	ShockMeta shockRequest

	nextNode int
}

type createCall struct {
	Path              string
	Type              string
	Content           any
	CreateUploadNodes bool
	Overwrite         bool
}

type shockRequest struct {
	Method        string
	Authorization string
	FormField     string
	Filename      string
	ContentLength int64
}

func newFakeWorkspace(t *testing.T) *fakeWorkspace {
	t.Helper()
	f := &fakeWorkspace{t: t, ShockPuts: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWorkspace) URL() string { return f.srv.URL + "/services/Workspace" }

func (f *fakeWorkspace) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/shock_api/node/") {
		f.handleShock(w, r)
		return
	}
	f.handleRPC(w, r)
}

func (f *fakeWorkspace) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Method != "Workspace.create" {
		http.Error(w, "unexpected method "+req.Method, http.StatusNotImplemented)
		return
	}

	var params struct {
		Objects           [][]any `json:"objects"`
		CreateUploadNodes bool    `json:"createUploadNodes"`
		Overwrite         bool    `json:"overwrite"`
	}
	if len(req.Params) == 0 {
		http.Error(w, "no params", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(params.Objects) == 0 || len(params.Objects[0]) < 2 {
		http.Error(w, "no objects", http.StatusBadRequest)
		return
	}

	spec := params.Objects[0]
	call := createCall{
		CreateUploadNodes: params.CreateUploadNodes,
		Overwrite:         params.Overwrite,
	}
	call.Path, _ = spec[0].(string)
	call.Type, _ = spec[1].(string)
	if len(spec) > 3 {
		call.Content = spec[3]
	}

	f.mu.Lock()
	f.CreateCalls = append(f.CreateCalls, call)
	shockURL := ""
	if params.CreateUploadNodes {
		f.nextNode++
		shockURL = f.srv.URL + "/shock_api/node/" + nodeID(f.nextNode)
	}
	f.mu.Unlock()

	// A 12-slot ObjectMeta, shaped exactly like WorkspaceImpl.pm's
	// _generate_object_meta: it stops at shockurl and omits the error slot.
	dir, name := splitLast(call.Path)
	meta := []any{
		name,                            // 0 ObjectName
		call.Type,                       // 1 ObjectType
		dir,                             // 2 FullObjectPath (directory, trailing /)
		"2026-08-20T12:00:00Z",          // 3 creation_time
		"11111111-2222-3333-4444-5555",  // 4 ObjectID
		"someuser@bvbrc",                // 5 object_owner
		0,                               // 6 ObjectSize
		map[string]any{},                // 7 UserMetadata
		map[string]any{"is_folder": ""}, // 8 AutoMetadata
		"o",                             // 9 user_permission
		"n",                             // 10 global_permission
		shockURL,                        // 11 shockurl
	}

	writeRPCResult(w, [][]any{meta})
}

func (f *fakeWorkspace) handleShock(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/shock_api/node/")

	rec := shockRequest{
		Method:        r.Method,
		Authorization: r.Header.Get("Authorization"),
		ContentLength: r.ContentLength,
	}

	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "bad content type: "+err.Error(), http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		http.Error(w, "no multipart part: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec.FormField = part.FormName()
	rec.Filename = part.FileName()
	body, err := io.ReadAll(part)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.ShockPuts[id] = body
	f.ShockMeta = rec
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":200,"data":{"id":"` + id + `"}}`))
}

// LastShockUpload returns the bytes of the most recent Shock PUT.
func (f *fakeWorkspace) LastShockUpload() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ShockPuts[nodeID(f.nextNode)]
}

func writeRPCResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "1",
		"version": "1.1",
		"result":  []any{result},
	})
}

func nodeID(n int) string {
	return "node-" + string(rune('a'+n-1))
}

func splitLast(p string) (dir, name string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i+1], p[i+1:]
}

// allBytesPayload returns a payload containing every byte value 0x00–0xFF.
func allBytesPayload() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func testClient(f *fakeWorkspace, token string) *Client {
	return NewClient(Config{
		WorkspaceURL: f.URL(),
		Token:        token,
		Timeout:      10 * time.Second,
	}, nil)
}

func TestWorkspaceUploadFile_BinaryRoundTrip(t *testing.T) {
	f := newFakeWorkspace(t)
	c := testClient(f, "un=tester|sig=deadbeef")

	payload := allBytesPayload()
	want := sha256.Sum256(payload)

	obj, err := c.WorkspaceUploadFile(context.Background(),
		"/tester@bvbrc/home/out/vectors.f32", payload, WorkspaceTypeUnspecified)
	if err != nil {
		t.Fatalf("WorkspaceUploadFile: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/vectors.f32" {
		t.Errorf("object path = %q, want the full destination path", obj.Path)
	}

	got := sha256.Sum256(f.LastShockUpload())
	if got != want {
		t.Fatalf("sha256 mismatch: stored %x, uploaded %x (%d bytes stored, %d sent)",
			got, want, len(f.LastShockUpload()), len(payload))
	}
}

func TestWorkspaceUploadFile_ProtocolShape(t *testing.T) {
	f := newFakeWorkspace(t)
	c := testClient(f, "un=tester|sig=deadbeef")

	if _, err := c.WorkspaceUploadFile(context.Background(),
		"/tester@bvbrc/home/out/chunks.jsonl.gz", allBytesPayload(), WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("WorkspaceUploadFile: %v", err)
	}

	if len(f.CreateCalls) != 1 {
		t.Fatalf("Workspace.create calls = %d, want 1", len(f.CreateCalls))
	}
	create := f.CreateCalls[0]
	if !create.CreateUploadNodes {
		t.Error("Workspace.create did not set createUploadNodes")
	}
	if !create.Overwrite {
		t.Error("Workspace.create did not preserve overwrite semantics")
	}
	if create.Content != nil {
		t.Errorf("Workspace.create carried inline content %v, want null — bytes must go via Shock", create.Content)
	}

	// ws-create.pl: PUT, multipart field "upload", Authorization: OAuth <token>.
	if f.ShockMeta.Method != http.MethodPut {
		t.Errorf("Shock method = %s, want PUT", f.ShockMeta.Method)
	}
	if f.ShockMeta.FormField != "upload" {
		t.Errorf("Shock multipart field = %q, want \"upload\"", f.ShockMeta.FormField)
	}
	if f.ShockMeta.Filename != "chunks.jsonl.gz" {
		t.Errorf("Shock filename = %q, want the object basename", f.ShockMeta.Filename)
	}
	if f.ShockMeta.Authorization != "OAuth un=tester|sig=deadbeef" {
		t.Errorf("Shock Authorization = %q, want \"OAuth <token>\"", f.ShockMeta.Authorization)
	}
	if f.ShockMeta.ContentLength <= 0 {
		t.Error("Shock request had no Content-Length; a chunked body is not verified to work")
	}
}

// TestWorkspaceUploadFile_NoShockURLIsAnError pins the decision that an absent
// Shock URL fails loudly rather than silently falling back to the inline path,
// which would corrupt binary content.
func TestWorkspaceUploadFile_NoShockURLIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := []any{
			"f.bin", "unspecified", "/tester@bvbrc/home/", "2026-08-20T12:00:00Z",
			"id", "tester@bvbrc", 0, map[string]any{}, map[string]any{}, "o", "n", "",
		}
		writeRPCResult(w, [][]any{meta})
	}))
	defer srv.Close()

	c := NewClient(Config{WorkspaceURL: srv.URL, Token: "t", Timeout: 5 * time.Second}, nil)
	_, err := c.WorkspaceUploadFile(context.Background(), "/tester@bvbrc/home/f.bin", []byte{0xff}, WorkspaceTypeUnspecified)
	if err == nil {
		t.Fatal("expected an error when the create response carries no Shock URL")
	}
	if !strings.Contains(err.Error(), "slot 11") {
		t.Errorf("error = %v, want it to name the empty ObjectMeta slot", err)
	}
}

// TestJSONInlineContentCorruptsBinary is the regression pin for #172: it drives
// the *old* inline path — file bytes handed to encoding/json as a string — and
// asserts the exact corruption the issue documented.
func TestJSONInlineContentCorruptsBinary(t *testing.T) {
	gzipish := append([]byte{0x1f, 0x8b, 0x08, 0x00}, allBytesPayload()...)

	tests := []struct {
		name        string
		payload     []byte
		wantCorrupt bool
	}{
		{name: "every byte 0x00-0xFF", payload: allBytesPayload(), wantCorrupt: true},
		{name: "gzip magic + binary", payload: gzipish, wantCorrupt: true},
		{name: "float32 blob", payload: []byte{0x00, 0x00, 0x80, 0x3f, 0xcd, 0xcc, 0x4c, 0xbe}, wantCorrupt: true},
		// Text is unaffected, which is why the corruption went unnoticed: the
		// JSON manifests round-tripped byte-exact while every binary sibling did not.
		{name: "ascii json manifest", payload: []byte(`{"files":[{"name":"a.txt","size":3}]}`), wantCorrupt: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeWorkspace(t)
			c := testClient(f, "un=tester")

			// This is precisely what the removed WorkspaceUpload did.
			content := string(tt.payload)
			if _, err := c.WorkspaceCreate(context.Background(), WorkspaceCreateInput{
				Path:      "/tester@bvbrc/home/out/blob",
				Type:      WorkspaceTypeUnspecified,
				Content:   &content,
				Overwrite: true,
			}); err != nil {
				t.Fatalf("WorkspaceCreate: %v", err)
			}

			received, ok := f.CreateCalls[0].Content.(string)
			if !ok {
				t.Fatalf("server received content of type %T, want string", f.CreateCalls[0].Content)
			}
			got := []byte(received)

			if !tt.wantCorrupt {
				if sha256.Sum256(got) != sha256.Sum256(tt.payload) {
					t.Fatalf("text payload was altered in transit: %q vs %q", got, tt.payload)
				}
				return
			}

			if sha256.Sum256(got) == sha256.Sum256(tt.payload) {
				t.Fatal("inline JSON round-trip preserved binary bytes; the regression pin is no longer meaningful")
			}
			replacements := strings.Count(received, "�")
			if replacements == 0 {
				t.Fatal("expected U+FFFD replacement characters in the round-tripped content")
			}
			// The issue's arithmetic: each invalid byte becomes a 3-byte U+FFFD,
			// so the stored size grows by exactly 2 bytes per replacement —
			// 105738 − 2×24261 = 57216 for the reported gzip.
			if grew := len(got) - len(tt.payload); grew != 2*replacements {
				t.Errorf("size grew by %d bytes for %d replacements, want %d",
					grew, replacements, 2*replacements)
			}
		})
	}
}

func TestParseWorkspaceObjectTuple_SpecLayout(t *testing.T) {
	// Shaped like WorkspaceImpl.pm's _generate_object_meta: 12 slots, the
	// directory in [2] with a trailing slash, permissions in [9]/[10], the Shock
	// URL in [11].
	tuple := []any{
		"vectors.f32",
		"unspecified",
		"/tester@bvbrc/home/out/",
		"2026-08-20T12:00:00Z",
		"1a2b3c4d-0000-1111-2222-333344445555",
		"tester@bvbrc",
		float64(3948608),
		map[string]any{"run": "42"},
		map[string]any{"is_folder": "0"},
		"o",
		"n",
		"https://example.invalid/services/shock_api/node/abc-123",
	}

	obj, err := parseWorkspaceObjectTuple(tuple)
	if err != nil {
		t.Fatalf("parseWorkspaceObjectTuple: %v", err)
	}

	if obj.Name != "vectors.f32" {
		t.Errorf("Name = %q, want slot [0]", obj.Name)
	}
	if obj.Path != "/tester@bvbrc/home/out/vectors.f32" {
		t.Errorf("Path = %q, want slot [2] joined with slot [0]", obj.Path)
	}
	if obj.Type != WorkspaceTypeUnspecified {
		t.Errorf("Type = %q, want slot [1]", obj.Type)
	}
	if obj.Owner != "tester@bvbrc" {
		t.Errorf("Owner = %q, want slot [5] — not the path in slot [2]", obj.Owner)
	}
	if obj.Size != 3948608 {
		t.Errorf("Size = %d, want slot [6]", obj.Size)
	}
	if obj.UserMetadata["run"] != "42" {
		t.Errorf("UserMetadata = %v, want slot [7]", obj.UserMetadata)
	}
	if obj.UserPermission != "o" || obj.GlobalPermission != "n" {
		t.Errorf("permissions = %q/%q, want slots [9]/[10]", obj.UserPermission, obj.GlobalPermission)
	}
	if obj.ShockURL != "https://example.invalid/services/shock_api/node/abc-123" {
		t.Errorf("ShockURL = %q, want slot [11] — slot [10] is the global permission", obj.ShockURL)
	}
	if obj.CreationTime.IsZero() {
		t.Error("CreationTime not parsed from slot [3]")
	}
	if obj.Error != "" {
		t.Errorf("Error = %q, want empty for a 12-slot tuple", obj.Error)
	}
}

func TestParseWorkspaceObjectTuple_ThirteenSlotsCarryError(t *testing.T) {
	tuple := []any{
		"x", "unspecified", "/tester@bvbrc/home/", "2026-08-20T12:00:00Z",
		"id", "tester@bvbrc", float64(0), map[string]any{}, map[string]any{},
		"o", "n", "", "permission denied",
	}
	obj, err := parseWorkspaceObjectTuple(tuple)
	if err != nil {
		t.Fatalf("parseWorkspaceObjectTuple: %v", err)
	}
	if obj.Error != "permission denied" {
		t.Errorf("Error = %q, want slot [12]", obj.Error)
	}
}

func TestParseWorkspaceGetEntry_MetaDataPair(t *testing.T) {
	// Workspace.spec: get(...) returns list<tuple<ObjectMeta,ObjectData>>.
	meta := []any{
		"manifest.json", "json", "/tester@bvbrc/home/out/", "2026-08-20T12:00:00Z",
		"id-1", "tester@bvbrc", float64(1006), map[string]any{}, map[string]any{},
		"o", "n", "",
	}
	entry := []any{meta, `{"files":[]}`}

	obj, err := parseWorkspaceGetEntry(entry)
	if err != nil {
		t.Fatalf("parseWorkspaceGetEntry: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/manifest.json" {
		t.Errorf("Path = %q, want the path from the meta half of the pair", obj.Path)
	}
	if obj.Data != `{"files":[]}` {
		t.Errorf("Data = %q, want the ObjectData half of the pair", obj.Data)
	}
	if obj.Type != WorkspaceTypeJSON {
		t.Errorf("Type = %q; the meta half was not parsed as ObjectMeta", obj.Type)
	}
}

func TestParseWorkspaceGetEntry_BareMetaTuple(t *testing.T) {
	// Defensive: a bare metadata tuple (no data half) must still parse.
	meta := []any{
		"manifest.json", "json", "/tester@bvbrc/home/out/", "2026-08-20T12:00:00Z",
		"id-1", "tester@bvbrc", float64(1006), map[string]any{}, map[string]any{},
		"o", "n", "",
	}
	obj, err := parseWorkspaceGetEntry(meta)
	if err != nil {
		t.Fatalf("parseWorkspaceGetEntry: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/manifest.json" {
		t.Errorf("Path = %q", obj.Path)
	}
}
