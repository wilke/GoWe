package cwltool

import (
	"testing"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
)

func TestResolveResources(t *testing.T) {
	rr := func(m map[string]any) map[string]any { return map[string]any{"ResourceRequirement": m} }
	tests := []struct {
		name    string
		tool    *cwl.CommandLineTool
		cfg     Config
		cores   int
		ram     int64
		want    toolexec.ResourceConfig
		wantErr bool
	}{
		{name: "nothing declared, no caps", tool: &cwl.CommandLineTool{}, cores: 1, ram: 1024},
		{name: "declared cores and ram", tool: &cwl.CommandLineTool{Requirements: rr(map[string]any{"coresMin": 2, "ramMin": 2048})},
			cores: 2, ram: 2048, want: toolexec.ResourceConfig{Cores: 2, RamMB: 2048}},
		{name: "worker caps apply when undeclared", tool: &cwl.CommandLineTool{},
			cfg: Config{MaxCPUs: 4, MaxMemMB: 8192}, cores: 1, ram: 1024,
			want: toolexec.ResourceConfig{Cores: 4, RamMB: 8192}},
		{name: "cores exceed worker max", tool: &cwl.CommandLineTool{Requirements: rr(map[string]any{"coresMin": 8})},
			cfg: Config{MaxCPUs: 4}, cores: 8, ram: 1024, wantErr: true},
		{name: "ram exceeds worker max", tool: &cwl.CommandLineTool{Hints: rr(map[string]any{"ramMin": 4096})},
			cfg: Config{MaxMemMB: 2048}, cores: 1, ram: 4096, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.Cores = tt.cores
			runtime.Ram = tt.ram
			got, err := ResolveResources(runtime, tt.cfg, tt.tool)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
