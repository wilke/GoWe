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
	if !strings.Contains(updated.Stderr, "stuck task failed") {
		t.Errorf("expected stuck task reason in stderr, got: %s", updated.Stderr)
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
