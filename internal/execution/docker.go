package execution

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/pkg/cwl"
)

// ContainerMount is an alias for iwdr.ContainerMount.
type ContainerMount = iwdr.ContainerMount

// DockerRuntime executes commands in Docker containers.
type DockerRuntime struct {
	// DockerCommand is the path to the docker binary (default: "docker").
	DockerCommand string
}

// Run executes a command in a Docker container.
func (r *DockerRuntime) Run(ctx context.Context, spec RunSpec) (*RunResult, error) {
	if len(spec.Command) == 0 {
		return nil, ErrEmptyCommand
	}

	if spec.Image == "" {
		return nil, ErrNoDockerImage
	}

	dockerCmd := r.DockerCommand
	if dockerCmd == "" {
		dockerCmd = "docker"
	}

	// Build Docker command.
	args := []string{"run", "--rm", "-i"}

	// GPU support: use --gpus for NVIDIA GPU passthrough.
	if spec.GPU.Enabled {
		if spec.GPU.DeviceID != "" {
			// Specific GPU(s): --gpus '"device=0"' or --gpus '"device=0,1"'
			args = append(args, "--gpus", fmt.Sprintf(`"device=%s"`, spec.GPU.DeviceID))
			// Also set CUDA_VISIBLE_DEVICES for applications that check it.
			args = append(args, "-e", "CUDA_VISIBLE_DEVICES="+spec.GPU.DeviceID)
		} else {
			// All GPUs
			args = append(args, "--gpus", "all")
		}
	}

	// Mount working directory.
	absWorkDir := resolveSymlinks(spec.WorkDir)
	args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=/var/spool/cwl", absWorkDir))
	args = append(args, "-w", "/var/spool/cwl")

	// Mount volumes.
	for hostPath, containerPath := range spec.Volumes {
		resolved := resolveSymlinks(hostPath)
		args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", resolved, containerPath))
	}

	// Set environment variables.
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Add image and command.
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	cmd := exec.CommandContext(ctx, dockerCmd, args...)

	// Handle stdin.
	if spec.Stdin != "" {
		stdinPath := spec.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(spec.WorkDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin: %w", err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Handle stdout.
	var stdoutBuf bytes.Buffer
	if spec.Stdout != "" {
		stdoutPath := filepath.Join(spec.WorkDir, spec.Stdout)
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = &stdoutBuf
	}

	// Handle stderr.
	var stderrBuf bytes.Buffer
	if spec.Stderr != "" {
		stderrPath := filepath.Join(spec.WorkDir, spec.Stderr)
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run the command.
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run docker: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}

// executeDocker executes a tool in a Docker container.
// containerMounts contains files from InitialWorkDirRequirement with absolute entrynames.
func (e *Engine) executeDocker(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []ContainerMount) (*RunResult, error) {
	e.logger.Info("executing in Docker", "image", dockerImage, "command", cmdResult.Command)

	// Create directories.
	tmpDir := workDir + "_tmp"
	for _, dir := range []string{workDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Build Docker command.
	dockerArgs := []string{"run", "--rm", "-i"}

	// GPU support: use --gpus for NVIDIA GPU passthrough.
	if e.gpu.Enabled {
		if e.gpu.DeviceID != "" {
			// Specific GPU(s): --gpus '"device=0"'
			dockerArgs = append(dockerArgs, "--gpus", fmt.Sprintf(`"device=%s"`, e.gpu.DeviceID))
			// Also set CUDA_VISIBLE_DEVICES for applications that check it.
			dockerArgs = append(dockerArgs, "-e", "CUDA_VISIBLE_DEVICES="+e.gpu.DeviceID)
		} else {
			// All GPUs
			dockerArgs = append(dockerArgs, "--gpus", "all")
		}
	}

	// Mount working directory.
	absWorkDir := resolveSymlinks(workDir)
	dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=/var/spool/cwl", absWorkDir))
	dockerArgs = append(dockerArgs, "-w", "/var/spool/cwl")

	// Mount tmp directory.
	absTmpDir := resolveSymlinks(tmpDir)
	dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=/tmp", absTmpDir))

	// Mount input files that are outside working directory.
	mounts := collectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", hostPath, containerPath))
	}

	// Mount files from InitialWorkDirRequirement with absolute entrynames.
	for _, cm := range containerMounts {
		resolved := resolveSymlinks(cm.HostPath)
		dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", resolved, cm.ContainerPath))
	}

	// Set environment variables.
	envVars := extractEnvVars(tool, inputs)
	for name, value := range envVars {
		dockerArgs = append(dockerArgs, "-e", fmt.Sprintf("%s=%s", name, value))
	}

	// Add image and command.
	dockerArgs = append(dockerArgs, dockerImage)
	dockerArgs = append(dockerArgs, cmdResult.Command...)

	e.logger.Debug("docker command", "args", dockerArgs)

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)

	// Handle stdin.
	if cmdResult.Stdin != "" {
		stdinPath := cmdResult.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(workDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin: %w", err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Determine stdout capture filename.
	stdoutCapture := cmdResult.Stdout
	if stdoutCapture == "" && hasStdoutOutput(tool) {
		stdoutCapture = "cwl.stdout.txt"
	}

	// Handle stdout.
	var stdoutBuf bytes.Buffer
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = &stdoutBuf
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr.
	var stderrBuf bytes.Buffer
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run Docker command.
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run docker: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}
