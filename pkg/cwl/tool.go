package cwl

// CommandLineTool is a typed representation of a CWL CommandLineTool.
// See https://www.commonwl.org/v1.2/CommandLineTool.html
type CommandLineTool struct {
	ID           string                    `json:"id,omitempty"`
	Class        string                    `json:"class,omitempty"`
	CWLVersion   string                    `json:"cwlVersion,omitempty"`
	Doc          string                    `json:"doc,omitempty"`
	Label        string                    `json:"label,omitempty"`
	BaseCommand  any                       `json:"baseCommand,omitempty"` // string or []string; normalized by parser
	Inputs       map[string]ToolInputParam `json:"inputs,omitempty"`
	Outputs      map[string]ToolOutputParam `json:"outputs,omitempty"`
	Hints        map[string]any            `json:"hints,omitempty"`
	Requirements map[string]any            `json:"requirements,omitempty"`

	// Arguments are command-line arguments not tied to input parameters.
	// Each entry can be a string, expression, or CommandLineBinding.
	// See https://www.commonwl.org/v1.2/CommandLineTool.html
	Arguments []ArgumentEntry `json:"arguments,omitempty"`

	// Stdin specifies the file path or expression for standard input.
	Stdin string `json:"stdin,omitempty"`

	// Stdout specifies the file name for capturing standard output.
	Stdout string `json:"stdout,omitempty"`

	// Stderr specifies the file name for capturing standard error.
	Stderr string `json:"stderr,omitempty"`

	// SuccessCodes are exit codes that indicate success (default: [0]).
	SuccessCodes []int `json:"successCodes,omitempty"`

	// TemporaryFailCodes are exit codes that indicate temporary failure (can be retried).
	TemporaryFailCodes []int `json:"temporaryFailCodes,omitempty"`

	// PermanentFailCodes are exit codes that indicate permanent failure.
	PermanentFailCodes []int `json:"permanentFailCodes,omitempty"`
}

// ToolInputParam is a CWL tool input parameter.
// Handles both shorthand ("read1: File") and expanded form.
type ToolInputParam struct {
	Type    string `json:"type,omitempty"`
	Doc     string `json:"doc,omitempty"`
	Label   string `json:"label,omitempty"`
	Default any    `json:"default,omitempty"`

	// InputBinding controls how this parameter appears on the command line.
	InputBinding *InputBinding `json:"inputBinding,omitempty"`

	// ItemInputBinding is the inputBinding for array items (nested binding in array type).
	// This is parsed from the inputBinding inside the array type definition.
	ItemInputBinding *InputBinding `json:"itemInputBinding,omitempty"`

	// ArrayItemTypes contains the item type name(s) for array types.
	// For arrays referencing SchemaDefRequirement types, this contains the type names
	// (e.g., ["#Stage"] or ["#Map1", "#Map2"] for union types).
	ArrayItemTypes []string `json:"arrayItemTypes,omitempty"`

	// RecordFields contains field definitions for record types.
	// Each field may have its own inputBinding for command line generation.
	RecordFields []RecordField `json:"recordFields,omitempty"`

	// SecondaryFiles specifies additional files associated with this input.
	SecondaryFiles []SecondaryFileSchema `json:"secondaryFiles,omitempty"`

	// Format specifies the file format (for File types).
	Format any `json:"format,omitempty"` // string or []string

	// Streamable indicates if the file can be streamed.
	Streamable bool `json:"streamable,omitempty"`

	// LoadContents loads the file content into the input object.
	LoadContents bool `json:"loadContents,omitempty"`

	// LoadListing controls directory listing behavior (for Directory types).
	LoadListing string `json:"loadListing,omitempty"`
}

// ToolOutputParam is a CWL tool output parameter.
type ToolOutputParam struct {
	Type  string `json:"type,omitempty"`
	Doc   string `json:"doc,omitempty"`
	Label string `json:"label,omitempty"`

	// OutputBinding specifies how to collect this output.
	OutputBinding *OutputBinding `json:"outputBinding,omitempty"`

	// SecondaryFiles specifies additional files associated with this output.
	SecondaryFiles []SecondaryFileSchema `json:"secondaryFiles,omitempty"`

	// OutputRecordFields contains field definitions for record output types.
	// Each field may have its own outputBinding and secondaryFiles.
	OutputRecordFields []OutputRecordField `json:"outputRecordFields,omitempty"`

	// Format specifies the file format (for File types).
	Format any `json:"format,omitempty"` // string or []string

	// Streamable indicates if the file can be streamed.
	Streamable bool `json:"streamable,omitempty"`
}

// ExpressionTool is a typed representation of a CWL ExpressionTool.
// See https://www.commonwl.org/v1.2/Workflow.html#ExpressionTool
type ExpressionTool struct {
	ID           string                               `json:"id,omitempty"`
	Class        string                               `json:"class,omitempty"`
	CWLVersion   string                               `json:"cwlVersion,omitempty"`
	Doc          string                               `json:"doc,omitempty"`
	Label        string                               `json:"label,omitempty"`
	Inputs       map[string]ToolInputParam            `json:"inputs,omitempty"`
	Outputs      map[string]ExpressionToolOutputParam `json:"outputs,omitempty"`
	Hints        map[string]any                       `json:"hints,omitempty"`
	Requirements map[string]any                       `json:"requirements,omitempty"`

	// Expression is the JavaScript expression to evaluate.
	Expression string `json:"expression,omitempty"`
}

// ExpressionToolOutputParam is an output parameter for ExpressionTool.
type ExpressionToolOutputParam struct {
	Type   string `json:"type,omitempty"`
	Doc    string `json:"doc,omitempty"`
	Label  string `json:"label,omitempty"`
	Format any    `json:"format,omitempty"` // string or []string
}
