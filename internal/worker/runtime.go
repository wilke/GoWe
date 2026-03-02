package worker

import (
	"context"

	"github.com/me/gowe/pkg/runtime"
)

// Runtime executes a task's command, optionally inside a container.
// This is a type alias for the shared runtime.Runtime interface.
type Runtime = runtime.Runtime

// RunSpec describes what to execute.
// This is a type alias for the shared runtime.RunSpec.
type RunSpec = runtime.RunSpec

// GPUConfig specifies GPU requirements for container execution.
// This is a type alias for the shared runtime.GPUConfig.
type GPUConfig = runtime.GPUConfig

// RunResult captures the output of an execution.
// This is a type alias for the shared runtime.RunResult.
type RunResult = runtime.RunResult

// CommandRunner abstracts command execution for testing.
// This is a type alias for the shared runtime.CommandRunner.
type CommandRunner = runtime.CommandRunner

// BareRuntime executes commands directly on the host.
type BareRuntime = runtime.Bare

// DockerRuntime executes commands inside Docker containers.
type DockerRuntime = runtime.Docker

// ApptainerRuntime executes commands inside Apptainer (Singularity) containers.
type ApptainerRuntime = runtime.Apptainer

// NewBareRuntime creates a BareRuntime.
func NewBareRuntime() *runtime.Bare {
	return runtime.NewBare()
}

// NewDockerRuntime creates a DockerRuntime.
func NewDockerRuntime() *runtime.Docker {
	return runtime.NewDocker()
}

// NewApptainerRuntime creates an ApptainerRuntime.
func NewApptainerRuntime() *runtime.Apptainer {
	return runtime.NewApptainer()
}

// NewRuntime creates a Runtime based on the runtime name.
func NewRuntime(name string) (Runtime, error) {
	return runtime.New(name)
}

// newBareRuntimeWithRunner creates a BareRuntime with a custom command runner (for testing).
func newBareRuntimeWithRunner(runner CommandRunner) *runtime.Bare {
	return runtime.NewBareWithRunner(runner)
}

// newDockerRuntimeWithRunner creates a DockerRuntime with a custom command runner (for testing).
func newDockerRuntimeWithRunner(runner CommandRunner) *runtime.Docker {
	return runtime.NewDockerWithRunner(runner)
}

// newApptainerRuntimeWithRunner creates an ApptainerRuntime with a custom command runner (for testing).
func newApptainerRuntimeWithRunner(runner CommandRunner) *runtime.Apptainer {
	return runtime.NewApptainerWithRunner(runner)
}

// Verify interface compliance.
var (
	_ Runtime = (*runtime.Bare)(nil)
	_ Runtime = (*runtime.Docker)(nil)
	_ Runtime = (*runtime.Apptainer)(nil)
)

// Backwards compatibility: check if context is done.
func contextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
