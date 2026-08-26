package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
)

const wsTestToken = "un=tester@bvbrc|tokenid=1|expiry=9999999999|sig=deadbeef"

// writeCredentials points the CLI's stored-token lookup at a temp HOME.
func writeCredentials(t *testing.T, token string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gowe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(credentials{Token: token})
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), creds, 0o600); err != nil {
		t.Fatal(err)
	}
}

func binaryPayload(reps int) []byte {
	b := make([]byte, 0, 256*reps)
	for r := 0; r < reps; r++ {
		for i := 0; i < 256; i++ {
			b = append(b, byte(i))
		}
	}
	return b
}

// TestSubmitWorkspaceUpload_ThroughFake drives `gowe submit --workspace-upload
// --workspace-url` end to end against the shared fake: the File inputs must
// land in /<user>/home/.gowe-inputs byte-exact with their size recorded.
func TestSubmitWorkspaceUpload_ThroughFake(t *testing.T) {
	writeCredentials(t, wsTestToken)
	f := bvbrctest.New(t)
	url := startTestServer(t)

	dir := t.TempDir()
	r1 := binaryPayload(8)
	r2 := binaryPayload(3)
	if err := os.WriteFile(filepath.Join(dir, "sample1_R1.fastq.gz"), r1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample1_R2.fastq.gz"), r2, 0o644); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(dir, "job.yml")
	if err := os.WriteFile(job, []byte(`reads_r1:
  class: File
  path: sample1_R1.fastq.gz
reads_r2:
  class: File
  path: sample1_R2.fastq.gz
scientific_name: "Escherichia coli K-12"
taxonomy_id: 83333
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Silence the command's stdout/stderr chatter.
	oldOut, oldErr := os.Stdout, os.Stderr
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout, os.Stderr = devnull, devnull
	_, err := runCLI(t,
		"--server", url,
		"submit", testdataPath("separate/pipeline.cwl"),
		"--inputs", job,
		"--workspace-upload",
		"--workspace-url", f.WorkspaceURL(),
		"--dry-run",
	)
	os.Stdout, os.Stderr = oldOut, oldErr
	devnull.Close()
	if err != nil {
		t.Fatalf("submit --workspace-upload: %v", err)
	}

	const folder = "/tester@bvbrc/home/.gowe-inputs"
	if obj := f.Object(folder); obj == nil || obj.Type != "folder" {
		t.Errorf("inputs folder %s = %+v, want it created", folder, obj)
	}

	for name, want := range map[string][]byte{"sample1_R1.fastq.gz": r1, "sample1_R2.fastq.gz": r2} {
		dest := folder + "/" + name
		got := f.Bytes(dest)
		if sha256.Sum256(got) != sha256.Sum256(want) {
			t.Errorf("%s: stored %d bytes, want %d byte-exact", dest, len(got), len(want))
		}
		if obj := f.Object(dest); obj == nil || obj.Size != int64(len(want)) {
			t.Errorf("%s: workspace object = %+v, want size %d recorded", dest, obj, len(want))
		}
	}

	puts := f.Puts()
	if len(puts) != 2 {
		t.Fatalf("Shock PUTs = %d, want 2", len(puts))
	}
	for _, p := range puts {
		if p.Authorization != "OAuth "+wsTestToken {
			t.Errorf("Shock Authorization = %q, want the stored token with the OAuth scheme", p.Authorization)
		}
		if p.Filename == "" || len(p.TransferEncoding) != 0 || p.ContentLength != p.RawLength {
			t.Errorf("Shock PUT shape: filename %q, TE %v, Content-Length %d for %d bytes",
				p.Filename, p.TransferEncoding, p.ContentLength, p.RawLength)
		}
	}
	if n := len(f.CallsTo("Workspace.update_auto_meta")); n != 2 {
		t.Errorf("update_auto_meta calls = %d, want one per uploaded file", n)
	}
}

// A file that fails verification on the first attempt is retried and the
// retry re-opens the source.
func TestUploadInputsToWorkspace_RetriesFailedUpload(t *testing.T) {
	f := bvbrctest.New(t)
	failed := false
	f.ShockReply = func(bvbrctest.ShockPut) (int, string) {
		if !failed {
			failed = true
			return 200, `{"status":200,"error":null,"data":{"id":"x","file":{"name":"in.bin","size":1}}}`
		}
		return 0, ""
	}

	payload := binaryPayload(2)
	src := filepath.Join(t.TempDir(), "in.bin")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	inputs := map[string]any{
		"reads": map[string]any{"class": "File", "location": src},
		"name":  "unchanged",
	}
	out, err := uploadInputsToWorkspace(inputs, wsTestToken, f.WorkspaceURL())
	if err != nil {
		t.Fatalf("uploadInputsToWorkspace: %v", err)
	}

	const dest = "/tester@bvbrc/home/.gowe-inputs/in.bin"
	file, _ := out["reads"].(map[string]any)
	if file["location"] != "ws://"+dest || file["path"] != dest || file["basename"] != "in.bin" {
		t.Errorf("rewritten File = %v", file)
	}
	if out["name"] != "unchanged" {
		t.Errorf("non-File input altered: %v", out["name"])
	}
	if !bytes.Equal(f.Bytes(dest), payload) {
		t.Error("stored bytes differ from the payload")
	}
	if n := len(f.Puts()); n != 2 {
		t.Errorf("Shock PUTs = %d, want 2 (one rejected, one retried)", n)
	}
	if n := len(f.CallsTo("Workspace.delete")); n != 1 {
		t.Errorf("Workspace.delete calls = %d, want 1 (the failed attempt's placeholder)", n)
	}
}

func TestSubmitWorkspaceURLFlag(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("GOWE_WORKSPACE_URL", "")
		flag := newSubmitCmd().Flags().Lookup("workspace-url")
		if flag == nil {
			t.Fatal("--workspace-url flag missing")
		}
		if flag.DefValue != "https://p3.theseed.org/services/Workspace" {
			t.Errorf("default = %q, want the production Workspace URL", flag.DefValue)
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("GOWE_WORKSPACE_URL", "http://ws.test/services/Workspace")
		flag := newSubmitCmd().Flags().Lookup("workspace-url")
		if flag.DefValue != "http://ws.test/services/Workspace" {
			t.Errorf("default = %q, want GOWE_WORKSPACE_URL", flag.DefValue)
		}
	})
}
