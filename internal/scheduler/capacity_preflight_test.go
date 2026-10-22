package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/pkg/model"
)

// The #324 incident shape: an esviritu-group fleet of 32000 MiB workers next
// to larger default-group workers, and a `--group esviritu` submission whose
// tool asks for ramMin: $(inputs.ram_mb) = 64000. The template task the
// preflight sees carries no group, so a group-blind capacity check would pass
// (a default worker is big enough), the task would be created with group
// esviritu and never be checked out: QUEUED forever with only log warnings
// under --stuck-task-action=warn (the production default).

const memExprCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      ram_mb: int
    outputs: []
    steps:
      big:
        run: "#mem-tool"
        in:
          ram_mb: ram_mb
        out: [out]
  - id: mem-tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      InlineJavascriptRequirement: {}
      ResourceRequirement:
        ramMin: $(inputs.ram_mb)
    inputs:
      ram_mb: int
    outputs:
      out:
        type: string
`

// memExprStepGroupCWL is memExprCWL with the group set by a step hint
// instead of the submission label.
const memExprStepGroupCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      ram_mb: int
    outputs: []
    steps:
      big:
        run: "#mem-tool"
        hints:
          "gowe:Execution":
            worker_group: esviritu
        in:
          ram_mb: ram_mb
        out: [out]
  - id: mem-tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      InlineJavascriptRequirement: {}
      ResourceRequirement:
        ramMin: $(inputs.ram_mb)
    inputs:
      ram_mb: int
    outputs:
      out:
        type: string
`

// memExprScatterCWL scatters the same tool over a list of ram_mb values.
const memExprScatterCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
    inputs:
      rams: int[]
    outputs: []
    steps:
      fan:
        run: "#mem-tool"
        scatter: ram_mb
        in:
          ram_mb: rams
        out: [out]
  - id: mem-tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      InlineJavascriptRequirement: {}
      ResourceRequirement:
        ramMin: $(inputs.ram_mb)
    inputs:
      ram_mb: int
    outputs:
      out:
        type: string
`

// preflightFleet registers the incident fleet: default workers that fit
// anything, esviritu workers capped at 32000 MiB. withEsviritu=false leaves
// the esviritu group with no online worker.
func preflightFleet(t *testing.T, sched *Loop, withEsviritu bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, group string, cpus int, mem int64) {
		w := &model.Worker{ID: id, Name: id, Group: group, State: model.WorkerStateOnline, Runtime: model.RuntimeNone,
			Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, MaxCPUs: ip(cpus), MaxMemMB: i64(mem)}
		if err := sched.store.CreateWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	mk("default-1", "default", 128, 512000)
	if withEsviritu {
		mk("esviritu-1", "esviritu", 16, 32000)
		mk("esviritu-2", "esviritu", 16, 32000)
	}
}

func preflightScheduler(t *testing.T) *Loop {
	t.Helper()
	sched, st := testSetup(t)
	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 1
	sched.config.MaxRetries = 0
	sched.registry.Register(executor.NewWorkerExecutor(st, sched.logger))
	return sched
}

const wantEsvirituGap = `no online worker in group "esviritu" has >= 64000 MiB (largest 32000 MiB)`

func TestDispatchStep_PreflightCapacity_UsesEffectiveGroup(t *testing.T) {
	cases := []struct {
		name   string
		cwl    string
		labels map[string]string
	}{
		{"submission --group label", memExprCWL, map[string]string{"worker_group": "esviritu"}},
		{"step hint worker_group", memExprStepGroupCWL, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched := preflightScheduler(t)
			preflightFleet(t, sched, true)
			subID := registerGraphWorkflow(t, sched.store, tc.cwl, map[string]any{"ram_mb": 64000}, tc.labels, "")

			got := runToTerminal(t, sched, sched.store, subID, 5)
			if got.State != model.SubmissionStateFailed {
				t.Fatalf("submission state = %s, want FAILED", got.State)
			}
			si := getStepInstancesByStep(t, sched.store, subID)["big"]
			if si.State != model.StepStateFailed || !strings.Contains(si.Error, wantEsvirituGap) {
				t.Errorf("big = %s %q, want FAILED with %q", si.State, si.Error, wantEsvirituGap)
			}
			if tasks := hintsByStep(t, sched, subID)["big"]; len(tasks) != 0 {
				t.Errorf("no task must be created for an unfit step, got %d", len(tasks))
			}
		})
	}
}

func TestDispatchStep_PreflightCapacity_FitsInGroup(t *testing.T) {
	sched := preflightScheduler(t)
	preflightFleet(t, sched, true)
	subID := registerGraphWorkflow(t, sched.store, memExprCWL, map[string]any{"ram_mb": 32000},
		map[string]string{"worker_group": "esviritu"}, "")
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks := hintsByStep(t, sched, subID)["big"]
	if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
		t.Fatalf("tasks = %+v, want one QUEUED task", tasks)
	}
	if h := tasks[0].RuntimeHints; h.WorkerGroup != "esviritu" || h.RamMB != 32000 {
		t.Errorf("hints = %+v, want group esviritu ram 32000", h)
	}
}

// With no esviritu worker online the preflight keeps its pre-#324 semantics:
// the step dispatches and the task waits in the queue for the group (workers
// roll; the stuck-task detector owns that case). It is not failed for
// capacity it cannot judge.
func TestDispatchStep_PreflightCapacity_GroupOfflineKeepsLegacySemantics(t *testing.T) {
	sched := preflightScheduler(t)
	preflightFleet(t, sched, false)
	subID := registerGraphWorkflow(t, sched.store, memExprCWL, map[string]any{"ram_mb": 64000},
		map[string]string{"worker_group": "esviritu"}, "")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	si := getStepInstancesByStep(t, sched.store, subID)["big"]
	if si.State != model.StepStateDispatched {
		t.Fatalf("big = %s %q, want DISPATCHED", si.State, si.Error)
	}
	tasks := hintsByStep(t, sched, subID)["big"]
	if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
		t.Fatalf("tasks = %+v, want one QUEUED task", tasks)
	}
	if h := tasks[0].RuntimeHints; h.WorkerGroup != "esviritu" || h.RamMB != 64000 {
		t.Errorf("hints = %+v, want group esviritu ram 64000", h)
	}
}

func TestDispatchScatterStep_PreflightCapacity_PerIteration(t *testing.T) {
	sched := preflightScheduler(t)
	preflightFleet(t, sched, true)
	subID := registerGraphWorkflow(t, sched.store, memExprScatterCWL,
		map[string]any{"rams": []any{1000, 64000}}, map[string]string{"worker_group": "esviritu"}, "")

	got := runToTerminal(t, sched, sched.store, subID, 5)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}
	si := getStepInstancesByStep(t, sched.store, subID)["fan"]
	if si.State != model.StepStateFailed || !strings.Contains(si.Error, "scatter iteration 1: ") || !strings.Contains(si.Error, wantEsvirituGap) {
		t.Errorf("fan = %s %q, want FAILED naming iteration 1 and %q", si.State, si.Error, wantEsvirituGap)
	}
	if tasks := hintsByStep(t, sched, subID)["fan"]; len(tasks) != 0 {
		t.Errorf("no iteration task must be created, got %d", len(tasks))
	}
}

func TestDispatchScatterStep_PreflightCapacity_AllFit(t *testing.T) {
	sched := preflightScheduler(t)
	preflightFleet(t, sched, true)
	subID := registerGraphWorkflow(t, sched.store, memExprScatterCWL,
		map[string]any{"rams": []any{1000, 32000}}, map[string]string{"worker_group": "esviritu"}, "")
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks := hintsByStep(t, sched, subID)["fan"]
	if len(tasks) != 2 {
		si := getStepInstancesByStep(t, sched.store, subID)["fan"]
		t.Fatalf("fan tasks = %d, want 2 (si %s: %s)", len(tasks), si.State, si.Error)
	}
	got := map[int]int64{}
	for _, tk := range tasks {
		if tk.State != model.TaskStateQueued || tk.RuntimeHints.WorkerGroup != "esviritu" {
			t.Errorf("task %d = %s group %q", tk.ScatterIndex, tk.State, tk.RuntimeHints.WorkerGroup)
		}
		got[tk.ScatterIndex] = tk.RuntimeHints.RamMB
	}
	if got[0] != 1000 || got[1] != 32000 {
		t.Errorf("per-iteration ram = %v, want {0:1000 1:32000}", got)
	}
}

// An untagged task is only served by default-group workers; a bigger worker
// in another group must not make the stuck detector consider it matchable.
func TestDetectStuckTasks_UntaggedJudgedAgainstDefaultGroup(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	sched.config.StuckTaskThreshold = 2
	sched.config.StuckTaskAction = "fail"
	now := time.Now().UTC()
	for _, w := range []*model.Worker{
		{ID: "d1", Name: "d1", Group: "default", MaxCPUs: ip(16), MaxMemMB: i64(32000)},
		{ID: "e1", Name: "e1", Group: "esviritu", MaxCPUs: ip(64), MaxMemMB: i64(128000)},
	} {
		w.State, w.Runtime, w.Labels, w.LastSeen, w.RegisteredAt = model.WorkerStateOnline, model.RuntimeNone, map[string]string{}, now, now
		if err := st.CreateWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	task := &model.Task{
		ID: "task_untagged", SubmissionID: "sub_test", StepID: "s", State: model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		RuntimeHints: &model.RuntimeHints{RamMB: 64000},
		Inputs:       map[string]any{}, Outputs: map[string]any{}, MaxRetries: 3, CreatedAt: now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sched.detectStuckTasks(ctx, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetTask(ctx, "task_untagged")
	if got.State != model.TaskStateFailed {
		t.Fatalf("state = %s, want FAILED", got.State)
	}
	if !strings.Contains(got.Stderr, `group "default" has >= 64000 MiB (largest 32000 MiB)`) {
		t.Errorf("stderr lacks default-group capacity reason:\n%s", got.Stderr)
	}
}

func TestResolveWorkerGroup(t *testing.T) {
	stepG := &model.Step{Hints: &model.StepHints{WorkerGroup: "step-g"}}
	subG := &model.Submission{Labels: map[string]string{"worker_group": "sub-g"}}
	tests := []struct {
		name  string
		hints *model.RuntimeHints
		step  *model.Step
		sub   *model.Submission
		want  string
	}{
		{"nothing", nil, &model.Step{}, &model.Submission{}, ""},
		{"tool hint wins", &model.RuntimeHints{WorkerGroup: "tool-g"}, stepG, subG, "tool-g"},
		{"step beats label", &model.RuntimeHints{}, stepG, subG, "step-g"},
		{"label fallback", nil, &model.Step{}, subG, "sub-g"},
		{"nil step and sub", nil, nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveWorkerGroup(tt.hints, tt.step, tt.sub); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// preflightHints parity with createTaskFromStep: the group the preflight
// checks is the group the task is created with.
func TestPreflightHints_MatchesCreatedTaskGroup(t *testing.T) {
	sched, _ := testSetup(t)
	tmp := &model.Task{Tool: map[string]any{"class": "CommandLineTool"}, RuntimeHints: &model.RuntimeHints{Cores: 4}}
	step := &model.Step{ID: "s", Hints: &model.StepHints{WorkerGroup: "esviritu"}}
	sub := &model.Submission{ID: "sub", Labels: map[string]string{"worker_group": "ignored"}}
	si := &model.StepInstance{ID: "si", SubmissionID: "sub", StepID: "s"}

	task := sched.createTaskFromStep(si, tmp, step, sub, model.ExecutorTypeWorker, -1)

	online := capsOf(capW("esviritu", ip(16), i64(32000)))
	h := sched.preflightHints(tmp, step, sub, online, cwltool.ResourceRequest{Cores: 4})
	if h.WorkerGroup != task.RuntimeHints.WorkerGroup || h.WorkerGroup != "esviritu" || h.Cores != 4 {
		t.Errorf("online group: preflight %+v vs task %+v", h, task.RuntimeHints)
	}

	offline := capsOf(capW("default", ip(16), i64(32000)))
	h = sched.preflightHints(tmp, step, sub, offline, cwltool.ResourceRequest{Cores: 4})
	if h.WorkerGroup != "" || h.Cores != 0 || h.RamMB != 0 {
		t.Errorf("offline group must fall back to the group-less, capacity-less check, got %+v", h)
	}

	untagged := sched.preflightHints(tmp, &model.Step{}, &model.Submission{}, offline, cwltool.ResourceRequest{Cores: 4})
	if untagged.WorkerGroup != "default" || untagged.Cores != 4 {
		t.Errorf("untagged step must be checked against the default group, got %+v", untagged)
	}
}
