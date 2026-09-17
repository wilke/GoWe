package scheduler

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/stepinput"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ValidateSubmissionInputs validates workflow-level inputs against each
// step whose inputs are fully determined by workflow inputs (no upstream
// step outputs). Returns an error on the first problem. Steps that depend
// on other steps' outputs are skipped — they are validated at dispatch time.
func ValidateSubmissionInputs(logger *slog.Logger, wf *model.Workflow, inputs map[string]any) error {
	if wf.RawCWL == "" {
		return nil // No CWL to validate against.
	}

	p := parser.New(logger)
	graphDoc, err := p.ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return nil // Parse failure is not a submission validation error.
	}

	// Merge workflow input defaults.
	mergedInputs := MergeWorkflowInputDefaults(wf, inputs)

	// Build the set of workflow input IDs.
	wfInputIDs := make(map[string]bool, len(wf.Inputs))
	for _, inp := range wf.Inputs {
		wfInputIDs[inp.ID] = true
	}

	for _, step := range wf.Steps {
		// Only validate steps whose inputs come entirely from workflow
		// inputs (no upstream step outputs).
		if !allSourcesAreWorkflowInputs(step, wfInputIDs) {
			continue
		}

		// Find the tool for this step.
		toolRef := step.ToolRef
		var cwlTool *cwl.CommandLineTool
		for id, clt := range graphDoc.Tools {
			normalizedID := strings.TrimPrefix(id, "#")
			if normalizedID == toolRef || id == toolRef {
				cwlTool = clt
				break
			}
		}
		if cwlTool == nil {
			continue // Sub-workflow or expression tool — skip.
		}

		// Resolve step inputs.
		stepInputDefs := make([]stepinput.InputDef, len(step.In))
		for i, si := range step.In {
			stepInputDefs[i] = stepinput.InputDefFromModel(
				si.ID, si.Sources, si.Source,
				si.Default, si.ValueFrom, si.LoadContents, si.LinkMerge,
			)
		}
		job, err := stepinput.ResolveInputs(
			stepInputDefs, mergedInputs, nil,
			stepinput.Options{SkipAllValueFrom: true},
		)
		if err != nil {
			return fmt.Errorf("step %s: %w", step.ID, err)
		}

		// Apply record defaults then validate.
		validate.ApplyRecordFieldDefaults(cwlTool, job)

		if err := validate.ValidateRecordShape(cwlTool, job); err != nil {
			return fmt.Errorf("step %s: %w", step.ID, err)
		}
		if err := validate.ToolInputs(cwlTool, job); err != nil {
			return fmt.Errorf("step %s: %w", step.ID, err)
		}
	}

	return nil
}

// allSourcesAreWorkflowInputs returns true if every step input source is
// a workflow-level input (not an upstream step output).
func allSourcesAreWorkflowInputs(step model.Step, wfInputIDs map[string]bool) bool {
	for _, si := range step.In {
		sources := si.Sources
		if len(sources) == 0 && si.Source != "" {
			sources = strings.Split(si.Source, ",")
		}
		for _, src := range sources {
			// Step outputs contain "/" (e.g., "step_name/output_id").
			if strings.Contains(src, "/") {
				return false
			}
			// Workflow input reference.
			if !wfInputIDs[src] {
				return false
			}
		}
	}
	return true
}

// ValidateSubmissionInputsJSON is a convenience wrapper that accepts the
// raw JSON submission inputs and converts them. Used by the dry-run report.
func ValidateSubmissionInputsJSON(logger *slog.Logger, wf *model.Workflow, inputs map[string]any) []map[string]string {
	err := ValidateSubmissionInputs(logger, wf, inputs)
	if err == nil {
		return nil
	}
	return []map[string]string{
		{"field": "inputs", "message": err.Error()},
	}
}

// validateSubmissionInputsForHandler is a thin wrapper for use in HTTP
// handlers. It converts the result to a single error string or nil.
func validateSubmissionInputsForHandler(logger *slog.Logger, wf *model.Workflow, inputs map[string]any) error {
	return ValidateSubmissionInputs(logger, wf, inputs)
}


