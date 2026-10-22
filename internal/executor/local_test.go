package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLocalExecutor_Type(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())
	if got := e.Type(); got != model.ExecutorTypeLocal {
		t.Fatalf("Type() = %q, want %q", got, model.ExecutorTypeLocal)
	}
}

func TestLocalExecutor_EchoHello(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID:        "task_test_echo",
		Inputs:    map[string]any{"_base_command": []any{"echo", "hello"}},
		CreatedAt: time.Now(),
	}

	externalID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	if task.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want %q", task.Stdout, "hello\n")
	}

	if task.ExitCode == nil {
		t.Fatal("ExitCode is nil, expected 0")
	}
	if *task.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", *task.ExitCode)
	}

	// externalID should be an existing directory.
	info, err := os.Stat(externalID)
	if err != nil {
		t.Fatalf("externalID directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("externalID %q is not a directory", externalID)
	}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if state != model.TaskStateSuccess {
		t.Errorf("Status = %q, want %q", state, model.TaskStateSuccess)
	}
}

func TestLocalExecutor_FailingCommand(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID:        "task_test_fail",
		Inputs:    map[string]any{"_base_command": []any{"false"}},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v (expected nil — command ran but failed)", err)
	}

	if task.ExitCode == nil {
		t.Fatal("ExitCode is nil, expected non-zero")
	}
	if *task.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero")
	}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if state != model.TaskStateFailed {
		t.Errorf("Status = %q, want %q", state, model.TaskStateFailed)
	}
}

func TestLocalExecutor_MissingCommand(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID:        "task_test_nocommand",
		Inputs:    map[string]any{},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("Submit should return error for missing _base_command")
	}
}

func TestLocalExecutor_OutputGlob(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID: "task_test_glob",
		Inputs: map[string]any{
			"_base_command": []any{"sh", "-c", "echo content > output.txt"},
			"_output_globs": map[string]any{
				"result": "*.txt",
			},
		},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	result, ok := task.Outputs["result"]
	if !ok {
		t.Fatal("task.Outputs[\"result\"] not set")
	}

	path, ok := result.(string)
	if !ok {
		t.Fatalf("expected result to be a string, got %T", result)
	}
	if !strings.HasSuffix(path, "output.txt") {
		t.Errorf("result path %q does not end with output.txt", path)
	}
}

func TestLocalExecutor_ContextCancellation(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())

	task := &model.Task{
		ID:        "task_test_cancel",
		Inputs:    map[string]any{"_base_command": []any{"sleep", "10"}},
		CreatedAt: time.Now(),
	}

	// Cancel the context shortly after starting.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := e.Submit(ctx, task)

	// After cancellation the process should be killed.
	// cmd.Run returns a non-nil error when the context is cancelled,
	// which may surface as an exec.ExitError (signal: killed) or a
	// context error, depending on timing.
	if err == nil && task.ExitCode != nil && *task.ExitCode == 0 {
		t.Fatal("expected command to be terminated by context cancellation")
	}
}

func TestLocalExecutor_Logs(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID:        "task_test_logs",
		Inputs:    map[string]any{"_base_command": []any{"sh", "-c", "echo out; echo err >&2"}},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	stdout, stderr, err := e.Logs(context.Background(), task)
	if err != nil {
		t.Fatalf("Logs returned error: %v", err)
	}
	if stdout != "out\n" {
		t.Errorf("Logs stdout = %q, want %q", stdout, "out\n")
	}
	if stderr != "err\n" {
		t.Errorf("Logs stderr = %q, want %q", stderr, "err\n")
	}
}

// TestLocalExecutor_DeliversSecretEnv is the H4 regression test: before the
// #260 fix round, submitWithCWLTool built a cwltool.Config with no secret
// wiring at all, so a task dispatched to the local executor (the default
// when no workers are online) silently ran without any secret_env-delivered
// value. This drives the executor's real Submit()/HasTool() path (not
// cwltool.ExecuteTool directly) with a task carrying
// RuntimeHints.Secrets/SecretEnvNames and asserts the real value reached the
// process environment.
func TestLocalExecutor_DeliversSecretEnv(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	const secretValue = "local-executor-secret-0123456789"
	task := &model.Task{
		ID: "task_secret_env",
		Tool: map[string]any{
			"class":       "CommandLineTool",
			"baseCommand": []any{"sh", "-c", "echo \"got: $MY_TASK_SECRET\""},
		},
		Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"MY_TASK_SECRET": secretValue},
			SecretEnvNames: []string{"MY_TASK_SECRET"},
		},
		CreatedAt: time.Now(),
	}

	if _, err := e.Submit(context.Background(), task); err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if !strings.Contains(task.Stdout, secretValue) {
		t.Errorf("Stdout = %q, want it to contain the delivered secret value", task.Stdout)
	}
}

// TestLocalExecutor_ReinjectsCwltoolSecretsInput is the local-executor
// counterpart of internal/cwltool's IWDR re-injection test: a
// cwltool:Secrets-declared input consumed via
// InitialWorkDirRequirement/$(inputs.pw) must see the real value, not the
// literal model.SecretInputPlaceholder task.Job was persisted with.
func TestLocalExecutor_ReinjectsCwltoolSecretsInput(t *testing.T) {
	e := NewLocalExecutor(t.TempDir(), newTestLogger())

	const realSecret = "local-executor-reinjected-secret"
	task := &model.Task{
		ID: "task_secret_input",
		Tool: map[string]any{
			"class":       "CommandLineTool",
			"baseCommand": []any{"cat", "config.txt"},
			"inputs": map[string]any{
				"pw": map[string]any{"type": "string"},
			},
			"requirements": map[string]any{
				"InitialWorkDirRequirement": map[string]any{
					"listing": []any{
						map[string]any{
							"entryname": "config.txt",
							"entry":     "$(inputs.pw)",
						},
					},
				},
			},
		},
		Job: map[string]any{"pw": model.SecretInputPlaceholder},
		RuntimeHints: &model.RuntimeHints{
			Secrets:      map[string]string{"INPUT_PW": realSecret},
			SecretInputs: []string{"pw=INPUT_PW"},
		},
		CreatedAt: time.Now(),
	}

	if _, err := e.Submit(context.Background(), task); err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if !strings.Contains(task.Stdout, realSecret) {
		t.Errorf("Stdout = %q, want it to contain the re-injected secret value", task.Stdout)
	}
	// task.Job (what would be persisted) must stay untouched.
	if task.Job["pw"] != model.SecretInputPlaceholder {
		t.Errorf("task.Job[\"pw\"] = %v, want it to remain the placeholder", task.Job["pw"])
	}
}
