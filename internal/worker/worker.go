package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Worker is the core work loop that polls the server for tasks, executes
// them using the configured runtime, and reports results back.
type Worker struct {
	client   *Client
	runtime  Runtime
	stager   Stager
	workDir  string
	stageOut string
	poll     time.Duration
	logger   *slog.Logger
}

// Config holds worker configuration.
type Config struct {
	ServerURL string
	Name      string
	Hostname  string
	Runtime   string
	WorkDir   string
	StageOut  string
	Poll      time.Duration
}

// New creates a Worker from configuration.
func New(cfg Config, logger *slog.Logger) (*Worker, error) {
	rt, err := NewRuntime(cfg.Runtime)
	if err != nil {
		return nil, err
	}

	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(os.TempDir(), "gowe-worker")
	}
	if cfg.StageOut == "" {
		cfg.StageOut = "local"
	}
	if cfg.Poll == 0 {
		cfg.Poll = 5 * time.Second
	}

	return &Worker{
		client:   NewClient(cfg.ServerURL),
		runtime:  rt,
		stager:   NewFileStager(cfg.StageOut),
		workDir:  cfg.WorkDir,
		stageOut: cfg.StageOut,
		poll:     cfg.Poll,
		logger:   logger.With("component", "worker"),
	}, nil
}

// Run starts the main work loop. It registers with the server, then
// loops polling for tasks until the context is cancelled.
func (w *Worker) Run(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(w.workDir, 0o755); err != nil {
		return fmt.Errorf("create workdir %s: %w", w.workDir, err)
	}

	worker, err := w.client.Register(ctx, cfg.Name, cfg.Hostname, cfg.Runtime)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	w.logger.Info("registered with server",
		"worker_id", worker.ID,
		"name", worker.Name,
		"runtime", worker.Runtime,
	)

	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("shutting down, deregistering...")
			// Use a fresh context for deregistration.
			deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.client.Deregister(deregCtx); err != nil {
				w.logger.Error("deregister failed", "error", err)
			}
			return nil

		case <-ticker.C:
			if err := w.tick(ctx); err != nil {
				w.logger.Error("tick error", "error", err)
			}
		}
	}
}

// tick performs one iteration: heartbeat, check for work, execute if available.
func (w *Worker) tick(ctx context.Context) error {
	// Heartbeat first.
	if err := w.client.Heartbeat(ctx); err != nil {
		w.logger.Warn("heartbeat failed", "error", err)
	}

	// Check for work.
	task, err := w.client.Checkout(ctx)
	if err != nil {
		return fmt.Errorf("checkout: %w", err)
	}
	if task == nil {
		return nil // No work available
	}

	w.logger.Info("task received", "task_id", task.ID, "step_id", task.StepID)

	// Execute the task (blocking).
	if err := w.executeTask(ctx, task); err != nil {
		w.logger.Error("task execution failed", "task_id", task.ID, "error", err)
	}

	return nil
}

// executeTask runs a single task: stage-in → run → collect outputs → stage-out → report.
func (w *Worker) executeTask(ctx context.Context, task *model.Task) error {
	taskDir := filepath.Join(w.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("create task dir: %w", err))
	}

	// Extract reserved keys from task inputs.
	command := extractBaseCommand(task.Inputs)
	image, _ := task.Inputs["_docker_image"].(string)
	outputGlobs := extractOutputGlobs(task.Inputs)

	if len(command) == 0 {
		return w.reportFailure(ctx, task, fmt.Errorf("_base_command missing or empty"))
	}

	// Stage-in: copy file:// inputs to task directory.
	volumes := make(map[string]string)
	for k, v := range task.Inputs {
		if isReservedKey(k) {
			continue
		}
		if dir, ok := v.(map[string]any); ok && dir["class"] == "Directory" {
			loc, _ := dir["location"].(string)
			scheme, path := cwl.ParseLocationScheme(loc)
			if scheme == cwl.SchemeFile || scheme == "" {
				volumes[path] = "/work/" + k
			}
		}
	}

	// Run via configured runtime.
	spec := RunSpec{
		Image:   image,
		Command: command,
		WorkDir: taskDir,
		Volumes: volumes,
	}

	w.logger.Debug("executing task",
		"task_id", task.ID,
		"image", image,
		"command", command,
	)

	result, runErr := w.runtime.Run(ctx, spec)
	if runErr != nil {
		// Runtime infrastructure error (e.g., binary not found).
		return w.reportFailure(ctx, task, runErr)
	}

	// Collect outputs via glob patterns.
	outputs := make(map[string]any)
	for outputID, pattern := range outputGlobs {
		matches, err := filepath.Glob(filepath.Join(taskDir, pattern))
		if err != nil {
			continue
		}

		// Stage-out matched files.
		var staged []string
		for _, m := range matches {
			loc, err := w.stager.StageOut(ctx, m, task.ID)
			if err != nil {
				w.logger.Warn("stage-out failed", "file", m, "error", err)
				continue
			}
			staged = append(staged, loc)
		}

		if len(staged) == 1 {
			outputs[outputID] = staged[0]
		} else if len(staged) > 1 {
			outputs[outputID] = staged
		}
	}

	// Determine state from exit code.
	state := model.TaskStateSuccess
	if result.ExitCode != 0 {
		state = model.TaskStateFailed
	}

	return w.client.ReportComplete(ctx, task.ID, TaskResult{
		State:    state,
		ExitCode: &result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Outputs:  outputs,
	})
}

// reportFailure sends a FAILED completion with the given error as stderr.
func (w *Worker) reportFailure(ctx context.Context, task *model.Task, execErr error) error {
	exitCode := -1
	reportErr := w.client.ReportComplete(ctx, task.ID, TaskResult{
		State:    model.TaskStateFailed,
		ExitCode: &exitCode,
		Stderr:   execErr.Error(),
	})
	if reportErr != nil {
		return fmt.Errorf("report failure: %w (original: %v)", reportErr, execErr)
	}
	return execErr
}

// extractBaseCommand reads _base_command from task inputs as []string.
func extractBaseCommand(inputs map[string]any) []string {
	raw, ok := inputs["_base_command"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// extractOutputGlobs reads _output_globs from task inputs as map[string]string.
func extractOutputGlobs(inputs map[string]any) map[string]string {
	raw, ok := inputs["_output_globs"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}

// isReservedKey checks if a key is a reserved internal input key.
func isReservedKey(key string) bool {
	switch key {
	case "_base_command", "_output_globs", "_docker_image", "_bvbrc_app_id":
		return true
	}
	return false
}
