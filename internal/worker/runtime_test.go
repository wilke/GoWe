package worker

import (
	"context"
	"fmt"
	"testing"
)

// mockCommandRunner records calls and returns canned responses.
type mockCommandRunner struct {
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

func (m *mockCommandRunner) Run(_ context.Context, name string, args ...string) (string, string, int, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args})
	if m.callIdx >= len(m.results) {
		return "", "", -1, fmt.Errorf("unexpected call %d", m.callIdx)
	}
	r := m.results[m.callIdx]
	m.callIdx++
	return r.stdout, r.stderr, r.exitCode, r.err
}

func TestBareRuntime_Run(t *testing.T) {
	tmpDir := t.TempDir()
	rt := NewBareRuntime()

	result, err := rt.Run(context.Background(), RunSpec{
		Command: []string{"echo", "hello"},
		WorkDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want hello\\n", result.Stdout)
	}
}

func TestBareRuntime_EmptyCommand(t *testing.T) {
	rt := NewBareRuntime()
	_, err := rt.Run(context.Background(), RunSpec{
		Command: []string{},
		WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestDockerRuntime_Run(t *testing.T) {
	runner := &mockCommandRunner{
		results: []mockResult{
			{stdout: "container output\n", exitCode: 0},
		},
	}
	rt := newDockerRuntimeWithRunner(runner)

	result, err := rt.Run(context.Background(), RunSpec{
		Image:   "alpine:latest",
		Command: []string{"echo", "hello"},
		WorkDir: "/tmp/work",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "container output\n" {
		t.Errorf("stdout = %q, want container output\\n", result.Stdout)
	}

	// Verify docker args.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "docker" {
		t.Errorf("command = %q, want docker", call.name)
	}
	// Should contain run, --rm, image, and command.
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
}

func TestDockerRuntime_MissingImage(t *testing.T) {
	runner := &mockCommandRunner{}
	rt := newDockerRuntimeWithRunner(runner)

	_, err := rt.Run(context.Background(), RunSpec{
		Command: []string{"echo"},
		WorkDir: "/tmp/work",
	})
	if err == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestApptainerRuntime_Run(t *testing.T) {
	runner := &mockCommandRunner{
		results: []mockResult{
			{stdout: "apptainer output\n", exitCode: 0},
		},
	}
	rt := newApptainerRuntimeWithRunner(runner)

	result, err := rt.Run(context.Background(), RunSpec{
		Image:   "ubuntu:22.04",
		Command: []string{"echo", "test"},
		WorkDir: "/tmp/work",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}

	call := runner.calls[0]
	if call.name != "apptainer" {
		t.Errorf("command = %q, want apptainer", call.name)
	}
	// Should use docker:// prefix.
	found := false
	for _, a := range call.args {
		if a == "docker://ubuntu:22.04" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("args %v missing docker://ubuntu:22.04", call.args)
	}
}

func TestDockerRuntime_GPU(t *testing.T) {
	runner := &mockCommandRunner{
		results: []mockResult{
			{stdout: "gpu output\n", exitCode: 0},
		},
	}
	rt := newDockerRuntimeWithRunner(runner)

	_, err := rt.Run(context.Background(), RunSpec{
		Image:   "nvidia/cuda:12.0-base",
		Command: []string{"nvidia-smi"},
		WorkDir: "/tmp/work",
		GPU: GPUConfig{
			Enabled:  true,
			DeviceID: "0",
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	call := runner.calls[0]
	// Should contain --gpus with device specification.
	foundGPUs := false
	foundDevice := false
	for i, a := range call.args {
		if a == "--gpus" {
			foundGPUs = true
			if i+1 < len(call.args) && call.args[i+1] == `"device=0"` {
				foundDevice = true
			}
		}
	}
	if !foundGPUs {
		t.Errorf("args %v missing --gpus", call.args)
	}
	if !foundDevice {
		t.Errorf("args %v missing device=0", call.args)
	}
}

func TestDockerRuntime_GPU_All(t *testing.T) {
	runner := &mockCommandRunner{
		results: []mockResult{
			{stdout: "gpu output\n", exitCode: 0},
		},
	}
	rt := newDockerRuntimeWithRunner(runner)

	_, err := rt.Run(context.Background(), RunSpec{
		Image:   "nvidia/cuda:12.0-base",
		Command: []string{"nvidia-smi"},
		WorkDir: "/tmp/work",
		GPU: GPUConfig{
			Enabled: true,
			// Empty DeviceID means all GPUs
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	call := runner.calls[0]
	// Should contain --gpus all.
	foundAll := false
	for i, a := range call.args {
		if a == "--gpus" && i+1 < len(call.args) && call.args[i+1] == "all" {
			foundAll = true
		}
	}
	if !foundAll {
		t.Errorf("args %v missing --gpus all", call.args)
	}
}

func TestApptainerRuntime_GPU(t *testing.T) {
	runner := &mockCommandRunner{
		results: []mockResult{
			{stdout: "gpu output\n", exitCode: 0},
		},
	}
	rt := newApptainerRuntimeWithRunner(runner)

	_, err := rt.Run(context.Background(), RunSpec{
		Image:   "nvidia/cuda:12.0-base",
		Command: []string{"nvidia-smi"},
		WorkDir: "/tmp/work",
		GPU: GPUConfig{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	call := runner.calls[0]
	// Should contain --nv for NVIDIA GPU support.
	foundNV := false
	for _, a := range call.args {
		if a == "--nv" {
			foundNV = true
			break
		}
	}
	if !foundNV {
		t.Errorf("args %v missing --nv", call.args)
	}
}

func TestNewRuntime(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"docker", false},
		{"apptainer", false},
		{"none", false},
		{"", false},
		{"unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRuntime(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewRuntime(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}
