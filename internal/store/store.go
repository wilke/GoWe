package store

import (
	"context"
	"time"

	"github.com/me/gowe/pkg/model"
)

// ReapedWorker is a worker that ReapStaleWorkers marked offline, with the
// number of its RUNNING tasks it requeued.
type ReapedWorker struct {
	Worker   *model.Worker
	Requeued int
}

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
	// PurgeSubmissionSecrets clears a submission's secret values (keeping
	// SecretNames for auditability) and stamps SecretsPurgedAt=at.
	PurgeSubmissionSecrets(ctx context.Context, id string, at time.Time) error
	// ScrubTaskSecretsForSubmission clears RuntimeHints.Secrets and any
	// StagerOverrides.HTTPCredential from every task belonging to
	// submissionID, persisting the change. CancelNonTerminalTasks (a
	// state-only bulk UPDATE) and PurgeSubmissionSecrets (which only touches
	// the submissions row) never reach tasks.runtime_hints, so the per-task
	// subset of secrets delivered to a task can otherwise survive a cancel
	// or a manual/retention purge indefinitely (#260 H2/H3). Returns the
	// number of task rows actually rewritten (tasks with nothing to scrub
	// are skipped). Safe to call on a submission with no tasks or no
	// secret-bearing tasks (returns 0, nil).
	ScrubTaskSecretsForSubmission(ctx context.Context, submissionID string) (int, error)
	// ListSubmissionsWithSecretsForRetention returns terminal-state
	// submissions that still carry secret values, for the scheduler's
	// retention sweep. Secret values are never decrypted here (Secrets is
	// always nil on the results).
	ListSubmissionsWithSecretsForRetention(ctx context.Context) ([]*model.Submission, error)
	// ListSubmissionsAwaitingOutputStaging returns COMPLETED submissions with
	// an output destination and no recorded delivery outcome (SQL-filtered so
	// the post-stage phase's cost is bounded by pending deliveries).
	ListSubmissionsAwaitingOutputStaging(ctx context.Context) ([]*model.Submission, error)
	// ListSubmissionsAwaitingPrestage returns RUNNING submissions whose
	// server-side workspace pre-staging was started but never completed
	// (prestage_started_at set, prestage_completed_at NULL) — the recovery
	// half of the scheduler's pre-stage loop selection alongside its existing
	// PENDING scan (#268/#269): without it, a submission that reaches RUNNING
	// before pre-staging finishes is never revisited and its READY steps
	// defer forever.
	ListSubmissionsAwaitingPrestage(ctx context.Context) ([]*model.Submission, error)
	DeleteSubmission(ctx context.Context, id string) error
	UpdateSubmissionInputs(ctx context.Context, id string, inputs map[string]any) error
	GetChildSubmissions(ctx context.Context, parentTaskID string) ([]*model.Submission, error)
	CountSubmissionsByState(ctx context.Context, since time.Time, submittedBy string) (map[string]int, error)
	// GetSubmissionMeta is a lean SELECT of workflow_name and labels only
	// (no inputs/outputs/tasks/timestamps) — for label lookups that only
	// need to name a submission's workflow, e.g. Prometheus instrumentation
	// on the handler path where the caller never loads the full row.
	// Returns (nil, nil) when the submission does not exist.
	GetSubmissionMeta(ctx context.Context, id string) (*model.SubmissionMeta, error)

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
	// TerminalizeTaskFrom is UpdateTask guarded by a compare-and-set against a
	// single exact state: the write is skipped (applied=false, no error)
	// unless the task is still in `from`. Use this instead of TerminalizeTask
	// when the caller's snapshot is only valid for one specific non-terminal
	// state — e.g. detectStuckTasks failing a task it observed as QUEUED: a
	// worker that checks the task out between the snapshot and this write
	// must win, not be clobbered by a stale full-row write that resets
	// external_id and reports the task terminal out from under the worker.
	TerminalizeTaskFrom(ctx context.Context, task *model.Task, from model.TaskState) (bool, error)
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
	// ResetFailedTasks archives (reason reset) and resets the submission's
	// FAILED tasks to PENDING in one transaction.
	ResetFailedTasks(ctx context.Context, submissionID string) (int, error)
	ResetFailedSteps(ctx context.Context, submissionID string) (int, error)
	// ArchiveAndResetTask keeps the task's current record as an attempt
	// (reason, model.AttemptReason*) and writes task (the caller's blanked
	// copy) in one transaction, both only if the row is still in state from.
	// applied=false (nothing archived, nothing written) when it is not. A row
	// that never started and holds no output is written without an attempt.
	ArchiveAndResetTask(ctx context.Context, task *model.Task, from model.TaskState, reason string) (applied bool, err error)
	// ListTaskAttempts returns the task's archived attempts by attempt
	// ascending (empty when none).
	ListTaskAttempts(ctx context.Context, taskID string) ([]model.TaskAttempt, error)
	// CountTasksByState returns the current global count of tasks per state
	// (a single GROUP BY query), used to refresh the gowe_tasks{state} gauge
	// once per scheduler tick.
	CountTasksByState(ctx context.Context) (map[string]int, error)
	// CountTasksQueuedByGroup returns the current count of QUEUED tasks per
	// worker group (a single GROUP BY query keyed on the runtime_hints
	// worker_group JSON field, '' normalized to "default"), used to refresh
	// the gowe_queue_depth{group} gauge once per scheduler tick.
	CountTasksQueuedByGroup(ctx context.Context) (map[string]int, error)
	// CountRunningTasksByWorker returns the number of RUNNING worker-executor
	// tasks per owning worker id (external_id; unowned rows excluded), a
	// single GROUP BY query for the read-only per-host view (#323).
	CountRunningTasksByWorker(ctx context.Context) (map[string]int, error)
	// SumChargedByWorker returns the cores and RAM charged at checkout to the
	// RUNNING worker-executor tasks per owning worker id (NULL charges count
	// as 0), for the per-host view and the gowe_host_charged_* gauges.
	SumChargedByWorker(ctx context.Context) (map[string]model.WorkerCharge, error)

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
	// TouchWorker records a heartbeat in one UPDATE of state, last_seen and
	// peer_addr (only when peerAddr is not empty: "" keeps the stored value),
	// plus host_usage/host_usage_at (= lastSeen) only when usage is non-nil.
	// It never writes current_task, so it cannot clobber a concurrent
	// CheckoutTask the way a full-row UpdateWorker can.
	TouchWorker(ctx context.Context, id string, state model.WorkerState, lastSeen time.Time, peerAddr string, usage *model.WorkerUsage) error
	// DeleteWorker removes the worker and requeues its RUNNING worker-executor tasks atomically.
	DeleteWorker(ctx context.Context, id string) error
	// DeleteOfflineWorker is DeleteWorker restricted, inside its transaction,
	// to a worker that is offline, whose last_seen is older than now -
	// silentFor and, when group != "", whose group is group ("" and
	// "default" fold together). It reports whether the worker was deleted.
	DeleteOfflineWorker(ctx context.Context, id, group string, silentFor time.Duration) (bool, error)
	ListWorkers(ctx context.Context) ([]*model.Worker, error)
	ListWorkerGroups(ctx context.Context) ([]string, error)
	// AdoptWorkerKey binds a legacy worker (key_id='') to keyID; CAS, true if bound by this call.
	AdoptWorkerKey(ctx context.Context, id, keyID string) (bool, error)
	ListWorkersByKey(ctx context.Context, keyID string) ([]*model.Worker, error)
	// CheckoutTask hands the worker a QUEUED task (nil when none). It returns
	// nil, changing no task, when the worker row does not exist.
	CheckoutTask(ctx context.Context, workerID string, workerGroup string, runtime model.ContainerRuntime) (*model.Task, error)
	MarkStaleWorkersOffline(ctx context.Context, timeout time.Duration) ([]*model.Worker, error)
	// ReapStaleWorkers marks online workers whose last_seen is older than
	// timeout offline and requeues their RUNNING worker-executor tasks, in one
	// transaction. It also requeues RUNNING worker tasks still attributed to
	// workers that are offline and stale (stranded by an earlier failure).
	// Returns the workers it marked offline, each with its requeued count,
	// and the number of stranded tasks requeued.
	ReapStaleWorkers(ctx context.Context, timeout time.Duration) ([]ReapedWorker, int, error)
	RequeueWorkerTasks(ctx context.Context, workerID string) (int, error)
	ReconcileWorkerTasks(ctx context.Context, workerID string, running []string, minAge time.Duration) ([]string, error)
	// ReleaseWorkerTask moves the worker's current_task off taskID in one
	// UPDATE: if current_task is taskID it becomes the worker's most recently
	// started other RUNNING worker task, or '' if none. Any other
	// current_task, and every other column, is left unchanged. An unknown
	// worker is not an error.
	ReleaseWorkerTask(ctx context.Context, workerID, taskID string) error
	CancelledTasksForWorker(ctx context.Context, taskIDs []string) ([]string, error)

	// Worker key operations (per-worker authentication, hashed at rest)
	CreateWorkerKey(ctx context.Context, k *model.WorkerKey) error
	GetWorkerKeyByID(ctx context.Context, id string) (*model.WorkerKey, error)
	GetWorkerKeyByHash(ctx context.Context, hash string) (*model.WorkerKey, error)
	ListWorkerKeys(ctx context.Context) ([]*model.WorkerKey, error)
	UpdateWorkerKey(ctx context.Context, k *model.WorkerKey) error
	DeleteWorkerKey(ctx context.Context, id string) error
	// DeleteWorkerKeyForGroup removes the key only when its groups are
	// exactly [group]; false (no error) when the key does not exist or has
	// other groups.
	DeleteWorkerKeyForGroup(ctx context.Context, id, group string) (bool, error)
	CountWorkerKeys(ctx context.Context) (int, error)
	TouchWorkerKey(ctx context.Context, id string, when time.Time) error

	// Tool blessing operations (admin-approved token injection; rows are never deleted)
	CreateToolBlessing(ctx context.Context, b *model.ToolBlessing) error
	GetToolBlessing(ctx context.Context, id string) (*model.ToolBlessing, error)
	GetActiveToolBlessingByHash(ctx context.Context, toolHash string) (*model.ToolBlessing, error)
	ListToolBlessings(ctx context.Context, includeRevoked bool) ([]*model.ToolBlessing, error)
	RevokeToolBlessing(ctx context.Context, id, revokedBy, reason string) error
	AddToolBlessingImageSHA256(ctx context.Context, id, sha256 string) error

	// Worker-group access policy (#261) and server settings, managed by
	// admins at runtime and loaded into internal/groupacl.
	// ListWorkerGroupPolicies returns every rule sorted by group name; a row
	// that cannot be read is an error, never skipped (the policy fails closed).
	ListWorkerGroupPolicies(ctx context.Context) ([]*model.WorkerGroupPolicy, error)
	// UpsertWorkerGroupPolicy creates the rule of p.Group or replaces its
	// users, admins, updated_by and updated_at (created_at is kept); managers
	// are replaced unless p.Managers is nil (then kept).
	UpsertWorkerGroupPolicy(ctx context.Context, p *model.WorkerGroupPolicy) error
	// AddWorkerGroupUser / RemoveWorkerGroupUser change the users of an
	// existing rule in one transaction and report whether the list changed;
	// ErrWorkerGroupPolicyNotFound when the group has no rule.
	AddWorkerGroupUser(ctx context.Context, group, username, by string) (bool, error)
	RemoveWorkerGroupUser(ctx context.Context, group, username, by string) (bool, error)
	// DeleteWorkerGroupPolicy removes the rule of group and reports whether
	// one existed.
	DeleteWorkerGroupPolicy(ctx context.Context, group string) (bool, error)
	// GetServerSetting returns the value stored under key; found is false
	// (no error) when there is none.
	GetServerSetting(ctx context.Context, key string) (value string, found bool, err error)
	// SetServerSetting creates or replaces the value of key.
	SetServerSetting(ctx context.Context, key, value, updatedBy string) error

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
