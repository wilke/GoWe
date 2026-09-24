package wslocation

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"already ws scheme", "ws:///awilke@bvbrc/home/data/contigs.fasta", "ws:///awilke@bvbrc/home/data/contigs.fasta"},
		{"already http scheme", "http://example.com/file.txt", "http://example.com/file.txt"},
		{"already https scheme", "https://example.com/file.txt", "https://example.com/file.txt"},
		{"already file scheme", "file:///tmp/data.txt", "file:///tmp/data.txt"},
		{"already s3 scheme", "s3://bucket/key.txt", "s3://bucket/key.txt"},
		{"already shock scheme", "shock://p3.theseed.org/node/abc123", "shock://p3.theseed.org/node/abc123"},
		{"workspace-form path becomes ws URI", "/awilke@bvbrc/home/data/contigs.fasta", "ws:///awilke@bvbrc/home/data/contigs.fasta"},
		{"workspace-form path from UI placeholder", "/username@bvbrc/home/path/to/file", "ws:///username@bvbrc/home/path/to/file"},
		// The exact case the CLI's uploadFileToWorkspace produces
		// (internal/cli/submit.go): destFolder + "/" + basename, always of
		// this "/<user>@<domain>/..." shape, must resolve identically to the
		// pre-refactor `"ws://" + wsPath` it replaces.
		{"CLI workspace upload path", "/tester@bvbrc/home/.gowe-inputs/in.bin", "ws:///tester@bvbrc/home/.gowe-inputs/in.bin"},
		{"local absolute path unchanged", "/data/sample.fastq", "/data/sample.fastq"},
		{"local relative path unchanged", "sample1_R1.fastq.gz", "sample1_R1.fastq.gz"},
		{"relative path with dots unchanged", "../data/sample.fastq", "../data/sample.fastq"},
		{"bare @ segment not workspace form (empty user)", "/@bvbrc/home/x", "/@bvbrc/home/x"},
		{"trailing @ not workspace form (empty domain)", "/awilke@/home/x", "/awilke@/home/x"},
		{"no leading slash not workspace form", "awilke@bvbrc/home/x", "awilke@bvbrc/home/x"},
		{"root-only path not workspace form", "/", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.in); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsWorkspaceForm(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"typical workspace path", "/awilke@bvbrc/home/data", true},
		{"single segment", "/awilke@bvbrc", true},
		{"no slash prefix", "awilke@bvbrc/home", false},
		{"empty user", "/@bvbrc/home", false},
		{"empty domain", "/awilke@/home", false},
		{"no at sign", "/awilke/home", false},
		{"empty string", "", false},
		{"root only", "/", false},
		{"at sign in later segment only", "/home/awilke@bvbrc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWorkspaceForm(tt.in); got != tt.want {
				t.Errorf("IsWorkspaceForm(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
