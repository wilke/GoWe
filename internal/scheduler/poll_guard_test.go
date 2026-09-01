package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// stubExecutor lets tests script Status observations (and side effects that
// simulate concurrent writes landing between the poll's snapshot and its
// status read — the F-J race window).
type stubExecutor struct {
	typ    model.ExecutorType
	status func(ctx context.Context, task *model.Task) (model.TaskState, error)
	calls  int
}

func (s *stubExecutor) Type() model.ExecutorType { return s.typ }
func (s *stubExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	return task.ID, nil
}
func (s *stubExecutor) Status(ctx context.Context, task *model.Task) (model.TaskState, error) {
	s.calls++
	return s.status(ctx, task)
}
func (s *stubExecutor) Cancel(ctx context.Context, task *model.Task) error { return nil }
func (s *stubExecutor) Logs(ctx context.Context, task *model.Task) (string, string, error) {
	return "", "", nil
}

// pollSetup is testSetup plus access to the registry for stub executors.
func pollSetup(t *testing.T) (*Loop, store.Store, *executor.Registry) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := executor.NewRegistry(logger)
	return NewLoop(st, reg, DefaultConfig(), logger), st, reg
}

// seedPollTask creates a workflow, submission and a single task in the given
// state/executor, returning the submission ID (task ID is "task_poll").
func seedPollTask(t *testing.T, st store.Store, state model.TaskState, executorType model.ExecutorType, externalID string) string {
	t.Helper()
	ctx := context.Background()
	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)

	task := &model.Task{
		ID:           "task_poll",
		SubmissionID: subID,
		StepID:       "s1",
		State:        state,
		ExecutorType: executorType,
		ExternalID:   externalID,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		CreatedAt:    time.Now().UTC(),
	}
	if state == model.TaskStateRunning {
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		task.StartedAt = &started
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return subID
}

// TestPollInFlight_WorkerCheckoutNotClobbered is the F-J zombie regression:
// a checkout committing between the poll's QUEUED snapshot and its status
// read must not have its external_id/started_at overwritten by the poll —
// otherwise Requeue/ReconcileWorkerTasks (which match on external_id) can
// never see the task again. QUEUED worker tasks are skipped by the poll
// entirely (their state changes only via server handlers).
func TestPollInFlight_WorkerCheckoutNotClobbered(t *testing.T) {
	l, st, reg := pollSetup(t)
	ctx := context.Background()

	seedPollTask(t, st, model.TaskStateQueued, model.ExecutorTypeWorker, "task_poll")

	now := time.Now().UTC()
	worker := &model.Worker{
		ID: "wrk_1", Name: "w1", Hostname: "h1", Group: "default",
		State: model.WorkerStateOnline, Runtime: model.ContainerRuntime("none"),
		LastSeen: now, RegisteredAt: now,
	}
	if err := st.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// The stub reproduces the race deterministically: if the poll ever asks
	// for this QUEUED task's status, the checkout commits first (exactly the
	// old interleaving), and the poll's stale full-row write would then reset
	// external_id — the zombie.
	stub := &stubExecutor{
		typ: model.ExecutorTypeWorker,
		status: func(ctx context.Context, task *model.Task) (model.TaskState, error) {
			if task.State == model.TaskStateQueued {
				if _, err := st.CheckoutTask(ctx, "wrk_1", "default", model.ContainerRuntime("none")); err != nil {
					t.Errorf("checkout inside status: %v", err)
				}
			}
			return model.TaskStateRunning, nil
		},
	}
	reg.Register(stub)

	if err := l.pollInFlight(ctx, map[string]bool{}); err != nil {
		t.Fatalf("pollInFlight: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("Status called %d times for a QUEUED worker task, want 0 (belt: skipped from the QUEUED scan)", stub.calls)
	}
	got, err := st.GetTask(ctx, "task_poll")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateQueued || got.ExternalID != "task_poll" {
		t.Fatalf("after poll: state=%s external_id=%s, want untouched QUEUED/task_poll", got.State, got.ExternalID)
	}

	// Now the checkout lands (RUNNING, external_id=wrk_1, started_at stamped).
	checked, err := st.CheckoutTask(ctx, "wrk_1", "default", model.ContainerRuntime("none"))
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if checked == nil {
		t.Fatal("checkout returned nil — the poll clobbered the QUEUED task")
	}
	started := *checked.StartedAt

	// A second poll (RUNNING scan now sees the task) must leave the checkout's
	// bookkeeping intact.
	if err := l.pollInFlight(ctx, map[string]bool{}); err != nil {
		t.Fatalf("pollInFlight: %v", err)
	}
	got, err = st.GetTask(ctx, "task_poll")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING", got.State)
	}
	if got.ExternalID != "wrk_1" {
		t.Errorf("external_id = %q, want wrk_1 (clobbered by poll)", got.ExternalID)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v (unchanged)", got.StartedAt, started)
	}

	// The reconcile path must still attribute the task to the worker.
	orphans, err := st.ReconcileWorkerTasks(ctx, "wrk_1", []string{}, 0)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(orphans) != 1 || orphans[0] != "task_poll" {
		t.Errorf("reconcile matched %v, want [task_poll] — the task is invisible to reconciliation", orphans)
	}
}

// TestPollInFlight_QueuedToRunning_MarkTaskRunning: a bvbrc task observed
// RUNNING is persisted via the narrow CAS (state + started_at only), leaving
// the platform job id in external_id untouched.
func TestPollInFlight_QueuedToRunning_MarkTaskRunning(t *testing.T) {
	l, st, reg := pollSetup(t)
	ctx := context.Background()

	subID := seedPollTask(t, st, model.TaskStateQueued, model.ExecutorTypeBVBRC, "bvbrc_job_42")
	reg.Register(&stubExecutor{
		typ: model.ExecutorTypeBVBRC,
		status: func(ctx context.Context, task *model.Task) (model.TaskState, error) {
			return model.TaskStateRunning, nil
		},
	})

	affected := map[string]bool{}
	if err := l.pollInFlight(ctx, affected); err != nil {
		t.Fatalf("pollInFlight: %v", err)
	}

	got, err := st.GetTask(ctx, "task_poll")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING", got.State)
	}
	if got.StartedAt == nil {
		t.Error("expected started_at to be stamped")
	}
	if got.ExternalID != "bvbrc_job_42" {
		t.Errorf("external_id = %q, want bvbrc_job_42", got.ExternalID)
	}
	if !affected[subID] {
		t.Error("expected submission to be marked affected")
	}
}

// TestPollInFlight_CancelSkipNotResurrected: a cancel that SKIPs the task
// between the poll's snapshot and its status read must win — the poll's
// RUNNING observation lands on a no-longer-QUEUED task and is dropped.
func TestPollInFlight_CancelSkipNotResurrected(t *testing.T) {
	l, st, reg := pollSetup(t)
	ctx := context.Background()

	subID := seedPollTask(t, st, model.TaskStateQueued, model.ExecutorTypeBVBRC, "bvbrc_job_42")
	reg.Register(&stubExecutor{
		typ: model.ExecutorTypeBVBRC,
		status: func(ctx context.Context, task *model.Task) (model.TaskState, error) {
			// Concurrent cancel fan-out lands mid-poll.
			if _, err := st.CancelNonTerminalTasks(ctx, subID, time.Now().UTC()); err != nil {
				t.Errorf("cancel inside status: %v", err)
			}
			return model.TaskStateRunning, nil
		},
	})

	if err := l.pollInFlight(ctx, map[string]bool{}); err != nil {
		t.Fatalf("pollInFlight: %v", err)
	}

	got, err := st.GetTask(ctx, "task_poll")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateSkipped {
		t.Errorf("state = %s, want SKIPPED (cancel must not be resurrected)", got.State)
	}
}

// TestPollInFlight_TransientRegressionNotPersisted: a transient bvbrc
// RUNNING→QUEUED observation is deliberately not persisted (N1) — the row
// keeps its RUNNING state and bookkeeping until the next observation.
func TestPollInFlight_TransientRegressionNotPersisted(t *testing.T) {
	l, st, reg := pollSetup(t)
	ctx := context.Background()

	seedPollTask(t, st, model.TaskStateRunning, model.ExecutorTypeBVBRC, "bvbrc_job_42")
	reg.Register(&stubExecutor{
		typ: model.ExecutorTypeBVBRC,
		status: func(ctx context.Context, task *model.Task) (model.TaskState, error) {
			return model.TaskStateQueued, nil
		},
	})

	if err := l.pollInFlight(ctx, map[string]bool{}); err != nil {
		t.Fatalf("pollInFlight: %v", err)
	}

	got, err := st.GetTask(ctx, "task_poll")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING (regression must not be persisted)", got.State)
	}
	if got.StartedAt == nil {
		t.Error("started_at was cleared by the poll")
	}
	if got.ExternalID != "bvbrc_job_42" {
		t.Errorf("external_id = %q, want bvbrc_job_42", got.ExternalID)
	}
}
