package execution

import (
	"errors"
	"fmt"
)

// Sentinel errors.
var (
	ErrNoDockerImage = errors.New("Docker execution requested but no docker image specified")
	ErrNonZeroExit   = errors.New("command exited with non-zero status")
	ErrEmptyCommand  = errors.New("empty command")
)

// ExecutionError wraps errors with execution phase context.
type ExecutionError struct {
	Phase    string // "stage_in", "build_command", "execute", "collect_outputs", "stage_out"
	Err      error
	ExitCode int
}

func (e *ExecutionError) Error() string {
	if e.ExitCode != 0 {
		return fmt.Sprintf("%s: %v (exit code %d)", e.Phase, e.Err, e.ExitCode)
	}
	return fmt.Sprintf("%s: %v", e.Phase, e.Err)
}

func (e *ExecutionError) Unwrap() error {
	return e.Err
}
