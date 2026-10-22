//go:build integration

package cwlrunner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvVarRequirement_UsesRealRuntime_Apptainer(t *testing.T) {
	skipIfNoApptainer(t)
	if _, err := os.Stat(pythonSIF); err != nil {
		t.Skipf("%s not available", pythonSIF)
	}
	got := runEnvRuntimeTool(t, "  DockerRequirement:\n    dockerPull: python.sif", func(r *Runner) {
		r.ContainerRuntime = "apptainer"
		r.ImageDir = filepath.Dir(pythonSIF)
	})
	assertEnvMatchesArgs(t, got)
	if got["tmp_env"] != "/tmp/x" {
		t.Errorf("tmp_env = %q, want /tmp/x", got["tmp_env"])
	}
}
