package cwl

// Workflow is a typed representation of a CWL Workflow document.
// It is the intermediate form between raw YAML and model.Workflow.
type Workflow struct {
	ID         string
	Class      string
	CWLVersion string
	Doc        string
	Inputs     map[string]InputParam
	Outputs    map[string]OutputParam
	Steps      map[string]Step
}

// InputParam is a normalized CWL workflow input.
// Handles both shorthand ("reads_r1: File") and expanded form.
type InputParam struct {
	Type    string
	Doc     string
	Default any
}

// OutputParam is a CWL workflow output.
type OutputParam struct {
	Type         string
	OutputSource string
	Doc          string
}

// Step is a CWL workflow step.
type Step struct {
	Run           string
	In            map[string]StepInput
	Out           []string
	Scatter       []string
	ScatterMethod string // "dotproduct", "nested_crossproduct", or "flat_crossproduct"
	When          string
	Hints         map[string]any
	Requirements  map[string]any
}

// StepInput is a normalized CWL step input.
// Handles both shorthand ("read1: reads_r1") and expanded form.
type StepInput struct {
	Source  string
	Default any
}

// GoWeHint holds GoWe-specific hints extracted from CWL hints.
type GoWeHint struct {
	BVBRCAppID   string
	ExecutorType string
}
