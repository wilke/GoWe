package model

import (
	"fmt"
	"strings"
	"time"
)

// Worker represents a remote worker process that pulls and executes tasks.
type Worker struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Hostname     string            `json:"hostname"`
	Group        string            `json:"group"` // Worker group for task scheduling
	State        WorkerState       `json:"state"`
	Runtime      ContainerRuntime  `json:"runtime"`
	GPUEnabled   bool              `json:"gpu_enabled"`
	GPUDevice    string            `json:"gpu_device,omitempty"`
	Version      string            `json:"version,omitempty"`  // Build version (git commit hash)
	Datasets     map[string]string `json:"datasets,omitempty"` // Dataset ID → host path
	Labels       map[string]string `json:"labels,omitempty"`
	LastSeen     time.Time         `json:"last_seen"`
	CurrentTask  string            `json:"current_task,omitempty"`
	RegisteredAt time.Time         `json:"registered_at"`
	// MaxCPUs and MaxMemMB are the worker's self-reported capacity (its
	// effective --max-cpus / --max-mem). nil means a legacy worker or an
	// undetected value: CheckoutTask and the preflight apply no fit check
	// for that dimension (#324).
	MaxCPUs  *int   `json:"max_cpus,omitempty"`
	MaxMemMB *int64 `json:"max_mem_mb,omitempty"`
	// HostID is a stable, opaque identity of the physical host (a hash of
	// its machine-id, or the worker's --host-id). Empty means not reported
	// (#323).
	HostID string `json:"host_id,omitempty"`
	// HostCPUs and HostMemMB are the host's physical totals, reported even
	// when --max-cpus / --max-mem cap the worker lower. nil means a legacy
	// worker or an undetected value (#323).
	HostCPUs  *int   `json:"host_cpus,omitempty"`
	HostMemMB *int64 `json:"host_mem_mb,omitempty"`
	// Slots is the number of tasks the worker runs concurrently (currently
	// always 1). nil means not reported (#323).
	Slots *int `json:"slots,omitempty"`
	// CgroupCPU and CgroupMemory report whether the cpu / memory cgroup
	// controllers are usable by the worker. nil means not reported (#323).
	CgroupCPU    *bool `json:"cgroup_cpu,omitempty"`
	CgroupMemory *bool `json:"cgroup_memory,omitempty"`
	// GPUs is the GPU inventory reported by a --gpu worker. nil means not
	// reported; an empty list means a GPU worker with no usable GPU
	// metadata (#323).
	GPUs []GPUInfo `json:"gpus,omitempty"`
	// Usage is the latest sanitised usage the worker sent in a heartbeat and
	// UsageAt the server time it was received. nil means never reported (#323).
	Usage   *WorkerUsage `json:"usage,omitempty"`
	UsageAt *time.Time   `json:"usage_at,omitempty"`
	// KeyID is the id of the worker key that registered this worker. All
	// /workers/{id}/* routes require the caller's key id to match. Empty on
	// legacy rows (adopted on first authorized touch) and when worker auth is
	// disabled.
	KeyID string `json:"key_id,omitempty"`
}

// GPUInfo describes one GPU of a worker's host, as reported by nvidia-smi.
type GPUInfo struct {
	Index      int    `json:"index"`        // nvidia-smi device index
	UUID       string `json:"uuid"`         // e.g. GPU-1a2b3c4d-...
	Name       string `json:"name"`         // e.g. NVIDIA H200 NVL
	MemTotalMB int64  `json:"mem_total_mb"` // Total device memory in MiB
}

// WorkerUsage is a worker's live usage sample as stored and exposed by the
// server (#323).
type WorkerUsage struct {
	Host *HostUsage `json:"host,omitempty"`
	GPUs []GPUUsage `json:"gpus,omitempty"`
}

// HostUsage is a sample of the load and memory of a worker's host (#323).
// Memory is in MiB.
type HostUsage struct {
	Load1          float64 `json:"load1"`
	Load5          float64 `json:"load5"`
	MemTotalMB     int64   `json:"mem_total_mb"`
	MemAvailableMB int64   `json:"mem_available_mb"`
	// PSI "some" avg10 percentages from /proc/pressure; nil when unavailable.
	PSICPUSomeAvg10 *float64 `json:"psi_cpu_some_avg10,omitempty"`
	PSIMemSomeAvg10 *float64 `json:"psi_mem_some_avg10,omitempty"`
	// SampledAt is the worker's (UTC) sampling time.
	SampledAt time.Time `json:"sampled_at"`
}

// GPUUsage is a sample of one GPU's usage, identified by its UUID (#323). A
// nil field means the value was not available.
type GPUUsage struct {
	UUID      string   `json:"uuid"`
	MemUsedMB *int64   `json:"mem_used_mb,omitempty"`
	UtilPct   *float64 `json:"util_pct,omitempty"`
}

// MaxHostIDLen is the maximum length of a worker host id (#323).
const MaxHostIDLen = 64

// ValidateHostID checks that a worker host id is 1-MaxHostIDLen characters
// of [A-Za-z0-9._-].
func ValidateHostID(id string) error {
	if id == "" || len(id) > MaxHostIDLen {
		return fmt.Errorf("host id must be 1-%d characters, got %d", MaxHostIDLen, len(id))
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("host id contains invalid character %q (allowed: A-Z a-z 0-9 . _ -)", c)
		}
	}
	return nil
}

// HeartbeatRequest is the body a worker sends on each heartbeat. RunningTasks is
// the set of task IDs the worker is currently executing; the server uses it to
// reconcile orphaned tasks and to decide which tasks to cancel. An empty body
// (no running tasks) is valid and backward-compatible.
type HeartbeatRequest struct {
	RunningTasks []string `json:"running_tasks,omitempty"`
	// Host and GPUs carry the worker's latest usage sample (#323). Both are
	// optional: an older worker omits them and an older server ignores them.
	Host *HostUsage `json:"host,omitempty"`
	GPUs []GPUUsage `json:"gpu_usage,omitempty"`
}

// HeartbeatResponse is the server's reply to a heartbeat. CancelTasks lists task
// IDs the worker is running whose submission has been cancelled — the worker
// should kill those and report them CANCELLED.
type HeartbeatResponse struct {
	WorkerID    string      `json:"worker_id"`
	State       WorkerState `json:"state"`
	CancelTasks []string    `json:"cancel_tasks,omitempty"`
}

// WorkerState represents the lifecycle state of a Worker.
type WorkerState string

const (
	WorkerStateOnline   WorkerState = "online"
	WorkerStateOffline  WorkerState = "offline"
	WorkerStateDraining WorkerState = "draining"
)

// ValidWorkerTransitions defines the allowed state transitions for Workers.
var ValidWorkerTransitions = map[WorkerState][]WorkerState{
	WorkerStateOnline:   {WorkerStateOffline, WorkerStateDraining},
	WorkerStateOffline:  {WorkerStateOnline}, // Worker comes back after heartbeat timeout
	WorkerStateDraining: {WorkerStateOffline},
}

// CanTransitionTo returns true if moving from the current state to next is valid.
func (s WorkerState) CanTransitionTo(next WorkerState) bool {
	for _, allowed := range ValidWorkerTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// ContainerRuntime identifies which container runtime(s) a worker supports.
// A worker may advertise multiple runtimes as a comma-separated string
// (e.g., "none,apptainer"), meaning it can run both bare and containerized tasks.
type ContainerRuntime string

const (
	RuntimeDocker    ContainerRuntime = "docker"
	RuntimeApptainer ContainerRuntime = "apptainer"
	RuntimeNone      ContainerRuntime = "none"
)

// ParseRuntimes splits a comma-separated runtime string into individual values.
func ParseRuntimes(rt ContainerRuntime) []ContainerRuntime {
	var result []ContainerRuntime
	for _, part := range strings.Split(string(rt), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, ContainerRuntime(part))
		}
	}
	if len(result) == 0 {
		return []ContainerRuntime{RuntimeNone}
	}
	return result
}

// HasContainerRuntime returns true if the runtime set includes docker or apptainer.
func HasContainerRuntime(rt ContainerRuntime) bool {
	for _, r := range ParseRuntimes(rt) {
		if r == RuntimeDocker || r == RuntimeApptainer {
			return true
		}
	}
	return false
}

// PreferredContainerRuntime returns the first container runtime (docker/apptainer)
// from the set, or "none" if no container runtime is available.
func PreferredContainerRuntime(rt ContainerRuntime) string {
	for _, r := range ParseRuntimes(rt) {
		if r == RuntimeDocker || r == RuntimeApptainer {
			return string(r)
		}
	}
	return "none"
}

// ValidateRuntimes checks that all values in a comma-separated runtime string are valid.
func ValidateRuntimes(rt string) error {
	for _, part := range strings.Split(rt, ",") {
		part = strings.TrimSpace(part)
		switch ContainerRuntime(part) {
		case RuntimeDocker, RuntimeApptainer, RuntimeNone:
			// valid
		default:
			return fmt.Errorf("invalid runtime %q: must be docker, apptainer, or none", part)
		}
	}
	return nil
}
