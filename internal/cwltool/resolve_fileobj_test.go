package cwltool

import "testing"

// Comprehensive behavior pin for ResolveFileObject — the single File/Directory
// resolver shared by cwltool and cwlrunner (issue #123). Guards the contract so
// the unified implementation can't drift.
func TestResolveFileObject_Behavior(t *testing.T) {
	t.Run("relative location joined with baseDir and path/basename derived", func(t *testing.T) {
		out := ResolveFileObject(map[string]any{"class": "File", "location": "sub/x.fasta"}, "/base")
		if out["location"] != "/base/sub/x.fasta" {
			t.Errorf("location = %v, want /base/sub/x.fasta", out["location"])
		}
		if out["path"] != "/base/sub/x.fasta" {
			t.Errorf("path = %v, want /base/sub/x.fasta", out["path"])
		}
		if out["basename"] != "x.fasta" {
			t.Errorf("basename = %v, want x.fasta", out["basename"])
		}
		if out["nameroot"] != "x" || out["nameext"] != ".fasta" {
			t.Errorf("nameroot/nameext = %v/%v, want x/.fasta", out["nameroot"], out["nameext"])
		}
		if out["dirname"] != "/base/sub" {
			t.Errorf("dirname = %v, want /base/sub", out["dirname"])
		}
	})

	t.Run("file:// derives path and keeps basename", func(t *testing.T) {
		out := ResolveFileObject(map[string]any{"class": "File", "location": "file:///tmp/a.tar.gz"}, "/base")
		if out["path"] != "/tmp/a.tar.gz" {
			t.Errorf("path = %v, want /tmp/a.tar.gz", out["path"])
		}
		// nameext is the last extension only.
		if out["nameroot"] != "a.tar" || out["nameext"] != ".gz" {
			t.Errorf("nameroot/nameext = %v/%v, want a.tar/.gz", out["nameroot"], out["nameext"])
		}
	})

	t.Run("existing basename is not overwritten", func(t *testing.T) {
		out := ResolveFileObject(map[string]any{"class": "File", "location": "/abs/x.txt", "basename": "renamed.txt"}, "/base")
		if out["basename"] != "renamed.txt" {
			t.Errorf("basename = %v, want renamed.txt (preserved)", out["basename"])
		}
	})

	t.Run("Directory listing entries are recursively resolved", func(t *testing.T) {
		out := ResolveFileObject(map[string]any{
			"class":    "Directory",
			"location": "/base/dir",
			"listing": []any{
				map[string]any{"class": "File", "location": "child.txt"},
			},
		}, "/base")
		listing, ok := out["listing"].([]any)
		if !ok || len(listing) != 1 {
			t.Fatalf("listing = %v, want 1 entry", out["listing"])
		}
		child := listing[0].(map[string]any)
		// Child relative location resolved against the directory's path.
		if child["location"] != "/base/dir/child.txt" {
			t.Errorf("listing child location = %v, want /base/dir/child.txt", child["location"])
		}
		if child["basename"] != "child.txt" {
			t.Errorf("listing child basename = %v, want child.txt", child["basename"])
		}
	})

	t.Run("File secondaryFiles entries are recursively resolved against baseDir", func(t *testing.T) {
		out := ResolveFileObject(map[string]any{
			"class":    "File",
			"location": "/base/aln.bam",
			"secondaryFiles": []any{
				map[string]any{"class": "File", "location": "aln.bai"},
			},
		}, "/base")
		sf, ok := out["secondaryFiles"].([]any)
		if !ok || len(sf) != 1 {
			t.Fatalf("secondaryFiles = %v, want 1 entry", out["secondaryFiles"])
		}
		if sf[0].(map[string]any)["location"] != "/base/aln.bai" {
			t.Errorf("secondaryFile location = %v, want /base/aln.bai", sf[0].(map[string]any)["location"])
		}
	})

	t.Run("does not mutate the input map", func(t *testing.T) {
		in := map[string]any{"class": "File", "location": "rel.txt"}
		_ = ResolveFileObject(in, "/base")
		if in["location"] != "rel.txt" {
			t.Errorf("input mutated: location = %v, want rel.txt", in["location"])
		}
		if _, ok := in["path"]; ok {
			t.Errorf("input mutated: path was added")
		}
	})
}
