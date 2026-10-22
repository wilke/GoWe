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
)

// envRuntimeTool is the #334 tool shape: EnvVarRequirement and arguments both
// use $(runtime.*) and must agree.
const envRuntimeTool = `
cwlVersion: v1.2
class: CommandLineTool
requirements:
  ShellCommandRequirement: {}
  ResourceRequirement:
    coresMin: 3
  EnvVarRequirement:
    envDef:
      T_ENV: $(runtime.tmpdir)/x
      N_ENV: $(runtime.cores)
      O_ENV: $(runtime.outdir)
%s
baseCommand: []
arguments:
  - shellQuote: false
    valueFrom: 'echo tmp_env=$T_ENV tmp_arg=$(runtime.tmpdir)/x n_env=$N_ENV n_arg=$(runtime.cores) o_env=$O_ENV o_arg=$(runtime.outdir)'
inputs: []
outputs:
  out: stdout
stdout: out.txt
`

// runEnvRuntimeTool runs the tool and returns the key=value pairs it echoed.
func runEnvRuntimeTool(t *testing.T, docker string, configure func(*Runner)) map[string]string {
	t.Helper()
	dir := t.TempDir()
	cwlPath := filepath.Join(dir, "t.cwl")
	if err := os.WriteFile(cwlPath, []byte(strings.Replace(envRuntimeTool, "%s", docker, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(dir, "job.yml")
	if err := os.WriteFile(jobPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	r.OutDir = filepath.Join(dir, "out")
	if configure != nil {
		configure(r)
	}
	var buf bytes.Buffer
	if err := r.Execute(context.Background(), cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res struct {
		Out struct {
			Path     string `json:"path"`
			Location string `json:"location"`
		} `json:"out"`
	}
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	p := res.Out.Path
	if p == "" {
		p = strings.TrimPrefix(res.Out.Location, "file://")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range strings.Fields(string(data)) {
		if k, v, ok := strings.Cut(f, "="); ok {
			got[k] = v
		}
	}
	return got
}

func assertEnvMatchesArgs(t *testing.T, got map[string]string) {
	t.Helper()
	for _, p := range [][2]string{{"tmp_env", "tmp_arg"}, {"n_env", "n_arg"}, {"o_env", "o_arg"}} {
		if got[p[0]] == "" || got[p[0]] != got[p[1]] {
			t.Errorf("%s=%q but %s=%q (env must match arguments)", p[0], got[p[0]], p[1], got[p[1]])
		}
	}
	if got["n_env"] != "3" {
		t.Errorf("runtime.cores = %q, want 3", got["n_env"])
	}
}

func TestEnvVarRequirement_UsesRealRuntime_Local(t *testing.T) {
	got := runEnvRuntimeTool(t, "", func(r *Runner) { r.NoContainer = true })
	assertEnvMatchesArgs(t, got)
	if strings.HasPrefix(got["tmp_env"], "/tmp/cwl-tmp") {
		t.Errorf("tmpdir is the default runtime value: %q", got["tmp_env"])
	}
}
