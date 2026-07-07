// Package webhook sends completion notifications to external callback URLs
// when a submission reaches a terminal state (COMPLETED, FAILED, CANCELLED).
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Payload is the JSON body POSTed to the callback URL.
type Payload struct {
	WorkflowID   string     `json:"workflow_id"`
	WorkflowName string     `json:"workflow_name"`
	Status       string     `json:"status"`
	SessionID    string     `json:"session_id"`
	Owner        string     `json:"owner"`
	CompletedAt  string     `json:"completed_at"`
	Steps        []StepInfo `json:"steps"`
	OutputPaths  []string   `json:"output_paths"`
}

// StepInfo describes one task in the webhook payload.
type StepInfo struct {
	StepName     string  `json:"step_name"`
	App          string  `json:"app"`
	Status       string  `json:"status"`
	TaskID       string  `json:"task_id"`
	ElapsedTime  string  `json:"elapsed_time"`
	ErrorMessage *string `json:"error_message"`
}

// Config holds webhook client configuration.
type Config struct {
	// Timeout is the HTTP timeout for each webhook attempt.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts on network errors or 5xx responses.
	MaxRetries int

	// RetryBackoff is the delay between retries.
	RetryBackoff time.Duration
}

// DefaultConfig returns sensible defaults for webhook delivery.
func DefaultConfig() Config {
	return Config{
		Timeout:      10 * time.Second,
		MaxRetries:   2,
		RetryBackoff: 1 * time.Second,
	}
}

// MapSubmissionStatus maps internal GoWe submission states to the webhook
// status strings expected by the gateway.
func MapSubmissionStatus(state model.SubmissionState) string {
	switch state {
	case model.SubmissionStateCompleted:
		return "succeeded"
	case model.SubmissionStateFailed:
		return "failed"
	case model.SubmissionStateCancelled:
		return "cancelled"
	default:
		return string(state)
	}
}

// MapTaskStatus maps internal GoWe task states to webhook status strings.
func MapTaskStatus(state model.TaskState) string {
	switch state {
	case model.TaskStateSuccess:
		return "succeeded"
	case model.TaskStateFailed:
		return "failed"
	case model.TaskStateSkipped:
		return "skipped"
	default:
		return string(state)
	}
}

// FormatElapsedTime returns a human-readable duration string like "2m 34s".
// Returns "" if either time pointer is nil.
func FormatElapsedTime(start, end *time.Time) string {
	if start == nil || end == nil {
		return ""
	}
	d := end.Sub(*start)
	if d < 0 {
		d = 0
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// ExtractApp extracts the app name from a task's CWL Tool hints.
// It looks for gowe:Execution.bvbrc_app_id, falling back to the task's StepID.
func ExtractApp(task *model.Task) string {
	if task.Tool != nil {
		if hints, ok := task.Tool["hints"].(map[string]any); ok {
			goweMap, ok := hints["gowe:Execution"].(map[string]any)
			if !ok {
				goweMap, _ = hints["goweHint"].(map[string]any)
			}
			if goweMap != nil {
				if app, ok := goweMap["bvbrc_app_id"].(string); ok && app != "" {
					return app
				}
			}
		}
	}
	// Also check the BVBRCAppID field directly.
	if task.BVBRCAppID != "" {
		return task.BVBRCAppID
	}
	return task.StepID
}

// ExtractOutputPaths extracts file/directory location values from submission outputs.
func ExtractOutputPaths(outputs map[string]any) []string {
	var paths []string
	for _, v := range outputs {
		extractLocations(v, &paths)
	}
	return paths
}

// extractLocations recursively extracts "location" values from CWL File/Directory objects.
func extractLocations(v any, paths *[]string) {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			if loc, ok := val["location"].(string); ok && loc != "" {
				*paths = append(*paths, loc)
			}
		}
		// Check for secondary files within File objects.
		if sf, ok := val["secondaryFiles"].([]any); ok {
			for _, s := range sf {
				extractLocations(s, paths)
			}
		}
	case []any:
		for _, item := range val {
			extractLocations(item, paths)
		}
	}
}

// BuildPayload constructs the webhook payload from a submission and its tasks.
func BuildPayload(sub *model.Submission) *Payload {
	completedAt := ""
	if sub.CompletedAt != nil {
		completedAt = sub.CompletedAt.Format(time.RFC3339)
	}

	sessionID := ""
	if sub.Labels != nil {
		sessionID = sub.Labels["session_id"]
	}

	steps := make([]StepInfo, 0, len(sub.Tasks))
	for i := range sub.Tasks {
		task := &sub.Tasks[i]
		var errMsg *string
		if task.State == model.TaskStateFailed && task.Stderr != "" {
			s := task.Stderr
			errMsg = &s
		}

		steps = append(steps, StepInfo{
			StepName:     task.StepID,
			App:          ExtractApp(task),
			Status:       MapTaskStatus(task.State),
			TaskID:       task.ID,
			ElapsedTime:  FormatElapsedTime(task.StartedAt, task.CompletedAt),
			ErrorMessage: errMsg,
		})
	}

	outputPaths := ExtractOutputPaths(sub.Outputs)
	if outputPaths == nil {
		outputPaths = []string{}
	}

	return &Payload{
		WorkflowID:   sub.WorkflowID,
		WorkflowName: sub.WorkflowName,
		Status:       MapSubmissionStatus(sub.State),
		SessionID:    sessionID,
		Owner:        sub.SubmittedBy,
		CompletedAt:  completedAt,
		Steps:        steps,
		OutputPaths:  outputPaths,
	}
}

// Send delivers the webhook payload to the callback URL asynchronously.
// It retries on network errors or 5xx responses up to cfg.MaxRetries times.
// It does not retry on 4xx responses.
// This function is designed to be called in a goroutine (fire-and-forget).
func Send(callbackURL string, payload *Payload, cfg Config, logger *slog.Logger) {
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("webhook: marshal payload", "error", err, "callback_url", callbackURL)
		return
	}

	client := &http.Client{Timeout: cfg.Timeout}

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(cfg.RetryBackoff)
		}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
		if err != nil {
			cancel()
			logger.Error("webhook: create request", "error", err, "callback_url", callbackURL, "attempt", attempt+1)
			return // Bad URL — don't retry
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		cancel()

		if err != nil {
			logger.Warn("webhook: request failed",
				"error", err,
				"callback_url", callbackURL,
				"attempt", attempt+1,
				"max_attempts", cfg.MaxRetries+1,
			)
			continue // Retry on network errors
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Info("webhook: delivered",
				"callback_url", callbackURL,
				"status_code", resp.StatusCode,
				"workflow_id", payload.WorkflowID,
			)
			return
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			logger.Error("webhook: rejected (4xx, not retrying)",
				"callback_url", callbackURL,
				"status_code", resp.StatusCode,
				"attempt", attempt+1,
			)
			return // Don't retry on 4xx
		}

		// 5xx — retry
		logger.Warn("webhook: server error, retrying",
			"callback_url", callbackURL,
			"status_code", resp.StatusCode,
			"attempt", attempt+1,
			"max_attempts", cfg.MaxRetries+1,
		)
	}

	logger.Error("webhook: all attempts exhausted",
		"callback_url", callbackURL,
		"max_attempts", cfg.MaxRetries+1,
	)
}
