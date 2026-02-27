package toolexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/internal/iwdr"
)

// executeLocal executes a tool locally without Docker in the specified work directory.
func (e *Executor) executeLocal(ctx context.Context, opts *Options) (*Result, error) {
	startTime := time.Now()
	e.logger.Info("executing locally", "command", opts.Command.Command)

	tool := opts.Tool
	workDir := opts.WorkDir
	cmdResult := opts.Command
	inputs := opts.Inputs

	// Create the working directory.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work directory: %w", err)
	}

	// Stage input files.
	if err := stageInputFiles(inputs, workDir); err != nil {
		return nil, fmt.Errorf("stage inputs: %w", err)
	}

	// Build command.
	if len(cmdResult.Command) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	// Check for ShellCommandRequirement - run through shell if present.
	var cmd *exec.Cmd
	if hasShellCommandRequirement(tool) {
		// Join command parts and run through shell.
		cmdStr := strings.Join(cmdResult.Command, " ")
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, cmdResult.Command[0], cmdResult.Command[1:]...)
	}
	cmd.Dir = workDir

	// Set environment variables from EnvVarRequirement.
	cmd.Env = os.Environ() // Start with current environment
	envVars := extractEnvVars(tool, inputs)
	for name, value := range envVars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
	}

	// Handle stdin.
	if cmdResult.Stdin != "" {
		stdinPath := cmdResult.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(workDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin %s: %w", stdinPath, err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Determine stdout capture filename.
	stdoutCapture := cmdResult.Stdout
	if stdoutCapture == "" && hasStdoutOutput(tool) {
		stdoutCapture = "cwl.stdout.txt"
	}

	// Handle stdout - capture to temp file first, then move to workDir after execution.
	// This prevents the stdout file from appearing in the workDir during execution
	// (e.g., affecting `find` commands that scan the workDir).
	var stdoutFile *os.File
	var stdoutTempPath string
	if stdoutCapture != "" {
		var err error
		stdoutFile, err = os.CreateTemp("", "cwl-stdout-*")
		if err != nil {
			return nil, fmt.Errorf("create stdout temp file: %w", err)
		}
		stdoutTempPath = stdoutFile.Name()
		defer os.Remove(stdoutTempPath)
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = io.Discard
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr - capture to temp file first, then move to workDir after execution.
	var stderrFile *os.File
	var stderrTempPath string
	if stderrCapture != "" {
		var err error
		stderrFile, err = os.CreateTemp("", "cwl-stderr-*")
		if err != nil {
			return nil, fmt.Errorf("create stderr temp file: %w", err)
		}
		stderrTempPath = stderrFile.Name()
		defer os.Remove(stderrTempPath)
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = io.Discard
	}

	// Run command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				return nil, fmt.Errorf("command failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	// Move stdout/stderr from temp files to the workDir after command completion.
	if stdoutCapture != "" && stdoutTempPath != "" {
		stdoutFile.Close()
		stdoutFinalPath := filepath.Join(workDir, stdoutCapture)
		if err := moveFile(stdoutTempPath, stdoutFinalPath); err != nil {
			return nil, fmt.Errorf("move stdout file: %w", err)
		}
	}
	if stderrCapture != "" && stderrTempPath != "" {
		stderrFile.Close()
		stderrFinalPath := filepath.Join(workDir, stderrCapture)
		if err := moveFile(stderrTempPath, stderrFinalPath); err != nil {
			return nil, fmt.Errorf("move stderr file: %w", err)
		}
	}

	// Capture resource usage from ProcessState.
	peakMemoryKB := getResourceUsage(cmd.ProcessState)
	duration := time.Since(startTime)

	// Collect outputs (passing exit code for runtime.exitCode).
	outputs, err := e.CollectOutputs(tool, workDir, inputs, exitCode, opts.OutDir, opts.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return &Result{
		Outputs:      outputs,
		ExitCode:     exitCode,
		PeakMemoryKB: peakMemoryKB,
		StartTime:    startTime,
		Duration:     duration,
	}, nil
}

// executeInDocker executes a tool in a Docker container with the specified work directory.
// Note: For containerized execution, resource usage captures the container CLI process overhead,
// not the application inside the container.
func (e *Executor) executeInDocker(ctx context.Context, opts *Options) (*Result, error) {
	startTime := time.Now()
	dockerImage := opts.DockerImage
	e.logger.Info("executing in Docker", "image", dockerImage, "command", opts.Command.Command)

	tool := opts.Tool
	workDir := opts.WorkDir
	cmdResult := opts.Command
	inputs := opts.Inputs
	containerMounts := opts.ContainerMounts
	dockerOutputDir := opts.DockerOutputDir

	// Create directories for this execution.
	tmpDir := workDir + "_tmp"
	outputDir := workDir + "_output" // For dockerOutputDirectory
	for _, dir := range []string{workDir, tmpDir, outputDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Stage input files.
	if err := stageInputFiles(inputs, workDir); err != nil {
		return nil, fmt.Errorf("stage inputs: %w", err)
	}

	// Build Docker command.
	dockerArgs := []string{"run", "--rm", "-i"}

	// GPU support: use --gpus for NVIDIA GPU passthrough.
	if opts.GPU.Enabled {
		if opts.GPU.DeviceID != "" {
			// Specific GPU(s): --gpus '"device=0"'
			dockerArgs = append(dockerArgs, "--gpus", fmt.Sprintf(`"device=%s"`, opts.GPU.DeviceID))
			// Also set CUDA_VISIBLE_DEVICES for applications that check it.
			dockerArgs = append(dockerArgs, "-e", "CUDA_VISIBLE_DEVICES="+opts.GPU.DeviceID)
		} else {
			// All GPUs
			dockerArgs = append(dockerArgs, "--gpus", "all")
		}
	}

	// Determine container working directory.
	containerWorkDir := "/var/spool/cwl"
	if dockerOutputDir != "" {
		// When dockerOutputDirectory is specified, outputs go there instead.
		containerWorkDir = dockerOutputDir
	}

	// Mount working directory (resolve symlinks for macOS /tmp -> /private/tmp).
	// Use --mount syntax to handle paths with colons (Docker -v uses : as separator).
	// For Docker-in-Docker, translate container paths to host paths.
	absWorkDir := translateDockerPath(ResolveSymlinks(workDir), opts.DockerHostPathMap)
	dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=/var/spool/cwl", absWorkDir))

	// If dockerOutputDirectory is specified, mount outputDir there.
	if dockerOutputDir != "" {
		absOutputDir := translateDockerPath(ResolveSymlinks(outputDir), opts.DockerHostPathMap)
		dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", absOutputDir, dockerOutputDir))
	}

	// Set working directory.
	dockerArgs = append(dockerArgs, "-w", containerWorkDir)

	// Mount tmp directory.
	absTmpDir := translateDockerPath(ResolveSymlinks(tmpDir), opts.DockerHostPathMap)
	dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=/tmp", absTmpDir))

	// Mount input files that are outside working directory.
	mounts := CollectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		translatedPath := translateDockerPath(hostPath, opts.DockerHostPathMap)
		dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", translatedPath, containerPath))
	}

	// Mount files/directories at absolute paths (from InitialWorkDirRequirement with absolute entryname).
	for _, m := range containerMounts {
		absHostPath := translateDockerPath(ResolveSymlinks(m.HostPath), opts.DockerHostPathMap)
		dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", absHostPath, m.ContainerPath))
	}

	// Add image.
	dockerArgs = append(dockerArgs, dockerImage)

	// Add tool command. For ShellCommandRequirement, wrap in /bin/sh -c.
	if hasShellCommandRequirement(tool) {
		cmdStr := strings.Join(cmdResult.Command, " ")
		dockerArgs = append(dockerArgs, "/bin/sh", "-c", cmdStr)
	} else {
		dockerArgs = append(dockerArgs, cmdResult.Command...)
	}

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

	// Handle stdout - capture to file if specified or needed for output.
	var stdoutFile *os.File
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		var err error
		stdoutFile, err = os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		// Discard stdout to keep JSON output clean.
		cmd.Stdout = io.Discard
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr - capture to file if specified or needed for output.
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = io.Discard
	}

	// Run Docker command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				return nil, fmt.Errorf("docker command failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("docker command failed: %w", err)
		}
	}

	// Capture resource usage from ProcessState (note: this is for the docker CLI, not the container).
	peakMemoryKB := getResourceUsage(cmd.ProcessState)
	duration := time.Since(startTime)

	// If dockerOutputDirectory was used, copy outputs from outputDir to workDir.
	if dockerOutputDir != "" {
		if err := iwdr.CopyDirContents(outputDir, workDir); err != nil {
			return nil, fmt.Errorf("copy outputs from dockerOutputDirectory: %w", err)
		}
	}

	// Collect outputs (passing exit code for runtime.exitCode).
	outputs, err := e.CollectOutputs(tool, workDir, inputs, exitCode, opts.OutDir, opts.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return &Result{
		Outputs:      outputs,
		ExitCode:     exitCode,
		PeakMemoryKB: peakMemoryKB,
		StartTime:    startTime,
		Duration:     duration,
	}, nil
}

// executeInApptainer executes a tool in an Apptainer container with the specified work directory.
// Note: For containerized execution, resource usage captures the container CLI process overhead,
// not the application inside the container.
func (e *Executor) executeInApptainer(ctx context.Context, opts *Options) (*Result, error) {
	startTime := time.Now()
	dockerImage := opts.DockerImage
	e.logger.Info("executing in Apptainer", "image", dockerImage, "command", opts.Command.Command)

	tool := opts.Tool
	workDir := opts.WorkDir
	cmdResult := opts.Command
	inputs := opts.Inputs
	containerMounts := opts.ContainerMounts
	dockerOutputDir := opts.DockerOutputDir

	// Create directories for this execution.
	tmpDir := workDir + "_tmp"
	outputDir := workDir + "_output" // For dockerOutputDirectory
	for _, dir := range []string{workDir, tmpDir, outputDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Stage input files.
	if err := stageInputFiles(inputs, workDir); err != nil {
		return nil, fmt.Errorf("stage inputs: %w", err)
	}

	// Build Apptainer command.
	apptainerArgs := []string{"exec"}

	// Determine container working directory.
	containerWorkDir := "/var/spool/cwl"
	if dockerOutputDir != "" {
		containerWorkDir = dockerOutputDir
	}

	// Mount working directory (resolve symlinks for consistency).
	absWorkDir := ResolveSymlinks(workDir)
	apptainerArgs = append(apptainerArgs, "--bind", absWorkDir+":/var/spool/cwl")

	// If dockerOutputDirectory is specified, mount outputDir there.
	if dockerOutputDir != "" {
		absOutputDir := ResolveSymlinks(outputDir)
		apptainerArgs = append(apptainerArgs, "--bind", absOutputDir+":"+dockerOutputDir)
	}

	// Set working directory.
	apptainerArgs = append(apptainerArgs, "--pwd", containerWorkDir)

	// Mount tmp directory.
	absTmpDir := ResolveSymlinks(tmpDir)
	apptainerArgs = append(apptainerArgs, "--bind", absTmpDir+":/tmp")

	// Mount input files that are outside working directory.
	mounts := CollectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		apptainerArgs = append(apptainerArgs, "--bind", hostPath+":"+containerPath+":ro")
	}

	// Mount files/directories at absolute paths (from InitialWorkDirRequirement with absolute entryname).
	for _, m := range containerMounts {
		absHostPath := ResolveSymlinks(m.HostPath)
		apptainerArgs = append(apptainerArgs, "--bind", absHostPath+":"+m.ContainerPath)
	}

	// Add image with docker:// prefix for pulling from Docker registries.
	apptainerArgs = append(apptainerArgs, "docker://"+dockerImage)

	// Add tool command. For ShellCommandRequirement, wrap in /bin/sh -c.
	if hasShellCommandRequirement(tool) {
		cmdStr := strings.Join(cmdResult.Command, " ")
		apptainerArgs = append(apptainerArgs, "/bin/sh", "-c", cmdStr)
	} else {
		apptainerArgs = append(apptainerArgs, cmdResult.Command...)
	}

	e.logger.Debug("apptainer command", "args", apptainerArgs)

	cmd := exec.CommandContext(ctx, "apptainer", apptainerArgs...)

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

	// Handle stdout - capture to file if specified or needed for output.
	var stdoutFile *os.File
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		var err error
		stdoutFile, err = os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = io.Discard
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr - capture to file if specified or needed for output.
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = io.Discard
	}

	// Run Apptainer command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				return nil, fmt.Errorf("apptainer command failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("apptainer command failed: %w", err)
		}
	}

	// Capture resource usage from ProcessState (note: this is for the apptainer CLI, not the container).
	peakMemoryKB := getResourceUsage(cmd.ProcessState)
	duration := time.Since(startTime)

	// If dockerOutputDirectory was used, copy outputs from outputDir to workDir.
	if dockerOutputDir != "" {
		if err := iwdr.CopyDirContents(outputDir, workDir); err != nil {
			return nil, fmt.Errorf("copy outputs from dockerOutputDirectory: %w", err)
		}
	}

	// Collect outputs (passing exit code for runtime.exitCode).
	outputs, err := e.CollectOutputs(tool, workDir, inputs, exitCode, opts.OutDir, opts.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return &Result{
		Outputs:      outputs,
		ExitCode:     exitCode,
		PeakMemoryKB: peakMemoryKB,
		StartTime:    startTime,
		Duration:     duration,
	}, nil
}

// moveFile moves a file from src to dst, attempting rename first then falling back to copy.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-device fallback: copy then remove.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return err
	}
	return os.Remove(src)
}

// ResolveSymlinks resolves symlinks in a path for Docker mounts.
// On macOS, /tmp is a symlink to /private/tmp which can cause issues with Docker.
// Always returns an absolute path.
func ResolveSymlinks(path string) string {
	// First make absolute.
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	// Then resolve symlinks.
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return absPath
	}
	return resolved
}

// translateDockerPath translates a container path to the Docker host path.
// This is needed for Docker-in-Docker scenarios where the worker container
// uses the host's Docker socket. Paths in docker run commands must be valid
// on the host, not inside the worker container.
func translateDockerPath(path string, pathMap map[string]string) string {
	if pathMap == nil || len(pathMap) == 0 {
		return path
	}

	// Try each mapping prefix.
	for containerPrefix, hostPrefix := range pathMap {
		if strings.HasPrefix(path, containerPrefix) {
			return hostPrefix + strings.TrimPrefix(path, containerPrefix)
		}
	}

	return path
}
