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
		Tool:       tool,
		Command:    cmdResult,
		Inputs:     inputs,
		WorkDir:    workDir,
		OutDir:     r.OutDir,
		Mode:       toolexec.ModeLocal,
		Namespaces: r.namespaces,
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
func (r *Runner) executeInDockerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string) (*ExecutionResult, error) {
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
func (r *Runner) executeInApptainerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string) (*ExecutionResult, error) {
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
	})
	if err != nil {
		return nil, err
	}
	return toExecutionResult(result), nil
}
