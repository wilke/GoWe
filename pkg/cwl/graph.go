package cwl

// GraphDocument represents a packed $graph CWL document containing
// one Workflow and zero or more CommandLineTools.
type GraphDocument struct {
	CWLVersion string
	Workflow   *Workflow
	Tools      map[string]*CommandLineTool // keyed by id (without "#")
}
