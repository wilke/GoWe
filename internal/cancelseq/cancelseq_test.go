package cancelseq

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// newTestStore returns a fresh, migrated in-memory store and a logger that
// discards output, for direct (non-HTTP) exercise of this package's
// functions.
func newTestStore(t *testing.T) store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// seedRunningSubmission creates a workflow + a RUNNING submission with one
// non-terminal step instance and one non-terminal (RUNNING) task, and
// returns the submission.
func seedRunningSubmission(t *testing.T, st store.Store, subID string) *model.Submission {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	wfID := "wf_" + subID
	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: wfID, Name: wfID, Class: "Workflow",
		Steps:     []model.Step{{ID: "step1"}},
		CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := &model.Submission{
		ID: subID, WorkflowID: wfID, WorkflowName: wfID,
		State: model.SubmissionStateRunning, Inputs: map[string]any{},
		SubmittedBy: model.AnonymousUser.Username,
		CreatedAt:   base,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	if err := st.CreateStepInstance(ctx, &model.StepInstance{
		ID: subID + "_si1", SubmissionID: subID, StepID: "step1",
		State: model.StepStateRunning, Outputs: map[string]any{},
		CreatedAt: base,
	}); err != nil {
		t.Fatalf("create step instance: %v", err)
	}

	if err := st.CreateTask(ctx, &model.Task{
		ID: subID + "_task1", SubmissionID: subID, StepID: "step1",
		StepInstanceID: subID + "_si1",
		State:          model.TaskStateRunning, ExecutorType: model.ExecutorTypeLocal,
		ScatterIndex: -1, Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{},
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	return sub
}

// TestRun_CancelsStepsAndTasksAndReturnsCounts is a direct unit test of Run
// (no HTTP handler in the loop): it seeds one non-terminal step instance and
// one non-terminal task on a RUNNING submission, cancels it, and checks both
// the returned Result counts and the persisted rows.
func TestRun_CancelsStepsAndTasksAndReturnsCounts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	logger := testLogger()
	reg := metrics.NewRegistry(metrics.Config{})

	sub := seedRunningSubmission(t, st, "sub_run")

	now := time.Now().UTC()
	sub.State = model.SubmissionStateCancelled
	sub.CompletedAt = &now

	result, err := Run(ctx, st, reg, logger, sub, now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StepsCancelled != 1 {
		t.Errorf("StepsCancelled = %d, want 1", result.StepsCancelled)
	}
	if result.TasksCancelled != 1 {
		t.Errorf("TasksCancelled = %d, want 1", result.TasksCancelled)
	}
	if result.ChildrenCancelled != 0 {
		t.Errorf("ChildrenCancelled = %d, want 0 (no sub-workflow proxies)", result.ChildrenCancelled)
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateCancelled {
		t.Errorf("submission state = %s, want CANCELLED", gotSub.State)
	}

	gotTask, err := st.GetTask(ctx, "sub_run_task1")
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateSkipped {
		t.Errorf("task state = %s, want SKIPPED", gotTask.State)
	}
}

// TestCancelDescendants_ChildlessProxyLeftRunning: a sub-workflow proxy task
// with no child submission yet (the scheduler may create one at any moment)
// must be left RUNNING rather than retired — retiring it here would disarm
// pollSubworkflowTask's reconciliation, the only path that would otherwise
// cancel a child created after this walk.
func TestCancelDescendants_ChildlessProxyLeftRunning(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	logger := testLogger()
	reg := metrics.NewRegistry(metrics.Config{})

	sub := seedRunningSubmission(t, st, "sub_proxy")

	proxy := &model.Task{
		ID: "sub_proxy_proxy1", SubmissionID: sub.ID, StepID: "subwf",
		State: model.TaskStateRunning, ExecutorType: model.ExecutorTypeSubworkflow,
		ScatterIndex: -1, Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{},
	}
	if err := st.CreateTask(ctx, proxy); err != nil {
		t.Fatalf("create proxy task: %v", err)
	}

	now := time.Now().UTC()
	n := CancelDescendants(ctx, st, reg, logger, sub.ID, now, map[string]bool{})
	if n != 0 {
		t.Errorf("CancelDescendants returned %d, want 0 (no child submissions exist)", n)
	}

	gotProxy, err := st.GetTask(ctx, proxy.ID)
	if err != nil || gotProxy == nil {
		t.Fatalf("get proxy task: %v", err)
	}
	if gotProxy.State != model.TaskStateRunning {
		t.Errorf("childless proxy state = %s, want RUNNING (left alone, not retired)", gotProxy.State)
	}
}
