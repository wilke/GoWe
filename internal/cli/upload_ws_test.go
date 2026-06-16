package cli

import (
	"testing"
)

// A Directory input whose location is a remote URI (ws://, shock://, http(s)://)
// is already accessible to the executor/worker and must be preserved verbatim —
// not uploaded and not stripped of its location. Regression guard for the
// "Directory loses location" half of #117 (uploadDirectoryInput previously did
// an unconditional delete(result, "location")).
func TestUploadDirectoryInput_PreservesRemoteURI(t *testing.T) {
	cases := []struct {
		name, location string
	}{
		{"workspace", "ws:///awilke@bvbrc/home/gowe-ws-test"},
		{"shock", "shock://p3.theseed.org/node/abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"class": "Directory", "location": tc.location}
			out, err := uploadDirectoryInput(in, true)
			if err != nil {
				t.Fatalf("uploadDirectoryInput: %v", err)
			}
			if got := out["location"]; got != tc.location {
				t.Errorf("location = %v, want %q (must be preserved, not dropped)", got, tc.location)
			}
		})
	}
}

// A file:// Directory is LOCAL and must NOT take the remote-URI early-return —
// it must still go through the upload path. Regression guard: cwl.IsURI returns
// true for file://, so the guard must check the scheme, not just IsURI. Using a
// nonexistent path means no upload is attempted (no server client needed); the
// observable is that the local-path branch ran and stripped the location.
func TestUploadDirectoryInput_FileURLNotPreserved(t *testing.T) {
	in := map[string]any{"class": "Directory", "location": "file:///nonexistent-dir-xyz-12345"}
	out, err := uploadDirectoryInput(in, true)
	if err != nil {
		t.Fatalf("uploadDirectoryInput: %v", err)
	}
	if _, ok := out["location"]; ok {
		t.Errorf("file:// directory kept its location %v — it took the remote-URI early-return instead of the local upload path", out["location"])
	}
}

// A File input with a remote URI location is likewise preserved (already the
// case; locks it in alongside the Directory fix).
func TestUploadFileInput_PreservesRemoteURI(t *testing.T) {
	in := map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/data/contigs.fasta"}
	out, err := uploadFileInput(in, true)
	if err != nil {
		t.Fatalf("uploadFileInput: %v", err)
	}
	if got := out["location"]; got != "ws:///awilke@bvbrc/home/data/contigs.fasta" {
		t.Errorf("location = %v, want preserved ws:// URI", got)
	}
}
