package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestMigrate_Idempotent_NewColumnsUsable extends the plain "second Migrate
// doesn't error" check: it also confirms the #184 PR2 columns (dispatched_at,
// stage_in_ms/stage_out_ms, the four submission staging stamps) round-trip a
// write/read after the schema has been migrated twice.
func TestMigrate_Idempotent_NewColumnsUsable(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	prestageStart := sub.CreatedAt.Add(time.Second)
	sub.PrestageStartedAt = &prestageStart
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	task := sampleTask(sub.ID)
	dispatchedAt := task.CreatedAt.Add(time.Millisecond)
	stageIn := int64(1500)
	task.DispatchedAt = &dispatchedAt
	task.StageInMs = &stageIn
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.PrestageStartedAt == nil || !gotSub.PrestageStartedAt.Equal(prestageStart) {
		t.Errorf("prestage_started_at = %v, want %v", gotSub.PrestageStartedAt, prestageStart)
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.DispatchedAt == nil || !gotTask.DispatchedAt.Equal(dispatchedAt) {
		t.Errorf("dispatched_at = %v, want %v", gotTask.DispatchedAt, dispatchedAt)
	}
	if gotTask.StageInMs == nil || *gotTask.StageInMs != stageIn {
		t.Errorf("stage_in_ms = %v, want %d", gotTask.StageInMs, stageIn)
	}
	if gotTask.StageOutMs != nil {
		t.Errorf("stage_out_ms = %v, want nil", gotTask.StageOutMs)
	}
}

// TestCheckoutTask_StartedAtStampedDispatchedAtSurvives verifies that
// CheckoutTask's narrow UPDATE (state, external_id, started_at only) leaves a
// pre-existing dispatched_at value untouched — the column is written once at
// submit time (by the scheduler, simulated here at CreateTask) and CheckoutTask
// never overwrites it.
func TestCheckoutTask_StartedAtStampedDispatchedAtSurvives(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	dispatchedAt := task.CreatedAt.Add(50 * time.Millisecond)
	task.DispatchedAt = &dispatchedAt
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	w := sampleWorker()
	st.CreateWorker(ctx, w)

	before := time.Now().UTC()
	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got == nil {
		t.Fatal("expected a task, got nil")
	}
	if got.StartedAt == nil {
		t.Fatal("expected started_at to be stamped by checkout")
	}
	if got.StartedAt.Before(before) {
		t.Errorf("started_at = %v, want >= %v (fresh checkout stamp)", got.StartedAt, before)
	}
	if got.DispatchedAt == nil || !got.DispatchedAt.Equal(dispatchedAt) {
		t.Errorf("dispatched_at = %v, want unchanged %v", got.DispatchedAt, dispatchedAt)
	}

	// Re-read from the store (not just the in-memory return value) to confirm
	// the persisted row, not just CheckoutTask's constructed struct, carries
	// both stamps.
	reread, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if reread.StartedAt == nil || reread.DispatchedAt == nil {
		t.Fatalf("persisted row missing stamps: started=%v dispatched=%v", reread.StartedAt, reread.DispatchedAt)
	}
	if !reread.DispatchedAt.Equal(dispatchedAt) {
		t.Errorf("persisted dispatched_at = %v, want %v", reread.DispatchedAt, dispatchedAt)
	}
}

// TestResetFailedTasks_ClearsStartedAt is the M1 regression test: without
// clearing started_at, a retried bvbrc task's MarkTaskRunning
// (COALESCE(started_at, ?)) would silently keep the PRIOR attempt's stamp
// instead of recording the new one.
func TestResetFailedTasks_ClearsStartedAt(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	task := sampleTask(sub.ID)
	task.State = model.TaskStateFailed
	task.ExecutorType = model.ExecutorTypeBVBRC
	staleStarted := task.CreatedAt.Add(time.Minute)
	staleCompleted := task.CreatedAt.Add(2 * time.Minute)
	staleStageIn := int64(1500)
	staleStageOut := int64(750)
	task.StartedAt = &staleStarted
	task.CompletedAt = &staleCompleted
	task.StageInMs = &staleStageIn
	task.StageOutMs = &staleStageOut
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	n, err := st.ResetFailedTasks(ctx, sub.ID)
	if err != nil {
		t.Fatalf("reset failed tasks: %v", err)
	}
	if n != 1 {
		t.Fatalf("reset count = %d, want 1", n)
	}

	afterReset, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if afterReset.StartedAt != nil {
		t.Fatalf("started_at = %v, want nil after reset", afterReset.StartedAt)
	}
	if afterReset.State != model.TaskStatePending {
		t.Fatalf("state = %q, want PENDING", afterReset.State)
	}
	// #190-review fix 6: a new attempt must not show the prior attempt's
	// stage times.
	if afterReset.StageInMs != nil {
		t.Errorf("stage_in_ms = %v, want nil after reset", afterReset.StageInMs)
	}
	if afterReset.StageOutMs != nil {
		t.Errorf("stage_out_ms = %v, want nil after reset", afterReset.StageOutMs)
	}

	// Simulate the scheduler re-dispatching (PENDING -> QUEUED) and the bvbrc
	// platform later reporting RUNNING for the new attempt.
	afterReset.State = model.TaskStateQueued
	if err := st.UpdateTask(ctx, afterReset); err != nil {
		t.Fatalf("update task to queued: %v", err)
	}

	freshRunStart := time.Now().UTC()
	applied, err := st.MarkTaskRunning(ctx, task.ID)
	if err != nil {
		t.Fatalf("mark task running: %v", err)
	}
	if !applied {
		t.Fatal("expected MarkTaskRunning to apply")
	}

	final, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if final.StartedAt == nil {
		t.Fatal("expected started_at to be stamped by MarkTaskRunning")
	}
	// The regression this guards against: COALESCE would have kept
	// staleStarted (the FIRST attempt's stamp) had ResetFailedTasks not
	// cleared it. Assert the new stamp is not the stale one, and is at least
	// as recent as the retry.
	if final.StartedAt.Equal(staleStarted) {
		t.Fatalf("started_at = %v, want a fresh stamp (not the prior attempt's %v)", final.StartedAt, staleStarted)
	}
	if final.StartedAt.Before(freshRunStart) {
		t.Errorf("started_at = %v, want >= %v", final.StartedAt, freshRunStart)
	}
}

// TestExecSubmissionUpdate_StagingStampsSurviveFinalize confirms the four
// staging timestamps are included in execSubmissionUpdate's SET clause: a
// submission carrying prestage/poststage stamps that later goes through
// FinalizeSubmission (the full-row CAS write used when a submission
// completes) must not have them silently NULLed.
func TestExecSubmissionUpdate_StagingStampsSurviveFinalize(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	prestageStart := sub.CreatedAt.Add(time.Second)
	prestageDone := sub.CreatedAt.Add(2 * time.Second)
	sub.PrestageStartedAt = &prestageStart
	sub.PrestageCompletedAt = &prestageDone
	if applied, err := st.UpdateSubmissionIfState(ctx, sub, model.SubmissionStatePending, ""); err != nil || !applied {
		t.Fatalf("stamp prestage: applied=%v err=%v", applied, err)
	}

	// Finalize (as the scheduler does on completion): this must carry the
	// staging stamps forward from the in-memory sub, not drop them.
	sub.State = model.SubmissionStateCompleted
	now := time.Now().UTC()
	sub.CompletedAt = &now
	poststageStart := now.Add(time.Second)
	poststageDone := now.Add(2 * time.Second)
	sub.PoststageStartedAt = &poststageStart
	sub.PoststageCompletedAt = &poststageDone
	applied, err := st.FinalizeSubmission(ctx, sub)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !applied {
		t.Fatal("expected finalize to apply")
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.PrestageStartedAt == nil || !got.PrestageStartedAt.Equal(prestageStart) {
		t.Errorf("prestage_started_at = %v, want %v", got.PrestageStartedAt, prestageStart)
	}
	if got.PrestageCompletedAt == nil || !got.PrestageCompletedAt.Equal(prestageDone) {
		t.Errorf("prestage_completed_at = %v, want %v", got.PrestageCompletedAt, prestageDone)
	}
	if got.PoststageStartedAt == nil || !got.PoststageStartedAt.Equal(poststageStart) {
		t.Errorf("poststage_started_at = %v, want %v", got.PoststageStartedAt, poststageStart)
	}
	if got.PoststageCompletedAt == nil || !got.PoststageCompletedAt.Equal(poststageDone) {
		t.Errorf("poststage_completed_at = %v, want %v", got.PoststageCompletedAt, poststageDone)
	}
}

// TestExecTaskUpdate_StageMsSurvivesUpdate confirms stage_in_ms/stage_out_ms
// are included in execTaskUpdate's SET clause, e.g. the worker-clear path
// that runs a plain UpdateTask after a terminal report.
func TestExecTaskUpdate_StageMsSurvivesUpdate(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	task := sampleTask(sub.ID)
	task.ExecutorType = model.ExecutorTypeWorker
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	stageIn := int64(2200)
	stageOut := int64(400)
	task.State = model.TaskStateSuccess
	task.StageInMs = &stageIn
	task.StageOutMs = &stageOut
	completed := time.Now().UTC()
	task.CompletedAt = &completed
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	got, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.StageInMs == nil || *got.StageInMs != stageIn {
		t.Errorf("stage_in_ms = %v, want %d", got.StageInMs, stageIn)
	}
	if got.StageOutMs == nil || *got.StageOutMs != stageOut {
		t.Errorf("stage_out_ms = %v, want %d", got.StageOutMs, stageOut)
	}
}

// TestUpdateSubmissionIfState_RejectsWhenStateAlreadyMoved is the killing
// test for the prestage CAS in internal/scheduler/workspace.go: a plain
// UpdateSubmission call has no state guard and always writes, so a mutant
// that swaps UpdateSubmissionIfState for UpdateSubmission stays green
// against every other test in this suite. This test targets the guard
// directly — call UpdateSubmissionIfState expecting PENDING against a row
// that is actually CANCELLED and assert both that the call reports
// applied=false AND that the row is left completely untouched (the field the
// caller was trying to change must NOT have moved).
func TestUpdateSubmissionIfState_RejectsWhenStateAlreadyMoved(t *testing.T) {
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

	// Move the row to CANCELLED out from under the in-memory sub, the way a
	// concurrent HTTP cancel would.
	cancelled, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	cancelled.State = model.SubmissionStateCancelled
	now := time.Now().UTC()
	cancelled.CompletedAt = &now
	if applied, err := st.FinalizeSubmission(ctx, cancelled); err != nil || !applied {
		t.Fatalf("finalize (cancel): applied=%v err=%v", applied, err)
	}

	// Attempt the prestage completed-stamp write against the stale
	// (still-PENDING) in-memory snapshot — this is exactly what
	// prestageWorkspaceInputs does when a cancel lands mid-stage.
	prestageCompleted := now.Add(time.Second)
	sub.PrestageCompletedAt = &prestageCompleted
	applied, err := st.UpdateSubmissionIfState(ctx, sub, model.SubmissionStatePending, sub.OutputState)
	if err != nil {
		t.Fatalf("update if state: %v", err)
	}
	if applied {
		t.Fatal("expected applied=false: submission is no longer PENDING")
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.State != model.SubmissionStateCancelled {
		t.Fatalf("state = %q, want CANCELLED (must not be resurrected)", got.State)
	}
	if got.PrestageCompletedAt != nil {
		t.Fatalf("prestage_completed_at = %v, want nil (rejected write must not land)", got.PrestageCompletedAt)
	}
}
