package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
	"github.com/me/gowe/pkg/model"
)

const (
	testWSUser  = "tester@bvbrc"
	testWSToken = "un=tester@bvbrc|tokenid=session-token-123|sig=abc"
)

// newWorkspaceUI builds a UI whose workspace calls go to the fake service.
func newWorkspaceUI(t *testing.T, fake *bvbrctest.Server) *UI {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(nil, logger, Config{WorkspaceURL: fake.WorkspaceURL()})
}

// withSession injects a session into the request the way AuthMiddleware does.
func withSession(r *http.Request, sess *model.Session) *http.Request {
	if sess == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), sessionContextKey, sess))
}

func testSession() *model.Session {
	return &model.Session{ID: "sess-1", UserID: testWSUser, Username: testWSUser, Role: "user", Token: testWSToken}
}

// uploadRequest builds a multipart POST like the file picker's upload form.
func uploadRequest(t *testing.T, folder, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if folder != "" {
		if err := mw.WriteField("folder", folder); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/workspace/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// allBytes is a payload covering every byte value, including 0x80-0xFF, which
// the old inline-JSON path used to mangle.
func allBytes(repeat int) []byte {
	out := make([]byte, 0, 256*repeat)
	for i := 0; i < repeat; i++ {
		for b := 0; b < 256; b++ {
			out = append(out, byte(b))
		}
	}
	return out
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json (body %q)", ct, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
}

func TestHandleWorkspaceUpload_RoundTrip(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)

	payload := allBytes(12) // 3 KiB, every byte value
	folder := "/" + testWSUser + "/home/inputs"
	req := withSession(uploadRequest(t, folder, "reads.fq.gz", payload), testSession())
	rec := httptest.NewRecorder()

	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	decodeJSON(t, rec, &resp)

	wantPath := folder + "/reads.fq.gz"
	if resp.Path != wantPath {
		t.Errorf("path = %q, want %q", resp.Path, wantPath)
	}
	if resp.Name != "reads.fq.gz" {
		t.Errorf("name = %q, want reads.fq.gz", resp.Name)
	}
	if resp.Type != "reads" {
		t.Errorf("type = %q, want reads", resp.Type)
	}
	if resp.Size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", resp.Size, len(payload))
	}

	// Bytes stored exactly as sent.
	if got := fake.Bytes(wantPath); !bytes.Equal(got, payload) {
		t.Errorf("stored %d bytes, want %d byte-exact", len(got), len(payload))
	}
	if obj := fake.Object(wantPath); obj == nil || obj.Size != int64(len(payload)) {
		t.Errorf("workspace object = %+v, want recorded size %d", obj, len(payload))
	}

	// The PUT was made as the session user, with the OAuth scheme, with a
	// filename and an exact Content-Length.
	put, ok := fake.LastPut()
	if !ok {
		t.Fatal("no Shock PUT recorded")
	}
	if put.Authorization != "OAuth "+testWSToken {
		t.Errorf("Authorization = %q, want the session token under the OAuth scheme", put.Authorization)
	}
	if put.FormField != "upload" {
		t.Errorf("form field = %q, want upload", put.FormField)
	}
	if put.Filename != "reads.fq.gz" {
		t.Errorf("filename = %q, want reads.fq.gz", put.Filename)
	}
	if put.ContentLength <= 0 || put.ContentLength != put.RawLength {
		t.Errorf("Content-Length = %d, raw body = %d; want an exact declared length", put.ContentLength, put.RawLength)
	}
	if len(put.TransferEncoding) != 0 {
		t.Errorf("Transfer-Encoding = %v, want none", put.TransferEncoding)
	}
	if calls := fake.CallsTo("Workspace.update_auto_meta"); len(calls) != 1 {
		t.Errorf("update_auto_meta calls = %d, want 1", len(calls))
	}
}

func TestHandleWorkspaceUpload_RequiresSession(t *testing.T) {
	tests := []struct {
		name string
		sess *model.Session
	}{
		{name: "no session", sess: nil},
		{name: "session without token", sess: &model.Session{ID: "s", Username: testWSUser}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := bvbrctest.New(t)
			u := newWorkspaceUI(t, fake)

			req := withSession(uploadRequest(t, "", "a.txt", []byte("hello")), tt.sess)
			rec := httptest.NewRecorder()
			u.HandleWorkspaceUpload(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			var resp map[string]string
			decodeJSON(t, rec, &resp)
			if resp["error"] == "" {
				t.Error("expected a JSON error message")
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("fake received %d calls without a session, want 0", n)
			}
			if _, ok := fake.LastPut(); ok {
				t.Error("a Shock PUT was made without a session")
			}
		})
	}
}

func TestHandleWorkspaceUpload_RetriesAfterShockFailure(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)

	// The first PUT fails; the retry must rewind the part and go through the
	// whole create → PUT → update_auto_meta protocol again on a fresh node.
	failed := false
	fake.ShockReply = func(put bvbrctest.ShockPut) (int, string) {
		if !failed {
			failed = true
			return http.StatusInternalServerError, `{"status":500,"error":["disk full"],"data":null}`
		}
		return 0, ""
	}

	payload := allBytes(4)
	folder := "/" + testWSUser + "/home"
	req := withSession(uploadRequest(t, folder, "blob.bin", payload), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	puts := fake.Puts()
	if len(puts) != 2 {
		t.Fatalf("Shock PUTs = %d, want 2 (one failure, one success)", len(puts))
	}
	if puts[0].NodeID == puts[1].NodeID {
		t.Error("retry reused the same Shock node; every attempt must re-create the object")
	}
	for i, p := range puts {
		if !bytes.Equal(p.Body, payload) {
			t.Errorf("PUT %d carried %d bytes, want the full %d-byte payload (rewind failed?)", i, len(p.Body), len(payload))
		}
	}
	if got := fake.Bytes(folder + "/blob.bin"); !bytes.Equal(got, payload) {
		t.Errorf("stored %d bytes, want %d byte-exact", len(got), len(payload))
	}
}

func TestHandleWorkspaceUpload_EmptyFile(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)

	folder := "/" + testWSUser + "/home"
	req := withSession(uploadRequest(t, folder, "empty.txt", nil), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, ok := fake.LastPut(); ok {
		t.Error("an empty file must be stored inline, not PUT to Shock")
	}
	obj := fake.Object(folder + "/empty.txt")
	if obj == nil || obj.NodeID != "" || obj.Size != 0 {
		t.Errorf("object = %+v, want an inline 0-byte object", obj)
	}
}

func seedListing(fake *bvbrctest.Server) {
	home := "/" + testWSUser + "/home"
	fake.Put(bvbrctest.Object{Path: home + "/sub", Type: "folder"})
	fake.Put(bvbrctest.Object{Path: home + "/reads.fq", Type: "reads", Size: 42})
	fake.Put(bvbrctest.Object{Path: home + "/sub/nested.txt", Type: "txt", Size: 7}) // not a direct child
}

func TestHandleWorkspaceAPI_FullPaths(t *testing.T) {
	fake := bvbrctest.New(t)
	seedListing(fake)
	u := newWorkspaceUI(t, fake)
	home := "/" + testWSUser + "/home"

	type item struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
		IsFolder bool   `json:"isFolder"`
	}
	var resp struct {
		Path  string `json:"path"`
		Items []item `json:"items"`
	}

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "explicit path", query: "?path=" + home},
		{name: "default path is the user's home", query: ""},
		{name: "trailing slash", query: "?path=" + home + "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := withSession(httptest.NewRequest(http.MethodGet, "/api/workspace/ls"+tc.query, nil), testSession())
			rec := httptest.NewRecorder()
			u.HandleWorkspaceAPI(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			resp.Items = nil
			decodeJSON(t, rec, &resp)

			if strings.TrimSuffix(resp.Path, "/") != home {
				t.Errorf("path = %q, want %q", resp.Path, home)
			}
			byName := map[string]item{}
			for _, it := range resp.Items {
				byName[it.Name] = it
			}
			if len(byName) != 2 {
				t.Fatalf("items = %+v, want exactly sub and reads.fq", resp.Items)
			}
			if sub := byName["sub"]; sub.Path != home+"/sub" || !sub.IsFolder || sub.Type != "folder" {
				t.Errorf("sub = %+v, want full path %s/sub and isFolder", sub, home)
			}
			if f := byName["reads.fq"]; f.Path != home+"/reads.fq" || f.IsFolder || f.Size != 42 || f.Type != "reads" {
				t.Errorf("reads.fq = %+v, want full path %s/reads.fq, size 42", f, home)
			}
		})
	}
}

func TestHandleWorkspaceAPI_RequiresSession(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/workspace/ls", nil)
	rec := httptest.NewRecorder()
	u.HandleWorkspaceAPI(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("fake received %d calls without a session, want 0", n)
	}
}

func TestHandleWorkspace_RendersTypedPaths(t *testing.T) {
	fake := bvbrctest.New(t)
	seedListing(fake)
	u := newWorkspaceUI(t, fake)
	home := "/" + testWSUser + "/home"

	req := withSession(httptest.NewRequest(http.MethodGet, "/workspace?path="+home, nil), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspace(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The folder link carries the object's full path, composed by the client
	// from the directory and name slots — no guessing in the template.
	if want := `href="/workspace?path=%2Ftester%40bvbrc%2Fhome%2Fsub"`; !strings.Contains(body, want) {
		t.Errorf("folder link %s missing from page:\n%s", want, body)
	}
	if !strings.Contains(body, ">sub<") {
		t.Error("folder name not rendered")
	}
	if !strings.Contains(body, ">reads.fq<") || !strings.Contains(body, ">reads<") {
		t.Error("file name/type not rendered")
	}
	if strings.Contains(body, "nested.txt") {
		t.Error("a nested object leaked into the parent listing")
	}
	if strings.Contains(body, "Empty directory") {
		t.Error("non-empty listing rendered as empty")
	}
}

func TestHandleWorkspace_RequiresSession(t *testing.T) {
	for _, tt := range []struct {
		name string
		sess *model.Session
	}{
		{name: "no session", sess: nil},
		{name: "session without token", sess: &model.Session{ID: "s", Username: testWSUser}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := bvbrctest.New(t)
			u := newWorkspaceUI(t, fake)

			rec := httptest.NewRecorder()
			u.HandleWorkspace(rec, withSession(httptest.NewRequest(http.MethodGet, "/workspace", nil), tt.sess))

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303 redirect to login", rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "/login" {
				t.Errorf("Location = %q, want /login", loc)
			}
			if n := len(fake.Calls()); n != 0 {
				t.Errorf("fake received %d calls without a session, want 0", n)
			}
		})
	}
}

func TestHandleWorkspaceUpload_TooLarge(t *testing.T) {
	fake := bvbrctest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(nil, logger, Config{WorkspaceURL: fake.WorkspaceURL(), UploadMaxSize: 1024})

	req := withSession(uploadRequest(t, "/"+testWSUser+"/home", "big.bin", allBytes(16)), testSession()) // 4 KiB
	rec := httptest.NewRecorder()
	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	if resp["error"] == "" {
		t.Error("expected a JSON error message")
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("fake received %d calls for an oversized upload, want 0", n)
	}
}

func TestHandleWorkspaceUpload_RejectsControlCharacters(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)

	req := withSession(uploadRequest(t, "/"+testWSUser+"/home", "evil\x01name.txt", []byte("x")), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("fake received %d calls for a rejected name, want 0", n)
	}
}

func TestHandleWorkspaceUpload_NoRetryOnPermissionError(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)
	fake.RPCError = func(method string, _ json.RawMessage) (int, string) {
		if method == "Workspace.create" {
			return -32401, "User tester@bvbrc does not have permission to write to /other@bvbrc/home"
		}
		return 0, ""
	}

	req := withSession(uploadRequest(t, "/other@bvbrc/home", "a.txt", []byte("hello")), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspaceUpload(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "permission") {
		t.Errorf("error body %q does not surface the service's message", rec.Body.String())
	}
	if n := len(fake.CallsTo("Workspace.create")); n != 1 {
		t.Errorf("Workspace.create calls = %d, want exactly 1 (no retry on a permission error)", n)
	}
	if _, ok := fake.LastPut(); ok {
		t.Error("a Shock PUT was made despite the create failing")
	}
}

func TestHandleWorkspaceAPI_EmptyDirectory(t *testing.T) {
	fake := bvbrctest.New(t)
	u := newWorkspaceUI(t, fake)
	fake.LsReply = func([]string) (map[string][][]any, bool) {
		return map[string][][]any{}, true
	}
	home := "/" + testWSUser + "/home"

	req := withSession(httptest.NewRequest(http.MethodGet, "/api/workspace/ls?path="+home+"/empty", nil), testSession())
	rec := httptest.NewRecorder()
	u.HandleWorkspaceAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an empty listing (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path  string `json:"path"`
		Items []any  `json:"items"`
	}
	decodeJSON(t, rec, &resp)
	if resp.Items == nil || len(resp.Items) != 0 {
		t.Errorf("items = %v, want an empty (non-null) list", resp.Items)
	}

	// The browser page renders it as an empty directory rather than an error.
	rec = httptest.NewRecorder()
	u.HandleWorkspace(rec, withSession(httptest.NewRequest(http.MethodGet, "/workspace?path="+home+"/empty", nil), testSession()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Empty directory") {
		t.Errorf("browser page status = %d, want 200 with 'Empty directory'", rec.Code)
	}
}
