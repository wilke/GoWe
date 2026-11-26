package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// A service identity (<name>@service.gowe) has no BV-BRC token and may never
// use the BV-BRC executor: the scheduler fails such a step at dispatch and
// refuses to resubmit such a task, before the executor is reached.

const svcBVBRCUser = "model-router@service.gowe"

// serviceBVBRCCWL routes its only step to the BV-BRC executor.
const serviceBVBRCCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: {}
    outputs: []
    steps:
      s:
        run: "#app-tool"
        hints:
          "gowe:Execution":
            executor: bvbrc
            bvbrc_app_id: Date
        in: {}
        out: [out]
  - id: app-tool
    class: CommandLineTool
    baseCommand: echo
    inputs: {}
    outputs:
      out:
        type: string
`

// bvbrcGuardExecutor stands in for the BV-BRC executor. Submit fails the
// test unless allow is set; submitted records the tasks that reached it.
type bvbrcGuardExecutor struct {
	t         *testing.T
	allow     bool
	submitted []string
}

func (e *bvbrcGuardExecutor) Type() model.ExecutorType { return model.ExecutorTypeBVBRC }

func (e *bvbrcGuardExecutor) Submit(_ context.Context, task *model.Task) (string, error) {
	e.submitted = append(e.submitted, task.ID)
	if !e.allow {
		e.t.Errorf("task %s reached the BV-BRC executor's Submit", task.ID)
		return "", errors.New("the BV-BRC executor must not be called")
	}
	return "job-" + task.ID, nil
}

func (e *bvbrcGuardExecutor) Status(context.Context, *model.Task) (model.TaskState, error) {
	return model.TaskStateQueued, nil
}

func (e *bvbrcGuardExecutor) Cancel(context.Context, *model.Task) error { return nil }

func (e *bvbrcGuardExecutor) Logs(context.Context, *model.Task) (string, string, error) {
	return "", "", nil
}

func TestDispatchStep_ServiceIdentityBVBRCDenied(t *testing.T) {
	t.Run("service identity: step failed, no task, executor never called", func(t *testing.T) {
		sched, st := testSetup(t)
		guard := &bvbrcGuardExecutor{t: t}
		sched.registry.Register(guard)
		subID := registerGraphWorkflowAs(t, st, serviceBVBRCCWL, nil, svcBVBRCUser)

		got := runToTerminal(t, sched, st, subID, 5)
		if got.State != model.SubmissionStateFailed {
			t.Fatalf("submission state = %s, want FAILED", got.State)
		}
		si := getStepInstancesByStep(t, st, subID)["s"]
		if si.State != model.StepStateFailed || si.Error != model.ServiceBVBRCDenied {
			t.Errorf("step = %s %q, want FAILED %q", si.State, si.Error, model.ServiceBVBRCDenied)
		}
		if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
			t.Errorf("no task may be created for the denied step, got %d", len(tasks))
		}
		if len(guard.submitted) != 0 {
			t.Errorf("Submit called for %v", guard.submitted)
		}
	})

	// Control: the same workflow of a normal user (with a token) reaches the
	// executor, so the case above is refused for the identity, not for the
	// setup.
	t.Run("normal user: dispatched to the executor", func(t *testing.T) {
		sched, st := testSetup(t)
		guard := &bvbrcGuardExecutor{t: t, allow: true}
		sched.registry.Register(guard)
		subID := registerBVBRCSubmission(t, st, "alice", credTestToken)
		if err := sched.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		tasks := hintsByStep(t, sched, subID)["s"]
		if len(tasks) != 1 || tasks[0].ExecutorType != model.ExecutorTypeBVBRC || len(guard.submitted) != 1 {
			si := getStepInstancesByStep(t, st, subID)["s"]
			t.Fatalf("tasks = %d, submitted %v (step %s %q); want one BV-BRC task submitted", len(tasks), guard.submitted, si.State, si.Error)
		}
	})
}

// A FAILED BV-BRC task of a service submission (one that exists from before
// the dispatch check, or from any other path) is not resubmitted by the retry
// phase: it fails permanently with the reason and never reaches Submit.
func TestResubmitRetrying_ServiceIdentityBVBRCDenied(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	guard := &bvbrcGuardExecutor{t: t}
	sched.registry.Register(guard)
	subID := registerGraphWorkflowAs(t, st, serviceBVBRCCWL, nil, svcBVBRCUser)

	// Seed the state a dispatched-and-failed attempt leaves: submission
	// RUNNING, step DISPATCHED, task FAILED with retries left.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil || sub == nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	sub.State = model.SubmissionStateRunning
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("UpdateSubmission: %v", err)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	si.State = model.StepStateDispatched
	if err := st.UpdateStepInstance(ctx, si); err != nil {
		t.Fatalf("UpdateStepInstance: %v", err)
	}
	now := time.Now().UTC()
	task := &model.Task{
		ID:             "task_svc_bvbrc",
		SubmissionID:   subID,
		StepID:         "s",
		StepInstanceID: si.ID,
		State:          model.TaskStateFailed,
		ExecutorType:   model.ExecutorTypeBVBRC,
		BVBRCAppID:     "Date",
		Inputs:         map[string]any{},
		Outputs:        map[string]any{},
		ScatterIndex:   -1,
		MaxRetries:     3,
		CreatedAt:      now,
		CompletedAt:    &now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Tick 1: markRetries (FAILED→RETRYING). Tick 2: resubmitRetrying.
	for i := 0; i < 2; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != model.TaskStateFailed || got.Stderr != model.ServiceBVBRCDenied {
		t.Fatalf("task = %s %q, want FAILED %q", got.State, got.Stderr, model.ServiceBVBRCDenied)
	}
	if got.RetryCount != got.MaxRetries {
		t.Errorf("retry budget not exhausted: retry %d / max %d", got.RetryCount, got.MaxRetries)
	}
	if len(guard.submitted) != 0 {
		t.Errorf("Submit called for %v", guard.submitted)
	}
	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Errorf("submission state = %s, want FAILED", final.State)
	}
	if len(guard.submitted) != 0 {
		t.Errorf("Submit called for %v after more ticks", guard.submitted)
	}
}

// registerBVBRCSubmission stores serviceBVBRCCWL with a PENDING submission
// by user carrying token ("" = none) and its WAITING step instances, and
// returns the submission ID.
func registerBVBRCSubmission(t *testing.T, st store.Store, user, token string) string {
	t.Helper()
	ctx := context.Background()
	p := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	graph, err := p.ParseGraph([]byte(serviceBVBRCCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "bvbrc-token-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = serviceBVBRCCWL
	wf.Class = "Workflow"
	wf.CreatedAt, wf.UpdatedAt = now, now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		SubmittedBy:  user,
		UserToken:    token,
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	for _, step := range wf.Steps {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", step.ID, err)
		}
	}
	return sub.ID
}

// A BV-BRC task of a submission without the submitter's token (here an
// anonymous one) fails once, permanently, before dispatch: the executor's
// Submit is never called, the task is not retried and no attempt is archived.
func TestDispatchStep_TokenlessBVBRCFailsPermanently(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	guard := &bvbrcGuardExecutor{t: t}
	sched.registry.Register(guard)
	subID := registerBVBRCSubmission(t, st, model.AnonymousUser.Username, "")

	got := runToTerminal(t, sched, st, subID, 10)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}
	// More ticks: nothing is retried.
	for i := 0; i < 3; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	si := getStepInstancesByStep(t, st, subID)["s"]
	if si.State != model.StepStateFailed || si.Error != model.BVBRCTokenRequired {
		t.Errorf("step = %s %q, want FAILED %q", si.State, si.Error, model.BVBRCTokenRequired)
	}
	tasks := hintsByStep(t, sched, subID)["s"]
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want exactly one", len(tasks))
	}
	task := tasks[0]
	if task.State != model.TaskStateFailed || task.Stderr != model.BVBRCTokenRequired {
		t.Errorf("task = %s %q, want FAILED %q", task.State, task.Stderr, model.BVBRCTokenRequired)
	}
	if task.RetryCount != 0 || task.MaxRetries != task.RetryCount {
		t.Errorf("retry %d / max %d, want 0 / 0 (no retries)", task.RetryCount, task.MaxRetries)
	}
	if attempts, err := st.ListTaskAttempts(ctx, task.ID); err != nil || len(attempts) != 0 {
		t.Errorf("attempts = %+v, %v; want none", attempts, err)
	}
	if len(guard.submitted) != 0 {
		t.Errorf("Submit called for %v", guard.submitted)
	}
}

// A FAILED BV-BRC task of a token-less (anonymous) submission with retries
// left is not resubmitted: the retry fails it permanently with the reason
// and never reaches Submit.
func TestResubmitRetrying_TokenlessBVBRCDenied(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	guard := &bvbrcGuardExecutor{t: t}
	sched.registry.Register(guard)
	subID := registerBVBRCSubmission(t, st, model.AnonymousUser.Username, "")

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil || sub == nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	sub.State = model.SubmissionStateRunning
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("UpdateSubmission: %v", err)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	si.State = model.StepStateDispatched
	if err := st.UpdateStepInstance(ctx, si); err != nil {
		t.Fatalf("UpdateStepInstance: %v", err)
	}
	now := time.Now().UTC()
	task := &model.Task{
		ID:             "task_anon_bvbrc",
		SubmissionID:   subID,
		StepID:         "s",
		StepInstanceID: si.ID,
		State:          model.TaskStateFailed,
		ExecutorType:   model.ExecutorTypeBVBRC,
		BVBRCAppID:     "Date",
		Inputs:         map[string]any{},
		Outputs:        map[string]any{},
		ScatterIndex:   -1,
		MaxRetries:     3,
		CreatedAt:      now,
		CompletedAt:    &now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Tick 1: markRetries (FAILED→RETRYING). Tick 2: resubmitRetrying.
	for i := 0; i < 2; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != model.TaskStateFailed || got.Stderr != model.BVBRCTokenRequired {
		t.Fatalf("task = %s %q, want FAILED %q", got.State, got.Stderr, model.BVBRCTokenRequired)
	}
	if got.RetryCount != got.MaxRetries {
		t.Errorf("retry budget not exhausted: retry %d / max %d", got.RetryCount, got.MaxRetries)
	}
	if final := runToTerminal(t, sched, st, subID, 5); final.State != model.SubmissionStateFailed {
		t.Errorf("submission state = %s, want FAILED", final.State)
	}
	if len(guard.submitted) != 0 {
		t.Errorf("Submit called for %v", guard.submitted)
	}
}

// Last line of defence: a token-less task that reaches the real BV-BRC
// executor is refused there, and the scheduler makes that refusal permanent
// (executor.ErrNoBVBRCToken), so the task is never retried.
func TestSubmitAndUpdateTask_NoBVBRCTokenIsPermanent(t *testing.T) {
	l, st, reg := pollSetup(t)
	ctx := context.Background()
	reg.Register(executor.NewBVBRCExecutor("", nil, slog.New(slog.NewTextHandler(io.Discard, nil))))

	_, subID := createPipeline(t, st, []model.Step{{ID: "s1", ToolRef: "#t"}}, map[string]any{}, 0)
	task := &model.Task{
		ID:           "task_no_bvbrc_token",
		SubmissionID: subID,
		StepID:       "s1",
		State:        model.TaskStatePending,
		ExecutorType: model.ExecutorTypeBVBRC,
		BVBRCAppID:   "Date",
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		MaxRetries:   3,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	l.submitAndUpdateTask(ctx, task)

	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != model.TaskStateFailed || !strings.Contains(got.Stderr, executor.ErrNoBVBRCToken.Error()) {
		t.Fatalf("task = %s %q, want FAILED with the executor's refusal", got.State, got.Stderr)
	}
	if got.MaxRetries != got.RetryCount {
		t.Errorf("retry %d / max %d: the refusal must be permanent", got.RetryCount, got.MaxRetries)
	}
}
