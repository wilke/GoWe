package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// This file covers GoWe issue #200, the two leftovers disclosed by #199
// (PR #199, commit 4eb9e27):
//
//  1. dispatchScatterSubWorkflow / dispatchSubWorkflowStep (and the shared
//     failSubworkflowProxy helper) failure branches must set
//     StepInstance.Error, not just flip the state to FAILED silently.
//  2. ExpressionTool scatter per-iteration 'when' evaluation failure must
//     fail the step (CWL spec: non-boolean 'when' must fail), matching the
//     CommandLineTool scatter path — not warn-and-continue.

// valueFromErrorScatterSubwfCWL: scatterSubwfCWL with a per-iteration
// valueFrom expression that throws (calls an undefined function), so
// applyScatterValueFrom fails during dispatchScatterSubWorkflow before any
// proxy task is persisted.
const valueFromErrorScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
      - class: InlineJavascriptRequirement
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
          letter:
            source: letters
            valueFrom: $(nonexistent_fn())
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

// whenEvalErrorScatterSubwfCWL: scatterSubwfCWL with a per-iteration 'when'
// that evaluates to a non-boolean, so evaluateWhenForScatterIterationFromSteps
// fails during dispatchScatterSubWorkflow before any proxy task is persisted.
const whenEvalErrorScatterSubwfCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
      - class: SubworkflowFeatureRequirement
      - class: InlineJavascriptRequirement
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
        when: '$(1)'
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

// exprToolScatterWhenErrorCWL: a scatter step over a plain (non-subworkflow)
// ExpressionTool whose per-iteration 'when' evaluates to a non-boolean. Pre-#200
// this logged a warning and ran the iteration anyway (silently ignoring the
// broken 'when'); CWL spec requires a non-boolean 'when' to fail the step.
const exprToolScatterWhenErrorCWL = `
cwlVersion: v1.2
class: Workflow
requirements:
  - class: ScatterFeatureRequirement
  - class: InlineJavascriptRequirement
inputs:
  items: string[]
outputs:
  results:
    type: string[]
    outputSource: scatter_step/out
steps:
  scatter_step:
    scatter: item
    when: '$(1)'
    in:
      item: items
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        item: string
      outputs:
        out: string
      expression: "${return {'out': inputs.item + '-done'};}"
`

// TestDispatchScatterSubWorkflow_ErrorAnnotation covers the three
// dispatchScatterSubWorkflow failure branches that previously flipped the
// step to FAILED without setting StepInstance.Error: a non-array scatter
// input, a per-iteration valueFrom failure, and a per-iteration 'when'
// evaluation failure. All three fail before any proxy task is persisted, so
// buildSubmissionError's message must come straight from StepInstance.Error.
func TestDispatchScatterSubWorkflow_ErrorAnnotation(t *testing.T) {
	tests := []struct {
		name          string
		cwl           string
		inputs        map[string]any
		wantErrSubstr string
	}{
		{
			name:          "scatter_input_not_array",
			cwl:           scatterSubwfCWL,
			inputs:        map[string]any{"letters": "not-an-array"},
			wantErrSubstr: "not an array",
		},
		{
			name:          "valueFrom_error",
			cwl:           valueFromErrorScatterSubwfCWL,
			inputs:        map[string]any{"letters": []any{"a", "b"}},
			wantErrSubstr: "valueFrom",
		},
		{
			name:          "when_eval_error",
			cwl:           whenEvalErrorScatterSubwfCWL,
			inputs:        map[string]any{"letters": []any{"a", "b"}},
			wantErrSubstr: "when evaluation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)

			subID := registerGraphWorkflow(t, st, tt.cwl, tt.inputs, nil, "")

			sub := runToTerminal(t, sched, st, subID, 10)
			if sub.State != model.SubmissionStateFailed {
				t.Fatalf("submission state = %s, want FAILED", sub.State)
			}

			si := getStepInstancesByStep(t, st, subID)["scatter_step"]
			if si.State != model.StepStateFailed {
				t.Fatalf("scatter_step.State = %s, want FAILED", si.State)
			}
			if si.Error == "" {
				t.Fatal("scatter_step step instance Error is empty, want a persisted diagnostic")
			}
			if !strings.Contains(si.Error, tt.wantErrSubstr) {
				t.Errorf("scatter_step step instance Error = %q, want it to contain %q", si.Error, tt.wantErrSubstr)
			}

			if sub.Error == nil || sub.Error.Message == "" {
				t.Fatal("submission Error is nil/empty, want a diagnostic message")
			}
			if !strings.Contains(sub.Error.Message, "scatter_step") {
				t.Errorf("submission Error.Message = %q, want it to mention the failing step", sub.Error.Message)
			}
			if !strings.Contains(sub.Error.Message, tt.wantErrSubstr) {
				t.Errorf("submission Error.Message = %q, want it to contain %q (surfaced from StepInstance.Error, no task exists yet under this step)",
					sub.Error.Message, tt.wantErrSubstr)
			}
		})
	}
}

// TestFailSubworkflowProxy_SetsStepInstanceError verifies that
// failSubworkflowProxy — the shared helper used by dispatchSubWorkflowStep,
// dispatchScatterSubWorkflow, and repairSubworkflowChild for a non-retryable
// "create child submission" failure — now sets StepInstance.Error (previously
// only task.Stderr carried the reason), and that the diagnostic surfaces
// through buildSubmissionError.
func TestFailSubworkflowProxy_SetsStepInstanceError(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflow(t, st, scatterSubwfCWL,
		map[string]any{"letters": []any{"x"}}, nil, "")

	dispatchOnly(t, sched)

	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy task, got %d", len(proxies))
	}
	task := proxies[0]
	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateDispatched {
		t.Fatalf("scatter_step.State = %s, want DISPATCHED before failSubworkflowProxy", si.State)
	}

	const reason = "create child submission: simulated store failure"
	sched.failSubworkflowProxy(ctx, task, si, reason)

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if gotTask.State != model.TaskStateFailed {
		t.Errorf("proxy task State = %s, want FAILED", gotTask.State)
	}
	if !strings.Contains(gotTask.Stderr, reason) {
		t.Errorf("proxy task Stderr = %q, want it to contain %q", gotTask.Stderr, reason)
	}

	gotSI := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if gotSI.State != model.StepStateFailed {
		t.Fatalf("scatter_step.State = %s, want FAILED", gotSI.State)
	}
	if gotSI.Error == "" {
		t.Fatal("scatter_step step instance Error is empty, want the failSubworkflowProxy reason persisted")
	}
	if !strings.Contains(gotSI.Error, reason) {
		t.Errorf("scatter_step step instance Error = %q, want it to contain %q", gotSI.Error, reason)
	}

	// The diagnostic must surface through buildSubmissionError somewhere
	// (Message and/or the failed task's Context.Stderr).
	subErr := sched.buildSubmissionError(ctx, []*model.StepInstance{gotSI})
	if subErr == nil {
		t.Fatal("buildSubmissionError returned nil")
	}
	surfaced := strings.Contains(subErr.Message, reason) ||
		(subErr.Context != nil && strings.Contains(subErr.Context.Stderr, reason))
	if !surfaced {
		t.Errorf("buildSubmissionError result %+v does not surface reason %q", subErr, reason)
	}
}

// TestExpressionToolScatter_WhenEvalError_FailsStep verifies GoWe #200 item 2:
// a per-iteration 'when' evaluation failure in an ExpressionTool scatter must
// fail the step (with a persisted StepInstance.Error), matching the
// CommandLineTool scatter path — not warn-and-continue, which previously ran
// the iteration anyway and let the step complete despite the broken 'when'.
func TestExpressionToolScatter_WhenEvalError_FailsStep(t *testing.T) {
	sched, st := testSetup(t)

	subID := registerGraphWorkflow(t, st, exprToolScatterWhenErrorCWL,
		map[string]any{"items": []any{"a", "b"}}, nil, "")

	sub := runToTerminal(t, sched, st, subID, 10)
	if sub.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED (a non-boolean 'when' must fail the step, not run it)", sub.State)
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateFailed {
		t.Fatalf("scatter_step.State = %s, want FAILED", si.State)
	}
	if si.Error == "" {
		t.Fatal("scatter_step step instance Error is empty, want a persisted diagnostic")
	}
	if !strings.Contains(si.Error, "when evaluation") {
		t.Errorf("scatter_step step instance Error = %q, want it to reference the when-evaluation failure", si.Error)
	}

	if sub.Error == nil || sub.Error.Message == "" {
		t.Fatal("submission Error is nil/empty, want a diagnostic message")
	}
	if !strings.Contains(sub.Error.Message, "scatter_step") {
		t.Errorf("submission Error.Message = %q, want it to mention the failing step", sub.Error.Message)
	}
}
