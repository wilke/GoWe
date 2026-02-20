package execution

import (
	"context"
)

// Runtime abstracts the execution environment (local process, Docker, etc.).
type Runtime interface {
	// Run executes a command and returns the result.
	Run(ctx context.Context, spec RunSpec) (*RunResult, error)
}

// RunSpec describes what to execute.
type RunSpec struct {
	Command []string          // Command and arguments
	WorkDir string            // Working directory
	Env     map[string]string // Environment variables
	Stdin   string            // Path to stdin file (optional)
	Stdout  string            // Path to capture stdout (optional)
	Stderr  string            // Path to capture stderr (optional)
	Image   string            // Docker image (for Docker runtime)
	Volumes map[string]string // Host path -> container path mappings
	GPU     GPUConfig         // GPU configuration
}

// GPUConfig specifies GPU requirements for container execution.
type GPUConfig struct {
	Enabled  bool   // Whether to enable GPU access
	DeviceID string // Specific GPU device (e.g., "0", "1", "0,1") - empty means all
}

// RunResult holds the result of a command execution.
type RunResult struct {
	ExitCode int
	Stdout   string // Captured stdout content (if not redirected to file)
	Stderr   string // Captured stderr content (if not redirected to file)
}
