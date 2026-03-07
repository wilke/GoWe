package model

import "time"

// Submission is a specific execution of a Workflow with concrete input values.
type Submission struct {
	ID            string            `json:"id"`
	WorkflowID    string            `json:"workflow_id"`
	WorkflowName  string            `json:"workflow_name"`
	State         SubmissionState   `json:"state"`
	Inputs        map[string]any    `json:"inputs"`
	Outputs       map[string]any    `json:"outputs,omitempty"`
	Error         *SubmissionError  `json:"error,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	SubmittedBy   string            `json:"submitted_by,omitempty"`
	Tasks         []Task            `json:"tasks,omitempty"`
	TaskSummary   TaskSummary       `json:"task_summary,omitempty"`   // Computed field, not stored
	QueuePosition int               `json:"queue_position,omitempty"` // Computed field for pending submissions
	CreatedAt     time.Time         `json:"created_at"`
	CompletedAt   *time.Time        `json:"completed_at"`

	// Child submission linkage: if set, this submission was created by the
	// scheduler to execute a sub-workflow step on behalf of a parent task.
	ParentTaskID string `json:"parent_task_id,omitempty"`

	// Authentication token fields (not serialized to JSON responses).
	UserToken    string    `json:"-"` // Provider token for downstream calls
	TokenExpiry  time.Time `json:"-"` // Token expiration time
	AuthProvider string    `json:"-"` // Provider name (bvbrc, mgrast)
}

// SubmissionError captures structured failure details when a submission fails.
type SubmissionError struct {
	Code    string               `json:"code"`
	Message string               `json:"message"`
	Context *SubmissionErrDetail `json:"context,omitempty"`
}

// SubmissionErrDetail provides specific context about where the failure occurred.
type SubmissionErrDetail struct {
	StepID   string `json:"step_id,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// TaskSummary provides an aggregate count of task states within a Submission.
type TaskSummary struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Scheduled int `json:"scheduled"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Success   int `json:"success"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

// ComputeTaskSummary calculates the TaskSummary from a slice of Tasks.
func ComputeTaskSummary(tasks []Task) TaskSummary {
	s := TaskSummary{Total: len(tasks)}
	for _, t := range tasks {
		switch t.State {
		case TaskStatePending:
			s.Pending++
		case TaskStateScheduled:
			s.Scheduled++
		case TaskStateQueued:
			s.Queued++
		case TaskStateRunning:
			s.Running++
		case TaskStateSuccess:
			s.Success++
		case TaskStateFailed:
			s.Failed++
		case TaskStateSkipped:
			s.Skipped++
		}
	}
	return s
}
