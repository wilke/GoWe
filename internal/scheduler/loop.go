package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/cwloutput"
	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/exprtool"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/stepinput"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Config holds scheduler configuration.
type Config struct {
	PollInterval     time.Duration
	MaxRetries       int    // Default max retries for tasks (0 = no retries).
	DefaultExecutor  string // Server-wide default executor (overrides CWL hints). Empty = hint-based.
	WorkspaceStaging string // "server" = pre/post-stage ws:// on scheduler, "" = passthrough to workers.

	// PreflightDeferralTicks is the number of ticks to defer a worker task dispatch
	// when no online worker can satisfy its requirements. After this many ticks,
	// the step is failed with a descriptive error. 0 = disable pre-flight check.
	PreflightDeferralTicks int

	// StuckTaskThreshold is the number of consecutive ticks with zero progress
	// before a class of QUEUED worker tasks is considered stuck. 0 = disable.
	StuckTaskThreshold int

	// StuckTaskAction is the action to take when stuck tasks are detected:
	// "warn" (default) logs an error, "fail" also fails the oldest task.
	StuckTaskAction string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:           2 * time.Second,
		MaxRetries:             3,
		PreflightDeferralTicks: 30,
		StuckTaskThreshold:     30,
		StuckTaskAction:        "warn",
	}
}

// WorkerCapabilities summarizes what online workers can do. Built once per tick.
type WorkerCapabilities struct {
	OnlineCount  int
	HasContainer bool              // any worker with docker/apptainer
	Groups       map[string]int    // group → count of online workers
	Datasets     map[string]int    // dataset ID → count of online workers
	Workers      []*model.Worker   // full list of online workers
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

	// Cached per-tick: structured worker capability snapshot.
	cachedWorkerCaps *WorkerCapabilities

	// deferredSteps tracks how many ticks each step has been deferred due to
	// pre-flight check failure (no capable worker). Key = stepInstanceID.
	deferredSteps map[string]int

	// stuckTracker detects classes of QUEUED tasks making zero progress.
	stuck stuckTracker

	// wsStager handles server-side workspace pre/post-staging (nil if disabled).
	wsStager wsStagerInterface

	// unsupportedSteps tracks step instances that failed due to unsupported
	// CWL requirements (e.g., InplaceUpdateRequirement). Key = stepInstanceID,
	// value = human-readable reason. Used by buildSubmissionError to set the
	// UNSUPPORTED_REQUIREMENT error code so the CLI can exit with code 33.
	unsupportedSteps map[string]string

	// cache provides per-tick memoization for frequently-read DB entities
	// (submissions, workflows, step instances). Reset at the start of each Tick().
	cache *tickCache
}

// taskRequirementKey groups QUEUED tasks by their scheduling requirements
// so stuck detection can identify WHICH class of tasks is stuck.
type taskRequirementKey struct {
	WorkerGroup string
	PrestageIDs string // sorted, comma-joined
}

// stuckTracker tracks per-requirement-key progress of QUEUED tasks.
type stuckTracker struct {
	lastCounts map[taskRequirementKey]int
	staleTicks map[taskRequirementKey]int
}

// NewLoop creates a new scheduler loop.
func NewLoop(st store.Store, reg *executor.Registry, cfg Config, logger *slog.Logger) *Loop {
	return &Loop{
		store:            st,
		registry:         reg,
		config:           cfg,
		logger:           logger.With("component", "scheduler"),
		stopCh:           make(chan struct{}),
		doneCh:           make(chan struct{}),
		deferredSteps:    make(map[string]int),
		unsupportedSteps: make(map[string]string),
		stuck: stuckTracker{
			lastCounts: make(map[taskRequirementKey]int),
			staleTicks: make(map[taskRequirementKey]int),
		},
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

// updateStepInstance persists a step instance update and invalidates the tick cache.
func (l *Loop) updateStepInstance(ctx context.Context, si *model.StepInstance) error {
	err := l.store.UpdateStepInstance(ctx, si)
	if err == nil && l.cache != nil {
		l.cache.invalidateSteps(si.SubmissionID)
	}
	return err
}

// updateSubmission persists a submission update and invalidates the tick cache.
func (l *Loop) updateSubmission(ctx context.Context, sub *model.Submission) error {
	err := l.store.UpdateSubmission(ctx, sub)
	if err == nil && l.cache != nil {
		l.cache.invalidateSubmission(sub.ID)
	}
	return err
}

// Tick runs a single scheduling iteration using the 3-level state architecture:
// Submissions → StepInstances → Tasks.
func (l *Loop) Tick(ctx context.Context) error {
	l.cachedWorkerCaps = nil // Reset per-tick worker capability cache.
	l.cache = newTickCache() // Reset per-tick entity cache.
	affected := make(map[string]bool) // submissionIDs touched this tick

	// Phase 1: Advance WAITING StepInstances to READY when all dependencies are met.
	if err := l.advanceWaiting(ctx, affected); err != nil {
		return fmt.Errorf("phase 1 (waiting): %w", err)
	}

	// Phase 1.5: Pre-stage workspace inputs for PENDING submissions (server-side mode).
	if l.wsStager != nil {
		if err := l.prestageWorkspaceInputs(ctx, affected); err != nil {
			return fmt.Errorf("phase 1.5 (pre-stage): %w", err)
		}
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

	// Phase 3.5: Detect stuck QUEUED worker tasks (progress-based).
	if l.config.StuckTaskThreshold > 0 {
		if err := l.detectStuckTasks(ctx, affected); err != nil {
			l.logger.Error("phase 3.5 (stuck detection)", "error", err)
		}
	}

	// Phase 4: Advance DISPATCHED/RUNNING StepInstances when all their Tasks are terminal.
	if err := l.advanceSteps(ctx, affected); err != nil {
		return fmt.Errorf("phase 4 (advance steps): %w", err)
	}

	// Phase 5: Finalize submissions where all StepInstances are terminal.
	if err := l.finalizeSubmissions(ctx, affected); err != nil {
		return fmt.Errorf("phase 5 (finalize): %w", err)
	}

	// Phase 5.5: Upload outputs to workspace for completed submissions (server-side mode).
	if l.wsStager != nil {
		if err := l.poststageWorkspaceOutputs(ctx, affected); err != nil {
			return fmt.Errorf("phase 5.5 (post-stage): %w", err)
		}
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
		allSteps, err := l.cache.listStepsBySubmission(ctx, l.store, subID)
		if err != nil {
			l.logger.Error("list steps for submission", "submission_id", subID, "error", err)
			continue
		}
		stepByStepID := make(map[string]*model.StepInstance)
		for _, s := range allSteps {
			stepByStepID[s.StepID] = s
		}

		// Load workflow to get step dependencies.
		sub, err := l.cache.getSubmission(ctx, l.store, subID)
		if err != nil || sub == nil {
			l.logger.Error("get submission for advance", "submission_id", subID, "error", err)
			continue
		}
		wf, err := l.cache.getWorkflow(ctx, l.store, sub.WorkflowID)
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
				if err := l.updateStepInstance(ctx, si); err != nil {
					l.logger.Error("skip step", "si_id", si.ID, "error", err)
					continue
				}
				l.logger.Info("step skipped (dependency blocked)", "si_id", si.ID, "step_id", si.StepID)
				affected[subID] = true
			} else if satisfied {
				si.State = model.StepStateReady
				if err := l.updateStepInstance(ctx, si); err != nil {
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
		sub, err := l.cache.getSubmission(ctx, l.store, si.SubmissionID)
		if err != nil || sub == nil {
			l.logger.Error("get submission for dispatch", "si_id", si.ID, "error", err)
			continue
		}
		wf, err := l.cache.getWorkflow(ctx, l.store, sub.WorkflowID)
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
		return l.updateStepInstance(ctx, si)
	}

	// Find the step definition.
	step := findStep(wf, si.StepID)
	if step == nil {
		return fmt.Errorf("step %s not found in workflow %s", si.StepID, wf.ID)
	}

	// Build upstream outputs from completed sibling StepInstances.
	allSteps, err := l.cache.listStepsBySubmission(ctx, l.store, si.SubmissionID)
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

	// Evaluate 'when' condition for non-scatter steps.
	// For scatter steps, the 'when' condition is evaluated per-iteration inside
	// dispatchScatterStep, not at the step level.
	if step.When != "" && len(step.Scatter) == 0 {
		shouldRun, err := l.evaluateWhenConditionFromSteps(step, mergedInputs, stepOutputs)
		if err != nil {
			// CWL spec: non-boolean 'when' expressions must fail the step.
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			l.logger.Error("when condition evaluation failed", "si_id", si.ID, "error", err)
			return l.updateStepInstance(ctx, si)
		} else if !shouldRun {
			now := time.Now().UTC()
			si.State = model.StepStateSkipped
			si.Outputs = make(map[string]any)
			si.CompletedAt = &now
			l.logger.Info("step skipped (when condition false)", "si_id", si.ID, "step_id", si.StepID)
			return l.updateStepInstance(ctx, si)
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

	// Pre-flight check: if executor is "worker", verify a capable worker exists.
	if execType == model.ExecutorTypeWorker && l.config.PreflightDeferralTicks > 0 {
		caps := l.workerCapabilities()
		canMatch, reason := canMatchTask(caps, tmpTask.RuntimeHints)
		if !canMatch {
			l.deferredSteps[si.ID]++
			count := l.deferredSteps[si.ID]
			if count >= l.config.PreflightDeferralTicks {
				delete(l.deferredSteps, si.ID)
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				l.logger.Error("step failed: no capable worker",
					"si_id", si.ID, "step_id", si.StepID, "reason", reason,
					"deferred_ticks", count)
				return l.updateStepInstance(ctx, si)
			}
			if count == 1 {
				l.logger.Warn("deferring step dispatch: no capable worker",
					"si_id", si.ID, "step_id", si.StepID, "reason", reason)
			} else {
				l.logger.Debug("deferring step dispatch: no capable worker",
					"si_id", si.ID, "step_id", si.StepID, "reason", reason,
					"deferred_ticks", count)
			}
			return nil // Leave step in READY for next tick.
		}
		// Worker can match — clear any prior deferral.
		delete(l.deferredSteps, si.ID)
	}

	// InplaceUpdateRequirement requires in-process filesystem sharing between
	// workflow steps. Server mode (both local and worker executors) stages outputs
	// through the store, breaking the in-place mutation contract. Reject with a
	// clear unsupported signal so cwltest classifies this as "unsupported" (exit 33).
	if hasInplaceUpdateReq(tmpTask.Tool) {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		reason := "InplaceUpdateRequirement is not supported in server execution mode"
		l.unsupportedSteps[si.ID] = reason
		l.logger.Warn("unsupported requirement", "requirement", "InplaceUpdateRequirement", "si_id", si.ID)
		return l.updateStepInstance(ctx, si)
	}

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
			// Materialize file/directory literals in expression tool outputs.
			tmpDir, mkErr := os.MkdirTemp("", "exprtool-"+si.ID+"-")
			if mkErr == nil {
				if materialized, matErr := fileliteral.MaterializeOutputs(outputs, tmpDir); matErr == nil {
					outputs = materialized
				} else {
					l.logger.Warn("failed to materialize expression tool outputs", "si_id", si.ID, "error", matErr)
				}
			}
			si.State = model.StepStateCompleted
			si.Outputs = outputs
			si.CompletedAt = &now
			l.logger.Info("expression tool completed", "si_id", si.ID)
		}
		return l.updateStepInstance(ctx, si)
	}

	// Normal CommandLineTool dispatch — create a single Task.
	task := l.createTaskFromStep(si, tmpTask, step, sub, execType, -1)

	// Resolve legacy inputs.
	if err := ResolveTaskInputs(task, step, mergedInputs, tasksByStep, nil); err != nil {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.updateStepInstance(ctx, si)
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

	return l.updateStepInstance(ctx, si)
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
			return l.updateStepInstance(ctx, si)
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
		return l.updateStepInstance(ctx, si)
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
				return l.updateStepInstance(ctx, si)
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
				return l.updateStepInstance(ctx, si)
			}
			// Materialize file/directory literals in scatter iteration outputs.
			if tmpDir, mkErr := os.MkdirTemp("", fmt.Sprintf("exprtool-%s-%d-", si.ID, i)); mkErr == nil {
				if materialized, matErr := fileliteral.MaterializeOutputs(outputs, tmpDir); matErr == nil {
					outputs = materialized
				}
			}
			results = append(results, outputs)
		}
		si.Outputs = l.mergeScatterOutputs(results, step, method, scatterArrays)
		now := time.Now().UTC()
		si.State = model.StepStateCompleted
		si.CompletedAt = &now
		l.logger.Info("scatter expression tool completed", "si_id", si.ID, "iterations", len(combinations))
		return l.updateStepInstance(ctx, si)
	}

	// For CommandLineTools, create and submit individual Tasks.
	allCompleted := true
	var results []map[string]any

	for i, combo := range combinations {
		// Evaluate 'when' per iteration.
		if step.When != "" {
			shouldRun, err := l.evaluateWhenForScatterIterationFromSteps(step, combo, mergedInputs, stepOutputs)
			if err != nil {
				// CWL spec: non-boolean 'when' expressions must fail.
				now := time.Now().UTC()
				si.State = model.StepStateFailed
				si.CompletedAt = &now
				l.logger.Error("scatter when eval failed", "si_id", si.ID, "iter", i, "error", err)
				return l.updateStepInstance(ctx, si)
			} else if !shouldRun {
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				// Create a terminal task so advanceSteps can find skipped iterations.
				task := l.createTaskFromStep(si, tmpTask, step, sub, execType, i)
				task.Outputs = nullOutputs
				task.State = model.TaskStateSuccess
				now := time.Now().UTC()
				task.CompletedAt = &now
				task.Job = combo
				if err := l.store.CreateTask(ctx, task); err != nil {
					now := time.Now().UTC()
					si.State = model.StepStateFailed
					si.CompletedAt = &now
					return l.updateStepInstance(ctx, si)
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
			return l.updateStepInstance(ctx, si)
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
				return l.updateStepInstance(ctx, si)
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
		si.ScatterMethod = method
		if method == "nested_crossproduct" && len(step.Scatter) > 1 {
			si.ScatterDims = make([]int, len(step.Scatter))
			for i, name := range step.Scatter {
				si.ScatterDims[i] = len(scatterArrays[name])
			}
		}
	}

	return l.updateStepInstance(ctx, si)
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
		return l.updateStepInstance(ctx, si)
	}

	if err := l.executeChildSubmission(ctx, childSub); err != nil {
		now := time.Now().UTC()
		si.State = model.StepStateFailed
		si.CompletedAt = &now
		return l.updateStepInstance(ctx, si)
	}

	now := time.Now().UTC()
	if childSub.State == model.SubmissionStateCompleted {
		si.State = model.StepStateCompleted
		si.Outputs = childSub.Outputs
	} else {
		si.State = model.StepStateFailed
	}
	si.CompletedAt = &now

	return l.updateStepInstance(ctx, si)
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
			return l.updateStepInstance(ctx, si)
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
				return l.updateStepInstance(ctx, si)
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
			return l.updateStepInstance(ctx, si)
		}

		if err := l.executeChildSubmission(ctx, childSub); err != nil {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.updateStepInstance(ctx, si)
		}

		if childSub.State != model.SubmissionStateCompleted {
			now := time.Now().UTC()
			si.State = model.StepStateFailed
			si.CompletedAt = &now
			return l.updateStepInstance(ctx, si)
		}

		results = append(results, childSub.Outputs)
	}

	si.Outputs = l.mergeScatterOutputs(results, step, method, scatterArrays)
	now := time.Now().UTC()
	si.State = model.StepStateCompleted
	si.CompletedAt = &now
	l.logger.Info("scatter sub-workflow completed", "si_id", si.ID, "iterations", len(combinations))

	return l.updateStepInstance(ctx, si)
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
		// Propagate worker group from step hints to task runtime hints.
		if step.Hints.WorkerGroup != "" {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			if task.RuntimeHints.WorkerGroup == "" {
				task.RuntimeHints.WorkerGroup = step.Hints.WorkerGroup
			}
		}
		// Propagate dataset requirements from step hints to task runtime hints.
		if len(step.Hints.RequiredDatasets) > 0 {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			if len(task.RuntimeHints.RequiredDatasets) == 0 {
				task.RuntimeHints.RequiredDatasets = step.Hints.RequiredDatasets
			}
		}
	}

	// Submission-level worker group (from labels) as fallback.
	if sub.Labels != nil {
		if wg := sub.Labels["worker_group"]; wg != "" {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			if task.RuntimeHints.WorkerGroup == "" {
				task.RuntimeHints.WorkerGroup = wg
			}
		}
	}

	return task
}

// determineExecutorType determines the executor type for a step.
// The server's DefaultExecutor (WHERE to run) takes precedence over CWL hints.
// CWL DockerRequirement (HOW to run) is orthogonal — it's passed to the worker
// via runtime hints so the worker can choose bare vs container execution.
//
// When no executor is explicitly set and the step uses a container (DockerRequirement),
// prefer dispatching to a worker if any online workers are registered. This avoids
// running container tasks on the server when workers are available.
func (l *Loop) determineExecutorType(step *model.Step, sub *model.Submission) model.ExecutorType {
	// Server-wide default executor overrides CWL hints.
	if l.config.DefaultExecutor != "" {
		return model.ExecutorType(l.config.DefaultExecutor)
	}
	// Explicit step hint from CWL (gowe:Execution.executor) — but not "container",
	// which describes HOW to run (use container runtime), not WHERE to run.
	if step.Hints != nil && step.Hints.ExecutorType != "" && step.Hints.ExecutorType != model.ExecutorTypeContainer {
		return step.Hints.ExecutorType
	}
	// Auto-promote container tasks to worker when workers are available.
	// This covers DockerRequirement steps that don't have an explicit gowe:Execution hint.
	if step.Hints != nil && step.Hints.DockerImage != "" {
		caps := l.workerCapabilities()
		if caps.OnlineCount > 0 {
			return model.ExecutorTypeWorker
		}
	}
	return model.ExecutorTypeLocal
}

// workerCapabilities returns a cached snapshot of online worker capabilities.
// Built once per tick from ListWorkers().
func (l *Loop) workerCapabilities() *WorkerCapabilities {
	if l.cachedWorkerCaps != nil {
		return l.cachedWorkerCaps
	}
	caps := &WorkerCapabilities{
		Groups:   make(map[string]int),
		Datasets: make(map[string]int),
	}
	workers, err := l.store.ListWorkers(context.Background())
	if err != nil {
		l.cachedWorkerCaps = caps
		return caps
	}
	for _, w := range workers {
		if w.State != model.WorkerStateOnline {
			continue
		}
		caps.OnlineCount++
		caps.Workers = append(caps.Workers, w)
		if model.HasContainerRuntime(w.Runtime) {
			caps.HasContainer = true
		}
		group := w.Group
		if group == "" {
			group = "default"
		}
		caps.Groups[group]++
		for dsID := range w.Datasets {
			caps.Datasets[dsID]++
		}
	}
	l.cachedWorkerCaps = caps
	return caps
}

// canMatchTask checks if any single online worker can satisfy ALL of a task's
// scheduling constraints simultaneously. Returns (true, "") if at least one
// worker matches, or (false, reason) with a human-readable explanation.
//
// Only GoWe-specific hard constraints are checked:
//   - Worker group (gowe:Execution.worker_group)
//   - Prestage datasets (gowe:ResourceData with mode=prestage)
//
// DockerRequirement (DockerImage) is NOT checked here because CWL treats it as
// a hint — workers without container runtimes can still execute tools bare.
// Container runtime matching is handled by CheckoutTask at checkout time.
func canMatchTask(caps *WorkerCapabilities, hints *model.RuntimeHints) (bool, string) {
	if caps.OnlineCount == 0 {
		return false, "no online workers"
	}

	wantGroup := ""
	if hints != nil && hints.WorkerGroup != "" {
		wantGroup = hints.WorkerGroup
	}
	var prestageIDs []string
	if hints != nil {
		for _, ds := range hints.RequiredDatasets {
			if ds.Mode == "prestage" {
				prestageIDs = append(prestageIDs, ds.ID)
			}
		}
	}

	// Fast path: no constraints beyond "any worker".
	if wantGroup == "" && len(prestageIDs) == 0 {
		return true, ""
	}

	for _, w := range caps.Workers {
		// Check worker group.
		if wantGroup != "" {
			wGroup := w.Group
			if wGroup == "" {
				wGroup = "default"
			}
			if wGroup != wantGroup {
				continue
			}
		}
		// Check prestage datasets — worker must have ALL of them.
		if len(prestageIDs) > 0 {
			allPresent := true
			for _, dsID := range prestageIDs {
				if _, ok := w.Datasets[dsID]; !ok {
					allPresent = false
					break
				}
			}
			if !allPresent {
				continue
			}
		}
		return true, ""
	}

	// Build descriptive reason.
	var reasons []string
	if wantGroup != "" {
		if caps.Groups[wantGroup] == 0 {
			reasons = append(reasons, fmt.Sprintf("no workers in group %q", wantGroup))
		}
	}
	for _, dsID := range prestageIDs {
		if caps.Datasets[dsID] == 0 {
			reasons = append(reasons, fmt.Sprintf("no workers with prestage dataset %q", dsID))
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no single worker satisfies all constraints simultaneously")
	}
	return false, strings.Join(reasons, "; ")
}

// hasOnlineWorkers checks if any workers are currently online.
func (l *Loop) hasOnlineWorkers() bool {
	return l.workerCapabilities().OnlineCount > 0
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

	// Propagate output destination so workers can stage outputs to the right place.
	if sub.OutputDestination != "" {
		if task.RuntimeHints == nil {
			task.RuntimeHints = &model.RuntimeHints{}
		}
		task.RuntimeHints.OutputDestination = sub.OutputDestination
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
		if si.Source == "" && len(si.Sources) == 0 {
			continue
		}
		// Use Sources array, fall back to Source string.
		sources := si.Sources
		if len(sources) == 0 && si.Source != "" {
			sources = strings.Split(si.Source, ",")
		}
		if len(sources) == 1 {
			src := sources[0]
			if strings.Contains(src, "/") {
				parts := strings.SplitN(src, "/", 2)
				stepID, outputID := parts[0], parts[1]
				if outputs, ok := stepOutputs[stepID]; ok {
					inputs[si.ID] = outputs[outputID]
				}
			} else {
				// Always set the input, even if nil. CWL spec requires
				// null (not undefined) for missing optional inputs so
				// that when expressions like $(inputs.x !== null) work.
				inputs[si.ID] = submissionInputs[src]
			}
		} else if len(sources) > 1 {
			values := make([]any, len(sources))
			for i, src := range sources {
				if strings.Contains(src, "/") {
					parts := strings.SplitN(src, "/", 2)
					if outputs, ok := stepOutputs[parts[0]]; ok {
						values[i] = outputs[parts[1]]
					}
				} else {
					values[i] = submissionInputs[src]
				}
			}
			inputs[si.ID] = values
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

// detectStuckTasks identifies classes of QUEUED worker tasks making zero progress.
// Tasks are grouped by their scheduling requirements; if a group's count hasn't
// decreased for StuckTaskThreshold consecutive ticks AND no capable worker exists,
// an error is logged and optionally the oldest task is failed.
func (l *Loop) detectStuckTasks(ctx context.Context, affected map[string]bool) error {
	queuedTasks, err := l.store.GetTasksByState(ctx, model.TaskStateQueued)
	if err != nil {
		return err
	}

	// Group QUEUED worker tasks by requirement key.
	currentCounts := make(map[taskRequirementKey]int)
	tasksByKey := make(map[taskRequirementKey][]*model.Task)
	for _, task := range queuedTasks {
		if task.ExecutorType != model.ExecutorTypeWorker {
			continue
		}
		key := requirementKeyForTask(task)
		currentCounts[key]++
		tasksByKey[key] = append(tasksByKey[key], task)
	}

	caps := l.workerCapabilities()

	for key, count := range currentCounts {
		lastCount, existed := l.stuck.lastCounts[key]
		if existed && count >= lastCount {
			// No progress (count didn't decrease).
			l.stuck.staleTicks[key]++
		} else {
			// Progress made or new key.
			l.stuck.staleTicks[key] = 0
		}
		l.stuck.lastCounts[key] = count

		// Build a synthetic RuntimeHints for canMatchTask from the key.
		hints := hintsFromRequirementKey(key)
		canMatch, reason := canMatchTask(caps, hints)

		// Emit an early warning at half the stuck threshold when no worker can match.
		if l.stuck.staleTicks[key] == l.config.StuckTaskThreshold/2 && !canMatch {
			l.logger.Warn("tasks approaching stuck threshold: no capable worker",
				"count", count, "reason", reason,
				"group", key.WorkerGroup,
				"prestage", key.PrestageIDs,
				"stale_ticks", l.stuck.staleTicks[key],
				"threshold", l.config.StuckTaskThreshold)
		}

		if l.stuck.staleTicks[key] < l.config.StuckTaskThreshold {
			continue
		}

		if !canMatch {
			l.logger.Error("stuck tasks: no capable worker",
				"count", count, "reason", reason,
				"group", key.WorkerGroup,
				"prestage", key.PrestageIDs,
				"stale_ticks", l.stuck.staleTicks[key])
		} else {
			l.logger.Warn("stuck tasks: queued but not being picked up",
				"count", count,
				"group", key.WorkerGroup,
				"prestage", key.PrestageIDs,
				"stale_ticks", l.stuck.staleTicks[key])
		}

		if l.config.StuckTaskAction == "fail" && !canMatch {
			// Fail the oldest task in this group (by CreatedAt).
			oldest := tasksByKey[key][0]
			for _, t := range tasksByKey[key][1:] {
				if t.CreatedAt.Before(oldest.CreatedAt) {
					oldest = t
				}
			}

			// Build a rich error message with task requirements and worker summary.
			stderrMsg := buildStuckTaskError(key, reason, caps, l.stuck.staleTicks[key])

			now := time.Now().UTC()
			oldest.State = model.TaskStateFailed
			oldest.Stderr = stderrMsg
			oldest.CompletedAt = &now
			// No capable worker exists — retrying won't help. Exhaust retries
			// so markRetries does not re-queue this task.
			oldest.MaxRetries = oldest.RetryCount
			if err := l.store.UpdateTask(ctx, oldest); err != nil {
				l.logger.Error("fail stuck task", "task_id", oldest.ID, "error", err)
			} else {
				l.logger.Info("failed stuck task", "task_id", oldest.ID, "reason", reason)
				affected[oldest.SubmissionID] = true
			}
		}
	}

	// Clean up keys that no longer have queued tasks.
	for key := range l.stuck.lastCounts {
		if currentCounts[key] == 0 {
			delete(l.stuck.lastCounts, key)
			delete(l.stuck.staleTicks, key)
		}
	}

	return nil
}

// requirementKeyForTask builds a taskRequirementKey from a task's runtime hints.
func requirementKeyForTask(task *model.Task) taskRequirementKey {
	key := taskRequirementKey{}
	if task.RuntimeHints != nil {
		key.WorkerGroup = task.RuntimeHints.WorkerGroup
		var prestageIDs []string
		for _, ds := range task.RuntimeHints.RequiredDatasets {
			if ds.Mode == "prestage" {
				prestageIDs = append(prestageIDs, ds.ID)
			}
		}
		if len(prestageIDs) > 0 {
			sort.Strings(prestageIDs)
			key.PrestageIDs = strings.Join(prestageIDs, ",")
		}
	}
	return key
}

// hintsFromRequirementKey reconstructs a RuntimeHints from a taskRequirementKey
// for use with canMatchTask.
func hintsFromRequirementKey(key taskRequirementKey) *model.RuntimeHints {
	hints := &model.RuntimeHints{}
	hints.WorkerGroup = key.WorkerGroup
	if key.PrestageIDs != "" {
		for _, id := range strings.Split(key.PrestageIDs, ",") {
			hints.RequiredDatasets = append(hints.RequiredDatasets, model.DatasetRequirement{
				ID:   id,
				Mode: "prestage",
			})
		}
	}
	return hints
}

// buildStuckTaskError constructs a detailed error message for a stuck task,
// including what the task required and what workers are currently available.
func buildStuckTaskError(key taskRequirementKey, reason string, caps *WorkerCapabilities, staleTicks int) string {
	var b strings.Builder
	b.WriteString("Task stuck: no capable worker available\n")

	// Required section.
	b.WriteString("Required:")
	if key.WorkerGroup != "" {
		fmt.Fprintf(&b, " worker_group=%s", key.WorkerGroup)
	}
	if key.PrestageIDs != "" {
		fmt.Fprintf(&b, " prestage=[%s]", key.PrestageIDs)
	}
	if key.WorkerGroup == "" && key.PrestageIDs == "" {
		b.WriteString(" (no specific constraints)")
	}
	b.WriteString("\n")

	// Available workers section.
	if caps.OnlineCount == 0 {
		b.WriteString("Available workers: 0 online\n")
	} else {
		// Collect unique groups and runtimes.
		groupSet := make(map[string]bool)
		runtimeSet := make(map[string]bool)
		datasetSet := make(map[string]bool)
		for _, w := range caps.Workers {
			g := w.Group
			if g == "" {
				g = "default"
			}
			groupSet[g] = true
			runtimeSet[string(w.Runtime)] = true
			for dsID := range w.Datasets {
				datasetSet[dsID] = true
			}
		}
		groups := sortedKeys(groupSet)
		runtimes := sortedKeys(runtimeSet)
		datasets := sortedKeys(datasetSet)

		fmt.Fprintf(&b, "Available workers: %d online (groups: [%s], runtimes: [%s]",
			caps.OnlineCount, strings.Join(groups, ", "), strings.Join(runtimes, ", "))
		if len(datasets) > 0 {
			fmt.Fprintf(&b, ", datasets: [%s]", strings.Join(datasets, ", "))
		}
		b.WriteString(")\n")
	}

	fmt.Fprintf(&b, "Reason: %s\n", reason)
	fmt.Fprintf(&b, "Stale ticks: %d", staleTicks)
	return b.String()
}

// sortedKeys returns the keys of a map[string]bool in sorted order.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
					if err := l.updateStepInstance(ctx, si); err != nil {
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
					sub, _ := l.cache.getSubmission(ctx, l.store, si.SubmissionID)
					if sub != nil {
						wf, _ := l.cache.getWorkflow(ctx, l.store, sub.WorkflowID)
						if wf != nil {
							step := findStep(wf, si.StepID)
							if step != nil {
								results := make([]map[string]any, len(tasks))
								for _, t := range tasks {
									if t.ScatterIndex >= 0 && t.ScatterIndex < len(results) {
										results[t.ScatterIndex] = t.Outputs
									}
								}
								if si.ScatterMethod == "nested_crossproduct" && len(si.ScatterDims) > 1 {
									si.Outputs = mergeScatterResultsNested(results, step.Out, si.ScatterDims)
								} else {
									si.Outputs = mergeScatterResults(results, step.Out)
								}
							}
						}
					}
				} else if len(tasks) == 1 {
					si.Outputs = tasks[0].Outputs
				}
			}
			si.CompletedAt = &now

			if err := l.updateStepInstance(ctx, si); err != nil {
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
		sub, err := l.cache.getSubmission(ctx, l.store, subID)
		if err != nil {
			l.logger.Error("get submission for finalize", "submission_id", subID, "error", err)
			continue
		}
		if sub == nil || sub.State.IsTerminal() {
			continue
		}

		// Load step instances instead of tasks.
		steps, err := l.cache.listStepsBySubmission(ctx, l.store, subID)
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
				sub.Error = l.buildSubmissionError(ctx, steps)
			} else {
				sub.State = model.SubmissionStateCompleted

				// Collect workflow outputs from step instance outputs.
				wf, wfErr := l.cache.getWorkflow(ctx, l.store, sub.WorkflowID)
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
						sub.Error = &model.SubmissionError{
							Code:    "OUTPUT_COLLECTION_FAILED",
							Message: outErr.Error(),
						}
					} else {
						sub.Outputs = outputs
						l.logger.Debug("collected workflow outputs", "submission_id", subID, "outputs", len(outputs))
					}
				}
			}
			now := time.Now().UTC()
			sub.CompletedAt = &now
			if err := l.updateSubmission(ctx, sub); err != nil {
				l.logger.Error("finalize submission", "submission_id", subID, "error", err)
			} else {
				l.logger.Info("submission finalized", "submission_id", subID, "state", sub.State)
			}
		} else if (anyActive || anyFailed) && sub.State == model.SubmissionStatePending {
			sub.State = model.SubmissionStateRunning
			if err := l.updateSubmission(ctx, sub); err != nil {
				l.logger.Error("activate submission", "submission_id", subID, "error", err)
			} else {
				l.logger.Info("submission running", "submission_id", subID)
			}
		}
	}

	return nil
}

// buildSubmissionError constructs a SubmissionError from the first failed step
// and its associated failed task (if any), including exit code and stderr snippet.
func (l *Loop) buildSubmissionError(ctx context.Context, steps []*model.StepInstance) *model.SubmissionError {
	// Find the first failed step instance.
	var failedStep *model.StepInstance
	for _, si := range steps {
		if si.State == model.StepStateFailed {
			failedStep = si
			break
		}
	}
	if failedStep == nil {
		return &model.SubmissionError{
			Code:    "STEP_FAILED",
			Message: "one or more steps failed",
		}
	}

	// Check if this step failed due to an unsupported requirement.
	if reason, ok := l.unsupportedSteps[failedStep.ID]; ok {
		delete(l.unsupportedSteps, failedStep.ID)
		return &model.SubmissionError{
			Code:    string(model.ErrUnsupportedRequirement),
			Message: reason,
			Context: &model.SubmissionErrDetail{StepID: failedStep.StepID},
		}
	}

	subErr := &model.SubmissionError{
		Code:    "STEP_FAILED",
		Message: fmt.Sprintf("step '%s' failed", failedStep.StepID),
		Context: &model.SubmissionErrDetail{
			StepID: failedStep.StepID,
		},
	}

	// Look for a failed task under this step to get exit code and stderr.
	tasks, err := l.store.ListTasksByStepInstance(ctx, failedStep.ID)
	if err != nil {
		return subErr
	}

	for _, task := range tasks {
		if task.State == model.TaskStateFailed {
			subErr.Code = "TASK_FAILED"
			subErr.Context.TaskID = task.ID
			subErr.Context.ExitCode = task.ExitCode

			// Include a stderr snippet (truncate to 1000 chars for storage).
			stderr := task.Stderr
			if len(stderr) > 1000 {
				stderr = stderr[:1000] + "...(truncated)"
			}
			if stderr != "" {
				subErr.Context.Stderr = stderr
			}

			subErr.Message = fmt.Sprintf("step '%s' task failed", failedStep.StepID)
			if task.ExitCode != nil {
				subErr.Message = fmt.Sprintf("step '%s' task failed with exit code %d", failedStep.StepID, *task.ExitCode)
			}
			break
		}
	}

	return subErr
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
			si.LinkMerge,
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
	// CWL spec priority: job requirements > tool requirements > step requirements > workflow requirements.
	mergeRequirementsIntoTool(tool, graphDoc.Workflow, step.ID)

	// Merge cwl:requirements from the job input document (highest priority).
	if jobReqs, ok := submissionInputs["cwl:requirements"].([]any); ok {
		mergeJobRequirementsIntoTool(tool, jobReqs)
	}

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

	// Extract gowe:ResourceData from both requirements and hints.
	for _, section := range []string{"requirements", "hints"} {
		var m map[string]any
		if section == "requirements" {
			m, _ = tool["requirements"].(map[string]any)
		} else {
			m, _ = tool["hints"].(map[string]any)
		}
		if m == nil {
			continue
		}
		if rd, ok := m["gowe:ResourceData"].(map[string]any); ok {
			if datasets, ok := rd["datasets"].([]any); ok {
				for _, d := range datasets {
					dm, _ := d.(map[string]any)
					if dm == nil {
						continue
					}
					hints.RequiredDatasets = append(hints.RequiredDatasets, model.DatasetRequirement{
						ID:     stringFromAny(dm["id"]),
						Path:   stringFromAny(dm["path"]),
						Size:   stringFromAny(dm["size"]),
						Mode:   stringFromAny(dm["mode"]),
						Source: stringFromAny(dm["source"]),
					})
				}
			}
			break
		}
	}

	return hints
}

// stringFromAny converts any value to string (helper for map field extraction).
func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
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

	// Extract gowe:ResourceData from requirements and hints.
	for _, m := range []map[string]any{tool.Requirements, tool.Hints} {
		if m == nil {
			continue
		}
		if rd, ok := m["gowe:ResourceData"].(map[string]any); ok {
			if datasets, ok := rd["datasets"].([]any); ok {
				for _, d := range datasets {
					dm, _ := d.(map[string]any)
					if dm == nil {
						continue
					}
					hints.RequiredDatasets = append(hints.RequiredDatasets, model.DatasetRequirement{
						ID:     stringFromAny(dm["id"]),
						Path:   stringFromAny(dm["path"]),
						Size:   stringFromAny(dm["size"]),
						Mode:   stringFromAny(dm["mode"]),
						Source: stringFromAny(dm["source"]),
					})
				}
			}
			break
		}
	}

	return hints
}

// hasInplaceUpdateReq checks if a tool map has InplaceUpdateRequirement enabled.
func hasInplaceUpdateReq(tool map[string]any) bool {
	if tool == nil {
		return false
	}
	reqs, ok := tool["requirements"].(map[string]any)
	if !ok {
		return false
	}
	iur, ok := reqs["InplaceUpdateRequirement"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := iur["inplaceUpdate"].(bool)
	return enabled
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

	// Populate directory listings for inputs with loadListing before evaluation.
	// ExpressionTools need listings populated just like CommandLineTools.
	cwltool.PopulateDirectoryListingsFromDefs(tool.Inputs, tool.Requirements, task.Job, false)

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

// mergeJobRequirementsIntoTool merges cwl:requirements from the job input document
// into a tool map. Job requirements have the highest priority, overriding all other requirements.
func mergeJobRequirementsIntoTool(tool map[string]any, jobReqs []any) {
	if tool == nil || len(jobReqs) == 0 {
		return
	}

	toolReqs, _ := tool["requirements"].(map[string]any)
	if toolReqs == nil {
		toolReqs = make(map[string]any)
	}

	for _, req := range jobReqs {
		reqMap, ok := req.(map[string]any)
		if !ok {
			continue
		}
		class, _ := reqMap["class"].(string)
		if class == "" {
			continue
		}
		// Job requirements override — use class name as key.
		toolReqs[class] = reqMap
	}

	tool["requirements"] = toolReqs
}
