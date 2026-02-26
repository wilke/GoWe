package execution

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/pkg/cwl"
)

// ApptainerRuntime executes commands in Apptainer containers.
type ApptainerRuntime struct {
	// ApptainerCommand is the path to the apptainer binary (default: "apptainer").
	ApptainerCommand string
}

// Run executes a command in an Apptainer container.
func (r *ApptainerRuntime) Run(ctx context.Context, spec RunSpec) (*RunResult, error) {
	if len(spec.Command) == 0 {
		return nil, ErrEmptyCommand
	}

	if spec.Image == "" {
		return nil, ErrNoDockerImage
	}

	apptainerCmd := r.ApptainerCommand
	if apptainerCmd == "" {
		apptainerCmd = "apptainer"
	}

	// Build Apptainer command.
	args := []string{"exec"}

	// GPU support: use --nv for NVIDIA GPU passthrough.
	if spec.GPU.Enabled {
		args = append(args, "--nv")
		if spec.GPU.DeviceID != "" {
			// Set CUDA_VISIBLE_DEVICES for applications that check it.
			if spec.Env == nil {
				spec.Env = make(map[string]string)
			}
			spec.Env["CUDA_VISIBLE_DEVICES"] = spec.GPU.DeviceID
		}
	}

	// Mount working directory.
	absWorkDir := resolveSymlinks(spec.WorkDir)
	args = append(args, "--bind", absWorkDir+":/var/spool/cwl")
	args = append(args, "--pwd", "/var/spool/cwl")

	// Mount volumes.
	for hostPath, containerPath := range spec.Volumes {
		resolved := resolveSymlinks(hostPath)
		args = append(args, "--bind", resolved+":"+containerPath+":ro")
	}

	// Set environment variables.
	for k, v := range spec.Env {
		args = append(args, "--env", k+"="+v)
	}

	// Add image with docker:// prefix for pulling from Docker registries.
	args = append(args, "docker://"+spec.Image)
	args = append(args, spec.Command...)

	cmd := exec.CommandContext(ctx, apptainerCmd, args...)

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
			return nil, fmt.Errorf("run apptainer: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}

// executeApptainer executes a tool in an Apptainer container.
// containerMounts contains files from InitialWorkDirRequirement with absolute entrynames.
// dockerOutputDir: custom output directory inside container (from dockerOutputDirectory).
func (e *Engine) executeApptainer(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string, containerMounts []ContainerMount, dockerOutputDir string) (*RunResult, error) {
	e.logger.Info("executing in Apptainer", "image", dockerImage, "command", cmdResult.Command)

	// Create directories.
	tmpDir := workDir + "_tmp"
	outputDir := workDir + "_output" // For dockerOutputDirectory
	for _, dir := range []string{workDir, tmpDir, outputDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Determine container working directory.
	containerWorkDir := "/var/spool/cwl"
	if dockerOutputDir != "" {
		containerWorkDir = dockerOutputDir
	}

	// Build Apptainer command.
	apptainerArgs := []string{"exec"}

	// GPU support: use --nv for NVIDIA GPU passthrough.
	if e.gpu.Enabled {
		apptainerArgs = append(apptainerArgs, "--nv")
		if e.gpu.DeviceID != "" {
			apptainerArgs = append(apptainerArgs, "--env", "CUDA_VISIBLE_DEVICES="+e.gpu.DeviceID)
		}
	}

	// Mount working directory.
	absWorkDir := resolveSymlinks(workDir)
	apptainerArgs = append(apptainerArgs, "--bind", absWorkDir+":/var/spool/cwl")

	// If dockerOutputDirectory is specified, mount outputDir there.
	if dockerOutputDir != "" {
		absOutputDir := resolveSymlinks(outputDir)
		apptainerArgs = append(apptainerArgs, "--bind", absOutputDir+":"+dockerOutputDir)
	}

	// Set working directory.
	apptainerArgs = append(apptainerArgs, "--pwd", containerWorkDir)

	// Mount tmp directory.
	absTmpDir := resolveSymlinks(tmpDir)
	apptainerArgs = append(apptainerArgs, "--bind", absTmpDir+":/tmp")

	// Mount input files that are outside working directory.
	mounts := collectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		apptainerArgs = append(apptainerArgs, "--bind", hostPath+":"+containerPath+":ro")
	}

	// Mount files from InitialWorkDirRequirement with absolute entrynames.
	for _, cm := range containerMounts {
		resolved := resolveSymlinks(cm.HostPath)
		apptainerArgs = append(apptainerArgs, "--bind", resolved+":"+cm.ContainerPath)
	}

	// Set environment variables.
	envVars := extractEnvVars(tool, inputs)
	for name, value := range envVars {
		apptainerArgs = append(apptainerArgs, "--env", fmt.Sprintf("%s=%s", name, value))
	}

	// Add image with docker:// prefix.
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

	// Run Apptainer command.
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run apptainer: %w", err)
		}
	}

	// If dockerOutputDirectory was used, copy outputs from outputDir to workDir.
	if dockerOutputDir != "" {
		if err := iwdr.CopyDirContents(outputDir, workDir); err != nil {
			return nil, fmt.Errorf("copy outputs from dockerOutputDirectory: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}
