package fileliteral

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two tasks with a file literal of the same basename and different contents,
// each materialized in its own task dir, must not overwrite each other.
func TestMaterializeFileObjectIn_TaskDirsDoNotCollide(t *testing.T) {
	root := t.TempDir()
	dirA, dirB := filepath.Join(root, "task_a_staging"), filepath.Join(root, "task_b_staging")
	objA := map[string]any{"class": "File", "basename": "input.txt", "contents": "from task A"}
	objB := map[string]any{"class": "File", "basename": "input.txt", "contents": "from task B"}

	for _, tc := range []struct {
		obj map[string]any
		dir string
	}{{objA, dirA}, {objB, dirB}} {
		changed, err := MaterializeFileObjectIn(tc.obj, tc.dir)
		if err != nil || !changed {
			t.Fatalf("MaterializeFileObjectIn(%s) = %v, %v", tc.dir, changed, err)
		}
	}

	for _, tc := range []struct {
		obj  map[string]any
		dir  string
		want string
	}{{objA, dirA, "from task A"}, {objB, dirB, "from task B"}} {
		p, _ := tc.obj["path"].(string)
		if !strings.HasPrefix(p, tc.dir+string(filepath.Separator)) {
			t.Errorf("path %q not under %q", p, tc.dir)
		}
		if tc.obj["location"] != "file://"+p {
			t.Errorf("location = %v, want file://%s", tc.obj["location"], p)
		}
		b, err := os.ReadFile(p)
		if err != nil || string(b) != tc.want {
			t.Errorf("%s = %q (err %v), want %q", p, b, err, tc.want)
		}
	}
}

func TestMaterializeFileObjectIn_LeavesFilesWithPath(t *testing.T) {
	obj := map[string]any{"class": "File", "contents": "x", "path": "/data/x.txt"}
	changed, err := MaterializeFileObjectIn(obj, t.TempDir())
	if err != nil || changed {
		t.Fatalf("changed = %v, err = %v; a File with a path must be left alone", changed, err)
	}
	if obj["path"] != "/data/x.txt" {
		t.Errorf("path rewritten: %v", obj["path"])
	}
}
