package runtime

import (
	"bytes"
	"context"
	"fmt"
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

	// Set environment variables.
	if len(spec.Env) > 0 {
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
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
