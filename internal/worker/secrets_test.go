package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// --- mergeTaskSecrets ---------------------------------------------------

func TestMergeTaskSecrets(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name        string
		base        map[string]string
		taskSecrets map[string]string
		want        map[string]string
	}{
		{
			name:        "no task secrets: base passed through unchanged",
			base:        map[string]string{"HF_TOKEN": "hf_abc"},
			taskSecrets: nil,
			want:        map[string]string{"HF_TOKEN": "hf_abc"},
		},
		{
			name:        "task secrets merged in, disjoint names",
			base:        map[string]string{"HF_TOKEN": "hf_abc"},
			taskSecrets: map[string]string{"API_KEY": "sub_xyz"},
			want:        map[string]string{"HF_TOKEN": "hf_abc", "API_KEY": "sub_xyz"},
		},
		{
			name:        "task secret wins on name collision with worker-level secret",
			base:        map[string]string{"HF_TOKEN": "worker-level"},
			taskSecrets: map[string]string{"HF_TOKEN": "submission-level"},
			want:        map[string]string{"HF_TOKEN": "submission-level"},
		},
		{
			name:        "nil base",
			base:        nil,
			taskSecrets: map[string]string{"A": "va"},
			want:        map[string]string{"A": "va"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeTaskSecrets(tt.base, tt.taskSecrets, logger)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeTaskSecrets() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestMergeTaskSecrets_CollisionLogsWARN_NamesOnly verifies the collision
// WARN names the secret but never its value.
func TestMergeTaskSecrets_CollisionLogsWARN_NamesOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	got := mergeTaskSecrets(
		map[string]string{"HF_TOKEN": "worker-level-value-123456"},
		map[string]string{"HF_TOKEN": "submission-level-value-654321"},
		logger,
	)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "HF_TOKEN") {
		t.Errorf("log output = %q, want it to name HF_TOKEN", logOutput)
	}
	if strings.Contains(logOutput, "worker-level-value-123456") || strings.Contains(logOutput, "submission-level-value-654321") {
		t.Errorf("log output leaked a secret value: %q", logOutput)
	}
	if got["HF_TOKEN"] != "submission-level-value-654321" {
		t.Errorf("task secret should win: got %q", got["HF_TOKEN"])
	}
}

// TestMergeTaskSecrets_DoesNotMutateSharedMap guards against a shared
// secrets map (e.g. the worker's own w.secrets) being mutated in place.
func TestMergeTaskSecrets_DoesNotMutateSharedMap(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shared := map[string]string{"HF_TOKEN": "hf_abc"}

	got := mergeTaskSecrets(shared, map[string]string{"API_KEY": "sub_xyz"}, logger)

	if len(shared) != 1 {
		t.Fatalf("shared map was mutated: %v", shared)
	}
	if _, ok := shared["API_KEY"]; ok {
		t.Error("shared map leaked API_KEY")
	}
	if got["API_KEY"] != "sub_xyz" {
		t.Errorf("returned map missing merged secret: %v", got)
	}
}

// --- reinjectSecretInputs ------------------------------------------------

func TestReinjectSecretInputs(t *testing.T) {
	tests := []struct {
		name       string
		task       *model.Task
		wantJob    map[string]any
		wantErrHas string
	}{
		{
			name: "nil RuntimeHints: no-op",
			task: &model.Task{
				Job: map[string]any{"password": model.SecretInputPlaceholder},
			},
			wantJob: map[string]any{"password": model.SecretInputPlaceholder},
		},
		{
			name: "no SecretInputs entries: no-op",
			task: &model.Task{
				Job:          map[string]any{"password": model.SecretInputPlaceholder},
				RuntimeHints: &model.RuntimeHints{},
			},
			wantJob: map[string]any{"password": model.SecretInputPlaceholder},
		},
		{
			name: "matching entry: job value replaced with the real secret",
			task: &model.Task{
				Job: map[string]any{"password": model.SecretInputPlaceholder, "other": "unrelated"},
				RuntimeHints: &model.RuntimeHints{
					SecretInputs: []string{"password=INPUT_PASSWORD"},
					Secrets:      map[string]string{"INPUT_PASSWORD": "hunter2"},
				},
			},
			wantJob: map[string]any{"password": "hunter2", "other": "unrelated"},
		},
		{
			name: "missing secret value: error naming the step input id",
			task: &model.Task{
				Job: map[string]any{"password": model.SecretInputPlaceholder},
				RuntimeHints: &model.RuntimeHints{
					SecretInputs: []string{"password=INPUT_PASSWORD"},
					Secrets:      map[string]string{}, // scrubbed or never delivered
				},
			},
			wantErrHas: "password",
		},
		{
			name: "malformed entry: error",
			task: &model.Task{
				Job: map[string]any{},
				RuntimeHints: &model.RuntimeHints{
					SecretInputs: []string{"not-a-kv-pair"},
				},
			},
			wantErrHas: "malformed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reinjectSecretInputs(tt.task)
			if tt.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("reinjectSecretInputs() error = %v, want it to contain %q", err, tt.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("reinjectSecretInputs() unexpected error: %v", err)
			}
			for k, want := range tt.wantJob {
				if got := tt.task.Job[k]; got != want {
					t.Errorf("task.Job[%q] = %v, want %v", k, got, want)
				}
			}
		})
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
	redacted := redactSecrets(result.Stdout, cfg.SecretEnvVars)
	if strings.Contains(redacted, secretValue) {
		t.Errorf("redacted stdout still contains the secret value: %q", redacted)
	}
	if !strings.Contains(redacted, "***REDACTED***") {
		t.Errorf("redacted stdout = %q, want a redaction marker", redacted)
	}
}
