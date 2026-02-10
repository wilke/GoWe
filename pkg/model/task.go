package model

import "time"

// Task is a concrete, schedulable unit of work created from a Step.
type Task struct {
	ID           string        `json:"id"`
	SubmissionID string        `json:"submission_id"`
	StepID       string        `json:"step_id"`
	State        TaskState     `json:"state"`
	ExecutorType ExecutorType  `json:"executor_type"`
	ExternalID   string        `json:"external_id,omitempty"`
	BVBRCAppID   string        `json:"bvbrc_app_id,omitempty"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	Outputs      map[string]any `json:"outputs,omitempty"`
	DependsOn    []string      `json:"depends_on,omitempty"`
	RetryCount   int           `json:"retry_count"`
	MaxRetries   int           `json:"max_retries"`
	Stdout       string        `json:"-"`
	Stderr       string        `json:"-"`
	ExitCode     *int          `json:"-"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	CompletedAt  *time.Time    `json:"completed_at,omitempty"`
}
