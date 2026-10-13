package toolexec

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

func localEnvDump(t *testing.T, mutate func(*Options)) string {
	t.Helper()
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	opts := &Options{
		Tool: &cwl.CommandLineTool{
			ID:    "t",
			Class: "CommandLineTool",
			Requirements: map[string]any{
				"EnvVarRequirement": map[string]any{
					"envDef": []any{map[string]any{"envName": "REQ_VAR", "envValue": "req-value"}},
				},
			},
		},
		Command: &cmdline.BuildResult{Command: []string{"env"}},
		Inputs:  map[string]any{},
		WorkDir: t.TempDir(),
		Mode:    ModeLocal,
	}
	if mutate != nil {
		mutate(opts)
	}
	res, err := e.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res.Stdout
}

func TestExecuteLocal_EnvAllowlist(t *testing.T) {
	const sentinel = "sentinel-token-value-98765"
	tests := []struct {
		name         string
		inherit      bool
		wantSentinel bool
	}{
		{"allow-list hides host secrets", false, false},
		{"inherit-host-env restores host env", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BVBRC_TOKEN", sentinel)
			out := localEnvDump(t, func(o *Options) {
				o.InheritHostEnv = tt.inherit
				o.EnvVars = map[string]string{"WORKER_VAR": "wv"}
				o.SecretEnvVars = map[string]string{"SEC_VAR": "sv-123456789"}
			})
			if got := strings.Contains(out, sentinel); got != tt.wantSentinel {
				t.Errorf("sentinel present = %v, want %v\n%s", got, tt.wantSentinel, out)
			}
			for _, want := range []string{"PATH=", "HOME=", "TMPDIR=", "REQ_VAR=req-value", "WORKER_VAR=wv", "SEC_VAR=sv-123456789"} {
				if !strings.Contains(out, want) {
					t.Errorf("env missing %q\n%s", want, out)
				}
			}
		})
	}
}

func TestBaseToolEnv_GPU(t *testing.T) {
	t.Setenv("CUDA_HOME", "/usr/local/cuda")
	t.Setenv("NVIDIA_VISIBLE_DEVICES", "all")
	t.Setenv("LD_LIBRARY_PATH", "/opt/lib")
	t.Setenv("LC_ALL", "C")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "nope")

	tests := []struct {
		name string
		gpu  bool
		want map[string]bool
	}{
		{"no gpu", false, map[string]bool{"CUDA_HOME=": false, "NVIDIA_VISIBLE_DEVICES=": false, "LD_LIBRARY_PATH=": false, "LC_ALL=": true, "AWS_SECRET_ACCESS_KEY=": false}},
		{"gpu", true, map[string]bool{"CUDA_HOME=": true, "NVIDIA_VISIBLE_DEVICES=": true, "LD_LIBRARY_PATH=": true, "LC_ALL=": true, "AWS_SECRET_ACCESS_KEY=": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &Options{}
			opts.GPU.Enabled = tt.gpu
			env := strings.Join(baseToolEnv(opts), "\n")
			for prefix, want := range tt.want {
				if got := strings.Contains(env, prefix); got != want {
					t.Errorf("%s present = %v, want %v", prefix, got, want)
				}
			}
		})
	}
}
