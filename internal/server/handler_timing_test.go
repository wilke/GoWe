package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// timingBase is the fixed anchor every hand-set timestamp derives from. The
// store persists caller-set stamps verbatim, so durations computed from these
// are deterministic; only now-based values (waiting so far) need lower-bound
// assertions.
var timingBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func at(secs int) time.Time      { return timingBase.Add(time.Duration(secs) * time.Second) }
func atp(secs int) *time.Time    { t := at(secs); return &t }
func almostEq(a, b float64) bool { return math.Abs(a-b) < 0.0011 }

// seedWorkflow stores a minimal workflow definition with the given steps.
func seedTimingWorkflow(t *testing.T, st store.Store, id string, steps []model.Step) {
	t.Helper()
	err := st.CreateWorkflow(context.Background(), &model.Workflow{
		ID: id, Name: id, Class: "Workflow", Steps: steps,
		CreatedAt: timingBase, UpdatedAt: timingBase,
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
}

func seedTimingSub(t *testing.T, st store.Store, sub *model.Submission) {
	t.Helper()
	if sub.Inputs == nil {
		sub.Inputs = map[string]any{}
	}
	if sub.SubmittedBy == "" {
		// Anonymous test requests are authorized only for their own rows.
		sub.SubmittedBy = model.AnonymousUser.Username
	}
	if err := st.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("seed submission %s: %v", sub.ID, err)
	}
}

func seedTimingStep(t *testing.T, st store.Store, si *model.StepInstance) {
	t.Helper()
	if si.Outputs == nil {
		si.Outputs = map[string]any{}
	}
	if err := st.CreateStepInstance(context.Background(), si); err != nil {
		t.Fatalf("seed step instance %s: %v", si.ID, err)
	}
}

func seedTimingTask(t *testing.T, st store.Store, task *model.Task) {
	t.Helper()
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task %s: %v", task.ID, err)
	}
}

func getTiming(t *testing.T, srv *Server, id, query string) *timingReport {
	t.Helper()
	env := doGet(t, srv, "/api/v1/submissions/"+id+"/timing"+query)
	var rep timingReport
	if err := json.Unmarshal(env.Data, &rep); err != nil {
		t.Fatalf("unmarshal timing report: %v", err)
	}
	return &rep
}

func findTaskRow(t *testing.T, rep *timingReport, id string) *taskTiming {
	t.Helper()
	for i := range rep.Tasks {
		if rep.Tasks[i].TaskID == id {
			return &rep.Tasks[i]
		}
	}
	t.Fatalf("task row %s not found", id)
	return nil
}

func findStepRow(t *testing.T, rep *timingReport, stepID string) *stepTiming {
	t.Helper()
	for i := range rep.Steps {
		if rep.Steps[i].StepID == stepID {
			return &rep.Steps[i]
		}
	}
	t.Fatalf("step row %s not found", stepID)
	return nil
}

// TestTimingPerStateRules exercises one task per trust-rule row of the plan's
// per-state table.
func TestTimingPerStateRules(t *testing.T) {
	srv, st := testServerWithStore()

	steps := []model.Step{}
	for _, id := range []string{"s_success", "s_failed", "s_running", "s_queued_worker",
		"s_cancelled", "s_cancelled_started", "s_synthetic", "s_retrying", "s_scheduled", "s_failed_submit", "s_proxy"} {
		steps = append(steps, model.Step{ID: id})
	}
	seedTimingWorkflow(t, st, "wf_rules", steps)

	subID := "sub_rules"
	seedTimingSub(t, st, &model.Submission{
		ID: subID, WorkflowID: "wf_rules", WorkflowName: "wf_rules",
		State: model.SubmissionStateRunning, CreatedAt: timingBase,
	})

	mkTask := func(id, stepID string, exec model.ExecutorType, state model.TaskState,
		created time.Time, started, completed *time.Time) *model.Task {
		siID := "si_" + id
		seedTimingStep(t, st, &model.StepInstance{
			ID: siID, SubmissionID: subID, StepID: stepID,
			State: model.StepStateRunning, CreatedAt: timingBase,
		})
		return &model.Task{
			ID: id, SubmissionID: subID, StepID: stepID, StepInstanceID: siID,
			State: state, ExecutorType: exec, ScatterIndex: -1,
			CreatedAt: created, StartedAt: started, CompletedAt: completed,
		}
	}

	// SUCCESS: queue = started − created, run = completed − started.
	seedTimingTask(t, st, mkTask("t_success", "s_success", model.ExecutorTypeLocal,
		model.TaskStateSuccess, at(0), atp(10), atp(40)))
	// FAILED with both stamps: run present.
	seedTimingTask(t, st, mkTask("t_failed", "s_failed", model.ExecutorTypeLocal,
		model.TaskStateFailed, at(0), atp(10), atp(40)))
	// RUNNING: queue from trusted started_at, no run yet.
	seedTimingTask(t, st, mkTask("t_running", "s_running", model.ExecutorTypeLocal,
		model.TaskStateRunning, at(0), atp(5), nil))
	// QUEUED worker task with a stale dispatch-time started_at: NEVER trusted.
	seedTimingTask(t, st, mkTask("t_queued_worker", "s_queued_worker", model.ExecutorTypeWorker,
		model.TaskStateQueued, at(0), atp(1), nil))
	// SKIPPED, never started: kind cancelled, queue = completed − created.
	seedTimingTask(t, st, mkTask("t_cancelled", "s_cancelled", model.ExecutorTypeLocal,
		model.TaskStateSkipped, at(0), nil, atp(20)))
	// SKIPPED after it started: no queue, no run.
	seedTimingTask(t, st, mkTask("t_cancelled_started", "s_cancelled_started", model.ExecutorTypeLocal,
		model.TaskStateSkipped, at(0), atp(5), atp(20)))
	// When-skip synthetic: SUCCESS with nil started_at (subworkflow executor).
	seedTimingTask(t, st, mkTask("t_synthetic", "s_synthetic", model.ExecutorTypeSubworkflow,
		model.TaskStateSuccess, at(0), nil, atp(0)))
	// RETRYING with the failed attempt's stale stamps.
	retrying := mkTask("t_retrying", "s_retrying", model.ExecutorTypeLocal,
		model.TaskStateRetrying, at(0), atp(10), atp(20))
	retrying.RetryCount = 2
	seedTimingTask(t, st, retrying)
	// SCHEDULED between attempts: same rule row as RETRYING (last window).
	scheduled := mkTask("t_scheduled", "s_scheduled", model.ExecutorTypeLocal,
		model.TaskStateScheduled, at(0), atp(10), atp(25))
	scheduled.RetryCount = 1
	seedTimingTask(t, st, scheduled)
	// FAILED at submit (no executor): completed set, started never stamped.
	seedTimingTask(t, st, mkTask("t_failed_submit", "s_failed_submit", model.ExecutorTypeLocal,
		model.TaskStateFailed, at(0), nil, atp(15)))
	// Sub-workflow proxy: run_s = child wall once the child is terminal.
	seedTimingTask(t, st, mkTask("t_proxy", "s_proxy", model.ExecutorTypeSubworkflow,
		model.TaskStateSuccess, at(0), atp(0), atp(60)))
	seedTimingSub(t, st, &model.Submission{
		ID: "sub_child_rules", WorkflowID: "wf_rules", WorkflowName: "wf_rules",
		State: model.SubmissionStateCompleted, ParentTaskID: "t_proxy",
		CreatedAt: at(1), CompletedAt: atp(50),
	})

	rep := getTiming(t, srv, subID, "")

	assertSecs := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s: got nil, want %v", name, want)
		}
		if !almostEq(*got, want) {
			t.Errorf("%s: got %v, want %v", name, *got, want)
		}
	}
	assertNil := func(name string, got *float64) {
		t.Helper()
		if got != nil {
			t.Errorf("%s: got %v, want nil", name, *got)
		}
	}

	t.Run("success", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_success")
		if r.Kind != timingKindTask {
			t.Errorf("kind = %q, want task", r.Kind)
		}
		assertSecs("queue_s", r.QueueS, 10)
		assertSecs("run_s", r.RunS, 30)
	})

	t.Run("failed", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_failed")
		assertSecs("queue_s", r.QueueS, 10)
		assertSecs("run_s", r.RunS, 30)
	})

	t.Run("running", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_running")
		assertSecs("queue_s", r.QueueS, 5)
		assertNil("run_s", r.RunS)
	})

	t.Run("queued worker never trusts started_at", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_queued_worker")
		assertNil("run_s", r.RunS)
		if r.QueueS == nil {
			t.Fatal("queue_s: got nil, want waiting-so-far value")
		}
		// now − created is far larger than the 1s the stale stamp implies.
		if *r.QueueS < 60 {
			t.Errorf("queue_s = %v, want now-based waiting (not the stale stamp)", *r.QueueS)
		}
		if r.StartedAt == nil {
			t.Error("raw started_at should still be returned")
		}
	})

	t.Run("cancelled never started", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_cancelled")
		if r.Kind != timingKindCancelled {
			t.Errorf("kind = %q, want cancelled", r.Kind)
		}
		assertSecs("queue_s", r.QueueS, 20)
		assertNil("run_s", r.RunS)
	})

	t.Run("cancelled after start", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_cancelled_started")
		if r.Kind != timingKindCancelled {
			t.Errorf("kind = %q, want cancelled", r.Kind)
		}
		assertNil("queue_s", r.QueueS)
		assertNil("run_s", r.RunS)
	})

	t.Run("when-skip synthetic", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_synthetic")
		if r.Kind != timingKindSkippedIteration {
			t.Errorf("kind = %q, want skipped-iteration", r.Kind)
		}
		assertNil("queue_s", r.QueueS)
		assertNil("run_s", r.RunS)
	})

	t.Run("retrying reports last window", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_retrying")
		if !r.Retrying {
			t.Error("retrying flag not set")
		}
		if r.RetryCount != 2 {
			t.Errorf("retry_count = %d, want 2", r.RetryCount)
		}
		assertSecs("run_s (last attempt)", r.RunS, 10)
		if r.QueueS == nil {
			t.Error("queue_s (since dispatch) missing")
		}
	})

	t.Run("scheduled between attempts reports last window", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_scheduled")
		if !r.Retrying {
			t.Error("retrying flag not set")
		}
		assertSecs("run_s (last attempt)", r.RunS, 15)
		if r.QueueS == nil {
			t.Error("queue_s (since dispatch) missing")
		}
	})

	t.Run("failed at submit", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_failed_submit")
		if r.Kind != timingKindTask {
			t.Errorf("kind = %q, want task", r.Kind)
		}
		assertSecs("queue_s", r.QueueS, 15)
		assertNil("run_s", r.RunS)
	})

	t.Run("subworkflow proxy runs over child wall", func(t *testing.T) {
		r := findTaskRow(t, rep, "t_proxy")
		if r.Kind != timingKindSubworkflow {
			t.Errorf("kind = %q, want subworkflow", r.Kind)
		}
		if r.ChildSubmissionID != "sub_child_rules" {
			t.Errorf("child_submission_id = %q, want sub_child_rules", r.ChildSubmissionID)
		}
		// Child wall 49s, not the proxy's own 60s window.
		assertSecs("run_s", r.RunS, 49)
	})

	t.Run("aggregates exclude skipped iterations", func(t *testing.T) {
		// compute = 30 (success) + 30 (failed) + 10 (retrying last window)
		//         + 15 (scheduled last window) + 49 (proxy child wall).
		// The synthetic contributes nothing.
		if !almostEq(rep.Submission.ComputeS, 134) {
			t.Errorf("compute_s = %v, want 134", rep.Submission.ComputeS)
		}
		if rep.Submission.Counts.Total != 11 {
			t.Errorf("counts.total = %d, want 11", rep.Submission.Counts.Total)
		}
		if rep.Submission.SchedulingS == nil || !almostEq(*rep.Submission.SchedulingS, 0) {
			t.Errorf("scheduling_s = %v, want 0", rep.Submission.SchedulingS)
		}
	})
}

// TestTimingCriticalPath verifies the diamond-DAG longest path over hand-set
// timestamps, plus step wall/fan-in aggregates.
func TestTimingCriticalPath(t *testing.T) {
	srv, st := testServerWithStore()

	seedTimingWorkflow(t, st, "wf_diamond", []model.Step{
		{ID: "A"},
		{ID: "B", DependsOn: []string{"A"}},
		{ID: "C", DependsOn: []string{"A"}},
		{ID: "D", DependsOn: []string{"B", "C"}},
	})

	subID := "sub_diamond"
	seedTimingSub(t, st, &model.Submission{
		ID: subID, WorkflowID: "wf_diamond", WorkflowName: "wf_diamond",
		State:     model.SubmissionStateCompleted,
		CreatedAt: timingBase.Add(-10 * time.Second), CompletedAt: atp(45),
	})

	// Step intervals: A 0→10 (10s), B 10→30 (20s), C 10→15 (5s), D 30→40 (10s).
	// Critical path A→B→D = 40s.
	mk := func(stepID string, createdS, startedS, taskDoneS, siDoneS int) {
		siID := "si_" + stepID
		seedTimingStep(t, st, &model.StepInstance{
			ID: siID, SubmissionID: subID, StepID: stepID,
			State: model.StepStateCompleted, CreatedAt: timingBase.Add(-10 * time.Second),
			CompletedAt: atp(siDoneS),
		})
		seedTimingTask(t, st, &model.Task{
			ID: "t_" + stepID, SubmissionID: subID, StepID: stepID, StepInstanceID: siID,
			State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal, ScatterIndex: -1,
			CreatedAt: at(createdS), StartedAt: atp(startedS), CompletedAt: atp(taskDoneS),
		})
	}
	mk("A", 0, 1, 8, 10)
	mk("B", 10, 11, 28, 30)
	mk("C", 10, 11, 14, 15)
	mk("D", 30, 31, 39, 40)

	rep := getTiming(t, srv, subID, "")

	if rep.Submission.CriticalPathS == nil || !almostEq(*rep.Submission.CriticalPathS, 40) {
		t.Fatalf("critical_path_s = %v, want 40", rep.Submission.CriticalPathS)
	}
	if rep.Submission.WallS == nil || !almostEq(*rep.Submission.WallS, 55) {
		t.Errorf("wall_s = %v, want 55", rep.Submission.WallS)
	}
	if rep.Submission.SchedulingS == nil || !almostEq(*rep.Submission.SchedulingS, 10) {
		t.Errorf("scheduling_s = %v, want 10", rep.Submission.SchedulingS)
	}

	a := findStepRow(t, rep, "A")
	if a.WallS == nil || !almostEq(*a.WallS, 10) {
		t.Errorf("step A wall_s = %v, want 10", a.WallS)
	}
	if a.FanInS == nil || !almostEq(*a.FanInS, 2) {
		t.Errorf("step A fan_in_s = %v, want 2", a.FanInS)
	}
	if a.MaxRunS == nil || !almostEq(*a.MaxRunS, 7) {
		t.Errorf("step A max_run_s = %v, want 7", a.MaxRunS)
	}
	if a.Inline {
		t.Error("step A should not be inline")
	}
}

// TestTimingInlineStep covers zero-task steps: wall from the step instance's
// own stamps, flagged inline.
func TestTimingInlineStep(t *testing.T) {
	srv, st := testServerWithStore()

	seedTimingWorkflow(t, st, "wf_inline", []model.Step{{ID: "expr"}, {ID: "waiting"}})
	subID := "sub_inline"
	seedTimingSub(t, st, &model.Submission{
		ID: subID, WorkflowID: "wf_inline", WorkflowName: "wf_inline",
		State: model.SubmissionStateRunning, CreatedAt: timingBase,
	})
	seedTimingStep(t, st, &model.StepInstance{
		ID: "si_expr", SubmissionID: subID, StepID: "expr",
		State: model.StepStateCompleted, CreatedAt: at(0), CompletedAt: atp(12),
	})
	seedTimingStep(t, st, &model.StepInstance{
		ID: "si_waiting", SubmissionID: subID, StepID: "waiting",
		State: model.StepStateWaiting, CreatedAt: at(0),
	})

	rep := getTiming(t, srv, subID, "")

	expr := findStepRow(t, rep, "expr")
	if !expr.Inline {
		t.Error("expr step should be inline")
	}
	if expr.WallS == nil || !almostEq(*expr.WallS, 12) {
		t.Errorf("expr wall_s = %v, want 12", expr.WallS)
	}

	waiting := findStepRow(t, rep, "waiting")
	if !waiting.Inline {
		t.Error("waiting step should be inline")
	}
	if waiting.WallS != nil {
		t.Errorf("waiting wall_s = %v, want nil", *waiting.WallS)
	}

	// Zero tasks: scheduling_s absent, nothing crashes.
	if rep.Submission.SchedulingS != nil {
		t.Errorf("scheduling_s = %v, want nil (no tasks)", *rep.Submission.SchedulingS)
	}
}

// TestTimingProjection proves the S4 projection: task rows carry Tool, Job
// and stdout/stderr in the store, and none of it may ship through /timing.
func TestTimingProjection(t *testing.T) {
	srv, st := testServerWithStore()

	seedTimingWorkflow(t, st, "wf_proj", []model.Step{{ID: "s1"}})
	subID := "sub_proj"
	seedTimingSub(t, st, &model.Submission{
		ID: subID, WorkflowID: "wf_proj", WorkflowName: "wf_proj",
		State: model.SubmissionStateCompleted, CreatedAt: timingBase, CompletedAt: atp(60),
	})
	seedTimingStep(t, st, &model.StepInstance{
		ID: "si_proj", SubmissionID: subID, StepID: "s1",
		State: model.StepStateCompleted, CreatedAt: at(0), CompletedAt: atp(50),
	})
	seedTimingTask(t, st, &model.Task{
		ID: "t_proj", SubmissionID: subID, StepID: "s1", StepInstanceID: "si_proj",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal, ScatterIndex: -1,
		Tool:      map[string]any{"class": "CommandLineTool", "baseCommand": "echo"},
		Job:       map[string]any{"message": "SEKRIT-INPUT"},
		Inputs:    map[string]any{"message": "SEKRIT-INPUT"},
		Outputs:   map[string]any{"out": "SEKRIT-OUTPUT"},
		Stdout:    "SEKRIT-STDOUT",
		Stderr:    "SEKRIT-STDERR",
		CreatedAt: at(0), StartedAt: atp(1), CompletedAt: atp(40),
	})

	req := httptest.NewRequest("GET", "/api/v1/submissions/"+subID+"/timing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{`"tool"`, `"job"`, `"stdout"`, `"stderr"`, `"inputs"`, `"outputs"`, "SEKRIT"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaks %s: %s", forbidden, body)
		}
	}
	// The timing fields themselves must be present.
	for _, required := range []string{`"queue_s"`, `"run_s"`, `"critical_path_s"`, `"created_at"`} {
		if !strings.Contains(body, required) {
			t.Errorf("response missing %s", required)
		}
	}
}

// TestTimingIncludeChildren verifies ?include_children recursion into
// sub-workflow child submissions.
func TestTimingIncludeChildren(t *testing.T) {
	srv, st := testServerWithStore()

	seedTimingWorkflow(t, st, "wf_parent", []model.Step{{ID: "subwf"}})
	parentID := "sub_parent"
	seedTimingSub(t, st, &model.Submission{
		ID: parentID, WorkflowID: "wf_parent", WorkflowName: "wf_parent",
		State: model.SubmissionStateCompleted, CreatedAt: timingBase, CompletedAt: atp(100),
	})
	seedTimingStep(t, st, &model.StepInstance{
		ID: "si_subwf", SubmissionID: parentID, StepID: "subwf",
		State: model.StepStateCompleted, CreatedAt: at(0), CompletedAt: atp(95),
	})
	seedTimingTask(t, st, &model.Task{
		ID: "t_subwf_proxy", SubmissionID: parentID, StepID: "subwf", StepInstanceID: "si_subwf",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeSubworkflow, ScatterIndex: -1,
		CreatedAt: at(0), StartedAt: atp(0), CompletedAt: atp(95),
	})

	seedTimingWorkflow(t, st, "wf_child", []model.Step{{ID: "inner"}})
	childID := "sub_child"
	seedTimingSub(t, st, &model.Submission{
		ID: childID, WorkflowID: "wf_child", WorkflowName: "wf_child",
		State: model.SubmissionStateCompleted, ParentTaskID: "t_subwf_proxy",
		CreatedAt: at(1), CompletedAt: atp(90),
	})
	seedTimingStep(t, st, &model.StepInstance{
		ID: "si_inner", SubmissionID: childID, StepID: "inner",
		State: model.StepStateCompleted, CreatedAt: at(1), CompletedAt: atp(85),
	})
	seedTimingTask(t, st, &model.Task{
		ID: "t_inner", SubmissionID: childID, StepID: "inner", StepInstanceID: "si_inner",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal, ScatterIndex: -1,
		CreatedAt: at(2), StartedAt: atp(5), CompletedAt: atp(80),
	})

	// Without the flag: no children, but the proxy links the child.
	rep := getTiming(t, srv, parentID, "")
	if len(rep.Children) != 0 {
		t.Fatalf("children = %d, want 0 without include_children", len(rep.Children))
	}
	proxy := findTaskRow(t, rep, "t_subwf_proxy")
	if proxy.ChildSubmissionID != childID {
		t.Errorf("child_submission_id = %q, want %q", proxy.ChildSubmissionID, childID)
	}

	// With the flag: the child's full report is nested.
	rep = getTiming(t, srv, parentID, "?include_children=true")
	if len(rep.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(rep.Children))
	}
	child := rep.Children[0]
	if child.Submission.ID != childID {
		t.Errorf("child submission id = %q, want %q", child.Submission.ID, childID)
	}
	inner := findTaskRow(t, child, "t_inner")
	if inner.RunS == nil || !almostEq(*inner.RunS, 75) {
		t.Errorf("child task run_s = %v, want 75", inner.RunS)
	}
	if child.Submission.WallS == nil || !almostEq(*child.Submission.WallS, 89) {
		t.Errorf("child wall_s = %v, want 89", child.Submission.WallS)
	}
}

// TestTimingNotFound: unknown submissions answer 404.
func TestTimingNotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/submissions/sub_nope/timing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
