package cwl

// CommandLineTool is a typed representation of a CWL CommandLineTool.
// See https://www.commonwl.org/v1.2/CommandLineTool.html
type CommandLineTool struct {
	ID          string
	Class       string
	CWLVersion  string
	Doc         string
	Label       string
	BaseCommand any // string or []string; normalized by parser
	Inputs      map[string]ToolInputParam
	Outputs     map[string]ToolOutputParam
	Hints       map[string]any
	Requirements map[string]any

	// Arguments are command-line arguments not tied to input parameters.
	// Can be strings or structured Argument objects.
	Arguments []any

	// Stdin specifies the file path or expression for standard input.
	Stdin string

	// Stdout specifies the file name for capturing standard output.
	Stdout string

	// Stderr specifies the file name for capturing standard error.
	Stderr string

	// SuccessCodes are exit codes that indicate success (default: [0]).
	SuccessCodes []int

	// TemporaryFailCodes are exit codes that indicate temporary failure (can be retried).
	TemporaryFailCodes []int

	// PermanentFailCodes are exit codes that indicate permanent failure.
	PermanentFailCodes []int
}

// ToolInputParam is a CWL tool input parameter.
// Handles both shorthand ("read1: File") and expanded form.
type ToolInputParam struct {
	Type    string
	Doc     string
	Label   string
	Default any

	// InputBinding controls how this parameter appears on the command line.
	InputBinding *InputBinding

	// ItemInputBinding is the inputBinding for array items (nested binding in array type).
	// This is parsed from the inputBinding inside the array type definition.
	ItemInputBinding *InputBinding

	// RecordFields contains field definitions for record types.
	// Each field may have its own inputBinding for command line generation.
	RecordFields []RecordField

	// SecondaryFiles specifies additional files associated with this input.
	SecondaryFiles []SecondaryFileSchema

	// Format specifies the file format (for File types).
	Format any // string or []string

	// Streamable indicates if the file can be streamed.
	Streamable bool

	// LoadContents loads the file content into the input object.
	LoadContents bool

	// LoadListing controls directory listing behavior (for Directory types).
	LoadListing string
}

// ToolOutputParam is a CWL tool output parameter.
type ToolOutputParam struct {
	Type  string
	Doc   string
	Label string

	// OutputBinding specifies how to collect this output.
	OutputBinding *OutputBinding

	// SecondaryFiles specifies additional files associated with this output.
	SecondaryFiles []SecondaryFileSchema

	// OutputRecordFields contains field definitions for record output types.
	// Each field may have its own outputBinding and secondaryFiles.
	OutputRecordFields []OutputRecordField

	// Format specifies the file format (for File types).
	Format any // string or []string

	// Streamable indicates if the file can be streamed.
	Streamable bool
}

// ExpressionTool is a typed representation of a CWL ExpressionTool.
// See https://www.commonwl.org/v1.2/Workflow.html#ExpressionTool
type ExpressionTool struct {
	ID          string
	Class       string
	CWLVersion  string
	Doc         string
	Label       string
	Inputs      map[string]ToolInputParam
	Outputs     map[string]ExpressionToolOutputParam
	Hints       map[string]any
	Requirements map[string]any

	// Expression is the JavaScript expression to evaluate.
	Expression string
}

// ExpressionToolOutputParam is an output parameter for ExpressionTool.
type ExpressionToolOutputParam struct {
	Type   string
	Doc    string
	Label  string
	Format any // string or []string
}
