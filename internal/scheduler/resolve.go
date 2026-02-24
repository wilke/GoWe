package scheduler

import (
	"fmt"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ResolveTaskInputs populates task.Inputs by resolving each StepInput source
// against either the submission-level workflow inputs or the outputs of upstream
// tasks. It also injects reserved keys (_base_command, _output_globs) from the
// step's inline tool definition when present.
//
// If expressionLib is provided and a step input has a valueFrom expression
// (StepInputExpressionRequirement), it will be evaluated with `self` set to
// the resolved source value and `inputs` set to the workflow inputs.
func ResolveTaskInputs(
	task *model.Task,
	step *model.Step,
	submissionInputs map[string]any,
	tasksByStepID map[string]*model.Task,
	expressionLib []string,
) error {
	resolved := make(map[string]any, len(step.In))

	// Create evaluator for valueFrom expressions if we have an expression library.
	var evaluator *cwlexpr.Evaluator
	if len(expressionLib) > 0 {
		evaluator = cwlexpr.NewEvaluator(expressionLib)
	} else {
		// Create evaluator without library for basic expression support.
		evaluator = cwlexpr.NewEvaluator(nil)
	}

	for _, si := range step.In {
		var val any
		var hasSource bool

		if si.Source == "" {
			hasSource = false
		} else if strings.Contains(si.Source, "/") {
			// Upstream task output: "stepID/outputID"
			parts := strings.SplitN(si.Source, "/", 2)
			stepID, outputID := parts[0], parts[1]

			depTask, ok := tasksByStepID[stepID]
			if !ok {
				return fmt.Errorf("resolve: upstream step %q not found for input %q", stepID, si.ID)
			}

			val, ok = depTask.Outputs[outputID]
			if !ok {
				// Tolerate missing outputs on SUCCESS tasks with no output
				// mapping (e.g. BV-BRC tasks that write to workspace, not
				// local files). If the task has some outputs but not the
				// requested one, still error (likely a source typo).
				if depTask.State == model.TaskStateSuccess && len(depTask.Outputs) == 0 {
					resolved[si.ID] = nil
					continue
				}
				return fmt.Errorf("resolve: output %q not found on step %q for input %q", outputID, stepID, si.ID)
			}
			hasSource = true
		} else {
			// Workflow-level input
			var ok bool
			val, ok = submissionInputs[si.Source]
			if !ok {
				return fmt.Errorf("resolve: workflow input %q not found for input %q", si.Source, si.ID)
			}
			hasSource = true
		}

		// Evaluate valueFrom expression if present (StepInputExpressionRequirement).
		if si.ValueFrom != "" {
			// Per CWL spec: `inputs` contains workflow inputs, `self` is the resolved source value.
			ctx := cwlexpr.NewContext(submissionInputs)
			if hasSource {
				ctx = ctx.WithSelf(val)
			}
			evaluated, err := evaluator.Evaluate(si.ValueFrom, ctx)
			if err != nil {
				return fmt.Errorf("resolve: step %q input %q valueFrom: %w", step.ID, si.ID, err)
			}
			val = evaluated
		} else if !hasSource {
			// No source and no valueFrom - skip this input.
			continue
		}

		resolved[si.ID] = val
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
