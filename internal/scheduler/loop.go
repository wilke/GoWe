package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Config holds scheduler configuration.
type Config struct {
	PollInterval time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{PollInterval: 2 * time.Second}
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

// Tick runs a single scheduling iteration.
func (l *Loop) Tick(ctx context.Context) error {
	affected := make(map[string]bool) // submissionIDs touched this tick

	// Phase 1: Advance PENDING tasks whose dependencies are satisfied.
	if err := l.advancePending(ctx, affected); err != nil {
		return fmt.Errorf("phase 1 (pending): %w", err)
	}

	// Phase 2: Dispatch SCHEDULED tasks to executors.
	if err := l.dispatchScheduled(ctx, affected); err != nil {
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

	// Phase 4: Finalize submissions where all tasks are terminal.
	if err := l.finalizeSubmissions(ctx, affected); err != nil {
		return fmt.Errorf("phase 4 (finalize): %w", err)
	}

	// Phase 5: Transition newly-FAILED tasks to RETRYING if retries remain.
	if err := l.markRetries(ctx, affected); err != nil {
		return fmt.Errorf("phase 5 (retries): %w", err)
	}

	return nil
}

// advancePending transitions PENDING tasks to SCHEDULED (deps met) or SKIPPED (blocked).
func (l *Loop) advancePending(ctx context.Context, affected map[string]bool) error {
	pending, err := l.store.GetTasksByState(ctx, model.TaskStatePending)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	// Group by submission to load sibling tasks once per submission.
	bySubmission := groupBySubmission(pending)

	for subID, tasks := range bySubmission {
		allTasks, err := l.store.ListTasksBySubmission(ctx, subID)
		if err != nil {
			l.logger.Error("list tasks for submission", "submission_id", subID, "error", err)
			continue
		}
		tasksByStep := BuildTasksByStepID(allTasks)

		for _, task := range tasks {
			satisfied, blocked := AreDependenciesSatisfied(task, tasksByStep)

			if blocked {
				now := time.Now().UTC()
				task.State = model.TaskStateSkipped
				task.CompletedAt = &now
				if err := l.store.UpdateTask(ctx, task); err != nil {
					l.logger.Error("skip task", "task_id", task.ID, "error", err)
					continue
				}
				l.logger.Info("task skipped (dependency blocked)", "task_id", task.ID, "step_id", task.StepID)
				affected[subID] = true
			} else if satisfied {
				task.State = model.TaskStateScheduled
				if err := l.store.UpdateTask(ctx, task); err != nil {
					l.logger.Error("schedule task", "task_id", task.ID, "error", err)
					continue
				}
				l.logger.Debug("task scheduled", "task_id", task.ID, "step_id", task.StepID)
				affected[subID] = true
			}
		}
	}

	return nil
}

// dispatchScheduled resolves inputs, submits to executors, and records the result.
func (l *Loop) dispatchScheduled(ctx context.Context, affected map[string]bool) error {
	scheduled, err := l.store.GetTasksByState(ctx, model.TaskStateScheduled)
	if err != nil {
		return err
	}

	for _, task := range scheduled {
		if err := l.submitTask(ctx, task); err != nil {
			l.logger.Error("submit task", "task_id", task.ID, "error", err)
		}
		affected[task.SubmissionID] = true
	}

	return nil
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

		if err := l.submitTask(ctx, task); err != nil {
			l.logger.Error("retry submit", "task_id", task.ID, "error", err)
		}
		affected[task.SubmissionID] = true
	}

	return nil
}

// submitTask resolves inputs, calls executor.Submit, checks status, and persists.
func (l *Loop) submitTask(ctx context.Context, task *model.Task) error {
	// Load submission for workflow inputs.
	sub, err := l.store.GetSubmission(ctx, task.SubmissionID)
	if err != nil {
		return fmt.Errorf("get submission %s: %w", task.SubmissionID, err)
	}
	if sub == nil {
		return fmt.Errorf("submission %s not found", task.SubmissionID)
	}

	// Check token expiry before task execution.
	if !sub.TokenExpiry.IsZero() && time.Now().After(sub.TokenExpiry) {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = "user token expired before task execution"
		task.CompletedAt = &now
		l.logger.Warn("task failed due to token expiry", "task_id", task.ID, "submission_id", sub.ID)
		return l.store.UpdateTask(ctx, task)
	}

	// Load workflow for step definitions.
	wf, err := l.store.GetWorkflow(ctx, sub.WorkflowID)
	if err != nil {
		return fmt.Errorf("get workflow %s: %w", sub.WorkflowID, err)
	}
	if wf == nil {
		return fmt.Errorf("workflow %s not found", sub.WorkflowID)
	}

	// Find the step for this task.
	step := findStep(wf, task.StepID)
	if step == nil {
		return fmt.Errorf("step %s not found in workflow %s", task.StepID, wf.ID)
	}

	// Load sibling tasks for upstream output resolution.
	allTasks, err := l.store.ListTasksBySubmission(ctx, task.SubmissionID)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	tasksByStep := BuildTasksByStepID(allTasks)

	// For worker executor tasks, populate Tool and Job fields from the CWL.
	if task.ExecutorType == model.ExecutorTypeWorker {
		if err := l.populateToolAndJob(task, step, wf, sub.Inputs, tasksByStep); err != nil {
			l.logger.Warn("failed to populate Tool/Job, falling back to legacy mode",
				"task_id", task.ID, "error", err)
		}
	}

	// Resolve inputs (sets _base_command, _output_globs, and real inputs).
	// This is still needed for backward compatibility and non-worker executors.
	// Note: expressionLib is nil here; workflow's InlineJavascriptRequirement could be passed for advanced expressions.
	if err := ResolveTaskInputs(task, step, sub.Inputs, tasksByStep, nil); err != nil {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = err.Error()
		task.CompletedAt = &now
		if updateErr := l.store.UpdateTask(ctx, task); updateErr != nil {
			l.logger.Error("update failed task", "task_id", task.ID, "error", updateErr)
		}
		return fmt.Errorf("resolve inputs for task %s: %w", task.ID, err)
	}

	// Add user token to RuntimeHints for executor/worker.
	// This allows BVBRCExecutor and workers to authenticate with the user's credentials.
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

	// Get the executor.
	exec, err := l.registry.Get(task.ExecutorType)
	if err != nil {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = err.Error()
		task.CompletedAt = &now
		if updateErr := l.store.UpdateTask(ctx, task); updateErr != nil {
			l.logger.Error("update failed task", "task_id", task.ID, "error", updateErr)
		}
		return fmt.Errorf("get executor for task %s: %w", task.ID, err)
	}

	// Submit to executor (synchronous for LocalExecutor).
	now := time.Now().UTC()
	task.StartedAt = &now
	externalID, submitErr := exec.Submit(ctx, task)
	task.ExternalID = externalID

	if submitErr != nil {
		// Submit itself failed (e.g., command not found).
		task.State = model.TaskStateFailed
		task.Stderr = submitErr.Error()
		completedAt := time.Now().UTC()
		task.CompletedAt = &completedAt
		l.logger.Info("task failed (submit error)", "task_id", task.ID, "error", submitErr)
	} else {
		// Check immediate status (synchronous executors complete within Submit).
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

	return l.store.UpdateTask(ctx, task)
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

// finalizeSubmissions updates submission state based on task states.
func (l *Loop) finalizeSubmissions(ctx context.Context, affected map[string]bool) error {
	// Also check all RUNNING submissions in case tasks were completed via worker HTTP API.
	runningSubs, _, err := l.store.ListSubmissions(ctx, model.ListOptions{State: "RUNNING", Limit: 100})
	if err != nil {
		l.logger.Error("list running submissions for finalize", "error", err)
	} else {
		for _, sub := range runningSubs {
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

		allTerminal := true
		anyFailed := false
		anyActive := false

		for i := range sub.Tasks {
			t := &sub.Tasks[i]
			if !t.State.IsTerminal() {
				allTerminal = false
				if t.State != model.TaskStatePending {
					anyActive = true
				}
			}
			if t.State == model.TaskStateFailed {
				anyFailed = true
			}
		}

		if allTerminal {
			if anyFailed {
				sub.State = model.SubmissionStateFailed
			} else {
				sub.State = model.SubmissionStateCompleted
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

// findStep returns the Step with the given ID from the workflow, or nil.
func findStep(wf *model.Workflow, stepID string) *model.Step {
	for i := range wf.Steps {
		if wf.Steps[i].ID == stepID {
			return &wf.Steps[i]
		}
	}
	return nil
}

// groupBySubmission organizes tasks into a map keyed by SubmissionID.
func groupBySubmission(tasks []*model.Task) map[string][]*model.Task {
	m := make(map[string][]*model.Task)
	for _, t := range tasks {
		m[t.SubmissionID] = append(m[t.SubmissionID], t)
	}
	return m
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
	var toolID string
	if step.ToolRef != "" {
		toolID = strings.TrimPrefix(step.ToolRef, "#")
		toolID = strings.TrimSuffix(toolID, ".cwl")
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

	if tool == nil {
		return fmt.Errorf("tool %q not found in parsed workflow", toolID)
	}

	// Resolve job inputs (same logic as ResolveTaskInputs but store in Job).
	job := make(map[string]any)
	for _, si := range step.In {
		if si.Source == "" {
			continue
		}

		if strings.Contains(si.Source, "/") {
			// Upstream task output: "stepID/outputID"
			parts := strings.SplitN(si.Source, "/", 2)
			stepID, outputID := parts[0], parts[1]

			depTask, exists := tasksByStepID[stepID]
			if !exists {
				return fmt.Errorf("upstream step %q not found", stepID)
			}

			val, exists := depTask.Outputs[outputID]
			if !exists {
				if depTask.State == model.TaskStateSuccess && len(depTask.Outputs) == 0 {
					job[si.ID] = nil
					continue
				}
				return fmt.Errorf("output %q not found on step %q", outputID, stepID)
			}
			job[si.ID] = val
		} else {
			// Workflow-level input.
			val, exists := submissionInputs[si.Source]
			if !exists {
				return fmt.Errorf("workflow input %q not found", si.Source)
			}
			job[si.ID] = val
		}
	}

	task.Tool = tool
	task.Job = job
	task.RuntimeHints = runtimeHints

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
				}
			}
			if ram, ok := resReq["ramMin"]; ok {
				switch v := ram.(type) {
				case int:
					hints.RamMB = int64(v)
				case float64:
					hints.RamMB = int64(v)
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
