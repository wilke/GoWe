package worker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Runtime executes a task's command, optionally inside a container.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// RunSpec describes what to execute.
type RunSpec struct {
	Image   string            // Container image (empty for bare execution)
	Command []string          // Command and arguments
	WorkDir string            // Working directory on the host
	Volumes map[string]string // host:container mount pairs
}

// RunResult captures the output of an execution.
type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// osCommandRunner is the real implementation using os/exec.
type osCommandRunner struct{}

func (r *osCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	switch e := runErr.(type) {
	case nil:
		return stdout, stderr, 0, nil
	case *exec.ExitError:
		return stdout, stderr, e.ExitCode(), nil
	default:
		return stdout, stderr, -1, runErr
	}
}

// BareRuntime executes commands directly on the host.
type BareRuntime struct {
	runner CommandRunner
}

// NewBareRuntime creates a BareRuntime.
func NewBareRuntime() *BareRuntime {
	return &BareRuntime{runner: &osCommandRunner{}}
}

func newBareRuntimeWithRunner(runner CommandRunner) *BareRuntime {
	return &BareRuntime{runner: runner}
}

func (r *BareRuntime) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("bare runtime: empty command")
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.WorkDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	result := RunResult{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	switch e := runErr.(type) {
	case nil:
		result.ExitCode = 0
	case *exec.ExitError:
		result.ExitCode = e.ExitCode()
	default:
		return result, fmt.Errorf("bare runtime: %w", runErr)
	}

	return result, nil
}

// DockerRuntime executes commands inside Docker containers.
type DockerRuntime struct {
	runner CommandRunner
}

// NewDockerRuntime creates a DockerRuntime.
func NewDockerRuntime() *DockerRuntime {
	return &DockerRuntime{runner: &osCommandRunner{}}
}

func newDockerRuntimeWithRunner(runner CommandRunner) *DockerRuntime {
	return &DockerRuntime{runner: runner}
}

func (r *DockerRuntime) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.Image == "" {
		return RunResult{}, fmt.Errorf("docker runtime: image is required")
	}
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("docker runtime: empty command")
	}

	args := []string{
		"run", "--rm",
		"-v", spec.WorkDir + ":/work",
		"-w", "/work",
	}
	for hostPath, containerPath := range spec.Volumes {
		args = append(args, "-v", hostPath+":"+containerPath)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	stdout, stderr, exitCode, err := r.runner.Run(ctx, "docker", args...)
	if err != nil {
		return RunResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
			fmt.Errorf("docker runtime: %w", err)
	}

	return RunResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// ApptainerRuntime executes commands inside Apptainer (Singularity) containers.
type ApptainerRuntime struct {
	runner CommandRunner
}

// NewApptainerRuntime creates an ApptainerRuntime.
func NewApptainerRuntime() *ApptainerRuntime {
	return &ApptainerRuntime{runner: &osCommandRunner{}}
}

func newApptainerRuntimeWithRunner(runner CommandRunner) *ApptainerRuntime {
	return &ApptainerRuntime{runner: runner}
}

func (r *ApptainerRuntime) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.Image == "" {
		return RunResult{}, fmt.Errorf("apptainer runtime: image is required")
	}
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("apptainer runtime: empty command")
	}

	args := []string{
		"exec",
		"--bind", spec.WorkDir + ":/work",
		"--pwd", "/work",
	}
	for hostPath, containerPath := range spec.Volumes {
		args = append(args, "--bind", hostPath+":"+containerPath)
	}
	args = append(args, "docker://"+spec.Image)
	args = append(args, spec.Command...)

	stdout, stderr, exitCode, err := r.runner.Run(ctx, "apptainer", args...)
	if err != nil {
		return RunResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
			fmt.Errorf("apptainer runtime: %w", err)
	}

	return RunResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// NewRuntime creates a Runtime based on the runtime name.
func NewRuntime(name string) (Runtime, error) {
	switch name {
	case "docker":
		return NewDockerRuntime(), nil
	case "apptainer":
		return NewApptainerRuntime(), nil
	case "none", "":
		return NewBareRuntime(), nil
	default:
		return nil, fmt.Errorf("unknown runtime: %s", name)
	}
}
