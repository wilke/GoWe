package toolexec

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// TestCollectOutputs_Validation covers #279 (a required File glob matching
// nothing) and #310 (cwl.output.json missing/invalid/extra keys) end to end
// through the shared CollectOutputs entry point.
func TestCollectOutputs_Validation(t *testing.T) {
	enum := map[string]any{"type": "enum", "symbols": []any{"red", "blue"}}
	arrOf := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	globTool := func(typ string, schema any) *cwl.CommandLineTool {
		return &cwl.CommandLineTool{Outputs: map[string]cwl.ToolOutputParam{
			"must": {Type: typ, TypeSchema: schema, OutputBinding: &cwl.OutputBinding{Glob: "missing.txt"}},
		}}
	}
	jsonTool := &cwl.CommandLineTool{Outputs: map[string]cwl.ToolOutputParam{
		"req": {Type: "string", TypeSchema: "string"},
		"col": {Type: "enum", TypeSchema: enum},
	}}

	tests := []struct {
		name       string
		tool       *cwl.CommandLineTool
		outputJSON string
		wantErr    string
		wantKeys   []string
	}{
		{"required File glob matches nothing (#279)", globTool("File", "File"), "", `output "must"`, nil},
		{"optional File glob matches nothing", globTool("File?", []any{"null", "File"}), "", "", []string{"must"}},
		{"required File[] glob matches nothing is empty array", globTool("File[]", arrOf("File")), "", "", []string{"must"}},
		{"cwl.output.json missing required (#310)", jsonTool, `{"col":"red","extra":1}`, `output "req"`, nil},
		{"cwl.output.json bad enum (#310)", jsonTool, `{"req":"x","col":"green"}`, `output "col"`, nil},
		{"cwl.output.json extra key dropped", jsonTool, `{"req":"x","col":"red","extra":1}`, "", []string{"req", "col"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			if tc.outputJSON != "" {
				if err := os.WriteFile(filepath.Join(workDir, "cwl.output.json"), []byte(tc.outputJSON), 0644); err != nil {
					t.Fatal(err)
				}
			}
			outputs, err := NewExecutor(slog.Default()).CollectOutputs(tc.tool, workDir, nil, 0, workDir, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got []string
			for k := range outputs {
				got = append(got, k)
			}
			sort.Strings(got)
			sort.Strings(tc.wantKeys)
			if !reflect.DeepEqual(got, tc.wantKeys) {
				t.Errorf("keys = %v, want %v", got, tc.wantKeys)
			}
		})
	}
}
