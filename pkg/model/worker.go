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
