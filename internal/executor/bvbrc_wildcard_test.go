package executor

import (
	"context"
	"sort"
	"testing"

	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
	"github.com/me/gowe/pkg/model"
)

// wildcardTestTask builds a task whose Tool declares one wildcard-glob
// output (id "reports", glob "*.txt", the given CWL type) plus one
// result_folder-shaped output that must be ignored by buildOutputsFromGlobs.
// The task carries a per-task token via RuntimeHints so the executor can
// build a Workspace client for listResultFolder.
func wildcardTestTask(outputType string) *model.Task {
	return &model.Task{
		ID: "task_1",
		Tool: map[string]any{
			"outputs": map[string]any{
				"result_folder": map[string]any{
					"type":          "Directory",
					"outputBinding": map[string]any{"glob": "."},
				},
				"reports": map[string]any{
					"type":          outputType,
					"outputBinding": map[string]any{"glob": "*.txt"},
				},
			},
		},
		RuntimeHints: &model.RuntimeHints{
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Token: "un=tester|sig=abc"},
			},
		},
	}
}

func newWildcardTestExecutor(t *testing.T, f *bvbrctest.Server) *BVBRCExecutor {
	t.Helper()
	e := NewBVBRCExecutor("", &mockRPCCaller{}, bvbrcLogger())
	e.SetWorkspaceURL(f.WorkspaceURL())
	return e
}

func TestBuildOutputsFromGlobs_WildcardSingleMatch(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/report.txt", Type: "txt"})

	e := newWildcardTestExecutor(t, f)
	task := wildcardTestTask("File")

	outputs, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v", err)
	}

	out, ok := outputs["reports"].(map[string]any)
	if !ok {
		t.Fatalf("outputs[reports] = %#v, want a File object", outputs["reports"])
	}
	if out["basename"] != "report.txt" {
		t.Errorf("basename = %v, want report.txt", out["basename"])
	}
	wantLoc := "ws:///tester@bvbrc/home/results/.asm/report.txt"
	if out["location"] != wantLoc {
		t.Errorf("location = %v, want %v", out["location"], wantLoc)
	}
}

func TestBuildOutputsFromGlobs_WildcardMultiMatchArray(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/b.txt", Type: "txt"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/a.txt", Type: "txt"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/notes.log", Type: "txt"}) // does not match *.txt

	e := newWildcardTestExecutor(t, f)
	task := wildcardTestTask("File[]")

	outputs, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v", err)
	}

	arr, ok := outputs["reports"].([]any)
	if !ok {
		t.Fatalf("outputs[reports] = %#v, want an array", outputs["reports"])
	}
	if len(arr) != 2 {
		t.Fatalf("len(reports) = %d, want 2 (notes.log must not match *.txt)", len(arr))
	}

	var names []string
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("array element = %#v, want a File object", item)
		}
		names = append(names, obj["basename"].(string))
	}
	sort.Strings(names)
	if names[0] != "a.txt" || names[1] != "b.txt" {
		t.Errorf("basenames = %v, want [a.txt b.txt] (sorted)", names)
	}
}

// TestBuildOutputsFromGlobs_WildcardSkipsJobResultAndFolderEntries confirms
// that a wildcard glob broad enough to match everything in the result folder
// (e.g. "*") only picks up files, not "folder" or "job_result" typed
// entries — job_result being the type BV-BRC's own job-result containers
// carry (confirmed against a live service during the #154/#198 promotion
// round-trip), which are containers holding files, not files themselves.
func TestBuildOutputsFromGlobs_WildcardSkipsJobResultAndFolderEntries(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/report.txt", Type: "txt"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/subdir", Type: "folder"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/nested_job", Type: "job_result"})

	e := newWildcardTestExecutor(t, f)
	task := &model.Task{
		ID: "task_1",
		Tool: map[string]any{
			"outputs": map[string]any{
				"result_folder": map[string]any{
					"type":          "Directory",
					"outputBinding": map[string]any{"glob": "."},
				},
				"reports": map[string]any{
					"type":          "File[]",
					"outputBinding": map[string]any{"glob": "*"},
				},
			},
		},
		RuntimeHints: &model.RuntimeHints{
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Token: "un=tester|sig=abc"},
			},
		},
	}

	outputs, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v", err)
	}

	arr, ok := outputs["reports"].([]any)
	if !ok {
		t.Fatalf("outputs[reports] = %#v, want an array", outputs["reports"])
	}
	if len(arr) != 1 {
		t.Fatalf("len(reports) = %d, want 1 (folder/job_result entries must be excluded): %#v", len(arr), arr)
	}
	obj, ok := arr[0].(map[string]any)
	if !ok || obj["basename"] != "report.txt" {
		t.Errorf("reports[0] = %#v, want report.txt", arr[0])
	}
}

func TestBuildOutputsFromGlobs_WildcardMultiMatchScalarErrors(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/a.txt", Type: "txt"})
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/b.txt", Type: "txt"})

	e := newWildcardTestExecutor(t, f)
	task := wildcardTestTask("File") // scalar type, but two files match *.txt

	_, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err == nil {
		t.Fatal("expected an error for a non-array output matching more than one file")
	}
}

func TestBuildOutputsFromGlobs_EmptyListingSkipsWithoutError(t *testing.T) {
	f := bvbrctest.New(t)
	// No objects registered under the result folder: WorkspaceLs returns an
	// empty listing for it, not an error.

	e := newWildcardTestExecutor(t, f)
	task := wildcardTestTask("File")

	outputs, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v, want nil (empty listing must be tolerated)", err)
	}
	if _, ok := outputs["reports"]; ok {
		t.Errorf("outputs[reports] = %#v, want absent (unresolved, not errored)", outputs["reports"])
	}
	// The result_folder output is still populated.
	if _, ok := outputs["result_folder"]; !ok {
		t.Error("expected result_folder to still be populated")
	}
}

func TestBuildOutputsFromGlobs_ConcreteGlobUnaffectedByWildcards(t *testing.T) {
	f := bvbrctest.New(t)
	f.Put(bvbrctest.Object{Path: "/tester@bvbrc/home/results/.asm/report.txt", Type: "txt"})

	e := newWildcardTestExecutor(t, f)
	task := &model.Task{
		ID: "task_1",
		Tool: map[string]any{
			"outputs": map[string]any{
				"result_folder": map[string]any{
					"type":          "Directory",
					"outputBinding": map[string]any{"glob": "."},
				},
				"log": map[string]any{
					"type":          "File",
					"outputBinding": map[string]any{"glob": "asm.log"}, // concrete, no wildcard
				},
			},
		},
		RuntimeHints: &model.RuntimeHints{
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Token: "un=tester|sig=abc"},
			},
		},
	}

	outputs, err := e.buildOutputsFromGlobs(context.Background(), task, "/tester@bvbrc/home/results", "asm")
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v", err)
	}
	out, ok := outputs["log"].(map[string]any)
	if !ok {
		t.Fatalf("outputs[log] = %#v, want a File object", outputs["log"])
	}
	if out["basename"] != "asm.log" {
		t.Errorf("basename = %v, want asm.log", out["basename"])
	}
	// No workspace listing call should have been made — the glob had no
	// wildcard characters.
	if calls := f.CallsTo("Workspace.ls"); len(calls) != 0 {
		t.Errorf("Workspace.ls calls = %d, want 0 for an all-concrete-glob tool", len(calls))
	}
}

func TestOutputTypeIsArray(t *testing.T) {
	tests := []struct {
		name string
		typ  any
		want bool
	}{
		{"scalar File", "File", false},
		{"array shorthand", "File[]", true},
		{"optional array shorthand", "File[]?", true},
		{"array map form", map[string]any{"type": "array", "items": "File"}, true},
		{"union with array", []any{"null", "File[]"}, true},
		{"union without array", []any{"null", "File"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cwlTypeIsArray(tt.typ)
			if got != tt.want {
				t.Errorf("cwlTypeIsArray(%#v) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}
