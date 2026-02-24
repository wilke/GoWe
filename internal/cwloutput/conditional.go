// Package cwloutput provides shared CWL output processing utilities
// used by both cwlrunner and the scheduler.
package cwloutput

import (
	"fmt"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// ApplyPickValue selects a value from a list of values based on the pickValue mode.
// Returns the selected value and an error if the mode constraints are violated.
//
// Modes:
//   - "first_non_null": Returns the first non-null value; errors if all are null
//   - "the_only_non_null": Returns the single non-null value; errors if multiple or none
//   - "all_non_null": Returns an array of all non-null values (may be empty)
//   - "" (empty): Returns the values array as-is (default behavior for single source)
func ApplyPickValue(values []any, pickValue string) (any, error) {
	switch pickValue {
	case "first_non_null":
		for _, v := range values {
			if v != nil {
				return v, nil
			}
		}
		return nil, fmt.Errorf("pickValue first_non_null: all sources are null")

	case "the_only_non_null":
		var nonNull any
		count := 0
		for _, v := range values {
			if v != nil {
				nonNull = v
				count++
			}
		}
		if count == 0 {
			return nil, fmt.Errorf("pickValue the_only_non_null: no non-null values")
		}
		if count > 1 {
			return nil, fmt.Errorf("pickValue the_only_non_null: multiple non-null values (%d)", count)
		}
		return nonNull, nil

	case "all_non_null":
		result := make([]any, 0, len(values))
		for _, v := range values {
			if v != nil {
				result = append(result, v)
			}
		}
		return result, nil

	case "":
		// No pickValue - return single value or array
		if len(values) == 1 {
			return values[0], nil
		}
		return values, nil

	default:
		return nil, fmt.Errorf("unknown pickValue: %s", pickValue)
	}
}

// ApplyLinkMerge combines values according to the linkMerge mode.
//
// Modes:
//   - "merge_flattened": Flattens all arrays into a single array
//   - "merge_nested" (default): Keeps values as nested arrays
func ApplyLinkMerge(values []any, linkMerge string) []any {
	if linkMerge != "merge_flattened" {
		// Default is merge_nested - return as-is
		return values
	}

	// Flatten arrays
	var flattened []any
	for _, v := range values {
		if arr, ok := v.([]any); ok {
			flattened = append(flattened, arr...)
		} else {
			flattened = append(flattened, v)
		}
	}
	return flattened
}

// CollectWorkflowOutputs gathers workflow-level outputs from completed task outputs.
// It handles multiple sources, linkMerge, and pickValue as specified in the workflow outputs.
//
// Parameters:
//   - outputs: The workflow output definitions
//   - workflowInputs: The workflow-level inputs (for passthrough outputs)
//   - taskOutputs: Map of stepID -> task outputs map
//
// Returns the collected outputs map or an error if pickValue constraints are violated.
func CollectWorkflowOutputs(
	outputs []model.WorkflowOutput,
	workflowInputs map[string]any,
	taskOutputs map[string]map[string]any,
) (map[string]any, error) {
	result := make(map[string]any)

	for _, output := range outputs {
		// Collect all sources
		var sources []string
		if len(output.OutputSources) > 0 {
			sources = output.OutputSources
		} else if output.OutputSource != "" {
			sources = []string{output.OutputSource}
		} else {
			// No source - output is null
			result[output.ID] = nil
			continue
		}

		// Resolve each source
		var values []any
		for _, source := range sources {
			val := resolveSource(source, workflowInputs, taskOutputs)
			values = append(values, val)
		}

		// Apply linkMerge if multiple sources
		if len(sources) > 1 {
			values = ApplyLinkMerge(values, output.LinkMerge)
		}

		// Handle scatter outputs with pickValue
		// If single source is an array (from scatter), apply pickValue to array elements
		if len(sources) == 1 && output.PickValue != "" {
			if arr, ok := values[0].([]any); ok {
				values = arr
			}
		}

		// Apply pickValue
		val, err := ApplyPickValue(values, output.PickValue)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", output.ID, err)
		}
		result[output.ID] = val
	}

	return result, nil
}

// resolveSource resolves a single output source reference to its value.
// Source can be:
//   - "stepID/outputID" -> task output
//   - "inputID" -> workflow input (passthrough)
func resolveSource(source string, workflowInputs map[string]any, taskOutputs map[string]map[string]any) any {
	if strings.Contains(source, "/") {
		// Task output reference
		parts := strings.SplitN(source, "/", 2)
		stepID, outputID := parts[0], parts[1]
		if outputs, ok := taskOutputs[stepID]; ok {
			return outputs[outputID]
		}
		return nil
	}
	// Workflow input passthrough
	return workflowInputs[source]
}
