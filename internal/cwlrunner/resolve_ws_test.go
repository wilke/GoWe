package cwlrunner

import "testing"

// resolveFileObject must preserve remote URI locations (ws://, shock://,
// http(s)://) rather than joining them with baseDir as if relative (issue #117).
func TestResolveFileObject_PreservesRemoteURIs(t *testing.T) {
	cases := []struct {
		name, class, location string
	}{
		{"workspace file", "File", "ws:///user@bvbrc/home/data/contigs.fasta"},
		{"workspace dir", "Directory", "ws:///user@bvbrc/home/output"},
		{"shock file", "File", "shock://p3.theseed.org/node/abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := resolveFileObject(map[string]any{"class": tc.class, "location": tc.location}, "/jobs/basedir")
			if got := out["location"]; got != tc.location {
				t.Errorf("location = %q, want %q (preserved, not joined)", got, tc.location)
			}
			if p, ok := out["path"].(string); ok && p != "" {
				t.Errorf("path = %q set for a remote URI; want no local path", p)
			}
		})
	}
}

func TestResolveFileObject_LocalStillResolved(t *testing.T) {
	out := resolveFileObject(map[string]any{"class": "File", "location": "file:///tmp/x.fasta"}, "/jobs/basedir")
	if got := out["location"]; got != "file:///tmp/x.fasta" {
		t.Errorf("location = %q, want file:///tmp/x.fasta", got)
	}
	if got := out["path"]; got != "/tmp/x.fasta" {
		t.Errorf("path = %q, want /tmp/x.fasta", got)
	}
}
