package cwlrunner

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
)

// ParallelConfig configures parallel execution behavior.
type ParallelConfig struct {
	// Enabled controls whether parallel execution is used.
	Enabled bool

	// MaxWorkers limits concurrent step/scatter executions.
	// Default: runtime.NumCPU()
	MaxWorkers int

	// FailFast stops execution on first error.
	// Default: true
	FailFast bool
}

// DefaultParallelConfig returns the default parallel configuration.
func DefaultParallelConfig() ParallelConfig {
	return ParallelConfig{
		Enabled:    false,
		MaxWorkers: runtime.NumCPU(),
		FailFast:   true,
	}
}

// stepJob represents a step to be executed.
type stepJob struct {
	stepID string
	step   cwl.Step
	inputs map[string]any
}

// stepResult represents the result of a step execution.
type stepResult struct {
	stepID  string
	outputs map[string]any
	err     error
}

// parallelExecutor manages concurrent workflow step execution.
type parallelExecutor struct {
	runner     *Runner
	graph      *cwl.GraphDocument
	dag        *parser.DAGResult
	config     ParallelConfig
	workflowInputs map[string]any
	evaluator  *cwlexpr.Evaluator // For valueFrom and when expressions

	// Step state tracking
	mu          sync.RWMutex
	stepOutputs map[string]map[string]any // Completed step outputs
	pending     map[string][]string       // stepID -> dependencies not yet satisfied
	dependents  map[string][]string       // stepID -> steps that depend on this one
}

// newParallelExecutor creates a new parallel executor.
func newParallelExecutor(r *Runner, graph *cwl.GraphDocument, dag *parser.DAGResult,
	workflowInputs map[string]any, config ParallelConfig) *parallelExecutor {

	pe := &parallelExecutor{
		runner:         r,
		graph:          graph,
		dag:            dag,
		config:         config,
		workflowInputs: workflowInputs,
		evaluator:      cwlexpr.NewEvaluator(extractExpressionLib(graph)),
		stepOutputs:    make(map[string]map[string]any),
		pending:        make(map[string][]string),
		dependents:     make(map[string][]string),
	}

	// Build dependency maps from DAG
	pe.initDependencies()

	return pe
}

// initDependencies builds the dependency tracking maps from the DAG.
func (pe *parallelExecutor) initDependencies() {
	// For each step, find its dependencies by checking DAG edges
	for _, stepID := range pe.dag.Order {
		var deps []string
		// Check all edges to find dependencies (steps that must complete before this one)
		for sourceID, targets := range pe.dag.Edges {
			for _, target := range targets {
				if target == stepID {
					deps = append(deps, sourceID)
				}
			}
		}
		pe.pending[stepID] = deps

		// Build reverse map (dependents)
		for _, dep := range deps {
			pe.dependents[dep] = append(pe.dependents[dep], stepID)
		}
	}
}

// getReadySteps returns steps that have all dependencies satisfied.
func (pe *parallelExecutor) getReadySteps() []string {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	var ready []string
	for stepID, deps := range pe.pending {
		if len(deps) == 0 {
			ready = append(ready, stepID)
		}
	}
	return ready
}

// markCompleted marks a step as completed and updates dependency tracking.
// Returns the list of steps that are now ready to execute.
func (pe *parallelExecutor) markCompleted(stepID string, outputs map[string]any) []string {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// Store outputs
	pe.stepOutputs[stepID] = outputs

	// Remove from pending
	delete(pe.pending, stepID)

	// Update dependents - remove this step from their dependency list
	var newlyReady []string
	for _, dependentID := range pe.dependents[stepID] {
		deps := pe.pending[dependentID]
		// Remove stepID from deps
		var newDeps []string
		for _, d := range deps {
			if d != stepID {
				newDeps = append(newDeps, d)
			}
		}
		pe.pending[dependentID] = newDeps

		// If no more dependencies, it's ready
		if len(newDeps) == 0 {
			newlyReady = append(newlyReady, dependentID)
		}
	}

	return newlyReady
}

// getStepOutputs returns the outputs for completed steps (thread-safe copy).
func (pe *parallelExecutor) getStepOutputs() map[string]map[string]any {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	result := make(map[string]map[string]any, len(pe.stepOutputs))
	for k, v := range pe.stepOutputs {
		result[k] = v
	}
	return result
}

// execute runs the workflow with parallel step execution.
func (pe *parallelExecutor) execute(ctx context.Context) (map[string]any, error) {
	totalSteps := len(pe.dag.Order)
	if totalSteps == 0 {
		return collectWorkflowOutputs(pe.graph.Workflow, pe.workflowInputs, pe.stepOutputs), nil
	}

	// Create cancellable context for fail-fast
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Channels for job distribution and result collection
	jobs := make(chan stepJob, totalSteps)
	results := make(chan stepResult, totalSteps)

	// Start worker pool
	var wg sync.WaitGroup
	numWorkers := pe.config.MaxWorkers
	if numWorkers > totalSteps {
		numWorkers = totalSteps
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	pe.runner.logger.Debug("starting parallel execution",
		"steps", totalSteps,
		"workers", numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go pe.stepWorker(ctx, jobs, results, &wg)
	}

	// Track execution state
	inFlight := 0
	completedCount := 0
	var firstErr error

	// Enqueue initially ready steps
	readySteps := pe.getReadySteps()
	for _, stepID := range readySteps {
		job, err := pe.createStepJob(stepID)
		if err != nil {
			close(jobs)
			cancel()
			wg.Wait()
			return nil, fmt.Errorf("prepare step %s: %w", stepID, err)
		}

		select {
		case jobs <- job:
			inFlight++
			// Remove from pending since we're processing it
			pe.mu.Lock()
			delete(pe.pending, stepID)
			pe.mu.Unlock()
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		}
	}

	// Process results and enqueue newly ready steps
	for completedCount < totalSteps {
		select {
		case result := <-results:
			inFlight--
			completedCount++

			if result.err != nil {
				pe.runner.logger.Error("step failed",
					"step", result.stepID,
					"error", result.err)

				if pe.config.FailFast {
					firstErr = fmt.Errorf("step %s: %w", result.stepID, result.err)
					cancel()
					// Drain remaining in-flight jobs
					for inFlight > 0 {
						<-results
						inFlight--
						completedCount++
					}
					close(jobs)
					wg.Wait()
					return nil, firstErr
				}
				// Continue with other steps if not fail-fast
				continue
			}

			pe.runner.logger.Debug("step completed",
				"step", result.stepID,
				"completed", completedCount,
				"total", totalSteps)

			// Mark completed and get newly ready steps
			newlyReady := pe.markCompleted(result.stepID, result.outputs)

			// Enqueue newly ready steps
			for _, stepID := range newlyReady {
				job, err := pe.createStepJob(stepID)
				if err != nil {
					if pe.config.FailFast {
						close(jobs)
						cancel()
						wg.Wait()
						return nil, fmt.Errorf("prepare step %s: %w", stepID, err)
					}
					continue
				}

				select {
				case jobs <- job:
					inFlight++
				case <-ctx.Done():
					close(jobs)
					wg.Wait()
					return nil, ctx.Err()
				}
			}

		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, ctx.Err()
		}
	}

	close(jobs)
	wg.Wait()

	// Collect workflow outputs
	return collectWorkflowOutputs(pe.graph.Workflow, pe.workflowInputs, pe.getStepOutputs()), nil
}

// createStepJob creates a job for the given step.
func (pe *parallelExecutor) createStepJob(stepID string) (stepJob, error) {
	step := pe.graph.Workflow.Steps[stepID]
	stepOutputs := pe.getStepOutputs()
	// For scattered steps, defer valueFrom evaluation to after scatter expansion.
	var stepEvaluator *cwlexpr.Evaluator
	if len(step.Scatter) == 0 {
		stepEvaluator = pe.evaluator
	}
	stepInputs, err := resolveStepInputs(step, pe.workflowInputs, stepOutputs, pe.runner.cwlDir, stepEvaluator)
	if err != nil {
		return stepJob{}, fmt.Errorf("step %s: %w", stepID, err)
	}

	return stepJob{
		stepID: stepID,
		step:   step,
		inputs: stepInputs,
	}, nil
}

// stepWorker processes steps from the jobs channel.
func (pe *parallelExecutor) stepWorker(ctx context.Context, jobs <-chan stepJob,
	results chan<- stepResult, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				return
			}

			outputs, err := pe.executeStep(ctx, job)

			select {
			case results <- stepResult{
				stepID:  job.stepID,
				outputs: outputs,
				err:     err,
			}:
			case <-ctx.Done():
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// executeStep executes a single workflow step.
func (pe *parallelExecutor) executeStep(ctx context.Context, job stepJob) (map[string]any, error) {
	pe.runner.logger.Info("executing step (parallel)", "step", job.stepID)

	// Check if this is an ExpressionTool
	toolRef := stripHash(job.step.Run)
	if exprTool, ok := pe.graph.ExpressionTools[toolRef]; ok {
		// Handle conditional execution
		if job.step.When != "" {
			evalCtx := cwlexpr.NewContext(job.inputs)
			shouldRun, err := pe.evaluator.EvaluateBool(job.step.When, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("when expression: %w", err)
			}
			if !shouldRun {
				pe.runner.logger.Info("skipping step (when condition false)", "step", job.stepID)
				return make(map[string]any), nil
			}
		}

		return pe.runner.executeExpressionTool(exprTool, job.inputs, pe.graph)
	}

	// Otherwise it's a CommandLineTool
	tool := pe.graph.Tools[toolRef]
	if tool == nil {
		return nil, fmt.Errorf("tool %s not found", job.step.Run)
	}

	// Handle scatter if present
	if len(job.step.Scatter) > 0 {
		if pe.config.Enabled && pe.config.MaxWorkers > 1 {
			return pe.runner.executeScatterParallel(ctx, pe.graph, tool, job.step, job.inputs, pe.config, pe.evaluator)
		}
		return pe.runner.executeScatter(ctx, pe.graph, tool, job.step, job.inputs, pe.evaluator)
	}

	// Handle conditional execution
	if job.step.When != "" {
		evalCtx := cwlexpr.NewContext(job.inputs)
		shouldRun, err := pe.evaluator.EvaluateBool(job.step.When, evalCtx)
		if err != nil {
			return nil, fmt.Errorf("when expression: %w", err)
		}
		if !shouldRun {
			pe.runner.logger.Info("skipping step (when condition false)", "step", job.stepID)
			return make(map[string]any), nil
		}
	}

	return pe.runner.executeTool(ctx, pe.graph, tool, job.inputs, false)
}

// newCWLExprEvaluator creates a new expression evaluator with the given library.
func newCWLExprEvaluator(lib []string) *cwlexpr.Evaluator {
	return cwlexpr.NewEvaluator(lib)
}

// newCWLExprContext creates a new expression context with the given inputs.
func newCWLExprContext(inputs map[string]any) *cwlexpr.Context {
	return cwlexpr.NewContext(inputs)
}
