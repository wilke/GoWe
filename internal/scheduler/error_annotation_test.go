package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
//
// It also covers GoWe issue #205, the two leftovers disclosed by #200/#204
// (PR #204, commit 2230698):
//
//  3. dispatchStep's token-expiry and preflight no-capable-worker branches
//     must set StepInstance.Error, not just flip the state to FAILED.
//  4. buildSubmissionError's task-lookup loop must not clobber a specific,
//     non-empty StepInstance.Error with the generic "step task failed"
//     message when a FAILED task also exists under the step.

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

// TestDispatchStep_TokenExpiry_SetsStepInstanceError covers GoWe #205 item 1:
// dispatchStep's token-expiry branch (~line 616) previously flipped the step
// to FAILED with only a log line. It must now persist a concise, actionable
// StepInstance.Error that also surfaces through buildSubmissionError.
func TestDispatchStep_TokenExpiry_SetsStepInstanceError(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// UpdateSubmission does not persist TokenExpiry (it's an insert-only
	// column), so the submission is created directly with an already-expired
	// token rather than going through createPipeline + a follow-up update.
	wfID := "wf_" + uuid.New().String()
	subID := "sub_" + uuid.New().String()
	wf := &model.Workflow{
		ID:         wfID,
		Name:       "test-workflow",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{
				ID: "step1",
				ToolInline: &model.Tool{
					ID:          "tool1",
					Class:       "CommandLineTool",
					BaseCommand: []string{"echo", "hello"},
				},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		TokenExpiry:  now.Add(-time.Hour),
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	initialSI := &model.StepInstance{
		ID:           "si_" + uuid.New().String(),
		SubmissionID: subID,
		StepID:       "step1",
		State:        model.StepStateWaiting,
		Outputs:      map[string]any{},
		CreatedAt:    now,
	}
	if err := st.CreateStepInstance(ctx, initialSI); err != nil {
		t.Fatalf("CreateStepInstance: %v", err)
	}

	got := runToTerminal(t, sched, st, subID, 5)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}

	si := getStepInstancesByStep(t, st, subID)["step1"]
	if si.State != model.StepStateFailed {
		t.Fatalf("step1.State = %s, want FAILED", si.State)
	}
	if si.Error == "" {
		t.Fatal("step1 step instance Error is empty, want a persisted diagnostic")
	}
	if !strings.Contains(si.Error, "token expired") {
		t.Errorf("step1 step instance Error = %q, want it to mention token expiry", si.Error)
	}

	if got.Error == nil || got.Error.Message == "" {
		t.Fatal("submission Error is nil/empty, want a diagnostic message")
	}
	if !strings.Contains(got.Error.Message, "token expired") {
		t.Errorf("submission Error.Message = %q, want it to surface the token-expiry reason", got.Error.Message)
	}
}

// TestDispatchStep_PreflightNoCapableWorker_SetsStepInstanceError covers GoWe
// #205 item 1: dispatchStep's preflight no-capable-worker branch (~line 701),
// reached once PreflightDeferralTicks deferrals are exhausted, previously
// flipped the step to FAILED with only a log line ("reason" was computed but
// discarded). It must now reuse that reason in a persisted StepInstance.Error.
func TestDispatchStep_PreflightNoCapableWorker_SetsStepInstanceError(t *testing.T) {
	sched, st := testSetup(t)

	sched.config.DefaultExecutor = "worker"
	sched.config.PreflightDeferralTicks = 1
	sched.config.MaxRetries = 0

	steps := []model.Step{
		{
			ID: "container_step",
			ToolInline: &model.Tool{
				ID:          "tool1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
			Hints: &model.StepHints{
				DockerImage: "alpine",
			},
		},
	}

	_, subID := createPipeline(t, st, steps, map[string]any{}, 0)

	got := runToTerminal(t, sched, st, subID, 5)
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", got.State)
	}

	si := getStepInstancesByStep(t, st, subID)["container_step"]
	if si.State != model.StepStateFailed {
		t.Fatalf("container_step.State = %s, want FAILED", si.State)
	}
	if si.Error == "" {
		t.Fatal("container_step step instance Error is empty, want a persisted diagnostic")
	}
	if !strings.Contains(si.Error, "no capable worker") || !strings.Contains(si.Error, "no online workers") {
		t.Errorf("container_step step instance Error = %q, want it to reference the no-capable-worker reason", si.Error)
	}

	if got.Error == nil || got.Error.Message == "" {
		t.Fatal("submission Error is nil/empty, want a diagnostic message")
	}
	if !strings.Contains(got.Error.Message, "no capable worker") {
		t.Errorf("submission Error.Message = %q, want it to surface the no-capable-worker reason", got.Error.Message)
	}
}

// TestBuildSubmissionError_MessagePrecedence covers GoWe #205 item 2:
// buildSubmissionError's task-lookup loop must not overwrite Message with the
// generic "step task failed" text when the step instance already carries a
// specific, non-empty Error — while still enriching Context (exit code,
// stderr) from the failed task. When StepInstance.Error is empty, the
// existing generic-message behavior (pinned by earlier tests) is unchanged.
func TestBuildSubmissionError_MessagePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		siError string
	}{
		{
			name:    "specific_si_error_wins_over_generic_task_message",
			siError: "scatter iteration 2 valueFrom: boom",
		},
		{
			name:    "empty_si_error_falls_back_to_generic_task_message",
			siError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			ctx := context.Background()

			_, subID := createPipeline(t, st, []model.Step{{ID: "step1"}}, map[string]any{}, 0)

			si := getStepInstancesByStep(t, st, subID)["step1"]
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.Error = tt.siError
			si.CompletedAt = &now
			if err := st.UpdateStepInstance(ctx, si); err != nil {
				t.Fatalf("UpdateStepInstance: %v", err)
			}

			exitCode := 1
			task := &model.Task{
				ID:             "task_" + uuid.New().String(),
				SubmissionID:   subID,
				StepID:         "step1",
				StepInstanceID: si.ID,
				State:          model.TaskStateFailed,
				ExecutorType:   model.ExecutorTypeLocal,
				Inputs:         map[string]any{},
				Outputs:        map[string]any{},
				ScatterIndex:   -1,
				ExitCode:       &exitCode,
				Stderr:         "boom: exit 1",
			}
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}

			subErr := sched.buildSubmissionError(ctx, []*model.StepInstance{si})
			if subErr == nil {
				t.Fatal("buildSubmissionError returned nil")
			}

			if tt.siError != "" {
				if !strings.Contains(subErr.Message, tt.siError) {
					t.Errorf("Message = %q, want it to contain the specific si.Error %q", subErr.Message, tt.siError)
				}
				if strings.Contains(subErr.Message, "task failed") {
					t.Errorf("Message = %q, generic 'task failed' text leaked through despite a specific si.Error (#205)", subErr.Message)
				}
			} else {
				// Existing pinned behavior: empty si.Error falls back to the
				// generic task-derived message.
				wantMsg := fmt.Sprintf("step '%s' task failed with exit code %d", "step1", exitCode)
				if subErr.Message != wantMsg {
					t.Errorf("Message = %q, want generic %q", subErr.Message, wantMsg)
				}
			}

			// Context must still be enriched from the task regardless of
			// which branch set Message.
			if subErr.Context == nil || subErr.Context.TaskID != task.ID {
				t.Errorf("Context.TaskID = %v, want %q (task-derived enrichment must survive)", subErr.Context, task.ID)
			}
			if subErr.Context.ExitCode == nil || *subErr.Context.ExitCode != exitCode {
				t.Errorf("Context.ExitCode = %v, want %d", subErr.Context.ExitCode, exitCode)
			}
			if subErr.Context.Stderr != task.Stderr {
				t.Errorf("Context.Stderr = %q, want %q", subErr.Context.Stderr, task.Stderr)
			}
		})
	}
}
