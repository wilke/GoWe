package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// --- Packed $graph fixtures -------------------------------------------------
//
// dispatchScatterSubWorkflow / dispatchSubWorkflowStep parse wf.RawCWL, so the
// fixtures must be genuine packed $graph documents (createPipeline sets no
// RawCWL and cannot exercise these paths).

// scatterSubwfCWL: main scatters scatter_step over the embedded echo-wf.
const scatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
    outputs:
      results:
        type: string[]
        outputSource: scatter_step/out
    steps:
      scatter_step:
        run: "#echo-wf"
        scatter: letter
        in:
          letter: letters
        out: [out]
  - id: echo-wf
    class: Workflow
    inputs:
      letter: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: letter
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

// whenScatterSubwfCWL: scatterSubwfCWL with a per-iteration 'when' that skips
// the letter "b".
const whenScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
    outputs:
      results:
        type: string[]
        outputSource: scatter_step/out
    steps:
      scatter_step:
        run: "#echo-wf"
        scatter: letter
        when: '$(inputs.letter != "b")'
        in:
          letter: letters
        out: [out]
  - id: echo-wf
    class: Workflow
    inputs:
      letter: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: letter
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

// inertScatterSubwfCWL: like scatterSubwfCWL, but the child tool targets the
// worker executor. With no workers online the child's step defers in
// pre-flight, so children stay PENDING across ticks — this makes cancel
// reconciliation deterministic (children cannot self-complete before the
// scheduler cancels them).
const inertScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
    outputs:
      results:
        type: string[]
        outputSource: scatter_step/out
    steps:
      scatter_step:
        run: "#echo-wf"
        scatter: letter
        in:
          letter: letters
        out: [out]
  - id: echo-wf
    class: Workflow
    inputs:
      letter: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: letter
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    hints:
      "gowe:Execution":
        executor: worker
    inputs:
      msg:
        type: string
        inputBinding:
          position: 1
    outputs:
      out:
        type: string
`

// nestedScatterSubwfCWL: two scatter inputs with nested_crossproduct.
const nestedScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
      nums: string[]
    outputs: []
    steps:
      combine_step:
        run: "#combine-wf"
        scatter: [letter, num]
        scatterMethod: nested_crossproduct
        in:
          letter: letters
          num: nums
        out: [out]
  - id: combine-wf
    class: Workflow
    inputs:
      letter: string
      num: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: letter
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

// headOfLineCWL: scatterSubwfCWL plus an independent side step that must
// dispatch (and complete inline via the local executor) in the same tick as
// the in-flight scatter sub-workflow.
const headOfLineCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
    outputs: []
    steps:
      scatter_step:
        run: "#echo-wf"
        scatter: letter
        in:
          letter: letters
        out: [out]
      side_step:
        run: "#side-tool"
        in: {}
        out: []
  - id: echo-wf
    class: Workflow
    inputs:
      letter: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: letter
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
  - id: side-tool
    class: CommandLineTool
    baseCommand: [echo, side]
    inputs: []
    outputs: []
`

// nonScatterSubwfCWL: a plain (non-scatter) sub-workflow step whose child
// really executes an echo through the local executor.
const nonScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: SubworkflowFeatureRequirement
    inputs:
      msg: string
    outputs: []
    steps:
      subwf_step:
        run: "#child-wf"
        in:
          msg: msg
        out: []
  - id: child-wf
    class: Workflow
    inputs:
      msg: string
    outputs: []
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: msg
        out: []
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    inputs:
      msg:
        type: string
        inputBinding:
          position: 1
    outputs: []
`

// twoLevelScatterSubwfCWL: main scatters mid_step over "#mid-wf"; mid-wf in
// turn scatters leaf_step over "#leaf-wf"; leaf-wf runs an inert
// worker-executor echo so grandchildren stay in flight until cancelled. The
// non-scattered `reps` input is passed through from main so mid-wf has an
// array to scatter over without nested-array workflow inputs.
const twoLevelScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letters: string[]
      reps: string[]
    outputs: []
    steps:
      mid_step:
        run: "#mid-wf"
        scatter: letter
        in:
          letter: letters
          reps: reps
        out: [out]
  - id: mid-wf
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
    inputs:
      letter: string
      reps: string[]
    outputs:
      out:
        type: string[]
        outputSource: leaf_step/out
    steps:
      leaf_step:
        run: "#leaf-wf"
        scatter: rep
        in:
          rep: reps
        out: [out]
  - id: leaf-wf
    class: Workflow
    inputs:
      rep: string
    outputs:
      out:
        type: string
        outputSource: echo/out
    steps:
      echo:
        run: "#echo-tool"
        in:
          msg: rep
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    hints:
      "gowe:Execution":
        executor: worker
    inputs:
      msg:
        type: string
        inputBinding:
          position: 1
    outputs:
      out:
        type: string
`

// --- Helpers ----------------------------------------------------------------

// registerGraphWorkflow parses a packed $graph document the same way workflow
// registration does, stores the resulting model.Workflow (with RawCWL), a
// PENDING submission, and one WAITING StepInstance per step. Returns the
// submission ID.
func registerGraphWorkflow(t *testing.T, st store.Store, rawCWL string, inputs map[string]any,
	labels map[string]string, outputDest string) string {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p := parser.New(logger)
	graph, err := p.ParseGraph([]byte(rawCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "subwf-proxy-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = rawCWL
	wf.Class = "Workflow"
	wf.CreatedAt = now
	wf.UpdatedAt = now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	if labels == nil {
		labels = map[string]string{}
	}
	sub := &model.Submission{
		ID:                "sub_" + uuid.New().String(),
		WorkflowID:        wf.ID,
		WorkflowName:      wf.Name,
		State:             model.SubmissionStatePending,
		Inputs:            inputs,
		Outputs:           map[string]any{},
		Labels:            labels,
		OutputDestination: outputDest,
		CreatedAt:         now,
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

// subworkflowProxies returns the submission's subworkflow tasks sorted by
// ScatterIndex.
func subworkflowProxies(t *testing.T, st store.Store, subID string) []*model.Task {
	t.Helper()
	tasks, err := st.ListTasksBySubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	var proxies []*model.Task
	for _, task := range tasks {
		if task.ExecutorType == model.ExecutorTypeSubworkflow {
			proxies = append(proxies, task)
		}
	}
	sort.Slice(proxies, func(i, j int) bool { return proxies[i].ScatterIndex < proxies[j].ScatterIndex })
	return proxies
}

// onlyChild returns the single child submission of a proxy task.
func onlyChild(t *testing.T, st store.Store, taskID string) *model.Submission {
	t.Helper()
	children, err := st.GetChildSubmissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetChildSubmissions(%s): %v", taskID, err)
	}
	if len(children) != 1 {
		t.Fatalf("expected exactly 1 child for task %s, got %d", taskID, len(children))
	}
	return children[0]
}

// finalizeChildAs drives a child submission (and all its step instances) to a
// terminal state directly in the store, standing in for the child's own
// execution.
func finalizeChildAs(t *testing.T, st store.Store, childID string, state model.SubmissionState,
	outputs map[string]any, subErr *model.SubmissionError) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	var stepState model.StepInstanceState
	switch state {
	case model.SubmissionStateCompleted:
		stepState = model.StepStateCompleted
	case model.SubmissionStateFailed:
		stepState = model.StepStateFailed
	case model.SubmissionStateCancelled:
		stepState = model.StepStateSkipped
	default:
		t.Fatalf("finalizeChildAs: unsupported state %q", state)
	}

	steps, err := st.ListStepsBySubmission(ctx, childID)
	if err != nil {
		t.Fatalf("ListStepsBySubmission(%s): %v", childID, err)
	}
	for _, si := range steps {
		si.State = stepState
		si.CompletedAt = &now
		if err := st.UpdateStepInstance(ctx, si); err != nil {
			t.Fatalf("UpdateStepInstance(%s): %v", si.ID, err)
		}
	}

	child, err := st.GetSubmission(ctx, childID)
	if err != nil {
		t.Fatalf("GetSubmission(%s): %v", childID, err)
	}
	child.State = state
	if outputs != nil {
		child.Outputs = outputs
	}
	child.Error = subErr
	child.CompletedAt = &now
	if err := st.UpdateSubmission(ctx, child); err != nil {
		t.Fatalf("UpdateSubmission(%s): %v", childID, err)
	}
}

// completeChildSteps marks every step of a child COMPLETED with the given
// outputs but leaves the submission itself PENDING, so the scheduler's own
// finalize phase completes the child (out-of-order fan-in scenarios).
func completeChildSteps(t *testing.T, st store.Store, childID string, outputs map[string]any) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	steps, err := st.ListStepsBySubmission(ctx, childID)
	if err != nil {
		t.Fatalf("ListStepsBySubmission(%s): %v", childID, err)
	}
	for _, si := range steps {
		si.State = model.StepStateCompleted
		si.Outputs = outputs
		si.CompletedAt = &now
		if err := st.UpdateStepInstance(ctx, si); err != nil {
			t.Fatalf("UpdateStepInstance(%s): %v", si.ID, err)
		}
	}
}

// cancelParentLikeHandler mimics the server's cancel handler: terminal
// submission write plus CancelNonTerminalSteps/Tasks fan-out.
func cancelParentLikeHandler(t *testing.T, st store.Store, subID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission(%s): %v", subID, err)
	}
	sub.State = model.SubmissionStateCancelled
	sub.CompletedAt = &now
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("UpdateSubmission(cancel): %v", err)
	}
	if _, err := st.CancelNonTerminalSteps(ctx, subID, now); err != nil {
		t.Fatalf("CancelNonTerminalSteps: %v", err)
	}
	if _, err := st.CancelNonTerminalTasks(ctx, subID, now); err != nil {
		t.Fatalf("CancelNonTerminalTasks: %v", err)
	}
}

// dispatchOnly runs phases 1+2 (advanceWaiting + dispatchReady) so tests can
// observe the freshly-dispatched state before pollInFlight/advanceSteps mutate
// it further in the same tick.
func dispatchOnly(t *testing.T, sched *Loop) {
	t.Helper()
	ctx := context.Background()
	sched.cache = newTickCache()
	affected := make(map[string]bool)
	if err := sched.advanceWaiting(ctx, affected); err != nil {
		t.Fatalf("advanceWaiting: %v", err)
	}
	if err := sched.dispatchReady(ctx, affected); err != nil {
		t.Fatalf("dispatchReady: %v", err)
	}
}

// --- Test 1: dispatch -------------------------------------------------------

// TestScatterSubwf_Dispatch verifies the non-blocking dispatch snapshot: the
// step goes DISPATCHED with scatter metadata in the same transaction that
// persists N RUNNING proxy tasks, and each proxy is paired with a PENDING
// child submission carrying the combo inputs, the inherited routing labels
// (fresh map, parent_task overwritten), and NO OutputDestination.
func TestScatterSubwf_Dispatch(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b", "c"}},
		map[string]string{"debug": "true", "worker_group": "wg1", "project": "not-inherited"},
		"ws:///user@host/results/")

	dispatchOnly(t, sched)

	// Step instance: DISPATCHED with scatter metadata.
	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateDispatched {
		t.Errorf("si.State = %q, want DISPATCHED", si.State)
	}
	if si.ScatterCount != 3 {
		t.Errorf("si.ScatterCount = %d, want 3", si.ScatterCount)
	}
	if si.ScatterMethod != "dotproduct" {
		t.Errorf("si.ScatterMethod = %q, want dotproduct", si.ScatterMethod)
	}

	// Proxy tasks: RUNNING, Job=combo, ScatterIndex 0..2, MaxRetries=0.
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 3 {
		t.Fatalf("expected 3 proxy tasks, got %d", len(proxies))
	}
	letters := []string{"a", "b", "c"}
	for i, proxy := range proxies {
		if proxy.State != model.TaskStateRunning {
			t.Errorf("proxy %d state = %q, want RUNNING", i, proxy.State)
		}
		if proxy.ScatterIndex != i {
			t.Errorf("proxy %d ScatterIndex = %d, want %d", i, proxy.ScatterIndex, i)
		}
		if proxy.MaxRetries != 0 {
			t.Errorf("proxy %d MaxRetries = %d, want 0", i, proxy.MaxRetries)
		}
		if got := proxy.Job["letter"]; got != letters[i] {
			t.Errorf("proxy %d Job[letter] = %v, want %q", i, got, letters[i])
		}
		if got := proxy.Inputs["letter"]; got != letters[i] {
			t.Errorf("proxy %d Inputs[letter] = %v, want %q", i, got, letters[i])
		}

		// Child: PENDING, linked by ParentTaskID, combo inputs, fresh labels.
		child := onlyChild(t, st, proxy.ID)
		if child.State != model.SubmissionStatePending {
			t.Errorf("child %d state = %q, want PENDING", i, child.State)
		}
		if child.ParentTaskID != proxy.ID {
			t.Errorf("child %d ParentTaskID = %q, want %q", i, child.ParentTaskID, proxy.ID)
		}
		if got := child.Inputs["letter"]; got != letters[i] {
			t.Errorf("child %d Inputs[letter] = %v, want %q", i, got, letters[i])
		}
		if child.Labels["parent_task"] != proxy.ID {
			t.Errorf("child %d parent_task label = %q, want %q", i, child.Labels["parent_task"], proxy.ID)
		}
		if child.Labels["debug"] != "true" {
			t.Errorf("child %d debug label = %q, want true", i, child.Labels["debug"])
		}
		if child.Labels["worker_group"] != "wg1" {
			t.Errorf("child %d worker_group label = %q, want wg1", i, child.Labels["worker_group"])
		}
		if _, ok := child.Labels["project"]; ok {
			t.Errorf("child %d inherited unrelated label 'project' (labels must be a fresh map)", i)
		}

		// OutputDestination must NOT be inherited [F6]. GetChildSubmissions
		// does not select the column, so re-read the full row.
		full, err := st.GetSubmission(ctx, child.ID)
		if err != nil {
			t.Fatalf("GetSubmission(child %d): %v", i, err)
		}
		if full.OutputDestination != "" {
			t.Errorf("child %d OutputDestination = %q, want empty", i, full.OutputDestination)
		}

		// Child step instances exist and are WAITING.
		childSteps, err := st.ListStepsBySubmission(ctx, child.ID)
		if err != nil {
			t.Fatalf("ListStepsBySubmission(child %d): %v", i, err)
		}
		if len(childSteps) != 1 || childSteps[0].State != model.StepStateWaiting {
			t.Errorf("child %d steps = %d (state %v), want 1 WAITING",
				i, len(childSteps), childSteps)
		}
	}
}

// TestScatterSubwf_Dispatch_WhenSkip verifies that a when-skipped combination
// becomes a terminal SUCCESS task with null outputs and no child submission,
// exactly like plain scatter.
func TestScatterSubwf_Dispatch_WhenSkip(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, whenScatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

	dispatchOnly(t, sched)

	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 3 {
		t.Fatalf("expected 3 tasks (2 proxies + 1 when-skip), got %d", len(proxies))
	}

	skipped := proxies[1] // letter "b"
	if skipped.State != model.TaskStateSuccess {
		t.Errorf("when-skipped task state = %q, want SUCCESS", skipped.State)
	}
	if out, ok := skipped.Outputs["out"]; !ok || out != nil {
		t.Errorf("when-skipped task Outputs = %v, want {out: null}", skipped.Outputs)
	}
	children, err := st.GetChildSubmissions(ctx, skipped.ID)
	if err != nil {
		t.Fatalf("GetChildSubmissions: %v", err)
	}
	if len(children) != 0 {
		t.Errorf("when-skipped task has %d children, want 0", len(children))
	}

	for _, i := range []int{0, 2} {
		if proxies[i].State != model.TaskStateRunning {
			t.Errorf("proxy %d state = %q, want RUNNING", i, proxies[i].State)
		}
		onlyChild(t, st, proxies[i].ID)
	}

	// Finalize the non-skipped children; the fan-in must complete the step
	// with a null hole at the skipped index (when-skip synthetics are SUCCESS,
	// not SKIPPED, so the anySkipped guard must not fire).
	finalizeChildAs(t, st, onlyChild(t, st, proxies[0].ID).ID,
		model.SubmissionStateCompleted, map[string]any{"out": "A"}, nil)
	finalizeChildAs(t, st, onlyChild(t, st, proxies[2].ID).ID,
		model.SubmissionStateCompleted, map[string]any{"out": "C"}, nil)

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateCompleted {
		t.Fatalf("si.State = %q, want COMPLETED", si.State)
	}
	out, ok := si.Outputs["out"].([]any)
	if !ok || len(out) != 3 {
		t.Fatalf("si.Outputs[out] = %v, want a 3-element array", si.Outputs["out"])
	}
	if out[0] != "A" || out[1] != nil || out[2] != "C" {
		t.Errorf("si.Outputs[out] = %v, want [A <nil> C] (null hole at the skipped index)", out)
	}
}

// --- Test 2: pollInFlight advancement / reconciliation / repair -------------

// TestScatterSubwf_PollAdvancement verifies the child→proxy state copy:
// COMPLETED→SUCCESS (outputs verbatim), FAILED→FAILED (child error in
// stderr), CANCELLED→FAILED ("cancelled" in stderr) — and that the parent
// step and submission then fail with the child's detail.
func TestScatterSubwf_PollAdvancement(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 3 {
		t.Fatalf("expected 3 proxies, got %d", len(proxies))
	}

	finalizeChildAs(t, st, onlyChild(t, st, proxies[0].ID).ID,
		model.SubmissionStateCompleted, map[string]any{"out": "A"}, nil)
	finalizeChildAs(t, st, onlyChild(t, st, proxies[1].ID).ID,
		model.SubmissionStateFailed, nil,
		&model.SubmissionError{Code: "STEP_FAILED", Message: "boom"})
	finalizeChildAs(t, st, onlyChild(t, st, proxies[2].ID).ID,
		model.SubmissionStateCancelled, nil, nil)

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	proxies = subworkflowProxies(t, st, subID)
	if proxies[0].State != model.TaskStateSuccess {
		t.Errorf("proxy 0 state = %q, want SUCCESS", proxies[0].State)
	}
	if got := proxies[0].Outputs["out"]; got != "A" {
		t.Errorf("proxy 0 Outputs[out] = %v, want A", got)
	}
	if proxies[1].State != model.TaskStateFailed {
		t.Errorf("proxy 1 state = %q, want FAILED", proxies[1].State)
	}
	if !strings.Contains(proxies[1].Stderr, "boom") || !strings.Contains(proxies[1].Stderr, "STEP_FAILED") {
		t.Errorf("proxy 1 Stderr = %q, want the child error detail", proxies[1].Stderr)
	}
	if proxies[2].State != model.TaskStateFailed {
		t.Errorf("proxy 2 state = %q, want FAILED (child CANCELLED maps to FAILED)", proxies[2].State)
	}
	if !strings.Contains(proxies[2].Stderr, "cancelled") {
		t.Errorf("proxy 2 Stderr = %q, want it to mention cancellation", proxies[2].Stderr)
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateFailed {
		t.Errorf("si.State = %q, want FAILED", si.State)
	}
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("sub.State = %q, want FAILED", sub.State)
	}
	if sub.Error == nil || !strings.Contains(sub.Error.Context.Stderr, "boom") {
		t.Errorf("sub.Error = %+v, want the child failure detail propagated", sub.Error)
	}
}

// TestScatterSubwf_Reconciliation verifies pollInFlight branch (a): when the
// proxy's own submission is terminal (cancel handler crashed between its
// submission and step/task writes), the children are cancelled, the proxies
// SKIPPED, and the step conditionally SKIPPED.
func TestScatterSubwf_Reconciliation(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	// Inert fixture: children cannot self-complete before reconciliation.
	subID := registerGraphWorkflow(t, st, inertScatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 2 {
		t.Fatalf("expected 2 proxies, got %d", len(proxies))
	}

	// Crash-window cancel: ONLY the submission goes terminal.
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	now := time.Now().UTC()
	sub.State = model.SubmissionStateCancelled
	sub.CompletedAt = &now
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("UpdateSubmission(cancel): %v", err)
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	for i, proxy := range subworkflowProxies(t, st, subID) {
		if proxy.State != model.TaskStateSkipped {
			t.Errorf("proxy %d state = %q, want SKIPPED", i, proxy.State)
		}
		child := onlyChild(t, st, proxy.ID)
		if child.State != model.SubmissionStateCancelled {
			t.Errorf("child %d state = %q, want CANCELLED", i, child.State)
		}
	}
	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateSkipped {
		t.Errorf("si.State = %q, want SKIPPED", si.State)
	}
	// The submission must stay CANCELLED.
	sub, _ = st.GetSubmission(ctx, subID)
	if sub.State != model.SubmissionStateCancelled {
		t.Errorf("sub.State = %q, want CANCELLED", sub.State)
	}
}

// TestScatterSubwf_RepairFromJob verifies pollInFlight branch (b): a proxy
// whose child vanished (crash between the dispatch transaction and child
// creation) gets a new child rebuilt from the persisted task.Job, and the
// scatter then completes normally.
func TestScatterSubwf_RepairFromJob(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 3 {
		t.Fatalf("expected 3 proxies, got %d", len(proxies))
	}

	// Simulate the crash window: children never got created.
	for _, proxy := range proxies {
		if err := st.DeleteSubmission(ctx, onlyChild(t, st, proxy.ID).ID); err != nil {
			t.Fatalf("DeleteSubmission: %v", err)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2 (repair): %v", err)
	}

	letters := []string{"a", "b", "c"}
	for i, proxy := range subworkflowProxies(t, st, subID) {
		if proxy.State != model.TaskStateRunning {
			t.Errorf("proxy %d state = %q, want RUNNING after repair", i, proxy.State)
		}
		child := onlyChild(t, st, proxy.ID)
		if child.State != model.SubmissionStatePending {
			t.Errorf("repaired child %d state = %q, want PENDING", i, child.State)
		}
		if got := child.Inputs["letter"]; got != letters[i] {
			t.Errorf("repaired child %d Inputs[letter] = %v, want %q (from task.Job)", i, got, letters[i])
		}
	}

	// The repaired children drive the scatter to normal completion.
	for i, proxy := range subworkflowProxies(t, st, subID) {
		finalizeChildAs(t, st, onlyChild(t, st, proxy.ID).ID,
			model.SubmissionStateCompleted, map[string]any{"out": strings.ToUpper(letters[i])}, nil)
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Fatalf("sub.State = %q, want COMPLETED", sub.State)
	}
	results, _ := sub.Outputs["results"].([]any)
	want := []any{"A", "B", "C"}
	if fmt.Sprint(results) != fmt.Sprint(want) {
		t.Errorf("results = %v, want %v", results, want)
	}
}

// --- Test 3: fan-in ---------------------------------------------------------

// TestScatterSubwf_FanIn_OutOfOrder verifies that children finishing out of
// order still gather in ScatterIndex order.
func TestScatterSubwf_FanIn_OutOfOrder(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 3 {
		t.Fatalf("expected 3 proxies, got %d", len(proxies))
	}

	// Child 2 finishes first; children 0 and 1 only have their steps done, so
	// the scheduler's own finalize phase completes them one tick later.
	finalizeChildAs(t, st, onlyChild(t, st, proxies[2].ID).ID,
		model.SubmissionStateCompleted, map[string]any{"out": "C"}, nil)
	completeChildSteps(t, st, onlyChild(t, st, proxies[0].ID).ID, map[string]any{"out": "A"})
	completeChildSteps(t, st, onlyChild(t, st, proxies[1].ID).ID, map[string]any{"out": "B"})

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	proxies = subworkflowProxies(t, st, subID)
	if proxies[2].State != model.TaskStateSuccess {
		t.Fatalf("proxy 2 state = %q, want SUCCESS (finished first)", proxies[2].State)
	}
	if proxies[0].State == model.TaskStateSuccess && proxies[1].State == model.TaskStateSuccess {
		t.Fatal("proxies 0/1 already SUCCESS — completion is not out of order")
	}
	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State.IsTerminal() {
		t.Fatalf("si terminal (%q) before all proxies finished", si.State)
	}

	// Remaining children finalize; fan-in must order by ScatterIndex.
	for i := 0; i < 3; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", i+3, err)
		}
	}
	si = getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateCompleted {
		t.Fatalf("si.State = %q, want COMPLETED", si.State)
	}
	got, _ := si.Outputs["out"].([]any)
	want := []any{"A", "B", "C"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("si.Outputs[out] = %v, want %v (ScatterIndex order)", got, want)
	}

	sub, _ := st.GetSubmission(ctx, subID)
	if sub.State != model.SubmissionStateCompleted {
		t.Errorf("sub.State = %q, want COMPLETED", sub.State)
	}
	results, _ := sub.Outputs["results"].([]any)
	if fmt.Sprint(results) != fmt.Sprint(want) {
		t.Errorf("workflow results = %v, want %v", results, want)
	}
}

// TestScatterSubwf_FanIn_NestedCrossproduct verifies nested_crossproduct
// dims are persisted at dispatch and honored at fan-in.
func TestScatterSubwf_FanIn_NestedCrossproduct(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, nestedScatterSubwfCWL,
		map[string]any{"letters": []any{"x", "y"}, "nums": []any{"1", "2", "3"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	si := getStepInstancesByStep(t, st, subID)["combine_step"]
	if si.ScatterCount != 6 {
		t.Fatalf("si.ScatterCount = %d, want 6", si.ScatterCount)
	}
	if si.ScatterMethod != "nested_crossproduct" {
		t.Errorf("si.ScatterMethod = %q, want nested_crossproduct", si.ScatterMethod)
	}
	if fmt.Sprint(si.ScatterDims) != fmt.Sprint([]int{2, 3}) {
		t.Errorf("si.ScatterDims = %v, want [2 3]", si.ScatterDims)
	}

	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 6 {
		t.Fatalf("expected 6 proxies, got %d", len(proxies))
	}
	for _, proxy := range proxies {
		out := fmt.Sprintf("%v-%v", proxy.Job["letter"], proxy.Job["num"])
		finalizeChildAs(t, st, onlyChild(t, st, proxy.ID).ID,
			model.SubmissionStateCompleted, map[string]any{"out": out}, nil)
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	si = getStepInstancesByStep(t, st, subID)["combine_step"]
	if si.State != model.StepStateCompleted {
		t.Fatalf("si.State = %q, want COMPLETED", si.State)
	}
	want := []any{
		[]any{"x-1", "x-2", "x-3"},
		[]any{"y-1", "y-2", "y-3"},
	}
	if fmt.Sprint(si.Outputs["out"]) != fmt.Sprint(want) {
		t.Errorf("si.Outputs[out] = %v, want %v", si.Outputs["out"], want)
	}
}

// TestScatterSubwf_FanIn_Guards unit-tests the advanceSteps fan-in guards
// directly against hand-built task sets: an incomplete distinct-index set
// waits, duplicate rows do not oversize the gathered array, and a SKIPPED
// task in the set skips the step instead of completing over the hole. [F5,M2]
func TestScatterSubwf_FanIn_Guards(t *testing.T) {
	type taskSpec struct {
		index int
		state model.TaskState
		out   any
	}
	tests := []struct {
		name         string
		scatterCount int
		tasks        []taskSpec
		wantState    model.StepInstanceState
		wantOut      []any // only checked for COMPLETED
	}{
		{
			name:         "incomplete distinct index set waits",
			scatterCount: 3,
			tasks: []taskSpec{
				{0, model.TaskStateSuccess, "A"},
				{1, model.TaskStateSuccess, "B"},
			},
			wantState: model.StepStateDispatched, // unchanged: fan-in must wait
		},
		{
			name:         "duplicate legacy rows do not oversize",
			scatterCount: 2,
			tasks: []taskSpec{
				{0, model.TaskStateSuccess, "A"},
				{0, model.TaskStateSuccess, "A"},
				{1, model.TaskStateSuccess, "B"},
			},
			wantState: model.StepStateCompleted,
			wantOut:   []any{"A", "B"},
		},
		{
			name:         "skipped task in set skips the step",
			scatterCount: 2,
			tasks: []taskSpec{
				{0, model.TaskStateSuccess, "A"},
				{1, model.TaskStateSkipped, nil},
			},
			wantState: model.StepStateSkipped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			ctx := context.Background()

			subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
				map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

			// Hand-build the dispatched state (bypassing dispatch) so the
			// task set can be inconsistent on purpose.
			si := getStepInstancesByStep(t, st, subID)["scatter_step"]
			si.State = model.StepStateDispatched
			si.ScatterCount = tt.scatterCount
			si.ScatterMethod = "dotproduct"
			if err := st.UpdateStepInstance(ctx, si); err != nil {
				t.Fatalf("UpdateStepInstance: %v", err)
			}
			now := time.Now().UTC()
			for _, spec := range tt.tasks {
				task := &model.Task{
					ID:             "task_" + uuid.New().String(),
					SubmissionID:   subID,
					StepID:         "scatter_step",
					StepInstanceID: si.ID,
					State:          spec.state,
					ExecutorType:   model.ExecutorTypeSubworkflow,
					ScatterIndex:   spec.index,
					Inputs:         map[string]any{},
					Outputs:        map[string]any{"out": spec.out},
					CompletedAt:    &now,
					CreatedAt:      now,
				}
				if err := st.CreateTask(ctx, task); err != nil {
					t.Fatalf("CreateTask: %v", err)
				}
			}

			sched.cache = newTickCache()
			if err := sched.advanceSteps(ctx, map[string]bool{}); err != nil {
				t.Fatalf("advanceSteps: %v", err)
			}

			got := getStepInstancesByStep(t, st, subID)["scatter_step"]
			if got.State != tt.wantState {
				t.Fatalf("si.State = %q, want %q", got.State, tt.wantState)
			}
			if tt.wantState == model.StepStateCompleted {
				out, _ := got.Outputs["out"].([]any)
				if fmt.Sprint(out) != fmt.Sprint(tt.wantOut) {
					t.Errorf("si.Outputs[out] = %v, want %v", out, tt.wantOut)
				}
			}
		})
	}
}

// --- Test 4: head-of-line ---------------------------------------------------

// TestScatterSubwf_HeadOfLine verifies that an unrelated READY step dispatches
// (and completes) in the same tick as a mid-flight scatter sub-workflow — the
// old serial implementation blocked the whole tick until the scatter finished.
func TestScatterSubwf_HeadOfLine(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, headOfLineCWL,
		map[string]any{"letters": []any{"a", "b", "c"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	siByStep := getStepInstancesByStep(t, st, subID)
	if siByStep["side_step"].State != model.StepStateCompleted {
		t.Errorf("side_step state = %q, want COMPLETED in the same tick", siByStep["side_step"].State)
	}
	scatterState := siByStep["scatter_step"].State
	if scatterState != model.StepStateDispatched && scatterState != model.StepStateRunning {
		t.Errorf("scatter_step state = %q, want DISPATCHED or RUNNING (mid-flight)", scatterState)
	}
	if proxies := subworkflowProxies(t, st, subID); len(proxies) != 3 {
		t.Errorf("expected 3 in-flight proxies, got %d", len(proxies))
	}
}

// --- Test 5: MaxRetries=0 invariant -----------------------------------------

// TestScatterSubwf_ProxyNotRetried verifies a FAILED proxy is never picked up
// by markRetries, even with a non-zero scheduler MaxRetries default —
// retrying would erase the child's error. [F8]
func TestScatterSubwf_ProxyNotRetried(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 3 // proxies must override this with 0
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b"}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	proxies := subworkflowProxies(t, st, subID)
	finalizeChildAs(t, st, onlyChild(t, st, proxies[0].ID).ID,
		model.SubmissionStateFailed, nil,
		&model.SubmissionError{Code: "STEP_FAILED", Message: "child exploded"})
	finalizeChildAs(t, st, onlyChild(t, st, proxies[1].ID).ID,
		model.SubmissionStateCompleted, map[string]any{"out": "B"}, nil)

	// Tick 2 fails the proxy; tick 3 would be the retry pickup.
	for i := 2; i <= 3; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	proxy, err := st.GetTask(ctx, proxies[0].ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if proxy.State != model.TaskStateFailed {
		t.Errorf("proxy state = %q, want FAILED (never RETRYING)", proxy.State)
	}
	if proxy.RetryCount != 0 {
		t.Errorf("proxy RetryCount = %d, want 0", proxy.RetryCount)
	}
	if !strings.Contains(proxy.Stderr, "child exploded") {
		t.Errorf("proxy Stderr = %q, child error must be preserved", proxy.Stderr)
	}
	sub, _ := st.GetSubmission(ctx, subID)
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("sub.State = %q, want FAILED", sub.State)
	}
}

// --- Test 8: non-scatter single-proxy path ----------------------------------

// TestSubwf_NonScatter_EndToEnd runs a plain sub-workflow step through the
// full tick loop: one proxy (ScatterIndex -1), one child that really executes
// its echo step via the local executor, then proxy SUCCESS and parent
// COMPLETED.
func TestSubwf_NonScatter_EndToEnd(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, nonScatterSubwfCWL,
		map[string]any{"msg": "hello-child"}, nil, "")

	var sub *model.Submission
	for tick := 1; tick <= 10; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", tick, err)
		}
		var err error
		sub, err = st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("GetSubmission: %v", err)
		}
		if sub.State.IsTerminal() {
			break
		}
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Fatalf("sub.State = %q, want COMPLETED", sub.State)
	}

	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy task, got %d", len(proxies))
	}
	proxy := proxies[0]
	if proxy.ScatterIndex != -1 {
		t.Errorf("proxy ScatterIndex = %d, want -1", proxy.ScatterIndex)
	}
	if proxy.MaxRetries != 0 {
		t.Errorf("proxy MaxRetries = %d, want 0", proxy.MaxRetries)
	}
	if proxy.State != model.TaskStateSuccess {
		t.Errorf("proxy state = %q, want SUCCESS", proxy.State)
	}

	si := getStepInstancesByStep(t, st, subID)["subwf_step"]
	if si.State != model.StepStateCompleted {
		t.Errorf("si.State = %q, want COMPLETED", si.State)
	}
	if si.ScatterCount != 0 {
		t.Errorf("si.ScatterCount = %d, want 0 (non-scatter)", si.ScatterCount)
	}

	// The child really ran its echo through the local executor.
	child := onlyChild(t, st, proxy.ID)
	if child.State != model.SubmissionStateCompleted {
		t.Errorf("child state = %q, want COMPLETED", child.State)
	}
	childTasks, err := st.ListTasksBySubmission(ctx, child.ID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission(child): %v", err)
	}
	if len(childTasks) != 1 {
		t.Fatalf("expected 1 child task, got %d", len(childTasks))
	}
	if childTasks[0].State != model.TaskStateSuccess {
		t.Errorf("child task state = %q, want SUCCESS", childTasks[0].State)
	}
	if !strings.Contains(childTasks[0].Stdout, "hello-child") {
		t.Errorf("child task Stdout = %q, want it to contain hello-child", childTasks[0].Stdout)
	}
}

// --- Test 10: empty scatter -------------------------------------------------

// TestScatterSubwf_EmptyScatter verifies an empty scatter completes inline
// with empty gathered outputs — no proxies, no children, no hang. [M1]
func TestScatterSubwf_EmptyScatter(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{}}, nil, "")

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateCompleted {
		t.Fatalf("si.State = %q, want COMPLETED (inline, no hang)", si.State)
	}
	out, ok := si.Outputs["out"].([]any)
	if !ok || len(out) != 0 {
		t.Errorf("si.Outputs[out] = %v, want empty array", si.Outputs["out"])
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks for empty scatter, got %d", len(tasks))
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateCompleted {
		t.Errorf("sub.State = %q, want COMPLETED", sub.State)
	}
	results, ok := sub.Outputs["results"].([]any)
	if !ok || len(results) != 0 {
		t.Errorf("sub.Outputs[results] = %v, want empty array", sub.Outputs["results"])
	}
}

// --- Test 11: cancel-vs-dispatch interleave ---------------------------------

// TestScatterSubwf_CancelVsDispatch verifies the step ends SKIPPED (never
// COMPLETED) whether the cancel lands before, during, or after the dispatch
// transaction. [F2,M2]
func TestScatterSubwf_CancelVsDispatch(t *testing.T) {
	t.Run("cancel before dispatch", func(t *testing.T) {
		sched, st := testSetup(t)
		ctx := context.Background()

		subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
			map[string]any{"letters": []any{"a", "b"}}, nil, "")
		cancelParentLikeHandler(t, st, subID)

		for i := 0; i < 2; i++ {
			if err := sched.Tick(ctx); err != nil {
				t.Fatalf("Tick: %v", err)
			}
		}
		if tasks, _ := st.ListTasksBySubmission(ctx, subID); len(tasks) != 0 {
			t.Errorf("expected 0 tasks, got %d", len(tasks))
		}
		si := getStepInstancesByStep(t, st, subID)["scatter_step"]
		if si.State != model.StepStateSkipped {
			t.Errorf("si.State = %q, want SKIPPED", si.State)
		}
		sub, _ := st.GetSubmission(ctx, subID)
		if sub.State != model.SubmissionStateCancelled {
			t.Errorf("sub.State = %q, want CANCELLED", sub.State)
		}
	})

	t.Run("cancel during dispatch", func(t *testing.T) {
		// The cancel handler runs between the step's READY snapshot and the
		// dispatch transaction: the transaction clobbers the handler's
		// SKIPPED with DISPATCHED, and the proxies/children are created after
		// the handler's fan-out snapshot. The post-dispatch re-read must undo
		// all of it.
		sched, st := testSetup(t)
		ctx := context.Background()

		subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
			map[string]any{"letters": []any{"a", "b"}}, nil, "")

		// Phase-1 only: step goes READY; snapshot it and the submission.
		sched.cache = newTickCache()
		if err := sched.advanceWaiting(ctx, map[string]bool{}); err != nil {
			t.Fatalf("advanceWaiting: %v", err)
		}
		subSnap, err := st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("GetSubmission: %v", err)
		}
		wf, err := st.GetWorkflow(ctx, subSnap.WorkflowID)
		if err != nil {
			t.Fatalf("GetWorkflow: %v", err)
		}
		siSnap := getStepInstancesByStep(t, st, subID)["scatter_step"]
		if siSnap.State != model.StepStateReady {
			t.Fatalf("si snapshot state = %q, want READY", siSnap.State)
		}

		// Cancel lands now (handler mimic: submission + steps + tasks).
		cancelParentLikeHandler(t, st, subID)

		// Dispatch proceeds from the stale READY snapshot.
		if err := sched.dispatchStep(ctx, siSnap, wf, subSnap); err != nil {
			t.Fatalf("dispatchStep: %v", err)
		}

		si := getStepInstancesByStep(t, st, subID)["scatter_step"]
		if si.State != model.StepStateSkipped {
			t.Errorf("si.State = %q, want SKIPPED (re-skipped after clobber)", si.State)
		}
		proxies := subworkflowProxies(t, st, subID)
		if len(proxies) != 2 {
			t.Fatalf("expected 2 proxies, got %d", len(proxies))
		}
		for i, proxy := range proxies {
			if proxy.State != model.TaskStateSkipped {
				t.Errorf("proxy %d state = %q, want SKIPPED", i, proxy.State)
			}
			child := onlyChild(t, st, proxy.ID)
			if child.State != model.SubmissionStateCancelled {
				t.Errorf("child %d state = %q, want CANCELLED", i, child.State)
			}
		}

		// Subsequent ticks must not resurrect anything.
		for i := 0; i < 3; i++ {
			if err := sched.Tick(ctx); err != nil {
				t.Fatalf("Tick: %v", err)
			}
		}
		si = getStepInstancesByStep(t, st, subID)["scatter_step"]
		if si.State != model.StepStateSkipped {
			t.Errorf("after ticks: si.State = %q, want SKIPPED", si.State)
		}
		sub, _ := st.GetSubmission(ctx, subID)
		if sub.State != model.SubmissionStateCancelled {
			t.Errorf("after ticks: sub.State = %q, want CANCELLED", sub.State)
		}
	})

	t.Run("cancel after dispatch", func(t *testing.T) {
		sched, st := testSetup(t)
		ctx := context.Background()

		subID := registerGraphWorkflow(t, st, inertScatterSubwfCWL,
			map[string]any{"letters": []any{"a", "b"}}, nil, "")

		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 1: %v", err)
		}
		cancelParentLikeHandler(t, st, subID)

		// The handler's CancelNonTerminalTasks fan-out EXCLUDES subworkflow
		// proxies: they must still be RUNNING here so the scheduler's
		// reconciliation (the only proxy cancel path) can reach the children.
		for i, proxy := range subworkflowProxies(t, st, subID) {
			if proxy.State != model.TaskStateRunning {
				t.Errorf("proxy %d state = %q, want RUNNING right after handler cancel", i, proxy.State)
			}
		}

		// One tick: pollSubworkflowTask observes the terminal parent, cancels
		// each child, and terminalizes the proxy SKIPPED.
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 2: %v", err)
		}
		for i, proxy := range subworkflowProxies(t, st, subID) {
			if proxy.State != model.TaskStateSkipped {
				t.Errorf("proxy %d state = %q, want SKIPPED after reconciliation", i, proxy.State)
			}
			child := onlyChild(t, st, proxy.ID)
			if child.State != model.SubmissionStateCancelled {
				t.Errorf("child %d state = %q, want CANCELLED (cascade reached the child)", i, child.State)
			}
		}

		for i := 0; i < 2; i++ {
			if err := sched.Tick(ctx); err != nil {
				t.Fatalf("Tick: %v", err)
			}
		}
		si := getStepInstancesByStep(t, st, subID)["scatter_step"]
		if si.State != model.StepStateSkipped {
			t.Errorf("si.State = %q, want SKIPPED (never COMPLETED)", si.State)
		}
		for i, proxy := range subworkflowProxies(t, st, subID) {
			if proxy.State != model.TaskStateSkipped {
				t.Errorf("proxy %d state = %q, want SKIPPED (not resurrected)", i, proxy.State)
			}
		}
		sub, _ := st.GetSubmission(ctx, subID)
		if sub.State != model.SubmissionStateCancelled {
			t.Errorf("sub.State = %q, want CANCELLED", sub.State)
		}
	})
}

// --- Test 12: recursive cancel cascade over nested scatter -------------------

// TestScatterSubwf_NestedCancelCascade verifies the cancel cascade reaches
// arbitrarily nested sub-workflows with no explicit recursion: cancelling the
// top parent leaves its proxies RUNNING (CancelNonTerminalTasks excludes
// them); reconciliation then cancels the mid-level children one tick at a
// time, whose own proxies stay RUNNING until the next reconciliation cancels
// the grandchildren — until every submission in the tree is CANCELLED and
// every proxy SKIPPED.
func TestScatterSubwf_NestedCancelCascade(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, twoLevelScatterSubwfCWL,
		map[string]any{"letters": []any{"a", "b"}, "reps": []any{"1", "2"}}, nil, "")

	// Tick 1: top-level dispatch (2 proxies + 2 mid children).
	// Tick 2: mid children dispatch (2 leaf proxies + 2 grandchildren each).
	for i := 1; i <= 2; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}

	topProxies := subworkflowProxies(t, st, subID)
	if len(topProxies) != 2 {
		t.Fatalf("expected 2 top-level proxies, got %d", len(topProxies))
	}
	var midChildIDs []string
	var midProxyIDs []string
	var grandchildIDs []string
	for _, proxy := range topProxies {
		mid := onlyChild(t, st, proxy.ID)
		midChildIDs = append(midChildIDs, mid.ID)
		midProxies := subworkflowProxies(t, st, mid.ID)
		if len(midProxies) != 2 {
			t.Fatalf("expected 2 leaf proxies under mid child %s, got %d", mid.ID, len(midProxies))
		}
		for _, midProxy := range midProxies {
			midProxyIDs = append(midProxyIDs, midProxy.ID)
			grandchildIDs = append(grandchildIDs, onlyChild(t, st, midProxy.ID).ID)
		}
	}
	if len(grandchildIDs) != 4 {
		t.Fatalf("expected 4 grandchildren, got %d", len(grandchildIDs))
	}

	// Cancel the top parent. The proxies must survive the handler fan-out.
	cancelParentLikeHandler(t, st, subID)
	for i, proxy := range subworkflowProxies(t, st, subID) {
		if proxy.State != model.TaskStateRunning {
			t.Errorf("top proxy %d state = %q, want RUNNING right after handler cancel", i, proxy.State)
		}
	}

	// The cascade advances one level per reconciliation pass; give it enough
	// ticks to reach the deepest level.
	for i := 0; i < 4; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("cascade Tick %d: %v", i+1, err)
		}
	}

	for i, proxy := range subworkflowProxies(t, st, subID) {
		if proxy.State != model.TaskStateSkipped {
			t.Errorf("top proxy %d state = %q, want SKIPPED", i, proxy.State)
		}
	}
	for _, midID := range midChildIDs {
		mid, err := st.GetSubmission(ctx, midID)
		if err != nil {
			t.Fatalf("GetSubmission(mid %s): %v", midID, err)
		}
		if mid.State != model.SubmissionStateCancelled {
			t.Errorf("mid child %s state = %q, want CANCELLED", midID, mid.State)
		}
	}
	for _, proxyID := range midProxyIDs {
		proxy, err := st.GetTask(ctx, proxyID)
		if err != nil {
			t.Fatalf("GetTask(mid proxy %s): %v", proxyID, err)
		}
		if proxy.State != model.TaskStateSkipped {
			t.Errorf("mid proxy %s state = %q, want SKIPPED", proxyID, proxy.State)
		}
	}
	for _, gcID := range grandchildIDs {
		gc, err := st.GetSubmission(ctx, gcID)
		if err != nil {
			t.Fatalf("GetSubmission(grandchild %s): %v", gcID, err)
		}
		if gc.State != model.SubmissionStateCancelled {
			t.Errorf("grandchild %s state = %q, want CANCELLED", gcID, gc.State)
		}
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateCancelled {
		t.Errorf("sub.State = %q, want CANCELLED (must not flip)", sub.State)
	}
}
