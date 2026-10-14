package cwlrunner

import (
	"io"
	"log/slog"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
)

// TestContainerOptionsResources verifies that the options built for a
// containerized execution (the scatter-iteration path) carry the same
// resources a plain step gets from cwltool.ResolveResources (#316).
func TestContainerOptionsResources(t *testing.T) {
	tests := []struct {
		name      string
		reqs      map[string]any
		hints     map[string]any
		wantCores int
		wantRamMB int64
	}{
		{name: "no ResourceRequirement"},
		{
			name:      "requirement ramMin and coresMin",
			reqs:      map[string]any{"ResourceRequirement": map[string]any{"ramMin": 2048, "coresMin": 2}},
			wantCores: 2, wantRamMB: 2048,
		},
		{
			name:      "hint ramMin only",
			hints:     map[string]any{"ResourceRequirement": map[string]any{"ramMin": 512}},
			wantRamMB: 512,
		},
		{
			name:      "coresMin only",
			reqs:      map[string]any{"ResourceRequirement": map[string]any{"coresMin": 3}},
			wantCores: 3,
		},
	}
	modes := map[string]toolexec.ExecutionMode{"docker": toolexec.ModeDocker, "apptainer": toolexec.ModeApptainer}
	for modeName, mode := range modes {
		for _, tt := range tests {
			t.Run(modeName+"/"+tt.name, func(t *testing.T) {
				r := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
				tool := &cwl.CommandLineTool{Requirements: tt.reqs, Hints: tt.hints}
				runtime := buildRuntimeContextWithInputs(tool, "/var/spool/cwl", map[string]any{}, nil)
				res, err := cwltool.ResolveResources(runtime, cwltool.Config{}, tool)
				if err != nil {
					t.Fatalf("ResolveResources: %v", err)
				}
				opts := r.containerOptions(mode, tool, &cmdline.BuildResult{}, nil, "img", "/w", nil, "", res)
				if opts.Resources.Cores != tt.wantCores {
					t.Errorf("Cores = %d, want %d", opts.Resources.Cores, tt.wantCores)
				}
				if opts.Resources.RamMB != tt.wantRamMB {
					t.Errorf("RamMB = %d, want %d", opts.Resources.RamMB, tt.wantRamMB)
				}
			})
		}
	}
}
