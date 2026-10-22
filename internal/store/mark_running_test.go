package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// markRunningFixture creates a workflow + submission and returns the store and
// submission for task seeding.
func markRunningFixture(t *testing.T) (*SQLiteStore, *model.Submission) {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	return st, sub
}

func TestMarkTaskRunning(t *testing.T) {
	preSet := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)

	tests := []struct {
		name        string
		initial     model.TaskState
		startedAt   *time.Time
		wantApplied bool
		wantState   model.TaskState
	}{
		{"applies from QUEUED, stamps started_at", model.TaskStateQueued, nil, true, model.TaskStateRunning},
		{"applies from QUEUED, preserves pre-set started_at", model.TaskStateQueued, &preSet, true, model.TaskStateRunning},
		// RUNNING simulates a checkout that won the race: the no-op must leave
		// the row — external_id included — completely untouched (the F-J zombie
		// was a full-row write here resetting external_id).
		{"refuses from RUNNING", model.TaskStateRunning, &preSet, false, model.TaskStateRunning},
		// SKIPPED simulates a concurrent cancel that already terminalized.
		{"refuses from SKIPPED", model.TaskStateSkipped, nil, false, model.TaskStateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, sub := markRunningFixture(t)
			ctx := context.Background()

			task := sampleTask(sub.ID)
			task.State = tt.initial
			task.ExecutorType = model.ExecutorTypeWorker
			task.ExternalID = "wrk_owner"
			task.StartedAt = tt.startedAt
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			applied, err := st.MarkTaskRunning(ctx, task.ID)
			if err != nil {
				t.Fatalf("mark running: %v", err)
			}
			if applied != tt.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tt.wantApplied)
			}

			got, err := st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if got.State != tt.wantState {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
			// external_id must survive both the applied write and the no-op.
			if got.ExternalID != "wrk_owner" {
				t.Errorf("external_id = %q, want %q (must never be touched)", got.ExternalID, "wrk_owner")
			}
			if tt.startedAt != nil {
				if got.StartedAt == nil || !got.StartedAt.Equal(*tt.startedAt) {
					t.Errorf("started_at = %v, want preserved %v", got.StartedAt, *tt.startedAt)
				}
			} else if tt.wantApplied {
				if got.StartedAt == nil {
					t.Error("expected started_at to be stamped")
				}
			} else if got.StartedAt != nil {
				t.Errorf("started_at = %v, want nil (no-op must not stamp)", got.StartedAt)
			}
		})
	}
}

func TestUpdateTaskPriority(t *testing.T) {
	st, sub := markRunningFixture(t)
	ctx := context.Background()

	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	task.ExternalID = "wrk_owner"
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := st.UpdateTaskPriority(ctx, task.ID, 7); err != nil {
		t.Fatalf("update priority: %v", err)
	}

	got, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Priority != 7 {
		t.Errorf("priority = %d, want 7", got.Priority)
	}
	// Only the priority column may change.
	if got.State != model.TaskStateQueued {
		t.Errorf("state = %q, want QUEUED", got.State)
	}
	if got.ExternalID != "wrk_owner" {
		t.Errorf("external_id = %q, want wrk_owner", got.ExternalID)
	}

	if err := st.UpdateTaskPriority(ctx, "task_missing", 1); err == nil {
		t.Error("expected error for unknown task id")
	}
}
