package parser

import (
	"fmt"
	"log/slog"
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

// ParseGraph parses a packed $graph CWL document into a GraphDocument.
// The input can be:
//   - A packed YAML document containing a $graph array with a Workflow
//   - A bare CommandLineTool or ExpressionTool (auto-wrapped into a single-step Workflow)
func (p *Parser) ParseGraph(data []byte) (*cwl.GraphDocument, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}

	version := stringField(raw, "cwlVersion")

	// Check if this is a bare CommandLineTool or ExpressionTool (no $graph).
	if _, hasGraph := raw["$graph"]; !hasGraph {
		class := stringField(raw, "class")
		if class == "CommandLineTool" || class == "ExpressionTool" {
			return p.wrapToolAsWorkflow(raw, version)
		}
		return nil, fmt.Errorf("missing $graph: document must be packed format or a bare CommandLineTool/ExpressionTool")
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
			wf, err := p.parseWorkflow(m)
			if err != nil {
				return nil, fmt.Errorf("$graph[%d] (Workflow): %w", i, err)
			}
			if version != "" && wf.CWLVersion == "" {
				wf.CWLVersion = version
			}
			graph.Workflow = wf

		case "CommandLineTool", "ExpressionTool":
			tool, err := p.parseTool(m)
			if err != nil {
				return nil, fmt.Errorf("$graph[%d] (%s): %w", i, class, err)
			}
			if tool.ID == "" {
				return nil, fmt.Errorf("$graph[%d] (%s): missing id", i, class)
			}
			graph.Tools[tool.ID] = tool

		default:
			return nil, fmt.Errorf("$graph[%d]: unknown class %q", i, class)
		}
	}

	if graph.Workflow == nil {
		return nil, fmt.Errorf("$graph contains no Workflow entry")
	}

	return graph, nil
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

// parseWorkflow parses a single CWL Workflow from a raw map.
func (p *Parser) parseWorkflow(raw map[string]any) (*cwl.Workflow, error) {
	wf := &cwl.Workflow{
		ID:         stringField(raw, "id"),
		Class:      stringField(raw, "class"),
		CWLVersion: stringField(raw, "cwlVersion"),
		Doc:        stringField(raw, "doc"),
		Inputs:     make(map[string]cwl.InputParam),
		Outputs:    make(map[string]cwl.OutputParam),
		Steps:      make(map[string]cwl.Step),
	}

	// Parse inputs: value is string (shorthand type) or map (expanded).
	if inputs, ok := raw["inputs"].(map[string]any); ok {
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
				return nil, fmt.Errorf("input %q: unexpected type %T", id, v)
			}
		}
	}

	// Parse outputs: always expanded (type + outputSource).
	if outputs, ok := raw["outputs"].(map[string]any); ok {
		for id, v := range outputs {
			if m, ok := v.(map[string]any); ok {
				wf.Outputs[id] = cwl.OutputParam{
					Type:         stringField(m, "type"),
					OutputSource: stringField(m, "outputSource"),
					Doc:          stringField(m, "doc"),
				}
			}
		}
	}

	// Parse steps.
	if steps, ok := raw["steps"].(map[string]any); ok {
		for id, v := range steps {
			if m, ok := v.(map[string]any); ok {
				step, err := p.parseStep(m)
				if err != nil {
					return nil, fmt.Errorf("step %q: %w", id, err)
				}
				wf.Steps[id] = step
			}
		}
	}

	return wf, nil
}

// parseStep parses a single CWL workflow step from a raw map.
func (p *Parser) parseStep(raw map[string]any) (cwl.Step, error) {
	step := cwl.Step{
		Run:     stringField(raw, "run"),
		Out:     stringSlice(raw, "out"),
		Scatter: stringSlice(raw, "scatter"),
		When:    stringField(raw, "when"),
		Hints:   mapField(raw, "hints"),
		In:      make(map[string]cwl.StepInput),
	}

	// Parse step inputs: value is string (shorthand source) or map (expanded).
	if in, ok := raw["in"].(map[string]any); ok {
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
	}

	return step, nil
}

// parseTool parses a single CWL CommandLineTool from a raw map.
func (p *Parser) parseTool(raw map[string]any) (*cwl.CommandLineTool, error) {
	tool := &cwl.CommandLineTool{
		ID:          stringField(raw, "id"),
		Class:       stringField(raw, "class"),
		CWLVersion:  stringField(raw, "cwlVersion"),
		Doc:         stringField(raw, "doc"),
		BaseCommand: raw["baseCommand"],
		Hints:       mapField(raw, "hints"),
		Inputs:      make(map[string]cwl.ToolInputParam),
		Outputs:     make(map[string]cwl.ToolOutputParam),
	}

	// Parse tool inputs: value is string (shorthand type) or map (expanded).
	if inputs, ok := raw["inputs"].(map[string]any); ok {
		for id, v := range inputs {
			switch val := v.(type) {
			case string:
				tool.Inputs[id] = cwl.ToolInputParam{Type: val}
			case map[string]any:
				typeStr := stringField(val, "type")
				if typeStr == "" {
					// Complex type (record array, null union, etc.) — serialize to string tag.
					typeStr = serializeCWLType(val["type"])
				}
				tool.Inputs[id] = cwl.ToolInputParam{
					Type:    typeStr,
					Doc:     stringField(val, "doc"),
					Default: val["default"],
				}
			}
		}
	}

	// Parse tool outputs.
	if outputs, ok := raw["outputs"].(map[string]any); ok {
		for id, v := range outputs {
			if m, ok := v.(map[string]any); ok {
				out := cwl.ToolOutputParam{
					Type: stringField(m, "type"),
				}
				if ob, ok := m["outputBinding"].(map[string]any); ok {
					out.OutputBinding = &cwl.OutputBinding{
						Glob: stringField(ob, "glob"),
					}
				}
				tool.Outputs[id] = out
			}
		}
	}

	return tool, nil
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
			to.Glob = out.OutputBinding.Glob
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
