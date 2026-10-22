//go:build integration

package cwlrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/hostres"
)

const pythonSIF = "/scout/containers/python.sif"

// runBindEnvTool runs a tool that lists a host directory (absolute path given
// as a string, not a File) and echoes $FOO, inside python.sif under
// --containall --cleanenv, and returns its stdout.
func runBindEnvTool(t *testing.T, hostDir string, configure func(*Runner)) string {
	t.Helper()
	skipIfNoApptainer(t)
	if _, err := os.Stat(pythonSIF); err != nil {
		t.Skipf("%s not available", pythonSIF)
	}

	tmpDir := t.TempDir()
	cwl := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [sh, -c]
requirements:
  DockerRequirement:
    dockerPull: python.sif
arguments:
  - 'ls "$0" 2>&1; echo "FOO=$FOO"; true'
inputs:
  dir:
    type: string
    inputBinding: {position: 1}
outputs:
  out:
    type: stdout
stdout: out.txt
`
	cwlPath := filepath.Join(tmpDir, "t.cwl")
	if err := os.WriteFile(cwlPath, []byte(cwl), 0o644); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(jobPath, []byte("dir: "+hostDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewRunner(logger)
	r.OutDir = filepath.Join(tmpDir, "output")
	r.ContainerRuntime = "apptainer"
	r.ImageDir = "/scout/containers"
	configure(r)

	var buf bytes.Buffer
	if err := r.Execute(context.Background(), cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	out, _ := result["out"].(map[string]any)
	path, _ := out["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v (raw %s)", err, buf.String())
	}
	return string(data)
}

func TestCWLRunner_ApptainerExtraBindAndEnv(t *testing.T) {
	hostDir := t.TempDir()
	const marker = "host-only-marker-file.dat"
	if err := os.WriteFile(filepath.Join(hostDir, marker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("with bind and env", func(t *testing.T) {
		out := runBindEnvTool(t, hostDir, func(r *Runner) {
			_, binds, err := hostres.ResolveBinds(slog.Default(), "", nil, []string{hostDir})
			if err != nil {
				t.Fatal(err)
			}
			r.ExtraBinds = binds
			r.EnvVars = map[string]string{"FOO": "bar"}
		})
		if !strings.Contains(out, marker) {
			t.Errorf("bound file not visible in container: %q", out)
		}
		if !strings.Contains(out, "FOO=bar") {
			t.Errorf("--env not visible in container: %q", out)
		}
	})

	t.Run("without bind or env", func(t *testing.T) {
		out := runBindEnvTool(t, hostDir, func(r *Runner) {})
		if strings.Contains(out, marker) {
			t.Errorf("host path visible without --extra-bind (containall isolation broken): %q", out)
		}
		if strings.Contains(out, "FOO=bar") {
			t.Errorf("FOO unexpectedly set: %q", out)
		}
	})
}
