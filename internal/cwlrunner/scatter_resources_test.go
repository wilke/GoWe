package cwlrunner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
				opts := r.containerOptions(mode, tool, &cmdline.BuildResult{}, nil, "img", "/w", nil, "", res, nil)
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

// TestScatterIterationsApplyResources runs a real 2-element scatter through
// Runner.Execute (sequential and parallel) with a capturing fake executor and
// asserts every iteration receives the tool's declared resources (#316).
func TestScatterIterationsApplyResources(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		name := "sequential"
		if parallel {
			name = "parallel"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(n, c string) {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0644); err != nil {
					t.Fatal(err)
				}
			}
			write("tool.cwl", `cwlVersion: v1.2
class: CommandLineTool
requirements:
  DockerRequirement:
    dockerPull: img.sif
  ResourceRequirement:
    ramMin: 2048
    coresMin: 2
baseCommand: echo
inputs:
  msg:
    type: string
    inputBinding: {position: 1}
outputs: {}
`)
			write("main.cwl", `cwlVersion: v1.2
class: Workflow
requirements:
  ScatterFeatureRequirement: {}
inputs:
  msgs: string[]
outputs: {}
steps:
  s:
    run: tool.cwl
    scatter: msg
    in: {msg: msgs}
    out: []
`)
			write("job.yml", "msgs: [a, b]\n")

			var mu sync.Mutex
			var got []toolexec.ResourceConfig
			r := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
			r.ContainerRuntime = "apptainer"
			r.OutDir = filepath.Join(dir, "out")
			r.Parallel.Enabled = parallel
			r.Parallel.MaxWorkers = 2
			r.execute = func(_ context.Context, opts *toolexec.Options) (*toolexec.Result, error) {
				mu.Lock()
				got = append(got, opts.Resources)
				mu.Unlock()
				return &toolexec.Result{Outputs: map[string]any{}}, nil
			}
			if err := r.Execute(context.Background(), filepath.Join(dir, "main.cwl"), filepath.Join(dir, "job.yml"), io.Discard); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("captured %d executions, want 2", len(got))
			}
			for i, res := range got {
				if res.RamMB != 2048 || res.Cores != 2 {
					t.Errorf("iteration %d: Resources = %+v, want RamMB=2048 Cores=2", i, res)
				}
			}
		})
	}
}
