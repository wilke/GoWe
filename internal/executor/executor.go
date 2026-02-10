package executor

import (
	"context"

	"github.com/me/gowe/pkg/model"
)

// Executor is a pluggable backend that runs Tasks.
type Executor interface {
	// Type returns the executor type identifier.
	Type() model.ExecutorType

	// Submit sends a task to the execution backend and returns an external ID.
	Submit(ctx context.Context, task *model.Task) (externalID string, err error)

	// Status checks the current state of a submitted task.
	Status(ctx context.Context, task *model.Task) (model.TaskState, error)

	// Cancel requests cancellation of a running task.
	Cancel(ctx context.Context, task *model.Task) error

	// Logs retrieves stdout and stderr for a task.
	Logs(ctx context.Context, task *model.Task) (stdout, stderr string, err error)
}
