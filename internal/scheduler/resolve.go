package scheduler

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/internal/stepinput"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ResolveTaskInputs populates task.Inputs by resolving each StepInput source
// against either the submission-level workflow inputs or the outputs of upstream
// tasks. It also injects reserved keys (_base_command, _output_globs) from the
// step's inline tool definition when present.
//
// Uses the shared stepinput package to implement full CWL semantics including:
// - Multiple sources (MultipleInputFeatureRequirement)
// - Default values
// - loadContents
// - valueFrom expressions (StepInputExpressionRequirement)
func ResolveTaskInputs(
	task *model.Task,
	step *model.Step,
	submissionInputs map[string]any,
	tasksByStepID map[string]*model.Task,
	expressionLib []string,
) error {
	// Build stepOutputs from completed upstream tasks.
	stepOutputs := make(map[string]map[string]any)
	for stepID, t := range tasksByStepID {
		if t.State == model.TaskStateSuccess && t.Outputs != nil {
			stepOutputs[stepID] = t.Outputs
		}
	}

	// Convert model.StepInput to stepinput.InputDef.
	inputs := make([]stepinput.InputDef, len(step.In))
	for i, si := range step.In {
		inputs[i] = stepinput.InputDefFromModel(
			si.ID,
			si.Sources,
			si.Source,
			si.Default,
			si.ValueFrom,
			si.LoadContents,
		)
	}

	// Use shared resolution logic.
	resolved, err := stepinput.ResolveInputs(inputs, submissionInputs, stepOutputs, stepinput.Options{
		ExpressionLib: expressionLib,
	})
	if err != nil {
		return fmt.Errorf("resolve inputs for task %s: %w", task.ID, err)
	}

	// Inject reserved keys from the inline tool definition.
	if step.ToolInline != nil {
		// _base_command: convert []string to []any for JSON compatibility.
		if len(step.ToolInline.BaseCommand) > 0 {
			cmd := make([]any, len(step.ToolInline.BaseCommand))
			for i, s := range step.ToolInline.BaseCommand {
				cmd[i] = s
			}
			resolved["_base_command"] = cmd
		}

		// _output_globs: map output ID → glob pattern for outputs with globs.
		globs := make(map[string]any)
		for _, out := range step.ToolInline.Outputs {
			if out.Glob != "" {
				globs[out.ID] = out.Glob
			}
		}
		if len(globs) > 0 {
			resolved["_output_globs"] = globs
		}
	}

	// _docker_image: inject from step hints if present.
	if step.Hints != nil && step.Hints.DockerImage != "" {
		resolved["_docker_image"] = step.Hints.DockerImage
	}

	// _bvbrc_app_id: inject from step hints if present.
	if step.Hints != nil && step.Hints.BVBRCAppID != "" {
		resolved["_bvbrc_app_id"] = step.Hints.BVBRCAppID
	}

	// Normalize typed values (e.g., wrap string → CWL Directory object).
	if step.ToolInline != nil {
		execType := ""
		if step.Hints != nil {
			execType = string(step.Hints.ExecutorType)
		}
		typeMap := buildInputTypeMap(step.ToolInline.Inputs)
		for k, v := range resolved {
			if cwlType, ok := typeMap[k]; ok {
				resolved[k] = normalizeValue(v, cwlType, execType)
			}
		}
	}

	task.Inputs = resolved
	return nil
}

// buildInputTypeMap creates an ID → CWL type lookup from tool inputs.
func buildInputTypeMap(inputs []model.ToolInput) map[string]string {
	m := make(map[string]string, len(inputs))
	for _, inp := range inputs {
		m[inp.ID] = inp.Type
	}
	return m
}

// normalizeValue wraps raw values in CWL typed objects when needed.
func normalizeValue(val any, cwlType, execType string) any {
	if val == nil {
		return nil
	}
	baseType := strings.TrimSuffix(cwlType, "?")
	if baseType == "Directory" {
		return normalizeDirectory(val, execType)
	}
	return val
}

// normalizeDirectory ensures a value becomes a CWL Directory object
// with a proper URI-schemed location.
//   - string "ws:///path"         → {class: Directory, location: "ws:///path"}
//   - string "/bare/path"         → infer scheme from executor → {class: Directory, location: "ws:///bare/path"}
//   - map with class: "Directory" → pass-through
func normalizeDirectory(val any, execType string) any {
	switch v := val.(type) {
	case string:
		if v == "" {
			return nil
		}
		scheme, _ := cwl.ParseLocationScheme(v)
		if scheme == "" {
			v = cwl.BuildLocation(cwl.InferScheme(execType), v)
		}
		return map[string]any{"class": "Directory", "location": v}
	case map[string]any:
		if v["class"] == "Directory" {
			return v
		}
		if loc, ok := v["location"]; ok {
			return map[string]any{"class": "Directory", "location": loc}
		}
		return val
	default:
		return val
	}
}

// AreDependenciesSatisfied checks whether all upstream dependencies of the
// given task have completed successfully.
//
// Returns:
//   - satisfied=true,  blocked=false  — all deps are SUCCESS (or no deps).
//   - satisfied=false, blocked=true   — a dep is missing, FAILED, or SKIPPED.
//   - satisfied=false, blocked=false  — deps exist but are not yet finished.
func AreDependenciesSatisfied(
	task *model.Task,
	tasksByStepID map[string]*model.Task,
) (satisfied bool, blocked bool) {
	if len(task.DependsOn) == 0 {
		return true, false
	}

	for _, depStepID := range task.DependsOn {
		dep, ok := tasksByStepID[depStepID]
		if !ok {
			return false, true
		}

		switch dep.State {
		case model.TaskStateFailed, model.TaskStateSkipped:
			return false, true
		case model.TaskStateSuccess:
			continue
		default:
			// Still pending, running, etc.
			return false, false
		}
	}

	return true, false
}

// BuildTasksByStepID creates a lookup map from step ID to task pointer.
func BuildTasksByStepID(tasks []*model.Task) map[string]*model.Task {
	m := make(map[string]*model.Task, len(tasks))
	for _, t := range tasks {
		m[t.StepID] = t
	}
	return m
}

// MergeWorkflowInputDefaults merges workflow input defaults with submission inputs.
// Returns a new map with defaults applied for any missing inputs.
func MergeWorkflowInputDefaults(wf *model.Workflow, submissionInputs map[string]any) map[string]any {
	if wf == nil || len(wf.Inputs) == 0 {
		return submissionInputs
	}

	merged := make(map[string]any)

	// Copy provided inputs.
	for k, v := range submissionInputs {
		merged[k] = v
	}

	// Add defaults for missing inputs.
	for _, inp := range wf.Inputs {
		if _, exists := merged[inp.ID]; !exists && inp.Default != nil {
			merged[inp.ID] = inp.Default
		}
	}

	return merged
}

// ResolveWorkflowSecondaryFiles resolves secondaryFiles for workflow inputs based
// on the workflow's input declarations. This should be called after MergeWorkflowInputDefaults.
func ResolveWorkflowSecondaryFiles(wf *model.Workflow, inputs map[string]any, cwlDir string) map[string]any {
	if wf == nil || wf.RawCWL == "" {
		return inputs
	}

	// Parse the workflow to get full CWL input definitions with secondaryFiles.
	p := parser.New(slog.Default())
	graphDoc, err := p.ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		// If parsing fails, return inputs unchanged.
		return inputs
	}

	// Get the workflow definition.
	if graphDoc.Workflow == nil {
		return inputs
	}
	cwlWorkflow := graphDoc.Workflow

	result := make(map[string]any)
	for k, v := range inputs {
		result[k] = v
	}

	// Resolve secondaryFiles for each input based on workflow declarations.
	for inputID, inputDef := range cwlWorkflow.Inputs {
		val, exists := result[inputID]
		if !exists || val == nil {
			continue
		}

		// Handle secondaryFiles at the input level.
		if len(inputDef.SecondaryFiles) > 0 {
			result[inputID] = secondaryfiles.ResolveForValue(val, inputDef.SecondaryFiles, cwlDir)
			continue
		}

		// Handle record types with field-level secondaryFiles.
		if len(inputDef.RecordFields) > 0 {
			recordVal, ok := val.(map[string]any)
			if !ok {
				continue
			}

			// Create a copy to avoid modifying the original.
			resolvedRecord := make(map[string]any)
			for k, v := range recordVal {
				resolvedRecord[k] = v
			}

			// Resolve secondaryFiles for each field.
			for _, field := range inputDef.RecordFields {
				if len(field.SecondaryFiles) == 0 {
					continue
				}
				if fieldVal, exists := resolvedRecord[field.Name]; exists && fieldVal != nil {
					resolvedRecord[field.Name] = secondaryfiles.ResolveForValue(fieldVal, field.SecondaryFiles, cwlDir)
				}
			}
			result[inputID] = resolvedRecord
		}
	}

	return result
}
