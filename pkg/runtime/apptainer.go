package runtime

import (
	"context"
	"fmt"
	"os"
)

// Apptainer executes commands inside Apptainer (Singularity) containers.
type Apptainer struct {
	runner CommandRunner
}

// NewApptainer creates an Apptainer runtime.
func NewApptainer() *Apptainer {
	return &Apptainer{runner: &osCommandRunner{}}
}

// NewApptainerWithRunner creates an Apptainer runtime with a custom command runner (for testing).
func NewApptainerWithRunner(runner CommandRunner) *Apptainer {
	return &Apptainer{runner: runner}
}

// Run executes the command inside an Apptainer container.
func (r *Apptainer) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.Image == "" {
		return RunResult{}, fmt.Errorf("apptainer runtime: image is required")
	}
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("apptainer runtime: empty command")
	}

	// Isolation: private PID namespace/tmp and no host environment.
	args := []string{"exec", "--containall", "--cleanenv"}
	if spec.ApptainerNoContainall {
		args = []string{"exec", "--cleanenv"}
	}

	// Network configuration.
	if spec.Network.Disabled {
		args = append(args, "--net", "--network", "none")
	}

	// GPU support: use --nv for NVIDIA GPU passthrough.
	if spec.GPU.Enabled {
		args = append(args, "--nv")
		// Use CUDA_VISIBLE_DEVICES to restrict to specific GPU(s).
		if spec.GPU.DeviceID != "" {
			args = append(args, "--env", "CUDA_VISIBLE_DEVICES="+spec.GPU.DeviceID)
		}
	}

	// Environment variables.
	for k, v := range spec.Env {
		args = append(args, "--env", k+"="+v)
	}

	// Bind mounts. /tmp gets a per-task directory: under --containall it
	// would otherwise be a small session tmpfs.
	tmpDir := spec.WorkDir + "_tmp"
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("apptainer runtime: create tmp dir: %w", err)
	}
	args = append(args, "--bind", spec.WorkDir+":/work", "--bind", tmpDir+":/tmp", "--pwd", "/work")
	for hostPath, containerPath := range spec.Volumes {
		args = append(args, "--bind", hostPath+":"+containerPath)
	}

	// Image (convert Docker reference to Apptainer format).
	args = append(args, "docker://"+spec.Image)
	args = append(args, spec.Command...)

	stdout, stderr, exitCode, err := r.runner.Run(ctx, "apptainer", args...)
	if err != nil {
		return RunResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode},
			fmt.Errorf("apptainer runtime: %w", err)
	}

	return RunResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}
