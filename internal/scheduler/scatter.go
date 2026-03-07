package scheduler

import (
	"fmt"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/model"
)

// Scatter combination and merge functions for the scheduler.
// These operate on model.Step / map[string]any types rather than
// cwl.Step / cwl.CommandLineTool types used in cwlrunner/scatter.go.

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

// copyInputs creates a shallow copy of an inputs map.
func copyInputs(inputs map[string]any) map[string]any {
	result := make(map[string]any, len(inputs))
	for k, v := range inputs {
		result[k] = v
	}
	return result
}

// scatterDotProduct generates input combinations for dot product scatter.
// All scatter arrays must have the same length; element i from each array is combined.
func scatterDotProduct(baseInputs map[string]any, scatterInputs []string, scatterArrays map[string][]any) []map[string]any {
	if len(scatterInputs) == 0 {
		return nil
	}

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

// scatterFlatCrossProduct generates flat array of results from cross product.
func scatterFlatCrossProduct(baseInputs map[string]any, scatterInputs []string, scatterArrays map[string][]any) []map[string]any {
	if len(scatterInputs) == 0 {
		return nil
	}

	first := scatterInputs[0]
	var combinations []map[string]any
	for _, val := range scatterArrays[first] {
		combo := copyInputs(baseInputs)
		combo[first] = val
		combinations = append(combinations, combo)
	}

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

// mergeScatterResults merges individual iteration outputs into arrays.
// outputIDs is the list of step output IDs (from step.Out).
func mergeScatterResults(results []map[string]any, outputIDs []string) map[string]any {
	merged := make(map[string]any)

	for _, outputID := range outputIDs {
		arr := make([]any, len(results))
		for i, result := range results {
			if result != nil {
				arr[i] = result[outputID]
			}
		}
		merged[outputID] = arr
	}

	return merged
}

// mergeScatterResultsNested merges outputs into nested arrays for nested_crossproduct.
// dims contains the size of each scatter dimension.
func mergeScatterResultsNested(results []map[string]any, outputIDs []string, dims []int) map[string]any {
	merged := make(map[string]any)

	for _, outputID := range outputIDs {
		merged[outputID] = nestResults(results, dims, 0, outputID)
	}

	return merged
}

// nestResults recursively builds nested arrays from flat results.
func nestResults(results []map[string]any, dims []int, dimIdx int, outputID string) any {
	if dimIdx >= len(dims) {
		return nil
	}

	outerSize := dims[dimIdx]

	hasZeroDim := false
	for _, d := range dims[dimIdx:] {
		if d == 0 {
			hasZeroDim = true
			break
		}
	}

	if dimIdx == len(dims)-1 {
		if hasZeroDim || len(results) == 0 {
			return make([]any, outerSize)
		}
		arr := make([]any, min(outerSize, len(results)))
		for i := range arr {
			if results[i] != nil {
				arr[i] = results[i][outputID]
			}
		}
		return arr
	}

	innerSize := 1
	for _, d := range dims[dimIdx+1:] {
		innerSize *= d
	}

	arr := make([]any, outerSize)
	for i := 0; i < outerSize; i++ {
		if hasZeroDim || innerSize == 0 {
			arr[i] = nestResults(nil, dims, dimIdx+1, outputID)
		} else {
			start := i * innerSize
			end := start + innerSize
			if end > len(results) {
				end = len(results)
			}
			if start >= len(results) {
				arr[i] = nestResults(nil, dims, dimIdx+1, outputID)
			} else {
				arr[i] = nestResults(results[start:end], dims, dimIdx+1, outputID)
			}
		}
	}

	return arr
}

// hasStepValueFrom returns true if any step input has a valueFrom expression.
func hasStepValueFrom(step *model.Step) bool {
	for _, si := range step.In {
		if si.ValueFrom != "" {
			return true
		}
	}
	return false
}

// applyScatterValueFrom evaluates valueFrom expressions on a single scatter
// iteration's inputs. This implements the CWL v1.2 requirement that valueFrom
// is applied AFTER scatter splits the array, so `self` refers to the individual
// element rather than the whole array.
func applyScatterValueFrom(step *model.Step, combo map[string]any, workflowInputs map[string]any, expressionLib []string) error {
	evaluator := cwlexpr.NewEvaluator(expressionLib)

	// Build the inputs context snapshot (before valueFrom transformation).
	inputsCtx := make(map[string]any)
	for k, v := range workflowInputs {
		inputsCtx[k] = v
	}
	for k, v := range combo {
		inputsCtx[k] = v
	}

	for _, si := range step.In {
		if si.ValueFrom == "" {
			continue
		}
		self := combo[si.ID]
		ctx := cwlexpr.NewContext(inputsCtx).WithSelf(self)
		evaluated, err := evaluator.Evaluate(si.ValueFrom, ctx)
		if err != nil {
			return fmt.Errorf("input %s valueFrom: %w", si.ID, err)
		}
		combo[si.ID] = evaluated
	}
	return nil
}
