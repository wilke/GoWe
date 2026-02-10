package model

import "testing"

func TestComputeTaskSummary(t *testing.T) {
	tasks := []Task{
		{State: TaskStatePending},
		{State: TaskStatePending},
		{State: TaskStateRunning},
		{State: TaskStateSuccess},
		{State: TaskStateFailed},
		{State: TaskStateSkipped},
		{State: TaskStateQueued},
		{State: TaskStateScheduled},
	}

	got := ComputeTaskSummary(tasks)

	if got.Total != 8 {
		t.Errorf("Total = %d, want 8", got.Total)
	}
	if got.Pending != 2 {
		t.Errorf("Pending = %d, want 2", got.Pending)
	}
	if got.Running != 1 {
		t.Errorf("Running = %d, want 1", got.Running)
	}
	if got.Success != 1 {
		t.Errorf("Success = %d, want 1", got.Success)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", got.Skipped)
	}
	if got.Queued != 1 {
		t.Errorf("Queued = %d, want 1", got.Queued)
	}
	if got.Scheduled != 1 {
		t.Errorf("Scheduled = %d, want 1", got.Scheduled)
	}
}

func TestComputeTaskSummary_Empty(t *testing.T) {
	got := ComputeTaskSummary(nil)
	if got.Total != 0 {
		t.Errorf("Total = %d, want 0", got.Total)
	}
}
