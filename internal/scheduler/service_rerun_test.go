package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

// A service task (gowe:Service) is never started a second time by the
// scheduler: the retry phase fails it, it waits in the queue without being
// failed for lack of a service-mode worker, and job requirements are never
// merged into its tool.

// seedDispatchedServiceTask stores a pipeline with step s1 (submission
// RUNNING, step DISPATCHED) and one worker service task in state, with
// retries left (as if created by an older server), and returns the
// submission ID.
func seedDispatchedServiceTask(t *testing.T, st store.Store, id string, state model.TaskState) string {
	t.Helper()
	ctx := context.Background()
	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil || sub == nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	sub.State = model.SubmissionStateRunning
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("UpdateSubmission: %v", err)
	}
	si := getStepInstancesByStep(t, st, subID)["s1"]
	si.State = model.StepStateDispatched
	if err := st.UpdateStepInstance(ctx, si); err != nil {
		t.Fatalf("UpdateStepInstance: %v", err)
	}
	now := time.Now().UTC()
	task := &model.Task{
		ID:             id,
		SubmissionID:   subID,
		StepID:         "s1",
		StepInstanceID: si.ID,
		State:          state,
		ExecutorType:   model.ExecutorTypeWorker,
		Inputs:         map[string]any{},
		Outputs:        map[string]any{},
		ScatterIndex:   -1,
		MaxRetries:     3,
		CreatedAt:      now,
		CompletedAt:    &now,
		Stderr:         "first attempt failed",
		RuntimeHints: &model.RuntimeHints{
			WorkerGroup: "llm",
			Service:     &model.ServiceSpec{PortInput: "port"},
		},
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return subID
}

// A service task found RETRYING (or as a stranded SCHEDULED claim) is failed
// permanently by the retry phase instead of being resubmitted; the step and
// the submission then fail.
func TestResubmitRetrying_ServiceTaskFailed(t *testing.T) {
	for _, state := range []model.TaskState{model.TaskStateRetrying, model.TaskStateScheduled} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			sched, st := testSetup(t)
			queueOnlyWorkerExecutor(sched, st)
			subID := seedDispatchedServiceTask(t, st, "task_svc_retry", state)

			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetTask(ctx, "task_svc_retry")
			if err != nil || got == nil {
				t.Fatalf("GetTask: %v", err)
			}
			// The stderr of the run is kept, the reason appended on a new line.
			if wantStderr := "first attempt failed\n" + model.ServiceNotRerun; got.State != model.TaskStateFailed || got.Stderr != wantStderr {
				t.Fatalf("task = %s %q, want FAILED %q (never resubmitted)", got.State, got.Stderr, wantStderr)
			}
			if got.RetryCount != got.MaxRetries || got.ExternalID != "" {
				t.Errorf("retry %d / max %d, owner %q; want the budget exhausted, never checked out", got.RetryCount, got.MaxRetries, got.ExternalID)
			}
			// Failed before the attempt is archived or the row blanked.
			if got.CompletedAt == nil {
				t.Error("completed_at blanked")
			}
			if attempts, err := st.ListTaskAttempts(ctx, "task_svc_retry"); err != nil || len(attempts) != 0 {
				t.Errorf("attempts = %+v, %v; want none archived", attempts, err)
			}
			if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
				t.Errorf("submission = %s, want FAILED", final.State)
			}
			if si := getStepInstancesByStep(t, st, subID)["s1"]; si.State != model.StepStateFailed {
				t.Errorf("step = %s, want FAILED", si.State)
			}
		})
	}
}

// A service task marked FAILED by the store's heartbeat reconcile (not in
// its live worker's heartbeat) fails its step and its submission through the
// normal phases: no retry (advanceSteps), then finalize.
func TestReconciledService_FailsStepAndSubmission(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	subID := seedDispatchedServiceTask(t, st, "task_svc_gone", model.TaskStateRunning)
	got, err := st.GetTask(ctx, "task_svc_gone")
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	got.ExternalID = "wrk_gone"
	started := time.Now().UTC().Add(-time.Hour)
	got.StartedAt, got.CompletedAt, got.MaxRetries = &started, nil, 0
	if err := st.UpdateTask(ctx, got); err != nil {
		t.Fatal(err)
	}

	// Two heartbeats with running_tasks that omit it: the first marks it
	// missing, the second (the mark now older than the grace window, 0
	// here) fails it.
	if requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_gone", []string{}, time.Minute); err != nil || len(requeued) != 0 {
		t.Fatalf("ReconcileWorkerTasks = %v, %v; want nothing requeued", requeued, err)
	}
	if marked, _ := st.GetTask(ctx, "task_svc_gone"); marked == nil || marked.State != model.TaskStateRunning || marked.ServiceMissingAt == nil {
		t.Fatalf("after the first omission = %+v, want RUNNING and marked missing", marked)
	}
	time.Sleep(2 * time.Millisecond)
	if requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_gone", []string{}, 0); err != nil || len(requeued) != 0 {
		t.Fatalf("ReconcileWorkerTasks = %v, %v; want nothing requeued", requeued, err)
	}
	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Fatalf("submission = %s, want FAILED", final.State)
	}
	if si := getStepInstancesByStep(t, st, subID)["s1"]; si.State != model.StepStateFailed {
		t.Errorf("step = %s, want FAILED", si.State)
	}
	task, _ := st.GetTask(ctx, "task_svc_gone")
	if task == nil || task.State != model.TaskStateFailed || task.Stderr != store.ServiceMissingStderr || task.RetryCount != 0 {
		t.Errorf("task = %+v, want FAILED %q, never retried", task, store.ServiceMissingStderr)
	}
}

// serviceStepCWL is a service workflow routed to the worker executor in
// group llm.
const serviceStepCWL = `cwlVersion: v1.2
$graph:
  - id: srv
    class: CommandLineTool
    baseCommand: [serve]
    inputs:
      port:
        type: int
    outputs: []
    hints:
      DockerRequirement:
        dockerPull: vllm.sif
      gowe:Execution:
        executor: worker
        worker_group: llm
      gowe:Service:
        port_input: port
  - id: main
    class: Workflow
    inputs:
      port: int
    outputs: []
    steps:
      s:
        run: "#srv"
        in:
          port: port
        out: []
`

// A service step is not failed by the dispatch preflight when no worker is
// online: its task waits in the queue, and the stuck-task detection never
// fails it either.
func TestServiceTask_WaitsInQueue(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 1
	sched.registry.Register(executor.NewWorkerExecutor(st, sched.logger))
	subID := registerGraphWorkflowInputsAs(t, st, serviceStepCWL, map[string]any{"port": 8000}, nil, "alice")

	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	if si.State == model.StepStateFailed {
		t.Fatalf("step failed at dispatch: %q", si.Error)
	}
	tasks := hintsByStep(t, sched, subID)["s"]
	if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued || !tasks[0].IsService {
		t.Fatalf("tasks = %+v, want one QUEUED service task", tasks)
	}

	sched.config.StuckTaskThreshold = 2
	sched.config.StuckTaskAction = "fail"
	for i := 0; i < 4; i++ {
		if err := sched.detectStuckTasks(ctx, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := st.GetTask(ctx, tasks[0].ID); got == nil || got.State != model.TaskStateQueued {
		t.Errorf("service task after stuck detection = %+v, want still QUEUED", got)
	}
}

// A service-mode worker is no capacity for ordinary tasks: an ordinary task
// whose group has only a service-mode worker online is failed as stuck.
func TestDetectStuckTasks_ServiceWorkerIsNoBatchCapacity(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	sched.config.StuckTaskThreshold = 2
	sched.config.StuckTaskAction = "fail"
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_svc", Name: "svc", Group: "llm", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, ServiceMode: true}); err != nil {
		t.Fatal(err)
	}
	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
	if err := st.CreateTask(ctx, &model.Task{
		ID: "task_batch", SubmissionID: subID, StepID: "s1", State: model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker, Inputs: map[string]any{}, Outputs: map[string]any{},
		ScatterIndex: -1, CreatedAt: now, RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sched.detectStuckTasks(ctx, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetTask(ctx, "task_batch")
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != model.TaskStateFailed {
		t.Errorf("ordinary task = %s, want FAILED: the only worker of its group takes services only", got.State)
	}
	if caps := sched.workerCapabilities(); caps.OnlineCount != 0 || len(caps.Workers) != 0 || caps.Groups["llm"] != 0 {
		t.Errorf("caps = online %d, workers %d, llm %d; want 0, 0, 0", caps.OnlineCount, len(caps.Workers), caps.Groups["llm"])
	}
}

// serviceMergeCWL is a one-step workflow whose tool has no requirements of
// its own.
const serviceMergeCWL = `{
  "$graph": [
    {"id": "#main", "class": "Workflow", "inputs": [], "outputs": [],
     "steps": {"step": {"run": "#tool", "in": {}, "out": []}}},
    {"id": "#tool", "class": "CommandLineTool", "baseCommand": ["serve"],
     "inputs": [], "outputs": []}
  ]
}`

// hasRequirement reports whether the tool map declares class under
// requirements (map or list form).
func hasRequirement(tool map[string]any, class string) bool {
	switch reqs := tool["requirements"].(type) {
	case map[string]any:
		_, ok := reqs[class]
		return ok
	case []any:
		for _, r := range reqs {
			if m, ok := r.(map[string]any); ok && m["class"] == class {
				return true
			}
		}
	}
	return false
}

// populateToolAndJob never merges job cwl:requirements into a service's tool
// (an ordinary step still gets them), and the stamped tool hash is the digest
// toolhash.DigestForStep computes from the same job requirements
// (jobRequirements, as inheritedHint uses them).
func TestPopulateToolAndJob_ServiceSkipsJobRequirements(t *testing.T) {
	inputs := map[string]any{"cwl:requirements": []any{
		map[string]any{"class": "EnvVarRequirement", "envDef": []any{map[string]any{"envName": "X", "envValue": "1"}}},
	}}
	for _, tt := range []struct {
		name    string
		hints   *model.StepHints
		service bool
	}{
		{"service step", &model.StepHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}}, true},
		{"ordinary step", &model.StepHints{WorkerGroup: "llm"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sched, _ := testSetup(t)
			wf := &model.Workflow{ID: "wf1", RawCWL: serviceMergeCWL}
			step := &model.Step{ID: "step", ToolRef: "tool", Hints: tt.hints}
			task := &model.Task{}
			if err := sched.populateToolAndJob(task, step, wf, inputs, map[string]*model.Task{}); err != nil {
				t.Fatalf("populateToolAndJob: %v", err)
			}
			if got := hasRequirement(task.Tool, "EnvVarRequirement"); got == tt.service {
				t.Errorf("job EnvVarRequirement merged = %v, want %v", got, !tt.service)
			}
			if got := jobRequirements(step, inputs); (got == nil) != tt.service {
				t.Errorf("jobRequirements = %v, want nil only for the service", got)
			}
			res, err := toolhash.DigestForStep(wf.RawCWL, step.ID, jobRequirements(step, inputs))
			if err != nil {
				t.Fatalf("DigestForStep: %v", err)
			}
			if task.RuntimeHints == nil || task.RuntimeHints.ToolHash != res.Hash {
				t.Errorf("tool hash = %v, want DigestForStep's %s", task.RuntimeHints, res.Hash)
			}
		})
	}
}
