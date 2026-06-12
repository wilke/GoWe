package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// runningTask creates and persists a RUNNING worker task attributed to workerID,
// started startedAgo in the past. Returns the task ID.
func runningTask(t *testing.T, st *SQLiteStore, sub *model.Submission, id, workerID string, startedAgo time.Duration) string {
	t.Helper()
	ctx := context.Background()
	task := sampleTask(sub.ID)
	task.ID = id
	task.State = model.TaskStateRunning
	task.ExecutorType = model.ExecutorTypeWorker
	task.ExternalID = workerID
	started := time.Now().UTC().Add(-startedAgo)
	task.StartedAt = &started
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
	return task.ID
}

func reconcileFixture(t *testing.T) (*SQLiteStore, *model.Submission) {
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

// A RUNNING task the worker no longer reports, older than the grace window, is a
// zombie and must be requeued — this is the server-restart orphan case (#118).
func TestReconcileWorkerTasks_RequeuesOrphan(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	id := runningTask(t, st, sub, "task_zombie", "wrk_alive", 10*time.Minute)

	// Worker reports an empty running set (it forgot the task across a restart).
	requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_alive", nil, time.Minute)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(requeued) != 1 || requeued[0] != id {
		t.Fatalf("requeued = %v, want [%s]", requeued, id)
	}

	got, _ := st.GetTask(ctx, id)
	if got.State != model.TaskStateQueued {
		t.Errorf("state = %s, want QUEUED", got.State)
	}
	if got.ExternalID != "" {
		t.Errorf("external_id = %q, want empty", got.ExternalID)
	}
	if got.StartedAt != nil {
		t.Errorf("started_at = %v, want nil", got.StartedAt)
	}
}

// A RUNNING task the worker still reports as running is legitimately in-flight and
// must be left alone.
func TestReconcileWorkerTasks_LeavesActiveTask(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	id := runningTask(t, st, sub, "task_active", "wrk_alive", 10*time.Minute)

	requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_alive", []string{id}, time.Minute)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(requeued) != 0 {
		t.Fatalf("requeued = %v, want none", requeued)
	}
	got, _ := st.GetTask(ctx, id)
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING (left alone)", got.State)
	}
}

// A freshly-checked-out RUNNING task that the worker has not yet reported (within
// the grace window) must not be reaped — avoids the checkout→first-heartbeat race.
func TestReconcileWorkerTasks_GraceWindow(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	id := runningTask(t, st, sub, "task_fresh", "wrk_alive", 2*time.Second)

	requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_alive", nil, time.Minute)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(requeued) != 0 {
		t.Fatalf("requeued = %v, want none (within grace window)", requeued)
	}
	got, _ := st.GetTask(ctx, id)
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING (grace window)", got.State)
	}
}

// Reconciliation is scoped to the heartbeating worker: a different worker's
// RUNNING tasks must not be touched, even if absent from this worker's report.
func TestReconcileWorkerTasks_IgnoresOtherWorkers(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	other := runningTask(t, st, sub, "task_other", "wrk_other", 10*time.Minute)

	requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_alive", nil, time.Minute)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(requeued) != 0 {
		t.Fatalf("requeued = %v, want none (other worker's task)", requeued)
	}
	got, _ := st.GetTask(ctx, other)
	if got.State != model.TaskStateRunning {
		t.Errorf("other worker task state = %s, want RUNNING", got.State)
	}
}

// CancelledTasksForWorker returns those candidate tasks whose owning submission is
// CANCELLED — the set the worker should kill (#113).
func TestCancelledTasksForWorker(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	cancelledTask := runningTask(t, st, sub, "task_cancelled", "wrk_alive", time.Minute)

	// A second submission that stays running, with its own task.
	liveSub := sampleSubmission(sub.WorkflowID)
	liveSub.ID = "sub_live"
	liveSub.State = model.SubmissionStateRunning
	if err := st.CreateSubmission(ctx, liveSub); err != nil {
		t.Fatalf("create live submission: %v", err)
	}
	liveTask := runningTask(t, st, liveSub, "task_live", "wrk_alive", time.Minute)

	// Cancel the first submission.
	sub.State = model.SubmissionStateCancelled
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("cancel submission: %v", err)
	}

	got, err := st.CancelledTasksForWorker(ctx, []string{cancelledTask, liveTask})
	if err != nil {
		t.Fatalf("cancelled tasks: %v", err)
	}
	if len(got) != 1 || got[0] != cancelledTask {
		t.Fatalf("cancelled = %v, want [%s]", got, cancelledTask)
	}
}
