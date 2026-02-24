//go:build integration

package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func skipIfNoApptainer(t *testing.T) {
	t.Helper()
	runner := &osCommandRunner{}
	_, _, _, err := runner.Run(context.Background(), "apptainer", "version")
	if err != nil {
		t.Skip("Apptainer not available, skipping integration test")
	}
}

func TestApptainerIntegration_EchoHello(t *testing.T) {
	skipIfNoApptainer(t)

	e := NewApptainerExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID: "task_integ_echo",
		Inputs: map[string]any{
			"_base_command": []any{"echo", "hello from apptainer"},
			"_docker_image": "alpine:latest",
		},
		CreatedAt: time.Now(),
	}

	externalID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Logf("externalID: %s", externalID)

	if !strings.Contains(task.Stdout, "hello from apptainer") {
		t.Errorf("Stdout = %q, want it to contain 'hello from apptainer'", task.Stdout)
	}
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", task.ExitCode)
	}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state != model.TaskStateSuccess {
		t.Errorf("Status = %q, want SUCCESS", state)
	}
}

func TestApptainerIntegration_OutputFile(t *testing.T) {
	skipIfNoApptainer(t)

	e := NewApptainerExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID: "task_integ_output",
		Inputs: map[string]any{
			"_base_command": []any{"sh", "-c", "echo content > output.txt"},
			"_docker_image": "alpine:latest",
			"_output_globs": map[string]any{
				"result": "*.txt",
			},
		},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	result, ok := task.Outputs["result"]
	if !ok {
		t.Fatal("task.Outputs[\"result\"] not set")
	}
	path, ok := result.(string)
	if !ok {
		t.Fatalf("result is %T, want string", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "content") {
		t.Errorf("output file = %q, want it to contain 'content'", string(data))
	}
}

func TestApptainerIntegration_FailingCommand(t *testing.T) {
	skipIfNoApptainer(t)

	e := NewApptainerExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID: "task_integ_fail",
		Inputs: map[string]any{
			"_base_command": []any{"false"},
			"_docker_image": "alpine:latest",
		},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit: %v (expected nil for non-zero exit)", err)
	}
	if task.ExitCode == nil || *task.ExitCode == 0 {
		t.Errorf("ExitCode = %v, want non-zero", task.ExitCode)
	}

	state, _ := e.Status(context.Background(), task)
	if state != model.TaskStateFailed {
		t.Errorf("Status = %q, want FAILED", state)
	}
}

func TestApptainerIntegration_DirectoryMount(t *testing.T) {
	skipIfNoApptainer(t)

	tmpDir := t.TempDir()
	e := NewApptainerExecutor(tmpDir, newTestLogger())

	// Create a directory with a file to mount as input.
	inputDir := t.TempDir()
	if err := os.WriteFile(inputDir+"/data.txt", []byte("mounted"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &model.Task{
		ID: "task_integ_dir",
		Inputs: map[string]any{
			"_base_command": []any{"cat", "/work/input_dir/data.txt"},
			"_docker_image": "alpine:latest",
			"input_dir": map[string]any{
				"class":    "Directory",
				"location": "file://" + inputDir,
			},
		},
		CreatedAt: time.Now(),
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if !strings.Contains(task.Stdout, "mounted") {
		t.Errorf("Stdout = %q, want it to contain 'mounted'", task.Stdout)
	}
}
