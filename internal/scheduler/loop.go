package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/cwloutput"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/exprtool"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/stepinput"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Config holds scheduler configuration.
type Config struct {
	PollInterval    time.Duration
	MaxRetries      int    // Default max retries for tasks (0 = no retries).
	DefaultExecutor string // Server-wide default executor (overrides CWL hints). Empty = hint-based.
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{PollInterval: 2 * time.Second, MaxRetries: 3}
}

// Loop implements the Scheduler interface with a polling-based scheduling loop.
type Loop struct {
	store    store.Store
	registry *executor.Registry
	config   Config
	logger   *slog.Logger
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewLoop creates a new scheduler loop.
func NewLoop(st store.Store, reg *executor.Registry, cfg Config, logger *slog.Logger) *Loop {
	return &Loop{
		store:    st,
		registry: reg,
		config:   cfg,
		logger:   logger.With("component", "scheduler"),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start begins the scheduling loop. Blocks until ctx is cancelled or Stop is called.
func (l *Loop) Start(ctx context.Context) error {
	l.logger.Info("scheduler started", "poll_interval", l.config.PollInterval)
	ticker := time.NewTicker(l.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.logger.Info("scheduler stopping (context cancelled)")
			close(l.doneCh)
			return ctx.Err()
		case <-l.stopCh:
			l.logger.Info("scheduler stopping (stop called)")
			close(l.doneCh)
			return nil
		case <-ticker.C:
			if err := l.Tick(ctx); err != nil {
				l.logger.Error("tick error", "error", err)
			}
		}
	}
}

// Stop gracefully shuts down the scheduler and waits for the current tick to finish.
// Safe to call multiple times.
func (l *Loop) Stop() error {
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
	<-l.doneCh
	return nil
}

// Tick runs a single scheduling iteration using the 3-level state architecture:
// Submissions → StepInstances → Tasks.
func (l *Loop) Tick(ctx context.Context) error {
	affected := make(map[string]bool) // submissionIDs touched this tick

	// Phase 1: Advance WAITING StepInstances to READY when all dependencies are met.
	if err := l.advanceWaiting(ctx, affected); err != nil {
		return fmt.Errorf("phase 1 (waiting): %w", err)
	}

	// Phase 2: Dispatch READY StepInstances — resolve inputs, create Tasks, submit to executors.
	if err := l.dispatchReady(ctx, affected); err != nil {
		return fmt.Errorf("phase 2 (dispatch): %w", err)
	}

	// Phase 2.5: Re-submit RETRYING tasks.
	if err := l.resubmitRetrying(ctx, affected); err != nil {
		return fmt.Errorf("phase 2.5 (retry): %w", err)
	}

	// Phase 3: Poll QUEUED/RUNNING tasks for status updates (async executors).
	if err := l.pollInFlight(ctx, affected); err != nil {
		return fmt.Errorf("phase 3 (poll): %w", err)
	}

	// Phase 4: Advance DISPATCHED/RUNNING StepInstances when all their Tasks are terminal.
	if err := l.advanceSteps(ctx, affected); err != nil {
		return fmt.Errorf("phase 4 (advance steps): %w", err)
	}

	// Phase 5: Finalize submissions where all StepInstances are terminal.
	if err := l.finalizeSubmissions(ctx, affected); err != nil {
		return fmt.Errorf("phase 5 (finalize): %w", err)
	}

	// Phase 6: Transition newly-FAILED tasks to RETRYING if retries remain.
	if err := l.markRetries(ctx, affected); err != nil {
		return fmt.Errorf("phase 6 (retries): %w", err)
	}

	return nil
}

// advanceWaiting transitions WAITING StepInstances to READY (deps met) or SKIPPED (blocked).
func (l *Loop) advanceWaiting(ctx context.Context, affected map[string]bool) error {
	waiting, err := l.store.ListStepsByState(ctx, model.StepStateWaiting)
	if err != nil {
		return err
	}
	if len(waiting) == 0 {
		return nil
	}

	// Group by submission to load sibling steps once per submission.
	bySubmission := make(map[string][]*model.StepInstance)
	for _, si := range waiting {
		bySubmission[si.SubmissionID] = append(bySubmission[si.SubmissionID], si)
	}

	for subID, steps := range bySubmission {
		// Load all step instances for this submission to check dependencies.
		allSteps, err := l.store.ListStepsBySubmission(ctx, subID)
		if err != nil {
			l.logger.Error("list steps for submission", "submission_id", subID, "error", err)
			continue
		}
		stepByStepID := make(map[string]*model.StepInstance)
		for _, s := range allSteps {
			stepByStepID[s.StepID] = s
		}

		// Load workflow to get step dependencies.
		sub, err := l.store.GetSubmission(ctx, subID)
		if err != nil || sub == nil {
			l.logger.Error("get submission for advance", "submission_id", subID, "error", err)
			continue
		}
		wf, err := l.store.GetWorkflow(ctx, sub.WorkflowID)
		if err != nil || wf == nil {
			l.logger.Error("get workflow for advance", "submission_id", subID, "error", err)
			continue
		}
		stepDefs := make(map[string]*model.Step)
		for i := range wf.Steps {
			stepDefs[wf.Steps[i].ID] = &wf.Steps[i]
		}

		for _, si := range steps {
			stepDef := stepDefs[si.StepID]
			if stepDef == nil {
				continue
			}

			satisfied, blocked := areStepDependenciesSatisfied(stepDef.DependsOn, stepByStepID)

			if blocked {
				now := time.Now().UTC()
				si.State = model.StepStateSkipped
				si.CompletedAt = &now
				if err := l.store.UpdateStepInstance(ctx, si); err != nil {
					l.logger.Error("skip step", "si_id", si.ID, "error", err)
					continue
				}
				l.logger.Info("step skipped (dependency blocked)", "si_id", si.ID, "step_id", si.StepID)
				affected[subID] = true
			} else if satisfied {
				si.State = model.StepStateReady
				if err := l.store.UpdateStepInstance(ctx, si); err != nil {
					l.logger.Error("ready step", "si_id", si.ID, "error", err)
					continue
				}
				l.logger.Debug("step ready", "si_id", si.ID, "step_id", si.StepID)
				affected[subID] = true
			}
		}
	}

	return nil
}

// areStepDependenciesSatisfied checks whether all upstream step dependencies are met.
func areStepDependenciesSatisfied(dependsOn []string, stepByStepID map[string]*model.StepInstance) (satisfied bool, blocked bool) {
	if len(dependsOn) == 0 {
		return true, false
	}
	for _, depStepID := range dependsOn {
		dep, ok := stepByStepID[depStepID]
		if !ok {
			return false, true
		}
		switch dep.State {
		case model.StepStateFailed, model.StepStateSkipped:
			return false, true
		case model.StepStateCompleted:
			continue
		default:
			return false, false
		}
	}
	return true, false
}

// dispatchReady dispatches READY StepInstances by resolving inputs and creating Tasks.
func (l *Loop) dispatchReady(ctx context.Context, affected map[string]bool) error {
	ready, err := l.store.ListStepsByState(ctx, model.StepStateReady)
	if err != nil {
		return err
	}

	for _, si := range ready {
		sub, err := l.store.GetSubmission(ctx, si.SubmissionID)
		if err != nil || sub == nil {
			l.logger.Error("get submission for dispatch", "si_id", si.ID, "error", err)
			continue
		}
		wf, err := l.store.GetWorkflow(ctx, sub.WorkflowID)
		if err != nil || wf == nil {
			l.logger.Error("get workflow for dispatch", "si_id", si.ID, "error", err)
			continue
		}
		if err := l.dispatchStep(ctx, si, wf, sub); err != nil {
			l.logger.Error("dispatch step", "si_id", si.ID, "step_id", si.StepID, "error", err)
		}
		affected[si.SubmissionID] = true
	}

	return nil
}

// dispatchStep handles dispatching a single READY StepInstance.
// It resolves inputs, evaluates conditions, creates Tasks, and submits to executors.
// After completion, the StepInstance is either DISPATCHED (async), COMPLETED, FAILED, or SKIPPED.
func (l *Loop) dispatchStep(ctx context.Context, si *model.StepInstance, wf *model.Workflow, sub *model.Submission) error {
	// Check token expiry.
	if !sub.TokenExpiry.IsZero() && time.Now().After(sub.TokenExpiry) {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		l.logger.Warn("step failed due to token expiry", "si_id", si.ID, "submission_id", sub.ID)
		return l.store.UpdateStepInstance(ctx, si)
	}

	// Find the step definition.
	step := findStep(wf, si.StepID)
	if step == nil {
		return fmt.Errorf("step %s not found in workflow %s", si.StepID, wf.ID)
	}

	// Build upstream outputs from completed sibling StepInstances.
	allSteps, err := l.store.ListStepsBySubmission(ctx, si.SubmissionID)
	if err != nil {
		return fmt.Errorf("list sibling steps: %w", err)
	}
	stepOutputs := make(map[string]map[string]any)
	for _, s := range allSteps {
		if s.State == model.StepStateCompleted && s.Outputs != nil {
			stepOutputs[s.StepID] = s.Outputs
		}
	}

	// Merge workflow input defaults.
	mergedInputs := MergeWorkflowInputDefaults(wf, sub.Inputs)
	mergedInputs = ResolveWorkflowSecondaryFiles(wf, mergedInputs, "")
	mergedInputs = ResolveWorkflowLoadContents(wf, mergedInputs, "")

	// Evaluate 'when' condition.
	if step.When != "" {
		shouldRun, err := l.evaluateWhenConditionFromSteps(step, mergedInputs, stepOutputs)
		if err != nil {
			l.logger.Warn("when condition evaluation failed", "si_id", si.ID, "error", err)
		} else if !shouldRun {
			now := time.Now().UTC()
			si.State = model.StepStateSkipped
			si.Outputs = make(map[string]any)
			si.CompletedAt = &now
			l.logger.Info("step skipped (when condition false)", "si_id", si.ID, "step_id", si.StepID)
			return l.store.UpdateStepInstance(ctx, si)
		}
	}

	// Create a temporary task to use with populateToolAndJob (which expects a Task).
	// This is used to resolve Tool/Job which are then propagated to real Tasks.
	tmpTask := &model.Task{
		SubmissionID: si.SubmissionID,
		StepID:       si.StepID,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
	}

	// Build tasksByStepID for backward-compat with populateToolAndJob.
	// Use a synthetic task map built from step instance outputs.
	tasksByStep := buildSyntheticTasksByStep(allSteps)

	if err := l.populateToolAndJob(tmpTask, step, wf, mergedInputs, tasksByStep); err != nil {
		l.logger.Warn("failed to populate Tool/Job, falling back to legacy mode",
			"si_id", si.ID, "error", err)
	}

	// Determine executor type.
	execType := l.determineExecutorType(step, sub)

	// Sub-workflow dispatch.
	if isSubWorkflow(tmpTask.Tool) {
		if len(step.Scatter) > 0 {
			return l.dispatchScatterSubWorkflow(ctx, si, tmpTask, step, wf, sub, mergedInputs, tasksByStep, stepOutputs)
		}
		return l.dispatchSubWorkflowStep(ctx, si, tmpTask, step, wf, sub, mergedInputs, tasksByStep, stepOutputs)
	}

	// Scatter dispatch.
	if len(step.Scatter) > 0 {
		return l.dispatchScatterStep(ctx, si, tmpTask, step, wf, sub, mergedInputs, tasksByStep, stepOutputs, execType)
	}

	// ExpressionTool dispatch.
	if isExpressionTool(tmpTask.Tool) {
		outputs, err := l.executeExpressionTool(tmpTask)
		now := time.Now().UTC()
		if err != nil {
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			l.logger.Error("expression tool failed", "si_id", si.ID, "error", err)
		} else {
			si.State = model.StepStateCompleted
			si.Outputs = outputs
			si.CompletedAt = &now
			l.logger.Info("expression tool completed", "si_id", si.ID)
		}
		return l.store.UpdateStepInstance(ctx, si)
	}

	// Normal CommandLineTool dispatch — create a single Task.
	task := l.createTaskFromStep(si, tmpTask, step, sub, execType, -1)

	// Resolve legacy inputs.
	if err := ResolveTaskInputs(task, step, mergedInputs, tasksByStep, nil); err != nil {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.store.UpdateStepInstance(ctx, si)
	}

	// Add user token.
	l.addUserToken(task, sub)

	if err := l.store.CreateTask(ctx, task); err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	// Submit to executor.
	l.submitAndUpdateTask(ctx, task)

	// Update step instance state based on task outcome.
	si.State = model.StepStateDispatched
	if task.State.IsTerminal() {
		now := time.Now().UTC()
		if task.State == model.TaskStateSuccess {
			si.State = model.StepStateCompleted
			si.Outputs = task.Outputs
		} else {
			si.State = model.StepStateFailed
		}
		si.CompletedAt = &now
	}

	return l.store.UpdateStepInstance(ctx, si)
}

// dispatchScatterStep handles scatter dispatch for CommandLineTools and ExpressionTools.
// Creates N Tasks (one per scatter combination) and executes them.
func (l *Loop) dispatchScatterStep(ctx context.Context, si *model.StepInstance, tmpTask *model.Task,
	step *model.Step, wf *model.Workflow, sub *model.Submission,
	mergedInputs map[string]any, tasksByStep map[string]*model.Task,
	stepOutputs map[string]map[string]any, execType model.ExecutorType) error {

	l.logger.Info("dispatching scatter step",
		"si_id", si.ID, "step_id", si.StepID,
		"scatter", step.Scatter, "method", step.ScatterMethod)

	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := tmpTask.Job[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}
		scatterArrays[scatterInput] = arr
	}

	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = scatterDotProduct(tmpTask.Job, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = scatterFlatCrossProduct(tmpTask.Job, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = scatterFlatCrossProduct(tmpTask.Job, step.Scatter, scatterArrays)
	default:
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.store.UpdateStepInstance(ctx, si)
	}

	// Apply valueFrom per-iteration.
	if hasStepValueFrom(step) {
		var expressionLib []string
		if tmpTask.RuntimeHints != nil {
			expressionLib = tmpTask.RuntimeHints.ExpressionLib
		}
		for _, combo := range combinations {
			if err := applyScatterValueFrom(step, combo, mergedInputs, expressionLib); err != nil {
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				return l.store.UpdateStepInstance(ctx, si)
			}
		}
	}

	si.ScatterCount = len(combinations)
	isExprTool := isExpressionTool(tmpTask.Tool)

	// For ExpressionTools, execute inline and collect results.
	if isExprTool {
		var results []map[string]any
		for i, combo := range combinations {
			if step.When != "" {
				shouldRun, err := l.evaluateWhenForScatterIterationFromSteps(step, combo, mergedInputs, stepOutputs)
				if err != nil {
					l.logger.Warn("scatter when eval failed", "si_id", si.ID, "iter", i, "error", err)
				} else if !shouldRun {
					nullOutputs := make(map[string]any)
					for _, outID := range step.Out {
						nullOutputs[outID] = nil
					}
					results = append(results, nullOutputs)
					continue
				}
			}
			iterTask := *tmpTask
			iterTask.Job = combo
			outputs, err := l.executeExpressionTool(&iterTask)
			if err != nil {
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				return l.store.UpdateStepInstance(ctx, si)
			}
			results = append(results, outputs)
		}
		si.Outputs = l.mergeScatterOutputs(results, step, method, scatterArrays)
		now := time.Now().UTC()
		si.State = model.StepStateCompleted
		si.CompletedAt = &now
		l.logger.Info("scatter expression tool completed", "si_id", si.ID, "iterations", len(combinations))
		return l.store.UpdateStepInstance(ctx, si)
	}

	// For CommandLineTools, create and submit individual Tasks.
	allCompleted := true
	var results []map[string]any

	for i, combo := range combinations {
		// Evaluate 'when' per iteration.
		if step.When != "" {
			shouldRun, err := l.evaluateWhenForScatterIterationFromSteps(step, combo, mergedInputs, stepOutputs)
			if err != nil {
				l.logger.Warn("scatter when eval failed", "si_id", si.ID, "iter", i, "error", err)
			} else if !shouldRun {
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				results = append(results, nullOutputs)
				continue
			}
		}

		task := l.createTaskFromStep(si, tmpTask, step, sub, execType, i)
		task.Job = combo

		l.addUserToken(task, sub)

		if err := l.store.CreateTask(ctx, task); err != nil {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}

		l.submitAndUpdateTask(ctx, task)

		if task.State.IsTerminal() {
			if task.State == model.TaskStateSuccess {
				results = append(results, task.Outputs)
			} else {
				// Scatter iteration failed.
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				return l.store.UpdateStepInstance(ctx, si)
			}
		} else {
			allCompleted = false
		}
	}

	if allCompleted {
		si.Outputs = l.mergeScatterOutputs(results, step, method, scatterArrays)
		now := time.Now().UTC()
		si.State = model.StepStateCompleted
		si.CompletedAt = &now
		l.logger.Info("scatter step completed", "si_id", si.ID, "iterations", len(combinations))
	} else {
		si.State = model.StepStateDispatched
	}

	return l.store.UpdateStepInstance(ctx, si)
}

// dispatchSubWorkflowStep handles a non-scatter sub-workflow step.
func (l *Loop) dispatchSubWorkflowStep(ctx context.Context, si *model.StepInstance, tmpTask *model.Task,
	step *model.Step, wf *model.Workflow, sub *model.Submission,
	mergedInputs map[string]any, tasksByStep map[string]*model.Task,
	stepOutputs map[string]map[string]any) error {

	l.logger.Info("dispatching sub-workflow step", "si_id", si.ID, "step_id", si.StepID)

	graphDoc, err := parser.New(l.logger).ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return fmt.Errorf("parse parent CWL: %w", err)
	}

	subGraph := graphDoc.SubWorkflows[step.ToolRef]
	if subGraph == nil {
		return fmt.Errorf("sub-workflow %q not found", step.ToolRef)
	}

	// Create a temporary task for the child submission linkage.
	parentTask := &model.Task{
		ID:           "task_" + uuid.New().String(),
		SubmissionID: si.SubmissionID,
		StepID:       si.StepID,
		StepInstanceID: si.ID,
		Tool:         tmpTask.Tool,
		Job:          tmpTask.Job,
		RuntimeHints: tmpTask.RuntimeHints,
	}

	childSub, err := l.createChildSubmission(ctx, parentTask, subGraph, tmpTask.Job, sub, wf)
	if err != nil {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.store.UpdateStepInstance(ctx, si)
	}

	if err := l.executeChildSubmission(ctx, childSub); err != nil {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.store.UpdateStepInstance(ctx, si)
	}

	now := time.Now().UTC()
	if childSub.State == model.SubmissionStateCompleted {
		si.State = model.StepStateCompleted
		si.Outputs = childSub.Outputs
	} else {
		si.State = model.StepStateFailed
	}
	si.CompletedAt = &now

	return l.store.UpdateStepInstance(ctx, si)
}

// dispatchScatterSubWorkflow handles scatter over a sub-workflow.
func (l *Loop) dispatchScatterSubWorkflow(ctx context.Context, si *model.StepInstance, tmpTask *model.Task,
	step *model.Step, wf *model.Workflow, sub *model.Submission,
	mergedInputs map[string]any, tasksByStep map[string]*model.Task,
	stepOutputs map[string]map[string]any) error {

	l.logger.Info("dispatching scatter sub-workflow", "si_id", si.ID, "step_id", si.StepID)

	graphDoc, err := parser.New(l.logger).ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return fmt.Errorf("parse parent CWL: %w", err)
	}

	subGraph := graphDoc.SubWorkflows[step.ToolRef]
	if subGraph == nil {
		return fmt.Errorf("sub-workflow %q not found", step.ToolRef)
	}

	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := tmpTask.Job[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}
		scatterArrays[scatterInput] = arr
	}

	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = scatterDotProduct(tmpTask.Job, step.Scatter, scatterArrays)
	case "flat_crossproduct", "nested_crossproduct":
		combinations = scatterFlatCrossProduct(tmpTask.Job, step.Scatter, scatterArrays)
	}

	if hasStepValueFrom(step) {
		var expressionLib []string
		if tmpTask.RuntimeHints != nil {
			expressionLib = tmpTask.RuntimeHints.ExpressionLib
		}
		for _, combo := range combinations {
			if err := applyScatterValueFrom(step, combo, mergedInputs, expressionLib); err != nil {
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				return l.store.UpdateStepInstance(ctx, si)
			}
		}
	}

	parentTask := &model.Task{
		ID:           "task_" + uuid.New().String(),
		SubmissionID: si.SubmissionID,
		StepID:       si.StepID,
		StepInstanceID: si.ID,
	}

	var results []map[string]any
	for i, combo := range combinations {
		if step.When != "" {
			shouldRun, err := l.evaluateWhenForScatterIterationFromSteps(step, combo, mergedInputs, stepOutputs)
			if err != nil {
				l.logger.Warn("scatter when eval failed", "si_id", si.ID, "iter", i, "error", err)
			} else if !shouldRun {
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				results = append(results, nullOutputs)
				continue
			}
		}

		childSub, err := l.createChildSubmission(ctx, parentTask, subGraph, combo, sub, wf)
		if err != nil {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}

		if err := l.executeChildSubmission(ctx, childSub); err != nil {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}

		if childSub.State != model.SubmissionStateCompleted {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.store.UpdateStepInstance(ctx, si)
		}

		results = append(results, childSub.Outputs)
	}

	si.Outputs = l.mergeScatterOutputs(results, step, method, scatterArrays)
	now := time.Now().UTC()
	si.State = model.StepStateCompleted
	si.CompletedAt = &now
	l.logger.Info("scatter sub-workflow completed", "si_id", si.ID, "iterations", len(combinations))

	return l.store.UpdateStepInstance(ctx, si)
}

// createTaskFromStep creates a new Task linked to a StepInstance.
func (l *Loop) createTaskFromStep(si *model.StepInstance, tmpTask *model.Task, step *model.Step,
	sub *model.Submission, execType model.ExecutorType, scatterIndex int) *model.Task {

	now := time.Now().UTC()
	task := &model.Task{
		ID:             "task_" + uuid.New().String(),
		SubmissionID:   si.SubmissionID,
		StepID:         si.StepID,
		StepInstanceID: si.ID,
		State:          model.TaskStateQueued,
		ExecutorType:   execType,
		ScatterIndex:   scatterIndex,
		Tool:           tmpTask.Tool,
		Job:            tmpTask.Job,
		RuntimeHints:   tmpTask.RuntimeHints,
		Inputs:         map[string]any{},
		Outputs:        map[string]any{},
		MaxRetries:     l.config.MaxRetries,
		CreatedAt:      now,
	}

	if step.Hints != nil {
		if step.Hints.BVBRCAppID != "" {
			task.BVBRCAppID = step.Hints.BVBRCAppID
		}
	}

	return task
}

// determineExecutorType determines the executor type for a step.
// The server's DefaultExecutor (WHERE to run) takes precedence over CWL hints.
// CWL DockerRequirement (HOW to run) is orthogonal — it's passed to the worker
// via runtime hints so the worker can choose bare vs container execution.
func (l *Loop) determineExecutorType(step *model.Step, sub *model.Submission) model.ExecutorType {
	// Server-wide default executor overrides CWL hints.
	if l.config.DefaultExecutor != "" {
		return model.ExecutorType(l.config.DefaultExecutor)
	}
	// Fall back to step hints from CWL.
	if step.Hints != nil && step.Hints.ExecutorType != "" {
		return step.Hints.ExecutorType
	}
	return model.ExecutorTypeLocal
}

// addUserToken adds user authentication token to task runtime hints.
func (l *Loop) addUserToken(task *model.Task, sub *model.Submission) {
	if sub.UserToken != "" {
		if task.RuntimeHints == nil {
			task.RuntimeHints = &model.RuntimeHints{}
		}
		if task.RuntimeHints.StagerOverrides == nil {
			task.RuntimeHints.StagerOverrides = &model.StagerOverrides{}
		}
		task.RuntimeHints.StagerOverrides.HTTPCredential = &model.HTTPCredential{
			Type:  "bearer",
			Token: sub.UserToken,
		}
	}
}

// submitAndUpdateTask submits a task to its executor and updates its state.
func (l *Loop) submitAndUpdateTask(ctx context.Context, task *model.Task) {
	exec, err := l.registry.Get(task.ExecutorType)
	if err != nil {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = err.Error()
		task.CompletedAt = &now
		l.store.UpdateTask(ctx, task)
		return
	}

	now := time.Now().UTC()
	task.StartedAt = &now
	externalID, submitErr := exec.Submit(ctx, task)
	task.ExternalID = externalID

	if submitErr != nil {
		task.State = model.TaskStateFailed
		task.Stderr = submitErr.Error()
		completedAt := time.Now().UTC()
		task.CompletedAt = &completedAt

		errMsg := submitErr.Error()
		if strings.Contains(errMsg, "signal: killed") ||
			strings.Contains(errMsg, "context deadline exceeded") ||
			strings.Contains(errMsg, "context canceled") {
			task.MaxRetries = task.RetryCount
		}
		l.logger.Info("task failed (submit error)", "task_id", task.ID, "error", submitErr)
	} else {
		newState, statusErr := exec.Status(ctx, task)
		if statusErr != nil {
			l.logger.Error("status check", "task_id", task.ID, "error", statusErr)
			task.State = model.TaskStateQueued
		} else if newState.IsTerminal() {
			task.State = newState
			completedAt := time.Now().UTC()
			task.CompletedAt = &completedAt
			stdout, stderr, _ := exec.Logs(ctx, task)
			task.Stdout = stdout
			task.Stderr = stderr
			l.logger.Info("task completed", "task_id", task.ID, "state", newState, "step_id", task.StepID)
		} else {
			task.State = model.TaskStateQueued
			l.logger.Debug("task queued", "task_id", task.ID, "step_id", task.StepID)
		}
	}

	l.store.UpdateTask(ctx, task)
}

// mergeScatterOutputs merges scatter results into arrays with proper nesting.
func (l *Loop) mergeScatterOutputs(results []map[string]any, step *model.Step,
	method string, scatterArrays map[string][]any) map[string]any {

	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		dims := make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			dims[i] = len(scatterArrays[name])
		}
		return mergeScatterResultsNested(results, step.Out, dims)
	}
	return mergeScatterResults(results, step.Out)
}

// buildSyntheticTasksByStep creates a fake tasksByStepID map from StepInstance outputs,
// for backward compatibility with populateToolAndJob.
func buildSyntheticTasksByStep(steps []*model.StepInstance) map[string]*model.Task {
	m := make(map[string]*model.Task)
	for _, si := range steps {
		if si.State == model.StepStateCompleted && si.Outputs != nil {
			m[si.StepID] = &model.Task{
				StepID:  si.StepID,
				State:   model.TaskStateSuccess,
				Outputs: si.Outputs,
			}
		}
	}
	return m
}

// evaluateWhenConditionFromSteps evaluates 'when' using step instance outputs.
func (l *Loop) evaluateWhenConditionFromSteps(step *model.Step, submissionInputs map[string]any, stepOutputs map[string]map[string]any) (bool, error) {
	if step.When == "" {
		return true, nil
	}

	inputs := make(map[string]any)
	for k, v := range submissionInputs {
		inputs[k] = v
	}

	for _, si := range step.In {
		if si.Source == "" {
			continue
		}
		if strings.Contains(si.Source, "/") {
			parts := strings.SplitN(si.Source, "/", 2)
			stepID, outputID := parts[0], parts[1]
			if outputs, ok := stepOutputs[stepID]; ok {
				if val, ok := outputs[outputID]; ok {
					inputs[si.ID] = val
				}
			}
		} else {
			if val, ok := submissionInputs[si.Source]; ok {
				inputs[si.ID] = val
			}
		}
	}

	evaluator := cwlexpr.NewEvaluator(nil)
	ctx := cwlexpr.NewContext(inputs)
	return evaluator.EvaluateBool(step.When, ctx)
}

// evaluateWhenForScatterIterationFromSteps evaluates 'when' for a scatter iteration using step outputs.
func (l *Loop) evaluateWhenForScatterIterationFromSteps(step *model.Step, iterInputs map[string]any,
	submissionInputs map[string]any, stepOutputs map[string]map[string]any) (bool, error) {

	inputs := make(map[string]any)
	for k, v := range submissionInputs {
		inputs[k] = v
	}
	for k, v := range iterInputs {
		inputs[k] = v
	}

	evaluator := cwlexpr.NewEvaluator(nil)
	evalCtx := cwlexpr.NewContext(inputs)
	return evaluator.EvaluateBool(step.When, evalCtx)
}

// resubmitRetrying re-submits RETRYING tasks to their executor.
func (l *Loop) resubmitRetrying(ctx context.Context, affected map[string]bool) error {
	retrying, err := l.store.GetTasksByState(ctx, model.TaskStateRetrying)
	if err != nil {
		return err
	}

	for _, task := range retrying {
		task.RetryCount++
		task.ExitCode = nil
		task.Stdout = ""
		task.Stderr = ""
		task.CompletedAt = nil
		task.StartedAt = nil

		l.logger.Info("retrying task", "task_id", task.ID, "attempt", task.RetryCount)
		l.submitAndUpdateTask(ctx, task)
		affected[task.SubmissionID] = true
	}

	return nil
}

// pollInFlight checks QUEUED and RUNNING tasks for status updates (for async executors).
func (l *Loop) pollInFlight(ctx context.Context, affected map[string]bool) error {
	for _, state := range []model.TaskState{model.TaskStateQueued, model.TaskStateRunning} {
		tasks, err := l.store.GetTasksByState(ctx, state)
		if err != nil {
			return err
		}

		for _, task := range tasks {
			exec, err := l.registry.Get(task.ExecutorType)
			if err != nil {
				l.logger.Error("get executor for poll", "task_id", task.ID, "error", err)
				continue
			}

			newState, err := exec.Status(ctx, task)
			if err != nil {
				l.logger.Error("poll status", "task_id", task.ID, "error", err)
				continue
			}

			if newState == task.State {
				continue
			}

			task.State = newState
			if newState == model.TaskStateRunning && task.StartedAt == nil {
				now := time.Now().UTC()
				task.StartedAt = &now
			}
			if newState.IsTerminal() {
				now := time.Now().UTC()
				task.CompletedAt = &now
				stdout, stderr, _ := exec.Logs(ctx, task)
				task.Stdout = stdout
				task.Stderr = stderr
				l.logger.Info("task completed (poll)", "task_id", task.ID, "state", newState)
			}

			if err := l.store.UpdateTask(ctx, task); err != nil {
				l.logger.Error("update polled task", "task_id", task.ID, "error", err)
				continue
			}
			affected[task.SubmissionID] = true
		}
	}

	return nil
}

// advanceSteps checks DISPATCHED/RUNNING StepInstances and completes them
// when all their Tasks are terminal.
func (l *Loop) advanceSteps(ctx context.Context, affected map[string]bool) error {
	for _, state := range []model.StepInstanceState{model.StepStateDispatched, model.StepStateRunning} {
		steps, err := l.store.ListStepsByState(ctx, state)
		if err != nil {
			return err
		}

		for _, si := range steps {
			tasks, err := l.store.ListTasksByStepInstance(ctx, si.ID)
			if err != nil {
				l.logger.Error("list tasks for step", "si_id", si.ID, "error", err)
				continue
			}

			if len(tasks) == 0 {
				continue
			}

			allTerminal := true
			anyFailed := false
			anyRunning := false
			for _, t := range tasks {
				if !t.State.IsTerminal() {
					allTerminal = false
					if t.State == model.TaskStateRunning {
						anyRunning = true
					}
				}
				if t.State == model.TaskStateFailed {
					anyFailed = true
				}
			}

			if !allTerminal {
				// Update to RUNNING if any task is running.
				if anyRunning && si.State == model.StepStateDispatched {
					si.State = model.StepStateRunning
					if err := l.store.UpdateStepInstance(ctx, si); err != nil {
						l.logger.Error("update step running", "si_id", si.ID, "error", err)
					}
					affected[si.SubmissionID] = true
				}
				continue
			}

			// All tasks terminal — merge outputs and complete.
			now := time.Now().UTC()
			if anyFailed {
				si.State = model.StepStateFailed
			} else {
				si.State = model.StepStateCompleted

				// Merge outputs from tasks, ordered by ScatterIndex.
				if si.ScatterCount > 0 {
					// Scatter step: merge by scatter index.
					// Load step definition for output IDs.
					sub, _ := l.store.GetSubmission(ctx, si.SubmissionID)
					if sub != nil {
						wf, _ := l.store.GetWorkflow(ctx, sub.WorkflowID)
						if wf != nil {
							step := findStep(wf, si.StepID)
							if step != nil {
								results := make([]map[string]any, len(tasks))
								for _, t := range tasks {
									if t.ScatterIndex >= 0 && t.ScatterIndex < len(results) {
										results[t.ScatterIndex] = t.Outputs
									}
								}
								si.Outputs = mergeScatterResults(results, step.Out)
							}
						}
					}
				} else if len(tasks) == 1 {
					si.Outputs = tasks[0].Outputs
				}
			}
			si.CompletedAt = &now

			if err := l.store.UpdateStepInstance(ctx, si); err != nil {
				l.logger.Error("complete step", "si_id", si.ID, "error", err)
				continue
			}
			l.logger.Info("step completed", "si_id", si.ID, "step_id", si.StepID, "state", si.State)
			affected[si.SubmissionID] = true
		}
	}

	return nil
}

// finalizeSubmissions updates submission state based on StepInstance states.
func (l *Loop) finalizeSubmissions(ctx context.Context, affected map[string]bool) error {
	// Check RUNNING and PENDING submissions.
	for _, state := range []string{"RUNNING", "PENDING"} {
		subs, _, err := l.store.ListSubmissions(ctx, model.ListOptions{State: state, Limit: 100})
		if err != nil {
			l.logger.Error("list submissions for finalize", "state", state, "error", err)
			continue
		}
		for _, sub := range subs {
			affected[sub.ID] = true
		}
	}

	for subID := range affected {
		sub, err := l.store.GetSubmission(ctx, subID)
		if err != nil {
			l.logger.Error("get submission for finalize", "submission_id", subID, "error", err)
			continue
		}
		if sub == nil || sub.State.IsTerminal() {
			continue
		}

		// Load step instances instead of tasks.
		steps, err := l.store.ListStepsBySubmission(ctx, subID)
		if err != nil {
			l.logger.Error("list steps for finalize", "submission_id", subID, "error", err)
			continue
		}

		allTerminal := true
		anyFailed := false
		anyActive := false

		for _, si := range steps {
			if !si.State.IsTerminal() {
				allTerminal = false
				if si.State != model.StepStateWaiting {
					anyActive = true
				}
			}
			if si.State == model.StepStateFailed {
				anyFailed = true
			}
		}

		if allTerminal {
			if anyFailed {
				sub.State = model.SubmissionStateFailed
			} else {
				sub.State = model.SubmissionStateCompleted

				// Collect workflow outputs from step instance outputs.
				wf, wfErr := l.store.GetWorkflow(ctx, sub.WorkflowID)
				if wfErr != nil {
					l.logger.Error("get workflow for output collection", "submission_id", subID, "error", wfErr)
				} else if wf != nil {
					stepOutputs := make(map[string]map[string]any)
					for _, si := range steps {
						if si.Outputs != nil {
							stepOutputs[si.StepID] = si.Outputs
						}
					}
					outputs, outErr := l.collectWorkflowOutputsFromSteps(wf, stepOutputs, sub.Inputs)
					if outErr != nil {
						l.logger.Error("collect workflow outputs", "submission_id", subID, "error", outErr)
						sub.State = model.SubmissionStateFailed
					} else {
						sub.Outputs = outputs
						l.logger.Debug("collected workflow outputs", "submission_id", subID, "outputs", len(outputs))
					}
				}
			}
			now := time.Now().UTC()
			sub.CompletedAt = &now
			if err := l.store.UpdateSubmission(ctx, sub); err != nil {
				l.logger.Error("finalize submission", "submission_id", subID, "error", err)
			} else {
				l.logger.Info("submission finalized", "submission_id", subID, "state", sub.State)
			}
		} else if (anyActive || anyFailed) && sub.State == model.SubmissionStatePending {
			sub.State = model.SubmissionStateRunning
			if err := l.store.UpdateSubmission(ctx, sub); err != nil {
				l.logger.Error("activate submission", "submission_id", subID, "error", err)
			} else {
				l.logger.Info("submission running", "submission_id", subID)
			}
		}
	}

	return nil
}

// markRetries transitions FAILED tasks with remaining retries to RETRYING.
func (l *Loop) markRetries(ctx context.Context, affected map[string]bool) error {
	failed, err := l.store.GetTasksByState(ctx, model.TaskStateFailed)
	if err != nil {
		return err
	}

	for _, task := range failed {
		if task.RetryCount >= task.MaxRetries {
			continue
		}
		task.State = model.TaskStateRetrying
		if err := l.store.UpdateTask(ctx, task); err != nil {
			l.logger.Error("mark retrying", "task_id", task.ID, "error", err)
			continue
		}
		l.logger.Info("task marked for retry", "task_id", task.ID, "retry_count", task.RetryCount, "max_retries", task.MaxRetries)
		affected[task.SubmissionID] = true
	}

	return nil
}

// collectWorkflowOutputsFromSteps gathers workflow outputs from step instance outputs.
func (l *Loop) collectWorkflowOutputsFromSteps(wf *model.Workflow, stepOutputs map[string]map[string]any, submissionInputs map[string]any) (map[string]any, error) {
	mergedInputs := MergeWorkflowInputDefaults(wf, submissionInputs)
	return cwloutput.CollectWorkflowOutputs(wf.Outputs, mergedInputs, stepOutputs)
}

// findStep returns the Step with the given ID from the workflow, or nil.
func findStep(wf *model.Workflow, stepID string) *model.Step {
	for i := range wf.Steps {
		if wf.Steps[i].ID == stepID {
			return &wf.Steps[i]
		}
	}
	return nil
}

// populateToolAndJob extracts the full CWL tool definition from the workflow's
// RawCWL and resolves inputs for the task. This enables workers to build the
// full command line with inputBindings, requirements, etc.
func (l *Loop) populateToolAndJob(task *model.Task, step *model.Step, wf *model.Workflow, submissionInputs map[string]any, tasksByStepID map[string]*model.Task) error {
	if wf.RawCWL == "" {
		return fmt.Errorf("workflow has no RawCWL")
	}

	// Use the parser to get proper CWL objects with inputBindings, etc.
	p := parser.New(l.logger)
	graphDoc, err := p.ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return fmt.Errorf("parse RawCWL: %w", err)
	}

	// Find the tool for this step.
	// ToolRef already has the # prefix stripped by the parser.
	// Use it directly as the lookup key - the parser stores tools with consistent IDs.
	var toolID string
	if step.ToolRef != "" {
		toolID = step.ToolRef
	} else if step.ToolInline != nil {
		toolID = step.ToolInline.ID
	}

	// Look up tool in the parsed graph.
	var tool map[string]any
	var runtimeHints *model.RuntimeHints

	// Check CommandLineTools
	for id, clt := range graphDoc.Tools {
		normalizedID := strings.TrimPrefix(id, "#")
		if normalizedID == toolID || id == toolID {
			// Convert cwl.CommandLineTool to map[string]any via JSON.
			data, err := json.Marshal(clt)
			if err != nil {
				return fmt.Errorf("marshal tool: %w", err)
			}
			if err := json.Unmarshal(data, &tool); err != nil {
				return fmt.Errorf("unmarshal tool: %w", err)
			}
			runtimeHints = extractRuntimeHintsFromCWLTool(clt)
			break
		}
	}

	// Check ExpressionTools if not found
	if tool == nil {
		for id, et := range graphDoc.ExpressionTools {
			normalizedID := strings.TrimPrefix(id, "#")
			if normalizedID == toolID || id == toolID {
				data, err := json.Marshal(et)
				if err != nil {
					return fmt.Errorf("marshal expression tool: %w", err)
				}
				if err := json.Unmarshal(data, &tool); err != nil {
					return fmt.Errorf("unmarshal expression tool: %w", err)
				}
				break
			}
		}
	}

	// Check SubWorkflows if not found
	if tool == nil {
		for id := range graphDoc.SubWorkflows {
			normalizedID := strings.TrimPrefix(id, "#")
			if normalizedID == toolID || id == toolID {
				// Mark as sub-workflow so submitTask can detect it.
				tool = subWorkflowMarker(normalizedID)
				break
			}
		}
	}

	if tool == nil {
		return fmt.Errorf("tool %q not found in parsed workflow", toolID)
	}

	// Build stepOutputs from completed upstream tasks.
	stepOutputs := make(map[string]map[string]any)
	for stepID, t := range tasksByStepID {
		if t.State == model.TaskStateSuccess && t.Outputs != nil {
			stepOutputs[stepID] = t.Outputs
		}
	}

	// Convert model.StepInput to stepinput.InputDef and resolve using shared logic.
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

	// Use shared resolution logic (handles defaults, multiple sources, valueFrom).
	// For scatter steps, skip ALL valueFrom — it must be applied per-iteration
	// AFTER scatter splits the array (CWL v1.2 spec). Non-scattered inputs with
	// valueFrom may reference scattered variables, so they need per-iteration eval too.
	opts := stepinput.Options{}
	if len(step.Scatter) > 0 {
		opts.SkipAllValueFrom = true
	}
	job, err := stepinput.ResolveInputs(inputs, submissionInputs, stepOutputs, opts)
	if err != nil {
		return fmt.Errorf("resolve job inputs: %w", err)
	}

	// Merge workflow-level and step-level requirements into the tool.
	// CWL spec priority: tool requirements > step requirements > workflow requirements.
	mergeRequirementsIntoTool(tool, graphDoc.Workflow, step.ID)

	task.Tool = tool
	task.Job = job
	task.RuntimeHints = runtimeHints

	// Refresh runtime hints after requirement merge (picks up inherited Docker, etc.).
	if merged := extractRuntimeHints(tool); merged != nil {
		if runtimeHints != nil {
			// Preserve existing hints, overlay merged ones.
			if merged.DockerImage != "" {
				runtimeHints.DockerImage = merged.DockerImage
			}
			if len(merged.ExpressionLib) > 0 {
				runtimeHints.ExpressionLib = merged.ExpressionLib
			}
		} else {
			runtimeHints = merged
		}
		task.RuntimeHints = runtimeHints
	}

	// Add namespaces from the graph document for format resolution.
	if len(graphDoc.Namespaces) > 0 {
		if task.RuntimeHints == nil {
			task.RuntimeHints = &model.RuntimeHints{}
		}
		task.RuntimeHints.Namespaces = graphDoc.Namespaces
	}

	return nil
}

// extractRuntimeHints extracts expression library and other hints from a tool (map form).
func extractRuntimeHints(tool map[string]any) *model.RuntimeHints {
	hints := &model.RuntimeHints{}

	// Check requirements for InlineJavascriptRequirement.
	if reqs, ok := tool["requirements"].(map[string]any); ok {
		if ijsReq, ok := reqs["InlineJavascriptRequirement"].(map[string]any); ok {
			if lib, ok := ijsReq["expressionLib"].([]any); ok {
				for _, item := range lib {
					if s, ok := item.(string); ok {
						hints.ExpressionLib = append(hints.ExpressionLib, s)
					}
				}
			}
		}

		// Extract DockerRequirement.
		if dockerReq, ok := reqs["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dockerReq["dockerPull"].(string); ok {
				hints.DockerImage = pull
			}
		}

		// Extract ResourceRequirement.
		if resReq, ok := reqs["ResourceRequirement"].(map[string]any); ok {
			if cores, ok := resReq["coresMin"]; ok {
				switch v := cores.(type) {
				case int:
					hints.Cores = v
				case float64:
					hints.Cores = int(v)
				case string:
					// Expression — leave as 0 (evaluated at execution time)
				}
			}
			if ram, ok := resReq["ramMin"]; ok {
				switch v := ram.(type) {
				case int:
					hints.RamMB = int64(v)
				case int64:
					hints.RamMB = v
				case float64:
					hints.RamMB = int64(v)
				case string:
					// Expression — leave as 0 (evaluated at execution time)
				}
			}
		}
	}

	// Also check hints section.
	if toolHints, ok := tool["hints"].(map[string]any); ok {
		if dockerReq, ok := toolHints["DockerRequirement"].(map[string]any); ok {
			if hints.DockerImage == "" {
				if pull, ok := dockerReq["dockerPull"].(string); ok {
					hints.DockerImage = pull
				}
			}
		}
	}

	return hints
}

// extractRuntimeHintsFromCWLTool extracts runtime hints from a parsed CWL CommandLineTool.
func extractRuntimeHintsFromCWLTool(tool *cwl.CommandLineTool) *model.RuntimeHints {
	hints := &model.RuntimeHints{}

	// Check requirements map.
	if tool.Requirements != nil {
		// InlineJavascriptRequirement
		if ijsReq, ok := tool.Requirements["InlineJavascriptRequirement"].(map[string]any); ok {
			if lib, ok := ijsReq["expressionLib"].([]any); ok {
				for _, item := range lib {
					if s, ok := item.(string); ok {
						hints.ExpressionLib = append(hints.ExpressionLib, s)
					}
				}
			}
		}

		// DockerRequirement
		if dockerReq, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dockerReq["dockerPull"].(string); ok {
				hints.DockerImage = pull
			}
		}

		// ResourceRequirement
		if resReq, ok := tool.Requirements["ResourceRequirement"].(map[string]any); ok {
			if cores, ok := resReq["coresMin"]; ok {
				switch v := cores.(type) {
				case int:
					hints.Cores = v
				case float64:
					hints.Cores = int(v)
				case string:
					// Expression — leave as 0 (evaluated at execution time)
				}
			}
			if ram, ok := resReq["ramMin"]; ok {
				switch v := ram.(type) {
				case int:
					hints.RamMB = int64(v)
				case float64:
					hints.RamMB = int64(v)
				case string:
					// Expression — leave as 0 (evaluated at execution time)
				}
			}
		}
	}

	// Check hints map.
	if tool.Hints != nil {
		if dockerReq, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if hints.DockerImage == "" {
				if pull, ok := dockerReq["dockerPull"].(string); ok {
					hints.DockerImage = pull
				}
			}
		}
	}

	return hints
}

// isExpressionTool checks if a tool map represents a CWL ExpressionTool.
func isExpressionTool(tool map[string]any) bool {
	if tool == nil {
		return false
	}
	class, ok := tool["class"].(string)
	return ok && class == "ExpressionTool"
}

// executeExpressionTool executes an ExpressionTool directly in the scheduler.
// ExpressionTools evaluate JavaScript expressions and don't need external execution.
func (l *Loop) executeExpressionTool(task *model.Task) (map[string]any, error) {
	// Convert task.Tool map back to cwl.ExpressionTool.
	data, err := json.Marshal(task.Tool)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}
	var tool cwl.ExpressionTool
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("unmarshal expression tool: %w", err)
	}

	// Validate inputs before execution.
	if err := validate.ExpressionToolInputs(&tool, task.Job); err != nil {
		return nil, err
	}

	// Get expression library from RuntimeHints.
	var expressionLib []string
	var cwlDir string
	if task.RuntimeHints != nil {
		expressionLib = task.RuntimeHints.ExpressionLib
		cwlDir = task.RuntimeHints.CWLDir
	}

	// Also extract from the tool itself if not in RuntimeHints.
	if len(expressionLib) == 0 {
		expressionLib = extractExpressionLibFromTool(task.Tool)
	}

	// Execute using the shared exprtool package.
	return exprtool.Execute(&tool, task.Job, exprtool.ExecuteOptions{
		ExpressionLib: expressionLib,
		CWLDir:        cwlDir,
	})
}

// extractExpressionLibFromTool extracts expressionLib from a tool's requirements.
func extractExpressionLibFromTool(tool map[string]any) []string {
	if tool == nil {
		return nil
	}
	reqs, ok := tool["requirements"].(map[string]any)
	if !ok {
		return nil
	}
	ijsReq, ok := reqs["InlineJavascriptRequirement"].(map[string]any)
	if !ok {
		return nil
	}
	lib, ok := ijsReq["expressionLib"].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range lib {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// mergeRequirementsIntoTool merges workflow-level and step-level requirements
// into a tool map. CWL spec priority:
//
//	tool requirements > step requirements > workflow requirements > tool hints > step hints > workflow hints
func mergeRequirementsIntoTool(tool map[string]any, wf *cwl.Workflow, stepID string) {
	if wf == nil || tool == nil {
		return
	}

	// Get or create tool requirements map.
	toolReqs, _ := tool["requirements"].(map[string]any)
	if toolReqs == nil {
		toolReqs = make(map[string]any)
	}

	toolHints, _ := tool["hints"].(map[string]any)
	if toolHints == nil {
		toolHints = make(map[string]any)
	}

	// Look up the cwl.Step for step-level requirements.
	var cwlStep *cwl.Step
	if s, ok := wf.Steps[stepID]; ok {
		cwlStep = &s
	}

	// Merge step requirements (higher priority than workflow, lower than tool).
	if cwlStep != nil && cwlStep.Requirements != nil {
		for key, val := range cwlStep.Requirements {
			if _, exists := toolReqs[key]; !exists {
				toolReqs[key] = val
			}
		}
	}

	// Merge workflow requirements (lowest priority among requirements).
	if wf.Requirements != nil {
		for key, val := range wf.Requirements {
			if _, exists := toolReqs[key]; !exists {
				toolReqs[key] = val
			}
		}
	}

	// Merge step hints.
	if cwlStep != nil && cwlStep.Hints != nil {
		for key, val := range cwlStep.Hints {
			if _, exists := toolReqs[key]; !exists {
				if _, exists := toolHints[key]; !exists {
					toolHints[key] = val
				}
			}
		}
	}

	// Merge workflow hints.
	if wf.Hints != nil {
		for key, val := range wf.Hints {
			if _, exists := toolReqs[key]; !exists {
				if _, exists := toolHints[key]; !exists {
					toolHints[key] = val
				}
			}
		}
	}

	if len(toolReqs) > 0 {
		tool["requirements"] = toolReqs
	}
	if len(toolHints) > 0 {
		tool["hints"] = toolHints
	}
}
