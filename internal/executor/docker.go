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

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// osCommandRunner is the real implementation using os/exec.
type osCommandRunner struct{}

func (r *osCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	switch e := runErr.(type) {
	case nil:
		return stdout, stderr, 0, nil
	case *exec.ExitError:
		return stdout, stderr, e.ExitCode(), nil
	default:
		return stdout, stderr, -1, runErr
	}
}

// DockerExecutor runs tasks inside Docker containers using the Docker CLI.
type DockerExecutor struct {
	logger  *slog.Logger
	workDir string
	runner  CommandRunner
}

// NewDockerExecutor creates a DockerExecutor rooted at workDir.
// If workDir is empty, os.TempDir() is used.
func NewDockerExecutor(workDir string, logger *slog.Logger) *DockerExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &DockerExecutor{
		workDir: workDir,
		logger:  logger.With("component", "docker-executor"),
		runner:  &osCommandRunner{},
	}
}

// newDockerExecutorWithRunner is used by tests to inject a mock CommandRunner.
func newDockerExecutorWithRunner(workDir string, logger *slog.Logger, runner CommandRunner) *DockerExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &DockerExecutor{
		workDir: workDir,
		logger:  logger.With("component", "docker-executor"),
		runner:  runner,
	}
}

// Type returns model.ExecutorTypeContainer.
func (e *DockerExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeContainer
}

// Submit runs the task synchronously inside a Docker container.
// It returns the container name as the externalID.
func (e *DockerExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
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

	containerName := "gowe-" + task.ID
	args := []string{
		"run", "--rm",
		"--name", containerName,
		"-v", taskDir + ":/work",
		"-w", "/work",
		image,
	}
	args = append(args, parts...)

	stdout, stderr, exitCode, runErr := e.runner.Run(ctx, "docker", args...)
	if runErr != nil {
		return "", fmt.Errorf("task %s: docker run: %w", task.ID, runErr)
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

	e.logger.Debug("docker task submitted",
		"task_id", task.ID,
		"image", image,
		"command", parts,
		"exit_code", exitCode,
	)

	return containerName, nil
}

// Status derives the task state from the recorded exit code.
func (e *DockerExecutor) Status(_ context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExitCode == nil {
		return model.TaskStateQueued, nil
	}
	if *task.ExitCode == 0 {
		return model.TaskStateSuccess, nil
	}
	return model.TaskStateFailed, nil
}

// Cancel attempts to stop and remove the Docker container.
func (e *DockerExecutor) Cancel(ctx context.Context, task *model.Task) error {
	if task.ExternalID == "" {
		return nil
	}
	_, _, _, err := e.runner.Run(ctx, "docker", "rm", "-f", task.ExternalID)
	return err
}

// Logs returns the captured stdout and stderr stored on the task.
func (e *DockerExecutor) Logs(_ context.Context, task *model.Task) (string, string, error) {
	return task.Stdout, task.Stderr, nil
}
