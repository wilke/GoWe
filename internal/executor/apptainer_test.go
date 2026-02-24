package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestApptainerExecutor_Type(t *testing.T) {
	e := NewApptainerExecutor(t.TempDir(), newTestLogger())
	if got := e.Type(); got != model.ExecutorTypeApptainer {
		t.Fatalf("Type() = %q, want %q", got, model.ExecutorTypeApptainer)
	}
}

func TestApptainerExecutor_SubmitSuccess(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "hello\n", stderr: "", exitCode: 0},
		},
	}
	e := newApptainerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID: "task_apptainer_echo",
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
	if externalID != "apptainer-task_apptainer_echo" {
		t.Errorf("externalID = %q, want %q", externalID, "apptainer-task_apptainer_echo")
	}
	if task.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want %q", task.Stdout, "hello\n")
	}
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", task.ExitCode)
	}

	// Verify apptainer was called with correct args.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 apptainer call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "apptainer" {
		t.Errorf("command = %q, want apptainer", call.name)
	}
	// Check key args are present.
	for _, want := range []string{"exec", "--pwd", "/work", "docker://alpine:latest", "echo", "hello"} {
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
	// Verify --bind is used (not -v).
	foundBind := false
	for _, a := range call.args {
		if a == "--bind" {
			foundBind = true
			break
		}
	}
	if !foundBind {
		t.Errorf("expected --bind in args, got %v", call.args)
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

func TestApptainerExecutor_SubmitFailure(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "", stderr: "error msg\n", exitCode: 1},
		},
	}
	e := newApptainerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

	task := &model.Task{
		ID: "task_apptainer_fail",
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

func TestApptainerExecutor_MissingImage(t *testing.T) {
	runner := &mockRunner{}
	e := newApptainerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

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

func TestApptainerExecutor_MissingCommand(t *testing.T) {
	runner := &mockRunner{}
	e := newApptainerExecutorWithRunner(t.TempDir(), newTestLogger(), runner)

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

func TestApptainerExecutor_Logs(t *testing.T) {
	e := NewApptainerExecutor(t.TempDir(), newTestLogger())
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

func TestApptainerExecutor_DirectoryMount(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "ok\n", stderr: "", exitCode: 0},
		},
	}
	tmpDir := t.TempDir()
	e := newApptainerExecutorWithRunner(tmpDir, newTestLogger(), runner)

	mountDir := filepath.Join(tmpDir, "mount_src")
	os.MkdirAll(mountDir, 0o755)

	task := &model.Task{
		ID: "task_dir_mount",
		Inputs: map[string]any{
			"_base_command": []any{"ls"},
			"_docker_image": "alpine:latest",
			"output_path": map[string]any{
				"class":    "Directory",
				"location": "file://" + mountDir,
			},
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	// Verify bind mount args include the Directory mount.
	call := runner.calls[0]
	foundMount := false
	for i, a := range call.args {
		if a == "--bind" && i+1 < len(call.args) && strings.Contains(call.args[i+1], mountDir+":/work/output_path") {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Errorf("expected bind mount for output_path, got args: %v", call.args)
	}
}

func TestApptainerExecutor_OutputGlob(t *testing.T) {
	runner := &mockRunner{
		results: []mockResult{
			{stdout: "", stderr: "", exitCode: 0},
		},
	}
	tmpDir := t.TempDir()
	e := newApptainerExecutorWithRunner(tmpDir, newTestLogger(), runner)

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
