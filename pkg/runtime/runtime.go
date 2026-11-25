// Package runtime provides container execution abstractions for Docker, Apptainer, and bare execution.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runtime executes commands, optionally inside a container.
type Runtime interface {
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// RunSpec describes what to execute.
type RunSpec struct {
	Image   string            // Container image (empty for bare execution)
	Command []string          // Command and arguments
	WorkDir string            // Working directory on the host
	Volumes map[string]string // host:container mount pairs
	Env     map[string]string // Environment variables
	GPU     GPUConfig         // GPU configuration
	Network NetworkConfig     // Network configuration

	// InheritHostEnv makes the bare runtime pass the full host environment to
	// the process (degraded; default is the BaseHostEnv allow-list).
	InheritHostEnv bool
	// ApptainerNoContainall drops --containall from the apptainer command
	// (--cleanenv is kept).
	ApptainerNoContainall bool
}

// GPUConfig specifies GPU access for one execution, in one of three cases:
//
//   - Legacy (Devices empty, AccessDenied false): the worker's static --gpu /
//     --gpu-id, given to every task of a one-task-at-a-time worker. Enabled
//     gives GPU access; DeviceID restricts it to that comma-separated list,
//     empty means every GPU.
//   - Assigned (Devices non-empty): the devices a multi-task worker assigned
//     to this task (UUIDs, or indices when the inventory has no UUID).
//     Enabled and DeviceID are ignored.
//   - AccessDenied: a multi-task GPU worker runs a task that needs no GPU;
//     the tool gets no GPU access. Overrides everything else.
type GPUConfig struct {
	Enabled  bool     // Whether to enable GPU access (legacy)
	DeviceID string   // Specific GPU device (e.g., "0", "1", "0,1") - empty means all (legacy)
	Devices  []string // Devices assigned to this task (multi-task worker)
	// AccessDenied means GPU access is denied for this task.
	AccessDenied bool
}

// HasAccess reports whether the execution gets GPU access (Docker --gpus,
// Apptainer --nv, the host CUDA_* / NVIDIA_* environment).
func (g GPUConfig) HasAccess() bool {
	return !g.AccessDenied && (len(g.Devices) > 0 || g.Enabled)
}

// VisibleDevices returns the comma-separated device list the execution is
// restricted to and whether it is restricted at all: the assigned Devices,
// else the legacy DeviceID when Enabled, else "" with restricted true when
// access is denied (no device). restricted is false for every GPU (legacy
// without DeviceID) and for no GPU configuration at all.
func (g GPUConfig) VisibleDevices() (list string, restricted bool) {
	switch {
	case g.AccessDenied:
		return "", true
	case len(g.Devices) > 0:
		return strings.Join(g.Devices, ","), true
	case g.Enabled && g.DeviceID != "":
		return g.DeviceID, true
	default:
		return "", false
	}
}

// CUDAEnv returns the CUDA_* variables for an execution that sees the host's
// device numbering (host processes and Apptainer; not Docker, which
// renumbers the devices it passes in): CUDA_VISIBLE_DEVICES=<list> plus
// CUDA_DEVICE_ORDER=PCI_BUS_ID for specific devices, so an index means the
// same device to CUDA as to nvidia-smi; CUDA_VISIBLE_DEVICES= (empty, no
// device) when access is denied; nothing otherwise.
func (g GPUConfig) CUDAEnv() []string {
	list, restricted := g.VisibleDevices()
	switch {
	case !restricted:
		return nil
	case list == "":
		return []string{"CUDA_VISIBLE_DEVICES="}
	default:
		return []string{"CUDA_VISIBLE_DEVICES=" + list, "CUDA_DEVICE_ORDER=PCI_BUS_ID"}
	}
}

// DockerGPUArgs returns the docker run arguments for GPU access: none without
// access, "--gpus all" for every GPU, else "--gpus" followed by exactly
// `"device=a,b"`, double quotes included. Docker parses the --gpus value as
// CSV, so an unquoted list would be split at its commas; the argv is exec'd,
// not run through a shell, so the quotes reach docker.
func (g GPUConfig) DockerGPUArgs() []string {
	if !g.HasAccess() {
		return nil
	}
	list, restricted := g.VisibleDevices()
	if !restricted {
		return []string{"--gpus", "all"}
	}
	return []string{"--gpus", `"device=` + list + `"`}
}

// NetworkConfig specifies network requirements for container execution.
type NetworkConfig struct {
	Disabled bool // Whether to disable network access (--network none)
}

// RunResult captures the output of an execution.
type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// osCommandRunner is the real implementation using os/exec.
type osCommandRunner struct{}

func (r *osCommandRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	switch e := runErr.(type) {
	case nil:
		return stdout, stderr, 0, nil
	case *exec.ExitError:
		return stdout, stderr, e.ExitCode(), nil
	default:
		return stdout, stderr, -1, runErr
	}
}

// New creates a Runtime based on the runtime name.
// Supported names: "docker", "apptainer", "none", "" (bare).
// For comma-separated values (e.g. "none,apptainer"), the preferred container
// runtime is selected (first docker/apptainer found), falling back to bare.
func New(name string) (Runtime, error) {
	// Handle comma-separated multi-runtime (e.g. "none,apptainer").
	if strings.Contains(name, ",") {
		for _, part := range strings.Split(name, ",") {
			part = strings.TrimSpace(part)
			switch part {
			case "docker":
				return NewDocker(), nil
			case "apptainer":
				return NewApptainer(), nil
			}
		}
		// No container runtime found — fall back to bare.
		return NewBare(), nil
	}
	switch name {
	case "docker":
		return NewDocker(), nil
	case "apptainer":
		return NewApptainer(), nil
	case "none", "":
		return NewBare(), nil
	default:
		return nil, fmt.Errorf("unknown runtime: %s", name)
	}
}
