package runtime

import (
	"context"
	"fmt"
)

// Docker executes commands inside Docker containers.
type Docker struct {
	runner CommandRunner
}

// NewDocker creates a Docker runtime.
func NewDocker() *Docker {
	return &Docker{runner: &osCommandRunner{}}
}

// NewDockerWithRunner creates a Docker runtime with a custom command runner (for testing).
func NewDockerWithRunner(runner CommandRunner) *Docker {
	return &Docker{runner: runner}
}

// Run executes the command inside a Docker container.
func (r *Docker) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.Image == "" {
		return RunResult{}, fmt.Errorf("docker runtime: image is required")
	}
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("docker runtime: empty command")
	}

	args := []string{"run", "--rm"}

	// Network configuration.
	if spec.Network.Disabled {
		args = append(args, "--network", "none")
	}

	// GPU support: use --gpus for NVIDIA GPU passthrough: all GPUs, or
	// --gpus '"device=0,1"' for specific ones (see GPUConfig.DockerGPUArgs).
	args = append(args, spec.GPU.DockerGPUArgs()...)
	if len(spec.GPU.Devices) == 0 && !spec.GPU.AccessDenied && spec.GPU.Enabled && spec.GPU.DeviceID != "" {
		// Legacy --gpu-id only (kept as before): also set
		// CUDA_VISIBLE_DEVICES for applications that check it. Not for
		// assigned devices: docker renumbers the devices it passes in.
		args = append(args, "-e", "CUDA_VISIBLE_DEVICES="+spec.GPU.DeviceID)
	}

	// Environment variables.
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Volume mounts.
	args = append(args, "-v", spec.WorkDir+":/work", "-w", "/work")
	for hostPath, containerPath := range spec.Volumes {
		args = append(args, "-v", hostPath+":"+containerPath)
	}

	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	stdout, stderr, exitCode, err := r.runner.Run(ctx, "docker", args...)
	if err != nil {
		return RunResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
			fmt.Errorf("docker runtime: %w", err)
	}

	return RunResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}
