package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestSubmitAndUpdateTask_ExecutorMatrix is the #184 PR2 dispatch/staging
// semantics test: submitAndUpdateTask stamps DispatchedAt for every executor,
// but only stamps StartedAt for synchronous executors (local/container/
// apptainer, whose Submit() call IS the execution start). worker and bvbrc
// are asynchronous — their StartedAt is left for CheckoutTask / MarkTaskRunning
// to stamp later, so submitAndUpdateTask must leave it nil.
func TestSubmitAndUpdateTask_ExecutorMatrix(t *testing.T) {
	cases := []struct {
		name         string
		executorType model.ExecutorType
		wantStarted  bool
	}{
		{"local_stamps_started", model.ExecutorTypeLocal, true},
		{"container_stamps_started", model.ExecutorTypeContainer, true},
		{"apptainer_stamps_started", model.ExecutorTypeApptainer, true},
		{"worker_leaves_started_nil", model.ExecutorTypeWorker, false},
		{"bvbrc_leaves_started_nil", model.ExecutorTypeBVBRC, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, st, reg := pollSetup(t)
			ctx := context.Background()

			reg.Register(&stubExecutor{
				typ: tc.executorType,
				status: func(ctx context.Context, task *model.Task) (model.TaskState, error) {
					return model.TaskStateQueued, nil // non-terminal: exercise the QUEUED path.
				},
			})

			_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
			task := &model.Task{
				ID:           "task_matrix_" + string(tc.executorType),
				SubmissionID: subID,
				StepID:       "s1",
				State:        model.TaskStatePending,
				ExecutorType: tc.executorType,
				Inputs:       map[string]any{},
				Outputs:      map[string]any{},
				ScatterIndex: -1,
				CreatedAt:    time.Now().UTC(),
			}
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			before := time.Now().UTC()
			l.submitAndUpdateTask(ctx, task)

			if task.DispatchedAt == nil {
				t.Fatal("expected DispatchedAt to be stamped for every executor")
			}
			if task.DispatchedAt.Before(before) {
				t.Errorf("DispatchedAt = %v, want >= %v", task.DispatchedAt, before)
			}

			if tc.wantStarted {
				if task.StartedAt == nil {
					t.Fatal("expected StartedAt to be stamped for a synchronous executor")
				}
			} else if task.StartedAt != nil {
				t.Fatalf("expected StartedAt to stay nil for an async executor, got %v", task.StartedAt)
			}

			// Confirm the persisted row (not just the in-memory struct)
			// agrees — persistSubmitOutcome routes through TerminalizeTask's
			// full-row write.
			persisted, err := st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if persisted.DispatchedAt == nil {
				t.Error("persisted DispatchedAt is nil")
			}
			if tc.wantStarted && persisted.StartedAt == nil {
				t.Error("persisted StartedAt is nil for a synchronous executor")
			}
			if !tc.wantStarted && persisted.StartedAt != nil {
				t.Errorf("persisted StartedAt = %v, want nil for an async executor", persisted.StartedAt)
			}
		})
	}
}

// TestSubmitAndUpdateTask_NoExecutor_LeavesDispatchedNil covers the
// registry.Get error path: the task was never actually handed to an
// executor, so DispatchedAt must stay nil (only the FAILED outcome and
// CompletedAt are stamped).
func TestSubmitAndUpdateTask_NoExecutor_LeavesDispatchedNil(t *testing.T) {
	l, st, _ := pollSetup(t)
	ctx := context.Background()

	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
	task := &model.Task{
		ID:           "task_no_exec",
		SubmissionID: subID,
		StepID:       "s1",
		State:        model.TaskStatePending,
		ExecutorType: model.ExecutorType("does-not-exist"),
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	l.submitAndUpdateTask(ctx, task)

	if task.DispatchedAt != nil {
		t.Errorf("DispatchedAt = %v, want nil (never dispatched)", task.DispatchedAt)
	}
	if task.State != model.TaskStateFailed {
		t.Errorf("state = %q, want FAILED", task.State)
	}
}

// TestCreateSubworkflowProxyTask_DispatchedAtEqualsCreatedAt covers proxy
// tasks: they are "dispatched" the instant they're created (the child
// submission itself is the dispatch), so DispatchedAt == CreatedAt.
func TestCreateSubworkflowProxyTask_DispatchedAtEqualsCreatedAt(t *testing.T) {
	l, st := testSetup(t)

	steps := []model.Step{
		{
			ID:      "sub_step",
			ToolRef: "#child.cwl",
		},
	}
	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)
	si := &model.StepInstance{
		ID:           "si_proxy",
		SubmissionID: subID,
		StepID:       "sub_step",
		State:        model.StepStateReady,
		Outputs:      map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}

	tmpTask := &model.Task{Job: map[string]any{}}
	sub := &model.Submission{ID: subID}
	task := l.createSubworkflowProxyTask(si, tmpTask, &steps[0], sub, map[string]any{}, -1)

	if task.DispatchedAt == nil {
		t.Fatal("expected DispatchedAt to be stamped for a proxy task")
	}
	if !task.DispatchedAt.Equal(task.CreatedAt) {
		t.Errorf("DispatchedAt = %v, want == CreatedAt %v", task.DispatchedAt, task.CreatedAt)
	}
}
