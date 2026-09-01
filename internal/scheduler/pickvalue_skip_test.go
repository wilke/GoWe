package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// This file covers the three end-to-end shapes from GoWe issue #197:
//
//  1. A step-level pickValue over multiple sources, where one producer is
//     when-skipped, must resolve to the picked value and the consumer must
//     run rather than being cascade-skipped (both "which producer is
//     skipped" orderings must behave symmetrically, since that symmetry is
//     exactly what the buggy computeDependsOn/comma-joined-Source path
//     broke).
//  2. A picked value that is itself an array (from a scattered/conditional
//     producer choice) must scatter correctly downstream instead of
//     corrupting the scatter shape.
//  3. pickValue: first_non_null over all-null sources must FAIL the step
//     with a persisted, human-readable error — not silently.

// runToTerminal ticks the scheduler until the submission reaches a terminal
// state (COMPLETED, FAILED, or CANCELLED) or maxTicks is exceeded.
func runToTerminal(t *testing.T, sched *Loop, st store.Store, subID string, maxTicks int) *model.Submission {
	t.Helper()
	ctx := context.Background()
	var sub *model.Submission
	for tick := 1; tick <= maxTicks; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		var err error
		sub, err = st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("tick %d: GetSubmission: %v", tick, err)
		}
		switch sub.State {
		case model.SubmissionStateCompleted, model.SubmissionStateFailed, model.SubmissionStateCancelled:
			return sub
		}
	}
	t.Fatalf("submission %s did not reach a terminal state after %d ticks (last state=%s)", subID, maxTicks, sub.State)
	return nil
}

// pickValueSkipCascadeCWL: two mutually-exclusive (via `when`) producers
// feed a consumer through a step-level pickValue: first_non_null. `flag`
// controls which producer runs; the other is skipped and must NOT cascade
// a skip to the consumer, and the consumer must see the picked value.
const pickValueSkipCascadeCWL = `
cwlVersion: v1.2
class: Workflow
requirements:
  - class: MultipleInputFeatureRequirement
  - class: InlineJavascriptRequirement
inputs:
  flag: boolean
outputs:
  final:
    type: string
    outputSource: consumer/out
steps:
  step_a:
    when: $(inputs.flag)
    in:
      flag: flag
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        flag: boolean
      outputs:
        out: string
      expression: "${return {'out': 'from-a'};}"
  step_b:
    when: $(!inputs.flag)
    in:
      flag: flag
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        flag: boolean
      outputs:
        out: string
      expression: "${return {'out': 'from-b'};}"
  consumer:
    in:
      picked:
        source: [step_a/out, step_b/out]
        pickValue: first_non_null
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        picked: string
      outputs:
        out: string
      expression: "${return {'out': inputs.picked};}"
`

func TestPickValue_SkipCascade_ConsumerRuns(t *testing.T) {
	tests := []struct {
		name string
		flag bool
		want string
	}{
		// Mode a: step_a runs, step_b is when-skipped.
		{name: "mode_a_skip_step_b", flag: true, want: "from-a"},
		// Mode b: step_b runs, step_a is when-skipped. This is the
		// asymmetric case that the comma-joined-Source computeDependsOn bug
		// broke (only the first source was ever registered as a dependency).
		{name: "mode_b_skip_step_a", flag: false, want: "from-b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			subID := registerGraphWorkflow(t, st, pickValueSkipCascadeCWL,
				map[string]any{"flag": tt.flag}, nil, "")

			sub := runToTerminal(t, sched, st, subID, 10)
			if sub.State != model.SubmissionStateCompleted {
				msg := ""
				if sub.Error != nil {
					msg = sub.Error.Message
				}
				t.Fatalf("submission state = %s, want COMPLETED (error: %s)", sub.State, msg)
			}

			got, _ := sub.Outputs["final"].(string)
			if got != tt.want {
				t.Errorf("outputs[final] = %q, want %q", got, tt.want)
			}

			steps := getStepInstancesByStep(t, st, subID)
			skippedID := "step_b"
			ranID := "step_a"
			if !tt.flag {
				skippedID, ranID = "step_a", "step_b"
			}
			if steps[skippedID].State != model.StepStateSkipped {
				t.Errorf("%s.State = %s, want SKIPPED", skippedID, steps[skippedID].State)
			}
			if steps[ranID].State != model.StepStateCompleted {
				t.Errorf("%s.State = %s, want COMPLETED", ranID, steps[ranID].State)
			}
			// The consumer must NOT have been cascade-skipped.
			if steps["consumer"].State != model.StepStateCompleted {
				t.Errorf("consumer.State = %s, want COMPLETED (must not cascade-skip on a SKIPPED dependency)", steps["consumer"].State)
			}
		})
	}
}

// pickValueScatterCWL: a step-level pickValue over multiple sources selects
// an entire array (one candidate is null via `when`, the other is the real
// array), and that picked array is used as the scatter key downstream.
// Pre-fix, the scatter key would see the raw multi-source shape
// [null, [...]] instead of the flat picked array.
const pickValueScatterCWL = `
cwlVersion: v1.2
class: Workflow
requirements:
  - class: MultipleInputFeatureRequirement
  - class: ScatterFeatureRequirement
  - class: InlineJavascriptRequirement
inputs:
  base_items: string[]
outputs:
  results:
    type: string[]
    outputSource: scatter_consumer/result
steps:
  gate:
    when: $(false)
    in:
      items: base_items
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        items:
          type: string[]
      outputs:
        out:
          type: string[]
      expression: "${return {'out': inputs.items};}"
  scatter_consumer:
    in:
      item:
        source: [gate/out, base_items]
        pickValue: first_non_null
    scatter: item
    out: [result]
    run:
      class: ExpressionTool
      inputs:
        item: string
      outputs:
        result: string
      expression: "${return {'result': inputs.item + '-done'};}"
`

func TestPickValue_PickedArrayAsScatterKey(t *testing.T) {
	sched, st := testSetup(t)
	subID := registerGraphWorkflow(t, st, pickValueScatterCWL,
		map[string]any{"base_items": []any{"x", "y", "z"}}, nil, "")

	sub := runToTerminal(t, sched, st, subID, 15)
	if sub.State != model.SubmissionStateCompleted {
		msg := ""
		if sub.Error != nil {
			msg = sub.Error.Message
		}
		t.Fatalf("submission state = %s, want COMPLETED (error: %s)", sub.State, msg)
	}

	results, ok := sub.Outputs["results"].([]any)
	if !ok {
		t.Fatalf("outputs[results] = %v (%T), want []any", sub.Outputs["results"], sub.Outputs["results"])
	}
	want := []string{"x-done", "y-done", "z-done"}
	if len(results) != len(want) {
		t.Fatalf("results length = %d, want %d (got %v)", len(results), len(want), results)
	}
	for i, w := range want {
		if got, _ := results[i].(string); got != w {
			t.Errorf("results[%d] = %q, want %q", i, got, w)
		}
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_consumer"]
	if si.ScatterCount != 3 {
		t.Errorf("scatter_consumer.ScatterCount = %d, want 3 (scatter must run over the flat picked array, not the raw multi-source shape)", si.ScatterCount)
	}
}

// pickValueAllNullCWL: both candidate producers are when-skipped, so
// pickValue: first_non_null has nothing to pick — this must fail the step
// loudly with a persisted diagnostic, not silently.
const pickValueAllNullCWL = `
cwlVersion: v1.2
class: Workflow
requirements:
  - class: MultipleInputFeatureRequirement
  - class: InlineJavascriptRequirement
inputs: {}
outputs:
  final:
    type: string?
    outputSource: consumer/out
steps:
  step_a:
    when: $(false)
    in: {}
    out: [out]
    run:
      class: ExpressionTool
      inputs: {}
      outputs:
        out: string
      expression: "${return {'out': 'never-a'};}"
  step_b:
    when: $(false)
    in: {}
    out: [out]
    run:
      class: ExpressionTool
      inputs: {}
      outputs:
        out: string
      expression: "${return {'out': 'never-b'};}"
  consumer:
    in:
      picked:
        source: [step_a/out, step_b/out]
        pickValue: first_non_null
    out: [out]
    run:
      class: ExpressionTool
      inputs:
        picked: string
      outputs:
        out: string
      expression: "${return {'out': inputs.picked};}"
`

func TestPickValue_AllNull_StepFailsLoudly(t *testing.T) {
	sched, st := testSetup(t)
	subID := registerGraphWorkflow(t, st, pickValueAllNullCWL, map[string]any{}, nil, "")

	sub := runToTerminal(t, sched, st, subID, 10)
	if sub.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", sub.State)
	}
	if sub.Error == nil || sub.Error.Message == "" {
		t.Fatal("submission Error is nil/empty, want a diagnostic message")
	}
	if !strings.Contains(sub.Error.Message, "consumer") {
		t.Errorf("submission Error.Message = %q, want it to mention the failing step", sub.Error.Message)
	}

	si := getStepInstancesByStep(t, st, subID)["consumer"]
	if si.State != model.StepStateFailed {
		t.Fatalf("consumer.State = %s, want FAILED", si.State)
	}
	if si.Error == "" {
		t.Error("consumer step instance Error is empty, want a persisted diagnostic (not a silent failure)")
	}
	if !strings.Contains(si.Error, "first_non_null") && !strings.Contains(si.Error, "null") {
		t.Errorf("consumer step instance Error = %q, want it to reference the pickValue/null failure", si.Error)
	}
}
