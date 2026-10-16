package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func newHinterLoop(t *testing.T) (*Loop, *bytes.Buffer) {
	t.Helper()
	sched, _ := testSetup(t)
	var buf bytes.Buffer
	sched.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return sched, &buf
}

func clToolMap(reqs, hints map[string]any) map[string]any {
	tool := map[string]any{
		"class":       "CommandLineTool",
		"baseCommand": "echo",
		"inputs":      map[string]any{"threads": map[string]any{"type": "int"}},
		"outputs":     map[string]any{},
	}
	if reqs != nil {
		tool["requirements"] = reqs
	}
	if hints != nil {
		tool["hints"] = hints
	}
	return tool
}

func rrReq(m map[string]any) map[string]any { return map[string]any{"ResourceRequirement": m} }

func TestResourceHinter_Apply(t *testing.T) {
	exprReqs := map[string]any{
		"InlineJavascriptRequirement": map[string]any{},
		"ResourceRequirement":         map[string]any{"coresMin": "$(inputs.threads)", "ramMin": 1000},
	}
	tests := []struct {
		name      string
		tool      map[string]any
		job       map[string]any
		wantNil   bool
		cores     int
		ram       int64
		wantWarns int
	}{
		{name: "nil tool", tool: nil, wantNil: true},
		{name: "not a CommandLineTool", tool: map[string]any{"class": "ExpressionTool"}, wantNil: true},
		{name: "no ResourceRequirement", tool: clToolMap(nil, nil), wantNil: true},
		{name: "static", tool: clToolMap(rrReq(map[string]any{"coresMin": 4, "ramMin": 2048}), nil), cores: 4, ram: 2048},
		{name: "expression", tool: clToolMap(exprReqs, nil), job: map[string]any{"threads": 16}, cores: 16, ram: 1000},
		{name: "hints section", tool: clToolMap(nil, rrReq(map[string]any{"coresMin": 3})), cores: 3},
		{name: "eval failure yields zero and one warn", tool: clToolMap(exprReqs, nil), job: map[string]any{}, cores: 0, ram: 1000, wantWarns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, buf := newHinterLoop(t)
			si := &model.StepInstance{ID: "si_1", StepID: "s"}
			task := &model.Task{StepID: "s", Tool: tt.tool, Job: tt.job}
			h := sched.newResourceHinter(si, task)
			if (h == nil) != tt.wantNil {
				t.Fatalf("hinter nil = %v, want %v", h == nil, tt.wantNil)
			}
			h.apply(task)
			h.apply(task) // second apply must not warn again
			if tt.wantNil {
				if task.RuntimeHints != nil {
					t.Errorf("no-op hinter set hints: %+v", task.RuntimeHints)
				}
				return
			}
			if task.RuntimeHints.Cores != tt.cores || task.RuntimeHints.RamMB != tt.ram {
				t.Errorf("got cores=%d ram=%d, want %d/%d", task.RuntimeHints.Cores, task.RuntimeHints.RamMB, tt.cores, tt.ram)
			}
			if n := strings.Count(buf.String(), "not evaluable at dispatch"); n != tt.wantWarns {
				t.Errorf("warn count = %d, want %d\n%s", n, tt.wantWarns, buf.String())
			}
		})
	}
}

func TestResourceHinter_ScatterCombosGetDistinctHints(t *testing.T) {
	sched, _ := newHinterLoop(t)
	tool := clToolMap(map[string]any{
		"InlineJavascriptRequirement": map[string]any{},
		"ResourceRequirement":         map[string]any{"coresMin": "$(inputs.threads)"},
	}, nil)
	shared := &model.RuntimeHints{WorkerGroup: "g"}
	tmpl := &model.Task{Tool: tool, Job: map[string]any{"threads": []any{4, 16}}, RuntimeHints: shared}
	h := sched.newResourceHinter(&model.StepInstance{ID: "si"}, tmpl)
	h.applyTemplate(tmpl)
	if tmpl.RuntimeHints.Cores != 0 {
		t.Errorf("template with array input should be 0, got %d", tmpl.RuntimeHints.Cores)
	}
	a := &model.Task{Tool: tool, Job: map[string]any{"threads": 4}, RuntimeHints: tmpl.RuntimeHints}
	b := &model.Task{Tool: tool, Job: map[string]any{"threads": 16}, RuntimeHints: tmpl.RuntimeHints}
	h.apply(a)
	h.apply(b)
	if a.RuntimeHints.Cores != 4 || b.RuntimeHints.Cores != 16 {
		t.Errorf("cores = %d/%d, want 4/16", a.RuntimeHints.Cores, b.RuntimeHints.Cores)
	}
	if a.RuntimeHints == b.RuntimeHints || a.RuntimeHints == tmpl.RuntimeHints {
		t.Error("tasks must not share a RuntimeHints pointer")
	}
	if a.RuntimeHints.WorkerGroup != "g" {
		t.Error("other hints must be preserved")
	}
}

const resourceExprCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
    inputs:
      threads: int
      many: int[]
    outputs: []
    steps:
      plain:
        run: "#tool"
        in:
          threads: threads
        out: [out]
      fanout:
        run: "#tool"
        scatter: threads
        in:
          threads: many
        out: [out]
      overridden:
        run: "#plain-tool"
        requirements:
          ResourceRequirement:
            coresMin: 7
        in: {}
        out: [out]
      toolwins:
        run: "#fixed-tool"
        requirements:
          ResourceRequirement:
            coresMin: 7
        in: {}
        out: [out]
  - id: tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      InlineJavascriptRequirement: {}
      ResourceRequirement:
        coresMin: $(inputs.threads)
        ramMin: 4096
    inputs:
      threads: int
    outputs:
      out:
        type: string
  - id: plain-tool
    class: CommandLineTool
    baseCommand: echo
    inputs: {}
    outputs:
      out:
        type: string
  - id: fixed-tool
    class: CommandLineTool
    baseCommand: echo
    requirements:
      ResourceRequirement:
        coresMin: 2
    inputs: {}
    outputs:
      out:
        type: string
`

// queueOnlyWorkerExecutor routes tasks to the worker executor so they stay
// QUEUED (no worker polls in these tests), with no preflight deferral.
func queueOnlyWorkerExecutor(sched *Loop, st store.Store) {
	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 0
	sched.registry.Register(executor.NewWorkerExecutor(st, sched.logger))
}

func hintsByStep(t *testing.T, sched *Loop, subID string) map[string][]*model.Task {
	t.Helper()
	tasks, err := sched.store.ListTasksBySubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	m := map[string][]*model.Task{}
	for _, tk := range tasks {
		m[tk.StepID] = append(m[tk.StepID], tk)
	}
	return m
}

func TestDispatchStep_ExpressionResourceHintsPersisted(t *testing.T) {
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	subID := registerGraphWorkflowWithSecrets(t, st, resourceExprCWL,
		map[string]any{"threads": 16, "many": []any{4, 16}}, nil)
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	byStep := hintsByStep(t, sched, subID)

	t.Run("expression resolved from job", func(t *testing.T) {
		tasks := byStep["plain"]
		if len(tasks) != 1 {
			t.Fatalf("plain tasks = %d, want 1", len(tasks))
		}
		h := tasks[0].RuntimeHints
		if h == nil || h.Cores != 16 || h.RamMB != 4096 {
			t.Errorf("hints = %+v, want cores=16 ram=4096", h)
		}
	})
	t.Run("step-level ResourceRequirement reaches hints", func(t *testing.T) {
		tasks := byStep["overridden"]
		if len(tasks) != 1 || tasks[0].RuntimeHints == nil || tasks[0].RuntimeHints.Cores != 7 {
			t.Fatalf("overridden hints = %+v", tasks)
		}
	})
	t.Run("tool ResourceRequirement beats step", func(t *testing.T) {
		tasks := byStep["toolwins"]
		if len(tasks) != 1 || tasks[0].RuntimeHints == nil || tasks[0].RuntimeHints.Cores != 2 {
			t.Fatalf("toolwins hints = %+v", tasks)
		}
	})
}

func TestDispatchScatterStep_PerIterationResourceHints(t *testing.T) {
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	subID := registerGraphWorkflowWithSecrets(t, st, resourceExprCWL,
		map[string]any{"threads": 1, "many": []any{4, 16}}, nil)
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	tasks := hintsByStep(t, sched, subID)["fanout"]
	if len(tasks) != 2 {
		si := getStepInstancesByStep(t, st, subID)["fanout"]
		t.Fatalf("fanout tasks = %d, want 2 (si %s: %s)", len(tasks), si.State, si.Error)
	}
	got := map[int]int{}
	for _, tk := range tasks {
		if tk.RuntimeHints == nil {
			t.Fatalf("task %d has no hints", tk.ScatterIndex)
		}
		got[tk.ScatterIndex] = tk.RuntimeHints.Cores
	}
	if got[0] != 4 || got[1] != 16 {
		t.Errorf("per-iteration cores = %v, want {0:4 1:16}", got)
	}
}
