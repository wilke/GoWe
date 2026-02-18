package cwl

// GraphDocument represents a packed $graph CWL document containing
// one Workflow and zero or more CommandLineTools.
type GraphDocument struct {
	CWLVersion    string
	OriginalClass string                      // "CommandLineTool", "Workflow", or "ExpressionTool"
	Workflow      *Workflow
	Tools         map[string]*CommandLineTool // keyed by id (without "#")
	Namespaces    map[string]string           // prefix -> URI mappings from $namespaces
}
