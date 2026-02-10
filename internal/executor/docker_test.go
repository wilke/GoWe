package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// mockRunner records calls and returns canned responses.
type mockRunner struct {
	calls   []mockCall
	results []mockResult
	callIdx int
}

type mockCall struct {
	name string
	args []string
}

type mockResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (m *mockRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args})
	if m.callIdx >= len(m.results) {
		return "", "", -1, fmt.Errorf("unexpected call %d", m.callIdx)
	}
	r := m.results[m.callIdx]
	m.callIdx++
	return r.stdout, r.stderr, r.exitCode, r.err
}

func TestDockerExecutor_Type(t *testing.T) {
	e := NewDockerExecutor(t.TempDir(), newTestLogger())
	if got := e.Type(); got != model.ExecutorTypeContainer {
		t.Fatalf("Type() = %q, want %q", got, model.ExecutorTypeContainer)
	}
}

func TestDockerExecutor_SubmitSuccess(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "hello\n", stderr: "", exitCode: 0},
		},
	}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID: "task_docker_echo",
		Inputs: map[string]any{
			"_base_command": []any{"echo", "hello"},
			"_docker_image": "alpine:latest",
		},
		CreatedAt: time.Now(),
	}

	externalID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if externalID != "gowe-task_docker_echo" {
		t.Errorf("externalID = %q, want %q", externalID, "gowe-task_docker_echo")
	}
	if task.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want %q", task.Stdout, "hello\n")
	}
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", task.ExitCode)
	}

	// Verify docker was called with correct args.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 docker call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "docker" {
		t.Errorf("command = %q, want docker", call.name)
	}
	for _, want := range []string{"run", "--rm", "alpine:latest", "echo", "hello"} {
		found := false
		for _, a := range call.args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args %v missing %q", call.args, want)
		}
	}

	// Verify status.
	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	if state != model.TaskStateSuccess {
		t.Errorf("Status = %q, want SUCCESS", state)
	}
}

func TestDockerExecutor_SubmitFailure(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "", stderr: "error msg\n", exitCode: 1},
		},
	}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID: "task_docker_fail",
		Inputs: map[string]any{
			"_base_command": []any{"false"},
			"_docker_image": "alpine:latest",
		},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v (expected nil for non-zero exit)", err)
	}
	if task.ExitCode == nil || *task.ExitCode != 1 {
		t.Errorf("ExitCode = %v, want 1", task.ExitCode)
	}

	state, _ := e.Status(context.Background(), task)
	if state != model.TaskStateFailed {
		t.Errorf("Status = %q, want FAILED", state)
	}
}

func TestDockerExecutor_MissingImage(t *testing.T) {
	runner := &mockRunner{}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID:     "task_no_image",
		Inputs: map[string]any{"_base_command": []any{"echo"}},
	}
	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for missing _docker_image")
	}
	if !strings.Contains(err.Error(), "_docker_image") {
		t.Errorf("error = %q, want it to mention _docker_image", err.Error())
	}
}

func TestDockerExecutor_MissingCommand(t *testing.T) {
	runner := &mockRunner{}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID:     "task_no_cmd",
		Inputs: map[string]any{"_docker_image": "alpine:latest"},
	}
	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for missing _base_command")
	}
	if !strings.Contains(err.Error(), "_base_command") {
		t.Errorf("error = %q, want it to mention _base_command", err.Error())
	}
}

func TestDockerExecutor_Cancel(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "", stderr: "", exitCode: 0},
		},
	}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID:         "task_cancel",
		ExternalID: "gowe-task_cancel",
	}
	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	if runner.calls[0].args[0] != "rm" || runner.calls[0].args[1] != "-f" {
		t.Errorf("expected 'docker rm -f', got %v", runner.calls[0].args)
	}
}

func TestDockerExecutor_CancelNoExternalID(t *testing.T) {
	runner := &mockRunner{}
	e := newDockerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{ID: "task_no_ext"}
	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected 0 calls for empty ExternalID, got %d", len(runner.calls))
	}
}

func TestDockerExecutor_Logs(t *testing.T) {
	e := NewDockerExecutor(t.TempDir(), newTestLogger())
	task := &model.Task{
		ID:     "task_logs",
		Stdout: "out\n",
		Stderr: "err\n",
	}
	stdout, stderr, err := e.Logs(context.Background(), task)
	if err != nil {
		t.Fatalf("Logs error: %v", err)
	}
	if stdout != "out\n" {
		t.Errorf("stdout = %q, want %q", stdout, "out\n")
	}
	if stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", stderr, "err\n")
	}
}

func TestDockerExecutor_OutputGlob(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "", stderr: "", exitCode: 0},
		},
	}
	tmpDir := t.TempDir()
	e := newDockerExecutorWithRunner(tmpDir, newTestLogger(), runner)

	task := &model.Task{
		ID: "task_glob",
		Inputs: map[string]any{
			"_base_command": []any{"echo"},
			"_docker_image": "alpine:latest",
			"_output_globs": map[string]any{
				"result": "*.txt",
			},
		},
		CreatedAt: time.Now(),
	}

	// Pre-create a file in taskDir to simulate container output.
	taskDir := filepath.Join(tmpDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "output.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	result, ok := task.Outputs["result"]
	if !ok {
		t.Fatal("task.Outputs[\"result\"] not set")
	}
	path, ok := result.(string)
	if !ok || !strings.HasSuffix(path, "output.txt") {
		t.Errorf("result = %v, want path ending in output.txt", result)
	}
}
