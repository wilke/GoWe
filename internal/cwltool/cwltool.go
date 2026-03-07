// Package cwltool provides standalone CWL CommandLineTool execution.
// It extracts the proven preprocessing + execution pipeline from cwlrunner
// into a reusable function that can be called by both cwl-runner and
// the distributed worker/executor paths.
package cwltool

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
)

// Config holds configuration for ExecuteTool.
type Config struct {
	Logger               *slog.Logger
	CWLDir               string            // Directory containing the CWL file
	Namespaces           map[string]string  // Namespace prefix -> URI mappings
	ExpressionLib        []string           // JavaScript library from InlineJavascriptRequirement
	ContainerRuntime     string             // "docker", "apptainer", or "" (auto-detect)
	NoContainer          bool               // Force local execution even with DockerRequirement
	GPU                  toolexec.GPUConfig // GPU configuration
	DockerHostPathMap    map[string]string  // Container path -> host path for DinD
	ResolveSecondary     bool               // Resolve secondary files from tool definitions
	JobRequirements      []any              // cwl:requirements from job file
	OutDir               string             // Output directory for resolved output paths
	RemoveDefaultListings bool              // Remove listings when loadListing is default (for worker/executor mode)
}

// Result holds the result of tool execution.
type Result struct {
	Outputs  map[string]any
	ExitCode int
	Stdout   string
	Stderr   string
}

// ExecuteTool executes a CWL CommandLineTool with the given inputs.
// This is the standalone equivalent of cwlrunner.Runner.executeToolWithStepID,
// extracted for use by server-local and distributed execution paths.
func ExecuteTool(ctx context.Context, cfg Config, tool *cwl.CommandLineTool, inputs map[string]any, workDir string) (*Result, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("executing tool", "id", tool.ID, "workDir", workDir)

	// Ensure work directory exists.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	// Resolve secondaryFiles for tool inputs if requested (direct tool execution).
	resolvedInputs := inputs
	if cfg.ResolveSecondary {
		resolvedInputs = secondaryfiles.ResolveForTool(tool, inputs, cfg.CWLDir)
	}

	// Merge tool input defaults with resolved inputs.
	mergedInputs, err := MergeToolDefaults(tool, resolvedInputs, cfg.CWLDir)
	if err != nil {
		return nil, fmt.Errorf("process inputs: %w", err)
	}

	// Ensure derived CWL properties (dirname, basename, nameroot, nameext, size)
	// are populated. In distributed mode, inputs from the upload pipeline may
	// lack these properties.
	PopulateDerivedFileProperties(mergedInputs)

	// Validate inputs against tool schema.
	if err := validate.ToolInputs(tool, mergedInputs); err != nil {
		return nil, err
	}

	// Validate file formats.
	if err := validate.ValidateFileFormat(tool, mergedInputs, cfg.Namespaces); err != nil {
		return nil, err
	}

	// Get expression library: prefer config, fall back to tool requirements.
	expressionLib := cfg.ExpressionLib
	if len(expressionLib) == 0 {
		expressionLib = ExtractExpressionLibFromTool(tool)
	}

	// Determine container runtime.
	containerRuntime := cfg.ContainerRuntime
	if cfg.NoContainer {
		containerRuntime = ""
	} else if containerRuntime == "" {
		if HasDockerRequirement(tool) {
			containerRuntime = "docker"
		}
	}

	// Make workDir absolute for use in runtime.outdir expressions.
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
	}

	// Populate directory listings for inputs with loadListing.
	PopulateDirectoryListings(tool, mergedInputs, cfg.RemoveDefaultListings)

	// Stage files from InitialWorkDirRequirement.
	useContainer := containerRuntime == "docker" || containerRuntime == "apptainer"
	inplaceUpdate := HasInplaceUpdateRequirement(tool)
	iwdResult, err := iwdr.Stage(tool, mergedInputs, workDir, iwdr.StageOptions{
		CopyForContainer: useContainer,
		CWLDir:           cfg.CWLDir,
		ExpressionLib:    expressionLib,
		InplaceUpdate:    inplaceUpdate,
	})
	if err != nil {
		return nil, fmt.Errorf("stage InitialWorkDirRequirement: %w", err)
	}
	var containerMounts []iwdr.ContainerMount
	if iwdResult != nil {
		containerMounts = iwdResult.ContainerMounts
		iwdr.UpdateInputPaths(mergedInputs, workDir, iwdResult.StagedPaths)
	}

	// Stage files with renamed basenames (from ExpressionTool modifications).
	if err := StageRenamedInputs(mergedInputs, workDir); err != nil {
		return nil, fmt.Errorf("stage renamed inputs: %w", err)
	}

	// Apply ToolTimeLimit if specified.
	timeLimit := GetToolTimeLimit(tool, mergedInputs)
	if timeLimit < 0 {
		return nil, fmt.Errorf("invalid ToolTimeLimit: timelimit must be non-negative, got %d", timeLimit)
	}
	if timeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeLimit)*time.Second)
		defer cancel()
	}

	// Build command line with runtime context appropriate for the execution environment.
	builder := cmdline.NewBuilder(expressionLib)

	// Determine output directory for result path resolution.
	outDir := cfg.OutDir
	if outDir == "" {
		outDir = workDir
	}

	var execResult *toolexec.Result
	var execErr error
	switch containerRuntime {
	case "docker":
		dockerImage := GetDockerImage(tool)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		dockerOutputDir := GetDockerOutputDirectory(tool)
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := BuildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", cmdResult.Command)
		executor := toolexec.NewExecutor(logger)
		execResult, execErr = executor.Execute(ctx, &toolexec.Options{
			Tool:              tool,
			Command:           cmdResult,
			Inputs:            mergedInputs,
			WorkDir:           workDir,
			OutDir:            outDir,
			Mode:              toolexec.ModeDocker,
			DockerImage:       dockerImage,
			ContainerMounts:   containerMounts,
			DockerOutputDir:   dockerOutputDir,
			Namespaces:        cfg.Namespaces,
			DockerHostPathMap: cfg.DockerHostPathMap,
			GPU:               cfg.GPU,
			JobRequirements:   cfg.JobRequirements,
		})

	case "apptainer":
		dockerImage := GetDockerImage(tool)
		if dockerImage == "" {
			return nil, fmt.Errorf("Apptainer execution requested but no docker image specified")
		}
		dockerOutputDir := GetDockerOutputDirectory(tool)
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := BuildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", cmdResult.Command)
		executor := toolexec.NewExecutor(logger)
		execResult, execErr = executor.Execute(ctx, &toolexec.Options{
			Tool:            tool,
			Command:         cmdResult,
			Inputs:          mergedInputs,
			WorkDir:         workDir,
			OutDir:          outDir,
			Mode:            toolexec.ModeApptainer,
			DockerImage:     dockerImage,
			ContainerMounts: containerMounts,
			DockerOutputDir: dockerOutputDir,
			Namespaces:      cfg.Namespaces,
			GPU:             cfg.GPU,
			JobRequirements: cfg.JobRequirements,
		})

	default:
		// Local execution.
		runtime := BuildRuntimeContextWithInputs(tool, workDir, mergedInputs, expressionLib)
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", cmdResult.Command)
		executor := toolexec.NewExecutor(logger)
		execResult, execErr = executor.Execute(ctx, &toolexec.Options{
			Tool:            tool,
			Command:         cmdResult,
			Inputs:          mergedInputs,
			WorkDir:         workDir,
			OutDir:          outDir,
			Mode:            toolexec.ModeLocal,
			Namespaces:      cfg.Namespaces,
			JobRequirements: cfg.JobRequirements,
		})
	}

	if execErr != nil {
		return nil, execErr
	}

	return &Result{
		Outputs:  execResult.Outputs,
		ExitCode: execResult.ExitCode,
	}, nil
}
