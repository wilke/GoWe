package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// testSetup creates an in-memory store, registers a LocalExecutor, and returns
// a ready-to-use scheduler Loop.
func testSetup(t *testing.T) (*Loop, store.Store) {
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
	reg.Register(executor.NewLocalExecutor(t.TempDir(), logger))

	return NewLoop(st, reg, DefaultConfig(), logger), st
}

// createPipeline creates a workflow, a PENDING submission, and one WAITING
// StepInstance per step. The scheduler will create Tasks when dispatching.
// maxRetries is stored in the scheduler config for this test.
// It returns (workflowID, submissionID).
func createPipeline(t *testing.T, st store.Store, steps []model.Step, inputs map[string]any, maxRetries int) (string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_" + uuid.New().String()
	subID := "sub_" + uuid.New().String()

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "test-workflow",
		CWLVersion: "v1.2",
		Steps:      steps,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	// Create StepInstances (the scheduler creates Tasks from these).
	for _, step := range steps {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: subID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", step.ID, err)
		}
	}

	return wfID, subID
}

// getStepInstancesByStep returns a map of stepID -> StepInstance for a submission.
func getStepInstancesByStep(t *testing.T, st store.Store, subID string) map[string]*model.StepInstance {
	t.Helper()
	steps, err := st.ListStepsBySubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("ListStepsBySubmission: %v", err)
	}
	m := make(map[string]*model.StepInstance, len(steps))
	for _, si := range steps {
		m[si.StepID] = si
	}
	return m
}

// TestTick_SingleStepNoDeps verifies that a single step with no dependencies
// completes in a single tick via the 3-level model:
// StepInstance WAITING -> READY -> DISPATCHED (Task created) -> COMPLETED
// Submission PENDING -> COMPLETED.
func TestTick_SingleStepNoDeps(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "echo_step",
			ToolInline: &model.Tool{
				ID:          "echo_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Verify task was created and completed by scheduler.
	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	if task.State != model.TaskStateSuccess {
		t.Errorf("task.State = %q, want %q", task.State, model.TaskStateSuccess)
	}
	if !strings.Contains(task.Stdout, "hello") {
		t.Errorf("task.Stdout = %q, want it to contain \"hello\"", task.Stdout)
	}
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Errorf("task.ExitCode = %v, want 0", task.ExitCode)
	}
	if task.StepInstanceID == "" {
		t.Error("task.StepInstanceID should be set")
	}

	// Verify submission state.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Errorf("sub.State = %q, want %q", sub.State, model.SubmissionStateCompleted)
	}
	if sub.CompletedAt == nil {
		t.Error("sub.CompletedAt should be set")
	}
}

// TestTick_TwoStepPipeline verifies a two-step pipeline where step2 depends
// on step1. With the 3-level model:
//   - Tick 1: step1 WAITING -> READY -> DISPATCHED (Task created, SUCCESS).
//     step2 still WAITING (dep not met at phase start). Submission RUNNING.
//   - Tick 2: step2 WAITING -> READY -> DISPATCHED (Task created, SUCCESS).
//     All steps terminal -> submission COMPLETED.
func TestTick_TwoStepPipeline(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "echo1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
		},
		{
			ID:        "step2",
			DependsOn: []string{"step1"},
			ToolInline: &model.Tool{
				ID:          "echo2",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "world"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Tick 1: step1 dispatched and completed. step2 still waiting.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 1: %v", err)
	}
	// Only step1 should have a task (step2 not yet dispatched).
	if len(tasks) != 1 {
		t.Fatalf("after tick 1: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].StepID != "step1" || tasks[0].State != model.TaskStateSuccess {
		t.Errorf("after tick 1: step1 task state = %q, want SUCCESS", tasks[0].State)
	}

	// Verify step instances.
	siByStep := getStepInstancesByStep(t, st, subID)
	if siByStep["step1"].State != model.StepStateCompleted {
		t.Errorf("after tick 1: step1 SI state = %q, want COMPLETED", siByStep["step1"].State)
	}
	if siByStep["step2"].State != model.StepStateWaiting {
		t.Errorf("after tick 1: step2 SI state = %q, want WAITING", siByStep["step2"].State)
	}

	// Tick 2: step2 dispatched and completed. Submission finalized.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	tasks, err = st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 2: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("after tick 2: expected 2 tasks, got %d", len(tasks))
	}
	taskByStep := make(map[string]*model.Task)
	for _, tk := range tasks {
		taskByStep[tk.StepID] = tk
	}
	if taskByStep["step2"].State != model.TaskStateSuccess {
		t.Errorf("after tick 2: step2 task state = %q, want SUCCESS", taskByStep["step2"].State)
	}
	if !strings.Contains(taskByStep["step2"].Stdout, "world") {
		t.Errorf("step2.Stdout = %q, want it to contain \"world\"", taskByStep["step2"].Stdout)
	}

	// Submission should be COMPLETED.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 2: %v", err)
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Errorf("after tick 2: sub.State = %q, want COMPLETED", sub.State)
	}
	if sub.CompletedAt == nil {
		t.Error("sub.CompletedAt should be set after COMPLETED")
	}
}

// TestTick_FailedDep_SkipsDownstream verifies that when a dependency fails
// (with no retries), the downstream step is SKIPPED and the submission is FAILED.
// With the 3-level model, step2 never gets a Task — its StepInstance goes
// directly from WAITING to SKIPPED.
func TestTick_FailedDep_SkipsDownstream(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "fail_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"false"},
			},
		},
		{
			ID:        "step2",
			DependsOn: []string{"step1"},
			ToolInline: &model.Tool{
				ID:          "echo_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "never"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Tick 1: step1 dispatched -> FAILED. step2 still WAITING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 1: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("after tick 1: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].State != model.TaskStateFailed {
		t.Errorf("after tick 1: step1 task state = %q, want FAILED", tasks[0].State)
	}
	if tasks[0].ExitCode == nil || *tasks[0].ExitCode != 1 {
		t.Errorf("after tick 1: step1.ExitCode = %v, want 1", tasks[0].ExitCode)
	}

	// Tick 2: step2 dep failed -> StepInstance SKIPPED. Submission FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	siByStep := getStepInstancesByStep(t, st, subID)
	if siByStep["step1"].State != model.StepStateFailed {
		t.Errorf("after tick 2: step1 SI state = %q, want FAILED", siByStep["step1"].State)
	}
	if siByStep["step2"].State != model.StepStateSkipped {
		t.Errorf("after tick 2: step2 SI state = %q, want SKIPPED", siByStep["step2"].State)
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 2: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("after tick 2: sub.State = %q, want FAILED", sub.State)
	}
}

// TestTick_SubmissionTransitions tracks the submission state through ticks for
// a single-step pipeline: PENDING -> COMPLETED.
func TestTick_SubmissionTransitions(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "echo1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "one"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Initial state: PENDING.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStatePending {
		t.Fatalf("initial sub.State = %q, want PENDING", sub.State)
	}

	// Tick 1: step dispatched and completed -> submission COMPLETED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	sub, err = st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 1: %v", err)
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Errorf("after tick 1: sub.State = %q, want COMPLETED", sub.State)
	}
	if sub.CompletedAt == nil {
		t.Error("sub.CompletedAt should be set")
	}
}

// TestTick_SubmissionTransitions_WithFailure tracks the submission state
// through a failure scenario: PENDING -> RUNNING -> FAILED.
func TestTick_SubmissionTransitions_WithFailure(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "fail_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"false"},
			},
		},
		{
			ID:        "step2",
			DependsOn: []string{"step1"},
			ToolInline: &model.Tool{
				ID:          "echo_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "never"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Tick 1: step1 dispatched -> FAILED. step2 still WAITING.
	// finalizeSubmissions: step1 FAILED, step2 WAITING -> RUNNING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 1: %v", err)
	}
	if sub.State != model.SubmissionStateRunning {
		t.Errorf("after tick 1: sub.State = %q, want RUNNING", sub.State)
	}

	// Tick 2: step2 dep failed -> SKIPPED. All terminal -> FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	sub, err = st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 2: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("after tick 2: sub.State = %q, want FAILED", sub.State)
	}
	if sub.CompletedAt == nil {
		t.Error("sub.CompletedAt should be set")
	}
}

// TestTick_RetryOnFailure verifies the retry mechanism: a failing task with
// MaxRetries=2 is retried twice before remaining permanently FAILED.
//
// Timeline:
//   - Tick 1: StepInstance WAITING -> READY -> DISPATCHED (Task created, FAILED).
//     markRetries: RetryCount(0) < MaxRetries(2) -> RETRYING.
//   - Tick 2: resubmitRetrying -> RetryCount=1 -> FAILED. markRetries: 1 < 2 -> RETRYING.
//   - Tick 3: resubmitRetrying -> RetryCount=2 -> FAILED. markRetries: 2 >= 2 -> stays FAILED.
func TestTick_RetryOnFailure(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 2
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "fail_step",
			ToolInline: &model.Tool{
				ID:          "fail_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"false"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 2)

	// Tick 1: Task created and fails. markRetries: 0 < 2 -> RETRYING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasksBySubmission after tick 1: err=%v, count=%d", err, len(tasks))
	}
	taskID := tasks[0].ID
	task, _ := st.GetTask(ctx, taskID)
	if task.State != model.TaskStateRetrying {
		t.Errorf("after tick 1: state = %q, want RETRYING", task.State)
	}
	if task.RetryCount != 0 {
		t.Errorf("after tick 1: RetryCount = %d, want 0", task.RetryCount)
	}

	// Simulate the failed attempt having reported staging times (as a worker
	// task would): resubmitRetrying (#190-review fix 6) must clear these
	// before the next attempt, so a retry doesn't show the prior attempt's
	// stage durations.
	staleStageIn := int64(1200)
	staleStageOut := int64(300)
	task.StageInMs = &staleStageIn
	task.StageOutMs = &staleStageOut
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("stamp stale stage times: %v", err)
	}

	// Tick 2: resubmit RETRYING -> RetryCount=1 -> FAILED -> RETRYING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	task, _ = st.GetTask(ctx, taskID)
	if task.State != model.TaskStateRetrying {
		t.Errorf("after tick 2: state = %q, want RETRYING", task.State)
	}
	if task.RetryCount != 1 {
		t.Errorf("after tick 2: RetryCount = %d, want 1", task.RetryCount)
	}
	if task.StageInMs != nil {
		t.Errorf("after tick 2: StageInMs = %v, want nil (resubmit must clear prior attempt's stage times)", task.StageInMs)
	}
	if task.StageOutMs != nil {
		t.Errorf("after tick 2: StageOutMs = %v, want nil (resubmit must clear prior attempt's stage times)", task.StageOutMs)
	}

	// Tick 3: resubmit RETRYING -> RetryCount=2 -> FAILED. 2 >= 2 -> stays FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	task, _ = st.GetTask(ctx, taskID)
	if task.State != model.TaskStateFailed {
		t.Errorf("after tick 3: state = %q, want FAILED", task.State)
	}
	if task.RetryCount != 2 {
		t.Errorf("after tick 3: RetryCount = %d, want 2", task.RetryCount)
	}

	// Submission should be FAILED after exhausting retries.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("sub.State = %q, want FAILED", sub.State)
	}
}

// TestTick_EmptyTick verifies that calling Tick with no tasks in the system
// completes without error.
func TestTick_EmptyTick(t *testing.T) {
	sched, _ := testSetup(t)
	ctx := context.Background()

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick with empty DB: %v", err)
	}
}

// TestListSubmissionsByState_IncludesChildren guards the scheduler's view of
// the submission table: child submissions (spawned by sub-workflow proxy
// tasks, ParentTaskID set) are hidden from user-facing listings via
// ListOptions.ExcludeChildren, but the scheduler must keep seeing them —
// they need scheduling like any other submission. If someone "helpfully"
// sets ExcludeChildren in listSubmissionsByState, children would never run.
func TestListSubmissionsByState_IncludesChildren(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	now := time.Now().UTC()

	wf := &model.Workflow{
		ID:         "wf_" + uuid.New().String(),
		Name:       "test-workflow",
		CWLVersion: "v1.2",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	parent := &model.Submission{
		ID:         "sub_parent",
		WorkflowID: wf.ID,
		State:      model.SubmissionStatePending,
		Inputs:     map[string]any{},
		CreatedAt:  now,
	}
	child := &model.Submission{
		ID:           "sub_child",
		WorkflowID:   wf.ID,
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		ParentTaskID: "task_proxy-1",
		CreatedAt:    now,
	}
	for _, sub := range []*model.Submission{parent, child} {
		if err := st.CreateSubmission(ctx, sub); err != nil {
			t.Fatalf("CreateSubmission %s: %v", sub.ID, err)
		}
	}

	subs, err := sched.listSubmissionsByState(ctx, "PENDING")
	if err != nil {
		t.Fatalf("listSubmissionsByState: %v", err)
	}
	got := make(map[string]bool, len(subs))
	for _, s := range subs {
		got[s.ID] = true
	}
	if !got["sub_parent"] || !got["sub_child"] {
		t.Errorf("listSubmissionsByState = %v, want both parent and child (children must remain schedulable)", got)
	}
}

// TestStart_StopsOnContextCancel verifies that Start returns when its context
// is cancelled.
func TestStart_StopsOnContextCancel(t *testing.T) {
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
	reg.Register(executor.NewLocalExecutor(t.TempDir(), logger))

	cfg := Config{PollInterval: 10 * time.Millisecond}
	sched := NewLoop(st, reg, cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- sched.Start(ctx)
	}()

	// Let the scheduler run a few ticks, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Start returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5 seconds after context cancellation")
	}
}

// waitForState polls sched.State() until it equals want or the timeout
// elapses, failing the test on timeout.
func waitForState(t *testing.T, sched *Loop, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sched.State() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("State() = %q after timeout, want %q", sched.State(), want)
}

// TestLoop_State covers the not_started -> running -> stopped lifecycle
// reported by State(), exercised via both context cancellation and an
// explicit Stop() call (issue #193: /api/v1/health must report the real
// scheduler state instead of a hardcoded "not_started").
func TestLoop_State(t *testing.T) {
	t.Run("not started", func(t *testing.T) {
		sched, _ := testSetup(t)
		if got := sched.State(); got != StateNotStarted {
			t.Errorf("State() before Start = %q, want %q", got, StateNotStarted)
		}
	})

	t.Run("running", func(t *testing.T) {
		sched, _ := testSetup(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		go func() { _ = sched.Start(ctx) }()
		waitForState(t, sched, StateRunning)
	})

	t.Run("stopped via context cancel", func(t *testing.T) {
		sched, _ := testSetup(t)
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() { done <- sched.Start(ctx) }()
		waitForState(t, sched, StateRunning)

		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Start did not return within 5 seconds after context cancellation")
		}
		if got := sched.State(); got != StateStopped {
			t.Errorf("State() after context cancel = %q, want %q", got, StateStopped)
		}
	})

	t.Run("stopped via Stop", func(t *testing.T) {
		sched, _ := testSetup(t)
		ctx := context.Background()

		go func() { _ = sched.Start(ctx) }()
		waitForState(t, sched, StateRunning)

		if err := sched.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if got := sched.State(); got != StateStopped {
			t.Errorf("State() after Stop = %q, want %q", got, StateStopped)
		}
	})
}

// --- Feature C: WorkerCapabilities / canMatchTask tests ---

func TestCanMatchTask_NoWorkers(t *testing.T) {
	caps := &WorkerCapabilities{
		Groups:   make(map[string]int),
		Datasets: make(map[string]int),
	}
	ok, reason := canMatchTask(caps, &model.RuntimeHints{DockerImage: "alpine"})
	if ok {
		t.Error("expected canMatchTask to return false with no workers")
	}
	if !strings.Contains(reason, "no online workers") {
		t.Errorf("unexpected reason: %s", reason)
	}
}

func TestCanMatchTask_NoConstraints(t *testing.T) {
	caps := &WorkerCapabilities{
		OnlineCount: 1,
		Workers:     []*model.Worker{{State: model.WorkerStateOnline, Runtime: model.RuntimeNone}},
		Groups:      map[string]int{"default": 1},
		Datasets:    make(map[string]int),
	}
	ok, _ := canMatchTask(caps, &model.RuntimeHints{})
	if !ok {
		t.Error("expected canMatchTask to return true with no constraints")
	}
}

func TestCanMatchTask_DockerImageNotChecked(t *testing.T) {
	// DockerImage is NOT a hard constraint for canMatchTask — CWL treats
	// DockerRequirement as a hint, and workers with runtime=none can run bare.
	// Container runtime matching is handled by CheckoutTask instead.
	caps := &WorkerCapabilities{
		OnlineCount: 1,
		Workers:     []*model.Worker{{State: model.WorkerStateOnline, Runtime: model.RuntimeNone}},
		Groups:      map[string]int{"default": 1},
		Datasets:    make(map[string]int),
	}
	ok, _ := canMatchTask(caps, &model.RuntimeHints{DockerImage: "alpine"})
	if !ok {
		t.Error("expected true: DockerImage should not be a hard constraint in canMatchTask")
	}
}

func TestCanMatchTask_WorkerGroup(t *testing.T) {
	caps := &WorkerCapabilities{
		OnlineCount: 2,
		Workers: []*model.Worker{
			{State: model.WorkerStateOnline, Runtime: model.RuntimeApptainer, Group: "default"},
			{State: model.WorkerStateOnline, Runtime: model.RuntimeApptainer, Group: "gpu"},
		},
		HasContainer: true,
		Groups:       map[string]int{"default": 1, "gpu": 1},
		Datasets:     make(map[string]int),
	}

	// Matching group.
	ok, _ := canMatchTask(caps, &model.RuntimeHints{DockerImage: "alpine", WorkerGroup: "gpu"})
	if !ok {
		t.Error("expected true: gpu worker exists")
	}

	// Non-matching group.
	ok, reason := canMatchTask(caps, &model.RuntimeHints{DockerImage: "alpine", WorkerGroup: "esmfold"})
	if ok {
		t.Error("expected false: no esmfold workers")
	}
	if !strings.Contains(reason, "esmfold") {
		t.Errorf("unexpected reason: %s", reason)
	}
}

func TestCanMatchTask_PrestageDatasets(t *testing.T) {
	caps := &WorkerCapabilities{
		OnlineCount: 1,
		Workers: []*model.Worker{
			{
				State:    model.WorkerStateOnline,
				Runtime:  model.RuntimeApptainer,
				Datasets: map[string]string{"boltz": "/data/boltz", "alphafold": "/data/alphafold"},
			},
		},
		HasContainer: true,
		Groups:       map[string]int{"default": 1},
		Datasets:     map[string]int{"boltz": 1, "alphafold": 1},
	}

	// Worker has the prestage dataset.
	hints := &model.RuntimeHints{
		DockerImage: "alpine",
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "boltz", Mode: "prestage"},
		},
	}
	ok, _ := canMatchTask(caps, hints)
	if !ok {
		t.Error("expected true: worker has boltz dataset")
	}

	// Worker missing a prestage dataset.
	hints = &model.RuntimeHints{
		DockerImage: "alpine",
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "chai", Mode: "prestage"},
		},
	}
	ok, reason := canMatchTask(caps, hints)
	if ok {
		t.Error("expected false: no worker with chai dataset")
	}
	if !strings.Contains(reason, "chai") {
		t.Errorf("unexpected reason: %s", reason)
	}

	// Cache-mode datasets are NOT checked (preferences, not requirements).
	hints = &model.RuntimeHints{
		DockerImage: "alpine",
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "missing_ds", Mode: "cache"},
		},
	}
	ok, _ = canMatchTask(caps, hints)
	if !ok {
		t.Error("expected true: cache-mode datasets are preferences, not requirements")
	}
}

func TestCanMatchTask_CombinedConstraints(t *testing.T) {
	// Worker in group "gpu" with boltz dataset, but NOT chai.
	caps := &WorkerCapabilities{
		OnlineCount: 1,
		Workers: []*model.Worker{
			{
				State:    model.WorkerStateOnline,
				Runtime:  model.RuntimeApptainer,
				Group:    "gpu",
				Datasets: map[string]string{"boltz": "/data/boltz"},
			},
		},
		HasContainer: true,
		Groups:       map[string]int{"gpu": 1},
		Datasets:     map[string]int{"boltz": 1},
	}

	// All constraints match.
	hints := &model.RuntimeHints{
		WorkerGroup: "gpu",
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "boltz", Mode: "prestage"},
		},
	}
	ok, _ := canMatchTask(caps, hints)
	if !ok {
		t.Error("expected true: worker satisfies all constraints")
	}

	// Group matches but dataset doesn't.
	hints = &model.RuntimeHints{
		WorkerGroup: "gpu",
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "boltz", Mode: "prestage"},
			{ID: "chai", Mode: "prestage"},
		},
	}
	ok, reason := canMatchTask(caps, hints)
	if ok {
		t.Error("expected false: worker doesn't have chai")
	}
	if !strings.Contains(reason, "chai") {
		t.Errorf("unexpected reason: %s", reason)
	}
}

// --- InplaceUpdateRequirement unsupported detection ---

func TestTick_InplaceUpdateRequirement_UnsupportedError(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	// CWL with InplaceUpdateRequirement enabled.
	rawCWL := `{
  "$graph": [
    {
      "id": "#main",
      "class": "Workflow",
      "inputs": [],
      "outputs": [{"id": "out", "type": "File", "outputSource": "inplace_step/out"}],
      "steps": {
        "inplace_step": {
          "run": "#inplace_tool",
          "in": [],
          "out": ["out"]
        }
      }
    },
    {
      "id": "#inplace_tool",
      "class": "CommandLineTool",
      "baseCommand": ["echo", "hello"],
      "requirements": {
        "InplaceUpdateRequirement": {"inplaceUpdate": true}
      },
      "inputs": [],
      "outputs": [{"id": "out", "type": "stdout"}],
      "stdout": "output.txt"
    }
  ]
}`

	steps := []model.Step{
		{
			ID:      "inplace_step",
			ToolRef: "inplace_tool",
			ToolInline: &model.Tool{
				ID:          "inplace_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Set RawCWL on the workflow so populateToolAndJob can parse the tool.
	sub, _ := st.GetSubmission(ctx, subID)
	wf, _ := st.GetWorkflow(ctx, sub.WorkflowID)
	wf.RawCWL = rawCWL
	_ = st.UpdateWorkflow(ctx, wf)

	// Run ticks until the submission reaches a terminal state.
	for i := 0; i < 5; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", i+1, err)
		}
		sub, _ = st.GetSubmission(ctx, subID)
		if sub.State.IsTerminal() {
			break
		}
	}

	// Verify submission is FAILED with UNSUPPORTED_REQUIREMENT error code.
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("sub.State = %q, want FAILED", sub.State)
	}
	if sub.Error == nil {
		t.Fatal("sub.Error is nil, expected UNSUPPORTED_REQUIREMENT error")
	}
	if sub.Error.Code != string(model.ErrUnsupportedRequirement) {
		t.Errorf("sub.Error.Code = %q, want %q", sub.Error.Code, model.ErrUnsupportedRequirement)
	}
	if !strings.Contains(sub.Error.Message, "InplaceUpdateRequirement") {
		t.Errorf("sub.Error.Message = %q, want it to mention InplaceUpdateRequirement", sub.Error.Message)
	}
}

// --- Feature A: Pre-flight deferral tests ---

func TestPreflightDeferral_NoWorker_DefersAndFails(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	// Configure for worker executor and small deferral threshold.
	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 3
	sched.config.MaxRetries = 0

	steps := []model.Step{
		{
			ID: "container_step",
			ToolInline: &model.Tool{
				ID:          "tool1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
			Hints: &model.StepHints{
				DockerImage: "alpine",
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Ticks 1 and 2: step should remain READY (deferred).
	for tick := 1; tick <= 2; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", tick, err)
		}
		siByStep := getStepInstancesByStep(t, st, subID)
		if siByStep["container_step"].State != model.StepStateReady {
			t.Errorf("after tick %d: expected READY, got %s", tick, siByStep["container_step"].State)
		}
		// No tasks should be created.
		tasks, _ := st.ListTasksBySubmission(ctx, subID)
		if len(tasks) != 0 {
			t.Errorf("after tick %d: expected 0 tasks, got %d", tick, len(tasks))
		}
	}

	// Tick 3: deferral threshold reached → step should be FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	siByStep := getStepInstancesByStep(t, st, subID)
	if siByStep["container_step"].State != model.StepStateFailed {
		t.Errorf("after tick 3: expected FAILED, got %s", siByStep["container_step"].State)
	}
}

func TestPreflightDeferral_WorkerComesOnline(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	// Register worker executor so it can be resolved.
	workerExec := executor.NewWorkerExecutor(st, sched.logger)
	sched.registry.Register(workerExec)

	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 5
	sched.config.MaxRetries = 0

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "tool1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
			Hints: &model.StepHints{
				DockerImage: "alpine",
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Tick 1: no workers → deferred.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	siByStep := getStepInstancesByStep(t, st, subID)
	if siByStep["step1"].State != model.StepStateReady {
		t.Fatalf("after tick 1: expected READY, got %s", siByStep["step1"].State)
	}

	// Register a worker.
	worker := &model.Worker{
		ID:       "worker_" + uuid.New().String(),
		Name:     "test-worker",
		State:    model.WorkerStateOnline,
		Runtime:  model.RuntimeApptainer,
		LastSeen: time.Now().UTC(),
	}
	if err := st.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}

	// Tick 2: worker online → step should be dispatched (QUEUED task created).
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	siByStep = getStepInstancesByStep(t, st, subID)
	if siByStep["step1"].State == model.StepStateReady {
		t.Error("after tick 2: step should no longer be READY after worker came online")
	}
	tasks, _ := st.ListTasksBySubmission(ctx, subID)
	if len(tasks) == 0 {
		t.Error("after tick 2: expected at least 1 task created")
	}
}

func TestPreflightDeferral_Disabled(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	// Register worker executor.
	workerExec := executor.NewWorkerExecutor(st, sched.logger)
	sched.registry.Register(workerExec)

	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 0 // Disabled.
	sched.config.MaxRetries = 0

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "tool1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// With preflight disabled, task should be created even without workers.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	tasks, _ := st.ListTasksBySubmission(ctx, subID)
	if len(tasks) == 0 {
		t.Error("expected task to be created when preflight is disabled")
	}
}

// --- Feature B: Stuck task detector tests ---

func TestDetectStuckTasks_ProgressResets(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	sched.config.StuckTaskThreshold = 3

	// Create a QUEUED worker task manually.
	now := time.Now().UTC()
	task := &model.Task{
		ID:           "task_stuck_test",
		SubmissionID: "sub_test",
		StepID:       "step1",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		RuntimeHints: &model.RuntimeHints{DockerImage: "alpine"},
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		CreatedAt:    now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	affected := make(map[string]bool)

	// Run 3 ticks: first is baseline (staleTicks=0), then 2 stale ticks.
	for i := 0; i < 3; i++ {
		if err := sched.detectStuckTasks(ctx, affected); err != nil {
			t.Fatalf("detectStuckTasks tick %d: %v", i, err)
		}
	}

	key := requirementKeyForTask(task)
	if sched.stuck.staleTicks[key] != 2 {
		t.Errorf("expected 2 stale ticks, got %d", sched.stuck.staleTicks[key])
	}

	// Simulate progress by removing the task (count drops to 0).
	task.State = model.TaskStateRunning
	nt := time.Now().UTC()
	task.StartedAt = &nt
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	if err := sched.detectStuckTasks(ctx, affected); err != nil {
		t.Fatalf("detectStuckTasks after progress: %v", err)
	}

	// Key should be cleaned up (0 queued tasks).
	if _, exists := sched.stuck.staleTicks[key]; exists {
		t.Error("expected stale ticks to be cleaned up after task left QUEUED")
	}
}

func TestDetectStuckTasks_FailAction(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	sched.config.StuckTaskThreshold = 2
	sched.config.StuckTaskAction = "fail"

	now := time.Now().UTC()
	task := &model.Task{
		ID:           "task_stuck_fail",
		SubmissionID: "sub_test",
		StepID:       "step1",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		RuntimeHints: &model.RuntimeHints{
			DockerImage: "alpine",
			WorkerGroup: "nonexistent",
		},
		Inputs:     map[string]any{},
		Outputs:    map[string]any{},
		MaxRetries: 3,
		CreatedAt:  now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	affected := make(map[string]bool)

	// Run threshold+1 ticks to trigger (first tick is baseline).
	for i := 0; i < 3; i++ {
		if err := sched.detectStuckTasks(ctx, affected); err != nil {
			t.Fatalf("detectStuckTasks tick %d: %v", i, err)
		}
	}

	// Task should be failed.
	updated, err := st.GetTask(ctx, "task_stuck_fail")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if updated.State != model.TaskStateFailed {
		t.Errorf("expected task to be FAILED, got %s", updated.State)
	}
	if !strings.Contains(updated.Stderr, "Task stuck: no capable worker") {
		t.Errorf("expected stuck task reason in stderr, got: %s", updated.Stderr)
	}
}

func TestScrubTaskToken(t *testing.T) {
	tests := []struct {
		name         string
		task         *model.Task
		wantNilCred  bool
		wantNilHints bool
	}{
		{
			name:         "nil RuntimeHints",
			task:         &model.Task{},
			wantNilHints: true,
		},
		{
			name: "nil StagerOverrides",
			task: &model.Task{
				RuntimeHints: &model.RuntimeHints{DockerImage: "alpine"},
			},
			wantNilCred: true,
		},
		{
			name: "nil HTTPCredential",
			task: &model.Task{
				RuntimeHints: &model.RuntimeHints{
					StagerOverrides: &model.StagerOverrides{},
				},
			},
			wantNilCred: true,
		},
		{
			name: "credential is scrubbed",
			task: &model.Task{
				RuntimeHints: &model.RuntimeHints{
					DockerImage: "alpine",
					StagerOverrides: &model.StagerOverrides{
						HTTPCredential: &model.HTTPCredential{
							Type:  "bearer",
							Token: "secret-token-value",
						},
					},
				},
			},
			wantNilCred: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scrubTaskToken(tt.task)

			if tt.wantNilHints {
				// RuntimeHints was nil and should stay nil.
				if tt.task.RuntimeHints != nil {
					t.Error("expected RuntimeHints to remain nil")
				}
				return
			}

			if tt.task.RuntimeHints.StagerOverrides == nil {
				if !tt.wantNilCred {
					t.Error("StagerOverrides unexpectedly nil")
				}
				return
			}

			if tt.task.RuntimeHints.StagerOverrides.HTTPCredential != nil {
				t.Errorf("HTTPCredential should be nil after scrub, got %+v",
					tt.task.RuntimeHints.StagerOverrides.HTTPCredential)
			}

			// Other fields should be preserved.
			if tt.task.RuntimeHints.DockerImage == "alpine" {
				// Verify DockerImage wasn't wiped.
				if tt.task.RuntimeHints.DockerImage != "alpine" {
					t.Error("DockerImage was unexpectedly cleared")
				}
			}
		})
	}
}

func TestRequirementKeyForTask(t *testing.T) {
	tests := []struct {
		name string
		task *model.Task
		want taskRequirementKey
	}{
		{
			name: "no hints",
			task: &model.Task{ExecutorType: model.ExecutorTypeWorker},
			want: taskRequirementKey{},
		},
		{
			name: "docker image ignored",
			task: &model.Task{
				ExecutorType: model.ExecutorTypeWorker,
				RuntimeHints: &model.RuntimeHints{DockerImage: "alpine"},
			},
			want: taskRequirementKey{}, // DockerImage not part of key
		},
		{
			name: "group and prestage",
			task: &model.Task{
				ExecutorType: model.ExecutorTypeWorker,
				RuntimeHints: &model.RuntimeHints{
					DockerImage: "alpine",
					WorkerGroup: "gpu",
					RequiredDatasets: []model.DatasetRequirement{
						{ID: "chai", Mode: "prestage"},
						{ID: "boltz", Mode: "prestage"},
						{ID: "cache_ds", Mode: "cache"},
					},
				},
			},
			want: taskRequirementKey{
				WorkerGroup: "gpu",
				PrestageIDs: "boltz,chai", // sorted
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := requirementKeyForTask(tt.task)
			if got != tt.want {
				t.Errorf("requirementKeyForTask() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGroupAutoInjectsToken verifies the scoped token-injection policy: only
// worker groups the operator opted into (--token-inject-groups) auto-receive the
// submitter's token; the default/untrusted groups stay opt-in (SPEC §13.5).
func TestGroupAutoInjectsToken(t *testing.T) {
	l := &Loop{config: Config{TokenInjectGroups: []string{"bvbrc", "esmfold"}}}
	cases := []struct {
		group string
		want  bool
	}{
		{"bvbrc", true},
		{"esmfold", true},
		{"default", false},
		{"", false}, // empty group resolves to "default"
		{"random", false},
	}
	for _, c := range cases {
		task := &model.Task{RuntimeHints: &model.RuntimeHints{WorkerGroup: c.group}}
		if got := l.groupAutoInjectsToken(task); got != c.want {
			t.Errorf("group %q: got %v, want %v", c.group, got, c.want)
		}
	}

	// With no configured groups, nothing auto-injects (safe default).
	empty := &Loop{config: Config{}}
	if empty.groupAutoInjectsToken(&model.Task{RuntimeHints: &model.RuntimeHints{WorkerGroup: "bvbrc"}}) {
		t.Error("empty TokenInjectGroups must never auto-inject")
	}
}

// TestAddUserToken verifies the least-privilege gate on embedding the
// submitter's token into a task's RuntimeHints (#133 — PR #132 had briefly
// made this unconditional for every worker task). BV-BRC executor tasks and
// the legacy wsStager==nil case always receive the token; worker tasks only
// receive it when the step opted in via gowe:Execution.inject_bvbrc_token, or
// when the task's worker group is in the operator's --token-inject-groups
// policy (in which case InjectBVBRCToken must also be set, so
// internal/worker/worker.go actually honors the grant).
func TestAddUserToken(t *testing.T) {
	tests := []struct {
		name         string
		executorType model.ExecutorType
		hint         bool
		group        string
		wsStagerNil  bool
		tokenInject  []string
		wantToken    bool
		wantHintSet  bool // RuntimeHints.InjectBVBRCToken after the call
	}{
		{
			name:         "bvbrc executor always gets token, regardless of staging mode",
			executorType: model.ExecutorTypeBVBRC,
			wantToken:    true,
		},
		{
			name:         "worker with opt-in hint gets token",
			executorType: model.ExecutorTypeWorker,
			hint:         true,
			wantToken:    true,
			wantHintSet:  true,
		},
		{
			name:         "worker without hint and without group policy gets no token",
			executorType: model.ExecutorTypeWorker,
			wantToken:    false,
		},
		{
			name:         "wsStager nil (legacy passthrough) always gets token",
			executorType: model.ExecutorTypeWorker,
			wsStagerNil:  true,
			wantToken:    true,
		},
		{
			name:         "worker in an opted-in group gets token and hint is set",
			executorType: model.ExecutorTypeWorker,
			group:        "esmfold",
			tokenInject:  []string{"esmfold"},
			wantToken:    true,
			wantHintSet:  true,
		},
		{
			name:         "worker in a non-opted-in group gets no token",
			executorType: model.ExecutorTypeWorker,
			group:        "default",
			tokenInject:  []string{"esmfold"},
			wantToken:    false,
		},
		{
			name:         "local executor without hint gets no token",
			executorType: model.ExecutorTypeLocal,
			wantToken:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loop{config: Config{TokenInjectGroups: tt.tokenInject}}
			if !tt.wsStagerNil {
				l.wsStager = unreachableStager()
			}
			task := &model.Task{
				ExecutorType: tt.executorType,
				RuntimeHints: &model.RuntimeHints{InjectBVBRCToken: tt.hint, WorkerGroup: tt.group},
			}
			sub := &model.Submission{UserToken: "the-token"}

			l.addUserToken(task, sub)

			gotToken := task.RuntimeHints.StagerOverrides != nil &&
				task.RuntimeHints.StagerOverrides.HTTPCredential != nil &&
				task.RuntimeHints.StagerOverrides.HTTPCredential.Token == "the-token"
			if gotToken != tt.wantToken {
				t.Errorf("token embedded = %v, want %v (hints: %+v)", gotToken, tt.wantToken, task.RuntimeHints)
			}
			if task.RuntimeHints.InjectBVBRCToken != tt.wantHintSet {
				t.Errorf("InjectBVBRCToken = %v, want %v", task.RuntimeHints.InjectBVBRCToken, tt.wantHintSet)
			}
		})
	}

	// No submitter token: nothing embedded regardless of policy.
	t.Run("no submitter token: nothing embedded", func(t *testing.T) {
		l := &Loop{config: Config{}}
		l.wsStager = unreachableStager()
		task := &model.Task{ExecutorType: model.ExecutorTypeBVBRC}
		sub := &model.Submission{UserToken: ""}
		l.addUserToken(task, sub)
		if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
			t.Errorf("expected no token embedded, got %+v", task.RuntimeHints.StagerOverrides)
		}
	})
}

// TestAreStepDependenciesSatisfied covers the CWL v1.2 "no skip cascade"
// semantics: a SKIPPED dependency must count as satisfied (its outputs
// resolve to null downstream), while a FAILED dependency must still block
// (and cascade-skip) the consumer.
func TestAreStepDependenciesSatisfied(t *testing.T) {
	mk := func(state model.StepInstanceState) *model.StepInstance {
		return &model.StepInstance{State: state}
	}

	tests := []struct {
		name          string
		dependsOn     []string
		steps         map[string]*model.StepInstance
		wantSatisfied bool
		wantBlocked   bool
	}{
		{
			name:          "no dependencies",
			dependsOn:     nil,
			steps:         map[string]*model.StepInstance{},
			wantSatisfied: true,
			wantBlocked:   false,
		},
		{
			name:      "all completed",
			dependsOn: []string{"a", "b"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateCompleted),
				"b": mk(model.StepStateCompleted),
			},
			wantSatisfied: true,
			wantBlocked:   false,
		},
		{
			name:      "skipped dependency is satisfied, not blocked",
			dependsOn: []string{"a"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateSkipped),
			},
			wantSatisfied: true,
			wantBlocked:   false,
		},
		{
			name:      "mix of completed and skipped is satisfied",
			dependsOn: []string{"a", "b"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateCompleted),
				"b": mk(model.StepStateSkipped),
			},
			wantSatisfied: true,
			wantBlocked:   false,
		},
		{
			name:      "failed dependency still blocks",
			dependsOn: []string{"a"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateFailed),
			},
			wantSatisfied: false,
			wantBlocked:   true,
		},
		{
			name:      "failed dependency blocks even alongside a completed one",
			dependsOn: []string{"a", "b"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateCompleted),
				"b": mk(model.StepStateFailed),
			},
			wantSatisfied: false,
			wantBlocked:   true,
		},
		{
			name:      "still-running dependency is neither satisfied nor blocked",
			dependsOn: []string{"a"},
			steps: map[string]*model.StepInstance{
				"a": mk(model.StepStateRunning),
			},
			wantSatisfied: false,
			wantBlocked:   false,
		},
		{
			name:          "missing dependency is treated as blocked",
			dependsOn:     []string{"missing"},
			steps:         map[string]*model.StepInstance{},
			wantSatisfied: false,
			wantBlocked:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			satisfied, blocked := areStepDependenciesSatisfied(tt.dependsOn, tt.steps)
			if satisfied != tt.wantSatisfied {
				t.Errorf("satisfied = %v, want %v", satisfied, tt.wantSatisfied)
			}
			if blocked != tt.wantBlocked {
				t.Errorf("blocked = %v, want %v", blocked, tt.wantBlocked)
			}
		})
	}
}
