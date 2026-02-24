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
		CWLVersion:    "v1.2",
		OriginalClass: "Workflow",
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
					In:  map[string]cwl.StepInput{"x": {Sources: []string{"input1"}}},
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
	g.CWLVersion = "v0.9" // truly unsupported version
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "unsupported") {
		t.Errorf("expected unsupported version error, got %v", apiErr.Details)
	}
}

func TestValidate_SupportedVersions(t *testing.T) {
	v := testValidator()
	for _, ver := range []string{"v1.0", "v1.1", "v1.2", "draft-3"} {
		g := validGraph()
		g.CWLVersion = ver
		if apiErr := v.Validate(g); apiErr != nil {
			t.Errorf("version %s should be supported, got %v", ver, apiErr)
		}
	}
}

func TestValidate_NoInputs_Allowed(t *testing.T) {
	// CWL v1.2 allows workflows with no inputs (they can have defaults).
	v := testValidator()
	g := &cwl.GraphDocument{
		CWLVersion:    "v1.2",
		OriginalClass: "Workflow",
		Workflow: &cwl.Workflow{
			ID:      "main",
			Class:   "Workflow",
			Inputs:  map[string]cwl.InputParam{},
			Outputs: map[string]cwl.OutputParam{"out": {Type: "File", OutputSource: "step1/out"}},
			Steps: map[string]cwl.Step{
				"step1": {
					Run: "#tool1",
					In:  map[string]cwl.StepInput{"x": {Default: "value"}}, // use default, not source
					Out: []string{"out"},
				},
			},
		},
		Tools: map[string]*cwl.CommandLineTool{
			"tool1": {ID: "tool1", Class: "CommandLineTool"},
		},
	}
	if apiErr := v.Validate(g); apiErr != nil {
		t.Errorf("expected valid, got %v", apiErr)
	}
}

func TestValidate_NoSteps_PassthroughWorkflow(t *testing.T) {
	// CWL v1.2 allows passthrough workflows (outputs directly from inputs).
	v := testValidator()
	g := &cwl.GraphDocument{
		CWLVersion:    "v1.2",
		OriginalClass: "Workflow",
		Workflow: &cwl.Workflow{
			ID:      "main",
			Class:   "Workflow",
			Inputs:  map[string]cwl.InputParam{"in1": {Type: "File"}},
			Outputs: map[string]cwl.OutputParam{"out": {Type: "File", OutputSource: "in1"}}, // passthrough
			Steps:   map[string]cwl.Step{},
		},
		Tools: map[string]*cwl.CommandLineTool{},
	}
	if apiErr := v.Validate(g); apiErr != nil {
		t.Errorf("expected valid, got %v", apiErr)
	}
}

func TestValidate_NoOutputs_Allowed(t *testing.T) {
	// CWL v1.2 allows workflows with no outputs (side-effect only).
	v := testValidator()
	g := validGraph()
	g.Workflow.Outputs = map[string]cwl.OutputParam{}
	// Also need to remove step output references from the workflow.
	g.Workflow.Steps["step1"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Sources: []string{"input1"}}},
		Out: []string{},
	}
	apiErr := v.Validate(g)
	if apiErr != nil {
		t.Errorf("workflows with no outputs should be valid, got %v", apiErr)
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
		In:  map[string]cwl.StepInput{"x": {Sources: []string{"input1"}}},
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

func TestValidate_StepNoOutputs_Allowed(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["no_out"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Sources: []string{"input1"}}},
		Out: []string{},
	}
	apiErr := v.Validate(g)
	if apiErr != nil {
		t.Fatalf("steps with no outputs should be allowed, got %v", apiErr.Details)
	}
}

func TestValidate_InvalidSourceRef(t *testing.T) {
	v := testValidator()
	g := validGraph()
	g.Workflow.Steps["step1"] = cwl.Step{
		Run: "#tool1",
		In:  map[string]cwl.StepInput{"x": {Sources: []string{"nonexistent/output"}}},
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
		In:  map[string]cwl.StepInput{"x": {Sources: []string{"input1"}}},
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
					In:  map[string]cwl.StepInput{"x": {Sources: []string{"b/out"}}},
					Out: []string{"out"},
				},
				"b": {
					Run: "#tool1",
					In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
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
		In:  map[string]cwl.StepInput{"x": {Sources: []string{""}, Default: nil}},
		Out: []string{"out"},
	}
	apiErr := v.Validate(g)
	if apiErr == nil {
		t.Fatal("expected error")
	}
	if !hasFieldErrorMsg(apiErr.Details, "no source, no default, and no valueFrom") {
		t.Errorf("expected no source error, got %v", apiErr.Details)
	}
}

func TestValidate_CommandLineTool_EmptyOutputs(t *testing.T) {
	v := testValidator()
	g := &cwl.GraphDocument{
		CWLVersion:    "v1.2",
		OriginalClass: "CommandLineTool",
		Workflow: &cwl.Workflow{
			ID:     "main",
			Class:  "Workflow",
			Inputs: map[string]cwl.InputParam{"x": {Type: "int"}},
			Outputs: map[string]cwl.OutputParam{},
			Steps: map[string]cwl.Step{
				"run_tool": {
					Run: "#tool",
					In:  map[string]cwl.StepInput{"x": {Sources: []string{"x"}}},
					Out: []string{},
				},
			},
		},
		Tools: map[string]*cwl.CommandLineTool{
			"tool": {ID: "tool", Class: "CommandLineTool"},
		},
	}
	if apiErr := v.Validate(g); apiErr != nil {
		t.Errorf("CommandLineTool with empty outputs should be valid, got %v", apiErr)
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
