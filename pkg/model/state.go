package model

// TaskState represents the lifecycle state of a Task.
type TaskState string

const (
	TaskStatePending   TaskState = "PENDING"
	TaskStateScheduled TaskState = "SCHEDULED"
	TaskStateQueued    TaskState = "QUEUED"
	TaskStateRunning   TaskState = "RUNNING"
	TaskStateSuccess   TaskState = "SUCCESS"
	TaskStateFailed    TaskState = "FAILED"
	TaskStateRetrying  TaskState = "RETRYING"
	TaskStateSkipped   TaskState = "SKIPPED"
)

// String returns the string representation of the task state.
func (s TaskState) String() string {
	return string(s)
}

// IsTerminal returns true if the task is in a final state.
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskStateSuccess, TaskStateFailed, TaskStateSkipped:
		return true
	}
	return false
}

// ValidTransitions defines the allowed state transitions for Tasks.
var ValidTaskTransitions = map[TaskState][]TaskState{
	TaskStatePending:   {TaskStateScheduled, TaskStateSkipped},
	TaskStateScheduled: {TaskStateQueued},
	TaskStateQueued:    {TaskStateRunning},
	TaskStateRunning:   {TaskStateSuccess, TaskStateFailed, TaskStateSkipped},
	TaskStateFailed:    {TaskStateRetrying},
	TaskStateRetrying:  {TaskStateQueued},
}

// CanTransitionTo returns true if moving from the current state to next is valid.
func (s TaskState) CanTransitionTo(next TaskState) bool {
	for _, allowed := range ValidTaskTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// SubmissionState represents the lifecycle state of a Submission.
type SubmissionState string

const (
	SubmissionStatePending   SubmissionState = "PENDING"
	SubmissionStateRunning   SubmissionState = "RUNNING"
	SubmissionStateCompleted SubmissionState = "COMPLETED"
	SubmissionStateFailed    SubmissionState = "FAILED"
	SubmissionStateCancelled SubmissionState = "CANCELLED"
)

// String returns the string representation of the submission state.
func (s SubmissionState) String() string {
	return string(s)
}

// IsTerminal returns true if the submission is in a final state.
func (s SubmissionState) IsTerminal() bool {
	switch s {
	case SubmissionStateCompleted, SubmissionStateFailed, SubmissionStateCancelled:
		return true
	}
	return false
}

// ValidSubmissionTransitions defines the allowed state transitions for Submissions.
var ValidSubmissionTransitions = map[SubmissionState][]SubmissionState{
	SubmissionStatePending: {SubmissionStateRunning, SubmissionStateCancelled},
	SubmissionStateRunning: {SubmissionStateCompleted, SubmissionStateFailed, SubmissionStateCancelled},
}

// CanTransitionTo returns true if moving from the current state to next is valid.
func (s SubmissionState) CanTransitionTo(next SubmissionState) bool {
	for _, allowed := range ValidSubmissionTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// ExecutorType identifies which executor backend runs a Task.
type ExecutorType string

const (
	ExecutorTypeLocal     ExecutorType = "local"
	ExecutorTypeBVBRC     ExecutorType = "bvbrc"
	ExecutorTypeContainer ExecutorType = "container"
)
