// Package cwltool provides standalone CWL CommandLineTool execution.
// It extracts the proven preprocessing + execution pipeline from cwlrunner
// into a reusable function that can be called by both cwl-runner and
// the distributed worker/executor paths.
package cwltool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
)

// maskSecretsInError redacts any secret value from err's message, for the
// #273 tool-level input validation error path: ValidateInputs' ToolLevel
// redaction already omits offending values from every message, but this is
// routed through the same secret masking as stderr/argv (toolexec.
// MaskSecretValues) as a second layer, in case a field name or expected-type
// description happens to contain a secret substring. Preserves
// errors.Is(_, validate.ErrInputValidation) when the original error wrapped
// it. Returns err unchanged when there is nothing to mask or no secrets are
// configured.
func maskSecretsInError(err error, secrets map[string]string) error {
	if err == nil || len(secrets) == 0 {
		return err
	}
	msg := err.Error()
	masked := toolexec.MaskSecretValues([]string{msg}, secrets)[0]
	if masked == msg {
		return err
	}
	if errors.Is(err, validate.ErrInputValidation) {
		return &maskedError{msg: masked, wrapped: validate.ErrInputValidation}
	}
	return errors.New(masked)
}

// maskedError carries a secret-redacted message while still satisfying
// errors.Is(_, validate.ErrInputValidation) for the original error, via
// Unwrap — unlike fmt.Errorf("%w: %s", ...), it does not re-prepend
// ErrInputValidation's own text (already present in msg, itself derived from
// an error that already wrapped it).
type maskedError struct {
	msg     string
	wrapped error
}

func (e *maskedError) Error() string { return e.msg }
func (e *maskedError) Unwrap() error { return e.wrapped }

// Config holds configuration for ExecuteTool.
type Config struct {
	Logger                *slog.Logger
	CWLDir                string               // Directory containing the CWL file
	Namespaces            map[string]string    // Namespace prefix -> URI mappings
	ExpressionLib         []string             // JavaScript library from InlineJavascriptRequirement
	ContainerRuntime      string               // "docker", "apptainer", or "" (auto-detect)
	NoContainer           bool                 // Force local execution even with DockerRequirement
	ImageDir              string               // Base directory for resolving relative .sif image paths
	GPU                   toolexec.GPUConfig   // GPU configuration
	MaxCPUs               int                  // Worker max CPUs (0 = no limit)
	MaxMemMB              int64                // Worker max memory in MiB (0 = no limit)
	ApptainerCgroups      bool                 // System supports cgroups v2 unified
	DockerHostPathMap     map[string]string    // Container path -> host path for DinD
	DockerVolume          string               // Named Docker volume shared with tool containers
	ResolveSecondary      bool                 // Resolve secondary files from tool definitions
	JobRequirements       []any                // cwl:requirements from job file
	OutDir                string               // Output directory for resolved output paths
	RemoveDefaultListings bool                 // Remove listings when loadListing is default (for worker/executor mode)
	ExtraBinds            []toolexec.ExtraBind // Extra bind mounts for containers (pre-staged datasets, admin paths)
	SecretEnvVars         map[string]string    // Secret env vars injected into containers (never logged, delivered via cmd.Env not argv)
	EnvVars               map[string]string    // Non-secret env vars injected into containers
	// SecretValues is the full set of secret name->value pairs in play for
	// this execution (env-exposed SecretEnvVars entries plus any job-only
	// values re-injected by ApplySecrets, e.g. a cwltool:Secrets input
	// consumed via $(inputs.x) in a commandLineBinding or
	// InitialWorkDirRequirement). It is used ONLY to redact secret values
	// out of log lines (the built command, docker/apptainer argv) — never
	// for container delivery. Populated by ApplySecrets; callers that build
	// Config without it get no masking beyond SecretEnvVars.
	SecretValues map[string]string

	// InputValidation controls the #273 tool-level input type-schema check
	// (validate.ToolInputs/ExpressionToolInputs). Empty means warn (see
	// validate.Mode.Effective) — the safe default for a task whose
	// RuntimeHints predate the server's --input-validation stamping.
	// cwl-runner sets this explicitly to enforce (its standalone default).
	InputValidation validate.Mode
}

// Result holds the result of tool execution.
type Result struct {
	Outputs  map[string]any
	ExitCode int
	Stdout   string
	Stderr   string
}

// resolveResources validates CWL resource requirements against worker limits
// and computes effective resource values. It returns a ResourceConfig for
// container execution and updates the runtime context with effective values.
//
// Resource flags (--memory, --cpus) are only set when a CWL ResourceRequirement
// or worker max limit is explicitly provided. When neither is set, zeros are
// returned (meaning "no limit" for the container runtime).
func resolveResources(runtime *cwlexpr.RuntimeContext, cfg Config, tool *cwl.CommandLineTool) (toolexec.ResourceConfig, error) {
	rr := getResourceRequirement(tool)
	_, hasCWLCores := rr["coresMin"]
	if !hasCWLCores {
		_, hasCWLCores = rr["cores"]
	}
	_, hasCWLRam := rr["ramMin"]

	// Validate: CWL requirements must not exceed worker limits.
	if cfg.MaxCPUs > 0 && hasCWLCores && runtime.Cores > cfg.MaxCPUs {
		return toolexec.ResourceConfig{}, fmt.Errorf("task requires %d cores but worker max is %d", runtime.Cores, cfg.MaxCPUs)
	}
	if cfg.MaxMemMB > 0 && hasCWLRam && runtime.Ram > cfg.MaxMemMB {
		return toolexec.ResourceConfig{}, fmt.Errorf("task requires %d MiB RAM but worker max is %d MiB", runtime.Ram, cfg.MaxMemMB)
	}

	var res toolexec.ResourceConfig

	// Set effective cores: CWL requirement takes priority, then worker max.
	if hasCWLCores {
		res.Cores = runtime.Cores
	} else if cfg.MaxCPUs > 0 {
		res.Cores = cfg.MaxCPUs
		runtime.Cores = cfg.MaxCPUs
	}
	// else: no limit, keep runtime default for expressions

	// Set effective RAM: CWL requirement takes priority, then worker max.
	if hasCWLRam {
		res.RamMB = runtime.Ram
	} else if cfg.MaxMemMB > 0 {
		res.RamMB = cfg.MaxMemMB
		runtime.Ram = cfg.MaxMemMB
	}
	// else: no limit, keep runtime default for expressions

	res.ApptainerCgroups = cfg.ApptainerCgroups
	return res, nil
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

	// Validate inputs against tool schema. Tool-level messages (mode=enforce
	// errors, mode=warn log lines) never include values (Options.ToolLevel),
	// but route the returned error through the same secret redaction as
	// stderr/argv anyway, in case a value happens to echo a secret substring.
	if err := validate.ToolInputs(tool, mergedInputs, cfg.InputValidation, logger); err != nil {
		return nil, maskSecretsInError(err, cfg.SecretValues)
	}

	// Validate File/Directory inputs have path or location.
	if err := validate.ValidateFileInputs(mergedInputs); err != nil {
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
	// Only use a container runtime when the tool has DockerRequirement.
	// This allows --runtime=apptainer to act as the preferred runtime
	// without forcing container execution for tools that don't need it.
	var containerRuntime string
	if !cfg.NoContainer && HasDockerRequirement(tool) {
		if cfg.ContainerRuntime != "" {
			containerRuntime = cfg.ContainerRuntime
		} else {
			containerRuntime = DetectContainerRuntime()
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

		// Determine runtime context paths based on execution mode.
		var containerWorkDir, runtimeTmpDir string
		if cfg.DockerVolume != "" {
			// Volume mode: the named volume is mounted into the tool container,
			// so files are accessible at their actual paths. Use workDir as
			// the container working directory rather than /var/spool/cwl.
			containerWorkDir = workDir
			if dockerOutputDir != "" {
				containerWorkDir = dockerOutputDir
			}
			runtimeTmpDir = workDir + "_tmp"

			// Stage any input files outside the volume mount into workDir
			// so they are accessible in the tool container via the volume.
			if err := stageOutOfVolumeInputs(mergedInputs, workDir); err != nil {
				return nil, fmt.Errorf("stage inputs for volume mode: %w", err)
			}
		} else {
			// Bind mount mode: workDir is bind-mounted at /var/spool/cwl.
			containerWorkDir = "/var/spool/cwl"
			if dockerOutputDir != "" {
				containerWorkDir = dockerOutputDir
			}
			runtimeTmpDir = "/tmp"
		}

		runtime := BuildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = runtimeTmpDir
		resources, err := resolveResources(runtime, cfg, tool)
		if err != nil {
			return nil, err
		}
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", toolexec.MaskSecretValues(cmdResult.Command, cfg.SecretValues))
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
			DockerVolume:      cfg.DockerVolume,
			GPU:               cfg.GPU,
			Resources:         resources,
			JobRequirements:   cfg.JobRequirements,
			ExtraBinds:        cfg.ExtraBinds,
			SecretEnvVars:     cfg.SecretEnvVars,
			EnvVars:           cfg.EnvVars,
			MaskSecrets:       cfg.SecretValues,
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
		resources, err := resolveResources(runtime, cfg, tool)
		if err != nil {
			return nil, err
		}
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", toolexec.MaskSecretValues(cmdResult.Command, cfg.SecretValues))
		executor := toolexec.NewExecutor(logger)
		execResult, execErr = executor.Execute(ctx, &toolexec.Options{
			Tool:            tool,
			Command:         cmdResult,
			Inputs:          mergedInputs,
			WorkDir:         workDir,
			OutDir:          outDir,
			Mode:            toolexec.ModeApptainer,
			DockerImage:     dockerImage,
			ImageDir:        cfg.ImageDir,
			ContainerMounts: containerMounts,
			DockerOutputDir: dockerOutputDir,
			Namespaces:      cfg.Namespaces,
			GPU:             cfg.GPU,
			Resources:       resources,
			JobRequirements: cfg.JobRequirements,
			ExtraBinds:      cfg.ExtraBinds,
			SecretEnvVars:   cfg.SecretEnvVars,
			EnvVars:         cfg.EnvVars,
			MaskSecrets:     cfg.SecretValues,
		})

	default:
		// Local execution.
		runtime := BuildRuntimeContextWithInputs(tool, workDir, mergedInputs, expressionLib)
		resources, err := resolveResources(runtime, cfg, tool)
		if err != nil {
			return nil, err
		}
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		logger.Debug("built command", "cmd", toolexec.MaskSecretValues(cmdResult.Command, cfg.SecretValues))
		executor := toolexec.NewExecutor(logger)
		execResult, execErr = executor.Execute(ctx, &toolexec.Options{
			Tool:            tool,
			Command:         cmdResult,
			Inputs:          mergedInputs,
			WorkDir:         workDir,
			OutDir:          outDir,
			Mode:            toolexec.ModeLocal,
			GPU:             cfg.GPU,
			Resources:       resources,
			Namespaces:      cfg.Namespaces,
			JobRequirements: cfg.JobRequirements,
			ExtraBinds:      cfg.ExtraBinds,
			SecretEnvVars:   cfg.SecretEnvVars,
			EnvVars:         cfg.EnvVars,
			MaskSecrets:     cfg.SecretValues,
		})
	}

	if execErr != nil {
		return nil, execErr
	}

	return &Result{
		Outputs:  execResult.Outputs,
		ExitCode: execResult.ExitCode,
		Stdout:   execResult.Stdout,
		Stderr:   execResult.Stderr,
	}, nil
}
