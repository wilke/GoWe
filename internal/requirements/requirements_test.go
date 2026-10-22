package requirements

import (
	"testing"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

func TestExtractEnvVars_Runtime(t *testing.T) {
	tool := &cwl.CommandLineTool{Requirements: map[string]any{
		"EnvVarRequirement": map[string]any{"envDef": []any{
			map[string]any{"envName": "T", "envValue": "$(runtime.tmpdir)/x"},
			map[string]any{"envName": "N", "envValue": "$(runtime.cores)"},
			map[string]any{"envName": "R", "envValue": "$(runtime.ram)"},
			map[string]any{"envName": "O", "envValue": "$(runtime.outdir)"},
		}},
	}}
	rt := cwlexpr.DefaultRuntimeContext()
	rt.OutDir, rt.TmpDir, rt.Cores, rt.Ram = "/w/out", "/w/tmp", 3, 2048
	def := cwlexpr.DefaultRuntimeContext()

	tests := []struct {
		name string
		rt   *cwlexpr.RuntimeContext
		want map[string]string
	}{
		{"explicit runtime", rt, map[string]string{"T": "/w/tmp/x", "N": "3", "R": "2048", "O": "/w/out"}},
		{"nil runtime uses defaults", nil, map[string]string{
			"T": def.TmpDir + "/x", "N": "1", "R": "1024", "O": def.OutDir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEnvVars(tool, map[string]any{}, nil, tt.rt)
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
