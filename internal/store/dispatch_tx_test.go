package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// --- CreateTasksAndDispatchStep tests ---

// dispatchFixture creates a workflow, submission, and a READY step instance,
// returning the store and the step instance.
func dispatchFixture(t *testing.T) (*SQLiteStore, *model.StepInstance) {
	t.Helper()
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

	si := &model.StepInstance{
		ID:           "si_dispatch-1",
		SubmissionID: sub.ID,
		StepID:       "assemble",
		State:        model.StepStateReady,
		Outputs:      map[string]any{},
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}
	return st, si
}

// dispatchTasks builds n proxy-style tasks bound to the step instance.
func dispatchTasks(si *model.StepInstance, n int) []*model.Task {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tasks := make([]*model.Task, 0, n)
	for i := 0; i < n; i++ {
		tasks = append(tasks, &model.Task{
			ID:             fmt.Sprintf("task_dispatch-%d", i),
			SubmissionID:   si.SubmissionID,
			StepID:         si.StepID,
			StepInstanceID: si.ID,
			State:          model.TaskStateRunning,
			ExecutorType:   model.ExecutorTypeSubworkflow,
			Inputs:         map[string]any{},
			Outputs:        map[string]any{},
			Job:            map[string]any{"item": fmt.Sprintf("value-%d", i)},
			ScatterIndex:   i,
			MaxRetries:     0,
			CreatedAt:      now,
		})
	}
	return tasks
}

func TestCreateTasksAndDispatchStep(t *testing.T) {
	st, si := dispatchFixture(t)
	ctx := context.Background()

	tasks := dispatchTasks(si, 3)
	si.State = model.StepStateDispatched
	si.ScatterCount = 3
	si.ScatterMethod = "dotproduct"
	si.ScatterDims = []int{3}

	if err := st.CreateTasksAndDispatchStep(ctx, tasks, si); err != nil {
		t.Fatalf("CreateTasksAndDispatchStep: %v", err)
	}

	got, err := st.ListTasksByStepInstance(ctx, si.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(got))
	}
	for i, task := range got {
		if task.ExecutorType != model.ExecutorTypeSubworkflow {
			t.Errorf("task %d executor = %q, want %q", i, task.ExecutorType, model.ExecutorTypeSubworkflow)
		}
		if task.State != model.TaskStateRunning {
			t.Errorf("task %d state = %q, want %q", i, task.State, model.TaskStateRunning)
		}
	}

	gotSI, err := st.GetStepInstance(ctx, si.ID)
	if err != nil {
		t.Fatalf("get step instance: %v", err)
	}
	if gotSI.State != model.StepStateDispatched {
		t.Errorf("step state = %q, want %q", gotSI.State, model.StepStateDispatched)
	}
	if gotSI.ScatterCount != 3 || gotSI.ScatterMethod != "dotproduct" {
		t.Errorf("scatter fields = (%d, %q), want (3, dotproduct)", gotSI.ScatterCount, gotSI.ScatterMethod)
	}
	if len(gotSI.ScatterDims) != 1 || gotSI.ScatterDims[0] != 3 {
		t.Errorf("scatter dims = %v, want [3]", gotSI.ScatterDims)
	}
}

func TestCreateTasksAndDispatchStep_Atomic(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(tasks []*model.Task, si *model.StepInstance)
	}{
		{
			name: "duplicate task ID mid-batch",
			corrupt: func(tasks []*model.Task, si *model.StepInstance) {
				tasks[2].ID = tasks[0].ID // primary-key collision on the third insert
			},
		},
		{
			name: "step instance does not exist",
			corrupt: func(tasks []*model.Task, si *model.StepInstance) {
				si.ID = "si_nonexistent"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, si := dispatchFixture(t)
			ctx := context.Background()
			originalSI := si.ID

			tasks := dispatchTasks(si, 3)
			si.State = model.StepStateDispatched
			si.ScatterCount = 3
			tt.corrupt(tasks, si)

			if err := st.CreateTasksAndDispatchStep(ctx, tasks, si); err == nil {
				t.Fatal("expected error, got nil")
			}

			// NOTHING may have persisted: no task rows, step instance unchanged.
			got, err := st.ListTasksBySubmission(ctx, si.SubmissionID)
			if err != nil {
				t.Fatalf("list tasks: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("expected 0 tasks after rollback, got %d", len(got))
			}
			gotSI, err := st.GetStepInstance(ctx, originalSI)
			if err != nil {
				t.Fatalf("get step instance: %v", err)
			}
			if gotSI.State != model.StepStateReady {
				t.Errorf("step state = %q, want %q (unchanged)", gotSI.State, model.StepStateReady)
			}
			if gotSI.ScatterCount != 0 {
				t.Errorf("scatter count = %d, want 0 (unchanged)", gotSI.ScatterCount)
			}
		})
	}
}

// --- CASTaskState tests ---

// TestCASTaskState verifies the generic compare-and-set is guarded: a task
// that concurrently left the `from` state (e.g. a cancel SKIPPED it) must not
// be moved by a stale snapshot — neither the retry marking (FAILED→RETRYING)
// nor the retry claiming (RETRYING→SCHEDULED).
func TestCASTaskState(t *testing.T) {
	tests := []struct {
		name        string
		state       model.TaskState
		from        model.TaskState
		to          model.TaskState
		wantApplied bool
		wantState   model.TaskState
	}{
		// Retry marking: FAILED→RETRYING.
		{"failed task flips to retrying", model.TaskStateFailed,
			model.TaskStateFailed, model.TaskStateRetrying, true, model.TaskStateRetrying},
		{"skipped task is left alone", model.TaskStateSkipped,
			model.TaskStateFailed, model.TaskStateRetrying, false, model.TaskStateSkipped},
		{"success task is left alone", model.TaskStateSuccess,
			model.TaskStateFailed, model.TaskStateRetrying, false, model.TaskStateSuccess},
		{"running task is left alone", model.TaskStateRunning,
			model.TaskStateFailed, model.TaskStateRetrying, false, model.TaskStateRunning},
		// Retry claiming: RETRYING→SCHEDULED.
		{"retrying task is claimed to scheduled", model.TaskStateRetrying,
			model.TaskStateRetrying, model.TaskStateScheduled, true, model.TaskStateScheduled},
		{"claim refused: task cancelled since snapshot", model.TaskStateSkipped,
			model.TaskStateRetrying, model.TaskStateScheduled, false, model.TaskStateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			task := sampleTask(sub.ID)
			task.State = tt.state
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			applied, err := st.CASTaskState(ctx, task.ID, tt.from, tt.to)
			if err != nil {
				t.Fatalf("CASTaskState: %v", err)
			}
			if applied != tt.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tt.wantApplied)
			}

			got, err := st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if got.State != tt.wantState {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
		})
	}
}

// --- GetChildSubmissions tests ---

func TestGetChildSubmissions_Error(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	parent := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, parent); err != nil {
		t.Fatalf("create parent submission: %v", err)
	}
	task := sampleTask(parent.ID)
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	child := sampleSubmission(wf.ID)
	child.ID = "sub_child-1"
	child.ParentTaskID = task.ID
	if err := st.CreateSubmission(ctx, child); err != nil {
		t.Fatalf("create child submission: %v", err)
	}

	// Fail the child with a structured error; GetChildSubmissions must
	// surface it so the scheduler can propagate the detail into the proxy
	// task's stderr.
	child.State = model.SubmissionStateFailed
	child.Error = &model.SubmissionError{Code: "STEP_FAILED", Message: "boom"}
	if err := st.UpdateSubmission(ctx, child); err != nil {
		t.Fatalf("update child submission: %v", err)
	}

	children, err := st.GetChildSubmissions(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetChildSubmissions: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	got := children[0]
	if got.State != model.SubmissionStateFailed {
		t.Errorf("state = %q, want %q", got.State, model.SubmissionStateFailed)
	}
	if got.Error == nil {
		t.Fatal("expected Error to be populated")
	}
	if got.Error.Code != "STEP_FAILED" || got.Error.Message != "boom" {
		t.Errorf("error = %+v, want Code=STEP_FAILED Message=boom", got.Error)
	}
}

// TestGetChildSubmissions_StrictScan verifies a corrupt child row surfaces as
// an error instead of being silently skipped: converting "child exists but is
// unreadable" into "no child" would make the scheduler's repair path create a
// duplicate child submission.
func TestGetChildSubmissions_StrictScan(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	parent := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, parent); err != nil {
		t.Fatalf("create parent submission: %v", err)
	}
	task := sampleTask(parent.ID)
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	child := sampleSubmission(wf.ID)
	child.ID = "sub_child-corrupt"
	child.ParentTaskID = task.ID
	if err := st.CreateSubmission(ctx, child); err != nil {
		t.Fatalf("create child submission: %v", err)
	}

	// Corrupt the child row directly (invalid JSON in inputs).
	if _, err := st.db.ExecContext(ctx,
		`UPDATE submissions SET inputs = '{not-json' WHERE id = ?`, child.ID); err != nil {
		t.Fatalf("corrupt child row: %v", err)
	}

	children, err := st.GetChildSubmissions(ctx, task.ID)
	if err == nil {
		t.Fatalf("expected error for corrupt child row, got %d children", len(children))
	}
	if !strings.Contains(err.Error(), child.ID) {
		t.Errorf("error = %q, want it to name the corrupt row %s", err, child.ID)
	}
}

// --- CreateSubmissionWithSteps tests ---

func submissionWithSteps(wfID string, n int) (*model.Submission, []*model.StepInstance) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	sub := sampleSubmission(wfID)
	sub.ID = "sub_with-steps-1"
	steps := make([]*model.StepInstance, 0, n)
	for i := 0; i < n; i++ {
		steps = append(steps, &model.StepInstance{
			ID:           fmt.Sprintf("si_with-steps-%d", i),
			SubmissionID: sub.ID,
			StepID:       fmt.Sprintf("step%d", i),
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		})
	}
	return sub, steps
}

func TestCreateSubmissionWithSteps(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub, steps := submissionWithSteps(wf.ID, 3)
	sub.UserToken = "secret-token"

	if err := st.CreateSubmissionWithSteps(ctx, sub, steps); err != nil {
		t.Fatalf("CreateSubmissionWithSteps: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if got == nil {
		t.Fatal("submission not persisted")
	}
	// Token round-trips through the CreateSubmission encryption path.
	if got.UserToken != "secret-token" {
		t.Errorf("UserToken = %q, want %q (encryption path must match CreateSubmission)", got.UserToken, "secret-token")
	}
	gotSteps, err := st.ListStepsBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ListStepsBySubmission: %v", err)
	}
	if len(gotSteps) != 3 {
		t.Fatalf("expected 3 step instances, got %d", len(gotSteps))
	}
	for _, si := range gotSteps {
		if si.State != model.StepStateWaiting {
			t.Errorf("step %s state = %q, want WAITING", si.ID, si.State)
		}
	}
}

// TestCreateSubmissionWithSteps_Atomic verifies all-or-nothing: a step insert
// failing mid-batch must roll back the submission row too, so a crash window
// can never leave a zero-step child submission for finalize to complete as an
// empty success.
func TestCreateSubmissionWithSteps_Atomic(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub, steps := submissionWithSteps(wf.ID, 3)
	steps[2].ID = steps[0].ID // primary-key collision on the third insert

	if err := st.CreateSubmissionWithSteps(ctx, sub, steps); err == nil {
		t.Fatal("expected error, got nil")
	}

	// NOTHING may have persisted: no submission row, no step rows.
	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if got != nil {
		t.Errorf("submission row persisted after rollback (state %q)", got.State)
	}
	gotSteps, err := st.ListStepsBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ListStepsBySubmission: %v", err)
	}
	if len(gotSteps) != 0 {
		t.Errorf("expected 0 step instances after rollback, got %d", len(gotSteps))
	}
}
