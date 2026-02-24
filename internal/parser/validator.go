package parser

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Validator performs semantic validation on a parsed CWL GraphDocument.
type Validator struct {
	logger *slog.Logger
}

// NewValidator creates a Validator with the given logger.
func NewValidator(logger *slog.Logger) *Validator {
	return &Validator{logger: logger.With("component", "validator")}
}

// Validate checks semantic correctness of a GraphDocument.
// Returns nil if valid, or an *model.APIError with FieldError details.
func (v *Validator) Validate(graph *cwl.GraphDocument) *model.APIError {
	var errs []model.FieldError

	errs = append(errs, v.validateVersion(graph)...)
	errs = append(errs, v.validateWorkflow(graph)...)
	errs = append(errs, v.validateSteps(graph)...)
	errs = append(errs, v.validateSources(graph)...)
	errs = append(errs, v.validateOutputSources(graph)...)
	errs = append(errs, v.validateToolRefs(graph)...)
	errs = append(errs, v.validateDAG(graph)...)

	if len(errs) == 0 {
		return nil
	}
	return model.NewValidationError("CWL validation failed", errs...)
}

func (v *Validator) validateVersion(graph *cwl.GraphDocument) []model.FieldError {
	if graph.CWLVersion == "" {
		return []model.FieldError{{Field: "cwlVersion", Message: "cwlVersion is required"}}
	}
	// Accept CWL v1.0, v1.1, and v1.2 (they are mostly compatible).
	switch graph.CWLVersion {
	case "v1.0", "v1.1", "v1.2", "draft-3":
		return nil
	default:
		return []model.FieldError{{
			Field:   "cwlVersion",
			Message: fmt.Sprintf("unsupported cwlVersion %q; expected v1.0, v1.1, or v1.2", graph.CWLVersion),
		}}
	}
}

func (v *Validator) validateWorkflow(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	wf := graph.Workflow
	if wf == nil {
		return []model.FieldError{{Field: "$graph", Message: "no Workflow entry found in $graph"}}
	}

	// CWL v1.2 allows workflows with no inputs (they can have defaults or be passthrough).
	// CWL v1.2 allows workflows with no steps (passthrough workflows that connect inputs to outputs).
	// CWL v1.2 allows workflows with no outputs (side-effect only workflows).
	// No longer enforce "at least one output" rule - it's valid per CWL spec.

	// All inputs must have a type.
	for id, inp := range wf.Inputs {
		if inp.Type == "" {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("inputs.%s.type", id),
				Message: fmt.Sprintf("input %q is missing type", id),
			})
		}
	}

	return errs
}

func (v *Validator) validateSteps(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	if graph.Workflow == nil {
		return nil
	}

	for id, step := range graph.Workflow.Steps {
		if step.Run == "" {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("steps.%s.run", id),
				Message: fmt.Sprintf("step %q is missing 'run' reference", id),
			})
		}
		// CWL allows steps with no outputs (side-effect only tools,
		// e.g. BV-BRC apps that write to workspace). No error needed.
	}

	return errs
}

func (v *Validator) validateSources(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	wf := graph.Workflow
	if wf == nil {
		return nil
	}

	// Build set of valid sources.
	validSources := make(map[string]bool)
	for id := range wf.Inputs {
		validSources[id] = true
	}
	for stepID, step := range wf.Steps {
		for _, outID := range step.Out {
			validSources[stepID+"/"+outID] = true
		}
	}

	// Check each step input source.
	for stepID, step := range wf.Steps {
		for inID, si := range step.In {
			if si.Source == "" && si.Default == nil && si.ValueFrom == "" {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
					Message: fmt.Sprintf("step %q input %q has no source, no default, and no valueFrom", stepID, inID),
				})
				continue
			}
			if si.Source != "" && !validSources[si.Source] {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("steps.%s.in.%s.source", stepID, inID),
					Message: fmt.Sprintf("source %q does not match any workflow input or step output", si.Source),
				})
			}
		}
	}

	return errs
}

func (v *Validator) validateOutputSources(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	wf := graph.Workflow
	if wf == nil {
		return nil
	}

	// Build set of valid sources: step outputs and workflow inputs (for passthrough).
	validSources := make(map[string]bool)
	for stepID, step := range wf.Steps {
		for _, outID := range step.Out {
			validSources[stepID+"/"+outID] = true
		}
	}
	// CWL allows workflow outputs to directly reference workflow inputs (passthrough).
	for inputID := range wf.Inputs {
		validSources[inputID] = true
	}

	for id, out := range wf.Outputs {
		if out.OutputSource == "" {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.outputSource", id),
				Message: fmt.Sprintf("output %q is missing outputSource", id),
			})
			continue
		}
		if !validSources[out.OutputSource] {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.outputSource", id),
				Message: fmt.Sprintf("outputSource %q does not match any step output or workflow input", out.OutputSource),
			})
		}
	}

	return errs
}

func (v *Validator) validateToolRefs(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	if graph.Workflow == nil {
		return nil
	}

	for stepID, step := range graph.Workflow.Steps {
		if step.Run == "" {
			continue // Already caught by validateSteps.
		}
		ref := step.Run
		if len(ref) > 0 && ref[0] == '#' {
			ref = ref[1:]
		}

		// Check if tool exists with the reference as-is (for packed documents with .cwl IDs).
		_, inTools := graph.Tools[ref]
		_, inExprTools := graph.ExpressionTools[ref]

		// If not found, try stripping .cwl extension (for external file references).
		if !inTools && !inExprTools && strings.HasSuffix(ref, ".cwl") {
			// External file reference - tool ID is filename without extension.
			base := filepath.Base(ref)
			strippedRef := strings.TrimSuffix(base, ".cwl")
			_, inTools = graph.Tools[strippedRef]
			_, inExprTools = graph.ExpressionTools[strippedRef]
		}

		if !inTools && !inExprTools {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("steps.%s.run", stepID),
				Message: fmt.Sprintf("run reference %q not found in $graph", step.Run),
			})
		}
	}

	return errs
}

// validateStepInputs checks that step inputs match the tool's declared inputs.
// CWL spec requires that step inputs correspond to inputs declared by the tool.
func (v *Validator) validateStepInputs(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	if graph.Workflow == nil {
		return nil
	}

	for stepID, step := range graph.Workflow.Steps {
		if step.Run == "" {
			continue // Already caught by validateSteps.
		}

		// Resolve the tool reference to get the tool's declared inputs.
		ref := step.Run
		if len(ref) > 0 && ref[0] == '#' {
			ref = ref[1:]
		}

		// Get the tool's declared inputs.
		var toolInputs map[string]cwl.ToolInputParam
		if tool, ok := graph.Tools[ref]; ok {
			toolInputs = tool.Inputs
		} else if exprTool, ok := graph.ExpressionTools[ref]; ok {
			toolInputs = exprTool.Inputs
		} else if strings.HasSuffix(ref, ".cwl") {
			// Try stripping .cwl extension (for external file references).
			base := filepath.Base(ref)
			strippedRef := strings.TrimSuffix(base, ".cwl")
			if tool, ok := graph.Tools[strippedRef]; ok {
				toolInputs = tool.Inputs
			} else if exprTool, ok := graph.ExpressionTools[strippedRef]; ok {
				toolInputs = exprTool.Inputs
			}
		}

		if toolInputs == nil {
			// Tool not found - already reported by validateToolRefs.
			continue
		}

		// Check each step input against the tool's declared inputs.
		for inID := range step.In {
			if _, declared := toolInputs[inID]; !declared {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
					Message: fmt.Sprintf("step %q input %q is not declared in the tool's inputs", stepID, inID),
				})
			}
		}
	}

	return errs
}

func (v *Validator) validateDAG(graph *cwl.GraphDocument) []model.FieldError {
	if graph.Workflow == nil {
		return nil
	}
	_, err := BuildDAG(graph.Workflow)
	if err != nil {
		return []model.FieldError{{
			Field:   "steps",
			Message: err.Error(),
		}}
	}
	return nil
}
