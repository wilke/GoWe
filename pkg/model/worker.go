package model

import "time"

// Worker represents a remote worker process that pulls and executes tasks.
type Worker struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Hostname     string            `json:"hostname"`
	State        WorkerState       `json:"state"`
	Runtime      ContainerRuntime  `json:"runtime"`
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

// ContainerRuntime identifies which container runtime a worker supports.
type ContainerRuntime string

const (
	RuntimeDocker    ContainerRuntime = "docker"
	RuntimeApptainer ContainerRuntime = "apptainer"
	RuntimeNone      ContainerRuntime = "none"
)
