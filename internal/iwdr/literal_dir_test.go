package iwdr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// With StageOptions.LiteralDir, a file literal in the listing is materialized
// below that directory (job-private) instead of the shared
// $TMPDIR/cwl-literals; the staged entry (a symlink for host execution)
// resolves there.
func TestStageFileLiteralUsesLiteralDir(t *testing.T) {
	tool := func(contents string) *cwl.CommandLineTool {
		return &cwl.CommandLineTool{Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{map[string]any{"class": "File", "basename": "lit.txt", "contents": contents}},
			},
		}}
	}
	for _, contents := range []string{"A", "B"} {
		workDir, litDir := t.TempDir(), t.TempDir()
		if _, err := Stage(tool(contents), map[string]any{}, workDir, StageOptions{LiteralDir: litDir}); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		staged := filepath.Join(workDir, "lit.txt")
		target, err := filepath.EvalSymlinks(staged)
		if err != nil {
			t.Fatalf("staged entry: %v", err)
		}
		wantDir, _ := filepath.EvalSymlinks(filepath.Join(litDir, "cwl-literals"))
		if filepath.Dir(target) != wantDir {
			t.Errorf("staged entry resolves to %q, want under %q", target, wantDir)
		}
		if b, err := os.ReadFile(staged); err != nil || string(b) != contents {
			t.Errorf("staged content = %q (err %v), want %q", b, err, contents)
		}
	}
}
