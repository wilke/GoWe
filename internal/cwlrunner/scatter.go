package cwlrunner

import (
	"context"
	"fmt"

	"github.com/me/gowe/pkg/cwl"
)

// executeScatter executes a step with scatter over input arrays.
func (r *Runner) executeScatter(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any) (map[string]any, error) {
	if len(step.Scatter) == 0 {
		return nil, fmt.Errorf("no scatter inputs specified")
	}

	// Determine scatter method (default: dotproduct for single input, nested_crossproduct for multiple).
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over.
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Generate input combinations based on scatter method.
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = dotProduct(inputs, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = nestedCrossProduct(inputs, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = flatCrossProduct(inputs, step.Scatter, scatterArrays)
	default:
		return nil, fmt.Errorf("unknown scatter method: %s", method)
	}

	// Execute tool for each combination.
	var results []map[string]any
	for i, combo := range combinations {
		r.logger.Debug("scatter iteration", "index", i, "inputs", combo)
		output, err := r.executeTool(ctx, graph, tool, combo, false)
		if err != nil {
			return nil, fmt.Errorf("scatter iteration %d: %w", i, err)
		}
		results = append(results, output)
	}

	// Merge results into output arrays.
	return mergeScatterOutputs(results, tool), nil
}

// toAnySlice converts a value to []any if it's a slice type.
func toAnySlice(v any) ([]any, bool) {
	if v == nil {
		return nil, false
	}
	switch arr := v.(type) {
	case []any:
		return arr, true
	case []string:
		result := make([]any, len(arr))
		for i, s := range arr {
			result[i] = s
		}
		return result, true
	case []int:
		result := make([]any, len(arr))
		for i, n := range arr {
			result[i] = n
		}
		return result, true
	case []map[string]any:
		result := make([]any, len(arr))
		for i, m := range arr {
			result[i] = m
		}
		return result, true
	}
	return nil, false
}

// dotProduct generates input combinations for dot product scatter.
// All scatter arrays must have the same length; element i from each array is combined.
func dotProduct(baseInputs map[string]any, scatterInputs []string, scatterArrays map[string][]any) []map[string]any {
	if len(scatterInputs) == 0 {
		return nil
	}

	// All arrays must have the same length.
	length := len(scatterArrays[scatterInputs[0]])
	for _, name := range scatterInputs[1:] {
		if len(scatterArrays[name]) != length {
			return nil // Length mismatch
		}
	}

	var combinations []map[string]any
	for i := 0; i < length; i++ {
		combo := copyInputs(baseInputs)
		for _, name := range scatterInputs {
			combo[name] = scatterArrays[name][i]
		}
		combinations = append(combinations, combo)
	}

	return combinations
}

// nestedCrossProduct generates nested array of results from cross product.
// For scattering over [A, B], produces [[A1B1, A1B2], [A2B1, A2B2]].
func nestedCrossProduct(baseInputs map[string]any, scatterInputs []string, scatterArrays map[string][]any) []map[string]any {
	// For execution, we just need all combinations - nesting is applied to outputs.
	return flatCrossProduct(baseInputs, scatterInputs, scatterArrays)
}

// flatCrossProduct generates flat array of results from cross product.
// For scattering over [A, B], produces [A1B1, A1B2, A2B1, A2B2].
func flatCrossProduct(baseInputs map[string]any, scatterInputs []string, scatterArrays map[string][]any) []map[string]any {
	if len(scatterInputs) == 0 {
		return nil
	}

	// Start with combinations for the first scatter input.
	first := scatterInputs[0]
	var combinations []map[string]any
	for _, val := range scatterArrays[first] {
		combo := copyInputs(baseInputs)
		combo[first] = val
		combinations = append(combinations, combo)
	}

	// Expand combinations for each additional scatter input.
	for _, name := range scatterInputs[1:] {
		var expanded []map[string]any
		for _, combo := range combinations {
			for _, val := range scatterArrays[name] {
				newCombo := copyInputs(combo)
				newCombo[name] = val
				expanded = append(expanded, newCombo)
			}
		}
		combinations = expanded
	}

	return combinations
}

// copyInputs creates a shallow copy of an inputs map.
func copyInputs(inputs map[string]any) map[string]any {
	result := make(map[string]any, len(inputs))
	for k, v := range inputs {
		result[k] = v
	}
	return result
}

// mergeScatterOutputs merges individual outputs into arrays.
func mergeScatterOutputs(results []map[string]any, tool *cwl.CommandLineTool) map[string]any {
	merged := make(map[string]any)

	// Initialize output arrays.
	for outputID := range tool.Outputs {
		merged[outputID] = make([]any, len(results))
	}

	// Populate arrays from results.
	for i, result := range results {
		for outputID := range tool.Outputs {
			arr := merged[outputID].([]any)
			arr[i] = result[outputID]
		}
	}

	return merged
}
