package parser

import (
	"fmt"
	"log/slog"

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
	if graph.CWLVersion != "v1.2" {
		return []model.FieldError{{
			Field:   "cwlVersion",
			Message: fmt.Sprintf("unsupported cwlVersion %q; expected v1.2", graph.CWLVersion),
		}}
	}
	return nil
}

func (v *Validator) validateWorkflow(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	wf := graph.Workflow
	if wf == nil {
		return []model.FieldError{{Field: "$graph", Message: "no Workflow entry found in $graph"}}
	}

	if len(wf.Inputs) == 0 {
		errs = append(errs, model.FieldError{Field: "inputs", Message: "workflow must have at least one input"})
	}
	if len(wf.Steps) == 0 {
		errs = append(errs, model.FieldError{Field: "steps", Message: "workflow must have at least one step"})
	}
	if len(wf.Outputs) == 0 {
		errs = append(errs, model.FieldError{Field: "outputs", Message: "workflow must have at least one output"})
	}

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
		if len(step.Out) == 0 {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("steps.%s.out", id),
				Message: fmt.Sprintf("step %q has no outputs", id),
			})
		}
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
			if si.Source == "" && si.Default == nil {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
					Message: fmt.Sprintf("step %q input %q has no source and no default", stepID, inID),
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

	// Build set of valid step/output pairs.
	validOutputs := make(map[string]bool)
	for stepID, step := range wf.Steps {
		for _, outID := range step.Out {
			validOutputs[stepID+"/"+outID] = true
		}
	}

	for id, out := range wf.Outputs {
		if out.OutputSource == "" {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.outputSource", id),
				Message: fmt.Sprintf("output %q is missing outputSource", id),
			})
			continue
		}
		if !validOutputs[out.OutputSource] {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.outputSource", id),
				Message: fmt.Sprintf("outputSource %q does not match any step output", out.OutputSource),
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
		if _, ok := graph.Tools[ref]; !ok {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("steps.%s.run", stepID),
				Message: fmt.Sprintf("run reference %q not found in $graph", step.Run),
			})
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
