package cwlrunner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// hasValueFrom returns true if any step input has a valueFrom expression.
func hasValueFrom(step cwl.Step) bool {
	for _, si := range step.In {
		if si.ValueFrom != "" {
			return true
		}
	}
	return false
}

// whenReferencesScatterVars checks if the when expression references any scattered variables.
// This is a simple heuristic that looks for "inputs.<varname>" patterns.
func whenReferencesScatterVars(when string, scatterVars []string) bool {
	for _, v := range scatterVars {
		// Check for common patterns: inputs.varname or inputs["varname"]
		if strings.Contains(when, "inputs."+v) ||
			strings.Contains(when, "inputs[\""+v+"\"]") ||
			strings.Contains(when, "inputs['"+v+"']") {
			return true
		}
	}
	return false
}

// executeScatter executes a step with scatter over input arrays.
func (r *Runner) executeScatter(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any, evaluator ...*cwlexpr.Evaluator) (map[string]any, error) {
	if len(step.Scatter) == 0 {
		return nil, fmt.Errorf("no scatter inputs specified")
	}

	// Get evaluator if provided.
	var eval *cwlexpr.Evaluator
	if len(evaluator) > 0 {
		eval = evaluator[0]
	}

	// Check 'when' condition early (before scatter validation).
	// This handles cases where the condition depends on non-scattered variables
	// and allows skipping the step even if scatter inputs are invalid.
	// Skip early check if the expression references scattered variables.
	if step.When != "" && eval != nil && !whenReferencesScatterVars(step.When, step.Scatter) {
		evalCtx := cwlexpr.NewContext(inputs)
		shouldRun, err := eval.EvaluateBool(step.When, evalCtx)
		if err != nil {
			// If evaluation fails (e.g., depends on scattered vars), continue to per-iteration eval.
			r.logger.Debug("when condition pre-check failed, will evaluate per-iteration", "error", err)
		} else if !shouldRun {
			r.logger.Info("skipping scatter step (when condition false)", "step", step.Run)
			// Return empty outputs for skipped scatter step.
			outputs := make(map[string]any)
			for _, outID := range step.Out {
				outputs[outID] = nil
			}
			return outputs, nil
		}
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

	// Evaluate valueFrom expressions per scatter iteration (after scatter expansion).
	if eval != nil && hasValueFrom(step) {
		for _, combo := range combinations {
			if err := evaluateValueFrom(step, combo, eval); err != nil {
				return nil, fmt.Errorf("scatter valueFrom: %w", err)
			}
		}
	}

	// Execute tool for each combination, evaluating 'when' condition per iteration.
	var results []map[string]any
	for i, combo := range combinations {
		r.logger.Debug("scatter iteration", "index", i, "inputs", combo)

		// Evaluate 'when' condition if present.
		if step.When != "" && eval != nil {
			evalCtx := cwlexpr.NewContext(combo)
			shouldRun, err := eval.EvaluateBool(step.When, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("scatter iteration %d when: %w", i, err)
			}
			if !shouldRun {
				// Condition is false for this iteration - output null for all outputs.
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				results = append(results, nullOutputs)
				continue
			}
		}

		output, err := r.executeTool(ctx, graph, tool, combo, false)
		if err != nil {
			return nil, fmt.Errorf("scatter iteration %d: %w", i, err)
		}
		results = append(results, output)
	}

	// Merge results into output arrays.
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		// Calculate dimensions for nested output structure.
		dims := make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			dims[i] = len(scatterArrays[name])
		}
		return mergeScatterOutputsNested(results, tool, dims), nil
	}
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

// mergeScatterOutputsNested merges individual outputs into nested arrays for nested_crossproduct.
// dims contains the size of each scatter dimension, e.g., [2, 2] for two scatter inputs of size 2.
// Results are structured as nested arrays: [[r0, r1], [r2, r3]] for dims=[2, 2].
func mergeScatterOutputsNested(results []map[string]any, tool *cwl.CommandLineTool, dims []int) map[string]any {
	merged := make(map[string]any)

	for outputID := range tool.Outputs {
		// Build nested array structure.
		merged[outputID] = nestResults(results, dims, 0, outputID)
	}

	return merged
}

// nestResults recursively builds nested arrays from flat results.
func nestResults(results []map[string]any, dims []int, dimIdx int, outputID string) any {
	if dimIdx >= len(dims) || len(results) == 0 {
		return nil
	}

	outerSize := dims[dimIdx]
	if dimIdx == len(dims)-1 {
		// Base case: innermost dimension, return flat array.
		arr := make([]any, min(outerSize, len(results)))
		for i := range arr {
			if results[i] != nil {
				arr[i] = results[i][outputID]
			}
		}
		return arr
	}

	// Calculate size of each inner chunk.
	innerSize := 1
	for _, d := range dims[dimIdx+1:] {
		innerSize *= d
	}

	// Build outer array with nested inner arrays.
	arr := make([]any, outerSize)
	for i := 0; i < outerSize; i++ {
		start := i * innerSize
		end := start + innerSize
		if end > len(results) {
			end = len(results)
		}
		if start >= len(results) {
			break
		}
		arr[i] = nestResults(results[start:end], dims, dimIdx+1, outputID)
	}

	return arr
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

// scatterResult holds the result of a single scatter iteration.
type scatterResult struct {
	index   int
	outputs map[string]any
	err     error
}

// executeScatterParallel executes scatter iterations in parallel.
func (r *Runner) executeScatterParallel(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any,
	config ParallelConfig, evaluator ...*cwlexpr.Evaluator) (map[string]any, error) {

	if len(step.Scatter) == 0 {
		return nil, fmt.Errorf("no scatter inputs specified")
	}

	// Determine scatter method
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Get evaluator if provided.
	var eval *cwlexpr.Evaluator
	if len(evaluator) > 0 {
		eval = evaluator[0]
	}

	// Generate input combinations
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

	// Evaluate valueFrom expressions per scatter iteration (after scatter expansion).
	if eval != nil && hasValueFrom(step) {
		for _, combo := range combinations {
			if err := evaluateValueFrom(step, combo, eval); err != nil {
				return nil, fmt.Errorf("scatter valueFrom: %w", err)
			}
		}
	}

	n := len(combinations)
	if n == 0 {
		return mergeScatterOutputs(nil, tool), nil
	}

	r.logger.Debug("executing scatter in parallel",
		"iterations", n,
		"max_concurrent", config.Semaphore.Capacity())

	// Create cancellable context for fail-fast
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use global semaphore for bounded parallelism across all steps and scatter iterations
	sem := config.Semaphore

	// Results storage (pre-allocated for order preservation)
	results := make([]scatterResult, n)

	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for i, combo := range combinations {
		wg.Add(1)
		go func(idx int, inputsCopy map[string]any) {
			defer wg.Done()

			// Acquire semaphore slot from global semaphore
			if !sem.Acquire(ctx) {
				results[idx] = scatterResult{index: idx, err: ctx.Err()}
				return
			}
			defer sem.Release()

			// Check if we should stop due to earlier error
			select {
			case <-ctx.Done():
				results[idx] = scatterResult{index: idx, err: ctx.Err()}
				return
			default:
			}

			r.logger.Debug("scatter iteration start", "index", idx)

			outputs, err := r.executeTool(ctx, graph, tool, inputsCopy, false)

			results[idx] = scatterResult{
				index:   idx,
				outputs: outputs,
				err:     err,
			}

			if err != nil && config.FailFast {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("scatter iteration %d: %w", idx, err)
					cancel()
				})
			}

			r.logger.Debug("scatter iteration complete", "index", idx, "error", err)
		}(i, combo)
	}

	wg.Wait()

	// Check for errors
	if firstErr != nil {
		return nil, firstErr
	}

	// If not fail-fast, check all results for errors
	if !config.FailFast {
		for _, res := range results {
			if res.err != nil {
				return nil, fmt.Errorf("scatter iteration %d: %w", res.index, res.err)
			}
		}
	}

	// Collect outputs in order
	outputMaps := make([]map[string]any, n)
	for i, res := range results {
		outputMaps[i] = res.outputs
	}

	// Apply nested structure for nested_crossproduct.
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		dims := make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			dims[i] = len(scatterArrays[name])
		}
		return mergeScatterOutputsNested(outputMaps, tool, dims), nil
	}
	return mergeScatterOutputs(outputMaps, tool), nil
}

// executeScatterParallelWithMetrics executes scatter iterations in parallel and records per-iteration metrics.
func (r *Runner) executeScatterParallelWithMetrics(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any, stepID string,
	config ParallelConfig, evaluator *cwlexpr.Evaluator) (map[string]any, error) {

	startTime := time.Now()

	// Execute the parallel scatter with iteration tracking
	outputs, iterMetrics, err := r.executeScatterParallelWithIterationMetrics(ctx, graph, tool, step, inputs, config, evaluator)

	duration := time.Since(startTime)

	// Record metrics for the scatter step
	if r.metrics != nil && r.metrics.Enabled() {
		status := "success"
		if err != nil {
			status = "failed"
		}

		stepMetrics := StepMetrics{
			StepID:     stepID,
			ToolID:     tool.ID,
			StartTime:  startTime,
			Duration:   duration,
			Status:     status,
			Iterations: iterMetrics,
		}

		// Compute scatter summary from iteration metrics
		if len(iterMetrics) > 0 {
			stepMetrics.ScatterSummary = ComputeScatterSummary(iterMetrics)
		}

		r.metrics.RecordStep(stepMetrics)
	}

	return outputs, err
}

// scatterResultWithMetrics holds the result of a single scatter iteration including metrics.
type scatterResultWithMetrics struct {
	index   int
	outputs map[string]any
	metrics IterationMetrics
	err     error
}

// executeScatterParallelWithIterationMetrics executes scatter in parallel and returns per-iteration metrics.
func (r *Runner) executeScatterParallelWithIterationMetrics(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any,
	config ParallelConfig, evaluator *cwlexpr.Evaluator) (map[string]any, []IterationMetrics, error) {

	if len(step.Scatter) == 0 {
		return nil, nil, fmt.Errorf("no scatter inputs specified")
	}

	// Determine scatter method
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Get evaluator if provided.
	var eval *cwlexpr.Evaluator
	if evaluator != nil {
		eval = evaluator
	}

	// Generate input combinations
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = dotProduct(inputs, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = nestedCrossProduct(inputs, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = flatCrossProduct(inputs, step.Scatter, scatterArrays)
	default:
		return nil, nil, fmt.Errorf("unknown scatter method: %s", method)
	}

	// Evaluate valueFrom expressions per scatter iteration
	if eval != nil && hasValueFrom(step) {
		for _, combo := range combinations {
			if err := evaluateValueFrom(step, combo, eval); err != nil {
				return nil, nil, fmt.Errorf("scatter valueFrom: %w", err)
			}
		}
	}

	n := len(combinations)
	if n == 0 {
		return mergeScatterOutputs(nil, tool), nil, nil
	}

	r.logger.Debug("executing scatter in parallel with metrics",
		"iterations", n,
		"max_concurrent", config.Semaphore.Capacity())

	// Create cancellable context for fail-fast
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use global semaphore for bounded parallelism
	sem := config.Semaphore

	// Results storage (pre-allocated for order preservation)
	results := make([]scatterResultWithMetrics, n)

	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for i, combo := range combinations {
		wg.Add(1)
		go func(idx int, inputsCopy map[string]any) {
			defer wg.Done()

			iterStart := time.Now()

			// Acquire semaphore slot from global semaphore
			if !sem.Acquire(ctx) {
				results[idx] = scatterResultWithMetrics{
					index: idx,
					err:   ctx.Err(),
					metrics: IterationMetrics{
						Index:       idx,
						Duration:    time.Since(iterStart),
						DurationStr: formatDuration(time.Since(iterStart)),
						Status:      "failed",
					},
				}
				return
			}
			defer sem.Release()

			// Check if we should stop due to earlier error
			select {
			case <-ctx.Done():
				results[idx] = scatterResultWithMetrics{
					index: idx,
					err:   ctx.Err(),
					metrics: IterationMetrics{
						Index:       idx,
						Duration:    time.Since(iterStart),
						DurationStr: formatDuration(time.Since(iterStart)),
						Status:      "failed",
					},
				}
				return
			default:
			}

			r.logger.Debug("scatter iteration start", "index", idx)

			// Execute the tool using internal method
			execResult, err := r.executeToolInternal(ctx, graph, tool, inputsCopy)

			iterDuration := time.Since(iterStart)
			var iterMetrics IterationMetrics

			if err != nil {
				iterMetrics = IterationMetrics{
					Index:       idx,
					Duration:    iterDuration,
					DurationStr: formatDuration(iterDuration),
					Status:      "failed",
				}
			} else {
				iterMetrics = IterationMetrics{
					Index:        idx,
					Duration:     execResult.Duration,
					DurationStr:  formatDuration(execResult.Duration),
					PeakMemoryKB: execResult.PeakMemoryKB,
					ExitCode:     execResult.ExitCode,
					Status:       "success",
				}
			}

			results[idx] = scatterResultWithMetrics{
				index:   idx,
				outputs: nil,
				metrics: iterMetrics,
				err:     err,
			}
			if execResult != nil {
				results[idx].outputs = execResult.Outputs
			}

			if err != nil && config.FailFast {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("scatter iteration %d: %w", idx, err)
					cancel()
				})
			}

			r.logger.Debug("scatter iteration complete", "index", idx, "error", err)
		}(i, combo)
	}

	wg.Wait()

	// Check for errors
	if firstErr != nil {
		return nil, nil, firstErr
	}

	// If not fail-fast, check all results for errors
	if !config.FailFast {
		for _, res := range results {
			if res.err != nil {
				return nil, nil, fmt.Errorf("scatter iteration %d: %w", res.index, res.err)
			}
		}
	}

	// Collect outputs and metrics in order
	outputMaps := make([]map[string]any, n)
	iterMetrics := make([]IterationMetrics, n)
	for i, res := range results {
		outputMaps[i] = res.outputs
		iterMetrics[i] = res.metrics
	}

	// Apply nested structure for nested_crossproduct.
	var outputs map[string]any
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		dims := make([]int, len(step.Scatter))
		for j, name := range step.Scatter {
			dims[j] = len(scatterArrays[name])
		}
		outputs = mergeScatterOutputsNested(outputMaps, tool, dims)
	} else {
		outputs = mergeScatterOutputs(outputMaps, tool)
	}

	return outputs, iterMetrics, nil
}
