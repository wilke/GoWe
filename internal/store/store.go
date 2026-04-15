package store

import (
	"context"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Store defines the persistence layer for GoWe entities.
type Store interface {
	// Workflow CRUD
	CreateWorkflow(ctx context.Context, wf *model.Workflow) error
	GetWorkflow(ctx context.Context, id string) (*model.Workflow, error)
	GetWorkflowByHash(ctx context.Context, hash string) (*model.Workflow, error)
	GetWorkflowByName(ctx context.Context, name string) (*model.Workflow, error)
	ListWorkflows(ctx context.Context, opts model.ListOptions) ([]*model.Workflow, int, error)
	UpdateWorkflow(ctx context.Context, wf *model.Workflow) error
	DeleteWorkflow(ctx context.Context, id string) error

	// Submission CRUD
	CreateSubmission(ctx context.Context, sub *model.Submission) error
	GetSubmission(ctx context.Context, id string) (*model.Submission, error)
	ListSubmissions(ctx context.Context, opts model.ListOptions) ([]*model.Submission, int, error)
	UpdateSubmission(ctx context.Context, sub *model.Submission) error
	UpdateSubmissionInputs(ctx context.Context, id string, inputs map[string]any) error
	GetChildSubmissions(ctx context.Context, parentTaskID string) ([]*model.Submission, error)
	CountSubmissionsByState(ctx context.Context, since time.Time, submittedBy string) (map[string]int, error)

	// StepInstance operations
	CreateStepInstance(ctx context.Context, si *model.StepInstance) error
	BatchCreateStepInstances(ctx context.Context, steps []*model.StepInstance) error
	GetStepInstance(ctx context.Context, id string) (*model.StepInstance, error)
	UpdateStepInstance(ctx context.Context, si *model.StepInstance) error
	ListStepsBySubmission(ctx context.Context, submissionID string) ([]*model.StepInstance, error)
	ListStepsByState(ctx context.Context, state model.StepInstanceState) ([]*model.StepInstance, error)
	CancelNonTerminalSteps(ctx context.Context, submissionID string, completedAt time.Time) (int, error)

	// Task operations
	CreateTask(ctx context.Context, task *model.Task) error
	GetTask(ctx context.Context, id string) (*model.Task, error)
	ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error)
	ListTasksBySubmissionPaged(ctx context.Context, submissionID string, opts model.ListOptions) ([]*model.Task, int, error)
	ListTasksByStepInstance(ctx context.Context, stepInstanceID string) ([]*model.Task, error)
	UpdateTask(ctx context.Context, task *model.Task) error
	GetTasksByState(ctx context.Context, state model.TaskState) ([]*model.Task, error)
	GetActiveTasks(ctx context.Context) ([]*model.Task, error)
	GetTaskSummaries(ctx context.Context, submissionIDs []string) (map[string]model.TaskSummary, error)
	CancelNonTerminalTasks(ctx context.Context, submissionID string, completedAt time.Time) (int, error)
	ResetFailedTasks(ctx context.Context, submissionID string) (int, error)
	ResetFailedSteps(ctx context.Context, submissionID string) (int, error)

	// Session operations
	CreateSession(ctx context.Context, sess *model.Session) error
	GetSession(ctx context.Context, id string) (*model.Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context) (int64, error)
	DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error)

	// Worker operations
	CreateWorker(ctx context.Context, w *model.Worker) error
	GetWorker(ctx context.Context, id string) (*model.Worker, error)
	UpdateWorker(ctx context.Context, w *model.Worker) error
	DeleteWorker(ctx context.Context, id string) error
	ListWorkers(ctx context.Context) ([]*model.Worker, error)
	CheckoutTask(ctx context.Context, workerID string, workerGroup string, runtime model.ContainerRuntime) (*model.Task, error)
	MarkStaleWorkersOffline(ctx context.Context, timeout time.Duration) ([]*model.Worker, error)
	RequeueWorkerTasks(ctx context.Context, workerID string) (int, error)

	// User operations
	GetUser(ctx context.Context, username string) (*model.User, error)
	GetOrCreateUser(ctx context.Context, username string, provider model.AuthProvider) (*model.User, error)
	UpdateUser(ctx context.Context, user *model.User) error
	ListUsers(ctx context.Context) ([]*model.User, error)
	LinkProvider(ctx context.Context, userID string, provider model.AuthProvider, username string) error

	// Label Vocabulary
	CreateLabelVocabulary(ctx context.Context, lv *model.LabelVocabulary) error
	ListLabelVocabulary(ctx context.Context) ([]*model.LabelVocabulary, error)
	DeleteLabelVocabulary(ctx context.Context, id string) error

	// Lifecycle
	Close() error
	Migrate(ctx context.Context) error
}
