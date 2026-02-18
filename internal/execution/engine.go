// Package execution provides shared CWL tool execution logic.
// This package is used by both cwl-runner and the distributed worker.
package execution

import (
	"context"
	"log/slog"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// Engine executes CWL CommandLineTools.
type Engine struct {
	logger  *slog.Logger
	stager  Stager
	runtime Runtime

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

	// Build runtime context.
	runtimeCtx := e.buildRuntimeContext(tool, workDir)

	// Build command line.
	builder := cmdline.NewBuilder(e.ExpressionLib)
	cmdResult, err := builder.Build(tool, inputs, runtimeCtx)
	if err != nil {
		return nil, &ExecutionError{Phase: "build_command", Err: err}
	}

	e.logger.Debug("built command", "cmd", cmdResult.Command)

	// Stage input files into workdir.
	if err := e.stageInputs(ctx, inputs, workDir); err != nil {
		return nil, &ExecutionError{Phase: "stage_in", Err: err}
	}

	// Determine execution mode.
	useDocker := hasDockerRequirement(tool)

	// Execute the tool.
	var runResult *RunResult
	if useDocker {
		dockerImage := getDockerImage(tool)
		if dockerImage == "" {
			return nil, &ExecutionError{Phase: "execute", Err: ErrNoDockerImage}
		}
		runResult, err = e.executeDocker(ctx, tool, cmdResult, inputs, dockerImage, workDir)
	} else {
		runResult, err = e.executeLocal(ctx, tool, cmdResult, inputs, workDir)
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
	outputs, err := e.collectOutputs(tool, workDir, inputs, runResult.ExitCode)
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
