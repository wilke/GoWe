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
	"github.com/me/gowe/pkg/cwl"
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
	logger      *slog.Logger
	parser      *parser.Parser
	workDir     string
	runner      CommandRunner
	keepWorkDir bool
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
		parser:  parser.New(logger),
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
		parser:  parser.New(logger),
		runner:  runner,
	}
}

// Type returns model.ExecutorTypeContainer.
func (e *DockerExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeContainer
}

// SetKeepWorkDir controls whether working directories are preserved after execution.
func (e *DockerExecutor) SetKeepWorkDir(keep bool) {
	e.keepWorkDir = keep
}

// Submit runs the task synchronously inside a Docker container.
// It returns the container name as the externalID.
// Each invocation gets a unique working directory that is cleaned up
// after outputs are collected, unless keepWorkDir is enabled.
func (e *DockerExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	taskDir := filepath.Join(e.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("task %s: create work dir: %w", task.ID, err)
	}

	// Use cwltool for full CWL support when Tool/Job are available.
	if task.HasTool() {
		return e.submitWithCWLTool(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	image, ok := task.Inputs["_docker_image"].(string)
	if !ok || image == "" {
		return "", fmt.Errorf("task %s: _docker_image is missing or empty", task.ID)
	}

	parts := extractBaseCommand(task.Inputs)
	if len(parts) == 0 {
		return "", fmt.Errorf("task %s: _base_command is missing or empty", task.ID)
	}

	// Collect Directory inputs as additional volume mounts.
	var volumeArgs []string
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
				volumeArgs = append(volumeArgs, "-v", path+":/work/"+k)
			default:
				return "", fmt.Errorf("task %s: unsupported scheme %q for Docker Directory input %q", task.ID, scheme, k)
			}
		}
	}

	containerName := "gowe-" + task.ID
	args := []string{
		"run", "--rm",
		"--name", containerName,
		"-v", taskDir + ":/work",
		"-w", "/work",
	}
	args = append(args, volumeArgs...)
	args = append(args, image)
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

// submitWithCWLTool executes a task using the cwltool package with full CWL support.
func (e *DockerExecutor) submitWithCWLTool(ctx context.Context, task *model.Task, taskDir string) (string, error) {
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

