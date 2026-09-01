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
	// CreateSubmissionWithSteps creates a submission and all its step
	// instances in a single transaction (all-or-nothing): a failure mid-batch
	// leaves no submission row behind, closing the zero-step crash window in
	// child-submission creation.
	CreateSubmissionWithSteps(ctx context.Context, sub *model.Submission, steps []*model.StepInstance) error
	GetSubmission(ctx context.Context, id string) (*model.Submission, error)
	ListSubmissions(ctx context.Context, opts model.ListOptions) ([]*model.Submission, int, error)
	UpdateSubmission(ctx context.Context, sub *model.Submission) error
	// FinalizeSubmission is UpdateSubmission guarded by a compare-and-set:
	// the write is skipped (applied=false, no error) when the submission is
	// already terminal.
	FinalizeSubmission(ctx context.Context, sub *model.Submission) (bool, error)
	// UpdateSubmissionIfState is UpdateSubmission guarded by a compare-and-set
	// on (state, output_state): the write is skipped (applied=false, no
	// error) unless the row still holds exactly expectState and
	// expectOutputState, so a caller that loaded the submission earlier can
	// never overwrite a concurrent transition.
	UpdateSubmissionIfState(ctx context.Context, sub *model.Submission, expectState model.SubmissionState, expectOutputState string) (bool, error)
	// ActivateSubmission moves a submission PENDING→RUNNING; applied=false
	// (no error) when it is no longer PENDING.
	ActivateSubmission(ctx context.Context, id string) (bool, error)
	// ListSubmissionsAwaitingOutputStaging returns COMPLETED submissions with
	// an output destination and no recorded delivery outcome (SQL-filtered so
	// the post-stage phase's cost is bounded by pending deliveries).
	ListSubmissionsAwaitingOutputStaging(ctx context.Context) ([]*model.Submission, error)
	DeleteSubmission(ctx context.Context, id string) error
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
	// CreateTasksAndDispatchStep creates every task of a dispatched step and
	// persists the step instance's new state in a single transaction
	// (all-or-nothing): a failure mid-batch leaves no tasks behind and the
	// step instance untouched.
	CreateTasksAndDispatchStep(ctx context.Context, tasks []*model.Task, si *model.StepInstance) error
	GetTask(ctx context.Context, id string) (*model.Task, error)
	ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error)
	ListTasksBySubmissionPaged(ctx context.Context, submissionID string, opts model.ListOptions) ([]*model.Task, int, error)
	ListTasksByStepInstance(ctx context.Context, stepInstanceID string) ([]*model.Task, error)
	UpdateTask(ctx context.Context, task *model.Task) error
	// TerminalizeTask is UpdateTask guarded by a compare-and-set: the write is
	// skipped (applied=false, no error) when the task is already terminal.
	TerminalizeTask(ctx context.Context, task *model.Task) (bool, error)
	// CASTaskState moves a task `from`→`to` only while it is still exactly in
	// `from`; applied=false (no error) otherwise, so a stale snapshot can
	// never overwrite a concurrent terminal write (e.g. a cancel that SKIPPED
	// the task). Used for retry marking (FAILED→RETRYING) and retry claiming
	// (RETRYING→SCHEDULED).
	CASTaskState(ctx context.Context, id string, from, to model.TaskState) (bool, error)
	// MarkTaskRunning transitions a task QUEUED→RUNNING (compare-and-set),
	// stamping started_at only when it is not already set. It writes no other
	// column — in particular never external_id — so a poll observation can
	// never clobber a concurrent checkout's worker assignment (the F-J zombie
	// class). applied=false (no error) when the task is no longer QUEUED.
	MarkTaskRunning(ctx context.Context, id string) (bool, error)
	// UpdateTaskPriority sets only the priority column. A full-row
	// read-modify-write here would be in the same clobber class as F-J: it
	// could overwrite a concurrent checkout's external_id/started_at.
	UpdateTaskPriority(ctx context.Context, id string, priority int) error
	GetTasksByState(ctx context.Context, state model.TaskState) ([]*model.Task, error)
	GetActiveTasks(ctx context.Context) ([]*model.Task, error)
	GetTaskSummaries(ctx context.Context, submissionIDs []string) (map[string]model.TaskSummary, error)
	// CancelNonTerminalTasks SKIPs a submission's non-terminal tasks EXCEPT
	// sub-workflow proxies: those are cancelled by the scheduler's per-tick
	// reconciliation so the cancel cascade reaches their child submissions.
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
	ListWorkerGroups(ctx context.Context) ([]string, error)
	CheckoutTask(ctx context.Context, workerID string, workerGroup string, runtime model.ContainerRuntime) (*model.Task, error)
	MarkStaleWorkersOffline(ctx context.Context, timeout time.Duration) ([]*model.Worker, error)
	RequeueWorkerTasks(ctx context.Context, workerID string) (int, error)
	ReconcileWorkerTasks(ctx context.Context, workerID string, running []string, minAge time.Duration) ([]string, error)
	CancelledTasksForWorker(ctx context.Context, taskIDs []string) ([]string, error)

	// Worker key operations (per-worker authentication, hashed at rest)
	CreateWorkerKey(ctx context.Context, k *model.WorkerKey) error
	GetWorkerKeyByID(ctx context.Context, id string) (*model.WorkerKey, error)
	GetWorkerKeyByHash(ctx context.Context, hash string) (*model.WorkerKey, error)
	ListWorkerKeys(ctx context.Context) ([]*model.WorkerKey, error)
	UpdateWorkerKey(ctx context.Context, k *model.WorkerKey) error
	DeleteWorkerKey(ctx context.Context, id string) error
	CountWorkerKeys(ctx context.Context) (int, error)
	TouchWorkerKey(ctx context.Context, id string, when time.Time) error

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
