package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Bare executes commands directly on the host without containerization.
type Bare struct {
	runner CommandRunner
}

// NewBare creates a Bare runtime.
func NewBare() *Bare {
	return &Bare{runner: &osCommandRunner{}}
}

// NewBareWithRunner creates a Bare runtime with a custom command runner (for testing).
func NewBareWithRunner(runner CommandRunner) *Bare {
	return &Bare{runner: runner}
}

// Run executes the command directly on the host.
func (r *Bare) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if len(spec.Command) == 0 {
		return RunResult{}, fmt.Errorf("bare runtime: empty command")
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.WorkDir

	// Set environment variables. A nil cmd.Env would inherit the whole host
	// environment (worker secrets included), so always start from the
	// allow-listed base unless the degraded InheritHostEnv mode is on.
	if spec.InheritHostEnv {
		cmd.Env = os.Environ()
	} else {
		cmd.Env = BaseHostEnv(spec.GPU.Enabled)
	}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	result := RunResult{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	switch e := runErr.(type) {
	case nil:
		result.ExitCode = 0
	case *exec.ExitError:
		result.ExitCode = e.ExitCode()
	default:
		return result, fmt.Errorf("bare runtime: %w", runErr)
	}

	return result, nil
}
