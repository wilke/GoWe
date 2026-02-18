package executor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TaskReader provides read access to tasks for the WorkerExecutor.
// This avoids importing the full store package.
type TaskReader interface {
	GetTask(ctx context.Context, id string) (*model.Task, error)
	UpdateTask(ctx context.Context, task *model.Task) error
}

// WorkerExecutor is a thin server-side executor for remote workers.
// Submit enqueues tasks to QUEUED state; workers pull them via the API
// and report status/completion back. Status reads the current task state
// from the store (workers push updates via HTTP).
type WorkerExecutor struct {
	store  TaskReader
	logger *slog.Logger
}

// NewWorkerExecutor creates a WorkerExecutor.
func NewWorkerExecutor(store TaskReader, logger *slog.Logger) *WorkerExecutor {
	return &WorkerExecutor{
		store:  store,
		logger: logger.With("component", "worker-executor"),
	}
}

// Type returns model.ExecutorTypeWorker.
func (e *WorkerExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeWorker
}

// Submit transitions the task to QUEUED. Workers will pick it up via the
// checkout API. Returns the task ID as the externalID.
func (e *WorkerExecutor) Submit(_ context.Context, task *model.Task) (string, error) {
	e.logger.Debug("task enqueued for worker pickup",
		"task_id", task.ID,
	)
	// The scheduler will transition SCHEDULED → QUEUED. We just return the
	// task ID as the external reference. The actual execution happens when
	// a worker checks out and runs the task.
	return task.ID, nil
}

// Status reads the fresh task state from the store. Workers update state
// via the API, so we just return whatever is persisted.
func (e *WorkerExecutor) Status(ctx context.Context, task *model.Task) (model.TaskState, error) {
	fresh, err := e.store.GetTask(ctx, task.ID)
	if err != nil {
		return "", fmt.Errorf("worker executor: get task %s: %w", task.ID, err)
	}
	if fresh == nil {
		return "", fmt.Errorf("worker executor: task %s not found", task.ID)
	}

	// Copy worker-updated fields back to the caller's task.
	task.Stdout = fresh.Stdout
	task.Stderr = fresh.Stderr
	task.ExitCode = fresh.ExitCode
	task.Outputs = fresh.Outputs
	task.CompletedAt = fresh.CompletedAt

	return fresh.State, nil
}

// Cancel marks the task as FAILED. The worker will see this on next heartbeat.
func (e *WorkerExecutor) Cancel(ctx context.Context, task *model.Task) error {
	fresh, err := e.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("worker executor: get task %s: %w", task.ID, err)
	}
	if fresh == nil {
		return fmt.Errorf("worker executor: task %s not found", task.ID)
	}

	if fresh.State.IsTerminal() {
		return nil // Already done
	}

	fresh.State = model.TaskStateFailed
	now := time.Now().UTC()
	fresh.CompletedAt = &now
	fresh.Stderr = "cancelled by server"

	return e.store.UpdateTask(ctx, fresh)
}

// Logs returns the stdout and stderr stored on the task.
func (e *WorkerExecutor) Logs(ctx context.Context, task *model.Task) (string, string, error) {
	fresh, err := e.store.GetTask(ctx, task.ID)
	if err != nil {
		return "", "", fmt.Errorf("worker executor: get task %s: %w", task.ID, err)
	}
	if fresh == nil {
		return task.Stdout, task.Stderr, nil
	}
	return fresh.Stdout, fresh.Stderr, nil
}
