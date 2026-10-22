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

// TestMaskSecretValues covers the argv-masking helper used before every
// tool-command log line: secret VALUES must never reach the log, while the
// real argv slice passed to exec.CommandContext is untouched.
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
			name:    "secret value in a command arg is masked",
			args:    []string{"run", "echo", "abcdef1234567890"},
			secrets: map[string]string{"HF_TOKEN": "abcdef1234567890"},
			want:    []string{"run", "echo", "***REDACTED***"},
		},
		{
			name:    "multiple secrets, only matching entries masked",
			args:    []string{"A=value-one-long", "B=value-two-long", "PUBLIC=not-secret"},
			secrets: map[string]string{"A": "value-one-long", "B": "value-two-long"},
			want:    []string{"A=***REDACTED***", "B=***REDACTED***", "PUBLIC=not-secret"},
		},
		{
			name:    "short secret values (<8 bytes) are not masked (avoids over-redaction)",
			args:    []string{"X=abcdefg"},
			secrets: map[string]string{"X": "abcdefg"}, // 7 bytes
			want:    []string{"X=abcdefg"},
		},
		{
			name:    "8-byte secret value is masked (at the threshold)",
			args:    []string{"X=abcdefgh"},
			secrets: map[string]string{"X": "abcdefgh"}, // 8 bytes
			want:    []string{"X=***REDACTED***"},
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
			got := MaskSecretValues(tt.args, tt.secrets)
			if len(got) != len(tt.want) {
				t.Fatalf("MaskSecretValues() = %v, want %v", got, tt.want)
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
	args := []string{"run", "echo", "abcdef123456secret"}
	secrets := map[string]string{"HF_TOKEN": "abcdef123456secret"}

	logged := MaskSecretValues(args, secrets)

	if args[2] != "abcdef123456secret" {
		t.Fatalf("original argv was mutated: %v", args)
	}
	if logged[2] != "***REDACTED***" {
		t.Fatalf("logged copy not masked: %v", logged)
	}
}

// TestSecretEnvAssignments covers the H5 helper that turns a secret map into
// "<prefix>NAME=value" cmd.Env entries — the ONLY place a secret VALUE is
// allowed to end up, never on a container-runtime argv.
func TestSecretEnvAssignments(t *testing.T) {
	t.Run("no prefix (docker)", func(t *testing.T) {
		got := secretEnvAssignments(map[string]string{"HF_TOKEN": "abc123"}, "")
		want := []string{"HF_TOKEN=abc123"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("secretEnvAssignments() = %v, want %v", got, want)
		}
	})
	t.Run("APPTAINERENV_ prefix", func(t *testing.T) {
		got := secretEnvAssignments(map[string]string{"HF_TOKEN": "abc123"}, "APPTAINERENV_")
		want := []string{"APPTAINERENV_HF_TOKEN=abc123"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("secretEnvAssignments() = %v, want %v", got, want)
		}
	})
	t.Run("empty secrets: nil, not an empty non-nil slice", func(t *testing.T) {
		if got := secretEnvAssignments(nil, ""); got != nil {
			t.Errorf("secretEnvAssignments(nil) = %v, want nil", got)
		}
	})
}

// TestExecuteInDocker_ArgvNeverCarriesSecretValue drives the real
// executeInDocker argv build (H5): a secret env var must appear on dockerArgs
// as a bare "-e NAME" (name only) — the value itself must never appear
// anywhere in the built argv, whether or not it would also need masking.
// This is checked by capturing the Debug "docker command" log (which logs
// the exact argv, merely redaction-passed) and independently confirming the
// NAME is present. docker is not assumed installed; the Debug log fires
// before cmd.Run().
func TestExecuteInDocker_ArgvNeverCarriesSecretValue(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewExecutor(logger)

	const secretName = "MY_SECRET"
	const secretValue = "abcdef123456secretvalue"
	opts := &Options{
		Tool:          &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:       &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:        map[string]any{},
		WorkDir:       t.TempDir(),
		Mode:          ModeDocker,
		DockerImage:   "alpine",
		SecretEnvVars: map[string]string{secretName: secretValue},
	}

	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "docker command") {
		t.Fatalf("expected a %q log line, got: %s", "docker command", logOutput)
	}
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("docker command log/argv leaked the secret VALUE: %s", logOutput)
	}
	if !strings.Contains(logOutput, secretName) {
		t.Errorf("docker command log/argv missing the secret NAME (-e %s expected): %s", secretName, logOutput)
	}
}

// TestExecuteInApptainer_ArgvNeverCarriesSecretValue is the Apptainer
// equivalent: no --env for a secret at all, so neither the NAME nor the
// VALUE appears in apptainerArgs; the delivery mechanism is
// APPTAINERENV_<NAME> in cmd.Env instead.
func TestExecuteInApptainer_ArgvNeverCarriesSecretValue(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewExecutor(logger)

	const secretName = "MY_SECRET"
	const secretValue = "abcdef123456secretvalue"
	opts := &Options{
		Tool:          &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:       &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:        map[string]any{},
		WorkDir:       t.TempDir(),
		Mode:          ModeApptainer,
		DockerImage:   "alpine",
		SecretEnvVars: map[string]string{secretName: secretValue},
	}

	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "apptainer command") {
		t.Fatalf("expected an %q log line, got: %s", "apptainer command", logOutput)
	}
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("apptainer command log/argv leaked the secret VALUE: %s", logOutput)
	}
	// Apptainer gets no --env for secrets at all: the NAME should not appear
	// either (unlike Docker's bare "-e NAME" form).
	if strings.Contains(logOutput, secretName) {
		t.Errorf("apptainer command log/argv unexpectedly names the secret (should carry neither name nor value): %s", logOutput)
	}
}

// TestExecuteInDocker_MaskSecretsCoversJobOnlySecrets proves the Debug
// "docker command" line masks a secret value that appears in the tool
// command itself (e.g. a cwltool:Secrets input consumed via
// $(inputs.pw)), not just values delivered through SecretEnvVars — this is
// what opts.MaskSecrets (superset of SecretEnvVars) is for.
func TestExecuteInDocker_MaskSecretsCoversJobOnlySecrets(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := NewExecutor(logger)

	const jobOnlySecret = "job-only-secret-value-123"
	opts := &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", jobOnlySecret}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        ModeDocker,
		DockerImage: "alpine",
		// Not in SecretEnvVars (it's job-only, never exposed to the env) —
		// only in MaskSecrets, exactly as cwltool.ApplySecrets populates
		// Config.SecretValues for a re-injected cwltool:Secrets input.
		MaskSecrets: map[string]string{"pw": jobOnlySecret},
	}

	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if strings.Contains(logOutput, jobOnlySecret) {
		t.Errorf("docker command log leaked a job-only secret value not present in SecretEnvVars: %s", logOutput)
	}
	if !strings.Contains(logOutput, "***REDACTED***") {
		t.Errorf("docker command log missing redaction marker: %s", logOutput)
	}
}

// TestExecuteLocal_InfoLogMasksSecretValue covers H6 for the local runtime's
// "executing locally" Info line, which logs the built command argv — the
// same argv that can carry a re-injected cwltool:Secrets value via
// $(inputs.x). Info is the default production log level, unlike the
// Docker/Apptainer "command" lines which are Debug-only.
func TestExecuteLocal_InfoLogMasksSecretValue(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := NewExecutor(logger)

	const secretValue = "job-only-secret-value-456"
	opts := &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", secretValue}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        ModeLocal,
		MaskSecrets: map[string]string{"pw": secretValue},
	}

	_, _ = e.Execute(context.Background(), opts)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "executing locally") {
		t.Fatalf("expected an %q log line, got: %s", "executing locally", logOutput)
	}
	if strings.Contains(logOutput, secretValue) {
		t.Errorf("executing-locally Info log leaked the secret value: %s", logOutput)
	}
	if !strings.Contains(logOutput, "***REDACTED***") {
		t.Errorf("executing-locally Info log missing redaction marker: %s", logOutput)
	}
}
