package cwlrunner

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// TestResolveStepInputs_PickValue covers GoWe issue #197: the standalone
// cwl-runner's resolveStepInputs must apply step-level pickValue the same
// way the server-side stepinput package does, so `bin/cwl-runner` and the
// server agree on step-level pickValue semantics.
func TestResolveStepInputs_PickValue(t *testing.T) {
	tests := []struct {
		name           string
		stepInput      cwl.StepInput
		workflowInputs map[string]any
		stepOutputs    map[string]map[string]any
		wantValue      any
		wantErrSubstr  string
	}{
		{
			name: "first_non_null over multi-source step outputs",
			stepInput: cwl.StepInput{
				Sources:   []string{"step_a/out", "step_b/out"},
				PickValue: "first_non_null",
			},
			stepOutputs: map[string]map[string]any{
				"step_b": {"out": "from-b"},
			},
			wantValue: "from-b",
		},
		{
			name: "the_only_non_null errors on multiple non-null values",
			stepInput: cwl.StepInput{
				Sources:   []string{"step_a/out", "step_b/out"},
				PickValue: "the_only_non_null",
			},
			stepOutputs: map[string]map[string]any{
				"step_a": {"out": "from-a"},
				"step_b": {"out": "from-b"},
			},
			wantErrSubstr: "multiple non-null values",
		},
		{
			name: "all sources null with first_non_null errors",
			stepInput: cwl.StepInput{
				Sources:   []string{"step_a/out", "step_b/out"},
				PickValue: "first_non_null",
			},
			stepOutputs:   map[string]map[string]any{},
			wantErrSubstr: "all sources are null",
		},
		{
			name: "single source array is picked across",
			stepInput: cwl.StepInput{
				Sources:   []string{"items"},
				PickValue: "first_non_null",
			},
			workflowInputs: map[string]any{"items": []any{nil, "x"}},
			wantValue:      "x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := cwl.Step{
				In: map[string]cwl.StepInput{"picked": tt.stepInput},
			}
			workflowInputs := tt.workflowInputs
			if workflowInputs == nil {
				workflowInputs = map[string]any{}
			}
			stepOutputs := tt.stepOutputs
			if stepOutputs == nil {
				stepOutputs = map[string]map[string]any{}
			}

			resolved, err := resolveStepInputs(step, workflowInputs, stepOutputs, "", nil)
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (resolved=%v)", tt.wantErrSubstr, resolved)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved["picked"] != tt.wantValue {
				t.Errorf("resolved[picked] = %#v, want %#v", resolved["picked"], tt.wantValue)
			}
		})
	}
}
