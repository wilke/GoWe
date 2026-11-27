package tokencheck

import (
	"testing"

	"github.com/me/gowe/pkg/model"
)

// ServiceJobRequirements refuses cwl:requirements (the key, whatever its
// value) for a service workflow only.
func TestServiceJobRequirements(t *testing.T) {
	service := &model.Workflow{Steps: []model.Step{{ID: "s", Hints: &model.StepHints{
		WorkerGroup: "llm", DockerImage: "vllm.sif", Service: &model.ServiceSpec{PortInput: "port"},
	}}}}
	ordinary := &model.Workflow{Steps: []model.Step{{ID: "s", Hints: &model.StepHints{WorkerGroup: "llm"}}}}
	tests := []struct {
		name    string
		wf      *model.Workflow
		inputs  map[string]any
		refused bool
	}{
		{"service, requirements", service, map[string]any{"cwl:requirements": []any{map[string]any{"class": "DockerRequirement"}}}, true},
		{"service, empty list", service, map[string]any{"cwl:requirements": []any{}}, true},
		{"service, null", service, map[string]any{"cwl:requirements": nil}, true},
		{"service, no key", service, map[string]any{"port": 8000}, false},
		{"ordinary, requirements", ordinary, map[string]any{"cwl:requirements": []any{map[string]any{"class": "DockerRequirement"}}}, false},
		{"no workflow", nil, map[string]any{"cwl:requirements": []any{}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServiceJobRequirements(tt.wf, tt.inputs)
			if (got != nil) != tt.refused {
				t.Fatalf("ServiceJobRequirements = %+v, want refused %v", got, tt.refused)
			}
			if got != nil && (got.Code != model.ErrValidation || got.Message != ServiceJobRequirementsRefused) {
				t.Errorf("error = %+v, want VALIDATION %q", got, ServiceJobRequirementsRefused)
			}
		})
	}

	// Check refuses it before anything else, also without raw CWL.
	c := &Checker{}
	if apiErr, _ := c.Check(t.Context(), service, map[string]any{"cwl:requirements": []any{}}, nil, true); apiErr == nil ||
		apiErr.Message != ServiceJobRequirementsRefused {
		t.Errorf("Check = %+v, want the service refusal", apiErr)
	}
}
