package bvbrc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// EnumerateApps lists all available BV-BRC applications.
func (c *Client) EnumerateApps(ctx context.Context) ([]AppDescription, error) {
	resp, err := c.CallAppService(ctx, "AppService.enumerate_apps")
	if err != nil {
		return nil, err
	}

	// Result is [[apps...]]
	var result [][]AppDescription
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("EnumerateApps", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return []AppDescription{}, nil
	}
	return result[0], nil
}

// QueryAppDescription gets detailed information about a specific application.
func (c *Client) QueryAppDescription(ctx context.Context, appID string) (*AppDescription, error) {
	resp, err := c.CallAppService(ctx, "AppService.query_app_description", appID)
	if err != nil {
		return nil, err
	}

	// Result is [app] (single element array)
	var result []AppDescription
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("QueryAppDescription", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return nil, NewError("QueryAppDescription", fmt.Sprintf("app %q not found", appID))
	}
	return &result[0], nil
}

// StartAppInput contains the parameters for starting a job.
type StartAppInput struct {
	// AppID is the application identifier (e.g., "GenomeAnnotation").
	AppID string

	// Params contains the application-specific parameters.
	Params map[string]any

	// OutputPath is the workspace path where results will be stored.
	OutputPath string
}

// StartApp submits a new job for execution.
func (c *Client) StartApp(ctx context.Context, input StartAppInput) (*Task, error) {
	resp, err := c.CallAppService(ctx, "AppService.start_app",
		input.AppID,
		input.Params,
		input.OutputPath,
	)
	if err != nil {
		return nil, err
	}

	// Result is [task]
	var result []Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("StartApp", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return nil, NewError("StartApp", "no task returned from server")
	}
	return &result[0], nil
}

// QueryTasks checks the status of one or more tasks.
func (c *Client) QueryTasks(ctx context.Context, taskIDs []string) (map[string]Task, error) {
	resp, err := c.CallAppService(ctx, "AppService.query_tasks", taskIDs)
	if err != nil {
		return nil, err
	}

	// Result is [{task_id: task, ...}]
	var result []map[string]Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("QueryTasks", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return map[string]Task{}, nil
	}
	return result[0], nil
}

// QueryTask checks the status of a single task.
func (c *Client) QueryTask(ctx context.Context, taskID string) (*Task, error) {
	tasks, err := c.QueryTasks(ctx, []string{taskID})
	if err != nil {
		return nil, err
	}

	task, ok := tasks[taskID]
	if !ok {
		return nil, NewError("QueryTask", fmt.Sprintf("task %q not found", taskID))
	}
	return &task, nil
}

// QueryTaskSummary gets a summary of all tasks for the authenticated user.
func (c *Client) QueryTaskSummary(ctx context.Context) (*TaskSummary, error) {
	resp, err := c.CallAppService(ctx, "AppService.query_task_summary")
	if err != nil {
		return nil, err
	}

	// Result is [{queued: N, in-progress: N, ...}]
	var result []TaskSummary
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("QueryTaskSummary", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return &TaskSummary{}, nil
	}
	return &result[0], nil
}

// EnumerateTasksInput contains parameters for listing tasks.
type EnumerateTasksInput struct {
	// Offset is the starting index (0-based).
	Offset int

	// Limit is the maximum number of tasks to return.
	Limit int
}

// EnumerateTasks lists tasks with pagination.
func (c *Client) EnumerateTasks(ctx context.Context, input EnumerateTasksInput) ([]Task, error) {
	resp, err := c.CallAppService(ctx, "AppService.enumerate_tasks",
		input.Offset,
		input.Limit,
	)
	if err != nil {
		return nil, err
	}

	// Result is [[tasks...]]
	var result [][]Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("EnumerateTasks", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return []Task{}, nil
	}
	return result[0], nil
}

// EnumerateTasksFilteredInput contains parameters for filtered task listing.
type EnumerateTasksFilteredInput struct {
	// Offset is the starting index (0-based).
	Offset int

	// Limit is the maximum number of tasks to return.
	Limit int

	// Filter contains optional filter criteria.
	Filter *TaskFilter
}

// TaskFilter specifies filter criteria for task listing.
type TaskFilter struct {
	// Status filters by task status.
	Status TaskState `json:"status,omitempty"`

	// App filters by application ID.
	App string `json:"app,omitempty"`

	// SubmitTimeStart filters by minimum submit time.
	SubmitTimeStart *time.Time `json:"submit_time_start,omitempty"`

	// SubmitTimeEnd filters by maximum submit time.
	SubmitTimeEnd *time.Time `json:"submit_time_end,omitempty"`
}

// EnumerateTasksFiltered lists tasks with filtering and pagination.
func (c *Client) EnumerateTasksFiltered(ctx context.Context, input EnumerateTasksFilteredInput) ([]Task, error) {
	filter := map[string]any{}
	if input.Filter != nil {
		if input.Filter.Status != "" {
			filter["status"] = string(input.Filter.Status)
		}
		if input.Filter.App != "" {
			filter["app"] = input.Filter.App
		}
		if input.Filter.SubmitTimeStart != nil {
			filter["submit_time_start"] = input.Filter.SubmitTimeStart.Format(time.RFC3339)
		}
		if input.Filter.SubmitTimeEnd != nil {
			filter["submit_time_end"] = input.Filter.SubmitTimeEnd.Format(time.RFC3339)
		}
	}

	resp, err := c.CallAppService(ctx, "AppService.enumerate_tasks_filtered",
		input.Offset,
		input.Limit,
		filter,
	)
	if err != nil {
		return nil, err
	}

	var result [][]Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("EnumerateTasksFiltered", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return []Task{}, nil
	}
	return result[0], nil
}

// KillTask cancels a running or queued task.
func (c *Client) KillTask(ctx context.Context, taskID string) error {
	resp, err := c.CallAppService(ctx, "AppService.kill_task", taskID)
	if err != nil {
		return err
	}

	// Result is [1] on success
	var result []int
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return WrapError("KillTask", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 || result[0] != 1 {
		return NewError("KillTask", "task cancellation failed")
	}
	return nil
}

// QueryAppLog retrieves execution logs for a task.
func (c *Client) QueryAppLog(ctx context.Context, taskID string) (string, error) {
	resp, err := c.CallAppService(ctx, "AppService.query_app_log", taskID)
	if err != nil {
		return "", err
	}

	// Result could be a string or an array with log data
	var logStr string
	if err := json.Unmarshal(resp.Result, &logStr); err != nil {
		// Try as array
		var result []string
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return "", WrapError("QueryAppLog", fmt.Errorf("unmarshaling result: %w", err))
		}
		if len(result) > 0 {
			logStr = result[0]
		}
	}

	return logStr, nil
}

// QueryTaskDetails gets detailed information about a specific task.
func (c *Client) QueryTaskDetails(ctx context.Context, taskID string) (*Task, error) {
	resp, err := c.CallAppService(ctx, "AppService.query_task_details", taskID)
	if err != nil {
		return nil, err
	}

	// Result is [task]
	var result []Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("QueryTaskDetails", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return nil, NewError("QueryTaskDetails", fmt.Sprintf("task %q not found", taskID))
	}
	return &result[0], nil
}

// RerunTask re-submits a previously completed or failed task.
func (c *Client) RerunTask(ctx context.Context, taskID string) (*Task, error) {
	resp, err := c.CallAppService(ctx, "AppService.rerun_task", taskID)
	if err != nil {
		return nil, err
	}

	var result []Task
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, WrapError("RerunTask", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(result) == 0 {
		return nil, NewError("RerunTask", "no task returned from server")
	}
	return &result[0], nil
}

// WaitForTask polls a task until it reaches a terminal state.
func (c *Client) WaitForTask(ctx context.Context, taskID string, pollInterval time.Duration) (*Task, error) {
	if pollInterval == 0 {
		pollInterval = 10 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Check immediately first
	task, err := c.QueryTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status.IsTerminal() {
		return task, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			task, err = c.QueryTask(ctx, taskID)
			if err != nil {
				return nil, err
			}
			if task.Status.IsTerminal() {
				return task, nil
			}
		}
	}
}
