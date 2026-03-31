package model

import "time"

// StepInstance is a runtime instance of a Step within a Submission.
// It tracks the step's lifecycle: dependency readiness, scatter fan-out/fan-in,
// conditional evaluation, and sub-workflow coordination.
type StepInstance struct {
	ID            string            `json:"id"`
	SubmissionID  string            `json:"submission_id"`
	StepID        string            `json:"step_id"`
	State         StepInstanceState `json:"state"`
	ScatterCount  int               `json:"scatter_count"`            // Number of scatter iterations (0 for non-scatter)
	ScatterMethod string            `json:"scatter_method,omitempty"` // dotproduct, flat_crossproduct, nested_crossproduct
	ScatterDims   []int             `json:"scatter_dims,omitempty"`   // Size of each scatter dimension
	Outputs       map[string]any    `json:"outputs,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

// StepInstanceState represents the lifecycle state of a StepInstance.
type StepInstanceState string

const (
	StepStateWaiting    StepInstanceState = "WAITING"
	StepStateReady      StepInstanceState = "READY"
	StepStateDispatched StepInstanceState = "DISPATCHED"
	StepStateRunning    StepInstanceState = "RUNNING"
	StepStateCompleted  StepInstanceState = "COMPLETED"
	StepStateFailed     StepInstanceState = "FAILED"
	StepStateSkipped    StepInstanceState = "SKIPPED"
)

// String returns the string representation of the step instance state.
func (s StepInstanceState) String() string {
	return string(s)
}

// IsTerminal returns true if the step instance is in a final state.
func (s StepInstanceState) IsTerminal() bool {
	switch s {
	case StepStateCompleted, StepStateFailed, StepStateSkipped:
		return true
	}
	return false
}

// ValidStepTransitions defines the allowed state transitions for StepInstances.
var ValidStepTransitions = map[StepInstanceState][]StepInstanceState{
	StepStateWaiting:    {StepStateReady, StepStateSkipped},
	StepStateReady:      {StepStateDispatched, StepStateFailed, StepStateSkipped},
	StepStateDispatched: {StepStateRunning, StepStateCompleted, StepStateFailed},
	StepStateRunning:    {StepStateCompleted, StepStateFailed},
}

// CanTransitionTo returns true if moving from the current state to next is valid.
func (s StepInstanceState) CanTransitionTo(next StepInstanceState) bool {
	for _, allowed := range ValidStepTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}
