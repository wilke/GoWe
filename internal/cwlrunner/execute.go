package cwlrunner

import (
	"context"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
)

// ExecutionResult holds the result of a tool execution including metrics.
// This is an alias for toolexec.Result for backward compatibility.
type ExecutionResult struct {
	Outputs      map[string]any
	ExitCode     int
	PeakMemoryKB int64
	StartTime    time.Time
	Duration     time.Duration
}

// toExecutionResult converts a toolexec.Result to ExecutionResult.
func toExecutionResult(r *toolexec.Result) *ExecutionResult {
	return &ExecutionResult{
		Outputs:      r.Outputs,
		ExitCode:     r.ExitCode,
		PeakMemoryKB: r.PeakMemoryKB,
		StartTime:    r.StartTime,
		Duration:     r.Duration,
	}
}

// executeLocalWithWorkDir executes a tool locally without Docker in the specified work directory.
func (r *Runner) executeLocalWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, workDir string) (*ExecutionResult, error) {
	executor := toolexec.NewExecutor(r.logger)
	result, err := executor.Execute(ctx, &toolexec.Options{
		Tool:            tool,
		Command:         cmdResult,
		Inputs:          inputs,
		WorkDir:         workDir,
		OutDir:          r.OutDir,
		Mode:            toolexec.ModeLocal,
		Namespaces:      r.namespaces,
		JobRequirements: r.jobRequirements,
	})
	if err != nil {
		return nil, err
	}
	return toExecutionResult(result), nil
}

// executeInDockerWithWorkDir executes a tool in a Docker container with the specified work directory.
// Note: For containerized execution, resource usage captures the container CLI process overhead,
// not the application inside the container.
// containerMounts: files to mount at absolute paths inside container (from InitialWorkDirRequirement).
// dockerOutputDir: custom output directory inside container (from dockerOutputDirectory).
// cores: evaluated ResourceRequirement cores (see resourceCoresWeight); passed through as the
// container --cpus limit when > 0. Same value used to size the --cores weighted-semaphore
// acquisition for this execution, evaluated once by the caller.
func (r *Runner) executeInDockerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string, cores int) (*ExecutionResult, error) {
	executor := toolexec.NewExecutor(r.logger)
	result, err := executor.Execute(ctx, &toolexec.Options{
		Tool:            tool,
		Command:         cmdResult,
		Inputs:          inputs,
		WorkDir:         workDir,
		OutDir:          r.OutDir,
		Mode:            toolexec.ModeDocker,
		DockerImage:     dockerImage,
		ContainerMounts: containerMounts,
		DockerOutputDir: dockerOutputDir,
		Namespaces:      r.namespaces,
		JobRequirements: r.jobRequirements,
		Resources:       toolexec.ResourceConfig{Cores: cores},
	})
	if err != nil {
		return nil, err
	}
	return toExecutionResult(result), nil
}

// executeInApptainerWithWorkDir executes a tool in an Apptainer container with the specified work directory.
// Note: For containerized execution, resource usage captures the container CLI process overhead,
// not the application inside the container.
// containerMounts: files to mount at absolute paths inside container (from InitialWorkDirRequirement).
// dockerOutputDir: custom output directory inside container (from dockerOutputDirectory).
// cores: evaluated ResourceRequirement cores (see resourceCoresWeight); passed through as the
// container --cpus limit when > 0 (Apptainer additionally requires cgroups v2 unified mode, which
// cwl-runner does not currently detect/set -- see ApptainerCgroups in internal/worker for the
// equivalent worker-side detection; --cpus is a no-op here until that's wired up for cwl-runner too).
func (r *Runner) executeInApptainerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string, cores int) (*ExecutionResult, error) {
	executor := toolexec.NewExecutor(r.logger)
	result, err := executor.Execute(ctx, &toolexec.Options{
		Tool:            tool,
		Command:         cmdResult,
		Inputs:          inputs,
		WorkDir:         workDir,
		OutDir:          r.OutDir,
		Mode:            toolexec.ModeApptainer,
		DockerImage:     dockerImage,
		ContainerMounts: containerMounts,
		DockerOutputDir: dockerOutputDir,
		Namespaces:      r.namespaces,
		JobRequirements: r.jobRequirements,
		Resources:       toolexec.ResourceConfig{Cores: cores},
	})
	if err != nil {
		return nil, err
	}
	return toExecutionResult(result), nil
}
