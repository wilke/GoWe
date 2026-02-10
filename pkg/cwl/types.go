package cwl

// Document represents a raw CWL document (single or $graph packed).
// These types are intentionally loose (map-based) for the bundler phase.
// Full typed parsing happens in internal/parser/ (Phase 4).
type Document map[string]any

// Class returns the CWL class (Workflow, CommandLineTool, ExpressionTool).
func (d Document) Class() string {
	if v, ok := d["class"].(string); ok {
		return v
	}
	return ""
}

// ID returns the document's id field, if present.
func (d Document) ID() string {
	if v, ok := d["id"].(string); ok {
		return v
	}
	return ""
}

// CWLVersion returns the cwlVersion field.
func (d Document) CWLVersion() string {
	if v, ok := d["cwlVersion"].(string); ok {
		return v
	}
	return ""
}

// IsGraph returns true if this is a $graph packed document.
func (d Document) IsGraph() bool {
	_, ok := d["$graph"]
	return ok
}

// Graph returns the $graph entries if this is a packed document.
func (d Document) Graph() []Document {
	g, ok := d["$graph"].([]any)
	if !ok {
		return nil
	}
	var docs []Document
	for _, entry := range g {
		if m, ok := entry.(map[string]any); ok {
			docs = append(docs, Document(m))
		}
	}
	return docs
}

// Steps returns the steps map from a Workflow document.
func (d Document) Steps() map[string]Document {
	steps, ok := d["steps"]
	if !ok {
		return nil
	}
	result := make(map[string]Document)
	switch s := steps.(type) {
	case map[string]any:
		for k, v := range s {
			if m, ok := v.(map[string]any); ok {
				result[k] = Document(m)
			}
		}
	}
	return result
}
