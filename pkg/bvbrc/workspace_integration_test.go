//go:build integration

package bvbrc_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
)

// TestWorkspaceUploadIntegration exercises the verified upload path against
// the production Workspace and Shock services. It needs a BV-BRC token
// (BVBRC_TOKEN or ~/.patric_token, see bvbrc.LoadToken) and skips without one:
//
//	BVBRC_TOKEN="$(cat ~/.patric_token)" go test -tags=integration -run TestWorkspaceUploadIntegration ./pkg/bvbrc/
//
// Everything is written under /<user>/home/.gowe-test/<uuid>/ and removed in
// cleanup. On its first run it records the real Shock PUT reply (token-free)
// to testdata/shock-put-reply.json as a reference fixture.
func TestWorkspaceUploadIntegration(t *testing.T) {
	token, err := bvbrc.LoadToken()
	if err != nil || token == "" {
		t.Skip("no BV-BRC token (set BVBRC_TOKEN or create ~/.patric_token)")
	}
	user := bvbrc.UsernameFromToken(token)
	if user == "" {
		t.Skip("BV-BRC token carries no username")
	}
	if parsed, err := bvbrc.ParseToken(token); err == nil && parsed.IsExpired() {
		t.Skip("BV-BRC token has expired")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := bvbrc.NewClient(bvbrc.DefaultConfig().WithToken(token), nil)

	base := "/" + user + "/home/.gowe-test"
	folder := base + "/" + uuidString(t)
	for _, dir := range []string{base, folder} {
		if _, err := c.WorkspaceCreateFolder(ctx, dir); err != nil && !bvbrc.IsExistsError(err) {
			t.Fatalf("create folder %s: %v", dir, err)
		}
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		if err := c.WorkspaceDelete(cctx, bvbrc.WorkspaceDeleteInput{
			Objects: []string{folder}, Force: true, DeleteDirectories: true,
		}); err != nil {
			t.Logf("cleanup of %s failed: %v", folder, err)
		}
	})
	t.Logf("test folder: %s", folder)

	payload := gzipRandom(t, 200<<10)
	local := filepath.Join(t.TempDir(), "chunks.jsonl.gz")
	if err := os.WriteFile(local, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("record shock put reply", func(t *testing.T) {
		recordShockPutReply(t, ctx, c, folder+"/fixture.gz", payload)
	})

	dest := folder + "/chunks.jsonl.gz"

	t.Run("upload verifies size", func(t *testing.T) {
		obj := uploadLocal(t, ctx, c, dest, local)
		if obj.Size != int64(len(payload)) {
			t.Fatalf("returned Size = %d, want %d", obj.Size, len(payload))
		}
		if obj.Path != dest {
			t.Errorf("returned Path = %q, want %q", obj.Path, dest)
		}
		assertLsSize(t, ctx, c, folder, "chunks.jsonl.gz", int64(len(payload)))
	})

	t.Run("download matches", func(t *testing.T) {
		got := downloadViaURL(t, ctx, c, dest)
		if sha256.Sum256(got) != sha256.Sum256(payload) {
			t.Fatalf("downloaded %d bytes differ from the %d uploaded", len(got), len(payload))
		}
	})

	t.Run("get returns shock url as data", func(t *testing.T) {
		objs, err := c.WorkspaceGet(ctx, bvbrc.WorkspaceGetInput{Objects: []string{dest}})
		if err != nil {
			t.Fatalf("WorkspaceGet: %v", err)
		}
		if len(objs) != 1 {
			t.Fatalf("WorkspaceGet returned %d objects, want 1", len(objs))
		}
		if !strings.Contains(objs[0].Data, "/node/") {
			t.Errorf("Data = %q, want the Shock node URL for a Shock-backed object", objs[0].Data)
		}
		if objs[0].ShockURL == "" || objs[0].ShockURL != objs[0].Data {
			t.Errorf("ShockURL = %q, Data = %q; want both to carry the node URL", objs[0].ShockURL, objs[0].Data)
		}
		if objs[0].Size != int64(len(payload)) {
			t.Errorf("Size = %d, want %d", objs[0].Size, len(payload))
		}
	})

	t.Run("overwrite same path", func(t *testing.T) {
		payload2 := gzipRandom(t, 64<<10)
		local2 := filepath.Join(t.TempDir(), "chunks.jsonl.gz")
		if err := os.WriteFile(local2, payload2, 0o644); err != nil {
			t.Fatal(err)
		}

		obj := uploadLocal(t, ctx, c, dest, local2)
		if obj.Size != int64(len(payload2)) {
			t.Fatalf("returned Size = %d, want %d after overwrite", obj.Size, len(payload2))
		}
		assertLsSize(t, ctx, c, folder, "chunks.jsonl.gz", int64(len(payload2)))

		got := downloadViaURL(t, ctx, c, dest)
		if sha256.Sum256(got) != sha256.Sum256(payload2) {
			t.Fatalf("downloaded %d bytes differ from the %d re-uploaded", len(got), len(payload2))
		}
	})

	t.Run("empty file inline", func(t *testing.T) {
		emptyDest := folder + "/empty.txt"
		obj, err := c.WorkspaceUploadReader(ctx, emptyDest, "empty.txt", bytes.NewReader(nil), 0, bvbrc.WorkspaceTypeUnspecified)
		if err != nil {
			t.Fatalf("WorkspaceUploadReader(empty): %v", err)
		}
		if obj.Size != 0 {
			t.Errorf("Size = %d, want 0", obj.Size)
		}
		assertLsSize(t, ctx, c, folder, "empty.txt", 0)
	})

	t.Run("download url for folder is empty", func(t *testing.T) {
		urls, err := c.WorkspaceGetDownloadURL(ctx, []string{dest, folder})
		if err != nil {
			t.Fatalf("WorkspaceGetDownloadURL: %v", err)
		}
		if urls[dest] == "" {
			t.Errorf("no download URL for %s", dest)
		}
		if urls[folder] != "" {
			t.Errorf("download URL for folder %s = %q, want \"\"", folder, urls[folder])
		}
	})
}

func uploadLocal(t *testing.T, ctx context.Context, c *bvbrc.Client, dest, local string) *bvbrc.WorkspaceObject {
	t.Helper()
	f, err := os.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	obj, err := c.WorkspaceUploadReader(ctx, dest, filepath.Base(local), f, st.Size(), bvbrc.WorkspaceTypeUnspecified)
	if err != nil {
		t.Fatalf("WorkspaceUploadReader: %v", err)
	}
	return obj
}

// assertLsSize proves the service recorded the size (update_auto_meta ran and
// the multipart filename rule was honoured): Workspace.ls reads it from the DB.
func assertLsSize(t *testing.T, ctx context.Context, c *bvbrc.Client, folder, name string, want int64) {
	t.Helper()
	listing, err := c.WorkspaceLs(ctx, bvbrc.WorkspaceLsInput{Paths: []string{folder}})
	if err != nil {
		t.Fatalf("WorkspaceLs: %v", err)
	}
	for _, obj := range listing[folder] {
		if obj.Name == name {
			if obj.Size != want {
				t.Fatalf("ls size of %s = %d, want %d", name, obj.Size, want)
			}
			return
		}
	}
	t.Fatalf("%s not listed in %s: %+v", name, folder, listing)
}

func downloadViaURL(t *testing.T, ctx context.Context, c *bvbrc.Client, dest string) []byte {
	t.Helper()
	urls, err := c.WorkspaceGetDownloadURL(ctx, []string{dest})
	if err != nil {
		t.Fatalf("WorkspaceGetDownloadURL: %v", err)
	}
	url := urls[dest]
	if url == "" {
		t.Fatalf("no download URL for %s", dest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "OAuth "+c.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d: %.200s", url, resp.StatusCode, body)
	}
	return body
}

// recordShockPutReply performs the raw protocol by hand (create with an
// upload node, multipart PUT) and stores Shock's reply as a fixture so the
// envelope the client parses is documented from a real exchange.
func recordShockPutReply(t *testing.T, ctx context.Context, c *bvbrc.Client, dest string, payload []byte) {
	t.Helper()
	obj, err := c.WorkspaceCreate(ctx, bvbrc.WorkspaceCreateInput{
		Path: dest, Type: bvbrc.WorkspaceTypeUnspecified, Overwrite: true, CreateUploadNodes: true,
	})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	if obj.ShockURL == "" {
		t.Fatal("no Shock URL in create reply")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("upload", filepath.Base(dest))
	if err != nil {
		t.Fatal(err)
	}
	part.Write(payload)
	mw.Close()

	// The body is deliberately non-replayable (a MultiReader hides the
	// bytes.Reader, so net/http sets no GetBody) and goes through the
	// library's own upload client: the exchange recorded here is the one the
	// client performs, including the fact that a PUT can never be silently
	// re-sent to a node whose file is already set (P6).
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, obj.ShockURL,
		io.NopCloser(io.MultiReader(bytes.NewReader(body.Bytes()))))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("request body is replayable; the recorded exchange must use a one-shot body")
	}
	req.ContentLength = int64(body.Len())
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "OAuth "+c.Token())
	resp, err := bvbrc.UploadHTTPClient(c).Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", obj.ShockURL, err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Shock PUT reply (HTTP %d): %s", resp.StatusCode, reply)

	if strings.Contains(string(reply), c.Token()) {
		t.Fatal("Shock reply contains the token; refusing to record it")
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, reply, "", "  "); err != nil {
		t.Fatalf("Shock reply is not JSON: %v", err)
	}
	pretty.WriteByte('\n')

	fixture := filepath.Join("testdata", "shock-put-reply.json")
	if _, err := os.Stat(fixture); err == nil && os.Getenv("BVBRC_RECORD_FIXTURES") == "" {
		t.Logf("fixture %s exists; set BVBRC_RECORD_FIXTURES=1 to re-record", fixture)
	} else {
		if err := os.MkdirAll(filepath.Dir(fixture), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s", fixture)
	}

	// The recorded reply must be one the client would accept.
	var env struct {
		Status int `json:"status"`
		Data   struct {
			File struct {
				Size int64 `json:"size"`
			} `json:"file"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.File.Size != int64(len(payload)) {
		t.Errorf("real Shock reported size %d, want %d", env.Data.File.Size, len(payload))
	}
}

func gzipRandom(t *testing.T, n int) []byte {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	return buf.Bytes()
}

func uuidString(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
