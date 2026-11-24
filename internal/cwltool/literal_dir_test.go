package cwltool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// A file-literal tool default is written below Config.LiteralDir when set, so
// two jobs running at once cannot overwrite each other's literal.
func TestMergeToolDefaults_LiteralDir(t *testing.T) {
	tool := func(contents string) *cwl.CommandLineTool {
		return &cwl.CommandLineTool{Inputs: map[string]cwl.ToolInputParam{
			"conf": {Type: "File", Default: map[string]any{"class": "File", "basename": "conf.txt", "contents": contents}},
		}}
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	gotA, err := mergeToolDefaults(tool("A"), map[string]any{}, "", dirA)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := mergeToolDefaults(tool("B"), map[string]any{}, "", dirB)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		got  map[string]any
		dir  string
		want string
	}{{gotA, dirA, "A"}, {gotB, dirB, "B"}} {
		p, _ := tc.got["conf"].(map[string]any)["path"].(string)
		if !strings.HasPrefix(p, filepath.Join(tc.dir, "cwl-literals")+string(filepath.Separator)) {
			t.Errorf("literal default at %q, want under %s/cwl-literals", p, tc.dir)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != tc.want {
			t.Errorf("%s = %q (err %v), want %q", p, b, err, tc.want)
		}
	}
}
