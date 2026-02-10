package scheduler

import (
	"fmt"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// ResolveTaskInputs populates task.Inputs by resolving each StepInput source
// against either the submission-level workflow inputs or the outputs of upstream
// tasks. It also injects reserved keys (_base_command, _output_globs) from the
// step's inline tool definition when present.
func ResolveTaskInputs(
	task *model.Task,
	step *model.Step,
	submissionInputs map[string]any,
	tasksByStepID map[string]*model.Task,
) error {
	resolved := make(map[string]any, len(step.In))

	for _, si := range step.In {
		if si.Source == "" {
			continue
		}

		if strings.Contains(si.Source, "/") {
			// Upstream task output: "stepID/outputID"
			parts := strings.SplitN(si.Source, "/", 2)
			stepID, outputID := parts[0], parts[1]

			depTask, ok := tasksByStepID[stepID]
			if !ok {
				return fmt.Errorf("resolve: upstream step %q not found for input %q", stepID, si.ID)
			}

			val, ok := depTask.Outputs[outputID]
			if !ok {
				return fmt.Errorf("resolve: output %q not found on step %q for input %q", outputID, stepID, si.ID)
			}

			resolved[si.ID] = val
		} else {
			// Workflow-level input
			val, ok := submissionInputs[si.Source]
			if !ok {
				return fmt.Errorf("resolve: workflow input %q not found for input %q", si.Source, si.ID)
			}

			resolved[si.ID] = val
		}
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

	task.Inputs = resolved
	return nil
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
