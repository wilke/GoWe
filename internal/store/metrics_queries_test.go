package store

import (
	"context"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// TestGetSubmissionMeta verifies the lean workflow_name/labels projection
// used by the server's per-request label lookup (Prometheus workflow
// labeling), and that it returns (nil, nil) for a missing submission.
func TestGetSubmissionMeta(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	sub.Labels = map[string]string{"project": "gowe-metrics"}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	meta, err := st.GetSubmissionMeta(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission meta: %v", err)
	}
	if meta == nil {
		t.Fatal("meta = nil, want a value")
	}
	if meta.WorkflowName != sub.WorkflowName {
		t.Errorf("WorkflowName = %q, want %q", meta.WorkflowName, sub.WorkflowName)
	}
	if meta.Labels["project"] != "gowe-metrics" {
		t.Errorf("Labels[project] = %q, want gowe-metrics", meta.Labels["project"])
	}

	missing, err := st.GetSubmissionMeta(ctx, "sub_does_not_exist")
	if err != nil {
		t.Fatalf("get submission meta (missing): %v", err)
	}
	if missing != nil {
		t.Errorf("meta for missing submission = %+v, want nil", missing)
	}
}

// TestCountTasksByState verifies the global GROUP BY count used to refresh
// the gowe_tasks{state} gauge once per scheduler tick.
func TestCountTasksByState(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	states := []model.TaskState{
		model.TaskStateQueued, model.TaskStateQueued, model.TaskStateRunning, model.TaskStateFailed,
	}
	for i, state := range states {
		task := sampleTask(sub.ID)
		task.ID = task.ID + "-" + string(rune('a'+i))
		task.State = state
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	counts, err := st.CountTasksByState(ctx)
	if err != nil {
		t.Fatalf("count tasks by state: %v", err)
	}
	if counts["QUEUED"] != 2 {
		t.Errorf("QUEUED count = %d, want 2", counts["QUEUED"])
	}
	if counts["RUNNING"] != 1 {
		t.Errorf("RUNNING count = %d, want 1", counts["RUNNING"])
	}
	if counts["FAILED"] != 1 {
		t.Errorf("FAILED count = %d, want 1", counts["FAILED"])
	}
	if counts["SUCCESS"] != 0 {
		t.Errorf("SUCCESS count = %d, want 0 (absent from the map)", counts["SUCCESS"])
	}
}

// TestCountTasksQueuedByGroup verifies the per-group QUEUED count used to
// refresh the gowe_queue_depth{group} gauge, including the ” → "default"
// normalization done in SQL.
func TestCountTasksQueuedByGroup(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	mk := func(id, group string, state model.TaskState) *model.Task {
		task := sampleTask(sub.ID)
		task.ID = id
		task.State = state
		if group != "" {
			task.RuntimeHints = &model.RuntimeHints{WorkerGroup: group}
		}
		return task
	}

	tasks := []*model.Task{
		mk("q1", "esmfold", model.TaskStateQueued),
		mk("q2", "esmfold", model.TaskStateQueued),
		mk("q3", "", model.TaskStateQueued),         // no runtime hints → default group
		mk("q4", "esmfold", model.TaskStateRunning), // not QUEUED, excluded
	}
	for _, task := range tasks {
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", task.ID, err)
		}
	}

	counts, err := st.CountTasksQueuedByGroup(ctx)
	if err != nil {
		t.Fatalf("count tasks queued by group: %v", err)
	}
	if counts["esmfold"] != 2 {
		t.Errorf("esmfold count = %d, want 2", counts["esmfold"])
	}
	if counts["default"] != 1 {
		t.Errorf("default count = %d, want 1", counts["default"])
	}
	if _, ok := counts[""]; ok {
		t.Error(`counts contains raw "" key, want it normalized to "default" in SQL`)
	}
}
