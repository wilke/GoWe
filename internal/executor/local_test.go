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
