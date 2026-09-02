package toolexec

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// asAnySlice converts a slice-typed value of any concrete element type (e.g.
// goja's export of a JS array of objects may come back as []map[string]any
// rather than []any) into []any, for uniform test assertions.
func asAnySlice(t *testing.T, v any) []any {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		t.Fatalf("value is %T, want a slice", v)
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

// TestCollectOutputs_OutputEvalBasenameRename is the engine-level regression
// test for GoWe#212: outputEval renaming a glob-collected File's basename
// must be honored on disk, not just in the in-memory File object. It mirrors
// the shape of the repro-oracle fixture (rename-tool.cwl): glob *.txt, sort,
// then prefix each basename in outputEval.
func TestCollectOutputs_OutputEvalBasenameRename(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"1.txt", "2.txt", "3.txt"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("content-"+name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tool := &cwl.CommandLineTool{
		Outputs: map[string]cwl.ToolOutputParam{
			"renamed": {
				Type: "File[]",
				OutputBinding: &cwl.OutputBinding{
					Glob: "*.txt",
					OutputEval: `
${
  self.sort(function(a,b){ return parseInt(a.nameroot,10) - parseInt(b.nameroot,10); });
  for (var i = 0; i < self.length; i++) {
    self[i].basename = inputs.prefix + self[i].basename;
  }
  return self;
}`,
				},
			},
		},
	}
	inputs := map[string]any{"prefix": "alpha_"}

	e := NewExecutor(slog.Default())
	outputs, err := e.CollectOutputs(tool, workDir, inputs, 0, workDir, nil)
	if err != nil {
		t.Fatalf("CollectOutputs: %v", err)
	}

	renamed := asAnySlice(t, outputs["renamed"])
	if len(renamed) != 3 {
		t.Fatalf("got %d files, want 3", len(renamed))
	}

	wantBasenames := []string{"alpha_1.txt", "alpha_2.txt", "alpha_3.txt"}
	var gotBasenames, gotOnDisk []string
	for _, item := range renamed {
		f, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("file entry is %T, want map[string]any", item)
		}
		basename, _ := f["basename"].(string)
		path, _ := f["path"].(string)
		gotBasenames = append(gotBasenames, basename)

		// The gate invariant: basename MUST equal filepath.Base(path),
		// asserted at disk level.
		if filepath.Base(path) != basename {
			t.Errorf("INCONSISTENT: basename=%q path-base=%q (path=%q)", basename, filepath.Base(path), path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("basename %q: file not found on disk at %q: %v", basename, path, err)
		}
		gotOnDisk = append(gotOnDisk, filepath.Base(path))
	}

	sort.Strings(gotBasenames)
	sort.Strings(gotOnDisk)
	sort.Strings(wantBasenames)
	if len(gotBasenames) != len(wantBasenames) {
		t.Fatalf("basenames = %v, want %v", gotBasenames, wantBasenames)
	}
	for i := range wantBasenames {
		if gotBasenames[i] != wantBasenames[i] {
			t.Errorf("basenames = %v, want %v", gotBasenames, wantBasenames)
			break
		}
	}
	for i := range wantBasenames {
		if gotOnDisk[i] != wantBasenames[i] {
			t.Errorf("on-disk names = %v, want %v", gotOnDisk, wantBasenames)
			break
		}
	}

	// The pre-rename names must no longer exist (renamed, not copied).
	for _, stale := range []string{"1.txt", "2.txt", "3.txt"} {
		if _, err := os.Stat(filepath.Join(workDir, stale)); !os.IsNotExist(err) {
			t.Errorf("stale pre-rename file %q still present on disk", stale)
		}
	}
}

// TestCollectOutputs_OutputEvalBasenameCollision reproduces the scatter
// collision shape (rename-scatter-wf.cwl): if outputEval assigns colliding
// basenames within a single glob result, CollectOutputs must error rather
// than silently clobber one file with another.
func TestCollectOutputs_OutputEvalBasenameCollision(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"1.txt", "2.txt"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("content-"+name), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tool := &cwl.CommandLineTool{
		Outputs: map[string]cwl.ToolOutputParam{
			"renamed": {
				Type: "File[]",
				OutputBinding: &cwl.OutputBinding{
					Glob: "*.txt",
					// Every file collapses onto the same basename.
					OutputEval: `
${
  for (var i = 0; i < self.length; i++) {
    self[i].basename = "same.txt";
  }
  return self;
}`,
				},
			},
		},
	}

	e := NewExecutor(slog.Default())
	_, err := e.CollectOutputs(tool, workDir, map[string]any{}, 0, workDir, nil)
	if err == nil {
		t.Fatal("expected an error for colliding outputEval basenames, got nil")
	}
}
