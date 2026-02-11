package store

import (
	"context"

	"github.com/me/gowe/pkg/model"
)

// Store defines the persistence layer for GoWe entities.
type Store interface {
	// Workflow CRUD
	CreateWorkflow(ctx context.Context, wf *model.Workflow) error
	GetWorkflow(ctx context.Context, id string) (*model.Workflow, error)
	GetWorkflowByHash(ctx context.Context, hash string) (*model.Workflow, error)
	ListWorkflows(ctx context.Context, opts model.ListOptions) ([]*model.Workflow, int, error)
	UpdateWorkflow(ctx context.Context, wf *model.Workflow) error
	DeleteWorkflow(ctx context.Context, id string) error

	// Submission CRUD
	CreateSubmission(ctx context.Context, sub *model.Submission) error
	GetSubmission(ctx context.Context, id string) (*model.Submission, error)
	ListSubmissions(ctx context.Context, opts model.ListOptions) ([]*model.Submission, int, error)
	UpdateSubmission(ctx context.Context, sub *model.Submission) error

	// Task operations
	CreateTask(ctx context.Context, task *model.Task) error
	GetTask(ctx context.Context, id string) (*model.Task, error)
	ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error)
	UpdateTask(ctx context.Context, task *model.Task) error
	GetTasksByState(ctx context.Context, state model.TaskState) ([]*model.Task, error)

	// Lifecycle
	Close() error
	Migrate(ctx context.Context) error
}
