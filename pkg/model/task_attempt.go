package model

import "time"

// Reasons an earlier attempt of a task ended and was archived (TaskAttempt.Reason).
const (
	// AttemptReasonRetry: the attempt failed and the scheduler retried the task.
	AttemptReasonRetry = "retry"
	// AttemptReasonReset: the attempt failed and a user re-queued the task
	// (REST retry of the submission, UI resume or recompute).
	AttemptReasonReset = "reset"
	// AttemptReasonWorkerOffline: the worker running the attempt stopped
	// sending heartbeats and the task was re-queued.
	AttemptReasonWorkerOffline = "worker_offline"
	// AttemptReasonWorkerRemoved: the worker running the attempt was deleted
	// or deregistered and the task was re-queued.
	AttemptReasonWorkerRemoved = "worker_removed"
	// AttemptReasonRequeued: the task was re-queued by another path.
	AttemptReasonRequeued = "requeued"
)

// MaxTaskAttempts is how many archived attempts are kept per task: archiving
// a newer one deletes the task's attempts older than the newest
// MaxTaskAttempts. Attempt numbers keep counting.
const MaxTaskAttempts = 20

// TaskAttempt is the record of an earlier attempt of a task, kept when the
// task record is retried, reset or requeued. It holds the task row's values
// at the moment the attempt was archived.
type TaskAttempt struct {
	TaskID       string     `json:"task_id"`
	Attempt      int        `json:"attempt"` // 1, 2, 3 ... in the order the attempts ended
	SubmissionID string     `json:"submission_id"`
	Reason       string     `json:"reason"` // one of the AttemptReason* constants
	State        string     `json:"state"`  // task state when archived
	ExitCode     *int       `json:"exit_code"`
	Stdout       string     `json:"stdout"`
	Stderr       string     `json:"stderr"`
	ExecutorType string     `json:"executor_type"`
	ExternalID   string     `json:"external_id"` // for worker tasks the worker id
	WorkerName   string     `json:"worker_name"` // '' if unknown
	RetryCount   int        `json:"retry_count"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	ArchivedAt   time.Time  `json:"archived_at"`
}

// WorkerLabel is the worker the attempt ran on as the UI and CLI show it: the
// worker's name, else its id. It is "-" when the attempt did not run on a
// worker: the task is not a worker task, or it was never checked out (a
// queued worker task's external_id is its own task id).
func (a TaskAttempt) WorkerLabel() string {
	if a.ExecutorType != string(ExecutorTypeWorker) || a.ExternalID == a.TaskID {
		return "-"
	}
	if a.WorkerName != "" {
		return a.WorkerName
	}
	if a.ExternalID != "" {
		return a.ExternalID
	}
	return "-"
}
