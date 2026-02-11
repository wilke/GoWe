package model

import "time"

// Submission is a specific execution of a Workflow with concrete input values.
type Submission struct {
	ID           string            `json:"id"`
	WorkflowID   string            `json:"workflow_id"`
	WorkflowName string            `json:"workflow_name"`
	State        SubmissionState   `json:"state"`
	Inputs       map[string]any    `json:"inputs"`
	Outputs      map[string]any    `json:"outputs,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	SubmittedBy  string            `json:"submitted_by,omitempty"`
	Tasks        []Task            `json:"tasks,omitempty"`
	TaskSummary  TaskSummary       `json:"task_summary,omitempty"` // Computed field, not stored
	CreatedAt    time.Time         `json:"created_at"`
	CompletedAt  *time.Time        `json:"completed_at"`
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
