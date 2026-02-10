package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
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
func (l *Loop) Stop() error {
	close(l.stopCh)
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

	// Resolve inputs (sets _base_command, _output_globs, and real inputs).
	if err := ResolveTaskInputs(task, step, sub.Inputs, tasksByStep); err != nil {
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = err.Error()
		task.CompletedAt = &now
		if updateErr := l.store.UpdateTask(ctx, task); updateErr != nil {
			l.logger.Error("update failed task", "task_id", task.ID, "error", updateErr)
		}
		return fmt.Errorf("resolve inputs for task %s: %w", task.ID, err)
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
