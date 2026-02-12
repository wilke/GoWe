package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

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
	parts := extractBaseCommand(task.Inputs)
	if len(parts) == 0 {
		return "", fmt.Errorf("task %s: _base_command is missing or empty", task.ID)
	}

	taskDir := filepath.Join(e.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("task %s: create work dir: %w", task.ID, err)
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

	e.logger.Debug("task submitted",
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
