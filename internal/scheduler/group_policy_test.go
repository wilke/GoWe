package scheduler

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Worker-group access at dispatch (#261).

// groupStepHintCWL routes its only step to group "gpu" with a step hint.
const groupStepHintCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: {}
    outputs: []
    steps:
      s:
        run: "#echo-tool"
        hints:
          "gowe:Execution":
            worker_group: gpu
        in: {}
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    inputs: {}
    outputs:
      out:
        type: string
`

// groupToolHintCWL routes its only step to group "gpu" with a hint on the
// tool document.
const groupToolHintCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: {}
    outputs: []
    steps:
      s:
        run: "#echo-tool"
        in: {}
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    hints:
      "gowe:Execution":
        worker_group: gpu
    inputs: {}
    outputs:
      out:
        type: string
`

// groupNoHintCWL carries no group: only the submission label can set one.
const groupNoHintCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: {}
    outputs: []
    steps:
      s:
        run: "#echo-tool"
        in: {}
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    inputs: {}
    outputs:
      out:
        type: string
`

// testPolicy is a worker-group policy as stored: default_deny and rules.
type testPolicy struct {
	defaultDeny bool
	rules       []model.WorkerGroupPolicy
}

// gpuPolicy restricts group "gpu" to alice and server admins.
var gpuPolicy = testPolicy{rules: []model.WorkerGroupPolicy{{Group: "gpu", Users: []string{"alice"}, Admins: true}}}

// storeTestPolicy writes tp to st and reloads p from st, as the admin API
// does after a change.
func storeTestPolicy(t *testing.T, st store.Store, p *groupacl.Policy, tp testPolicy) {
	t.Helper()
	ctx := context.Background()
	for i := range tp.rules {
		if err := st.UpsertWorkerGroupPolicy(ctx, &tp.rules[i]); err != nil {
			t.Fatalf("UpsertWorkerGroupPolicy: %v", err)
		}
	}
	if err := st.SetServerSetting(ctx, groupacl.DefaultDenyKey, strconv.FormatBool(tp.defaultDeny), "root"); err != nil {
		t.Fatalf("SetServerSetting: %v", err)
	}
	if err := p.Reload(ctx, st); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// loadTestGroupPolicy stores tp in st and returns a live policy loaded from
// it; "root" is the only server admin.
func loadTestGroupPolicy(t *testing.T, st store.Store, tp testPolicy) *groupacl.Policy {
	t.Helper()
	p := groupacl.New().WithAdmins(func(u string) bool { return u == "root" })
	storeTestPolicy(t, st, p, tp)
	return p
}

// registerGraphWorkflowAs is registerGraphWorkflow with a submitter.
func registerGraphWorkflowAs(t *testing.T, st store.Store, rawCWL string, labels map[string]string, user string) string {
	t.Helper()
	return registerGraphWorkflowInputsAs(t, st, rawCWL, map[string]any{}, labels, user)
}

// registerGraphWorkflowInputsAs is registerGraphWorkflowAs with submission
// inputs.
func registerGraphWorkflowInputsAs(t *testing.T, st store.Store, rawCWL string, inputs map[string]any, labels map[string]string, user string) string {
	t.Helper()
	ctx := context.Background()
	p := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	graph, err := p.ParseGraph([]byte(rawCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "group-policy-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = rawCWL
	wf.Class = "Workflow"
	wf.CreatedAt, wf.UpdatedAt = now, now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       labels,
		SubmittedBy:  user,
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

func TestDispatchStep_GroupPolicy(t *testing.T) {
	gpuLabel := map[string]string{"worker_group": "gpu"}
	tests := []struct {
		name       string
		cwl        string
		labels     map[string]string
		user       string
		nilPolicy  bool
		wantDenied bool
	}{
		{"step hint: unlisted submitter denied", groupStepHintCWL, nil, "mallory", false, true},
		{"step hint: listed submitter dispatched", groupStepHintCWL, nil, "alice", false, false},
		{"step hint: admin with admins true dispatched", groupStepHintCWL, nil, "root", false, false},
		{"step hint: anonymous denied", groupStepHintCWL, nil, "", false, true},
		{"label: unlisted submitter denied", groupNoHintCWL, gpuLabel, "mallory", false, true},
		{"label: listed submitter dispatched", groupNoHintCWL, gpuLabel, "alice", false, false},
		{"tool hint: unlisted submitter denied", groupToolHintCWL, nil, "mallory", false, true},
		{"tool hint: listed submitter dispatched", groupToolHintCWL, nil, "alice", false, false},
		{"default group not listed: open", groupNoHintCWL, nil, "mallory", false, false},
		{"nil policy: step hint dispatched", groupStepHintCWL, nil, "mallory", true, false},
		{"nil policy: label dispatched", groupNoHintCWL, gpuLabel, "mallory", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			queueOnlyWorkerExecutor(sched, st)
			if !tt.nilPolicy {
				sched.SetGroupPolicy(loadTestGroupPolicy(t, st, gpuPolicy))
			}
			subID := registerGraphWorkflowAs(t, st, tt.cwl, tt.labels, tt.user)

			if tt.wantDenied {
				got := runToTerminal(t, sched, st, subID, 5)
				if got.State != model.SubmissionStateFailed {
					t.Fatalf("submission state = %s, want FAILED", got.State)
				}
				reason := `submitter "` + tt.user + `" may not use worker group "gpu"`
				si := getStepInstancesByStep(t, st, subID)["s"]
				if si.State != model.StepStateFailed || si.Error != reason {
					t.Errorf("step = %s %q, want FAILED %q", si.State, si.Error, reason)
				}
				if got.Error == nil || !strings.Contains(got.Error.Message, reason) {
					t.Errorf("submission error = %+v, want it to carry %q", got.Error, reason)
				}
				if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
					t.Errorf("no task may be created for a denied step, got %d", len(tasks))
				}
				return
			}

			if err := sched.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			tasks := hintsByStep(t, sched, subID)["s"]
			if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
				si := getStepInstancesByStep(t, st, subID)["s"]
				t.Fatalf("tasks = %d (step %s %q), want one QUEUED task", len(tasks), si.State, si.Error)
			}
			wantGroup := ""
			if tt.cwl != groupNoHintCWL || tt.labels != nil {
				wantGroup = "gpu"
			}
			if got := tasks[0].RuntimeHints.WorkerGroup; got != wantGroup {
				t.Errorf("task group = %q, want %q", got, wantGroup)
			}
		})
	}
}

// A manager of a group may send tasks to it without being in its users; a
// manager of another group may not.
func TestDispatchStep_GroupPolicy_Manager(t *testing.T) {
	managed := testPolicy{rules: []model.WorkerGroupPolicy{
		{Group: "gpu", Users: []string{"alice"}, Managers: []string{"mgr"}},
		{Group: "cpu", Users: []string{}, Managers: []string{"other"}},
	}}
	t.Run("manager dispatched", func(t *testing.T) {
		sched, st := testSetup(t)
		queueOnlyWorkerExecutor(sched, st)
		sched.SetGroupPolicy(loadTestGroupPolicy(t, st, managed))
		subID := registerGraphWorkflowAs(t, st, groupStepHintCWL, nil, "mgr")
		if err := sched.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		tasks := hintsByStep(t, sched, subID)["s"]
		if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued || tasks[0].RuntimeHints.WorkerGroup != "gpu" {
			si := getStepInstancesByStep(t, st, subID)["s"]
			t.Fatalf("tasks = %+v (step %s %q), want one QUEUED task on gpu", tasks, si.State, si.Error)
		}
	})
	t.Run("manager of another group denied", func(t *testing.T) {
		sched, st := testSetup(t)
		queueOnlyWorkerExecutor(sched, st)
		sched.SetGroupPolicy(loadTestGroupPolicy(t, st, managed))
		subID := registerGraphWorkflowAs(t, st, groupStepHintCWL, nil, "other")
		assertStepGroupDenied(t, sched, st, subID, "other", "gpu")
	})
}

// A rule an admin adds after the submission was accepted applies at dispatch:
// the scheduler holds the same live handle the admin API reloads.
func TestDispatchStep_GroupPolicy_ChangedAtRuntime(t *testing.T) {
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	policy := loadTestGroupPolicy(t, st, testPolicy{})
	sched.SetGroupPolicy(policy)
	subID := registerGraphWorkflowAs(t, st, groupStepHintCWL, nil, "mallory")

	storeTestPolicy(t, st, policy, gpuPolicy)
	assertStepGroupDenied(t, sched, st, subID, "mallory", "gpu")
}

// Steps that do not run on the worker executor are not routed by group and
// are not checked.
func TestDispatchStep_GroupPolicy_LocalExecutorNotChecked(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	sched.SetGroupPolicy(loadTestGroupPolicy(t, st, gpuPolicy))
	subID := registerGraphWorkflowAs(t, st, groupStepHintCWL, nil, "mallory")
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	si := getStepInstancesByStep(t, st, subID)["s"]
	if strings.Contains(si.Error, "may not use worker group") {
		t.Errorf("local step must not be checked against the group policy: %q", si.Error)
	}
	if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 1 || tasks[0].ExecutorType != model.ExecutorTypeLocal {
		t.Errorf("want one local task, got %+v", tasks)
	}
}

// assertStepGroupDenied runs subID to its end and checks the dispatch denial:
// step "s" FAILED with the reason, submission FAILED, no task created.
func assertStepGroupDenied(t *testing.T, sched *Loop, st store.Store, subID, user, group string) {
	t.Helper()
	got := runToTerminal(t, sched, st, subID, 5)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}
	reason := `submitter "` + user + `" may not use worker group "` + group + `"`
	si := getStepInstancesByStep(t, st, subID)["s"]
	if si.State != model.StepStateFailed || si.Error != reason {
		t.Errorf("step = %s %q, want FAILED %q", si.State, si.Error, reason)
	}
	if tasks := hintsByStep(t, sched, subID)["s"]; len(tasks) != 0 {
		t.Errorf("no task may be created for a denied step, got %d", len(tasks))
	}
}

// groupScatterCWL scatters its only step over three items, routed to group
// "gpu" with a step hint.
const groupScatterCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
    inputs:
      items: string[]
    outputs: []
    steps:
      s:
        run: "#echo-tool"
        scatter: msg
        hints:
          "gowe:Execution":
            worker_group: gpu
        in:
          msg: items
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    inputs:
      msg:
        type: string
        inputBinding:
          position: 1
    outputs:
      out:
        type: string
`

// A scatter step is checked once, before any of its tasks exists (#261).
func TestDispatchStep_GroupPolicy_Scatter(t *testing.T) {
	inputs := map[string]any{"items": []any{"a", "b", "c"}}
	tests := []struct {
		name       string
		user       string
		wantDenied bool
	}{
		{"unlisted submitter denied", "mallory", true},
		{"listed submitter dispatched", "alice", false},
		{"admin with admins true dispatched", "root", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			queueOnlyWorkerExecutor(sched, st)
			sched.SetGroupPolicy(loadTestGroupPolicy(t, st, gpuPolicy))
			subID := registerGraphWorkflowInputsAs(t, st, groupScatterCWL, inputs, nil, tt.user)

			if tt.wantDenied {
				assertStepGroupDenied(t, sched, st, subID, tt.user, "gpu")
				return
			}
			if err := sched.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			tasks := hintsByStep(t, sched, subID)["s"]
			if len(tasks) != 3 {
				si := getStepInstancesByStep(t, st, subID)["s"]
				t.Fatalf("tasks = %d (step %s %q), want 3", len(tasks), si.State, si.Error)
			}
			for _, tk := range tasks {
				if tk.State != model.TaskStateQueued || tk.RuntimeHints == nil || tk.RuntimeHints.WorkerGroup != "gpu" {
					t.Errorf("task %s: state %s hints %+v, want QUEUED in group gpu", tk.ID, tk.State, tk.RuntimeHints)
				}
			}
		})
	}
}

// default_deny at dispatch: an unlisted group other than the default group is
// for server admins only; the default group stays open.
func TestDispatchStep_GroupPolicy_DefaultDeny(t *testing.T) {
	tests := []struct {
		name       string
		cwl        string
		user       string
		wantDenied bool
		wantGroup  string
	}{
		{"unlisted group: normal user denied", groupStepHintCWL, "mallory", true, "gpu"},
		{"unlisted group: admin dispatched", groupStepHintCWL, "root", false, "gpu"},
		{"unlisted group: anonymous denied", groupStepHintCWL, "anonymous", true, "gpu"},
		{"default group: normal user dispatched", groupNoHintCWL, "mallory", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			queueOnlyWorkerExecutor(sched, st)
			sched.SetGroupPolicy(loadTestGroupPolicy(t, st, testPolicy{defaultDeny: true}))
			subID := registerGraphWorkflowAs(t, st, tt.cwl, nil, tt.user)

			if tt.wantDenied {
				assertStepGroupDenied(t, sched, st, subID, tt.user, tt.wantGroup)
				return
			}
			if err := sched.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			tasks := hintsByStep(t, sched, subID)["s"]
			if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
				si := getStepInstancesByStep(t, st, subID)["s"]
				t.Fatalf("tasks = %d (step %s %q), want one QUEUED task", len(tasks), si.State, si.Error)
			}
			got := ""
			if tasks[0].RuntimeHints != nil {
				got = tasks[0].RuntimeHints.WorkerGroup
			}
			if got != tt.wantGroup {
				t.Errorf("task group = %q, want %q", got, tt.wantGroup)
			}
		})
	}
}

// A FAILED worker task the retry phase would put back in the queue is
// re-checked (#261): the task was created while the submitter was allowed
// (here: nothing stored yet), then an admin changes the policy at runtime
// (stored and reloaded into the handle the scheduler holds, as the admin API
// does), and the retry must not reach a worker once the policy denies its
// group.
func TestResubmitRetrying_GroupPolicy(t *testing.T) {
	defaultListed := testPolicy{rules: []model.WorkerGroupPolicy{{Group: "default", Users: []string{"alice"}}}}
	tests := []struct {
		name       string
		cwl        string
		policy     testPolicy
		user       string
		wantDenied bool
		wantGroup  string // in the reason
	}{
		{"restricted group: unlisted submitter refused", groupStepHintCWL, gpuPolicy, "mallory", true, "gpu"},
		{"restricted group: anonymous submitter refused", groupStepHintCWL, gpuPolicy, "anonymous", true, "gpu"},
		{"restricted group: listed submitter resubmitted", groupStepHintCWL, gpuPolicy, "alice", false, ""},
		{"restricted group: admin with admins true resubmitted", groupStepHintCWL, gpuPolicy, "root", false, ""},
		{"no group on the task: listed default group refused", groupNoHintCWL, defaultListed, "mallory", true, "default"},
		{"no group on the task: unlisted default group open", groupNoHintCWL, gpuPolicy, "mallory", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sched, st := testSetup(t)
			queueOnlyWorkerExecutor(sched, st)
			// The live policy: nothing stored yet, so everything is open.
			policy := loadTestGroupPolicy(t, st, testPolicy{})
			sched.SetGroupPolicy(policy)
			subID := registerGraphWorkflowAs(t, st, tt.cwl, nil, tt.user)
			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			tasks := hintsByStep(t, sched, subID)["s"]
			if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
				t.Fatalf("setup: tasks = %+v, want one QUEUED task", tasks)
			}
			task := tasks[0]
			if task.RetryCount >= task.MaxRetries {
				t.Fatalf("setup: task has no retries left (%d/%d)", task.RetryCount, task.MaxRetries)
			}
			// The attempt fails with retries left.
			if applied, err := st.CASTaskState(ctx, task.ID, model.TaskStateQueued, model.TaskStateFailed); err != nil || !applied {
				t.Fatalf("fail task: applied=%v err=%v", applied, err)
			}

			// The admin changes the policy; the scheduler's handle sees it.
			storeTestPolicy(t, st, policy, tt.policy)
			var logs bytes.Buffer
			sched.logger = slog.New(slog.NewTextHandler(&logs, nil))

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
			const warnMsg = "retry refused: worker group not allowed for submitter"

			if !tt.wantDenied {
				if got.State != model.TaskStateQueued || got.RetryCount != 1 {
					t.Errorf("task = %s retry %d (%q), want QUEUED after one retry", got.State, got.RetryCount, got.Stderr)
				}
				if strings.Contains(logs.String(), warnMsg) {
					t.Errorf("an allowed retry must not be refused:\n%s", logs.String())
				}
				return
			}

			reason := `submitter "` + tt.user + `" may not use worker group "` + tt.wantGroup + `"`
			if got.State != model.TaskStateFailed || got.Stderr != reason {
				t.Fatalf("task = %s %q, want FAILED %q", got.State, got.Stderr, reason)
			}
			if got.RetryCount != got.MaxRetries {
				t.Errorf("retry budget not exhausted: retry %d / max %d", got.RetryCount, got.MaxRetries)
			}
			// Permanent: further ticks neither retry the task nor re-queue it,
			// and the submission ends FAILED.
			sub := runToTerminal(t, sched, st, subID, 5)
			if sub.State != model.SubmissionStateFailed {
				t.Errorf("submission state = %s, want FAILED", sub.State)
			}
			if err := sched.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			again, err := st.GetTask(ctx, task.ID)
			if err != nil || again == nil {
				t.Fatalf("GetTask: %v", err)
			}
			if again.State != model.TaskStateFailed || again.RetryCount != got.RetryCount {
				t.Errorf("task after more ticks = %s retry %d, want FAILED retry %d", again.State, again.RetryCount, got.RetryCount)
			}
			if n := strings.Count(logs.String(), warnMsg); n != 1 {
				t.Errorf("WARN %q logged %d times, want 1:\n%s", warnMsg, n, logs.String())
			}
		})
	}
}
