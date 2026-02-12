package cwl

// CommandLineTool is a typed representation of a CWL CommandLineTool.
type CommandLineTool struct {
	ID          string
	Class       string
	CWLVersion  string
	Doc         string
	BaseCommand any // string or []string; normalized by parser
	Inputs      map[string]ToolInputParam
	Outputs     map[string]ToolOutputParam
	Hints       map[string]any
}

// ToolInputParam is a CWL tool input parameter.
// Handles both shorthand ("read1: File") and expanded form.
type ToolInputParam struct {
	Type    string
	Doc     string
	Default any
}

// ToolOutputParam is a CWL tool output parameter.
type ToolOutputParam struct {
	Type          string
	OutputBinding *OutputBinding
}

// OutputBinding holds the CWL outputBinding specification.
type OutputBinding struct {
	Glob string
}
