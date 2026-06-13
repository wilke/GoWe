package parser

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// reReturnStmt matches a JavaScript `return` keyword on a word boundary, used to
// recognize a CWL ${ ... } JavaScript expression body.
var reReturnStmt = regexp.MustCompile(`\breturn\b`)

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
	errs = append(errs, v.validateOutputBindings(graph)...)
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
			// Check if there are any non-empty sources.
			hasSource := false
			for _, s := range si.Sources {
				if s != "" {
					hasSource = true
					break
				}
			}
			if !hasSource && si.Default == nil && si.ValueFrom == "" {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
					Message: fmt.Sprintf("step %q input %q has no source, no default, and no valueFrom", stepID, inID),
				})
				continue
			}
			// Validate each source reference.
			for _, source := range si.Sources {
				if source != "" && !validSources[source] {
					errs = append(errs, model.FieldError{
						Field:   fmt.Sprintf("steps.%s.in.%s.source", stepID, inID),
						Message: fmt.Sprintf("source %q does not match any workflow input or step output", source),
					})
				}
			}
		}
	}

	return errs
}

// isArrayType checks if a CWL type string represents an array type.
// Matches patterns like "File[]", "string[]", "int[]", etc.
func isArrayType(typ string) bool {
	return strings.HasSuffix(typ, "[]")
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
		// Collect all sources to validate (single or multiple).
		var sources []string
		if out.OutputSource != "" {
			sources = []string{out.OutputSource}
		} else if len(out.OutputSources) > 0 {
			sources = out.OutputSources
		} else {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.outputSource", id),
				Message: fmt.Sprintf("output %q is missing outputSource", id),
			})
			continue
		}
		// Validate each source.
		for _, source := range sources {
			if !validSources[source] {
				errs = append(errs, model.FieldError{
					Field:   fmt.Sprintf("outputs.%s.outputSource", id),
					Message: fmt.Sprintf("outputSource %q does not match any step output or workflow input", source),
				})
			}
		}
		// Validate pickValue: all_non_null requires array output type.
		if out.PickValue == "all_non_null" && !isArrayType(out.Type) {
			errs = append(errs, model.FieldError{
				Field:   fmt.Sprintf("outputs.%s.pickValue", id),
				Message: fmt.Sprintf("pickValue 'all_non_null' requires array output type, got %q", out.Type),
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
		_, inSubWorkflows := graph.SubWorkflows[ref]

		// If not found, try stripping .cwl extension (for external file references).
		if !inTools && !inExprTools && !inSubWorkflows && strings.HasSuffix(ref, ".cwl") {
			// External file reference - tool ID is filename without extension.
			base := filepath.Base(ref)
			strippedRef := strings.TrimSuffix(base, ".cwl")
			_, inTools = graph.Tools[strippedRef]
			_, inExprTools = graph.ExpressionTools[strippedRef]
			_, inSubWorkflows = graph.SubWorkflows[strippedRef]
		}

		if !inTools && !inExprTools && !inSubWorkflows {
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

		// Get the tool's declared inputs (from CommandLineTool, ExpressionTool, or SubWorkflow).
		var toolInputs map[string]cwl.ToolInputParam
		var wfInputs map[string]cwl.InputParam
		if tool, ok := graph.Tools[ref]; ok {
			toolInputs = tool.Inputs
		} else if exprTool, ok := graph.ExpressionTools[ref]; ok {
			toolInputs = exprTool.Inputs
		} else if subWf, ok := graph.SubWorkflows[ref]; ok && subWf.Workflow != nil {
			wfInputs = subWf.Workflow.Inputs
		} else if strings.HasSuffix(ref, ".cwl") {
			// Try stripping .cwl extension (for external file references).
			base := filepath.Base(ref)
			strippedRef := strings.TrimSuffix(base, ".cwl")
			if tool, ok := graph.Tools[strippedRef]; ok {
				toolInputs = tool.Inputs
			} else if exprTool, ok := graph.ExpressionTools[strippedRef]; ok {
				toolInputs = exprTool.Inputs
			} else if subWf, ok := graph.SubWorkflows[strippedRef]; ok && subWf.Workflow != nil {
				wfInputs = subWf.Workflow.Inputs
			}
		}

		if toolInputs == nil && wfInputs == nil {
			// Tool not found - already reported by validateToolRefs.
			continue
		}

		// Check each step input against the tool's declared inputs.
		for inID := range step.In {
			if toolInputs != nil {
				if _, declared := toolInputs[inID]; !declared {
					errs = append(errs, model.FieldError{
						Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
						Message: fmt.Sprintf("step %q input %q is not declared in the tool's inputs", stepID, inID),
					})
				}
			} else if wfInputs != nil {
				if _, declared := wfInputs[inID]; !declared {
					errs = append(errs, model.FieldError{
						Field:   fmt.Sprintf("steps.%s.in.%s", stepID, inID),
						Message: fmt.Sprintf("step %q input %q is not declared in the subworkflow's inputs", stepID, inID),
					})
				}
			}
		}
	}

	return errs
}

// validateOutputBindings checks every tool's output globs and outputEval
// expressions for shell-style ${...} interpolations, which are invalid CWL —
// CWL parameter references use $(...) syntax. ${...} would be passed through
// as a literal string at execution time, producing output paths that never
// match any real file (e.g. "${params.output_path}/blast_out.json").
//
// Catches the bug pattern seen in the original blast-protein-search workflow
// which inlined a Homology tool with globs like
// "${params.output_path}/.${params.output_file}/blast_out.json".
func (v *Validator) validateOutputBindings(graph *cwl.GraphDocument) []model.FieldError {
	var errs []model.FieldError
	for toolID, tool := range graph.Tools {
		for outID, out := range tool.Outputs {
			if out.OutputBinding == nil {
				continue
			}
			pathPrefix := fmt.Sprintf("tools.%s.outputs.%s.outputBinding", toolID, outID)
			errs = append(errs, checkShellInterp(out.OutputBinding.Glob, pathPrefix+".glob", "glob")...)
			if out.OutputBinding.OutputEval != "" {
				errs = append(errs, checkShellInterpString(out.OutputBinding.OutputEval, pathPrefix+".outputEval", "outputEval")...)
			}
		}
	}
	return errs
}

// checkShellInterp reports any ${...} occurrences in a glob value (which may
// be a string, array of strings, or other CWL expression type). Returns one
// FieldError per offending string.
func checkShellInterp(glob any, field, kind string) []model.FieldError {
	switch g := glob.(type) {
	case string:
		return checkShellInterpString(g, field, kind)
	case []any:
		var errs []model.FieldError
		for i, item := range g {
			if s, ok := item.(string); ok {
				errs = append(errs, checkShellInterpString(s, fmt.Sprintf("%s[%d]", field, i), kind)...)
			}
		}
		return errs
	case []string:
		var errs []model.FieldError
		for i, s := range g {
			errs = append(errs, checkShellInterpString(s, fmt.Sprintf("%s[%d]", field, i), kind)...)
		}
		return errs
	}
	return nil
}

// checkShellInterpString reports a ${...} substring as a FieldError when it is
// shell-style interpolation. CWL parameter references use $(...), and ${...}
// embedded in a larger literal is shell syntax that CWL treats as a literal
// string. However, a value that is a single whole-string ${ ... } block
// containing a return statement is a valid CWL JavaScript expression *body*
// (legal in outputEval and expression globs with InlineJavascriptRequirement);
// such values are accepted.
func checkShellInterpString(s, field, kind string) []model.FieldError {
	if isJSExpressionBody(s) {
		return nil
	}

	idx := strings.Index(s, "${")
	if idx < 0 {
		return nil
	}
	// Extract the offending fragment up to the closing brace for a helpful message.
	end := strings.Index(s[idx:], "}")
	fragment := s[idx:]
	if end > 0 && end < 80 {
		fragment = s[idx : idx+end+1]
	} else if len(fragment) > 80 {
		fragment = fragment[:80] + "..."
	}
	return []model.FieldError{{
		Field: field,
		Message: fmt.Sprintf(
			"%s contains shell-style %q interpolation; CWL parameter references use $(...) syntax. "+
				"This pattern is passed through as a literal string and will never match a real file. "+
				"Replace with a valid CWL expression like $(inputs.<input_id>).",
			kind, fragment),
	}}
}

// isJSExpressionBody reports whether s is a single whole-string ${ ... } block
// containing a return statement — the CWL JavaScript expression-body form. Such
// a value is a valid expression (in outputEval or an expression glob when
// InlineJavascriptRequirement is in scope), not shell-style interpolation.
//
// It distinguishes:
//   - valid:   "${ return parseFloat(self[0].contents); }"   (whole block + return)
//   - invalid: "${params.output_path}/blast_out.json"        (embedded, no return)
//   - invalid: "${self[0].contents.trim()}"                  (no return; use $(...))
//
// Brace matching handles nested braces in the JS body (loops, conditionals).
func isJSExpressionBody(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "${") || !strings.HasSuffix(t, "}") {
		return false
	}
	// The opening "${" must balance with the trailing "}" as one block — i.e. the
	// brace opened at index 1 closes only at the final character. If it closes
	// earlier, the string is "${...}<more>" (embedded), not a whole expression.
	depth := 0
	for i := 1; i < len(t); i++ {
		switch t[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 && i != len(t)-1 {
				return false
			}
		}
	}
	if depth != 0 {
		return false // unbalanced
	}
	// A CWL ${...} body must return a value; require a return keyword to
	// distinguish it from shell-style ${var} / ${var.attr} references.
	return reReturnStmt.MatchString(t)
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
