package bvbrc_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
)

const testToken = "un=tester@bvbrc|tokenid=1|expiry=9999999999|sig=deadbeef"

func newClient(f *bvbrctest.Server, token string) *bvbrc.Client {
	return bvbrc.NewClient(bvbrc.Config{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        token,
		Timeout:      10 * time.Second,
	}, nil)
}

// allBytesPayload returns a payload containing every byte value 0x00–0xFF.
func allBytesPayload() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func gzipPayload(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for i := 0; i < 64; i++ {
		fmt.Fprintf(zw, `{"chunk":%d,"text":"line %d"}`+"\n", i, i)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func randomPayload(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(42)).Read(b)
	return b
}

type createParams struct {
	Objects           [][]any `json:"objects"`
	Overwrite         bool    `json:"overwrite"`
	CreateUploadNodes bool    `json:"createUploadNodes"`
}

func decodeCreate(t *testing.T, c bvbrctest.Call) createParams {
	t.Helper()
	var p createParams
	if err := json.Unmarshal(c.Params, &p); err != nil {
		t.Fatalf("decoding Workspace.create params: %v", err)
	}
	return p
}

func objectsParam(t *testing.T, c bvbrctest.Call) []string {
	t.Helper()
	var p struct {
		Objects []string `json:"objects"`
	}
	if err := json.Unmarshal(c.Params, &p); err != nil {
		t.Fatalf("decoding %s params: %v", c.Method, err)
	}
	return p.Objects
}

func TestWorkspaceUploadReader_RoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "gzip bytes", payload: nil}, // filled in below
		{name: "every byte 0x00-0xFF", payload: allBytesPayload()},
		{name: "3 MiB streamed", payload: randomPayload(3 << 20)},
	}
	tests[0].payload = gzipPayload(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := bvbrctest.New(t)
			c := newClient(f, testToken)

			const dest = "/tester@bvbrc/home/out/blob.bin"
			obj, err := c.WorkspaceUploadReader(context.Background(), dest, "blob.bin",
				bytes.NewReader(tt.payload), int64(len(tt.payload)), bvbrc.WorkspaceTypeUnspecified)
			if err != nil {
				t.Fatalf("WorkspaceUploadReader: %v", err)
			}

			if obj.Path != dest {
				t.Errorf("object path = %q, want %q", obj.Path, dest)
			}
			if obj.Size != int64(len(tt.payload)) {
				t.Errorf("object size = %d, want %d (the refreshed metadata)", obj.Size, len(tt.payload))
			}

			stored := f.Bytes(dest)
			if sha256.Sum256(stored) != sha256.Sum256(tt.payload) {
				t.Fatalf("sha256 mismatch: stored %d bytes, sent %d", len(stored), len(tt.payload))
			}

			// The size check is only meaningful if the service was asked to
			// refresh its metadata for exactly this object.
			autoMeta := f.CallsTo("Workspace.update_auto_meta")
			if len(autoMeta) != 1 {
				t.Fatalf("update_auto_meta calls = %d, want 1", len(autoMeta))
			}
			if got := objectsParam(t, autoMeta[0]); len(got) != 1 || got[0] != dest {
				t.Errorf("update_auto_meta objects = %v, want [%s]", got, dest)
			}
			if len(f.CallsTo("Workspace.delete")) != 0 {
				t.Error("a successful upload must not delete anything")
			}
		})
	}
}

func TestWorkspaceUploadReader_ProtocolShape(t *testing.T) {
	f := bvbrctest.New(t)
	c := newClient(f, testToken)

	payload := allBytesPayload()
	const dest = "/tester@bvbrc/home/out/chunks.jsonl.gz"
	if _, err := c.WorkspaceUploadReader(context.Background(), dest, "chunks.jsonl.gz",
		bytes.NewReader(payload), int64(len(payload)), bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("WorkspaceUploadReader: %v", err)
	}

	creates := f.CallsTo("Workspace.create")
	if len(creates) != 1 {
		t.Fatalf("Workspace.create calls = %d, want 1", len(creates))
	}
	create := decodeCreate(t, creates[0])
	if !create.CreateUploadNodes {
		t.Error("Workspace.create did not set createUploadNodes")
	}
	if !create.Overwrite {
		t.Error("Workspace.create did not set overwrite")
	}
	if len(create.Objects) != 1 || len(create.Objects[0]) < 4 {
		t.Fatalf("Workspace.create objects = %v", create.Objects)
	}
	if create.Objects[0][3] != nil {
		t.Errorf("Workspace.create carried inline content %v, want null — bytes must go via Shock", create.Objects[0][3])
	}

	put, ok := f.LastPut()
	if !ok {
		t.Fatal("no Shock PUT received")
	}
	if put.Method != http.MethodPut {
		t.Errorf("Shock method = %s, want PUT", put.Method)
	}
	if put.FormField != "upload" {
		t.Errorf("Shock multipart field = %q, want \"upload\"", put.FormField)
	}
	if put.Filename != "chunks.jsonl.gz" {
		t.Errorf("Shock filename = %q, want the object basename (an empty filename is never sized by the service)", put.Filename)
	}
	if put.Authorization != "OAuth "+testToken {
		t.Errorf("Shock Authorization = %q, want \"OAuth <token>\"", put.Authorization)
	}
	if len(put.TransferEncoding) != 0 {
		t.Errorf("Shock request used Transfer-Encoding %v; the body must be sent with an exact Content-Length", put.TransferEncoding)
	}
	if put.ContentLength <= 0 || put.ContentLength != put.RawLength {
		t.Errorf("Content-Length = %d, body received = %d bytes; they must match exactly", put.ContentLength, put.RawLength)
	}
	if put.ContentLength <= int64(len(payload)) {
		t.Errorf("Content-Length = %d does not cover the %d-byte payload plus multipart framing", put.ContentLength, len(payload))
	}
}

// TestWorkspaceUploadReader_EmptyIsInline pins the 0-byte rule: no Shock node,
// no PUT, no metadata refresh — an empty text object created inline.
func TestWorkspaceUploadReader_EmptyIsInline(t *testing.T) {
	f := bvbrctest.New(t)
	c := newClient(f, testToken)

	const dest = "/tester@bvbrc/home/out/empty.txt"
	obj, err := c.WorkspaceUploadReader(context.Background(), dest, "empty.txt",
		bytes.NewReader(nil), 0, bvbrc.WorkspaceTypeUnspecified)
	if err != nil {
		t.Fatalf("WorkspaceUploadReader: %v", err)
	}
	if obj.Path != dest || obj.Size != 0 {
		t.Errorf("object = %+v, want path %s with size 0", obj, dest)
	}

	if puts := f.Puts(); len(puts) != 0 {
		t.Errorf("Shock PUTs = %d, want 0 for an empty payload", len(puts))
	}
	if n := len(f.CallsTo("Workspace.update_auto_meta")); n != 0 {
		t.Errorf("update_auto_meta calls = %d, want 0 for an empty payload", n)
	}

	creates := f.CallsTo("Workspace.create")
	if len(creates) != 1 {
		t.Fatalf("Workspace.create calls = %d, want 1", len(creates))
	}
	create := decodeCreate(t, creates[0])
	if create.CreateUploadNodes {
		t.Error("Workspace.create requested an upload node for an empty payload")
	}
	if !create.Overwrite {
		t.Error("Workspace.create did not set overwrite")
	}
	if got, ok := create.Objects[0][3].(string); !ok || got != "" {
		t.Errorf("inline content = %#v, want \"\"", create.Objects[0][3])
	}
	if stored := f.Object(dest); stored == nil || stored.NodeID != "" {
		t.Errorf("stored object = %+v, want an inline object", stored)
	}
}

// TestWorkspaceUploadReader_VerificationFailures covers every check between
// the Shock PUT and the returned object: each must fail loudly, return no
// object, and delete the placeholder so nothing half-written is left behind.
func TestWorkspaceUploadReader_VerificationFailures(t *testing.T) {
	payload := allBytesPayload()
	const dest = "/tester@bvbrc/home/out/blob.bin"

	envelope := func(size int, md5 string) string {
		file := map[string]any{"name": "blob.bin", "size": size}
		if md5 != "" {
			file["checksum"] = map[string]string{"md5": md5}
		}
		b, _ := json.Marshal(map[string]any{
			"status": 200, "error": nil,
			"data": map[string]any{"id": "node-001", "file": file},
		})
		return string(b)
	}

	tests := []struct {
		name      string
		configure func(f *bvbrctest.Server)
		wantErr   string
		wantPut   bool
	}{
		{
			name: "envelope size mismatch",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(p bvbrctest.ShockPut) (int, string) {
					return http.StatusOK, envelope(len(p.Body)-1, "")
				}
			},
			wantErr: "Shock stored 255 bytes, expected 256",
			wantPut: true,
		},
		{
			name: "envelope error field",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
					return http.StatusOK, `{"status":500,"error":["disk full"],"data":null}`
				}
			},
			wantErr: "disk full",
			wantPut: true,
		},
		{
			name: "envelope error as a string",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
					return http.StatusOK, `{"status":500,"error":"node locked","data":null}`
				}
			},
			wantErr: "node locked",
			wantPut: true,
		},
		{
			name: "md5 mismatch",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(p bvbrctest.ShockPut) (int, string) {
					return http.StatusOK, envelope(len(p.Body), "00000000000000000000000000000000")
				}
			},
			wantErr: "md5",
			wantPut: true,
		},
		{
			name: "http 500 from shock",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
					return http.StatusInternalServerError, "upstream unavailable"
				}
			},
			wantErr: "HTTP 500",
			wantPut: true,
		},
		{
			name: "unparseable reply",
			configure: func(f *bvbrctest.Server) {
				f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
					return http.StatusOK, "<html>gateway</html>"
				}
			},
			wantErr: "parsing Shock reply",
			wantPut: true,
		},
		{
			name: "update_auto_meta reports a different size",
			configure: func(f *bvbrctest.Server) {
				f.AutoMetaSize = func(_ string, recorded int64) int64 { return recorded + 7 }
			},
			wantErr: "Workspace recorded 263 bytes for " + dest + ", expected 256",
			wantPut: true,
		},
		{
			name: "update_auto_meta never sized the object",
			configure: func(f *bvbrctest.Server) {
				f.AutoMetaSize = func(string, int64) int64 { return 0 }
			},
			wantErr: "Workspace recorded 0 bytes",
			wantPut: true,
		},
		{
			name: "malformed shock url with empty node id",
			configure: func(f *bvbrctest.Server) {
				f.ShockURL = func(string) string { return f.BaseURL() + "/shock_api/node/" }
			},
			wantErr: "malformed Shock upload URL",
			wantPut: false,
		},
		{
			name: "empty shock url",
			configure: func(f *bvbrctest.Server) {
				f.ShockURL = func(string) string { return "" }
			},
			wantErr: "slot 11",
			wantPut: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := bvbrctest.New(t)
			tt.configure(f)
			c := newClient(f, testToken)

			obj, err := c.WorkspaceUploadReader(context.Background(), dest, "blob.bin",
				bytes.NewReader(payload), int64(len(payload)), bvbrc.WorkspaceTypeUnspecified)
			if err == nil {
				t.Fatal("expected an error")
			}
			if obj != nil {
				t.Errorf("returned object %+v alongside the error; must be nil", obj)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}

			if got := len(f.Puts()) > 0; got != tt.wantPut {
				t.Errorf("Shock PUT issued = %v, want %v", got, tt.wantPut)
			}

			deletes := f.CallsTo("Workspace.delete")
			if len(deletes) != 1 {
				t.Fatalf("Workspace.delete calls = %d, want 1 (placeholder cleanup)", len(deletes))
			}
			if got := objectsParam(t, deletes[0]); len(got) != 1 || got[0] != dest {
				t.Errorf("Workspace.delete objects = %v, want [%s]", got, dest)
			}
			if f.Object(dest) != nil {
				t.Error("placeholder object still exists after the failed upload")
			}
		})
	}
}

// A reply without a checksum is acceptable: the md5 is verified only when
// Shock reports one, the size always.
func TestWorkspaceUploadReader_NoChecksumInReply(t *testing.T) {
	f := bvbrctest.New(t)
	f.ShockReply = func(p bvbrctest.ShockPut) (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"status":200,"error":null,"data":{"id":%q,"file":{"name":%q,"size":%d}}}`,
			p.NodeID, p.Filename, len(p.Body))
	}
	c := newClient(f, testToken)

	payload := allBytesPayload()
	if _, err := c.WorkspaceUploadReader(context.Background(), "/tester@bvbrc/home/out/blob.bin", "blob.bin",
		bytes.NewReader(payload), int64(len(payload)), bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("WorkspaceUploadReader: %v", err)
	}
}

func TestWorkspaceUploadReader_RejectsBadFilename(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/", "a/b"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			f := bvbrctest.New(t)
			c := newClient(f, testToken)

			_, err := c.WorkspaceUploadReader(context.Background(), "/tester@bvbrc/home/out/x", name,
				bytes.NewReader([]byte{1}), 1, bvbrc.WorkspaceTypeUnspecified)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "filename") {
				t.Errorf("error = %v, want it to name the filename", err)
			}
			if n := len(f.Calls()); n != 0 {
				t.Errorf("%d calls reached the service; a bad filename must be rejected before any", n)
			}
		})
	}
}

// The declared size is a contract: a source that yields fewer or more bytes
// than declared must fail the request rather than upload something else.
func TestWorkspaceUploadReader_SizeMismatchWithSource(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{name: "source shorter than declared", size: 300},
		{name: "source longer than declared", size: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := bvbrctest.New(t)
			c := newClient(f, testToken)

			const dest = "/tester@bvbrc/home/out/blob.bin"
			_, err := c.WorkspaceUploadReader(context.Background(), dest, "blob.bin",
				bytes.NewReader(allBytesPayload()), tt.size, bvbrc.WorkspaceTypeUnspecified)
			if err == nil {
				t.Fatal("expected an error")
			}
			if len(f.CallsTo("Workspace.delete")) != 1 {
				t.Errorf("Workspace.delete calls = %d, want 1 (placeholder cleanup)", len(f.CallsTo("Workspace.delete")))
			}
			if f.Object(dest) != nil {
				t.Error("placeholder object still exists after the failed upload")
			}
		})
	}
}

func TestWorkspaceUploadFile_WrapsReader(t *testing.T) {
	f := bvbrctest.New(t)
	c := newClient(f, testToken)

	payload := gzipPayload(t)
	const dest = "/tester@bvbrc/home/out/vectors.gz"
	obj, err := c.WorkspaceUploadFile(context.Background(), dest, payload, bvbrc.WorkspaceTypeUnspecified)
	if err != nil {
		t.Fatalf("WorkspaceUploadFile: %v", err)
	}
	if obj.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", obj.Size, len(payload))
	}
	if !bytes.Equal(f.Bytes(dest), payload) {
		t.Error("stored bytes differ from the payload")
	}
	if put, _ := f.LastPut(); put.Filename != "vectors.gz" {
		t.Errorf("Shock filename = %q, want the path basename", put.Filename)
	}
}

// TestWorkspaceCreate_RejectsInvalidUTF8 pins the guard on the inline path.
// Before it, binary bytes handed to WorkspaceCreate.Content went through
// encoding/json, which replaces every invalid byte with U+FFFD — issue #172.
func TestWorkspaceCreate_RejectsInvalidUTF8(t *testing.T) {
	gzipish := append([]byte{0x1f, 0x8b, 0x08, 0x00}, allBytesPayload()...)

	tests := []struct {
		name       string
		payload    []byte
		wantReject bool
	}{
		{name: "every byte 0x00-0xFF", payload: allBytesPayload(), wantReject: true},
		{name: "gzip magic + binary", payload: gzipish, wantReject: true},
		{name: "float32 blob", payload: []byte{0x00, 0x00, 0x80, 0x3f, 0xcd, 0xcc, 0x4c, 0xbe}, wantReject: true},
		// Text is unaffected, which is why the corruption went unnoticed: the
		// JSON manifests round-tripped byte-exact while every binary sibling did not.
		{name: "ascii json manifest", payload: []byte(`{"files":[{"name":"a.txt","size":3}]}`), wantReject: false},
		{name: "utf-8 text", payload: []byte("größe – 20 µm\n"), wantReject: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := bvbrctest.New(t)
			c := newClient(f, testToken)

			content := string(tt.payload)
			const dest = "/tester@bvbrc/home/out/blob"
			_, err := c.WorkspaceCreate(context.Background(), bvbrc.WorkspaceCreateInput{
				Path:      dest,
				Type:      bvbrc.WorkspaceTypeUnspecified,
				Content:   &content,
				Overwrite: true,
			})

			if !tt.wantReject {
				if err != nil {
					t.Fatalf("WorkspaceCreate: %v", err)
				}
				if got := f.Object(dest); got == nil || got.Content != content {
					t.Fatalf("text payload was altered in transit: %+v", got)
				}
				return
			}

			if err == nil {
				t.Fatal("expected invalid UTF-8 content to be rejected")
			}
			if !strings.Contains(err.Error(), "UTF-8") {
				t.Errorf("error = %v, want it to name UTF-8", err)
			}
			if n := len(f.Calls()); n != 0 {
				t.Errorf("%d calls reached the service; the guard must fire before the request", n)
			}

			// Why the guard exists — the issue's arithmetic: each invalid byte
			// becomes a 3-byte U+FFFD, so the size grows by exactly 2 bytes per
			// replacement (105738 − 2×24261 = 57216 for the reported gzip).
			encoded, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			var decoded string
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			replacements := strings.Count(decoded, "�")
			if replacements == 0 {
				t.Fatal("expected U+FFFD replacement characters after a JSON round-trip")
			}
			if grew := len(decoded) - len(tt.payload); grew != 2*replacements {
				t.Errorf("size grew by %d bytes for %d replacements, want %d", grew, replacements, 2*replacements)
			}
		})
	}
}

func TestWorkspaceGetDownloadURL_MultiPathWithNull(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/out/a.bin", Type: "unspecified", NodeID: "node-a", Size: 3})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/out", Type: "folder"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/out/b.bin", Type: "unspecified", NodeID: "node-b", Size: 4})
	c := newClient(f, testToken)

	paths := []string{
		"/tester@bvbrc/home/out/a.bin",
		"/tester@bvbrc/home/out", // folder → null
		"/tester@bvbrc/home/out/missing.bin",
		"/tester@bvbrc/home/out/b.bin",
	}
	urls, err := c.WorkspaceGetDownloadURL(context.Background(), paths)
	if err != nil {
		t.Fatalf("WorkspaceGetDownloadURL: %v", err)
	}

	if got := urls[paths[0]]; got != f.BaseURL()+"/download/node-a" {
		t.Errorf("url[a.bin] = %q, want the first entry of the flat list", got)
	}
	if got := urls[paths[3]]; got != f.BaseURL()+"/download/node-b" {
		t.Errorf("url[b.bin] = %q, want the fourth entry of the flat list", got)
	}
	for _, p := range paths[1:3] {
		if got, ok := urls[p]; !ok || got != "" {
			t.Errorf("url[%s] = %q (present %v), want \"\" for a null entry", p, got, ok)
		}
	}
}

func TestWorkspaceUpdateAutoMeta(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/out", Type: "folder"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/out/a.bin", Type: "unspecified", NodeID: "node-a", Size: 3948608})
	c := newClient(f, testToken)

	obj, err := c.WorkspaceUpdateAutoMeta(context.Background(), "/tester@bvbrc/home/out/a.bin")
	if err != nil {
		t.Fatalf("WorkspaceUpdateAutoMeta: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/a.bin" || obj.Size != 3948608 {
		t.Errorf("object = %+v, want the refreshed ObjectMeta of a.bin", obj)
	}

	if _, err := c.WorkspaceUpdateAutoMeta(context.Background(), "/tester@bvbrc/home/out"); err == nil {
		t.Error("expected an error for a folder path")
	}
}
