package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func capW(group string, cpus *int, mem *int64) *model.Worker {
	return &model.Worker{State: model.WorkerStateOnline, Runtime: model.RuntimeNone, Group: group, MaxCPUs: cpus, MaxMemMB: mem}
}

func capsOf(workers ...*model.Worker) *WorkerCapabilities {
	caps := &WorkerCapabilities{
		Groups:          map[string]int{},
		Datasets:        map[string]int{},
		MaxCoresByGroup: map[string]int{},
		MaxMemMBByGroup: map[string]int64{},
		UncappedByGroup: map[string]int{},
	}
	for _, w := range workers {
		caps.OnlineCount++
		caps.Workers = append(caps.Workers, w)
		g := w.Group
		if g == "" {
			g = "default"
		}
		caps.Groups[g]++
		if w.MaxCPUs != nil && *w.MaxCPUs > caps.MaxCoresByGroup[g] {
			caps.MaxCoresByGroup[g] = *w.MaxCPUs
		}
		if w.MaxMemMB != nil && *w.MaxMemMB > caps.MaxMemMBByGroup[g] {
			caps.MaxMemMBByGroup[g] = *w.MaxMemMB
		}
		if w.MaxCPUs == nil || w.MaxMemMB == nil {
			caps.UncappedByGroup[g]++
		}
	}
	return caps
}

func i64(v int64) *int64 { return &v }
func ip(v int) *int      { return &v }

func TestCanMatchTask_Capacity(t *testing.T) {
	tests := []struct {
		name       string
		caps       *WorkerCapabilities
		hints      *model.RuntimeHints
		want       bool
		wantReason []string
	}{
		{"fits", capsOf(capW("", ip(16), i64(32000))), &model.RuntimeHints{Cores: 16, RamMB: 32000}, true, nil},
		{"cores exceed", capsOf(capW("esviritu", ip(16), i64(32000))),
			&model.RuntimeHints{WorkerGroup: "esviritu", Cores: 64},
			false, []string{`no online worker in group "esviritu" has >= 64 cores (largest 16)`}},
		{"mem exceed exact text", capsOf(capW("esviritu", ip(16), i64(32000))),
			&model.RuntimeHints{WorkerGroup: "esviritu", RamMB: 64000},
			false, []string{`no online worker in group "esviritu" has >= 64000 MiB (largest 32000 MiB)`}},
		{"both exceed", capsOf(capW("", ip(4), i64(8000))),
			&model.RuntimeHints{Cores: 8, RamMB: 16000},
			false, []string{`group "default" has >= 8 cores (largest 4)`, `has >= 16000 MiB (largest 8000 MiB)`}},
		{"legacy nil caps fit", capsOf(capW("", nil, nil)), &model.RuntimeHints{Cores: 4096, RamMB: 1 << 30}, true, nil},
		{"one legacy worker in group fits", capsOf(capW("", ip(2), i64(100)), capW("", nil, nil)),
			&model.RuntimeHints{Cores: 64}, true, nil},
		{"other group's caps irrelevant", capsOf(capW("big", ip(128), i64(1<<20)), capW("small", ip(4), i64(4000))),
			&model.RuntimeHints{WorkerGroup: "small", Cores: 16},
			false, []string{`group "small" has >= 16 cores (largest 4)`}},
		{"worker big enough in group", capsOf(capW("g", ip(4), nil), capW("g", ip(32), nil)),
			&model.RuntimeHints{WorkerGroup: "g", Cores: 16}, true, nil},
		{"no capacity needed: fast path", capsOf(capW("", ip(1), i64(1))), &model.RuntimeHints{}, true, nil},
		{"zero hints nil hints", capsOf(capW("", ip(1), i64(1))), nil, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := canMatchTask(tt.caps, tt.hints)
			if ok != tt.want {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tt.want)
			}
			for _, sub := range tt.wantReason {
				if !strings.Contains(reason, sub) {
					t.Errorf("reason %q missing %q", reason, sub)
				}
			}
		})
	}
}

func TestWorkerCapabilities_MaxByGroup(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, group string, cpus *int, mem *int64, state model.WorkerState) {
		w := &model.Worker{ID: id, Name: id, Group: group, State: state, Runtime: model.RuntimeNone,
			Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, MaxCPUs: cpus, MaxMemMB: mem}
		if err := st.CreateWorker(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	mk("w1", "esviritu", ip(16), i64(32000), model.WorkerStateOnline)
	mk("w2", "esviritu", ip(64), i64(16000), model.WorkerStateOnline)
	mk("w3", "esviritu", ip(256), i64(1<<20), model.WorkerStateOffline) // offline: ignored
	mk("w4", "", nil, nil, model.WorkerStateOnline)

	caps := sched.workerCapabilities()
	if caps.MaxCoresByGroup["esviritu"] != 64 || caps.MaxMemMBByGroup["esviritu"] != 32000 {
		t.Errorf("esviritu max = %d/%d, want 64/32000", caps.MaxCoresByGroup["esviritu"], caps.MaxMemMBByGroup["esviritu"])
	}
	if caps.UncappedByGroup["default"] != 1 || caps.UncappedByGroup["esviritu"] != 0 {
		t.Errorf("uncapped = %v", caps.UncappedByGroup)
	}
	if _, ok := caps.MaxCoresByGroup["default"]; ok {
		t.Errorf("default group has no reported caps, got %v", caps.MaxCoresByGroup)
	}
}

func TestRequirementKeyForTask_IncludesCapacity(t *testing.T) {
	a := requirementKeyForTask(&model.Task{RuntimeHints: &model.RuntimeHints{WorkerGroup: "g", Cores: 16, RamMB: 32000}})
	b := requirementKeyForTask(&model.Task{RuntimeHints: &model.RuntimeHints{WorkerGroup: "g", Cores: 4, RamMB: 32000}})
	if a.Cores != 16 || a.RamMB != 32000 {
		t.Errorf("key = %+v", a)
	}
	if a == b {
		t.Error("keys with different cores must differ")
	}
	h := hintsFromRequirementKey(a)
	if h.Cores != 16 || h.RamMB != 32000 || h.WorkerGroup != "g" {
		t.Errorf("round trip hints = %+v", h)
	}
}

func TestBuildStuckTaskError_Capacity(t *testing.T) {
	caps := capsOf(capW("esviritu", ip(16), i64(32000)))
	msg := buildStuckTaskError(taskRequirementKey{WorkerGroup: "esviritu", Cores: 64, RamMB: 64000},
		"no online worker in group \"esviritu\" has >= 64 cores (largest 16)", caps, 30)
	for _, want := range []string{"cores>=64 ram_mb>=64000", "esviritu: 16 cores, 32000 MiB", "largest 16"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestDetectStuckTasks_CapacityReason(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	sched.config.StuckTaskThreshold = 2
	sched.config.StuckTaskAction = "fail"
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{ID: "w1", Name: "w1", Group: "esviritu", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now,
		MaxCPUs: ip(16), MaxMemMB: i64(32000)}); err != nil {
		t.Fatal(err)
	}
	task := &model.Task{
		ID: "task_big", SubmissionID: "sub_test", StepID: "s", State: model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "esviritu", RamMB: 64000},
		Inputs:       map[string]any{}, Outputs: map[string]any{}, MaxRetries: 3, CreatedAt: now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	affected := map[string]bool{}
	for i := 0; i < 3; i++ {
		if err := sched.detectStuckTasks(ctx, affected); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetTask(ctx, "task_big")
	if got.State != model.TaskStateFailed {
		t.Fatalf("state = %s, want FAILED", got.State)
	}
	if !strings.Contains(got.Stderr, `has >= 64000 MiB (largest 32000 MiB)`) {
		t.Errorf("stderr lacks capacity reason:\n%s", got.Stderr)
	}
}

const capacityPreflightCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      threads: int
    outputs: []
    steps:
      big:
        run: "#big-tool"
        in:
          threads: threads
        out: [out]
  - id: big-tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      InlineJavascriptRequirement: {}
      ResourceRequirement:
        coresMin: $(inputs.threads)
    hints:
      "gowe:Execution":
        executor: worker
    inputs:
      threads: int
    outputs:
      out:
        type: string
`

func TestDispatchStep_PreflightCapacity_SetsStepInstanceError(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	sched.config.PreflightDeferralTicks = 1
	sched.config.MaxRetries = 0
	now := time.Now().UTC()
	// An untagged step is checked against the default group (the only one
	// whose workers can check it out).
	if err := st.CreateWorker(ctx, &model.Worker{ID: "w1", Name: "w1", Group: "default", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now,
		MaxCPUs: ip(16), MaxMemMB: i64(32000)}); err != nil {
		t.Fatal(err)
	}
	subID := registerGraphWorkflowWithSecrets(t, st, capacityPreflightCWL, map[string]any{"threads": 64}, nil)

	got := runToTerminal(t, sched, st, subID, 5)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}
	si := getStepInstancesByStep(t, st, subID)["big"]
	if si.State != model.StepStateFailed {
		t.Fatalf("big.State = %s, want FAILED", si.State)
	}
	if !strings.Contains(si.Error, "no capable worker") || !strings.Contains(si.Error, `has >= 64 cores (largest 16)`) {
		t.Errorf("si.Error = %q, want capacity reason", si.Error)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "cores (largest 16)") {
		t.Errorf("submission error = %+v, want capacity reason surfaced", got.Error)
	}
}
