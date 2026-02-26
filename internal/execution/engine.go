// Package execution provides shared CWL tool execution logic.
// This package is used by both cwl-runner and the distributed worker.
package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/internal/loadcontents"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
)

// Engine executes CWL CommandLineTools.
type Engine struct {
	logger  *slog.Logger
	stager  Stager
	runtime Runtime
	gpu     GPUConfig // GPU configuration for container execution
	cwlDir  string    // Directory containing the CWL file

	// ExpressionLib contains JavaScript library code from InlineJavascriptRequirement.
	ExpressionLib []string

	// Namespaces for resolving format URIs.
	Namespaces map[string]string
}

// Config holds engine configuration.
type Config struct {
	Logger        *slog.Logger
	Stager        Stager
	Runtime       Runtime
	ExpressionLib []string
	Namespaces    map[string]string
	GPU           GPUConfig // GPU configuration for container execution
	CWLDir        string    // Directory containing the CWL file
}

// NewEngine creates a new execution engine.
func NewEngine(cfg Config) *Engine {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	stager := cfg.Stager
	if stager == nil {
		stager = NewFileStager("local")
	}

	runtime := cfg.Runtime
	if runtime == nil {
		runtime = &LocalRuntime{}
	}

	return &Engine{
		logger:        logger,
		stager:        stager,
		runtime:       runtime,
		gpu:           cfg.GPU,
		cwlDir:        cfg.CWLDir,
		ExpressionLib: cfg.ExpressionLib,
		Namespaces:    cfg.Namespaces,
	}
}

// ExecuteResult holds the result of tool execution.
type ExecuteResult struct {
	Outputs  map[string]any
	ExitCode int
	Stdout   string
	Stderr   string
}

// ExecuteTool executes a CWL CommandLineTool with the given inputs.
func (e *Engine) ExecuteTool(ctx context.Context, tool *cwl.CommandLineTool, inputs map[string]any, workDir string) (*ExecuteResult, error) {
	e.logger.Info("executing tool", "id", tool.ID, "workDir", workDir)

	// Ensure work directory exists.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, &ExecutionError{Phase: "setup", Err: err}
	}

	// Apply tool input defaults for any inputs not provided in the job.
	// Also processes loadContents with 64KB limit enforcement.
	mergedInputs, err := applyToolDefaults(tool, inputs, e.cwlDir)
	if err != nil {
		return nil, &ExecutionError{Phase: "process_inputs", Err: err}
	}

	e.logger.Debug("merged inputs", "tool_inputs_count", len(tool.Inputs), "job_inputs_count", len(inputs), "merged_count", len(mergedInputs))
	for k, v := range mergedInputs {
		e.logger.Debug("merged input", "key", k, "value_type", fmt.Sprintf("%T", v))
	}

	// Validate inputs against tool schema.
	if err := validate.ToolInputs(tool, mergedInputs); err != nil {
		return nil, &ExecutionError{Phase: "validate", Err: err}
	}

	// Build runtime context.
	runtimeCtx := e.buildRuntimeContext(tool, workDir)

	// Build command line.
	builder := cmdline.NewBuilder(e.ExpressionLib)
	cmdResult, err := builder.Build(tool, mergedInputs, runtimeCtx)
	if err != nil {
		return nil, &ExecutionError{Phase: "build_command", Err: err}
	}

	e.logger.Debug("built command", "cmd", cmdResult.Command)

	// Stage input files into workdir.
	if err := e.stageInputs(ctx, mergedInputs, workDir); err != nil {
		return nil, &ExecutionError{Phase: "stage_in", Err: err}
	}

	// Determine execution mode.
	useDocker := hasDockerRequirement(tool)

	// Stage files from InitialWorkDirRequirement.
	var containerMounts []iwdr.ContainerMount
	iwdResult, err := iwdr.Stage(tool, mergedInputs, workDir, iwdr.StageOptions{
		CopyForContainer: useDocker,
		CWLDir:           e.cwlDir,
		ExpressionLib:    e.ExpressionLib,
	})
	if err != nil {
		return nil, &ExecutionError{Phase: "stage_iwd", Err: err}
	}
	if iwdResult != nil {
		containerMounts = iwdResult.ContainerMounts
		iwdr.UpdateInputPaths(mergedInputs, workDir, iwdResult.StagedPaths)
	}

	// Execute the tool.
	var runResult *RunResult
	if useDocker {
		dockerImage := getDockerImage(tool)
		if dockerImage == "" {
			return nil, &ExecutionError{Phase: "execute", Err: ErrNoDockerImage}
		}
		runResult, err = e.executeDocker(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir, containerMounts)
	} else {
		runResult, err = e.executeLocal(ctx, tool, cmdResult, mergedInputs, workDir)
	}

	if err != nil {
		return nil, &ExecutionError{Phase: "execute", Err: err}
	}

	// Check exit code against successCodes.
	if !isSuccessCode(runResult.ExitCode, tool.SuccessCodes) {
		return &ExecuteResult{
			ExitCode: runResult.ExitCode,
			Stdout:   runResult.Stdout,
			Stderr:   runResult.Stderr,
		}, &ExecutionError{
			Phase:    "execute",
			Err:      ErrNonZeroExit,
			ExitCode: runResult.ExitCode,
		}
	}

	// Collect outputs.
	outputs, err := e.collectOutputs(tool, workDir, mergedInputs, runResult.ExitCode)
	if err != nil {
		return nil, &ExecutionError{Phase: "collect_outputs", Err: err}
	}

	return &ExecuteResult{
		Outputs:  outputs,
		ExitCode: runResult.ExitCode,
		Stdout:   runResult.Stdout,
		Stderr:   runResult.Stderr,
	}, nil
}

// buildRuntimeContext creates a RuntimeContext from tool requirements.
func (e *Engine) buildRuntimeContext(tool *cwl.CommandLineTool, workDir string) *cwlexpr.RuntimeContext {
	runtime := cwlexpr.DefaultRuntimeContext()
	runtime.OutDir = workDir
	runtime.TmpDir = workDir + "_tmp"

	// Apply ResourceRequirement if present.
	rr := getResourceRequirement(tool)
	if rr != nil {
		if coresMin, ok := rr["coresMin"]; ok {
			switch v := coresMin.(type) {
			case int:
				runtime.Cores = v
			case float64:
				runtime.Cores = int(v)
			}
		}
		if runtime.Cores == 1 {
			if cores, ok := rr["cores"]; ok {
				switch v := cores.(type) {
				case int:
					runtime.Cores = v
				case float64:
					runtime.Cores = int(v)
				}
			}
		}
		if ramMin, ok := rr["ramMin"]; ok {
			switch v := ramMin.(type) {
			case int:
				runtime.Ram = int64(v)
			case int64:
				runtime.Ram = v
			case float64:
				runtime.Ram = int64(v)
			}
		}
	}

	return runtime
}

// hasDockerRequirement checks if a tool has a DockerRequirement.
func hasDockerRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints["DockerRequirement"]; ok {
			return true
		}
	}
	return false
}

// getDockerImage extracts the Docker image from requirements or hints.
func getDockerImage(tool *cwl.CommandLineTool) string {
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	return ""
}

// getResourceRequirement extracts ResourceRequirement from hints or requirements.
func getResourceRequirement(tool *cwl.CommandLineTool) map[string]any {
	if tool.Requirements != nil {
		if rr, ok := tool.Requirements["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	if tool.Hints != nil {
		if rr, ok := tool.Hints["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	return nil
}

// isSuccessCode checks if an exit code is in the success codes list.
func isSuccessCode(code int, successCodes []int) bool {
	if len(successCodes) == 0 {
		return code == 0
	}
	for _, sc := range successCodes {
		if code == sc {
			return true
		}
	}
	return false
}

// hasShellCommandRequirement checks if a tool has ShellCommandRequirement.
func hasShellCommandRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["ShellCommandRequirement"]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints["ShellCommandRequirement"]; ok {
			return true
		}
	}
	return false
}

// hasStdoutOutput checks if the tool has any output of type "stdout".
func hasStdoutOutput(tool *cwl.CommandLineTool) bool {
	for _, output := range tool.Outputs {
		if output.Type == "stdout" {
			return true
		}
	}
	return false
}

// hasStderrOutput checks if the tool has any output of type "stderr".
func hasStderrOutput(tool *cwl.CommandLineTool) bool {
	for _, output := range tool.Outputs {
		if output.Type == "stderr" {
			return true
		}
	}
	return false
}

// applyToolDefaults merges tool input defaults with provided inputs.
// Returns a new map with defaults applied for any missing or nil inputs.
// Also processes loadContents for File inputs (with 64KB limit per CWL spec),
// resolves secondaryFiles from disk, and validates secondaryFiles requirements.
func applyToolDefaults(tool *cwl.CommandLineTool, inputs map[string]any, cwlDir string) (map[string]any, error) {
	result := make(map[string]any)

	// Only include inputs that are declared in the tool's inputs.
	// Undeclared inputs are ignored per CWL v1.2 spec.
	for inputID, inputDef := range tool.Inputs {
		var val any
		if v, exists := inputs[inputID]; exists {
			val = v
		} else if inputDef.Default != nil {
			// Use default value if input not provided.
			// Resolve File/Directory locations relative to CWL directory.
			val = resolveDefaultValue(inputDef.Default, cwlDir)
		}

		// Process loadContents for File inputs (with 64KB limit).
		if val != nil && inputDef.LoadContents {
			processedVal, err := loadcontents.Process(val, cwlDir)
			if err != nil {
				return nil, fmt.Errorf("input %q: %w", inputID, err)
			}
			val = processedVal
		}

		result[inputID] = val
	}

	// Note: secondaryFiles resolution happens at the CLI level for direct tool execution,
	// or at the scheduler level for workflow inputs. The execution engine only validates
	// that required secondaryFiles are present (they should have been resolved upstream).

	// Validate secondaryFiles requirements.
	for inputID, inputDef := range tool.Inputs {
		val := result[inputID]
		if val != nil {
			if err := secondaryfiles.ValidateInput(inputID, inputDef, val); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

// resolveDefaultValue resolves a default value, handling File/Directory objects specially.
// Relative paths are resolved against cwlDir.
func resolveDefaultValue(v any, cwlDir string) any {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			// Make a copy and resolve paths.
			result := make(map[string]any)
			for k, v := range val {
				result[k] = v
			}
			// Resolve location path.
			if loc, ok := result["location"].(string); ok {
				result["location"] = resolvePath(loc, cwlDir)
			}
			// Resolve path field.
			if path, ok := result["path"].(string); ok {
				result["path"] = resolvePath(path, cwlDir)
			}
			// Recursively resolve secondary files.
			if sf, ok := result["secondaryFiles"].([]any); ok {
				resolved := make([]any, len(sf))
				for i, item := range sf {
					resolved[i] = resolveDefaultValue(item, cwlDir)
				}
				result["secondaryFiles"] = resolved
			}
			return result
		}
		// For other maps, recursively process.
		result := make(map[string]any)
		for k, v := range val {
			result[k] = resolveDefaultValue(v, cwlDir)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = resolveDefaultValue(item, cwlDir)
		}
		return result
	default:
		return v
	}
}

// resolvePath resolves a relative path against a base directory.
func resolvePath(path, baseDir string) string {
	if path == "" || baseDir == "" {
		return path
	}
	// Don't modify URIs or absolute paths.
	if strings.HasPrefix(path, "file://") || strings.HasPrefix(path, "http://") ||
		strings.HasPrefix(path, "https://") || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}
