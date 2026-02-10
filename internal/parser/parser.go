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
// The input must be a packed YAML document containing a $graph array.
func (p *Parser) ParseGraph(data []byte) (*cwl.GraphDocument, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}

	version := stringField(raw, "cwlVersion")

	graphRaw, ok := raw["$graph"]
	if !ok {
		return nil, fmt.Errorf("missing $graph: document must be in packed format")
	}

	entries, ok := graphRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("$graph must be an array")
	}

	graph := &cwl.GraphDocument{
		CWLVersion: version,
		Tools:      make(map[string]*cwl.CommandLineTool),
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
				tool.Inputs[id] = cwl.ToolInputParam{
					Type:    stringField(val, "type"),
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

// extractStepHints extracts GoWe-specific hints from a CWL hints map.
func extractStepHints(hints map[string]any) *model.StepHints {
	if hints == nil {
		return nil
	}
	gowe, ok := hints["goweHint"].(map[string]any)
	if !ok {
		return nil
	}
	h := &model.StepHints{
		BVBRCAppID: stringField(gowe, "bvbrc_app_id"),
	}
	if et := stringField(gowe, "executor"); et != "" {
		h.ExecutorType = model.ExecutorType(et)
	}
	if h.BVBRCAppID == "" && h.ExecutorType == "" {
		return nil
	}
	return h
}
