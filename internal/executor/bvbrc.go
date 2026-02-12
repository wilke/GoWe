package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// reservedKeys are internal keys stripped from params before sending to BV-BRC.
var reservedKeys = map[string]bool{
	"_base_command":  true,
	"_output_globs":  true,
	"_docker_image":  true,
	"_bvbrc_app_id":  true,
}

// BVBRCExecutor submits and monitors bioinformatics jobs on BV-BRC
// via JSON-RPC 1.1. Submit is async — it returns a job UUID immediately
// and the scheduler polls Status until terminal.
type BVBRCExecutor struct {
	caller   bvbrc.RPCCaller
	username string
	logger   *slog.Logger
}

// NewBVBRCExecutor creates a BVBRCExecutor using the given RPC caller.
// username is extracted from the token and used for default workspace paths.
func NewBVBRCExecutor(caller bvbrc.RPCCaller, username string, logger *slog.Logger) *BVBRCExecutor {
	return &BVBRCExecutor{
		caller:   caller,
		username: username,
		logger:   logger.With("component", "bvbrc-executor"),
	}
}

// Type returns model.ExecutorTypeBVBRC.
func (e *BVBRCExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeBVBRC
}

// Submit calls AppService.start_app and returns the BV-BRC job UUID.
// The call returns immediately; the job runs asynchronously on BV-BRC.
func (e *BVBRCExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	appID := task.BVBRCAppID
	if appID == "" {
		if v, ok := task.Inputs["_bvbrc_app_id"].(string); ok && v != "" {
			appID = v
		}
	}
	if appID == "" {
		return "", fmt.Errorf("task %s: bvbrc_app_id is missing", task.ID)
	}

	// Build params: copy task inputs, stripping reserved keys and
	// extracting Directory locations for BV-BRC.
	params := make(map[string]any, len(task.Inputs))
	for k, v := range task.Inputs {
		if reservedKeys[k] {
			continue
		}
		if dir, ok := v.(map[string]any); ok && dir["class"] == "Directory" {
			loc, _ := dir["location"].(string)
			scheme, path := cwl.ParseLocationScheme(loc)
			switch scheme {
			case cwl.SchemeWorkspace, "":
				params[k] = path
			case cwl.SchemeShock:
				params[k] = loc
			default:
				return "", fmt.Errorf("task %s: unsupported scheme %q for Directory input %q", task.ID, scheme, k)
			}
		} else {
			params[k] = v
		}
	}

	// Determine workspace path from params or default.
	workspacePath, _ := params["output_path"].(string)
	if workspacePath == "" {
		workspacePath = fmt.Sprintf("/%s@patricbrc.org/home/", e.username)
	}

	e.logger.Debug("submitting job",
		"task_id", task.ID,
		"app_id", appID,
		"workspace", workspacePath,
	)

	result, err := e.caller.Call(ctx, "AppService.start_app", []any{appID, params, workspacePath})
	if err != nil {
		return "", fmt.Errorf("task %s: start_app: %w", task.ID, err)
	}

	// Response: result is [{id, status, ...}] where id may be a number or string.
	var jobs []map[string]any
	if err := json.Unmarshal(result, &jobs); err != nil {
		return "", fmt.Errorf("task %s: parse start_app response: %w", task.ID, err)
	}
	if len(jobs) == 0 {
		return "", fmt.Errorf("task %s: start_app returned empty result", task.ID)
	}

	jobID := fmt.Sprintf("%v", jobs[0]["id"])
	e.logger.Info("job submitted",
		"task_id", task.ID,
		"bvbrc_job_id", jobID,
		"bvbrc_status", jobs[0]["status"],
	)

	return jobID, nil
}

// Status calls AppService.query_tasks and maps the BV-BRC status to a TaskState.
func (e *BVBRCExecutor) Status(ctx context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExternalID == "" {
		return model.TaskStateQueued, nil
	}

	result, err := e.caller.Call(ctx, "AppService.query_tasks", []any{[]string{task.ExternalID}})
	if err != nil {
		return "", fmt.Errorf("task %s: query_tasks: %w", task.ID, err)
	}

	// Response: result is [{jobID: {id, status, ...}}]
	var results []map[string]struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &results); err != nil {
		return "", fmt.Errorf("task %s: parse query_tasks response: %w", task.ID, err)
	}
	if len(results) == 0 {
		return model.TaskStateQueued, nil
	}

	info, ok := results[0][task.ExternalID]
	if !ok {
		return model.TaskStateQueued, nil
	}

	return mapBVBRCState(info.Status), nil
}

// Cancel calls AppService.kill_task for the given task.
func (e *BVBRCExecutor) Cancel(ctx context.Context, task *model.Task) error {
	if task.ExternalID == "" {
		return nil
	}
	_, err := e.caller.Call(ctx, "AppService.kill_task", []any{task.ExternalID})
	if err != nil {
		return fmt.Errorf("task %s: kill_task: %w", task.ID, err)
	}
	return nil
}

// Logs calls AppService.query_app_log. On failure it falls back to stored task logs.
func (e *BVBRCExecutor) Logs(ctx context.Context, task *model.Task) (string, string, error) {
	if task.ExternalID == "" {
		return task.Stdout, task.Stderr, nil
	}

	result, err := e.caller.Call(ctx, "AppService.query_app_log", []any{task.ExternalID})
	if err != nil {
		e.logger.Debug("query_app_log failed, using stored logs", "task_id", task.ID, "error", err)
		return task.Stdout, task.Stderr, nil
	}

	var logText string
	if err := json.Unmarshal(result, &logText); err != nil {
		// Try as raw string.
		logText = string(result)
	}

	return logText, "", nil
}

// mapBVBRCState converts a BV-BRC job status string to a GoWe TaskState.
func mapBVBRCState(status string) model.TaskState {
	switch status {
	case "queued":
		return model.TaskStateQueued
	case "in-progress":
		return model.TaskStateRunning
	case "completed":
		return model.TaskStateSuccess
	case "failed", "deleted", "suspended":
		return model.TaskStateFailed
	default:
		return model.TaskStateQueued
	}
}
