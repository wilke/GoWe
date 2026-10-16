package cwlrunner

import (
	"context"
	"github.com/me/gowe/internal/cwlexpr"
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
func (r *Runner) executeLocalWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, workDir string, runtime *cwlexpr.RuntimeContext) (*ExecutionResult, error) {
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
		Runtime:         runtime,
		PreserveEnv:     r.PreserveEnv,
		InheritHostEnv:  r.PreserveEntireEnv,
		EnvVars:         r.EnvVars,
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
// res: effective resources from cwltool.ResolveResources (same rules as plain steps, #316):
// Cores/RamMB are set only when the tool declares coresMin/ramMin.
func (r *Runner) executeInDockerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string, res toolexec.ResourceConfig, runtime *cwlexpr.RuntimeContext) (*ExecutionResult, error) {
	result, err := r.runContainer(ctx, r.containerOptions(toolexec.ModeDocker, tool, cmdResult, inputs, dockerImage, workDir, containerMounts, dockerOutputDir, res, runtime))
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
// res: effective resources from cwltool.ResolveResources (#316); Cores > 0 becomes --cpus (Apptainer additionally requires cgroups v2 unified mode, which
// cwl-runner does not currently detect/set -- see ApptainerCgroups in internal/worker for the
// equivalent worker-side detection; --cpus is a no-op here until that's wired up for cwl-runner too).
func (r *Runner) executeInApptainerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string, res toolexec.ResourceConfig, runtime *cwlexpr.RuntimeContext) (*ExecutionResult, error) {
	result, err := r.runContainer(ctx, r.containerOptions(toolexec.ModeApptainer, tool, cmdResult, inputs, dockerImage, workDir, containerMounts, dockerOutputDir, res, runtime))
	if err != nil {
		return nil, err
	}
	return toExecutionResult(result), nil
}

// containerOptions builds the toolexec.Options for a containerized execution
// (Docker or Apptainer). Kept separate so tests can inspect what a scatter
// iteration actually passes to toolexec (#316).
func (r *Runner) containerOptions(mode toolexec.ExecutionMode, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []iwdr.ContainerMount, dockerOutputDir string, res toolexec.ResourceConfig, runtime *cwlexpr.RuntimeContext) *toolexec.Options {
	opts := &toolexec.Options{
		Tool:            tool,
		Command:         cmdResult,
		Inputs:          inputs,
		WorkDir:         workDir,
		OutDir:          r.OutDir,
		Mode:            mode,
		DockerImage:     dockerImage,
		ContainerMounts: containerMounts,
		DockerOutputDir: dockerOutputDir,
		Namespaces:      r.namespaces,
		JobRequirements: r.jobRequirements,
		Runtime:         runtime,
		Resources:       res,
		ExtraBinds:      r.ExtraBinds,
		EnvVars:         r.EnvVars,
	}
	if mode == toolexec.ModeApptainer {
		opts.ImageDir = r.ImageDir // #312: scatter iterations resolve relative .sif paths here too
	}
	return opts
}

// runContainer dispatches opts to the configured executor (real toolexec
// unless a test installed r.execute).
func (r *Runner) runContainer(ctx context.Context, opts *toolexec.Options) (*toolexec.Result, error) {
	if r.execute != nil {
		return r.execute(ctx, opts)
	}
	return toolexec.NewExecutor(r.logger).Execute(ctx, opts)
}
