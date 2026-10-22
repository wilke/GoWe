package cwltool

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// --- ApplySecrets: env exposure (M13: limited to SecretEnvNames) -----------

func TestApplySecrets_EnvExposureLimitedToSecretEnvNames(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	task := &model.Task{
		ID:  "task_1",
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets: map[string]string{
				"HF_TOKEN": "hf_abc",
				"INPUT_PW": "hunter2", // job-only, must stay out of the env
			},
			SecretEnvNames: []string{"HF_TOKEN"},
		},
	}
	cfg := &Config{}

	if _, err := ApplySecrets(cfg, task, logger); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	if len(cfg.SecretEnvVars) != 1 || cfg.SecretEnvVars["HF_TOKEN"] != "hf_abc" {
		t.Errorf("cfg.SecretEnvVars = %v, want only {HF_TOKEN: hf_abc}", cfg.SecretEnvVars)
	}
	if _, leaked := cfg.SecretEnvVars["INPUT_PW"]; leaked {
		t.Errorf("cfg.SecretEnvVars leaked a job-only (non-SecretEnvNames) secret: %v", cfg.SecretEnvVars)
	}
}

func TestApplySecrets_NoSecretEnvNames_NothingExposed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	task := &model.Task{
		ID:  "task_1",
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets: map[string]string{"INPUT_PW": "hunter2"},
			// SecretEnvNames empty: cwltool:Secrets-only task.
		},
	}
	cfg := &Config{}

	if _, err := ApplySecrets(cfg, task, logger); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	if len(cfg.SecretEnvVars) != 0 {
		t.Errorf("cfg.SecretEnvVars = %v, want empty", cfg.SecretEnvVars)
	}
}

// TestApplySecrets_TaskWinsOnCollision verifies a task secret overrides an
// existing cfg.SecretEnvVars entry of the same name (e.g. a worker-level
// --secret default), matching the pre-#260-fix-round mergeTaskSecrets
// behavior, and that a collision is logged by name only.
func TestApplySecrets_TaskWinsOnCollision(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	task := &model.Task{
		ID:  "task_1",
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"HF_TOKEN": "submission-level-value-654321"},
			SecretEnvNames: []string{"HF_TOKEN"},
		},
	}
	cfg := &Config{SecretEnvVars: map[string]string{"HF_TOKEN": "worker-level-value-123456"}}

	if _, err := ApplySecrets(cfg, task, logger); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	if cfg.SecretEnvVars["HF_TOKEN"] != "submission-level-value-654321" {
		t.Errorf("task secret should win: got %q", cfg.SecretEnvVars["HF_TOKEN"])
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "HF_TOKEN") {
		t.Errorf("log output = %q, want it to name HF_TOKEN", logOutput)
	}
	if strings.Contains(logOutput, "worker-level-value-123456") || strings.Contains(logOutput, "submission-level-value-654321") {
		t.Errorf("log output leaked a secret value: %q", logOutput)
	}
}

// TestApplySecrets_DoesNotMutateSharedSecretEnvVars guards against a shared
// map (e.g. the worker's own w.secrets) being mutated in place.
func TestApplySecrets_DoesNotMutateSharedSecretEnvVars(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shared := map[string]string{"HF_TOKEN": "hf_abc"}
	task := &model.Task{
		ID:  "task_1",
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"API_KEY": "sub_xyz"},
			SecretEnvNames: []string{"API_KEY"},
		},
	}
	cfg := &Config{SecretEnvVars: shared}

	if _, err := ApplySecrets(cfg, task, logger); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	if len(shared) != 1 {
		t.Fatalf("shared map was mutated: %v", shared)
	}
	if _, ok := shared["API_KEY"]; ok {
		t.Error("shared map leaked API_KEY")
	}
}

// --- ApplySecrets: job re-injection -----------------------------------------

func TestApplySecrets_JobReinjection(t *testing.T) {
	tests := []struct {
		name       string
		task       *model.Task
		wantJob    map[string]any
		wantErrHas string
	}{
		{
			name: "nil RuntimeHints: job returned unchanged",
			task: &model.Task{
				Job: map[string]any{"password": model.SecretInputPlaceholder},
			},
			wantJob: map[string]any{"password": model.SecretInputPlaceholder},
		},
		{
			name: "no SecretInputs entries: job returned unchanged",
			task: &model.Task{
				Job:          map[string]any{"password": model.SecretInputPlaceholder},
				RuntimeHints: &model.RuntimeHints{},
			},
			wantJob: map[string]any{"password": model.SecretInputPlaceholder},
		},
		{
			name: "matching entry: returned job has the real secret",
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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			job, err := ApplySecrets(cfg, tt.task, logger)
			if tt.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("ApplySecrets() error = %v, want it to contain %q", err, tt.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplySecrets() unexpected error: %v", err)
			}
			for k, want := range tt.wantJob {
				if got := job[k]; got != want {
					t.Errorf("job[%q] = %v, want %v", k, got, want)
				}
			}
		})
	}
}

// TestApplySecrets_NeverMutatesTaskJob is the core H4 invariant: task.Job
// (and therefore anything the server holds) must never be touched, only a
// copy returned.
func TestApplySecrets_NeverMutatesTaskJob(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	task := &model.Task{
		Job: map[string]any{"password": model.SecretInputPlaceholder},
		RuntimeHints: &model.RuntimeHints{
			SecretInputs: []string{"password=INPUT_PASSWORD"},
			Secrets:      map[string]string{"INPUT_PASSWORD": "hunter2"},
		},
	}
	cfg := &Config{}

	job, err := ApplySecrets(cfg, task, logger)
	if err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	if task.Job["password"] != model.SecretInputPlaceholder {
		t.Errorf("task.Job was mutated: %v", task.Job)
	}
	if job["password"] != "hunter2" {
		t.Errorf("returned job missing re-injected secret: %v", job)
	}
}

// --- ApplySecrets: SecretValues (log-masking superset) ---------------------

func TestApplySecrets_PopulatesSecretValuesForMasking(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	task := &model.Task{
		Job: map[string]any{"password": model.SecretInputPlaceholder},
		RuntimeHints: &model.RuntimeHints{
			Secrets: map[string]string{
				"HF_TOKEN":       "env-value",
				"INPUT_PASSWORD": "job-only-value",
			},
			SecretEnvNames: []string{"HF_TOKEN"},
			SecretInputs:   []string{"password=INPUT_PASSWORD"},
		},
	}
	cfg := &Config{}

	if _, err := ApplySecrets(cfg, task, logger); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	// SecretValues must cover BOTH the env-exposed and the job-only value —
	// it is the superset used purely for log redaction.
	if cfg.SecretValues["HF_TOKEN"] != "env-value" {
		t.Errorf("cfg.SecretValues missing env-exposed value: %v", cfg.SecretValues)
	}
	if cfg.SecretValues["INPUT_PASSWORD"] != "job-only-value" {
		t.Errorf("cfg.SecretValues missing job-only value: %v", cfg.SecretValues)
	}
}

// --- ApplySecrets: nil task / nil RuntimeHints guards -----------------------

func TestApplySecrets_NilTask_NoPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &Config{}
	if _, err := ApplySecrets(cfg, nil, logger); err != nil {
		t.Fatalf("ApplySecrets(nil task): %v", err)
	}
}

func TestApplySecrets_NilLogger_UsesDefault(t *testing.T) {
	task := &model.Task{
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"A": "va"},
			SecretEnvNames: []string{"A"},
		},
	}
	cfg := &Config{}
	if _, err := ApplySecrets(cfg, task, nil); err != nil {
		t.Fatalf("ApplySecrets(nil logger): %v", err)
	}
}
