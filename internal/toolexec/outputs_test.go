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

// TestCollectOutputs_RecordOutputEvalBasenameRename is the regression test
// for GoWe#214 gap 1: processOutputFileObject's record-type outputEval
// branch used to unconditionally recompute basename from path, discarding
// an intentional rename before cwloutput.NormalizeOutputFiles ever saw it.
// The rename must now survive processOutputFileObject and be honored on
// disk by NormalizeOutputFiles, exactly like the File[]/glob case in
// TestCollectOutputs_OutputEvalBasenameRename.
func TestCollectOutputs_RecordOutputEvalBasenameRename(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "1.txt"), []byte("content-1"), 0644); err != nil {
		t.Fatalf("write 1.txt: %v", err)
	}

	tool := &cwl.CommandLineTool{
		Outputs: map[string]cwl.ToolOutputParam{
			"myrecord": {
				Type: "record",
				OutputBinding: &cwl.OutputBinding{
					// No glob: self=null, the record is built entirely by
					// the expression, mirroring a record-typed output whose
					// File field is assembled (and renamed) by hand.
					OutputEval: `
${
  return {
    "file": {
      "class": "File",
      "path": runtime.outdir + "/1.txt",
      "basename": "alpha_1.txt"
    }
  };
}`,
				},
			},
		},
	}

	e := NewExecutor(slog.Default())
	outputs, err := e.CollectOutputs(tool, workDir, map[string]any{}, 0, workDir, nil)
	if err != nil {
		t.Fatalf("CollectOutputs: %v", err)
	}

	rec, ok := outputs["myrecord"].(map[string]any)
	if !ok {
		t.Fatalf("myrecord is %T, want map[string]any", outputs["myrecord"])
	}
	f, ok := rec["file"].(map[string]any)
	if !ok {
		t.Fatalf("myrecord.file is %T, want map[string]any", rec["file"])
	}

	basename, _ := f["basename"].(string)
	path, _ := f["path"].(string)
	if basename != "alpha_1.txt" {
		t.Errorf("basename = %q, want %q (intentional rename must be honored, not overwritten with the path-derived name)", basename, "alpha_1.txt")
	}
	// The gate invariant: basename MUST equal filepath.Base(path), asserted
	// at disk level, exactly as in TestCollectOutputs_OutputEvalBasenameRename.
	if filepath.Base(path) != basename {
		t.Errorf("INCONSISTENT: basename=%q path-base=%q (path=%q)", basename, filepath.Base(path), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("basename %q: file not found on disk at %q: %v", basename, path, err)
	}
	wantNameroot, wantNameext := "alpha_1", ".txt"
	if f["nameroot"] != wantNameroot {
		t.Errorf("nameroot = %v, want %v", f["nameroot"], wantNameroot)
	}
	if f["nameext"] != wantNameext {
		t.Errorf("nameext = %v, want %v", f["nameext"], wantNameext)
	}

	// The pre-rename name must no longer exist (renamed, not copied).
	if _, err := os.Stat(filepath.Join(workDir, "1.txt")); !os.IsNotExist(err) {
		t.Errorf("stale pre-rename file \"1.txt\" still present on disk")
	}
}

// TestCollectOutputs_RecordOutputEvalNoBasenameRegression is the no-op
// companion to TestCollectOutputs_RecordOutputEvalBasenameRename: when the
// record's File field does NOT carry an explicit basename, GoWe must still
// fill one in from path (the original, pre-#214 behavior for the common
// case), and no spurious rename should happen on disk.
func TestCollectOutputs_RecordOutputEvalNoBasenameRegression(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "2.txt"), []byte("content-2"), 0644); err != nil {
		t.Fatalf("write 2.txt: %v", err)
	}

	tool := &cwl.CommandLineTool{
		Outputs: map[string]cwl.ToolOutputParam{
			"myrecord": {
				Type: "record",
				OutputBinding: &cwl.OutputBinding{
					OutputEval: `
${
  return {
    "file": {
      "class": "File",
      "path": runtime.outdir + "/2.txt"
    }
  };
}`,
				},
			},
		},
	}

	e := NewExecutor(slog.Default())
	outputs, err := e.CollectOutputs(tool, workDir, map[string]any{}, 0, workDir, nil)
	if err != nil {
		t.Fatalf("CollectOutputs: %v", err)
	}

	rec := outputs["myrecord"].(map[string]any)
	f := rec["file"].(map[string]any)

	basename, _ := f["basename"].(string)
	path, _ := f["path"].(string)
	if basename != "2.txt" {
		t.Errorf("basename = %q, want %q (path-derived basename must still be filled in when absent)", basename, "2.txt")
	}
	if filepath.Base(path) != basename {
		t.Errorf("INCONSISTENT: basename=%q path-base=%q (path=%q)", basename, filepath.Base(path), path)
	}
	wantNameroot, wantNameext := "2", ".txt"
	if f["nameroot"] != wantNameroot {
		t.Errorf("nameroot = %v, want %v", f["nameroot"], wantNameroot)
	}
	if f["nameext"] != wantNameext {
		t.Errorf("nameext = %v, want %v", f["nameext"], wantNameext)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not found on disk at %q: %v", path, err)
	}
}
