package toolexec

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// installFakeApptainer puts a stub "apptainer" first on PATH. It records its
// argv (one per line) to args.txt and its APPTAINERENV_* environment to env.txt
// in the returned directory, then exits 0.
func installFakeApptainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + filepath.Join(dir, "args.txt") + "'\n" +
		"env | grep '^APPTAINERENV_' > '" + filepath.Join(dir, "env.txt") + "'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "apptainer"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func runFakeApptainer(t *testing.T, mutate func(*Options)) (args []string, env string) {
	t.Helper()
	dir := installFakeApptainer(t)
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	opts := &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        ModeApptainer,
		DockerImage: "alpine",
	}
	if mutate != nil {
		mutate(opts)
	}
	_, _ = e.Execute(context.Background(), opts)

	raw, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatalf("fake apptainer was not invoked: %v", err)
	}
	args = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	envRaw, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	return args, string(envRaw)
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestApptainerArgs_Isolation(t *testing.T) {
	tests := []struct {
		name          string
		noContainall  bool
		wantContainal bool
	}{
		{"default has containall and cleanenv", false, true},
		{"no-containall option keeps cleanenv only", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := runFakeApptainer(t, func(o *Options) { o.ApptainerNoContainall = tt.noContainall })
			if len(args) == 0 || args[0] != "exec" {
				t.Fatalf("args[0] = %v, want exec", args)
			}
			if got := indexOf(args, "--containall") >= 0; got != tt.wantContainal {
				t.Errorf("--containall present = %v, want %v (args %v)", got, tt.wantContainal, args)
			}
			if tt.wantContainal && (len(args) < 3 || args[1] != "--containall" || args[2] != "--cleanenv") {
				t.Errorf("want '--containall --cleanenv' right after the subcommand, got %v", args[:3])
			}
			if !tt.wantContainal && (len(args) < 2 || args[1] != "--cleanenv") {
				t.Errorf("want '--cleanenv' right after the subcommand, got %v", args[:2])
			}
			// The explicit binds the builder relies on must still be present.
			for _, want := range []string{"--home", "--pwd"} {
				if indexOf(args, want) < 0 {
					t.Errorf("missing %s in %v", want, args)
				}
			}
		})
	}
}

func TestApptainer_SecretViaAPPTAINERENV_Survives(t *testing.T) {
	_, env := runFakeApptainer(t, func(o *Options) {
		o.SecretEnvVars = map[string]string{"MY_SECRET": "s3cretvalue-123456"}
	})
	if !strings.Contains(env, "APPTAINERENV_MY_SECRET=s3cretvalue-123456") {
		t.Errorf("secret not delivered via APPTAINERENV_*; env:\n%s", env)
	}
}

func TestApptainerIsolationArgs(t *testing.T) {
	tests := []struct {
		name         string
		noContainall bool
		want         string
	}{
		{"default", false, "--containall --cleanenv"},
		{"no containall", true, "--cleanenv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(ApptainerIsolationArgs(tt.noContainall), " "); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
