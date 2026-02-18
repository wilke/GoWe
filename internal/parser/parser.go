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
	logger *slog.Logger
}

// New creates a Parser with the given logger.
func New(logger *slog.Logger) *Parser {
	return &Parser{logger: logger.With("component", "parser")}
}

// ParseGraphWithBase parses a CWL document and resolves $import directives.
// baseDir is used to resolve relative import paths.
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

	// Check if this is a bare document (no $graph).
	if _, hasGraph := raw["$graph"]; !hasGraph {
		class := stringField(raw, "class")
		switch class {
		case "CommandLineTool", "ExpressionTool":
			return p.wrapToolAsWorkflow(raw, version)
		case "Workflow":
			return p.parseBareWorkflow(raw, version)
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

		case "CommandLineTool", "ExpressionTool":
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
		stepIn[id] = cwl.StepInput{Source: id}
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

	return &cwl.GraphDocument{
		CWLVersion:    version,
		OriginalClass: "Workflow",
		Workflow:      wfResult.Workflow,
		Tools:         tools,
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
		stepIn[id] = cwl.StepInput{Source: id}
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
	Workflow    *cwl.Workflow
	InlineTools map[string]*cwl.CommandLineTool
}

// parseWorkflow parses a single CWL Workflow from a raw map.
func (p *Parser) parseWorkflow(raw map[string]any) (workflowParseResult, error) {
	result := workflowParseResult{
		InlineTools: make(map[string]*cwl.CommandLineTool),
	}

	wf := &cwl.Workflow{
		ID:         stringField(raw, "id"),
		Class:      stringField(raw, "class"),
		CWLVersion: stringField(raw, "cwlVersion"),
		Doc:        stringField(raw, "doc"),
		Inputs:     make(map[string]cwl.InputParam),
		Outputs:    make(map[string]cwl.OutputParam),
		Steps:      make(map[string]cwl.Step),
	}

	// Parse inputs: supports both array-style and map-style.
	inputs := normalizeToMap(raw["inputs"])
	for id, v := range inputs {
		switch val := v.(type) {
		case string:
			wf.Inputs[id] = cwl.InputParam{Type: val}
		case map[string]any:
			wf.Inputs[id] = cwl.InputParam{
				Type:    stringField(val, "type"),
				Doc:     stringField(val, "doc"),
				Default: val["default"],
			}
		default:
			return result, fmt.Errorf("input %q: unexpected type %T", id, v)
		}
	}

	// Parse outputs: supports both array-style and map-style.
	outputs := normalizeToMap(raw["outputs"])
	for id, v := range outputs {
		if m, ok := v.(map[string]any); ok {
			wf.Outputs[id] = cwl.OutputParam{
				Type:         stringField(m, "type"),
				OutputSource: stringField(m, "outputSource"),
				Doc:          stringField(m, "doc"),
			}
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
		}
	}

	result.Workflow = wf
	return result, nil
}

// normalizeToMap converts array-style CWL definitions to map-style.
// CWL supports both: inputs: [{id: x, type: File}] and inputs: {x: {type: File}}.
func normalizeToMap(v any) map[string]any {
	switch val := v.(type) {
	case map[string]any:
		return val
	case []any:
		result := make(map[string]any)
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					result[id] = m
				}
			}
		}
		return result
	}
	return make(map[string]any)
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
	Step       cwl.Step
	InlineTool *cwl.CommandLineTool // non-nil if step has inline tool
}

// parseStep parses a single CWL workflow step from a raw map.
func (p *Parser) parseStep(raw map[string]any, stepID string) (stepParseResult, error) {
	result := stepParseResult{}
	step := cwl.Step{
		Out:           stringSlice(raw, "out"),
		Scatter:       stringSlice(raw, "scatter"),
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
		// Inline tool - parse it and generate a reference.
		tool, err := p.parseTool(runVal)
		if err != nil {
			return result, fmt.Errorf("parse inline tool: %w", err)
		}
		// Generate ID for inline tool based on step ID.
		inlineID := stepID + "_inline"
		if tool.ID == "" {
			tool.ID = inlineID
		} else {
			inlineID = tool.ID
		}
		step.Run = "#" + inlineID
		result.InlineTool = tool
	}

	// Parse step inputs: supports both array-style and map-style.
	in := normalizeToMap(raw["in"])
	for id, v := range in {
		switch val := v.(type) {
		case string:
			step.In[id] = cwl.StepInput{Source: val}
		case map[string]any:
			step.In[id] = cwl.StepInput{
				Source:  stringField(val, "source"),
				Default: val["default"],
			}
		}
	}

	result.Step = step
	return result, nil
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
	if args, ok := raw["arguments"].([]any); ok {
		for _, arg := range args {
			switch a := arg.(type) {
			case string:
				tool.Arguments = append(tool.Arguments, a)
			case map[string]any:
				// Structured argument with position, prefix, valueFrom, etc.
				parsedArg := parseArgument(a)
				tool.Arguments = append(tool.Arguments, parsedArg)
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

	return out
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
			ms.In = append(ms.In, model.StepInput{
				ID:     inID,
				Source: si.Source,
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
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	// Handle YAML type coercion (e.g., type: int parsed as int).
	return fmt.Sprintf("%v", v)
}

// stringSlice safely extracts a []string from a map value.
// YAML decoder produces []any, not []string.
func stringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
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
