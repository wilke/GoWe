package executor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/me/gowe/internal/bundle"
	"github.com/me/gowe/pkg/model"
)

// TestBVBRCExecutor_WorkspaceURIRoundTrip proves the end-to-end invariant behind
// issue #117: a ws:// (or shock://) File/Directory input survives the bundling
// stage (bundle.ResolveFilePaths) un-mangled AND is flattened to a bare
// workspace-path string when submitted to the BV-BRC API — never sent as a CWL
// File/Directory object. This guards against a regression in any of the three
// resolvers re-introducing the filepath.Join mangling.
func TestBVBRCExecutor_WorkspaceURIRoundTrip(t *testing.T) {
	// 1. Inputs as they arrive from a job (CWL File/Directory objects with ws://).
	rawInputs := map[string]any{
		"output_path": map[string]any{
			"class":    "Directory",
			"location": "ws:///user@bvbrc/home/output",
		},
		"contigs": map[string]any{
			"class":    "File",
			"location": "ws:///user@bvbrc/home/data/contigs.fasta",
		},
		"shock_ref": map[string]any{
			"class":    "File",
			"location": "shock://p3.theseed.org/node/abc123",
		},
	}

	// 2. Bundling stage: ResolveFilePaths must NOT mangle the ws:// locations.
	resolvedInputs := make(map[string]any, len(rawInputs))
	for k, v := range rawInputs {
		resolvedInputs[k] = bundle.ResolveFilePaths(v, "/jobs/basedir")
	}

	// 3. Submit through the BV-BRC executor; it flattens File/Directory → string.
	mock := &mockRPCCaller{result: json.RawMessage(`[{"id":"j1","status":"queued"}]`)}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", BVBRCAppID: "GenomeAssembly2", Inputs: resolvedInputs}

	if _, err := e.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// 4. The BV-BRC API must receive flat workspace-path strings, not CWL objects.
	params, ok := mock.calls[0].Params[1].(map[string]any)
	if !ok {
		t.Fatalf("params not a map: %T", mock.calls[0].Params[1])
	}
	want := map[string]string{
		"output_path": "/user@bvbrc/home/output",
		"contigs":     "/user@bvbrc/home/data/contigs.fasta",
		"shock_ref":   "shock://p3.theseed.org/node/abc123",
	}
	for k, exp := range want {
		got, isStr := params[k].(string)
		if !isStr {
			t.Errorf("param %q = %T (%v), want flat string %q — a CWL object reached the API", k, params[k], params[k], exp)
			continue
		}
		if got != exp {
			t.Errorf("param %q = %q, want %q", k, got, exp)
		}
	}
}
