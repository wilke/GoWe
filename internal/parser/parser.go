package parser

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
	"gopkg.in/yaml.v3"
)

// Parser converts raw CWL YAML into typed CWL structs and domain models.
type Parser struct {
	logger  *slog.Logger
	baseDir string // Base directory for resolving relative file references
}

// New creates a Parser with the given logger.
func New(logger *slog.Logger) *Parser {
	return &Parser{logger: logger.With("component", "parser")}
}

// ParseGraphWithBase parses a CWL document and resolves $import directives.
// baseDir is used to resolve relative import paths and external tool references.
func (p *Parser) ParseGraphWithBase(data []byte, baseDir string) (*cwl.GraphDocument, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}

	// Resolve $import directives.
	resolved, err := resolveImports(raw, baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve imports: %w", err)
	}
	raw = resolved.(map[string]any)

	// Store base directory for resolving external tool references.
	p.baseDir = baseDir

	return p.parseGraphFromRaw(raw)
}

// ParseGraph parses a packed $graph CWL document into a GraphDocument.
// The input can be:
//   - A packed YAML document containing a $graph array with a Workflow
//   - A bare CommandLineTool or ExpressionTool (auto-wrapped into a single-step Workflow)
func (p *Parser) ParseGraph(data []byte) (*cwl.GraphDocument, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}
	return p.parseGraphFromRaw(raw)
}

// parseGraphFromRaw parses an already-unmarshaled CWL document.
func (p *Parser) parseGraphFromRaw(raw map[string]any) (*cwl.GraphDocument, error) {
	version := stringField(raw, "cwlVersion")

	// Parse $namespaces for prefix resolution.
	namespaces := parseNamespaces(raw)

	// Check if this is a bare document (no $graph).
	if _, hasGraph := raw["$graph"]; !hasGraph {
		class := stringField(raw, "class")
		switch class {
		case "CommandLineTool", "ExpressionTool":
			graph, err := p.wrapToolAsWorkflow(raw, version)
			if err != nil {
				return nil, err
			}
			graph.Namespaces = namespaces
			return graph, nil
		case "Workflow":
			graph, err := p.parseBareWorkflow(raw, version)
			if err != nil {
				return nil, err
			}
			graph.Namespaces = namespaces
			return graph, nil
		default:
			return nil, fmt.Errorf("missing $graph: document must be packed format or a bare CommandLineTool/ExpressionTool/Workflow")
		}
	}

	graphRaw := raw["$graph"]
	entries, ok := graphRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("$graph must be an array")
	}

	graph := &cwl.GraphDocument{
		CWLVersion:    version,
		OriginalClass: "Workflow",
		Tools:         make(map[string]*cwl.CommandLineTool),
		Namespaces:    namespaces,
	}

	for i, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("$graph[%d]: expected map, got %T", i, entry)
		}

		class := stringField(m, "class")
		switch class {
		case "Workflow":
			if graph.Workflow != nil {
				return nil, fmt.Errorf("$graph contains multiple Workflow entries")
			}
			wfResult, err := p.parseWorkflow(m)
			if err != nil {
				return nil, fmt.Errorf("$graph[%d] (Workflow): %w", i, err)
			}
			if version != "" && wfResult.Workflow.CWLVersion == "" {
				wfResult.Workflow.CWLVersion = version
			}
			graph.Workflow = wfResult.Workflow
			// Add inline tools from workflow steps.
			for toolID, tool := range wfResult.InlineTools {
				graph.Tools[toolID] = tool
			}
			// Add inline expression tools from workflow steps.
			for toolID, exprTool := range wfResult.InlineExpressionTools {
				if graph.ExpressionTools == nil {
					graph.ExpressionTools = make(map[string]*cwl.ExpressionTool)
				}
				graph.ExpressionTools[toolID] = exprTool
			}

		case "CommandLineTool":
			tool, err := p.parseTool(m)
			if err != nil {
				return nil, fmt.Errorf("$graph[%d] (%s): %w", i, class, err)
			}
			if tool.ID == "" {
				return nil, fmt.Errorf("$graph[%d] (%s): missing id", i, class)
			}
			// Strip leading "#" from tool ID for consistent lookup.
			toolID := tool.ID
			if strings.HasPrefix(toolID, "#") {
				toolID = toolID[1:]
			}
			graph.Tools[toolID] = tool

		case "ExpressionTool":
			exprTool, err := p.parseExpressionTool(m)
			if err != nil {
				return nil, fmt.Errorf("$graph[%d] (%s): %w", i, class, err)
			}
			if exprTool.ID == "" {
				return nil, fmt.Errorf("$graph[%d] (%s): missing id", i, class)
			}
			// Strip leading "#" from tool ID for consistent lookup.
			toolID := exprTool.ID
			if strings.HasPrefix(toolID, "#") {
				toolID = toolID[1:]
			}
			if graph.ExpressionTools == nil {
				graph.ExpressionTools = make(map[string]*cwl.ExpressionTool)
			}
			graph.ExpressionTools[toolID] = exprTool

		default:
			return nil, fmt.Errorf("$graph[%d]: unknown class %q", i, class)
		}
	}

	if graph.Workflow == nil {
		// No workflow found - create a synthetic one from the main tool.
		// Look for tool with id "main" (IDs are stored without "#" prefix), or use the first tool.
		var mainTool *cwl.CommandLineTool
		if tool, ok := graph.Tools["main"]; ok {
			mainTool = tool
		} else {
			// Use the first tool in the map.
			for _, tool := range graph.Tools {
				mainTool = tool
				break
			}
		}
		if mainTool == nil {
			return nil, fmt.Errorf("$graph contains no Workflow or tool entries")
		}
		graph.Workflow = createSyntheticWorkflow(mainTool)
		graph.OriginalClass = mainTool.Class
		p.logger.Debug("created synthetic workflow from packed tool", "tool_id", mainTool.ID)
	}

	return graph, nil
}

// createSyntheticWorkflow creates a workflow that wraps a single tool.
func createSyntheticWorkflow(tool *cwl.CommandLineTool) *cwl.Workflow {
	// Build workflow inputs (same as tool inputs).
	wfInputs := make(map[string]cwl.InputParam)
	for id, inp := range tool.Inputs {
		wfInputs[id] = cwl.InputParam{
			Type:    inp.Type,
			Doc:     inp.Doc,
			Default: inp.Default,
		}
	}

	// Build workflow outputs (same as tool outputs, with outputSource).
	wfOutputs := make(map[string]cwl.OutputParam)
	stepOutIDs := make([]string, 0, len(tool.Outputs))
	for id, out := range tool.Outputs {
		wfOutputs[id] = cwl.OutputParam{
			Type:         out.Type,
			OutputSource: "run_tool/" + id,
		}
		stepOutIDs = append(stepOutIDs, id)
	}

	// Build step inputs (map workflow inputs to step inputs).
	stepIn := make(map[string]cwl.StepInput)
	for id := range tool.Inputs {
		stepIn[id] = cwl.StepInput{Sources: []string{id}}
	}

	// Create Run reference - always use "#" prefix with the stripped ID.
	toolID := tool.ID
	if strings.HasPrefix(toolID, "#") {
		toolID = toolID[1:]
	}
	runRef := "#" + toolID

	return &cwl.Workflow{
		ID:         "main",
		Class:      "Workflow",
		CWLVersion: tool.CWLVersion,
		Doc:        tool.Doc,
		Inputs:     wfInputs,
		Outputs:    wfOutputs,
		Steps: map[string]cwl.Step{
			"run_tool": {
				Run: runRef,
				In:  stepIn,
				Out: stepOutIDs,
			},
		},
	}
}

// parseBareWorkflow parses a bare Workflow document (without $graph).
func (p *Parser) parseBareWorkflow(raw map[string]any, version string) (*cwl.GraphDocument, error) {
	wfResult, err := p.parseWorkflow(raw)
	if err != nil {
		return nil, fmt.Errorf("parse Workflow: %w", err)
	}

	if version != "" && wfResult.Workflow.CWLVersion == "" {
		wfResult.Workflow.CWLVersion = version
	}

	p.logger.Debug("parsed bare workflow", "id", wfResult.Workflow.ID)

	// Initialize tools map with any inline tools from steps.
	tools := wfResult.InlineTools
	if tools == nil {
		tools = make(map[string]*cwl.CommandLineTool)
	}

	// Initialize expression tools map with any inline expression tools from steps.
	exprTools := wfResult.InlineExpressionTools

	// Load external tool files referenced by steps.
	for stepID, step := range wfResult.Workflow.Steps {
		if step.Run == "" || strings.HasPrefix(step.Run, "#") {
			continue // Internal reference or empty
		}
		// Check if this is an external file reference (ends with .cwl).
		if strings.HasSuffix(step.Run, ".cwl") {
			toolPath := step.Run
			if !filepath.IsAbs(toolPath) && p.baseDir != "" {
				toolPath = filepath.Join(p.baseDir, toolPath)
			}

			// Check if already loaded.
			toolID := strings.TrimSuffix(filepath.Base(step.Run), ".cwl")
			if _, exists := tools[toolID]; exists {
				continue
			}
			if exprTools != nil {
				if _, exists := exprTools[toolID]; exists {
					continue
				}
			}

			// Load and parse the external tool file.
			toolData, err := os.ReadFile(toolPath)
			if err != nil {
				return nil, fmt.Errorf("load external tool %s for step %s: %w", step.Run, stepID, err)
			}

			var toolRaw map[string]any
			if err := yaml.Unmarshal(toolData, &toolRaw); err != nil {
				return nil, fmt.Errorf("parse external tool %s: %w", step.Run, err)
			}

			// Resolve $imports in the external tool.
			toolBaseDir := filepath.Dir(toolPath)
			resolved, err := resolveImports(toolRaw, toolBaseDir)
			if err != nil {
				return nil, fmt.Errorf("resolve imports in %s: %w", step.Run, err)
			}
			toolRaw = resolved.(map[string]any)

			class := stringField(toolRaw, "class")
			if class == "ExpressionTool" {
				exprTool, err := p.parseExpressionTool(toolRaw)
				if err != nil {
					return nil, fmt.Errorf("parse external tool %s: %w", step.Run, err)
				}
				if exprTool.ID == "" {
					exprTool.ID = toolID
				}
				if exprTools == nil {
					exprTools = make(map[string]*cwl.ExpressionTool)
				}
				exprTools[exprTool.ID] = exprTool
				p.logger.Debug("loaded external expression tool", "path", step.Run, "id", exprTool.ID)
			} else {
				tool, err := p.parseTool(toolRaw)
				if err != nil {
					return nil, fmt.Errorf("parse external tool %s: %w", step.Run, err)
				}
				if tool.ID == "" {
					tool.ID = toolID
				}
				tools[tool.ID] = tool
				p.logger.Debug("loaded external tool", "path", step.Run, "id", tool.ID)
			}
		}
	}

	return &cwl.GraphDocument{
		CWLVersion:      version,
		OriginalClass:   "Workflow",
		Workflow:        wfResult.Workflow,
		Tools:           tools,
		ExpressionTools: exprTools,
	}, nil
}

// wrapToolAsWorkflow wraps a bare CommandLineTool or ExpressionTool in a synthetic single-step Workflow.
func (p *Parser) wrapToolAsWorkflow(toolRaw map[string]any, version string) (*cwl.GraphDocument, error) {
	class := stringField(toolRaw, "class")

	// Parse the tool.
	tool, err := p.parseTool(toolRaw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", class, err)
	}

	// Generate tool ID if not present.
	toolID := tool.ID
	if toolID == "" {
		toolID = "tool"
		tool.ID = toolID
	}

	// Build synthetic workflow inputs (same as tool inputs).
	wfInputs := make(map[string]cwl.InputParam)
	for id, inp := range tool.Inputs {
		wfInputs[id] = cwl.InputParam{
			Type:    inp.Type,
			Doc:     inp.Doc,
			Default: inp.Default,
		}
	}

	// Build synthetic workflow outputs (same as tool outputs, with outputSource).
	wfOutputs := make(map[string]cwl.OutputParam)
	stepOutIDs := make([]string, 0, len(tool.Outputs))
	for id, out := range tool.Outputs {
		wfOutputs[id] = cwl.OutputParam{
			Type:         out.Type,
			OutputSource: "run_tool/" + id,
		}
		stepOutIDs = append(stepOutIDs, id)
	}

	// Build step inputs (map workflow inputs to step inputs).
	stepIn := make(map[string]cwl.StepInput)
	for id := range tool.Inputs {
		stepIn[id] = cwl.StepInput{Sources: []string{id}}
	}

	// Create synthetic workflow with single step.
	wf := &cwl.Workflow{
		ID:         "main",
		Class:      "Workflow",
		CWLVersion: version,
		Doc:        tool.Doc,
		Inputs:     wfInputs,
		Outputs:    wfOutputs,
		Steps: map[string]cwl.Step{
			"run_tool": {
				Run: "#" + toolID,
				In:  stepIn,
				Out: stepOutIDs,
			},
		},
	}

	// Copy hints from tool to step if present.
	if tool.Hints != nil {
		wf.Steps["run_tool"] = cwl.Step{
			Run:   "#" + toolID,
			In:    stepIn,
			Out:   stepOutIDs,
			Hints: tool.Hints,
		}
	}

	p.logger.Debug("auto-wrapped tool as workflow", "tool_id", toolID, "class", class)

	return &cwl.GraphDocument{
		CWLVersion:    version,
		OriginalClass: class,
		Workflow:      wf,
		Tools:         map[string]*cwl.CommandLineTool{toolID: tool},
	}, nil
}

// workflowParseResult holds the result of parsing a workflow.
type workflowParseResult struct {
	Workflow               *cwl.Workflow
	InlineTools            map[string]*cwl.CommandLineTool
	InlineExpressionTools  map[string]*cwl.ExpressionTool
}

// parseWorkflow parses a single CWL Workflow from a raw map.
func (p *Parser) parseWorkflow(raw map[string]any) (workflowParseResult, error) {
	result := workflowParseResult{
		InlineTools:           make(map[string]*cwl.CommandLineTool),
		InlineExpressionTools: make(map[string]*cwl.ExpressionTool),
	}

	wf := &cwl.Workflow{
		ID:           stringField(raw, "id"),
		Class:        stringField(raw, "class"),
		CWLVersion:   stringField(raw, "cwlVersion"),
		Doc:          stringField(raw, "doc"),
		Inputs:       make(map[string]cwl.InputParam),
		Outputs:      make(map[string]cwl.OutputParam),
		Steps:        make(map[string]cwl.Step),
		Hints:        normalizeHintsToMap(raw["hints"]),
		Requirements: normalizeHintsToMap(raw["requirements"]),
	}

	// Parse inputs: supports both array-style and map-style.
	inputs := normalizeToMap(raw["inputs"])
	for id, v := range inputs {
		switch val := v.(type) {
		case string:
			wf.Inputs[id] = cwl.InputParam{Type: val}
		case map[string]any:
			// Type can be a string or complex type (record, array, etc.)
			typeVal := stringField(val, "type")
			if typeVal == "" {
				// Complex type - serialize it.
				typeVal = serializeCWLType(val["type"])
			}
			inp := cwl.InputParam{
				Type:         typeVal,
				Doc:          stringField(val, "doc"),
				Default:      val["default"],
				LoadContents: boolField(val, "loadContents"),
			}
			// Parse secondaryFiles if present at the input level.
			inp.SecondaryFiles = parseSecondaryFiles(val["secondaryFiles"])

			// Parse record fields if this is a record type.
			if typeMap, ok := val["type"].(map[string]any); ok {
				if typeMap["type"] == "record" {
					inp.RecordFields = parseRecordFields(typeMap["fields"])
				}
			}
			wf.Inputs[id] = inp
		default:
			return result, fmt.Errorf("input %q: unexpected type %T", id, v)
		}
	}

	// Parse outputs: supports both array-style and map-style.
	outputs := normalizeToMap(raw["outputs"])
	for id, v := range outputs {
		if m, ok := v.(map[string]any); ok {
			// Use serializeCWLType for type to handle complex array types.
			typeVal := stringField(m, "type")
			if typeVal == "" {
				typeVal = serializeCWLType(m["type"])
			}
			outParam := cwl.OutputParam{
				Type:      typeVal,
				Doc:       stringField(m, "doc"),
				PickValue: stringField(m, "pickValue"),
				LinkMerge: stringField(m, "linkMerge"),
			}
			// outputSource can be a string or array of strings.
			if sources := normalizeSourceRefs(m["outputSource"]); len(sources) > 0 {
				if len(sources) == 1 {
					outParam.OutputSource = sources[0]
				} else {
					outParam.OutputSources = sources
				}
			}
			wf.Outputs[id] = outParam
		}
	}

	// Parse steps: supports both array-style and map-style.
	steps := normalizeToMap(raw["steps"])
	for id, v := range steps {
		if m, ok := v.(map[string]any); ok {
			stepResult, err := p.parseStep(m, id)
			if err != nil {
				return result, fmt.Errorf("step %q: %w", id, err)
			}
			wf.Steps[id] = stepResult.Step
			// Collect inline tools.
			if stepResult.InlineTool != nil {
				toolID := stepResult.InlineTool.ID
				result.InlineTools[toolID] = stepResult.InlineTool
			}
			if stepResult.InlineExpressionTool != nil {
				toolID := stepResult.InlineExpressionTool.ID
				result.InlineExpressionTools[toolID] = stepResult.InlineExpressionTool
			}
		}
	}

	result.Workflow = wf
	return result, nil
}

// normalizeToMap converts array-style CWL definitions to map-style.
// CWL supports both: inputs: [{id: x, type: File}] and inputs: {x: {type: File}}.
// Also handles packed format IDs like "#main/input" by extracting the last component.
func normalizeToMap(v any) map[string]any {
	switch val := v.(type) {
	case map[string]any:
		return val
	case []any:
		result := make(map[string]any)
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					// Normalize packed format IDs: "#main/input" -> "input"
					normalizedID := normalizePackedID(id)
					result[normalizedID] = m
				}
			}
		}
		return result
	}
	return make(map[string]any)
}

// normalizePackedID converts a packed format ID to a simple ID.
// "#main/input" -> "input"
// "#main/rev/output" -> "output" (for step outputs)
// "input" -> "input" (already simple)
func normalizePackedID(id string) string {
	// Strip leading #
	id = strings.TrimPrefix(id, "#")

	// Find the last / to get the local name
	lastSlash := strings.LastIndex(id, "/")
	if lastSlash >= 0 {
		return id[lastSlash+1:]
	}
	return id
}

// normalizeHintsToMap converts array-style hints/requirements to map-style keyed by class.
// CWL supports both: hints: [{class: DockerRequirement, ...}] and hints: {DockerRequirement: {...}}.
func normalizeHintsToMap(v any) map[string]any {
	switch val := v.(type) {
	case map[string]any:
		return val
	case []any:
		result := make(map[string]any)
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if class, ok := m["class"].(string); ok {
					result[class] = m
				}
			}
		}
		return result
	}
	return nil
}

// stepParseResult holds the result of parsing a workflow step.
type stepParseResult struct {
	Step                 cwl.Step
	InlineTool           *cwl.CommandLineTool   // non-nil if step has inline CommandLineTool
	InlineExpressionTool *cwl.ExpressionTool    // non-nil if step has inline ExpressionTool
}

// parseStep parses a single CWL workflow step from a raw map.
func (p *Parser) parseStep(raw map[string]any, stepID string) (stepParseResult, error) {
	result := stepParseResult{}

	// Normalize output IDs (packed format uses "#main/rev/output" -> "output")
	outIDs := stringSlice(raw, "out")
	normalizedOut := make([]string, len(outIDs))
	for i, outID := range outIDs {
		normalizedOut[i] = normalizePackedID(outID)
	}

	// Normalize scatter field (packed format uses "#main/rev/input" -> "input")
	scatterIDs := stringSlice(raw, "scatter")
	normalizedScatter := make([]string, len(scatterIDs))
	for i, scatterID := range scatterIDs {
		normalizedScatter[i] = normalizePackedID(scatterID)
	}

	step := cwl.Step{
		Out:           normalizedOut,
		Scatter:       normalizedScatter,
		ScatterMethod: stringField(raw, "scatterMethod"),
		When:          stringField(raw, "when"),
		Hints:         mapField(raw, "hints"),
		Requirements:  mapField(raw, "requirements"),
		In:            make(map[string]cwl.StepInput),
	}

	// Handle 'run' field: can be a string reference or an inline tool map.
	switch runVal := raw["run"].(type) {
	case string:
		step.Run = runVal
	case map[string]any:
		// Inline tool - check class to determine type.
		class := stringField(runVal, "class")
		inlineID := stepID + "_inline"

		if class == "ExpressionTool" {
			// Parse as ExpressionTool.
			exprTool, err := p.parseExpressionTool(runVal)
			if err != nil {
				return result, fmt.Errorf("parse inline expression tool: %w", err)
			}
			if exprTool.ID == "" {
				exprTool.ID = inlineID
			} else {
				inlineID = exprTool.ID
			}
			step.Run = "#" + inlineID
			result.InlineExpressionTool = exprTool
		} else {
			// Parse as CommandLineTool (default).
			tool, err := p.parseTool(runVal)
			if err != nil {
				return result, fmt.Errorf("parse inline tool: %w", err)
			}
			if tool.ID == "" {
				tool.ID = inlineID
			} else {
				inlineID = tool.ID
			}
			step.Run = "#" + inlineID
			result.InlineTool = tool
		}
	}

	// Parse step inputs: supports both array-style and map-style.
	in := normalizeToMap(raw["in"])
	for id, v := range in {
		switch val := v.(type) {
		case string:
			step.In[id] = cwl.StepInput{Sources: []string{normalizeSourceRef(val)}}
		case []any:
			// List of sources directly (MultipleInputFeatureRequirement shorthand)
			step.In[id] = cwl.StepInput{Sources: normalizeSourceRefs(val)}
		case map[string]any:
			step.In[id] = cwl.StepInput{
				Sources:      normalizeSourceRefs(val["source"]),
				Default:      val["default"],
				ValueFrom:    stringField(val, "valueFrom"),
				LoadContents: boolField(val, "loadContents"),
			}
		}
	}

	result.Step = step
	return result, nil
}

// normalizeSourceRef normalizes a packed format source reference.
// "#main/input" -> "input" (workflow input)
// "#main/rev/output" -> "rev/output" (step output)
// "echo_1/fileout" -> "echo_1/fileout" (already local step/output reference)
func normalizeSourceRef(source string) string {
	if source == "" {
		return ""
	}

	// Only normalize if it has a leading # (packed format)
	if !strings.HasPrefix(source, "#") {
		// Already a local reference like "input" or "echo_1/fileout"
		return source
	}

	// Strip leading #
	source = source[1:]

	// Count slashes to determine type
	parts := strings.Split(source, "/")
	if len(parts) <= 1 {
		// Simple ID like "#echo" -> "echo"
		return source
	}

	// If format is "workflow/input" -> "input"
	// If format is "workflow/step/output" -> "step/output"
	if len(parts) == 2 {
		// "main/input" means workflow input "input"
		return parts[len(parts)-1]
	}
	if len(parts) == 3 {
		// "main/rev/output" means step "rev" output "output"
		return parts[1] + "/" + parts[2]
	}

	// Fallback - return last component
	return parts[len(parts)-1]
}

// normalizeSourceRefs handles step input source which can be a string or array.
// Returns a slice of normalized source references.
func normalizeSourceRefs(source any) []string {
	if source == nil {
		return nil
	}
	switch v := source.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{normalizeSourceRef(v)}
	case []any:
		result := make([]string, 0, len(v))
		for _, s := range v {
			if str, ok := s.(string); ok && str != "" {
				result = append(result, normalizeSourceRef(str))
			}
		}
		return result
	case []string:
		result := make([]string, 0, len(v))
		for _, s := range v {
			if s != "" {
				result = append(result, normalizeSourceRef(s))
			}
		}
		return result
	}
	return nil
}

// parseTool parses a single CWL CommandLineTool from a raw map.
func (p *Parser) parseTool(raw map[string]any) (*cwl.CommandLineTool, error) {
	tool := &cwl.CommandLineTool{
		ID:           stringField(raw, "id"),
		Class:        stringField(raw, "class"),
		CWLVersion:   stringField(raw, "cwlVersion"),
		Doc:          stringField(raw, "doc"),
		Label:        stringField(raw, "label"),
		BaseCommand:  raw["baseCommand"],
		Hints:        normalizeHintsToMap(raw["hints"]),
		Requirements: normalizeHintsToMap(raw["requirements"]),
		Stdin:        stringField(raw, "stdin"),
		Stdout:       stringField(raw, "stdout"),
		Stderr:       stringField(raw, "stderr"),
		Inputs:       make(map[string]cwl.ToolInputParam),
		Outputs:      make(map[string]cwl.ToolOutputParam),
	}

	// Parse arguments array.
	// CWL spec: array<string | Expression | CommandLineBinding>
	if args, ok := raw["arguments"].([]any); ok {
		for _, arg := range args {
			switch a := arg.(type) {
			case string:
				// String literal or expression.
				tool.Arguments = append(tool.Arguments, cwl.ArgumentEntry{
					StringValue: a,
					IsString:    true,
				})
			case map[string]any:
				// Structured argument (CommandLineBinding).
				parsedArg := parseArgument(a)
				tool.Arguments = append(tool.Arguments, cwl.ArgumentEntry{
					Binding:  &parsedArg,
					IsString: false,
				})
			}
		}
	}

	// Parse exit codes.
	tool.SuccessCodes = intSlice(raw, "successCodes")
	tool.TemporaryFailCodes = intSlice(raw, "temporaryFailCodes")
	tool.PermanentFailCodes = intSlice(raw, "permanentFailCodes")

	// Parse tool inputs: supports both array-style and map-style.
	inputs := normalizeToMap(raw["inputs"])
	for id, v := range inputs {
		switch val := v.(type) {
		case string:
			tool.Inputs[id] = cwl.ToolInputParam{Type: val}
		case map[string]any:
			tool.Inputs[id] = parseToolInput(val)
		}
	}

	// Parse tool outputs: supports both array-style and map-style, and shorthand types.
	outputs := normalizeToMap(raw["outputs"])
	for id, v := range outputs {
		switch val := v.(type) {
		case string:
			// Shorthand format: output_id: type_name (e.g., "stdout_file: stdout")
			tool.Outputs[id] = cwl.ToolOutputParam{Type: val}
		case map[string]any:
			tool.Outputs[id] = parseToolOutput(val)
		}
	}

	return tool, nil
}

// parseExpressionTool parses a CWL ExpressionTool from a raw map.
func (p *Parser) parseExpressionTool(raw map[string]any) (*cwl.ExpressionTool, error) {
	tool := &cwl.ExpressionTool{
		ID:           stringField(raw, "id"),
		Class:        stringField(raw, "class"),
		CWLVersion:   stringField(raw, "cwlVersion"),
		Doc:          stringField(raw, "doc"),
		Label:        stringField(raw, "label"),
		Expression:   stringField(raw, "expression"),
		Hints:        normalizeHintsToMap(raw["hints"]),
		Requirements: normalizeHintsToMap(raw["requirements"]),
		Inputs:       make(map[string]cwl.ToolInputParam),
		Outputs:      make(map[string]cwl.ExpressionToolOutputParam),
	}

	// Parse tool inputs: supports both array-style and map-style.
	inputs := normalizeToMap(raw["inputs"])
	for id, v := range inputs {
		switch val := v.(type) {
		case string:
			tool.Inputs[id] = cwl.ToolInputParam{Type: val}
		case map[string]any:
			tool.Inputs[id] = parseToolInput(val)
		}
	}

	// Parse tool outputs: supports both array-style and map-style.
	outputs := normalizeToMap(raw["outputs"])
	for id, v := range outputs {
		switch val := v.(type) {
		case string:
			tool.Outputs[id] = cwl.ExpressionToolOutputParam{Type: val}
		case map[string]any:
			tool.Outputs[id] = cwl.ExpressionToolOutputParam{
				Type:   stringField(val, "type"),
				Doc:    stringField(val, "doc"),
				Label:  stringField(val, "label"),
				Format: val["format"],
			}
		}
	}

	return tool, nil
}

// parseToolInput parses a single tool input parameter from a raw map.
func parseToolInput(val map[string]any) cwl.ToolInputParam {
	typeStr := stringField(val, "type")
	if typeStr == "" {
		// Complex type (record array, null union, etc.) — serialize to string tag.
		typeStr = serializeCWLType(val["type"])
	}

	inp := cwl.ToolInputParam{
		Type:         typeStr,
		Doc:          stringField(val, "doc"),
		Label:        stringField(val, "label"),
		Default:      val["default"],
		Format:       val["format"],
		Streamable:   boolField(val, "streamable"),
		LoadContents: boolField(val, "loadContents"),
		LoadListing:  stringField(val, "loadListing"),
	}

	// Parse inputBinding.
	if ib, ok := val["inputBinding"].(map[string]any); ok {
		inp.InputBinding = parseInputBinding(ib)
	}

	// Parse item-level inputBinding from array type definition.
	// Example: type: { type: array, items: File, inputBinding: { prefix: "-YYY" } }
	if typeMap, ok := val["type"].(map[string]any); ok {
		if typeMap["type"] == "array" {
			if itemIB, ok := typeMap["inputBinding"].(map[string]any); ok {
				inp.ItemInputBinding = parseInputBinding(itemIB)
			}
		}
		// Parse record field definitions.
		// Example: type: { type: record, fields: [{name: a, type: int, inputBinding: {prefix: -a}}] }
		if typeMap["type"] == "record" {
			inp.RecordFields = parseRecordFields(typeMap["fields"])
		}
	}

	// Parse secondaryFiles.
	inp.SecondaryFiles = parseSecondaryFiles(val["secondaryFiles"])

	return inp
}

// parseToolOutput parses a single tool output parameter from a raw map.
func parseToolOutput(m map[string]any) cwl.ToolOutputParam {
	out := cwl.ToolOutputParam{
		Type:       stringField(m, "type"),
		Doc:        stringField(m, "doc"),
		Label:      stringField(m, "label"),
		Format:     m["format"],
		Streamable: boolField(m, "streamable"),
	}

	// Parse outputBinding.
	if ob, ok := m["outputBinding"].(map[string]any); ok {
		out.OutputBinding = parseOutputBinding(ob)
	}

	// Parse secondaryFiles.
	out.SecondaryFiles = parseSecondaryFiles(m["secondaryFiles"])

	// Check for record type with fields.
	if typeMap, ok := m["type"].(map[string]any); ok {
		if typeStr, ok := typeMap["type"].(string); ok && typeStr == "record" {
			out.Type = "record"
			out.OutputRecordFields = parseOutputRecordFields(typeMap["fields"])
		}
	}

	return out
}

// parseOutputRecordFields parses output record fields from a raw value.
func parseOutputRecordFields(v any) []cwl.OutputRecordField {
	if v == nil {
		return nil
	}

	var fields []cwl.OutputRecordField

	switch val := v.(type) {
	case []any:
		// Array-style fields: [{name: f1, type: File, ...}, ...]
		for _, item := range val {
			if fm, ok := item.(map[string]any); ok {
				fields = append(fields, parseOutputRecordField(fm))
			}
		}
	case map[string]any:
		// Map-style fields: {f1: {type: File, ...}, ...}
		for name, field := range val {
			if fm, ok := field.(map[string]any); ok {
				f := parseOutputRecordField(fm)
				f.Name = name
				fields = append(fields, f)
			}
		}
	}

	return fields
}

// parseOutputRecordField parses a single output record field.
func parseOutputRecordField(m map[string]any) cwl.OutputRecordField {
	field := cwl.OutputRecordField{
		Name:  stringField(m, "name"),
		Doc:   stringField(m, "doc"),
		Label: stringField(m, "label"),
	}

	// Parse type - can be string or complex type.
	if typeStr, ok := m["type"].(string); ok {
		field.Type = typeStr
	} else {
		field.Type = serializeCWLType(m["type"])
	}

	// Parse outputBinding.
	if ob, ok := m["outputBinding"].(map[string]any); ok {
		field.OutputBinding = parseOutputBinding(ob)
	}

	// Parse secondaryFiles.
	field.SecondaryFiles = parseSecondaryFiles(m["secondaryFiles"])

	return field
}

// parseInputBinding parses a CWL inputBinding from a raw map.
func parseInputBinding(ib map[string]any) *cwl.InputBinding {
	binding := &cwl.InputBinding{
		Prefix:        stringField(ib, "prefix"),
		ItemSeparator: stringField(ib, "itemSeparator"),
		ValueFrom:     stringField(ib, "valueFrom"),
		LoadContents:  boolField(ib, "loadContents"),
	}

	// Parse position (can be int or expression string).
	if pos, ok := ib["position"]; ok {
		switch p := pos.(type) {
		case int:
			binding.Position = p
		case float64:
			binding.Position = int(p)
		case string:
			binding.Position = p
		}
	}

	// Parse separate (default is true).
	if sep, ok := ib["separate"]; ok {
		if b, ok := sep.(bool); ok {
			binding.Separate = &b
		}
	}

	// Parse shellQuote (default is true).
	if sq, ok := ib["shellQuote"]; ok {
		if b, ok := sq.(bool); ok {
			binding.ShellQuote = &b
		}
	}

	return binding
}

// parseRecordFields parses record field definitions from a CWL type.
// Fields can be an array or map of field definitions.
func parseRecordFields(fields any) []cwl.RecordField {
	if fields == nil {
		return nil
	}

	var result []cwl.RecordField

	switch f := fields.(type) {
	case []any:
		// Array of field definitions.
		for _, item := range f {
			if fieldMap, ok := item.(map[string]any); ok {
				result = append(result, parseRecordField(fieldMap))
			}
		}
	case map[string]any:
		// Map of field name -> field definition.
		for name, val := range f {
			if fieldMap, ok := val.(map[string]any); ok {
				field := parseRecordField(fieldMap)
				field.Name = name
				result = append(result, field)
			}
		}
	}

	return result
}

// parseRecordField parses a single record field definition.
func parseRecordField(m map[string]any) cwl.RecordField {
	field := cwl.RecordField{
		Name:  stringField(m, "name"),
		Type:  serializeCWLType(m["type"]),
		Doc:   stringField(m, "doc"),
		Label: stringField(m, "label"),
	}

	// Parse inputBinding for this field.
	if ib, ok := m["inputBinding"].(map[string]any); ok {
		field.InputBinding = parseInputBinding(ib)
	}

	// Parse secondaryFiles for this field.
	field.SecondaryFiles = parseSecondaryFiles(m["secondaryFiles"])

	return field
}

// parseOutputBinding parses a CWL outputBinding from a raw map.
func parseOutputBinding(ob map[string]any) *cwl.OutputBinding {
	binding := &cwl.OutputBinding{
		LoadContents: boolField(ob, "loadContents"),
		LoadListing:  stringField(ob, "loadListing"),
		OutputEval:   stringField(ob, "outputEval"),
	}

	// Glob can be string or []string.
	if glob, ok := ob["glob"]; ok {
		binding.Glob = glob
	}

	return binding
}

// parseArgument parses a structured argument from a raw map.
func parseArgument(a map[string]any) cwl.Argument {
	arg := cwl.Argument{
		Prefix:    stringField(a, "prefix"),
		ValueFrom: stringField(a, "valueFrom"),
	}

	// Parse position (can be int or expression string).
	if pos, ok := a["position"]; ok {
		switch p := pos.(type) {
		case int:
			arg.Position = p
		case float64:
			arg.Position = int(p)
		case string:
			arg.Position = p
		}
	}

	// Parse separate.
	if sep, ok := a["separate"]; ok {
		if b, ok := sep.(bool); ok {
			arg.Separate = &b
		}
	}

	// Parse shellQuote.
	if sq, ok := a["shellQuote"]; ok {
		if b, ok := sq.(bool); ok {
			arg.ShellQuote = &b
		}
	}

	return arg
}

// parseSecondaryFiles parses the secondaryFiles field.
func parseSecondaryFiles(v any) []cwl.SecondaryFileSchema {
	if v == nil {
		return nil
	}

	var result []cwl.SecondaryFileSchema

	switch sf := v.(type) {
	case string:
		// Single pattern string.
		result = append(result, cwl.SecondaryFileSchema{Pattern: sf})
	case []any:
		for _, item := range sf {
			switch s := item.(type) {
			case string:
				result = append(result, cwl.SecondaryFileSchema{Pattern: s})
			case map[string]any:
				result = append(result, cwl.SecondaryFileSchema{
					Pattern:  stringField(s, "pattern"),
					Required: s["required"],
				})
			}
		}
	case map[string]any:
		// Single structured entry.
		result = append(result, cwl.SecondaryFileSchema{
			Pattern:  stringField(sf, "pattern"),
			Required: sf["required"],
		})
	}

	return result
}

// ToModel converts a typed CWL GraphDocument to a domain model Workflow.
func (p *Parser) ToModel(graph *cwl.GraphDocument, name string) (*model.Workflow, error) {
	wf := graph.Workflow
	now := time.Now().UTC()

	mw := &model.Workflow{
		Name:        name,
		CWLVersion:  graph.CWLVersion,
		Description: wf.Doc,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Convert inputs.
	for id, inp := range wf.Inputs {
		mw.Inputs = append(mw.Inputs, model.WorkflowInput{
			ID:       id,
			Type:     inp.Type,
			Required: inp.Default == nil,
			Default:  inp.Default,
		})
	}
	sort.Slice(mw.Inputs, func(i, j int) bool {
		return mw.Inputs[i].ID < mw.Inputs[j].ID
	})

	// Convert outputs.
	for id, out := range wf.Outputs {
		mw.Outputs = append(mw.Outputs, model.WorkflowOutput{
			ID:           id,
			Type:         out.Type,
			OutputSource: out.OutputSource,
		})
	}
	sort.Slice(mw.Outputs, func(i, j int) bool {
		return mw.Outputs[i].ID < mw.Outputs[j].ID
	})

	// Build workflow input set for DependsOn computation.
	inputIDs := make(map[string]bool, len(wf.Inputs))
	for id := range wf.Inputs {
		inputIDs[id] = true
	}

	// Convert steps.
	for stepID, step := range wf.Steps {
		toolRef := strings.TrimPrefix(step.Run, "#")

		ms := model.Step{
			ID:      stepID,
			ToolRef: toolRef,
			Out:     step.Out,
			Scatter: step.Scatter,
			When:    step.When,
		}

		// Convert step inputs.
		for inID, si := range step.In {
			// For model storage, join multiple sources with comma.
			// The cwl-runner uses cwl.StepInput.Sources directly.
			source := ""
			if len(si.Sources) == 1 {
				source = si.Sources[0]
			} else if len(si.Sources) > 1 {
				source = strings.Join(si.Sources, ",")
			}
			ms.In = append(ms.In, model.StepInput{
				ID:        inID,
				Source:    source,
				ValueFrom: si.ValueFrom,
			})
		}
		sort.Slice(ms.In, func(i, j int) bool {
			return ms.In[i].ID < ms.In[j].ID
		})

		// Resolve inline tool from graph.
		if tool, ok := graph.Tools[toolRef]; ok {
			ms.ToolInline = convertTool(tool)
		}

		// Extract GoWe hints from step or resolved tool.
		ms.Hints = extractStepHints(step.Hints)
		if ms.Hints == nil && ms.ToolInline != nil {
			ms.Hints = ms.ToolInline.Hints
		}

		// Compute DependsOn from source references.
		ms.DependsOn = computeDependsOn(ms.In, inputIDs)

		mw.Steps = append(mw.Steps, ms)
	}
	sort.Slice(mw.Steps, func(i, j int) bool {
		return mw.Steps[i].ID < mw.Steps[j].ID
	})

	return mw, nil
}

// convertTool converts a cwl.CommandLineTool to a model.Tool.
func convertTool(ct *cwl.CommandLineTool) *model.Tool {
	t := &model.Tool{
		ID:    ct.ID,
		Class: ct.Class,
	}

	// Normalize BaseCommand to []string.
	switch bc := ct.BaseCommand.(type) {
	case string:
		t.BaseCommand = []string{bc}
	case []string:
		t.BaseCommand = bc
	case []any:
		for _, v := range bc {
			if s, ok := v.(string); ok {
				t.BaseCommand = append(t.BaseCommand, s)
			}
		}
	}

	// Convert tool inputs.
	for id, inp := range ct.Inputs {
		t.Inputs = append(t.Inputs, model.ToolInput{
			ID:      id,
			Type:    inp.Type,
			Default: inp.Default,
			Doc:     inp.Doc,
		})
	}
	sort.Slice(t.Inputs, func(i, j int) bool {
		return t.Inputs[i].ID < t.Inputs[j].ID
	})

	// Convert tool outputs.
	for id, out := range ct.Outputs {
		to := model.ToolOutput{ID: id, Type: out.Type}
		if out.OutputBinding != nil {
			// Glob can be string or []string; convert to string for model.
			switch g := out.OutputBinding.Glob.(type) {
			case string:
				to.Glob = g
			case []any:
				// Take first glob pattern for the simplified model.
				if len(g) > 0 {
					if s, ok := g[0].(string); ok {
						to.Glob = s
					}
				}
			}
		}
		t.Outputs = append(t.Outputs, to)
	}
	sort.Slice(t.Outputs, func(i, j int) bool {
		return t.Outputs[i].ID < t.Outputs[j].ID
	})

	// Extract tool hints.
	t.Hints = extractStepHints(ct.Hints)

	return t
}

// computeDependsOn extracts step dependencies from source references.
// Source "assemble/contigs" means this step depends on "assemble".
// Bare sources (workflow inputs) create no dependency.
func computeDependsOn(inputs []model.StepInput, workflowInputs map[string]bool) []string {
	seen := make(map[string]bool)
	var deps []string
	for _, si := range inputs {
		if strings.Contains(si.Source, "/") {
			stepID := strings.SplitN(si.Source, "/", 2)[0]
			if !seen[stepID] {
				seen[stepID] = true
				deps = append(deps, stepID)
			}
		}
	}
	sort.Strings(deps)
	return deps
}

// --- Helper functions ---

// serializeCWLType converts a complex CWL type (map or array) to a string tag.
// Examples: {type: array, items: {type: record, name: "paired_end_lib"}} → "record:paired_end_lib[]"
// A YAML sequence like ["null", {type: array, ...}] indicates an optional complex type.
func serializeCWLType(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		base := stringField(t, "type")
		if base == "array" {
			items := serializeCWLType(t["items"])
			return items + "[]"
		}
		if base == "record" {
			name := stringField(t, "name")
			if name != "" {
				return "record:" + name
			}
			return "record"
		}
		return base
	case []any:
		// Union type like ["null", {type: array, ...}] — find the non-null member.
		for _, member := range t {
			if s, ok := member.(string); ok && s == "null" {
				continue
			}
			inner := serializeCWLType(member)
			if inner != "" {
				return inner + "?"
			}
		}
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// stringField safely extracts a string from a map.
// For the "type" key specifically, if the value is not a string (e.g., array for union types),
// this returns empty string so that serializeCWLType can handle complex types.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	// For non-string values (arrays, maps), return empty to signal complex type.
	// The caller (parseToolInput) will use serializeCWLType for complex types.
	// Exception: for certain fields like position that might be numeric, use Sprintf.
	if key == "type" {
		return ""
	}
	// Handle YAML type coercion (e.g., position: 1 parsed as int).
	return fmt.Sprintf("%v", v)
}

// stringSlice safely extracts a []string from a map value.
// YAML decoder produces []any, not []string.
// Also handles single string values (e.g., scatter: message).
func stringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case string:
		// Handle single string value (e.g., scatter: message)
		return []string{s}
	case []string:
		return s
	case []any:
		var result []string
		for _, item := range s {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}
	return nil
}

// mapField safely extracts a map[string]any from a map.
func mapField(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// boolField safely extracts a bool from a map.
func boolField(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// intSlice safely extracts a []int from a map value.
// YAML decoder produces []any with int/float64 values.
func intSlice(m map[string]any, key string) []int {
	v, ok := m[key]
	if !ok {
		return nil
	}

	switch s := v.(type) {
	case []int:
		return s
	case []any:
		var result []int
		for _, item := range s {
			switch i := item.(type) {
			case int:
				result = append(result, i)
			case float64:
				result = append(result, int(i))
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}
	return nil
}

// extractStepHints extracts GoWe-specific hints and CWL DockerRequirement from a hints map.
func extractStepHints(hints map[string]any) *model.StepHints {
	if hints == nil {
		return nil
	}

	var h model.StepHints

	// GoWe-specific hints.
	if gowe, ok := hints["goweHint"].(map[string]any); ok {
		h.BVBRCAppID = stringField(gowe, "bvbrc_app_id")
		if et := stringField(gowe, "executor"); et != "" {
			h.ExecutorType = model.ExecutorType(et)
		}
		h.DockerImage = stringField(gowe, "docker_image")
	}

	// CWL standard DockerRequirement.
	if dr, ok := hints["DockerRequirement"].(map[string]any); ok {
		pull := stringField(dr, "dockerPull")
		if pull != "" && h.DockerImage == "" {
			h.DockerImage = pull
		}
		if h.ExecutorType == "" && pull != "" {
			h.ExecutorType = model.ExecutorTypeContainer
		}
	}

	if h.BVBRCAppID == "" && h.ExecutorType == "" && h.DockerImage == "" {
		return nil
	}
	return &h
}

// parseNamespaces extracts $namespaces map from a CWL document.
// Returns a map of prefix -> URI for namespace resolution.
func parseNamespaces(raw map[string]any) map[string]string {
	ns, ok := raw["$namespaces"].(map[string]any)
	if !ok {
		return nil
	}

	result := make(map[string]string)
	for prefix, uri := range ns {
		if uriStr, ok := uri.(string); ok {
			result[prefix] = uriStr
		}
	}
	return result
}

// resolveImports recursively resolves $import directives in a CWL document.
// It loads referenced files and replaces the $import directive with the file contents.
func resolveImports(v any, baseDir string) (any, error) {
	switch val := v.(type) {
	case map[string]any:
		// Check if this is an $import directive.
		if importPath, ok := val["$import"].(string); ok && len(val) == 1 {
			// Resolve the import path relative to baseDir.
			fullPath := importPath
			if !filepath.IsAbs(importPath) {
				fullPath = filepath.Join(baseDir, importPath)
			}

			// Read and parse the imported file.
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return nil, fmt.Errorf("read import %q: %w", importPath, err)
			}

			var imported any
			if err := yaml.Unmarshal(data, &imported); err != nil {
				return nil, fmt.Errorf("parse import %q: %w", importPath, err)
			}

			// Recursively resolve imports in the imported content.
			importDir := filepath.Dir(fullPath)
			return resolveImports(imported, importDir)
		}

		// Recursively process all values in the map.
		result := make(map[string]any)
		for k, v := range val {
			resolved, err := resolveImports(v, baseDir)
			if err != nil {
				return nil, err
			}
			result[k] = resolved
		}
		return result, nil

	case []any:
		// Recursively process all elements in the array.
		result := make([]any, len(val))
		for i, item := range val {
			resolved, err := resolveImports(item, baseDir)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil

	default:
		// Primitive values are returned as-is.
		return v, nil
	}
}
