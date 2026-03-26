package toolexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/pkg/staging"
)

// resolveApptainerImage resolves a DockerRequirement image name for Apptainer.
// If the image ends with ".sif", it is treated as a local SIF file:
//   - Absolute paths are used as-is.
//   - Relative paths are resolved against imageDir (from --image-dir flag).
//   - If imageDir is empty, the relative path is passed through for Apptainer to resolve.
//
// Non-.sif images are prefixed with "docker://" for registry pulls.
func resolveApptainerImage(dockerImage, imageDir string) string {
	if strings.HasSuffix(dockerImage, ".sif") {
		if filepath.IsAbs(dockerImage) {
			return dockerImage
		}
		if imageDir != "" {
			return filepath.Join(imageDir, dockerImage)
		}
		return dockerImage
	}
	return "docker://" + dockerImage
}

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
		// Join command parts with proper shell quoting.
		cmdStr := cmdResult.JoinForShell()
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, cmdResult.Command[0], cmdResult.Command[1:]...)
	}
	cmd.Dir = workDir

	// Set environment variables.
	// Start with current environment, then override with CWL required vars.
	cmd.Env = os.Environ()

	// CWL spec requires HOME=$runtime.outdir and TMPDIR=$runtime.tmpdir
	tmpDir := workDir + "_tmp"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, fmt.Errorf("create tmp dir: %w", err)
	}
	cmd.Env = append(cmd.Env, "HOME="+workDir)
	cmd.Env = append(cmd.Env, "TMPDIR="+tmpDir)

	// Add environment variables from EnvVarRequirement.
	envVars := extractEnvVars(tool, inputs, opts.JobRequirements)
	for name, value := range envVars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
	}

	// GPU support: set CUDA_VISIBLE_DEVICES for local execution.
	if opts.GPU.Enabled && opts.GPU.DeviceID != "" {
		cmd.Env = append(cmd.Env, "CUDA_VISIBLE_DEVICES="+opts.GPU.DeviceID)
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
	// Docker's NVIDIA runtime remaps GPUs inside the container, so device=3
	// on the host becomes device 0 inside. Do not set CUDA_VISIBLE_DEVICES
	// since --gpus already restricts visibility and the host device IDs
	// don't match the remapped container device IDs.
	if opts.GPU.Enabled {
		if opts.GPU.DeviceID != "" {
			dockerArgs = append(dockerArgs, "--gpus", fmt.Sprintf("device=%s", opts.GPU.DeviceID))
		} else {
			dockerArgs = append(dockerArgs, "--gpus", "all")
		}
	}

	// Resource limits for container execution.
	if opts.Resources.RamMB > 0 {
		dockerArgs = append(dockerArgs, "--memory", fmt.Sprintf("%dm", opts.Resources.RamMB))
	}
	if opts.Resources.Cores > 0 {
		dockerArgs = append(dockerArgs, "--cpus", fmt.Sprintf("%d", opts.Resources.Cores))
	}

	// Network isolation: disable network access unless NetworkAccess requirement enables it.
	// CWL spec: NetworkAccess defaults to false (no network access).
	if !hasNetworkAccess(tool) {
		dockerArgs = append(dockerArgs, "--network", "none")
	}

	// Determine container working directory.
	containerWorkDir := "/var/spool/cwl"
	if dockerOutputDir != "" {
		// When dockerOutputDirectory is specified, outputs go there instead.
		containerWorkDir = dockerOutputDir
	}

	if opts.DockerVolume != "" {
		// Shared volume mode: mount the named volume into the tool container.
		// All files (workdir, staged inputs, uploads) are under the volume mount.
		// Paths are valid as-is — no host path translation needed.
		//
		// Determine the volume mount point by walking up from workDir to the
		// second path component (e.g., /workdir/scratch/task_xxx → /workdir).
		volumeMountPoint := workDir
		for {
			parent := filepath.Dir(volumeMountPoint)
			if parent == "/" || parent == "." {
				break
			}
			volumeMountPoint = parent
		}
		dockerArgs = append(dockerArgs, "-v", opts.DockerVolume+":"+volumeMountPoint)

		// Use the actual workdir path as the container working directory.
		// The runtime context was already built with workDir in cwltool.go.
		containerWorkDir = workDir
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
			// Mount the output directory at the dockerOutputDirectory path.
			// Use volume-subpath so the tool container can write to the
			// dockerOutputDirectory and outputs are captured on the volume.
			subpath := strings.TrimPrefix(outputDir, volumeMountPoint+"/")
			dockerArgs = append(dockerArgs, "--mount",
				fmt.Sprintf("type=volume,source=%s,target=%s,volume-subpath=%s",
					opts.DockerVolume, dockerOutputDir, subpath))
		}
		dockerArgs = append(dockerArgs, "-w", containerWorkDir)

		// CWL spec requires HOME=$runtime.outdir and TMPDIR=$runtime.tmpdir.
		dockerArgs = append(dockerArgs, "-e", "HOME="+containerWorkDir)
		dockerArgs = append(dockerArgs, "-e", "TMPDIR="+tmpDir)

		// Mount input files from outside the volume using path translation.
		mounts := CollectInputMounts(inputs)
		for hostPath, containerPath := range mounts {
			if strings.HasPrefix(hostPath, volumeMountPoint+"/") {
				continue // Already accessible via the named volume.
			}
			// Out-of-volume files: use host path translation.
			translatedPath := translateDockerPath(hostPath, opts.DockerHostPathMap)
			dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", translatedPath, containerPath))
		}

		// Mount IWDR files at absolute paths.
		for _, m := range containerMounts {
			if strings.HasPrefix(m.HostPath, volumeMountPoint+"/") {
				if m.HostPath == m.ContainerPath {
					continue // Same path, already in volume.
				}
				// HostPath is on the volume but needs a different container path.
				// Use volume-subpath to mount from the named volume.
				subpath := strings.TrimPrefix(m.HostPath, volumeMountPoint+"/")
				dockerArgs = append(dockerArgs, "--mount",
					fmt.Sprintf("type=volume,source=%s,target=%s,volume-subpath=%s",
						opts.DockerVolume, m.ContainerPath, subpath))
				continue
			}
			absHostPath := translateDockerPath(ResolveSymlinks(m.HostPath), opts.DockerHostPathMap)
			dockerArgs = append(dockerArgs, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", absHostPath, m.ContainerPath))
		}

		// Extra bind mounts (pre-staged datasets, admin paths).
		for _, eb := range opts.ExtraBinds {
			src := translateDockerPath(ResolveSymlinks(eb.HostPath), opts.DockerHostPathMap)
			dockerArgs = append(dockerArgs, "--mount",
				fmt.Sprintf("type=bind,source=%s,target=%s", src, eb.ContainerPath))
		}
	} else {
		// Bind mount mode: mount working directory with optional host path translation.
		// Use --mount syntax to handle paths with colons (Docker -v uses : as separator).
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

		// Extra bind mounts (pre-staged datasets, admin paths).
		for _, eb := range opts.ExtraBinds {
			src := translateDockerPath(ResolveSymlinks(eb.HostPath), opts.DockerHostPathMap)
			dockerArgs = append(dockerArgs, "--mount",
				fmt.Sprintf("type=bind,source=%s,target=%s", src, eb.ContainerPath))
		}

		// CWL spec requires HOME=$runtime.outdir and TMPDIR=$runtime.tmpdir
		dockerArgs = append(dockerArgs, "-e", "HOME="+containerWorkDir)
		dockerArgs = append(dockerArgs, "-e", "TMPDIR=/tmp")
	}

	// Set environment variables from EnvVarRequirement.
	envVars := extractEnvVars(tool, inputs, opts.JobRequirements)
	for name, value := range envVars {
		dockerArgs = append(dockerArgs, "-e", name+"="+value)
	}

	// Add image.
	dockerArgs = append(dockerArgs, dockerImage)

	// Add tool command. For ShellCommandRequirement, wrap in /bin/sh -c.
	if hasShellCommandRequirement(tool) {
		cmdStr := cmdResult.JoinForShell()
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
	// Always capture stderr in a buffer for error reporting.
	var stderrBuf bytes.Buffer
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = io.MultiWriter(stderrFile, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run Docker command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				stderrMsg := strings.TrimSpace(stderrBuf.String())
				if stderrMsg != "" {
					return nil, fmt.Errorf("docker command failed (exit %d): %s", exitCode, stderrMsg)
				}
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
	e.logger.Info("executing in Apptainer", "image", dockerImage, "image_dir", opts.ImageDir, "command", opts.Command.Command)

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
	// Use "exec" by default. Switch to "run" when the command relies on the
	// Docker ENTRYPOINT (detected by the first command element being a flag
	// like "-c"). "run" honors the image's runscript (Docker ENTRYPOINT),
	// while "exec" bypasses it.
	apptainerSubcmd := "exec"
	if !hasShellCommandRequirement(tool) && len(cmdResult.Command) > 0 && strings.HasPrefix(cmdResult.Command[0], "-") {
		apptainerSubcmd = "run"
	}
	apptainerArgs := []string{apptainerSubcmd}

	// Determine container working directory.
	containerWorkDir := "/var/spool/cwl"
	if dockerOutputDir != "" {
		containerWorkDir = dockerOutputDir
	}

	// Mount working directory and set HOME.
	// Apptainer does not allow overriding HOME via --env; use --home instead
	// which both bind-mounts the directory and sets $HOME.
	absWorkDir := ResolveSymlinks(workDir)
	apptainerArgs = append(apptainerArgs, "--home", absWorkDir+":"+containerWorkDir)

	// If dockerOutputDirectory is specified and differs from default,
	// also mount workDir at /var/spool/cwl for CWL compatibility.
	if dockerOutputDir != "" {
		apptainerArgs = append(apptainerArgs, apptainerMount(absWorkDir, "/var/spool/cwl", "")...)
		absOutputDir := ResolveSymlinks(outputDir)
		apptainerArgs = append(apptainerArgs, apptainerMount(absOutputDir, dockerOutputDir, "")...)
	}

	// Set working directory.
	apptainerArgs = append(apptainerArgs, "--pwd", containerWorkDir)

	// Mount tmp directory.
	absTmpDir := ResolveSymlinks(tmpDir)
	apptainerArgs = append(apptainerArgs, apptainerMount(absTmpDir, "/tmp", "")...)

	// Mount input files that are outside working directory.
	// Use --mount instead of --bind to handle colons in paths.
	mounts := CollectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		apptainerArgs = append(apptainerArgs, apptainerMount(hostPath, containerPath, "ro")...)
	}

	// Mount files/directories at absolute paths (from InitialWorkDirRequirement with absolute entryname).
	for _, m := range containerMounts {
		absHostPath := ResolveSymlinks(m.HostPath)
		apptainerArgs = append(apptainerArgs, apptainerMount(absHostPath, m.ContainerPath, "")...)
	}

	// Extra bind mounts (pre-staged datasets, admin paths).
	for _, eb := range opts.ExtraBinds {
		apptainerArgs = append(apptainerArgs, apptainerMount(ResolveSymlinks(eb.HostPath), eb.ContainerPath, "")...)
	}

	// CWL spec requires TMPDIR=$runtime.tmpdir (HOME is already set by --home above).
	apptainerArgs = append(apptainerArgs, "--env", "TMPDIR=/tmp")

	// Set environment variables from EnvVarRequirement.
	envVars := extractEnvVars(tool, inputs, opts.JobRequirements)
	for name, value := range envVars {
		apptainerArgs = append(apptainerArgs, "--env", name+"="+value)
	}

	// GPU support: use --nv for NVIDIA GPU passthrough.
	if opts.GPU.Enabled {
		apptainerArgs = append(apptainerArgs, "--nv")
		if opts.GPU.DeviceID != "" {
			apptainerArgs = append(apptainerArgs, "--env", "CUDA_VISIBLE_DEVICES="+opts.GPU.DeviceID)
		}
	}

	// Resource limits for container execution.
	// Apptainer --memory/--cpus require cgroups v2 unified mode.
	// On systems without it (most HPC), skip these flags silently.
	if opts.Resources.ApptainerCgroups {
		if opts.Resources.RamMB > 0 {
			apptainerArgs = append(apptainerArgs, "--memory", fmt.Sprintf("%dM", opts.Resources.RamMB))
		}
		if opts.Resources.Cores > 0 {
			apptainerArgs = append(apptainerArgs, "--cpus", fmt.Sprintf("%d", opts.Resources.Cores))
		}
	}

	// NOTE: Apptainer shares the host network by default and --net requires
	// root or admin config. Network isolation (CWL spec default: no network
	// when NetworkAccess requirement is absent) cannot be enforced. This is a
	// known limitation — test 227 (networkaccess_disabled) will fail.

	// Resolve image: local .sif files are used directly, others get docker:// prefix.
	resolvedImage := resolveApptainerImage(dockerImage, opts.ImageDir)
	e.logger.Debug("resolved apptainer image", "original", dockerImage, "resolved", resolvedImage)
	apptainerArgs = append(apptainerArgs, resolvedImage)

	// Add tool command. For ShellCommandRequirement, wrap in /bin/sh -c.
	if hasShellCommandRequirement(tool) {
		cmdStr := cmdResult.JoinForShell()
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
	// Always capture stderr in a buffer for error reporting.
	var stderrBuf bytes.Buffer
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = io.MultiWriter(stderrFile, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run Apptainer command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				stderrMsg := strings.TrimSpace(stderrBuf.String())
				if stderrMsg != "" {
					return nil, fmt.Errorf("apptainer command failed (exit %d): %s", exitCode, stderrMsg)
				}
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
// Delegates to the shared staging package.
// apptainerMount returns the arguments for an Apptainer bind mount.
// Uses --mount instead of --bind to correctly handle colons in paths
// (--bind uses colons as delimiters, which breaks on paths like "A:Gln2Cys").
func apptainerMount(src, dst, opts string) []string {
	spec := "type=bind,source=" + src + ",destination=" + dst
	if opts != "" {
		spec += "," + opts
	}
	return []string{"--mount", spec}
}

func ResolveSymlinks(path string) string {
	return staging.ResolveSymlinks(path)
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
