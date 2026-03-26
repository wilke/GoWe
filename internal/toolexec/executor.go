// Package toolexec provides shared CWL tool execution logic.
// This package is used by both cwl-runner and worker to execute CommandLineTools.
package toolexec

import (
	"context"
	"log/slog"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/pkg/cwl"
)

// ExecutionMode specifies how the tool should be executed.
type ExecutionMode int

const (
	// ModeLocal executes tools directly on the host.
	ModeLocal ExecutionMode = iota
	// ModeDocker executes tools in Docker containers.
	ModeDocker
	// ModeApptainer executes tools in Apptainer containers.
	ModeApptainer
)

// Result holds the result of a tool execution including metrics.
type Result struct {
	Outputs      map[string]any
	ExitCode     int
	PeakMemoryKB int64
	StartTime    time.Time
	Duration     time.Duration
}

// GPUConfig holds GPU configuration for container execution.
type GPUConfig struct {
	Enabled  bool   // Enable GPU support
	DeviceID string // Specific GPU device ID (e.g., "0", "1") - empty means use all/auto
}

// ResourceConfig holds resource limits for container execution.
type ResourceConfig struct {
	Cores            int   // Effective CPU cores for this task
	RamMB            int64 // Effective memory in MiB for this task
	ApptainerCgroups bool  // System supports cgroups v2 unified (Apptainer --memory/--cpus)
}

// ExtraBind represents an additional bind mount for container execution.
type ExtraBind struct {
	HostPath      string
	ContainerPath string
}

// Options configures a tool execution.
type Options struct {
	// Tool is the CWL CommandLineTool to execute.
	Tool *cwl.CommandLineTool

	// Command is the built command line from cmdline package.
	Command *cmdline.BuildResult

	// Inputs are the resolved input values.
	Inputs map[string]any

	// WorkDir is the execution working directory.
	WorkDir string

	// OutDir is the output directory path (for runtime.outdir expressions).
	OutDir string

	// Mode specifies the execution mode (local, docker, apptainer).
	Mode ExecutionMode

	// DockerImage is the container image to use (for docker/apptainer modes).
	DockerImage string

	// ContainerMounts are additional mounts for InitialWorkDirRequirement.
	ContainerMounts []iwdr.ContainerMount

	// DockerOutputDir is a custom output directory inside the container.
	DockerOutputDir string

	// Namespaces maps namespace prefixes to URIs for format resolution.
	Namespaces map[string]string

	// DockerHostPathMap maps container paths to Docker host paths.
	// This is needed for Docker-in-Docker scenarios where the worker runs
	// in a container but uses the host's Docker socket.
	// Format: container_path -> host_path
	DockerHostPathMap map[string]string

	// DockerVolume is a named Docker volume shared between the worker and
	// tool containers. When set, the volume is mounted into tool containers
	// and all paths under the volume mount point are used directly without
	// host path translation.
	DockerVolume string

	// ImageDir is the base directory for resolving relative .sif image paths.
	// When a DockerRequirement specifies a .sif file (e.g., "all.sif"),
	// it is resolved relative to this directory.
	ImageDir string

	// GPU configuration for container execution.
	GPU GPUConfig

	// Resources holds resource limits for container execution.
	Resources ResourceConfig

	// JobRequirements are cwl:requirements from the job file.
	// These can add or override tool requirements.
	JobRequirements []any

	// ExtraBinds are additional bind mounts injected into containers.
	// Used for pre-staged reference datasets and admin-specified paths.
	ExtraBinds []ExtraBind
}

// Executor executes CWL CommandLineTools.
type Executor struct {
	logger *slog.Logger
}

// NewExecutor creates a new Executor with the given logger.
func NewExecutor(logger *slog.Logger) *Executor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{logger: logger}
}

// Execute runs a CWL tool with the given options.
func (e *Executor) Execute(ctx context.Context, opts *Options) (*Result, error) {
	switch opts.Mode {
	case ModeDocker:
		return e.executeInDocker(ctx, opts)
	case ModeApptainer:
		return e.executeInApptainer(ctx, opts)
	default:
		return e.executeLocal(ctx, opts)
	}
}
