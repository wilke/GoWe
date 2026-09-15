package toolexec

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// TestMaskSecretValues covers the argv-masking helper used before the
// "docker command" / "apptainer command" Debug log lines: secret VALUES
// (e.g. from "--env NAME=value") must never reach the log, while the real
// argv slice passed to exec.CommandContext is untouched.
func TestMaskSecretValues(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		secrets map[string]string
		want    []string
	}{
		{
			name:    "no secrets: args passed through unchanged (same backing values)",
			args:    []string{"run", "--rm", "alpine"},
			secrets: nil,
			want:    []string{"run", "--rm", "alpine"},
		},
		{
			name:    "secret value in an --env entry is masked",
			args:    []string{"run", "-e", "HF_TOKEN=abcdef123456", "alpine"},
			secrets: map[string]string{"HF_TOKEN": "abcdef123456"},
			want:    []string{"run", "-e", "HF_TOKEN=***REDACTED***", "alpine"},
		},
		{
			name:    "multiple secrets, only matching entries masked",
			args:    []string{"-e", "A=value-one-long", "-e", "B=value-two-long", "-e", "PUBLIC=not-secret"},
			secrets: map[string]string{"A": "value-one-long", "B": "value-two-long"},
			want:    []string{"-e", "A=***REDACTED***", "-e", "B=***REDACTED***", "-e", "PUBLIC=not-secret"},
		},
		{
			name:    "short secret values (<6 bytes) are not masked (avoids over-redaction)",
			args:    []string{"-e", "X=ab"},
			secrets: map[string]string{"X": "ab"},
			want:    []string{"-e", "X=ab"},
		},
		{
			name:    "secret value appearing in the tool command itself is masked too",
			args:    []string{"alpine", "echo", "abcdef123456"},
			secrets: map[string]string{"HF_TOKEN": "abcdef123456"},
			want:    []string{"alpine", "echo", "***REDACTED***"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskSecretValues(tt.args, tt.secrets)
			if len(got) != len(tt.want) {
				t.Fatalf("maskSecretValues() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestMaskSecretValues_NeverMutatesInput guards the "log a copy, never
// mutate the real argv" invariant: the caller's original slice (what
// actually gets exec'd) must be untouched.
func TestMaskSecretValues_NeverMutatesInput(t *testing.T) {
	dockerArgs := []string{"run", "-e", "HF_TOKEN=abcdef123456", "alpine"}
	secrets := map[string]string{"HF_TOKEN": "abcdef123456"}

	logged := maskSecretValues(dockerArgs, secrets)

	if dockerArgs[2] != "HF_TOKEN=abcdef123456" {
		t.Fatalf("original argv was mutated: %v", dockerArgs)
	}
	if logged[2] != "HF_TOKEN=***REDACTED***" {
		t.Fatalf("logged copy not masked: %v", logged)
	}
}

// TestExecuteInDocker_DebugLogMasksSecretValue drives the real
// "docker command" Debug line (executeInDocker builds the full argv,
// including "-e NAME=value" for every opts.SecretEnvVars entry, before
// logging it) and confirms the emitted log record never contains the
// secret value while the returned/executed argv (proven indirectly: the
// build only fails later, at the actual `docker` exec, never before the log
// line) would still carry it. Running an actual container is not required —
// docker is not assumed to be installed in this environment — because the
// Debug log fires before cmd.Run(); the test only needs the build to reach
// that line, which it always does before any exec.
func TestExecuteInDocker_DebugLogMasksSecretValue(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewExecutor(logger)

	const secretValue = "abcdef123456secret"
	opts := &Options{
		Tool:          &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:       &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:        map[string]any{},
		WorkDir:       t.TempDir(),
		Mode:          ModeDocker,
		DockerImage:   "alpine",
		SecretEnvVars: map[string]string{"MY_SECRET": secretValue},
	}

	// The docker exec itself is expected to fail or be a no-op in this
	// sandboxed test environment; only the pre-exec Debug log matters here.
	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "docker command") {
		t.Fatalf("expected a %q log line, got: %s", "docker command", logOutput)
	}
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("docker command log leaked the secret value: %s", logOutput)
	}
	if !strings.Contains(logOutput, "***REDACTED***") {
		t.Errorf("docker command log missing redaction marker: %s", logOutput)
	}
}

// TestExecuteInApptainer_DebugLogMasksSecretValue is the Apptainer
// equivalent of TestExecuteInDocker_DebugLogMasksSecretValue.
func TestExecuteInApptainer_DebugLogMasksSecretValue(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewExecutor(logger)

	const secretValue = "abcdef123456secret"
	opts := &Options{
		Tool:          &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:       &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:        map[string]any{},
		WorkDir:       t.TempDir(),
		Mode:          ModeApptainer,
		DockerImage:   "alpine",
		SecretEnvVars: map[string]string{"MY_SECRET": secretValue},
	}

	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "apptainer command") {
		t.Fatalf("expected an %q log line, got: %s", "apptainer command", logOutput)
	}
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("apptainer command log leaked the secret value: %s", logOutput)
	}
	if !strings.Contains(logOutput, "***REDACTED***") {
		t.Errorf("apptainer command log missing redaction marker: %s", logOutput)
	}
}
