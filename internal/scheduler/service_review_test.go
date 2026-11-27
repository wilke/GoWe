package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Review fixes for "a service task is never started a second time", on the
// scheduler side: a service step always runs on the worker executor, the
// dispatch write never puts a checked-out task back in the queue, a READY
// service step adopts its existing task, and a FAILED service is never
// retried.

// serviceCWLWithExecutor is serviceStepCWL with its gowe:Execution.executor
// line replaced by hint (no executor line when "").
func serviceCWLWithExecutor(hint string) string {
	repl := ""
	if hint != "" {
		repl = "        executor: " + hint + "\n"
	}
	return strings.Replace(serviceStepCWL, "        executor: worker\n", repl, 1)
}

// newServiceSched is testSetup with the worker executor registered.
func newServiceSched(t *testing.T) (*Loop, store.Store) {
	t.Helper()
	sched, st := testSetup(t)
	sched.registry.Register(executor.NewWorkerExecutor(st, sched.logger))
	return sched, st
}

// onlyTask returns the one task of step s of subID.
func onlyTask(t *testing.T, sched *Loop, subID string) *model.Task {
	t.Helper()
	tasks := hintsByStep(t, sched, subID)["s"]
	if len(tasks) != 1 {
		t.Fatalf("tasks of step s = %d (%+v), want 1", len(tasks), tasks)
	}
	return tasks[0]
}

// R1: with no worker online, the default executor empty or local, a service
// step without an executor hint still gets a worker task, QUEUED.
func TestServiceStep_AlwaysWorkerExecutor(t *testing.T) {
	for _, def := range []string{"", "local", "worker"} {
		t.Run("default executor "+def, func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			sched.config.DefaultExecutor = def
			subID := registerGraphWorkflowInputsAs(t, st, serviceCWLWithExecutor(""), map[string]any{"port": 8000}, nil, "alice")

			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			si := getStepInstancesByStep(t, st, subID)["s"]
			if si.State != model.StepStateDispatched {
				t.Fatalf("step = %s %q, want DISPATCHED", si.State, si.Error)
			}
			task := onlyTask(t, sched, subID)
			if task.ExecutorType != model.ExecutorTypeWorker || task.State != model.TaskStateQueued || !task.IsService {
				t.Errorf("task = %s %s (service %v), want a QUEUED worker service task", task.ExecutorType, task.State, task.IsService)
			}
		})
	}
}

// R1: a forced executor other than worker, or a stored executor hint other
// than worker / container, fails a service step permanently: no task.
func TestServiceStep_NonWorkerExecutorFails(t *testing.T) {
	tests := []struct {
		name    string
		force   string
		hint    string
		wantErr string
	}{
		{"forced local", "local", "", `the server forces executor "local"`},
		{"forced bvbrc", "bvbrc", "worker", `the server forces executor "bvbrc"`},
		{"hint local", "", "local", `gowe:Execution.executor is "local"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := newServiceSched(t)
			sched.config.ForceExecutor = tt.force
			subID := registerGraphWorkflowInputsAs(t, st, serviceCWLWithExecutor(tt.hint), map[string]any{"port": 8000}, nil, "alice")

			final := runToTerminal(t, sched, st, subID, 5)
			if final.State != model.SubmissionStateFailed {
				t.Fatalf("submission = %s, want FAILED", final.State)
			}
			si := getStepInstancesByStep(t, st, subID)["s"]
			if si.State != model.StepStateFailed || !strings.Contains(si.Error, tt.wantErr) {
				t.Errorf("step = %s %q, want FAILED containing %q", si.State, si.Error, tt.wantErr)
			}
			if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
				t.Errorf("tasks = %+v, want none", tasks)
			}
		})
	}

	t.Run("forced worker", func(t *testing.T) {
		ctx := context.Background()
		sched, st := newServiceSched(t)
		sched.config.ForceExecutor = "worker"
		subID := registerGraphWorkflowInputsAs(t, st, serviceCWLWithExecutor("container"), map[string]any{"port": 8000}, nil, "alice")
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if task := onlyTask(t, sched, subID); task.ExecutorType != model.ExecutorTypeWorker || task.State != model.TaskStateQueued {
			t.Errorf("task = %s %s, want a QUEUED worker task", task.ExecutorType, task.State)
		}
	})
}

// R1: the worker-group check runs for a service step even when, without the
// service rule, it would have resolved to the local executor.
func TestServiceStep_GroupCheckRuns(t *testing.T) {
	sched, st := newServiceSched(t)
	sched.SetGroupPolicy(loadTestGroupPolicy(t, st, testPolicy{rules: []model.WorkerGroupPolicy{{Group: "llm", Users: []string{"alice"}}}}))
	subID := registerGraphWorkflowInputsAs(t, st, serviceCWLWithExecutor(""), map[string]any{"port": 8000}, nil, "bob")

	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Fatalf("submission = %s, want FAILED", final.State)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	if si.State != model.StepStateFailed || !strings.Contains(si.Error, `may not use worker group "llm"`) {
		t.Errorf("step = %s %q, want FAILED by the group check", si.State, si.Error)
	}
	if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none", tasks)
	}
}

// seedQueuedWorkerTask stores a QUEUED worker task of a fresh pipeline (a
// service of group llm when service is set) and an online worker that can
// take it, and returns the task as the scheduler holds it after CreateTask.
func seedQueuedWorkerTask(t *testing.T, st store.Store, service bool) (*model.Task, string) {
	t.Helper()
	ctx := context.Background()
	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
	si := getStepInstancesByStep(t, st, subID)["s1"]
	now := time.Now().UTC()
	task := &model.Task{
		ID: "task_race", SubmissionID: subID, StepID: "s1", StepInstanceID: si.ID,
		State: model.TaskStateQueued, ExecutorType: model.ExecutorTypeWorker,
		Inputs: map[string]any{}, Outputs: map[string]any{}, ScatterIndex: -1, MaxRetries: 3, CreatedAt: now,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm"},
	}
	if service {
		task.RuntimeHints.Service = &model.ServiceSpec{PortInput: "port"}
		task.MaxRetries = 0
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_1", Name: "w1", Group: "llm", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, ServiceMode: service}); err != nil {
		t.Fatal(err)
	}
	return task, subID
}

// checkStillCheckedOut fails unless task_race is RUNNING on wrk_1 with its
// checkout charges.
func checkStillCheckedOut(t *testing.T, st store.Store) {
	t.Helper()
	got, err := st.GetTask(context.Background(), "task_race")
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != model.TaskStateRunning || got.ExternalID != "wrk_1" || got.StartedAt == nil {
		t.Errorf("task = %s on %q, want RUNNING on wrk_1 (the checkout stands)", got.State, got.ExternalID)
	}
	if got.ChargedCores == nil || got.ChargedRamMB == nil || got.ChargedGPUs == nil {
		t.Errorf("charges = %v/%v/%v, want the checkout's", got.ChargedCores, got.ChargedRamMB, got.ChargedGPUs)
	}
}

// R2: a worker that checks the task out between the dispatch's CreateTask
// and its persist keeps it: the stale snapshot (QUEUED) is not written back,
// for batch and service tasks alike.
func TestPersistSubmitOutcome_CheckoutStands(t *testing.T) {
	for _, service := range []bool{false, true} {
		name := "batch"
		if service {
			name = "service"
		}
		t.Run(name+", submit after checkout", func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			stale, _ := seedQueuedWorkerTask(t, st, service)
			if got, err := st.CheckoutTask(ctx, "wrk_1", "llm", model.RuntimeNone); err != nil || got == nil {
				t.Fatalf("CheckoutTask = (%v, %v)", got, err)
			}

			sched.submitAndUpdateTask(ctx, stale)
			checkStillCheckedOut(t, st)
		})
		t.Run(name+", persist step with the stale snapshot", func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			stale, _ := seedQueuedWorkerTask(t, st, service)
			if got, err := st.CheckoutTask(ctx, "wrk_1", "llm", model.RuntimeNone); err != nil || got == nil {
				t.Fatalf("CheckoutTask = (%v, %v)", got, err)
			}
			stale.ExternalID = stale.ID // what the worker executor's Submit returns
			now := time.Now().UTC()
			stale.DispatchedAt = &now

			sched.persistSubmitOutcome(ctx, stale, model.TaskStateQueued)
			checkStillCheckedOut(t, st)
		})
	}

	// Without a concurrent checkout the outcome is written as before.
	t.Run("no checkout", func(t *testing.T) {
		ctx := context.Background()
		sched, st := newServiceSched(t)
		task, _ := seedQueuedWorkerTask(t, st, false)
		sched.submitAndUpdateTask(ctx, task)
		got, err := st.GetTask(ctx, "task_race")
		if err != nil || got == nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.State != model.TaskStateQueued || got.ExternalID != "task_race" || got.DispatchedAt == nil {
			t.Errorf("task = %s on %q (dispatched %v), want QUEUED with the submit outcome", got.State, got.ExternalID, got.DispatchedAt)
		}
	})
}

// R3: a READY service step that already owns a non-terminal task (its step
// update after the task was created did not land) adopts that task instead
// of creating a second one.
func TestServiceStep_AdoptsExistingTask(t *testing.T) {
	for _, checkedOut := range []bool{false, true} {
		name := "task QUEUED"
		wantStep := model.StepStateDispatched
		if checkedOut {
			name, wantStep = "task RUNNING", model.StepStateRunning
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			subID := registerGraphWorkflowInputsAs(t, st, serviceStepCWL, map[string]any{"port": 8000}, nil, "alice")
			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			first := onlyTask(t, sched, subID)
			if checkedOut {
				now := time.Now().UTC()
				if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_svc", Name: "svc", Group: "llm", State: model.WorkerStateOnline,
					Runtime: model.RuntimeApptainer, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, ServiceMode: true}); err != nil {
					t.Fatal(err)
				}
				if got, err := st.CheckoutTask(ctx, "wrk_svc", "llm", model.RuntimeApptainer); err != nil || got == nil || got.ID != first.ID {
					t.Fatalf("CheckoutTask = (%v, %v), want %s", got, err, first.ID)
				}
			}

			// The step update was lost: the step is READY again.
			si := getStepInstancesByStep(t, st, subID)["s"]
			si.State = model.StepStateReady
			if err := st.UpdateStepInstance(ctx, si); err != nil {
				t.Fatal(err)
			}
			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}

			if again := onlyTask(t, sched, subID); again.ID != first.ID {
				t.Errorf("task = %s, want the first task %s", again.ID, first.ID)
			}
			if si := getStepInstancesByStep(t, st, subID)["s"]; si.State != wantStep {
				t.Errorf("step = %s %q, want %s", si.State, si.Error, wantStep)
			}
		})
	}
}

// dispatchServiceStep registers serviceStepCWL for alice and runs one tick:
// step s DISPATCHED with one QUEUED service task, which it returns.
func dispatchServiceStep(t *testing.T, sched *Loop, st store.Store) (string, *model.Task) {
	t.Helper()
	subID := registerGraphWorkflowInputsAs(t, st, serviceStepCWL, map[string]any{"port": 8000}, nil, "alice")
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := onlyTask(t, sched, subID)
	if task.State != model.TaskStateQueued || !task.IsService {
		t.Fatalf("task = %s (service %v), want a QUEUED service task", task.State, task.IsService)
	}
	return subID, task
}

// Second review, item 3: a READY service step whose tasks have all ended
// gets no new task: FAILED (SKIPPED when the task is SKIPPED) with the
// reason; a FAILED one then fails the submission.
func TestServiceStep_TerminalTaskNotStartedAgain(t *testing.T) {
	for _, taskState := range []model.TaskState{model.TaskStateFailed, model.TaskStateSkipped} {
		t.Run("task "+string(taskState), func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			subID, first := dispatchServiceStep(t, sched, st)

			ended, err := st.GetTask(ctx, first.ID)
			if err != nil || ended == nil {
				t.Fatalf("GetTask: (%v, %v)", ended, err)
			}
			now := time.Now().UTC()
			ended.State, ended.CompletedAt, ended.Stderr = taskState, &now, "model server exited"
			if err := st.UpdateTask(ctx, ended); err != nil {
				t.Fatal(err)
			}
			// The step update was lost: the step is READY again.
			si := getStepInstancesByStep(t, st, subID)["s"]
			si.State = model.StepStateReady
			if err := st.UpdateStepInstance(ctx, si); err != nil {
				t.Fatal(err)
			}
			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}

			if again := onlyTask(t, sched, subID); again.ID != first.ID || again.State != taskState {
				t.Errorf("task = %s %s, want only the first task %s, still %s", again.ID, again.State, first.ID, taskState)
			}
			wantStep := model.StepStateFailed
			if taskState == model.TaskStateSkipped {
				wantStep = model.StepStateSkipped
			}
			si = getStepInstancesByStep(t, st, subID)["s"]
			if si.State != wantStep || !strings.Contains(si.Error, model.ServiceNotRerun) {
				t.Errorf("step = %s %q, want %s with %q", si.State, si.Error, wantStep, model.ServiceNotRerun)
			}
			if taskState == model.TaskStateFailed {
				if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
					t.Errorf("submission = %s, want FAILED", final.State)
				}
				if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 1 {
					t.Errorf("tasks = %d, want still 1", len(tasks))
				}
			}
		})
	}
}

// Second review, item 2 (scheduler side): a service whose worker stopped it
// without a cancel is stored FAILED by /complete; its step and submission
// then fail, and it is never retried.
func TestServiceStep_StoppedWithoutCancelFailsSubmission(t *testing.T) {
	ctx := context.Background()
	sched, st := newServiceSched(t)
	subID, first := dispatchServiceStep(t, sched, st)
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_svc", Name: "svc", Group: "llm", State: model.WorkerStateOnline,
		Runtime: model.RuntimeApptainer, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, ServiceMode: true}); err != nil {
		t.Fatal(err)
	}
	running, err := st.CheckoutTask(ctx, "wrk_svc", "llm", model.RuntimeApptainer)
	if err != nil || running == nil || running.ID != first.ID {
		t.Fatalf("CheckoutTask = (%v, %v), want %s", running, err, first.ID)
	}
	// What handleWorkerTaskComplete stores for SKIPPED without a cancel.
	exit := -1
	running.State, running.ExitCode, running.CompletedAt = model.TaskStateFailed, &exit, &now
	running.Stderr = "shutting down\nservice stopped by its worker without a cancel"
	if applied, err := st.TerminalizeTask(ctx, running); err != nil || !applied {
		t.Fatalf("TerminalizeTask = (%v, %v)", applied, err)
	}

	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Fatalf("submission = %s, want FAILED", final.State)
	}
	if si := getStepInstancesByStep(t, st, subID)["s"]; si.State != model.StepStateFailed {
		t.Errorf("step = %s %q, want FAILED", si.State, si.Error)
	}
	if task := onlyTask(t, sched, subID); task.State != model.TaskStateFailed || task.RetryCount != 0 {
		t.Errorf("task = %s (retry %d), want FAILED, never retried", task.State, task.RetryCount)
	}
}

// refuseServiceStore refuses every CreateTask with ErrServiceNotWorker, as
// insertTask does for a service task whose executor is not worker.
type refuseServiceStore struct {
	store.Store
	calls int
}

func (s *refuseServiceStore) CreateTask(_ context.Context, task *model.Task) error {
	s.calls++
	return fmt.Errorf("task %s: %w (executor %q)", task.ID, store.ErrServiceNotWorker, task.ExecutorType)
}

// Second review, item 6: when the store refuses the task with
// ErrServiceNotWorker, the step fails permanently (no retry every tick) and
// the submission fails.
func TestDispatchStep_ServiceNotWorkerFailsStep(t *testing.T) {
	ctx := context.Background()
	sched, st := newServiceSched(t)
	subID := registerGraphWorkflowInputsAs(t, st, serviceStepCWL, map[string]any{"port": 8000}, nil, "alice")
	refuse := &refuseServiceStore{Store: st}
	sched.store = refuse

	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	if si.State != model.StepStateFailed || !strings.Contains(si.Error, store.ErrServiceNotWorker.Error()) {
		t.Fatalf("step = %s %q, want FAILED with %q", si.State, si.Error, store.ErrServiceNotWorker)
	}
	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Errorf("submission = %s, want FAILED", final.State)
	}
	if refuse.calls != 1 {
		t.Errorf("CreateTask calls = %d, want 1 (the step is not dispatched again)", refuse.calls)
	}
	if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none", tasks)
	}
}

// Second review, item 7a: the retry phase's refusal of a service keeps the
// stdout and stderr of its run and appends the reason to stderr.
func TestResubmitRetrying_ServiceKeepsOutput(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	seedDispatchedServiceTask(t, st, "task_svc_retry", model.TaskStateRetrying)
	tk, err := st.GetTask(ctx, "task_svc_retry")
	if err != nil || tk == nil {
		t.Fatalf("GetTask: (%v, %v)", tk, err)
	}
	tk.Stdout = "serving on :8000"
	if err := st.UpdateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTask(ctx, "task_svc_retry")
	if err != nil || got == nil {
		t.Fatalf("GetTask: (%v, %v)", got, err)
	}
	if want := "first attempt failed\n" + model.ServiceNotRerun; got.State != model.TaskStateFailed || got.Stderr != want {
		t.Errorf("task = %s stderr %q, want FAILED %q", got.State, got.Stderr, want)
	}
	if got.Stdout != "serving on :8000" {
		t.Errorf("stdout = %q, want kept", got.Stdout)
	}
}

// R7: a FAILED service task with retries left (a hand-made row) is never
// marked RETRYING, its row is never blanked and no attempt is archived; the
// step and the submission fail.
func TestFailedServiceTask_NeverRetried(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	subID := seedDispatchedServiceTask(t, st, "task_svc_failed", model.TaskStateFailed)

	for tick := 1; tick <= 5; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetTask(ctx, "task_svc_failed")
		if err != nil || got == nil {
			t.Fatalf("GetTask: %v", err)
		}
		if got.State != model.TaskStateFailed || got.Stderr != "first attempt failed" || got.CompletedAt == nil || got.RetryCount != 0 {
			t.Fatalf("tick %d: task = %s %q (completed %v, retry %d), want the FAILED row unchanged",
				tick, got.State, got.Stderr, got.CompletedAt, got.RetryCount)
		}
	}
	if attempts, err := st.ListTaskAttempts(ctx, "task_svc_failed"); err != nil || len(attempts) != 0 {
		t.Errorf("attempts = %+v, %v; want none archived", attempts, err)
	}
	if si := getStepInstancesByStep(t, st, subID)["s1"]; si.State != model.StepStateFailed {
		t.Errorf("step = %s, want FAILED", si.State)
	}
	if sub, err := st.GetSubmission(ctx, subID); err != nil || sub == nil || sub.State != model.SubmissionStateFailed {
		t.Errorf("submission = %+v, %v; want FAILED", sub, err)
	}
}
