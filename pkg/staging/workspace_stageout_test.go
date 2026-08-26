package staging

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
)

func allBytesRepeated(reps int) []byte {
	payload := make([]byte, 0, 256*reps)
	for rep := 0; rep < reps; rep++ {
		for i := 0; i < 256; i++ {
			payload = append(payload, byte(i))
		}
	}
	return payload
}

func writeTemp(t *testing.T, name string, payload []byte) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// inlineContent returns the inline content of every Workspace.create call
// that carried one, keyed by destination path.
func inlineContent(t *testing.T, f *bvbrctest.Server) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range f.CallsTo("Workspace.create") {
		var p struct {
			Objects [][]any `json:"objects"`
		}
		if err := json.Unmarshal(c.Params, &p); err != nil {
			t.Fatal(err)
		}
		for _, spec := range p.Objects {
			if len(spec) > 3 {
				if s, ok := spec[3].(string); ok && s != "" {
					path, _ := spec[0].(string)
					out[path] = s
				}
			}
		}
	}
	return out
}

// TestWorkspaceStageOut_BinaryRoundTrip is the fix for #172: a stage-out of a
// payload containing every byte value must land byte-for-byte identical, and
// the service must have recorded the size.
func TestWorkspaceStageOut_BinaryRoundTrip(t *testing.T) {
	payload := allBytesRepeated(16)
	want := sha256.Sum256(payload)
	src := writeTemp(t, "chunks.jsonl.gz", payload)

	f := bvbrctest.New(t)
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
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

	stored := f.Bytes(dest)
	if got := sha256.Sum256(stored); got != want {
		t.Fatalf("sha256 mismatch: stored %d bytes (%x), staged out %d bytes (%x)",
			len(stored), got, len(payload), want)
	}
	if obj := f.Object(dest); obj == nil || obj.Size != int64(len(payload)) {
		t.Errorf("workspace object = %+v, want size %d recorded", obj, len(payload))
	}

	if inline := inlineContent(t, f); len(inline) != 0 {
		t.Errorf("file bytes were sent inline via Workspace.create for %v; they must go through Shock", inline)
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
		t.Errorf("Shock filename = %q, want the source basename", put.Filename)
	}
	if put.Authorization != "OAuth un=tester|sig=abc" {
		t.Errorf("Shock Authorization = %q, want \"OAuth <token>\"", put.Authorization)
	}
	if len(put.TransferEncoding) != 0 || put.ContentLength != put.RawLength {
		t.Errorf("Shock request: Transfer-Encoding %v, Content-Length %d, body %d bytes; want an exact Content-Length",
			put.TransferEncoding, put.ContentLength, put.RawLength)
	}

	// The destination folders were created on the way.
	for _, dir := range []string{"/tester@bvbrc/home/results", "/tester@bvbrc/home/results/1"} {
		if obj := f.Object(dir); obj == nil || obj.Type != "folder" {
			t.Errorf("folder %s = %+v, want it created", dir, obj)
		}
	}

	// A second stage-out into the same folder hits "already exists" on the
	// folders and overwrites the object; both must be handled quietly.
	if _, err := stager.StageOut(context.Background(), src, "task-2", StageOptions{
		Metadata: map[string]string{"destination": "ws:///tester@bvbrc/home/results/1/"},
	}); err != nil {
		t.Fatalf("second StageOut: %v", err)
	}
	if got := sha256.Sum256(f.Bytes(dest)); got != want {
		t.Error("second stage-out did not store the payload")
	}
}

// TestWorkspaceStageOut_RetriesAfterShockFailure: a transient Shock failure on
// the first attempt deletes that attempt's placeholder and the retry starts
// over with a fresh node (a node's file is immutable, so re-PUTting to the
// same one is never an option).
func TestWorkspaceStageOut_RetriesAfterShockFailure(t *testing.T) {
	payload := allBytesRepeated(4)
	src := writeTemp(t, "out.bin", payload)

	f := bvbrctest.New(t)
	failures := 0
	f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
		if failures == 0 {
			failures++
			return http.StatusInternalServerError, "shock hiccup"
		}
		return 0, ""
	}
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester",
		MaxRetries:   3,
	}, nil)

	const dest = "/tester@bvbrc/home/results/out.bin"
	loc, err := stager.StageOut(context.Background(), src, "task-1", StageOptions{
		Metadata: map[string]string{"destination": "/tester@bvbrc/home/results"},
	})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}
	if loc != "ws://"+dest {
		t.Errorf("location = %q", loc)
	}

	puts := f.Puts()
	if len(puts) != 2 {
		t.Fatalf("Shock PUTs = %d, want 2 (one failed, one retried)", len(puts))
	}
	if puts[0].NodeID == puts[1].NodeID {
		t.Errorf("retry reused node %s; every attempt must get a fresh node", puts[0].NodeID)
	}
	if sha256.Sum256(puts[1].Body) != sha256.Sum256(payload) {
		t.Error("retried PUT did not carry the full payload (the source must be re-opened per attempt)")
	}

	deletes := f.CallsTo("Workspace.delete")
	if len(deletes) != 1 {
		t.Errorf("Workspace.delete calls = %d, want 1 (the failed attempt's placeholder)", len(deletes))
	}
	if got := f.Bytes(dest); sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Errorf("final stored bytes differ from the payload")
	}
	if obj := f.Object(dest); obj == nil || obj.Size != int64(len(payload)) {
		t.Errorf("workspace object = %+v, want size %d", obj, len(payload))
	}
}

// TestWorkspaceStageOut_FailsAfterExhaustedRetries: when every attempt fails
// the stager reports the error and leaves no placeholder behind.
func TestWorkspaceStageOut_FailsAfterExhaustedRetries(t *testing.T) {
	src := writeTemp(t, "out.bin", allBytesRepeated(1))

	f := bvbrctest.New(t)
	f.ShockReply = func(p bvbrctest.ShockPut) (int, string) {
		// Shock "accepts" but reports a truncated store every time.
		return http.StatusOK, `{"status":200,"error":null,"data":{"id":"` + p.NodeID + `","file":{"name":"out.bin","size":1}}}`
	}
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester",
		MaxRetries:   2,
	}, nil)

	const dest = "/tester@bvbrc/home/results/out.bin"
	if _, err := stager.StageOut(context.Background(), src, "task-1", StageOptions{
		Metadata: map[string]string{"destination": "/tester@bvbrc/home/results"},
	}); err == nil {
		t.Fatal("expected StageOut to fail when Shock stores the wrong size")
	}
	if n := len(f.Puts()); n != 2 {
		t.Errorf("Shock PUTs = %d, want 2 attempts", n)
	}
	if f.Object(dest) != nil {
		t.Error("a placeholder survived the failed upload; it would be reported as delivered")
	}
}

// TestWorkspaceUploadContent_GoesThroughShock covers the manifest path
// (_gowe_outputs.json), which is text but is routed identically so no caller is
// left on the JSON-inline path.
func TestWorkspaceUploadContent_GoesThroughShock(t *testing.T) {
	f := bvbrctest.New(t)
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester",
	}, nil)

	const dest = "/tester@bvbrc/home/results/1/_gowe_outputs.json"
	content := `{"outputs":{"out":"ws:///tester@bvbrc/home/results/1/a.bin"}}`

	if _, err := stager.UploadContent(context.Background(), dest, content, StageOptions{}); err != nil {
		t.Fatalf("UploadContent: %v", err)
	}

	if got := string(f.Bytes(dest)); got != content {
		t.Errorf("stored content = %q, want %q", got, content)
	}
	if inline := inlineContent(t, f); len(inline) != 0 {
		t.Errorf("content was sent inline via Workspace.create for %v", inline)
	}
}

// TestWorkspaceStageOut_RPCRetryAfterGoodPut: a transient failure of the
// metadata refresh that follows a successful Shock PUT is retried at the
// JSON-RPC level by the client. The file must not be streamed a second time.
func TestWorkspaceStageOut_RPCRetryAfterGoodPut(t *testing.T) {
	payload := allBytesRepeated(4)
	src := writeTemp(t, "out.bin", payload)

	f := bvbrctest.New(t)
	failed := false
	f.Intercept = func(method string, _ json.RawMessage) (int, string) {
		if method == "Workspace.update_auto_meta" && !failed {
			failed = true
			return http.StatusInternalServerError, "upstream unavailable"
		}
		return 0, ""
	}
	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester",
		MaxRetries:   3,
	}, nil)

	const dest = "/tester@bvbrc/home/results/out.bin"
	loc, err := stager.StageOut(context.Background(), src, "task-1", StageOptions{
		Metadata: map[string]string{"destination": "/tester@bvbrc/home/results"},
	})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}
	if loc != "ws://"+dest {
		t.Errorf("location = %q", loc)
	}

	if n := len(f.Puts()); n != 1 {
		t.Fatalf("Shock PUTs = %d, want exactly 1: the RPC hiccup must not force a re-upload", n)
	}
	if n := len(f.CallsTo("Workspace.update_auto_meta")); n != 2 {
		t.Errorf("update_auto_meta calls = %d, want 2 (one failed, one retried)", n)
	}
	if n := len(f.CallsTo("Workspace.delete")); n != 0 {
		t.Errorf("Workspace.delete calls = %d, want 0", n)
	}
	if got := sha256.Sum256(f.Bytes(dest)); got != sha256.Sum256(payload) {
		t.Error("stored bytes differ from the payload")
	}
	if obj := f.Object(dest); obj == nil || obj.Size != int64(len(payload)) {
		t.Errorf("workspace object = %+v, want size %d", obj, len(payload))
	}
}
