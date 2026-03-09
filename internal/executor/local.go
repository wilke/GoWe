package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/model"
)

// LocalExecutor runs tasks as local OS processes.
type LocalExecutor struct {
	logger      *slog.Logger
	parser      *parser.Parser
	workDir     string
	keepWorkDir bool
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
		parser:  parser.New(logger),
	}
}

// SetKeepWorkDir controls whether working directories are preserved after execution.
// When true (debug mode), working directories are kept for inspection.
func (e *LocalExecutor) SetKeepWorkDir(keep bool) {
	e.keepWorkDir = keep
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

	// Use cwltool for full CWL support when Tool/Job are available.
	if task.HasTool() {
		return e.submitWithCWLTool(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	return e.submitLegacy(ctx, task, taskDir)
}

// submitWithCWLTool executes a task using the cwltool package with full CWL support.
func (e *LocalExecutor) submitWithCWLTool(ctx context.Context, task *model.Task, taskDir string) (string, error) {
	e.logger.Debug("executing with cwltool", "task_id", task.ID)

	// Parse tool from task.Tool map using the proper parser.
	tool, err := e.parser.ParseToolFromMap(task.Tool)
	if err != nil {
		return "", fmt.Errorf("task %s: parse tool: %w", task.ID, err)
	}

	// Build cwltool configuration.
	cfg := cwltool.Config{
		Logger:           e.logger,
		ResolveSecondary: true,
	}
	if task.RuntimeHints != nil {
		cfg.ExpressionLib = task.RuntimeHints.ExpressionLib
		cfg.Namespaces = task.RuntimeHints.Namespaces
		cfg.CWLDir = task.RuntimeHints.CWLDir
	}

	// Execute the tool.
	result, err := cwltool.ExecuteTool(ctx, cfg, tool, task.Job, taskDir)
	if err != nil {
		return taskDir, fmt.Errorf("task %s: execute: %w", task.ID, err)
	}

	// Capture results.
	task.ExitCode = &result.ExitCode
	task.Stdout = result.Stdout
	task.Stderr = result.Stderr
	task.Outputs = result.Outputs

	e.logger.Debug("task completed with cwltool",
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
// When the task has a Tool with successCodes defined, those are checked.
func (e *LocalExecutor) Status(_ context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExitCode == nil {
		return model.TaskStateQueued, nil
	}

	// Check against tool's successCodes if available.
	if task.Tool != nil {
		if sc, ok := task.Tool["successCodes"].([]any); ok && len(sc) > 0 {
			// Check if exit code is in successCodes.
			for _, code := range sc {
				switch c := code.(type) {
				case int:
					if *task.ExitCode == c {
						return model.TaskStateSuccess, nil
					}
				case float64:
					if *task.ExitCode == int(c) {
						return model.TaskStateSuccess, nil
					}
				}
			}
			// Exit code not in successCodes - check permanentFailCodes and temporaryFailCodes.
			if pf, ok := task.Tool["permanentFailCodes"].([]any); ok {
				for _, code := range pf {
					switch c := code.(type) {
					case int:
						if *task.ExitCode == c {
							return model.TaskStateFailed, nil
						}
					case float64:
						if *task.ExitCode == int(c) {
							return model.TaskStateFailed, nil
						}
					}
				}
			}
			// If not in successCodes or permanentFailCodes, it's a temporary failure (will retry).
			return model.TaskStateFailed, nil
		}
	}

	// Default behavior: exit code 0 is success, anything else is failure.
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
