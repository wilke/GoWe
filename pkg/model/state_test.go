package model

import "testing"

func TestTaskState_IsTerminal(t *testing.T) {
	tests := []struct {
		state    TaskState
		terminal bool
	}{
		{TaskStatePending, false},
		{TaskStateScheduled, false},
		{TaskStateQueued, false},
		{TaskStateRunning, false},
		{TaskStateSuccess, true},
		{TaskStateFailed, true},
		{TaskStateRetrying, false},
		{TaskStateSkipped, true},
	}
	for _, tt := range tests {
		if got := tt.state.IsTerminal(); got != tt.terminal {
			t.Errorf("TaskState(%q).IsTerminal() = %v, want %v", tt.state, got, tt.terminal)
		}
	}
}

func TestTaskState_CanTransitionTo(t *testing.T) {
	tests := []struct {
		from  TaskState
		to    TaskState
		valid bool
	}{
		// Valid transitions
		{TaskStatePending, TaskStateScheduled, true},
		{TaskStatePending, TaskStateSkipped, true},
		{TaskStateScheduled, TaskStateQueued, true},
		{TaskStateQueued, TaskStateRunning, true},
		{TaskStateRunning, TaskStateSuccess, true},
		{TaskStateRunning, TaskStateFailed, true},
		{TaskStateRunning, TaskStateSkipped, true},
		{TaskStateFailed, TaskStateRetrying, true},
		{TaskStateRetrying, TaskStateQueued, true},

		// Invalid transitions
		{TaskStatePending, TaskStateRunning, false},
		{TaskStatePending, TaskStateSuccess, false},
		{TaskStateScheduled, TaskStateRunning, false},
		{TaskStateSuccess, TaskStatePending, false},
		{TaskStateSuccess, TaskStateFailed, false},
		{TaskStateSkipped, TaskStatePending, false},
		{TaskStateRunning, TaskStatePending, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.valid {
			t.Errorf("TaskState(%q).CanTransitionTo(%q) = %v, want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}

func TestSubmissionState_IsTerminal(t *testing.T) {
	tests := []struct {
		state    SubmissionState
		terminal bool
	}{
		{SubmissionStatePending, false},
		{SubmissionStateRunning, false},
		{SubmissionStateCompleted, true},
		{SubmissionStateFailed, true},
		{SubmissionStateCancelled, true},
	}
	for _, tt := range tests {
		if got := tt.state.IsTerminal(); got != tt.terminal {
			t.Errorf("SubmissionState(%q).IsTerminal() = %v, want %v", tt.state, got, tt.terminal)
		}
	}
}

func TestSubmissionState_CanTransitionTo(t *testing.T) {
	tests := []struct {
		from  SubmissionState
		to    SubmissionState
		valid bool
	}{
		// Valid transitions
		{SubmissionStatePending, SubmissionStateRunning, true},
		{SubmissionStatePending, SubmissionStateCancelled, true},
		{SubmissionStateRunning, SubmissionStateCompleted, true},
		{SubmissionStateRunning, SubmissionStateFailed, true},
		{SubmissionStateRunning, SubmissionStateCancelled, true},

		// Invalid transitions
		{SubmissionStatePending, SubmissionStateCompleted, false},
		{SubmissionStateCompleted, SubmissionStatePending, false},
		{SubmissionStateFailed, SubmissionStateRunning, false},
		{SubmissionStateCancelled, SubmissionStateRunning, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.valid {
			t.Errorf("SubmissionState(%q).CanTransitionTo(%q) = %v, want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}
