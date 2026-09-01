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
	DefaultExecutor  string // Server-wide default executor (fallback when no CWL hint). Empty = auto.
	ForceExecutor    string // Force all tasks to this executor, ignoring CWL hints. Empty = respect hints.
	WorkspaceStaging string // "server" = pre/post-stage ws:// on scheduler, "" = passthrough to workers.

	// TokenInjectGroups lists worker groups whose tasks automatically receive the
	// submitter's provider token, without the per-tool gowe:Execution.inject_bvbrc_token
	// opt-in. This scopes the auto-injection to operator-trusted groups (e.g. curated
	// BV-BRC tool workers) while default/untrusted groups stay opt-in (least privilege,
	// SPECIFICATION.md §13.5, ADR-0010). Empty = no group auto-injection.
	TokenInjectGroups []string

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
	HasContainer bool            // any worker with docker/apptainer
	Groups       map[string]int  // group → count of online workers
	Datasets     map[string]int  // dataset ID → count of online workers
	Workers      []*model.Worker // full list of online workers
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

// finalizeSubmissionCAS persists a terminal submission update via
// compare-and-set (skipped when the submission is already terminal, e.g. a
// concurrent cancel won) and invalidates the tick cache. Returns whether the
// write was applied.
func (l *Loop) finalizeSubmissionCAS(ctx context.Context, sub *model.Submission) (bool, error) {
	applied, err := l.store.FinalizeSubmission(ctx, sub)
	if err == nil && l.cache != nil {
		l.cache.invalidateSubmission(sub.ID)
	}
	return applied, err
}

// activateSubmissionCAS moves a submission PENDING→RUNNING via compare-and-set
// (skipped when it is no longer PENDING) and invalidates the tick cache.
// Returns whether the write was applied.
func (l *Loop) activateSubmissionCAS(ctx context.Context, id string) (bool, error) {
	applied, err := l.store.ActivateSubmission(ctx, id)
	if err == nil && l.cache != nil {
		l.cache.invalidateSubmission(id)
	}
	return applied, err
}

// maxSubmissionListPages caps the pagination walk in listSubmissionsByState
// (10,000 submissions per state per tick) so a store bug that keeps returning
// full pages cannot spin the scheduler forever.
const maxSubmissionListPages = 100

// listSubmissionsByState collects every submission in the given state by
// paging through the store before any processing. A single Limit-100 page
// starves older rows: the default created_at DESC ordering returns the newest
// rows first, so anything beyond the first page would never be visited.
// Callers process the collected list only after the walk completes, because
// processing mutates submission states and would shift subsequent pages.
func (l *Loop) listSubmissionsByState(ctx context.Context, state string) ([]*model.Submission, error) {
	var all []*model.Submission
	seen := make(map[string]bool)
	for page := 0; ; page++ {
		if page >= maxSubmissionListPages {
			l.logger.Warn("submission list pagination cap reached; processing partial list",
				"state", state, "pages", maxSubmissionListPages, "collected", len(all))
			return all, nil
		}
		offset := page * model.MaxListLimit
		// created_at ASC keeps the page walk stable under concurrent inserts
		// (new rows land after the current position); the seen-map dedupes the
		// residual boundary shifts from concurrent removals.
		// ExcludeChildren must stay unset here: child submissions (scatter /
		// sub-workflow fan-out) are scheduled through this walk like any other
		// submission and would deadlock if hidden.
		subs, total, err := l.store.ListSubmissions(ctx, model.ListOptions{
			State: state, Limit: model.MaxListLimit, Offset: offset,
			SortBy: "created_at", SortDir: "asc",
		})
		if err != nil {
			return nil, fmt.Errorf("list %s submissions (offset %d): %w", state, offset, err)
		}
		for _, sub := range subs {
			if seen[sub.ID] {
				continue
			}
			seen[sub.ID] = true
			all = append(all, sub)
		}
		// Drive the walk by the row count, not the page length: the store
		// skips corrupt/undecryptable rows AFTER the SQL LIMIT, so a short
		// page does not mean the end of the result set.
		if offset+model.MaxListLimit >= total {
			return all, nil
		}
	}
}

// Tick runs a single scheduling iteration using the 3-level state architecture:
// Submissions → StepInstances → Tasks.
func (l *Loop) Tick(ctx context.Context) error {
	l.cachedWorkerCaps = nil          // Reset per-tick worker capability cache.
	l.cache = newTickCache()          // Reset per-tick entity cache.
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
				task.Inputs = combo
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
		task.Inputs = combo

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

// dispatchSubWorkflowStep handles a non-scatter sub-workflow step without
// blocking the tick loop: it persists a single proxy task (ExecutorType
// "subworkflow", RUNNING from birth) paired 1:1 with a child submission that
// flows through the normal tick machinery. pollInFlight advances the proxy
// from the child's state, and fan-in is the ordinary single-task path in
// advanceSteps.
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

	task := l.createSubworkflowProxyTask(si, tmpTask, step, sub, tmpTask.Job, -1)

	// Persist the proxy and the DISPATCHED step in ONE transaction: a crash
	// can never leave a DISPATCHED step without its task (or vice versa) —
	// either both land, or the step stays READY and is re-dispatched. [F4]
	si.State = model.StepStateDispatched
	if err := l.store.CreateTasksAndDispatchStep(ctx, []*model.Task{task}, si); err != nil {
		return fmt.Errorf("persist sub-workflow dispatch: %w", err)
	}
	l.cache.invalidateSteps(si.SubmissionID)

	childSub, err := l.createChildSubmission(ctx, task, subGraph, task.Job, sub, wf)
	if err != nil {
		// Child creation is not retryable (e.g. plaintext-token refusal):
		// surface the error on the proxy and fail the step outright. [F11]
		l.failSubworkflowProxy(ctx, task, si, fmt.Sprintf("create child submission: %v", err))
		return nil
	}

	l.reconcileDispatchWithCancel(ctx, sub.ID, si.ID, []*model.Task{task}, []*model.Submission{childSub})
	return nil
}

// dispatchScatterSubWorkflow handles scatter over a sub-workflow without
// blocking the tick loop: every scatter combination gets one persisted proxy
// task (RUNNING, Job=combo, MaxRetries=0) paired 1:1 with a child submission
// that flows through the normal tick machinery. The step fans in through the
// ordinary ScatterIndex merge in advanceSteps once pollInFlight has advanced
// every proxy from its child's state.
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

	// Empty scatter completes inline with empty merged outputs: advanceSteps
	// skips zero-task steps, so a DISPATCHED empty scatter would hang
	// forever. [M1]
	if len(combinations) == 0 {
		si.Outputs = l.mergeScatterOutputs(nil, step, method, scatterArrays)
		now := time.Now().UTC()
		si.State = model.StepStateCompleted
		si.CompletedAt = &now
		l.logger.Info("scatter sub-workflow completed (empty scatter)", "si_id", si.ID)
		return l.updateStepInstance(ctx, si)
	}

	// Build all proxy tasks up front. When-skipped combinations become
	// terminal SUCCESS tasks with null outputs — exactly like plain scatter —
	// so the ScatterIndex fan-in still sees a full index set.
	tasks := make([]*model.Task, 0, len(combinations))
	childless := make(map[string]bool) // task IDs of when-skipped combos: no child
	for i, combo := range combinations {
		if step.When != "" {
			shouldRun, err := l.evaluateWhenForScatterIterationFromSteps(step, combo, mergedInputs, stepOutputs)
			if err != nil {
				// CWL spec: non-boolean 'when' expressions must fail the step.
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
				now := time.Now().UTC()
				task := l.createTaskFromStep(si, tmpTask, step, sub, model.ExecutorTypeSubworkflow, i)
				task.Outputs = nullOutputs
				task.State = model.TaskStateSuccess
				task.CompletedAt = &now
				task.Job = combo
				task.Inputs = combo
				task.MaxRetries = 0
				tasks = append(tasks, task)
				childless[task.ID] = true
				continue
			}
		}
		tasks = append(tasks, l.createSubworkflowProxyTask(si, tmpTask, step, sub, combo, i))
	}

	// Persist all proxies and the DISPATCHED step in ONE transaction — a
	// crash mid-dispatch leaves the step READY with zero tasks, so the next
	// tick re-dispatches cleanly with no duplicates. [F4]
	si.State = model.StepStateDispatched
	si.ScatterCount = len(combinations)
	si.ScatterMethod = method
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		si.ScatterDims = make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			si.ScatterDims[i] = len(scatterArrays[name])
		}
	}
	if err := l.store.CreateTasksAndDispatchStep(ctx, tasks, si); err != nil {
		return fmt.Errorf("persist scatter sub-workflow dispatch: %w", err)
	}
	l.cache.invalidateSteps(si.SubmissionID)

	// Create the child submissions outside the transaction (idempotent per
	// proxy: a crash between the transaction and any child creation is
	// repaired by pollInFlight from the persisted task.Job).
	children := make([]*model.Submission, 0, len(tasks))
	for _, task := range tasks {
		if childless[task.ID] {
			continue
		}
		childSub, err := l.createChildSubmission(ctx, task, subGraph, task.Job, sub, wf)
		if err != nil {
			// Not retryable (e.g. plaintext-token refusal): fail the step
			// outright. Already-created siblings keep running until
			// pollInFlight's reconciliation cancels them against the
			// now-terminal step. [F11]
			l.failSubworkflowProxy(ctx, task, si, fmt.Sprintf("create child submission: %v", err))
			return nil
		}
		children = append(children, childSub)
	}

	l.reconcileDispatchWithCancel(ctx, sub.ID, si.ID, tasks, children)

	l.logger.Info("scatter sub-workflow dispatched",
		"si_id", si.ID, "iterations", len(combinations), "children", len(children))
	return nil
}

// createSubworkflowProxyTask builds the persisted proxy task that pairs 1:1
// with a child submission. Proxies are RUNNING from birth — never QUEUED, so
// CheckoutTask, the stuck detector, and requeue/reconcile (which all filter
// on executor_type='worker') never see them — and carry the fully-resolved
// child inputs in Job, so recovery can recreate the child without re-running
// scatter combination or valueFrom/when JavaScript. MaxRetries is pinned to
// 0: a FAILED proxy must never cycle through resubmitRetrying, which would
// erase the child's error. [F8]
func (l *Loop) createSubworkflowProxyTask(si *model.StepInstance, tmpTask *model.Task, step *model.Step,
	sub *model.Submission, job map[string]any, scatterIndex int) *model.Task {

	task := l.createTaskFromStep(si, tmpTask, step, sub, model.ExecutorTypeSubworkflow, scatterIndex)
	now := time.Now().UTC()
	task.State = model.TaskStateRunning
	task.StartedAt = &now
	task.Job = job
	task.Inputs = job
	task.MaxRetries = 0
	// No addUserToken: the proxy never executes, so a token at rest on a
	// long-lived RUNNING row is pure exposure, and propagating the parent's
	// OutputDestination here would contradict the child-level drop. [F6]
	return task
}

// failSubworkflowProxy records a non-retryable sub-workflow dispatch failure:
// the proxy task goes FAILED with the reason in Stderr (so
// buildSubmissionError surfaces it) and the step instance is failed. [F11]
func (l *Loop) failSubworkflowProxy(ctx context.Context, task *model.Task, si *model.StepInstance, reason string) {
	now := time.Now().UTC()
	task.State = model.TaskStateFailed
	task.Stderr = reason
	task.CompletedAt = &now
	if _, err := l.store.TerminalizeTask(ctx, task); err != nil {
		l.logger.Error("fail subworkflow proxy", "task_id", task.ID, "error", err)
	}
	si.State = model.StepStateFailed
	si.CompletedAt = &now
	if err := l.updateStepInstance(ctx, si); err != nil {
		l.logger.Error("fail subworkflow step", "si_id", si.ID, "error", err)
	}
	l.logger.Error("sub-workflow dispatch failed", "si_id", si.ID, "task_id", task.ID, "reason", reason)
}

// reconcileDispatchWithCancel closes the cancel-during-dispatch race: the
// cancel handler's CancelNonTerminalSteps/Tasks fan-out may have run between
// this step's READY read and the dispatch transaction, in which case the
// transaction's DISPATCHED write clobbered the handler's SKIPPED and the
// proxies/children were created after the handler's snapshot. Re-read the
// parent; if it went terminal, cancel the created children, SKIP the proxy
// tasks, and conditionally re-SKIP the step. pollInFlight's reconciliation is
// the per-tick backstop for anything this misses. [F2,M2]
func (l *Loop) reconcileDispatchWithCancel(ctx context.Context, subID, siID string,
	tasks []*model.Task, children []*model.Submission) {

	// Fresh read (not tickCache): the whole point is to observe a cancel that
	// landed after this tick's snapshot.
	fresh, err := l.store.GetSubmission(ctx, subID)
	if err != nil || fresh == nil {
		l.logger.Error("re-read submission after dispatch", "submission_id", subID, "error", err)
		return
	}
	if !fresh.State.IsTerminal() {
		return
	}
	l.logger.Info("submission went terminal during sub-workflow dispatch; undoing fan-out",
		"submission_id", subID, "si_id", siID, "state", fresh.State)
	for _, child := range children {
		l.cancelChildSubmission(ctx, child)
	}
	now := time.Now().UTC()
	for _, task := range tasks {
		task.State = model.TaskStateSkipped
		task.CompletedAt = &now
		// CAS: when-skipped synthetic SUCCESS tasks are already terminal and
		// stay as they are.
		if _, err := l.store.TerminalizeTask(ctx, task); err != nil {
			l.logger.Error("skip proxy task after cancel", "task_id", task.ID, "error", err)
		}
	}
	l.skipStepInstanceIfActive(ctx, siID)
}

// cancelChildSubmission cancels an active child submission and skips its
// steps and tasks, mirroring the server's cancel handler. The CAS finalize
// leaves a concurrently-terminalized child alone; the step/task writes only
// touch non-terminal rows either way, so a worker can never check out
// orphaned child work.
//
// Nested sub-workflows cascade one level per tick with no explicit recursion:
// CancelNonTerminalTasks excludes the child's own subworkflow proxies, so they
// stay RUNNING here — next tick pollSubworkflowTask observes their (now
// CANCELLED) submission, cancels the grandchildren, and SKIPs those proxies.
func (l *Loop) cancelChildSubmission(ctx context.Context, child *model.Submission) {
	now := time.Now().UTC()
	child.State = model.SubmissionStateCancelled
	child.CompletedAt = &now
	if _, err := l.finalizeSubmissionCAS(ctx, child); err != nil {
		l.logger.Error("cancel child submission", "child_id", child.ID, "error", err)
		return
	}
	if _, err := l.store.CancelNonTerminalSteps(ctx, child.ID, now); err != nil {
		l.logger.Error("cancel child steps", "child_id", child.ID, "error", err)
	}
	if _, err := l.store.CancelNonTerminalTasks(ctx, child.ID, now); err != nil {
		l.logger.Error("cancel child tasks", "child_id", child.ID, "error", err)
	}
	l.logger.Info("child submission cancelled", "child_id", child.ID)
}

// skipStepInstanceIfActive re-reads a step instance and marks it SKIPPED
// unless a concurrent writer already made it terminal. This is the
// "conditionally re-SKIP" half of the cancel-during-dispatch defense: the
// dispatch transaction may have clobbered the cancel handler's SKIPPED with
// DISPATCHED, and the cancel handler may crash between its submission and
// step writes. [F2,M2]
func (l *Loop) skipStepInstanceIfActive(ctx context.Context, siID string) {
	si, err := l.store.GetStepInstance(ctx, siID)
	if err != nil || si == nil {
		l.logger.Error("re-read step instance for skip", "si_id", siID, "error", err)
		return
	}
	if si.State.IsTerminal() {
		return
	}
	now := time.Now().UTC()
	si.State = model.StepStateSkipped
	si.CompletedAt = &now
	if err := l.updateStepInstance(ctx, si); err != nil {
		l.logger.Error("skip step instance", "si_id", siID, "error", err)
		return
	}
	l.logger.Info("step instance skipped (cancel reconciliation)", "si_id", siID)
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
		// Propagate GPU requirement from step hints.
		if step.Hints.RequiresGPU {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			task.RuntimeHints.RequiresGPU = true
		}
		// Propagate BV-BRC token injection hint.
		if step.Hints.InjectBVBRCToken {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			task.RuntimeHints.InjectBVBRCToken = true
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
		// Debug submissions: workers keep all task working data for inspection.
		if sub.Labels["debug"] == "true" {
			if task.RuntimeHints == nil {
				task.RuntimeHints = &model.RuntimeHints{}
			}
			task.RuntimeHints.Debug = true
		}
	}

	return task
}

// determineExecutorType determines the executor type for a step.
//
// Priority order:
//  1. ForceExecutor (server flag) — overrides everything, ignores all hints
//  2. CWL hint (gowe:Execution.executor) — explicit per-step routing
//  3. Auto-promote: DockerRequirement + online workers → worker executor
//  4. DefaultExecutor (server flag) — fallback when no hint is set
//  5. local
func (l *Loop) determineExecutorType(step *model.Step, sub *model.Submission) model.ExecutorType {
	// Force executor overrides everything.
	if l.config.ForceExecutor != "" {
		return model.ExecutorType(l.config.ForceExecutor)
	}
	// Explicit step hint from CWL (gowe:Execution.executor) — but not "container",
	// which describes HOW to run (use container runtime), not WHERE to run.
	if step.Hints != nil && step.Hints.ExecutorType != "" && step.Hints.ExecutorType != model.ExecutorTypeContainer {
		return step.Hints.ExecutorType
	}
	// Auto-promote container tasks to worker when workers are available.
	if step.Hints != nil && step.Hints.DockerImage != "" {
		caps := l.workerCapabilities()
		if caps.OnlineCount > 0 {
			return model.ExecutorTypeWorker
		}
	}
	// Server-wide default executor as fallback.
	if l.config.DefaultExecutor != "" {
		return model.ExecutorType(l.config.DefaultExecutor)
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

	wantGPU := hints != nil && hints.RequiresGPU

	// Fast path: no constraints beyond "any worker".
	if wantGroup == "" && len(prestageIDs) == 0 && !wantGPU {
		return true, ""
	}

	for _, w := range caps.Workers {
		// Check GPU requirement.
		if wantGPU && !w.GPUEnabled {
			continue
		}
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
	if wantGPU {
		hasGPU := false
		for _, w := range caps.Workers {
			if w.GPUEnabled {
				hasGPU = true
				break
			}
		}
		if !hasGPU {
			reasons = append(reasons, "no GPU-enabled workers")
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
// Worker and BV-BRC executor tasks always receive the submitter's token so
// that tools can make authenticated downstream calls (workspace, AppService).
// For other executor types, the token is only embedded when server-side
// workspace staging is disabled (wsStager == nil) or when the step has the
// InjectBVBRCToken hint.
// groupAutoInjectsToken reports whether a worker task's target group is one the
// operator has opted into automatic token injection via --token-inject-groups.
// Tasks in other groups (including the default group) get the token only when
// they explicitly opt in (gowe:Execution.inject_bvbrc_token) — preserving the
// least-privilege boundary in SPECIFICATION.md §13.5 for untrusted tools.
func (l *Loop) groupAutoInjectsToken(task *model.Task) bool {
	if len(l.config.TokenInjectGroups) == 0 {
		return false
	}
	group := "default"
	if task.RuntimeHints != nil && task.RuntimeHints.WorkerGroup != "" {
		group = task.RuntimeHints.WorkerGroup
	}
	for _, g := range l.config.TokenInjectGroups {
		if g == group {
			return true
		}
	}
	return false
}

func (l *Loop) addUserToken(task *model.Task, sub *model.Submission) {
	needsToken := l.wsStager == nil ||
		(task.RuntimeHints != nil && task.RuntimeHints.InjectBVBRCToken) ||
		task.ExecutorType == model.ExecutorTypeBVBRC ||
		(task.ExecutorType == model.ExecutorTypeWorker && l.groupAutoInjectsToken(task))
	if sub.UserToken != "" && needsToken {
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

// scrubTaskToken removes the user authentication token from a task's runtime
// hints so that credentials are not persisted in the database after the task
// reaches a terminal state. The token is only needed while the task is in
// flight; once complete, keeping it at rest is unnecessary exposure.
func scrubTaskToken(task *model.Task) {
	if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
		task.RuntimeHints.StagerOverrides.HTTPCredential = nil
	}
}

// submitAndUpdateTask submits a task to its executor and updates its state.
// The final persist is a guarded write: a concurrent HTTP cancel may have
// already terminalized (SKIPPED) the task, and the submit outcome must not
// resurrect it under a cancelled submission.
func (l *Loop) submitAndUpdateTask(ctx context.Context, task *model.Task) {
	exec, err := l.registry.Get(task.ExecutorType)
	if err != nil {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = err.Error()
		task.CompletedAt = &now
		l.persistSubmitOutcome(ctx, task)
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
			scrubTaskToken(task)
			l.logger.Info("task completed", "task_id", task.ID, "state", newState, "step_id", task.StepID)
		} else {
			task.State = model.TaskStateQueued
			l.logger.Debug("task queued", "task_id", task.ID, "step_id", task.StepID)
		}
	}

	l.persistSubmitOutcome(ctx, task)
}

// persistSubmitOutcome writes a submit result without overwriting a state a
// concurrent cancel already made terminal (TerminalizeTask's guard fits both
// the QUEUED and FAILED outcomes: write only while the row is non-terminal).
func (l *Loop) persistSubmitOutcome(ctx context.Context, task *model.Task) {
	applied, err := l.store.TerminalizeTask(ctx, task)
	if err != nil {
		l.logger.Error("persist submit outcome", "task_id", task.ID, "error", err)
		return
	}
	if !applied {
		l.logger.Info("task reached a terminal state concurrently, submit outcome discarded",
			"task_id", task.ID, "outcome_state", task.State)
	}
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

// resubmitRetrying re-submits RETRYING tasks to their executor. Each task is
// first CLAIMED with a RETRYING→SCHEDULED CAS: a concurrent cancel may have
// SKIPPED the task since the snapshot above was taken, and re-submitting it
// would resurrect a terminal task; applied=false means someone else moved the
// task and this loop must leave it alone.
func (l *Loop) resubmitRetrying(ctx context.Context, affected map[string]bool) error {
	// Reclaim stranded claims first: a crash between the RETRYING→SCHEDULED
	// claim below and the submit persist leaves a SCHEDULED row that no other
	// phase scans. The scheduler is single-goroutine, so any SCHEDULED task
	// observed at the start of this phase is such a stranded claim — sweep it
	// back to RETRYING so the normal path below reclaims it this tick.
	stranded, err := l.store.GetTasksByState(ctx, model.TaskStateScheduled)
	if err != nil {
		return err
	}
	for _, task := range stranded {
		applied, err := l.store.CASTaskState(ctx, task.ID, model.TaskStateScheduled, model.TaskStateRetrying)
		if err != nil {
			l.logger.Error("reclaim stranded scheduled task", "task_id", task.ID, "error", err)
			continue
		}
		if applied {
			l.logger.Warn("reclaimed stranded SCHEDULED task (crash mid-resubmit)", "task_id", task.ID)
		}
	}

	retrying, err := l.store.GetTasksByState(ctx, model.TaskStateRetrying)
	if err != nil {
		return err
	}

	for _, task := range retrying {
		applied, err := l.store.CASTaskState(ctx, task.ID, model.TaskStateRetrying, model.TaskStateScheduled)
		if err != nil {
			l.logger.Error("claim retrying task", "task_id", task.ID, "error", err)
			continue
		}
		if !applied {
			l.logger.Info("retry resubmit skipped: task no longer RETRYING", "task_id", task.ID)
			continue
		}

		task.State = model.TaskStateScheduled
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
			// Sub-workflow proxy tasks pair 1:1 with child submissions and
			// advance from the child's state, not from an executor backend —
			// intercept them before the registry lookup. [F8]
			if task.ExecutorType == model.ExecutorTypeSubworkflow {
				l.pollSubworkflowTask(ctx, task, affected)
				continue
			}
			// QUEUED worker tasks change state only via server handlers
			// (checkout → RUNNING, complete → terminal): polling them here
			// races the checkout transaction — the stale snapshot write was
			// the F-J zombie (external_id clobbered, task invisible to
			// Requeue/ReconcileWorkerTasks). They stay in the RUNNING scan so
			// worker-reported terminal states are still collected.
			if state == model.TaskStateQueued && task.ExecutorType == model.ExecutorTypeWorker {
				continue
			}
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

			if newState.IsTerminal() {
				task.State = newState
				now := time.Now().UTC()
				task.CompletedAt = &now
				stdout, stderr, _ := exec.Logs(ctx, task)
				task.Stdout = stdout
				task.Stderr = stderr
				scrubTaskToken(task)
				l.logger.Info("task completed (poll)", "task_id", task.ID, "state", newState)

				// CAS write: a concurrent cancel may have already terminalized
				// this task (SKIPPED); the poll result must not overwrite it.
				applied, err := l.store.TerminalizeTask(ctx, task)
				if err != nil {
					l.logger.Error("terminalize polled task", "task_id", task.ID, "error", err)
					continue
				}
				if !applied {
					l.logger.Info("task reached a terminal state concurrently, leaving as-is",
						"task_id", task.ID, "polled_state", newState)
				}
				affected[task.SubmissionID] = true
				continue
			}

			// Non-terminal observations persist through narrow CAS writes only —
			// never a full-row UpdateTask, which would clobber concurrent
			// handler writes (external_id, started_at; the F-J zombie).
			if task.State == model.TaskStateQueued && newState == model.TaskStateRunning {
				applied, err := l.store.MarkTaskRunning(ctx, task.ID)
				if err != nil {
					l.logger.Error("mark polled task running", "task_id", task.ID, "error", err)
					continue
				}
				if !applied {
					// The task left QUEUED concurrently (checkout, cancel SKIP,
					// worker report) — the winning write stands.
					l.logger.Debug("task no longer QUEUED, leaving as-is",
						"task_id", task.ID, "polled_state", newState)
					continue
				}
				affected[task.SubmissionID] = true
				continue
			}

			// Any other non-terminal observation — e.g. a transient bvbrc
			// RUNNING→QUEUED regression — is deliberately NOT persisted:
			// MarkTaskRunning cannot express it, and rewriting the row from a
			// stale snapshot is exactly the F-J clobber. The platform state is
			// re-observed next tick. [N1]
			l.logger.Debug("ignoring non-terminal state observation",
				"task_id", task.ID, "state", task.State, "polled_state", newState)
		}
	}

	return nil
}

// pollSubworkflowTask advances an in-flight sub-workflow proxy task from its
// child submission's state:
//
//	(a) reconciliation — the proxy's own submission or step instance is
//	    already terminal: cancel the child, retire the proxy as SKIPPED, and
//	    conditionally SKIP the step. This is the ONLY path that cancels a
//	    proxy: CancelNonTerminalTasks deliberately excludes subworkflow
//	    proxies so a parent cancel leaves them RUNNING (in this scan) until
//	    the cascade has reached their children — nested sub-workflows thus
//	    cancel one level per tick with no explicit recursion [F2,M2]
//	(b) repair — no child exists (crash between the dispatch transaction and
//	    child creation): recreate it deterministically from task.Job [F4]
//	(c) advancement — child COMPLETED → proxy SUCCESS with the child's
//	    outputs; child FAILED or CANCELLED → proxy FAILED with the child's
//	    error in Stderr. CANCELLED maps to FAILED (not SKIPPED) because
//	    advanceSteps sets anyFailed only for FAILED tasks — a SKIPPED proxy
//	    would let the parent COMPLETE with a null hole in the gathered
//	    array. [F1,F9]
//
// All child reads go through GetChildSubmissions, which is uncached — the
// tickCache could hold a pre-cancel snapshot. [F11]
func (l *Loop) pollSubworkflowTask(ctx context.Context, task *model.Task, affected map[string]bool) {
	// Fresh reads (not tickCache): reconciliation exists to observe cancels
	// that landed after this tick's cache was primed.
	sub, err := l.store.GetSubmission(ctx, task.SubmissionID)
	if err != nil {
		l.logger.Error("get submission for subworkflow poll", "task_id", task.ID, "error", err)
		return
	}
	si, err := l.store.GetStepInstance(ctx, task.StepInstanceID)
	if err != nil {
		l.logger.Error("get step instance for subworkflow poll", "task_id", task.ID, "error", err)
		return
	}

	// (a) Reconciliation.
	if sub == nil || sub.State.IsTerminal() || si == nil || si.State.IsTerminal() {
		children, err := l.store.GetChildSubmissions(ctx, task.ID)
		if err != nil {
			// Strict scan: a corrupt child row surfaces as an error. Skip the
			// task this tick (no terminalization) — the proxy stays RUNNING
			// and is retried next tick.
			l.logger.Error("list children for subworkflow reconcile", "task_id", task.ID, "error", err)
			return
		}
		for _, child := range children {
			if !child.State.IsTerminal() {
				l.cancelChildSubmission(ctx, child)
			}
		}
		now := time.Now().UTC()
		task.State = model.TaskStateSkipped
		task.CompletedAt = &now
		if _, err := l.store.TerminalizeTask(ctx, task); err != nil {
			l.logger.Error("skip orphaned subworkflow proxy", "task_id", task.ID, "error", err)
			return
		}
		if si != nil && !si.State.IsTerminal() {
			l.skipStepInstanceIfActive(ctx, si.ID)
		}
		l.logger.Info("subworkflow proxy reconciled (parent terminal)",
			"task_id", task.ID, "submission_id", task.SubmissionID)
		affected[task.SubmissionID] = true
		return
	}

	children, err := l.store.GetChildSubmissions(ctx, task.ID)
	if err != nil {
		// Strict scan: "child exists but its row is corrupt" surfaces as an
		// error, NOT as an empty list — running the repair below on it would
		// create a duplicate child. Skip the task this tick.
		l.logger.Error("list children for subworkflow poll", "task_id", task.ID, "error", err)
		return
	}

	// (b) Repair.
	if len(children) == 0 {
		l.repairSubworkflowChild(ctx, task, sub, si, affected)
		return
	}

	// (c) Advancement.
	child := children[0]
	switch child.State {
	case model.SubmissionStateCompleted:
		task.State = model.TaskStateSuccess
		task.Outputs = child.Outputs
	case model.SubmissionStateFailed:
		task.State = model.TaskStateFailed
		task.Stderr = formatChildError(child)
	case model.SubmissionStateCancelled:
		task.State = model.TaskStateFailed
		task.Stderr = fmt.Sprintf("child submission %s cancelled", child.ID)
	default:
		return // Child still in flight — poll again next tick.
	}
	now := time.Now().UTC()
	task.CompletedAt = &now
	// CAS write: a concurrent cancel may have already terminalized this proxy
	// (SKIPPED); the child's result must not overwrite it. [F3]
	applied, err := l.store.TerminalizeTask(ctx, task)
	if err != nil {
		l.logger.Error("terminalize subworkflow proxy", "task_id", task.ID, "error", err)
		return
	}
	if !applied {
		l.logger.Info("subworkflow proxy reached a terminal state concurrently, leaving as-is",
			"task_id", task.ID)
	} else {
		l.logger.Info("subworkflow proxy advanced",
			"task_id", task.ID, "state", task.State, "child_id", child.ID)
	}
	affected[task.SubmissionID] = true
}

// repairSubworkflowChild recreates a missing child submission from its
// persisted proxy: the dispatch crashed after the task transaction but before
// child creation. task.Job already holds the fully-resolved combination, so
// the repair re-parses the sub-workflow graph but never re-runs scatter
// combination or valueFrom/when JavaScript (deterministic recovery). [F4]
func (l *Loop) repairSubworkflowChild(ctx context.Context, task *model.Task,
	sub *model.Submission, si *model.StepInstance, affected map[string]bool) {

	fail := func(reason string) {
		// Same policy as dispatch-time child-creation failure. [F11]
		l.failSubworkflowProxy(ctx, task, si, reason)
		affected[task.SubmissionID] = true
	}

	wf, err := l.cache.getWorkflow(ctx, l.store, sub.WorkflowID)
	if err != nil || wf == nil {
		l.logger.Error("get workflow for subworkflow repair", "task_id", task.ID, "error", err)
		return
	}
	step := findStep(wf, task.StepID)
	if step == nil {
		fail(fmt.Sprintf("repair child submission: step %s not found in workflow %s", task.StepID, wf.ID))
		return
	}
	graphDoc, err := parser.New(l.logger).ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		fail(fmt.Sprintf("repair child submission: parse parent CWL: %v", err))
		return
	}
	subGraph := graphDoc.SubWorkflows[step.ToolRef]
	if subGraph == nil {
		fail(fmt.Sprintf("repair child submission: sub-workflow %q not found", step.ToolRef))
		return
	}
	child, err := l.createChildSubmission(ctx, task, subGraph, task.Job, sub, wf)
	if err != nil {
		fail(fmt.Sprintf("repair child submission: %v", err))
		return
	}
	l.logger.Info("subworkflow child repaired", "task_id", task.ID, "child_id", child.ID)
	affected[task.SubmissionID] = true
}

// formatChildError renders a failed child submission's error for the proxy
// task's Stderr, so the parent's buildSubmissionError surfaces the child's
// failure detail. [F9]
func formatChildError(child *model.Submission) string {
	if child.Error == nil {
		return fmt.Sprintf("child submission %s failed", child.ID)
	}
	msg := fmt.Sprintf("child submission %s failed: %s: %s", child.ID, child.Error.Code, child.Error.Message)
	if child.Error.Context != nil && child.Error.Context.Stderr != "" {
		msg += "\n" + child.Error.Context.Stderr
	}
	return msg
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
			// CAS write: oldest was snapshotted as QUEUED, but a worker may
			// have checked it out (QUEUED->RUNNING with a new external_id)
			// between the snapshot and this write, or a concurrent cancel
			// may have already terminalized it (SKIPPED). TerminalizeTask's
			// "not already terminal" guard would still let this stale
			// full-row write through against a checked-out RUNNING task,
			// clobbering the worker's external_id; require the task to
			// still be exactly QUEUED instead.
			applied, err := l.store.TerminalizeTaskFrom(ctx, oldest, model.TaskStateQueued)
			if err != nil {
				l.logger.Error("fail stuck task", "task_id", oldest.ID, "error", err)
			} else if !applied {
				l.logger.Debug("stuck task no longer queued, leaving",
					"task_id", oldest.ID)
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

	if caps.OnlineCount == 0 {
		b.WriteString("Available workers: 0 online\n")
	} else {
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
			anySkipped := false
			for _, t := range tasks {
				if t.State == model.TaskStateFailed && t.RetryCount < t.MaxRetries {
					// Task will be retried — treat as non-terminal.
					allTerminal = false
					continue
				}
				if !t.State.IsTerminal() {
					allTerminal = false
					if t.State == model.TaskStateRunning {
						anyRunning = true
					}
				}
				if t.State == model.TaskStateFailed {
					anyFailed = true
				}
				if t.State == model.TaskStateSkipped {
					anySkipped = true
				}
			}

			if allTerminal && si.ScatterCount > 0 {
				// Scatter fan-in requires the full index set: a crash during
				// legacy serial task creation can leave fewer task rows than
				// ScatterCount, and completing over the gap would emit an
				// undersized output array. Duplicate rows (same index twice)
				// are harmless — the results slice below is sized by
				// ScatterCount, never len(tasks). [F5]
				distinct := make(map[int]bool)
				for _, t := range tasks {
					if t.ScatterIndex >= 0 && t.ScatterIndex < si.ScatterCount {
						distinct[t.ScatterIndex] = true
					}
				}
				if len(distinct) < si.ScatterCount {
					l.logger.Warn("scatter fan-in waiting: incomplete task index set",
						"si_id", si.ID, "have", len(distinct), "want", si.ScatterCount)
					continue
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
			} else if anySkipped {
				// SKIPPED tasks arise only from cancellation
				// (CancelNonTerminalTasks or subworkflow reconciliation) —
				// when-skip synthetics are terminal SUCCESS, never SKIPPED.
				// Completing over a SKIPPED task would produce a COMPLETED
				// step under a cancelled submission (with null holes, for
				// scatter). Mirror the cancel and skip the step, scatter or
				// not. [M2]
				si.State = model.StepStateSkipped
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
								results := make([]map[string]any, si.ScatterCount)
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
		subs, err := l.listSubmissionsByState(ctx, state)
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
			if applied, err := l.finalizeSubmissionCAS(ctx, sub); err != nil {
				l.logger.Error("finalize submission", "submission_id", subID, "error", err)
			} else if !applied {
				// Lost the race to a concurrent terminal write (e.g. a
				// cancel): leave the winning state alone.
				l.logger.Info("finalize skipped: submission already terminal", "submission_id", subID)
			} else {
				l.logger.Info("submission finalized", "submission_id", subID, "state", sub.State)
			}
		} else if (anyActive || anyFailed) && sub.State == model.SubmissionStatePending {
			if applied, err := l.activateSubmissionCAS(ctx, sub.ID); err != nil {
				l.logger.Error("activate submission", "submission_id", subID, "error", err)
			} else if applied {
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
		// CAS write: only flip FAILED→RETRYING while the task is still FAILED
		// — a concurrent cancel may have SKIPPED it since the list above was
		// taken, and a retry must not resurrect a terminal task.
		applied, err := l.store.CASTaskState(ctx, task.ID, model.TaskStateFailed, model.TaskStateRetrying)
		if err != nil {
			l.logger.Error("mark retrying", "task_id", task.ID, "error", err)
			continue
		}
		if !applied {
			l.logger.Info("retry skipped: task no longer FAILED", "task_id", task.ID)
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
