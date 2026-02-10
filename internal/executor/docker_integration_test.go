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

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	runner := &osCommandRunner{}
	_, _, _, err := runner.Run(context.Background(), "docker", "info")
	if err != nil {
		t.Skip("Docker not available, skipping integration test")
	}
}

func TestDockerIntegration_EchoHello(t *testing.T) {
	skipIfNoDocker(t)

	e := NewDockerExecutor(t.TempDir(), newTestLogger())

	task := &model.Task{
		ID: "task_integ_echo",
		Inputs: map[string]any{
			"_base_command": []any{"echo", "hello from docker"},
			"_docker_image": "alpine:latest",
		},
		CreatedAt: time.Now(),
	}

	externalID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	t.Logf("externalID: %s", externalID)

	if !strings.Contains(task.Stdout, "hello from docker") {
		t.Errorf("Stdout = %q, want it to contain 'hello from docker'", task.Stdout)
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

func TestDockerIntegration_OutputFile(t *testing.T) {
	skipIfNoDocker(t)

	e := NewDockerExecutor(t.TempDir(), newTestLogger())

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

func TestDockerIntegration_FailingCommand(t *testing.T) {
	skipIfNoDocker(t)

	e := NewDockerExecutor(t.TempDir(), newTestLogger())

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
