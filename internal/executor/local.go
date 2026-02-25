package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// LocalExecutor runs tasks as local OS processes.
type LocalExecutor struct {
	logger  *slog.Logger
	workDir string
}

// NewLocalExecutor creates a LocalExecutor rooted at workDir.
// If workDir is empty, os.TempDir() is used.
func NewLocalExecutor(workDir string, logger *slog.Logger) *LocalExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &LocalExecutor{
		workDir: workDir,
		logger:  logger.With("component", "local-executor"),
	}
}

// Type returns model.ExecutorTypeLocal.
func (e *LocalExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeLocal
}

// Submit executes the task synchronously as a local process.
// It returns the task working directory as the externalID.
func (e *LocalExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	taskDir := filepath.Join(e.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("task %s: create work dir: %w", task.ID, err)
	}

	// Use execution.Engine for full CWL support when Tool/Job are available.
	if task.HasTool() {
		return e.submitWithEngine(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	return e.submitLegacy(ctx, task, taskDir)
}

// submitWithEngine executes a task using execution.Engine with full CWL output support.
func (e *LocalExecutor) submitWithEngine(ctx context.Context, task *model.Task, taskDir string) (string, error) {
	e.logger.Debug("executing with engine", "task_id", task.ID)

	// Parse tool from task.Tool map.
	tool, err := parseToolFromMap(task.Tool)
	if err != nil {
		return "", fmt.Errorf("task %s: parse tool: %w", task.ID, err)
	}

	// Build engine configuration.
	var expressionLib []string
	var namespaces map[string]string
	var cwlDir string
	if task.RuntimeHints != nil {
		expressionLib = task.RuntimeHints.ExpressionLib
		namespaces = task.RuntimeHints.Namespaces
		cwlDir = task.RuntimeHints.CWLDir
	}

	engine := execution.NewEngine(execution.Config{
		Logger:        e.logger,
		ExpressionLib: expressionLib,
		Namespaces:    namespaces,
		CWLDir:        cwlDir,
	})

	// Execute the tool.
	result, err := engine.ExecuteTool(ctx, tool, task.Job, taskDir)
	if err != nil {
		// Even on error, capture any partial results.
		if result != nil {
			task.ExitCode = &result.ExitCode
			task.Stdout = result.Stdout
			task.Stderr = result.Stderr
			task.Outputs = result.Outputs
		}
		return taskDir, fmt.Errorf("task %s: execute: %w", task.ID, err)
	}

	// Capture results.
	task.ExitCode = &result.ExitCode
	task.Stdout = result.Stdout
	task.Stderr = result.Stderr
	task.Outputs = result.Outputs

	e.logger.Debug("task completed with engine",
		"task_id", task.ID,
		"exit_code", result.ExitCode,
		"outputs", len(result.Outputs),
	)

	return taskDir, nil
}

// submitLegacy executes a task using the legacy _base_command approach.
func (e *LocalExecutor) submitLegacy(ctx context.Context, task *model.Task, taskDir string) (string, error) {
	parts := extractBaseCommand(task.Inputs)
	if len(parts) == 0 {
		return "", fmt.Errorf("task %s: _base_command is missing or empty", task.ID)
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = taskDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	task.Stdout = stdoutBuf.String()
	task.Stderr = stderrBuf.String()

	var exitCode int
	switch err := runErr.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = err.ExitCode()
	default:
		// Non-exit errors (e.g. binary not found) are returned directly.
		return "", fmt.Errorf("task %s: run command: %w", task.ID, runErr)
	}
	task.ExitCode = &exitCode

	// Collect outputs via glob patterns.
	if globs, ok := task.Inputs["_output_globs"].(map[string]any); ok {
		if task.Outputs == nil {
			task.Outputs = make(map[string]any)
		}
		for outputID, raw := range globs {
			pattern, ok := raw.(string)
			if !ok {
				continue
			}
			matches, err := filepath.Glob(filepath.Join(taskDir, pattern))
			if err != nil {
				continue
			}
			if len(matches) == 1 {
				task.Outputs[outputID] = matches[0]
			} else if len(matches) > 1 {
				task.Outputs[outputID] = matches
			}
		}
	}

	e.logger.Debug("task submitted (legacy)",
		"task_id", task.ID,
		"command", parts,
		"exit_code", exitCode,
	)

	return taskDir, nil
}

// Status derives the task state from the recorded exit code.
// If no exit code has been set yet the task is considered queued.
func (e *LocalExecutor) Status(_ context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExitCode == nil {
		return model.TaskStateQueued, nil
	}
	if *task.ExitCode == 0 {
		return model.TaskStateSuccess, nil
	}
	return model.TaskStateFailed, nil
}

// Cancel is a no-op for LocalExecutor; context cancellation handles termination.
func (e *LocalExecutor) Cancel(_ context.Context, _ *model.Task) error {
	return nil
}

// Logs returns the captured stdout and stderr stored on the task.
func (e *LocalExecutor) Logs(_ context.Context, task *model.Task) (string, string, error) {
	return task.Stdout, task.Stderr, nil
}

// extractBaseCommand reads the _base_command key from task inputs and converts
// its []any elements to a []string.
func extractBaseCommand(inputs map[string]any) []string {
	raw, ok := inputs["_base_command"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// parseToolFromMap converts a task.Tool map to a cwl.CommandLineTool.
func parseToolFromMap(toolMap map[string]any) (*cwl.CommandLineTool, error) {
	// Marshal to JSON then unmarshal to struct.
	data, err := json.Marshal(toolMap)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}

	var tool cwl.CommandLineTool
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("unmarshal tool: %w", err)
	}

	// Handle baseCommand which may be string or []string in YAML.
	if bc, ok := toolMap["baseCommand"]; ok {
		switch cmd := bc.(type) {
		case string:
			tool.BaseCommand = []string{cmd}
		case []any:
			var baseCmd []string
			for _, c := range cmd {
				if s, ok := c.(string); ok {
					baseCmd = append(baseCmd, s)
				}
			}
			tool.BaseCommand = baseCmd
		}
	}

	// Handle inputs map conversion.
	if inputs, ok := toolMap["inputs"].(map[string]any); ok {
		tool.Inputs = make(map[string]cwl.ToolInputParam)
		for id, param := range inputs {
			tool.Inputs[id] = parseInputParam(param)
		}
	}

	// Handle outputs map conversion.
	if outputs, ok := toolMap["outputs"].(map[string]any); ok {
		tool.Outputs = make(map[string]cwl.ToolOutputParam)
		for id, param := range outputs {
			tool.Outputs[id] = parseOutputParam(param)
		}
	}

	return &tool, nil
}

// parseInputParam converts an input parameter map to cwl.ToolInputParam.
func parseInputParam(param any) cwl.ToolInputParam {
	var result cwl.ToolInputParam
	switch p := param.(type) {
	case string:
		result.Type = p
	case map[string]any:
		if t, ok := p["type"].(string); ok {
			result.Type = t
		}
		if d, ok := p["default"]; ok {
			result.Default = d
		}
		if ib, ok := p["inputBinding"].(map[string]any); ok {
			result.InputBinding = parseInputBinding(ib)
		}
	}
	return result
}

// parseOutputParam converts an output parameter map to cwl.ToolOutputParam.
func parseOutputParam(param any) cwl.ToolOutputParam {
	var result cwl.ToolOutputParam
	switch p := param.(type) {
	case string:
		result.Type = p
	case map[string]any:
		if t, ok := p["type"].(string); ok {
			result.Type = t
		}
		if ob, ok := p["outputBinding"].(map[string]any); ok {
			result.OutputBinding = parseOutputBinding(ob)
		}
	}
	return result
}

// parseInputBinding converts an inputBinding map to cwl.InputBinding.
func parseInputBinding(m map[string]any) *cwl.InputBinding {
	ib := &cwl.InputBinding{}
	if pos, ok := m["position"].(float64); ok {
		p := int(pos)
		ib.Position = &p
	}
	if prefix, ok := m["prefix"].(string); ok {
		ib.Prefix = prefix
	}
	if sep, ok := m["separate"].(bool); ok {
		ib.Separate = &sep
	}
	if vf, ok := m["valueFrom"].(string); ok {
		ib.ValueFrom = vf
	}
	return ib
}

// parseOutputBinding converts an outputBinding map to cwl.OutputBinding.
func parseOutputBinding(m map[string]any) *cwl.OutputBinding {
	ob := &cwl.OutputBinding{}
	if glob, ok := m["glob"].(string); ok {
		ob.Glob = glob
	}
	if lc, ok := m["loadContents"].(bool); ok {
		ob.LoadContents = lc
	}
	if oe, ok := m["outputEval"].(string); ok {
		ob.OutputEval = oe
	}
	return ob
}
