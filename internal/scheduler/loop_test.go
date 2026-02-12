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

// createPipeline creates a workflow, a PENDING submission, and one PENDING task
// per step. maxRetries is set on each task at creation time (UpdateTask does not
// persist max_retries). It returns (workflowID, submissionID).
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

	for _, step := range steps {
		task := &model.Task{
			ID:           "task_" + uuid.New().String(),
			SubmissionID: subID,
			StepID:       step.ID,
			State:        model.TaskStatePending,
			ExecutorType: model.ExecutorTypeLocal,
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			DependsOn:    step.DependsOn,
			MaxRetries:   maxRetries,
			CreatedAt:    now,
		}
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", step.ID, err)
		}
	}

	return wfID, subID
}

// TestTick_SingleStepNoDeps verifies that a single step with no dependencies
// transitions from PENDING through SCHEDULED to SUCCESS in a single tick, and
// the parent submission is finalized as COMPLETED.
func TestTick_SingleStepNoDeps(t *testing.T) {
	sched, st := testSetup(t)
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

	// Verify task state.
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
// on step1. step1 completes in tick 1; step2 completes in tick 2.
//
// With the synchronous LocalExecutor, tasks complete within the dispatch phase
// of a single tick. The dependency check in phase 1 evaluates DB state at the
// start of the tick, so step2 does not see step1 as SUCCESS until tick 2.
//
// Because all non-terminal tasks are in PENDING state when finalizeSubmissions
// runs (LocalExecutor is synchronous), the submission goes directly from
// PENDING to COMPLETED when the last task finishes.
func TestTick_TwoStepPipeline(t *testing.T) {
	sched, st := testSetup(t)
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

	// Tick 1: step1 goes PENDING -> SCHEDULED -> SUCCESS.
	// step2 stays PENDING because step1 was still PENDING when phase 1 ran.
	// Submission stays PENDING because the only non-terminal task (step2) is PENDING,
	// and finalizeSubmissions requires anyActive (non-PENDING) to transition to RUNNING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 1: %v", err)
	}

	taskByStep := make(map[string]*model.Task)
	for _, tk := range tasks {
		taskByStep[tk.StepID] = tk
	}

	if taskByStep["step1"].State != model.TaskStateSuccess {
		t.Errorf("after tick 1: step1.State = %q, want SUCCESS", taskByStep["step1"].State)
	}
	if taskByStep["step2"].State != model.TaskStatePending {
		t.Errorf("after tick 1: step2.State = %q, want PENDING", taskByStep["step2"].State)
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 1: %v", err)
	}
	if sub.State != model.SubmissionStatePending {
		t.Errorf("after tick 1: sub.State = %q, want PENDING", sub.State)
	}

	// Tick 2: step2 goes PENDING -> SCHEDULED -> SUCCESS.
	// All tasks terminal, none failed -> submission COMPLETED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	tasks, err = st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 2: %v", err)
	}
	taskByStep = make(map[string]*model.Task)
	for _, tk := range tasks {
		taskByStep[tk.StepID] = tk
	}

	if taskByStep["step1"].State != model.TaskStateSuccess {
		t.Errorf("after tick 2: step1.State = %q, want SUCCESS", taskByStep["step1"].State)
	}
	if taskByStep["step2"].State != model.TaskStateSuccess {
		t.Errorf("after tick 2: step2.State = %q, want SUCCESS", taskByStep["step2"].State)
	}
	if !strings.Contains(taskByStep["step2"].Stdout, "world") {
		t.Errorf("step2.Stdout = %q, want it to contain \"world\"", taskByStep["step2"].Stdout)
	}

	// Submission should be COMPLETED.
	sub, err = st.GetSubmission(ctx, subID)
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
// (with no retries), the downstream task is SKIPPED and the submission is FAILED.
func TestTick_FailedDep_SkipsDownstream(t *testing.T) {
	sched, st := testSetup(t)
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

	// MaxRetries=0 so the failed task stays FAILED (no retry).
	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	// Tick 1: step1 PENDING -> SCHEDULED -> FAILED (exit code 1, MaxRetries=0 so no retry).
	// step2 stays PENDING since step1 was PENDING at phase 1 time.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 1: %v", err)
	}
	taskByStep := make(map[string]*model.Task)
	for _, tk := range tasks {
		taskByStep[tk.StepID] = tk
	}

	if taskByStep["step1"].State != model.TaskStateFailed {
		t.Errorf("after tick 1: step1.State = %q, want FAILED", taskByStep["step1"].State)
	}
	if taskByStep["step1"].ExitCode == nil || *taskByStep["step1"].ExitCode != 1 {
		t.Errorf("after tick 1: step1.ExitCode = %v, want 1", taskByStep["step1"].ExitCode)
	}
	if taskByStep["step2"].State != model.TaskStatePending {
		t.Errorf("after tick 1: step2.State = %q, want PENDING", taskByStep["step2"].State)
	}

	// Tick 2: phase 1 sees step2 PENDING, step1 is FAILED -> step2 SKIPPED.
	// Phase 4 finalizes: all terminal (FAILED + SKIPPED), anyFailed -> FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	tasks, err = st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission after tick 2: %v", err)
	}
	taskByStep = make(map[string]*model.Task)
	for _, tk := range tasks {
		taskByStep[tk.StepID] = tk
	}

	if taskByStep["step2"].State != model.TaskStateSkipped {
		t.Errorf("after tick 2: step2.State = %q, want SKIPPED", taskByStep["step2"].State)
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
//
// With synchronous LocalExecutor, all tasks in a single-step pipeline complete
// within one tick. The submission transitions directly PENDING -> COMPLETED
// because there are no in-flight (non-PENDING, non-terminal) tasks at finalize
// time. The intermediate RUNNING state only occurs with async executors or when
// there is a mix of active and terminal tasks at finalize time.
func TestTick_SubmissionTransitions(t *testing.T) {
	sched, st := testSetup(t)
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

	// Tick 1: step1 PENDING -> SCHEDULED -> SUCCESS -> submission COMPLETED.
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
// through a failure scenario: PENDING -> RUNNING -> FAILED. The RUNNING state
// is triggered when a task fails (anyFailed=true) while another is still PENDING.
func TestTick_SubmissionTransitions_WithFailure(t *testing.T) {
	sched, st := testSetup(t)
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

	// Initial state: PENDING.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStatePending {
		t.Fatalf("initial sub.State = %q, want PENDING", sub.State)
	}

	// Tick 1: step1 PENDING -> SCHEDULED -> FAILED. step2 stays PENDING.
	// finalizeSubmissions: step1=FAILED (terminal, anyFailed=true), step2=PENDING.
	// allTerminal=false, anyFailed=true, sub.State==PENDING -> RUNNING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	sub, err = st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission after tick 1: %v", err)
	}
	if sub.State != model.SubmissionStateRunning {
		t.Errorf("after tick 1: sub.State = %q, want RUNNING", sub.State)
	}

	// Tick 2: step2 PENDING -> dep step1 FAILED -> step2 SKIPPED.
	// finalizeSubmissions: all terminal (FAILED + SKIPPED), anyFailed -> FAILED.
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
//   - Tick 1: PENDING -> SCHEDULED -> FAILED. Phase 5: RetryCount(0) < 2 -> RETRYING.
//   - Tick 2: RETRYING -> resubmit (RetryCount=1) -> FAILED. Phase 5: 1 < 2 -> RETRYING.
//   - Tick 3: RETRYING -> resubmit (RetryCount=2) -> FAILED. Phase 5: 2 >= 2 -> stays FAILED.
func TestTick_RetryOnFailure(t *testing.T) {
	sched, st := testSetup(t)
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

	// MaxRetries=2 set at creation time (UpdateTask does not persist max_retries).
	_, subID := createPipeline(t, st, steps, map[string]any{}, 2)

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	taskID := tasks[0].ID

	// Tick 1: PENDING -> SCHEDULED -> FAILED. Phase 5: RetryCount(0) < MaxRetries(2) -> RETRYING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	task, err := st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask after tick 1: %v", err)
	}
	if task.State != model.TaskStateRetrying {
		t.Errorf("after tick 1: state = %q, want RETRYING", task.State)
	}
	if task.RetryCount != 0 {
		t.Errorf("after tick 1: RetryCount = %d, want 0", task.RetryCount)
	}

	// Tick 2: Phase 2.5 re-submits RETRYING -> RetryCount=1 -> FAILED.
	// Phase 5: 1 < 2 -> RETRYING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	task, err = st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask after tick 2: %v", err)
	}
	if task.State != model.TaskStateRetrying {
		t.Errorf("after tick 2: state = %q, want RETRYING", task.State)
	}
	if task.RetryCount != 1 {
		t.Errorf("after tick 2: RetryCount = %d, want 1", task.RetryCount)
	}

	// Tick 3: Phase 2.5 re-submits RETRYING -> RetryCount=2 -> FAILED.
	// Phase 5: 2 >= 2 -> stays FAILED.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	task, err = st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask after tick 3: %v", err)
	}
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
