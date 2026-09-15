package worker

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// --- mergeSecretsForRedaction ------------------------------------------------

func TestMergeSecretsForRedaction(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]string
		want map[string]string
	}{
		{
			name: "both nil",
			want: nil,
		},
		{
			name: "a empty: b passed through",
			a:    nil,
			b:    map[string]string{"X": "vx"},
			want: map[string]string{"X": "vx"},
		},
		{
			name: "b empty: a passed through",
			a:    map[string]string{"X": "vx"},
			b:    nil,
			want: map[string]string{"X": "vx"},
		},
		{
			name: "disjoint keys: union",
			a:    map[string]string{"A": "va"},
			b:    map[string]string{"B": "vb"},
			want: map[string]string{"A": "va", "B": "vb"},
		},
		{
			name: "overlapping key: b wins",
			a:    map[string]string{"A": "from-a"},
			b:    map[string]string{"A": "from-b"},
			want: map[string]string{"A": "from-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeSecretsForRedaction(tt.a, tt.b)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeSecretsForRedaction() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestMergeSecretsForRedaction_DoesNotMutateInputs guards against either
// shared map (e.g. the worker's own w.secrets) being mutated in place.
func TestMergeSecretsForRedaction_DoesNotMutateInputs(t *testing.T) {
	a := map[string]string{"A": "va"}
	b := map[string]string{"B": "vb"}

	got := mergeSecretsForRedaction(a, b)
	got["C"] = "leaked-into-a-or-b?"

	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("input maps mutated: a=%v b=%v", a, b)
	}
}

// --- End-to-end: env delivery + redaction via a real local execution -----

// TestSecretDelivery_EnvVar_AndRedaction runs an actual local process (no
// Docker) that echoes an injected secret env var to stdout, exactly as
// executeWithCWLTool wires cfg.SecretEnvVars, and proves redactSecrets (the
// same function executeWithCWLTool applies to captured output) strips the
// value from what would be reported to the server.
func TestSecretDelivery_EnvVar_AndRedaction(t *testing.T) {
	tool := &cwl.CommandLineTool{
		ID:          "echo-secret",
		Class:       "CommandLineTool",
		BaseCommand: []string{"sh", "-c", "echo \"got: $MY_TASK_SECRET\""},
	}

	secretValue := "supersecretvalue123456"
	cfg := cwltool.Config{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		SecretEnvVars: map[string]string{"MY_TASK_SECRET": secretValue},
	}

	workDir := t.TempDir()
	result, err := cwltool.ExecuteTool(context.Background(), cfg, tool, map[string]any{}, workDir)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !strings.Contains(result.Stdout, secretValue) {
		t.Fatalf("precondition failed: raw stdout %q does not contain the secret; the test proves nothing", result.Stdout)
	}

	// Mirrors executeWithCWLTool's redaction step.
	redacted := redactSecrets(result.Stdout, mergeSecretsForRedaction(cfg.SecretEnvVars, cfg.SecretValues))
	if strings.Contains(redacted, secretValue) {
		t.Errorf("redacted stdout still contains the secret value: %q", redacted)
	}
	if !strings.Contains(redacted, "***REDACTED***") {
		t.Errorf("redacted stdout = %q, want a redaction marker", redacted)
	}
}

// TestWorkerSecretPath_SecretEnvAndReinjection reproduces the exact call
// sequence executeWithCWLTool now runs (H4: build cfg with worker-level
// SecretEnvVars, call cwltool.ApplySecrets, then cwltool.ExecuteTool) end to
// end: a task carrying both a secret_env-delivered value (visible in the
// container env) and a cwltool:Secrets re-injected job value (visible via
// $(inputs.pw) in an InitialWorkDirRequirement-staged file, mirroring
// cwltool's own secret_job.cwl pattern) must see BOTH the real values, and
// the captured stdout must come back redacted for both.
func TestWorkerSecretPath_SecretEnvAndReinjection(t *testing.T) {
	tool := &cwl.CommandLineTool{
		ID:          "secret-job",
		Class:       "CommandLineTool",
		BaseCommand: []string{"sh", "-c", "echo \"env=$MY_TASK_SECRET\"; cat config.txt"},
		Inputs: map[string]cwl.ToolInputParam{
			"pw": {Type: "string"},
		},
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "config.txt",
						"entry":     "$(inputs.pw)",
					},
				},
			},
		},
	}

	const envSecret = "env-secret-value-0123456789"
	const jobSecret = "job-secret-value-9876543210"

	task := &model.Task{
		ID:  "task_1",
		Job: map[string]any{"pw": model.SecretInputPlaceholder},
		RuntimeHints: &model.RuntimeHints{
			Secrets: map[string]string{
				"MY_TASK_SECRET": envSecret,
				"INPUT_PW":       jobSecret,
			},
			SecretEnvNames: []string{"MY_TASK_SECRET"}, // NOT INPUT_PW (M13)
			SecretInputs:   []string{"pw=INPUT_PW"},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := cwltool.Config{Logger: logger}

	job, err := cwltool.ApplySecrets(&cfg, task, logger)
	if err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	// M13: only the secret_env-named value reaches cfg.SecretEnvVars.
	if len(cfg.SecretEnvVars) != 1 || cfg.SecretEnvVars["MY_TASK_SECRET"] != envSecret {
		t.Fatalf("cfg.SecretEnvVars = %v, want only MY_TASK_SECRET", cfg.SecretEnvVars)
	}
	if _, leaked := cfg.SecretEnvVars["INPUT_PW"]; leaked {
		t.Errorf("cfg.SecretEnvVars leaked the job-only INPUT_PW secret: %v", cfg.SecretEnvVars)
	}

	// task.Job itself must never be mutated — the server-persisted copy
	// stays the placeholder.
	if task.Job["pw"] != model.SecretInputPlaceholder {
		t.Errorf("task.Job was mutated: %v", task.Job)
	}
	if job["pw"] != jobSecret {
		t.Fatalf("returned job[\"pw\"] = %v, want the re-injected secret", job["pw"])
	}

	workDir := t.TempDir()
	result, err := cwltool.ExecuteTool(context.Background(), cfg, tool, job, workDir)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !strings.Contains(result.Stdout, envSecret) || !strings.Contains(result.Stdout, jobSecret) {
		t.Fatalf("precondition failed: stdout %q missing one of the real secret values", result.Stdout)
	}

	// Both the env-exposed value AND the job-only value must be redacted —
	// cfg.SecretValues (populated by ApplySecrets) covers the job-only one
	// that cfg.SecretEnvVars alone would miss post-M13.
	maskSet := mergeSecretsForRedaction(cfg.SecretEnvVars, cfg.SecretValues)
	redacted := redactSecrets(result.Stdout, maskSet)
	if strings.Contains(redacted, envSecret) || strings.Contains(redacted, jobSecret) {
		t.Errorf("redacted stdout still contains a secret value: %q", redacted)
	}
}
