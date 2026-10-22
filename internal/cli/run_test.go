package cli

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withTestClient points the package-level `client` at an httptest server that
// serves a fixed body for any /api/v1/files/download request, restoring the
// previous client on cleanup.
func withTestClient(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	prev := client
	client = NewClient(srv.URL, slog.Default())
	t.Cleanup(func() { client = prev })
}

// TestDownloadFileOutput_PrefersBasename covers #212 point D: downloadFileOutput
// must prefer the File object's spec-authoritative `basename` field for the
// local filename over the name implied by `location`, while keeping the
// on-disk result safely inside outDir even for an untrusted/legacy basename.
func TestDownloadFileOutput_PrefersBasename(t *testing.T) {
	tests := []struct {
		name         string
		fileMap      map[string]any
		wantBasename string
	}{
		{
			name: "basename present and differs from location - basename wins",
			fileMap: map[string]any{
				"class":    "File",
				"location": "file:///work_1/1.txt",
				"basename": "alpha_1.txt",
			},
			wantBasename: "alpha_1.txt",
		},
		{
			name: "basename absent - falls back to location-derived name (pre-#212 submissions)",
			fileMap: map[string]any{
				"class":    "File",
				"location": "file:///work_1/legacy.txt",
			},
			wantBasename: "legacy.txt",
		},
		{
			name: "basename empty string - falls back to location-derived name",
			fileMap: map[string]any{
				"class":    "File",
				"location": "file:///work_1/legacy2.txt",
				"basename": "",
			},
			wantBasename: "legacy2.txt",
		},
		{
			name: "basename with directory components is sanitized to its last element",
			fileMap: map[string]any{
				"class":    "File",
				"location": "file:///work_1/on-disk.txt",
				"basename": "../../etc/evil.txt",
			},
			wantBasename: "evil.txt",
		},
		{
			name: "basename of '..' is rejected, falls back to location-derived name",
			fileMap: map[string]any{
				"class":    "File",
				"location": "file:///work_1/safe.txt",
				"basename": "..",
			},
			wantBasename: "safe.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withTestClient(t, "file contents")
			outDir := t.TempDir()

			result := downloadFileOutput(tt.fileMap, outDir)

			gotBasename, _ := result["basename"].(string)
			if gotBasename != tt.wantBasename {
				t.Errorf("basename = %q, want %q", gotBasename, tt.wantBasename)
			}

			gotPath, _ := result["path"].(string)
			if filepath.Base(gotPath) != tt.wantBasename {
				t.Errorf("path base = %q, want %q (path=%q)", filepath.Base(gotPath), tt.wantBasename, gotPath)
			}

			// Containment: the downloaded file must never escape outDir,
			// regardless of what the (possibly untrusted) basename field says.
			absOutDir, _ := filepath.Abs(outDir)
			absPath, _ := filepath.Abs(gotPath)
			if absPath != filepath.Join(absOutDir, tt.wantBasename) {
				t.Errorf("path %q escaped outDir %q", absPath, absOutDir)
			}
			if _, err := os.Stat(gotPath); err != nil {
				t.Errorf("downloaded file not found at %q: %v", gotPath, err)
			}
		})
	}
}

// TestDownloadFileOutput_CollisionSuffixFallbackPreserved covers #212 scope
// guard: genuine collisions (e.g. unrenamed scatter branches that all emit
// the same basename) must still be disambiguated with the existing _N
// suffix, even after preferring basename for the primary name.
func TestDownloadFileOutput_CollisionSuffixFallbackPreserved(t *testing.T) {
	withTestClient(t, "file contents")
	outDir := t.TempDir()

	first := downloadFileOutput(map[string]any{
		"class":    "File",
		"location": "file:///work_1/report.txt",
		"basename": "report.txt",
	}, outDir)
	second := downloadFileOutput(map[string]any{
		"class":    "File",
		"location": "file:///work_2/report.txt",
		"basename": "report.txt",
	}, outDir)

	firstBasename, _ := first["basename"].(string)
	secondBasename, _ := second["basename"].(string)
	if firstBasename != "report.txt" {
		t.Errorf("first basename = %q, want report.txt", firstBasename)
	}
	if secondBasename != "report_2.txt" {
		t.Errorf("second basename = %q, want report_2.txt (collision suffix)", secondBasename)
	}

	if _, err := os.Stat(filepath.Join(outDir, "report.txt")); err != nil {
		t.Errorf("report.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "report_2.txt")); err != nil {
		t.Errorf("report_2.txt missing: %v", err)
	}
}
