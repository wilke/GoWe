package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ApptainerExecutor runs tasks inside Apptainer containers using the Apptainer CLI.
type ApptainerExecutor struct {
	logger  *slog.Logger
	workDir string
	runner  CommandRunner
}

// NewApptainerExecutor creates an ApptainerExecutor rooted at workDir.
// If workDir is empty, os.TempDir() is used.
func NewApptainerExecutor(workDir string, logger *slog.Logger) *ApptainerExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &ApptainerExecutor{
		workDir: workDir,
		logger:  logger.With("component", "apptainer-executor"),
		runner:  &osCommandRunner{},
	}
}

// newApptainerExecutorWithRunner is used by tests to inject a mock CommandRunner.
func newApptainerExecutorWithRunner(workDir string, logger *slog.Logger, runner CommandRunner) *ApptainerExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &ApptainerExecutor{
		workDir: workDir,
		logger:  logger.With("component", "apptainer-executor"),
		runner:  runner,
	}
}

// Type returns model.ExecutorTypeApptainer.
func (e *ApptainerExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeApptainer
}

// Submit runs the task synchronously inside an Apptainer container.
func (e *ApptainerExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	image, ok := task.Inputs["_docker_image"].(string)
	if !ok || image == "" {
		return "", fmt.Errorf("task %s: _docker_image is missing or empty", task.ID)
	}

	parts := extractBaseCommand(task.Inputs)
	if len(parts) == 0 {
		return "", fmt.Errorf("task %s: _base_command is missing or empty", task.ID)
	}

	taskDir := filepath.Join(e.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("task %s: create work dir: %w", task.ID, err)
	}

	// Collect Directory inputs as additional bind mounts.
	var bindArgs []string
	for k, v := range task.Inputs {
		if reservedKeys[k] {
			continue
		}
		if dir, ok := v.(map[string]any); ok && dir["class"] == "Directory" {
			loc, _ := dir["location"].(string)
			scheme, path := cwl.ParseLocationScheme(loc)
			switch scheme {
			case cwl.SchemeFile, "":
				if err := os.MkdirAll(path, 0o755); err != nil {
					return "", fmt.Errorf("task %s: mkdir for Directory input %q: %w", task.ID, k, err)
				}
				bindArgs = append(bindArgs, "--bind", path+":/work/"+k)
			default:
				return "", fmt.Errorf("task %s: unsupported scheme %q for Apptainer Directory input %q", task.ID, scheme, k)
			}
		}
	}

	args := []string{
		"exec",
		"--bind", taskDir + ":/work",
		"--pwd", "/work",
	}
	args = append(args, bindArgs...)
	args = append(args, "docker://"+image)
	args = append(args, parts...)

	stdout, stderr, exitCode, runErr := e.runner.Run(ctx, "apptainer", args...)
	if runErr != nil {
		return "", fmt.Errorf("task %s: apptainer exec: %w", task.ID, runErr)
	}

	task.Stdout = stdout
	task.Stderr = stderr
	task.ExitCode = &exitCode

	// Collect outputs via glob patterns on the host-side taskDir.
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

	e.logger.Debug("apptainer task submitted",
		"task_id", task.ID,
		"image", image,
		"command", parts,
		"exit_code", exitCode,
	)

	return "apptainer-" + task.ID, nil
}

// Status derives the task state from the recorded exit code.
func (e *ApptainerExecutor) Status(_ context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExitCode == nil {
		return model.TaskStateQueued, nil
	}
	if *task.ExitCode == 0 {
		return model.TaskStateSuccess, nil
	}
	return model.TaskStateFailed, nil
}

// Cancel is a no-op for Apptainer since exec runs synchronously.
func (e *ApptainerExecutor) Cancel(_ context.Context, _ *model.Task) error {
	return nil
}

// Logs returns the captured stdout and stderr stored on the task.
func (e *ApptainerExecutor) Logs(_ context.Context, task *model.Task) (string, string, error) {
	return task.Stdout, task.Stderr, nil
}
