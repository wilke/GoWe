// Package runtime provides container execution abstractions for Docker, Apptainer, and bare execution.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Runtime executes commands, optionally inside a container.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// RunSpec describes what to execute.
type RunSpec struct {
	Image   string            // Container image (empty for bare execution)
	Command []string          // Command and arguments
	WorkDir string            // Working directory on the host
	Volumes map[string]string // host:container mount pairs
	Env     map[string]string // Environment variables
	GPU     GPUConfig         // GPU configuration
	Network NetworkConfig     // Network configuration
}

// GPUConfig specifies GPU requirements for container execution.
type GPUConfig struct {
	Enabled  bool   // Whether to enable GPU access
	DeviceID string // Specific GPU device (e.g., "0", "1", "0,1") - empty means all
}

// NetworkConfig specifies network requirements for container execution.
type NetworkConfig struct {
	Disabled bool // Whether to disable network access (--network none)
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

// New creates a Runtime based on the runtime name.
// Supported names: "docker", "apptainer", "none", "" (bare).
func New(name string) (Runtime, error) {
	switch name {
	case "docker":
		return NewDocker(), nil
	case "apptainer":
		return NewApptainer(), nil
	case "none", "":
		return NewBare(), nil
	default:
		return nil, fmt.Errorf("unknown runtime: %s", name)
	}
}
