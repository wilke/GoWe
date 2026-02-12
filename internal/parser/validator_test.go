package parser

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

func testValidator() *Validator {
	return NewValidator(testParser().logger)
}

// validGraph returns a minimal valid GraphDocument for testing.
func validGraph() *cwl.GraphDocument {
	return &cwl.GraphDocument{
		CWLVersion: "v1.2",
		Workflow: &cwl.Workflow{
			ID:    "main",
			Class: "Workflow",
			Inputs: map[string]cwl.InputParam{
				"input1": {Type: "File"},
			},
			Outputs: map[string]cwl.OutputParam{
				"output1": {Type: "File", OutputSource: "step1/out"},
			},
			Steps: map[string]cwl.Step{
				"step1": {
					Run: "#tool1",
					In:  map[string]cwl.StepInput{"x": {Source: "input1"}},
					Out: []string{"out"},
				},
			},
		},
		Tools: map[string]*cwl.CommandLineTool{
			"tool1": {ID: "tool1", Class: "CommandLineTool"},
		},
	}
}

func TestValidate_ValidGraph(t *testing.T) {
	v := testValidator()
	if err := v.Validate(validGraph()); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidate_ValidPipeline(t *testing.T) {
	p := testParser()
	v := testValidator()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	if apiErr := v.Validate(graph); apiErr != nil {
		t.Errorf("expected valid, got %v", apiErr)
	}
}

func TestValidate_MissingCWLVersion(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.CWLVersion = ""
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "cwlVersion") {
		t.Errorf("expected cwlVersion error, got %v", apiErr.Details)
	}
}

func TestValidate_UnsupportedVersion(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.CWLVersion = "v1.0"
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "unsupported") {
		t.Errorf("expected unsupported version error, got %v", apiErr.Details)
	}
}

func TestValidate_NoInputs(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Inputs = map[string]cwl.InputParam{}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "inputs") {
		t.Errorf("expected inputs error, got %v", apiErr.Details)
	}
}

func TestValidate_NoSteps(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps = map[string]cwl.Step{}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "steps") {
		t.Errorf("expected steps error, got %v", apiErr.Details)
	}
}

func TestValidate_NoOutputs(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Outputs = map[string]cwl.OutputParam{}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "outputs") {
		t.Errorf("expected outputs error, got %v", apiErr.Details)
	}
}

func TestValidate_InputMissingType(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Inputs["bad_input"] = cwl.InputParam{Type: ""}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "inputs.bad_input.type") {
		t.Errorf("expected input type error, got %v", apiErr.Details)
	}
}

func TestValidate_StepMissingRun(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["bad_step"] = cwl.Step{
		Run: "",
		In:  map[string]cwl.StepInput{"x": {Source: "input1"}},
		Out: []string{"out"},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "steps.bad_step.run") {
		t.Errorf("expected run error, got %v", apiErr.Details)
	}
}

func TestValidate_StepNoOutputs(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["no_out"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Source: "input1"}},
		Out: []string{},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldError(apiErr.Details, "steps.no_out.out") {
		t.Errorf("expected out error, got %v", apiErr.Details)
	}
}

func TestValidate_InvalidSourceRef(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["step1"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Source: "nonexistent/output"}},
		Out: []string{"out"},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "does not match") {
		t.Errorf("expected source mismatch error, got %v", apiErr.Details)
	}
}

func TestValidate_InvalidOutputSource(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Outputs["output1"] = cwl.OutputParam{
		Type:         "File",
		OutputSource: "badstep/output",
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "does not match") {
		t.Errorf("expected outputSource error, got %v", apiErr.Details)
	}
}

func TestValidate_MissingOutputSource(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Outputs["output1"] = cwl.OutputParam{
		Type:         "File",
		OutputSource: "",
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "missing outputSource") {
		t.Errorf("expected missing outputSource error, got %v", apiErr.Details)
	}
}

func TestValidate_ToolRefNotInGraph(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["step1"] = cwl.Step{
		Run: "#missing-tool",
		In:  map[string]cwl.StepInput{"x": {Source: "input1"}},
		Out: []string{"out"},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "not found in $graph") {
		t.Errorf("expected tool ref error, got %v", apiErr.Details)
	}
}

func TestValidate_CycleDetected(t *testing.T) {
	v := testValidator()
	g := &cwl.GraphDocument{
		CWLVersion: "v1.2",
		Workflow: &cwl.Workflow{
			ID:    "main",
			Class: "Workflow",
			Inputs: map[string]cwl.InputParam{
				"input1": {Type: "File"},
			},
			Outputs: map[string]cwl.OutputParam{
				"output1": {Type: "File", OutputSource: "a/out"},
			},
			Steps: map[string]cwl.Step{
				"a": {
					Run: "#tool1",
					In:  map[string]cwl.StepInput{"x": {Source: "b/out"}},
					Out: []string{"out"},
				},
				"b": {
					Run: "#tool1",
					In:  map[string]cwl.StepInput{"x": {Source: "a/out"}},
					Out: []string{"out"},
				},
			},
		},
		Tools: map[string]*cwl.CommandLineTool{
			"tool1": {ID: "tool1", Class: "CommandLineTool"},
		},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "cycle") {
		t.Errorf("expected cycle error, got %v", apiErr.Details)
	}
}

func TestValidate_StepInputNoSourceNoDefault(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["step1"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Source: "", Default: nil}},
		Out: []string{"out"},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "no source and no default") {
		t.Errorf("expected no source error, got %v", apiErr.Details)
	}
}

// --- Test helpers ---

func hasFieldError(errs []model.FieldError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

func hasFieldErrorMsg(errs []model.FieldError, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}
