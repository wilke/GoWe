//go:build integration

package cwlrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestCWLRunner_ScatterResolvesImageDir is the #312 regression test: a
// scattered step whose DockerRequirement names a relative .sif must resolve it
// against --image-dir, as a plain tool step does, not against the cwd.
func TestCWLRunner_ScatterResolvesImageDir(t *testing.T) {
	skipIfNoApptainer(t)
	if _, err := os.Stat(pythonSIF); err != nil {
		t.Skipf("%s not available", pythonSIF)
	}
	tmpDir := t.TempDir()
	files := map[string]string{
		"echo.cwl": `
cwlVersion: v1.2
class: CommandLineTool
requirements:
  DockerRequirement:
    dockerPull: python.sif
baseCommand: echo
inputs:
  msg: {type: string, inputBinding: {position: 1}}
outputs:
  out: stdout
stdout: out.txt
`,
		"wf.cwl": `
cwlVersion: v1.2
class: Workflow
requirements:
  ScatterFeatureRequirement: {}
inputs:
  msgs: string[]
steps:
  say:
    run: echo.cwl
    scatter: msg
    in: {msg: msgs}
    out: [out]
outputs:
  outs:
    type: File[]
    outputSource: say/out
`,
		"job.yml": "msgs: [a, b]\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The test's cwd (the package dir) has no python.sif, so a resolution
	// against the cwd fails the scatter iterations.
	for _, parallel := range []bool{false, true} {
		name := map[bool]string{false: "sequential", true: "parallel"}[parallel]
		t.Run(name, func(t *testing.T) {
			r := NewRunner(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
			r.OutDir = filepath.Join(tmpDir, "out-"+name)
			r.ContainerRuntime = "apptainer"
			r.ImageDir = filepath.Dir(pythonSIF)
			r.Parallel = ParallelConfig{Enabled: parallel, MaxWorkers: 2}
			var buf bytes.Buffer
			if err := r.Execute(context.Background(), filepath.Join(tmpDir, "wf.cwl"), filepath.Join(tmpDir, "job.yml"), &buf); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var result struct {
				Outs []map[string]any `json:"outs"`
			}
			if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, buf.String())
			}
			if len(result.Outs) != 2 {
				t.Fatalf("want 2 scatter outputs, got %d: %s", len(result.Outs), buf.String())
			}
		})
	}
}
