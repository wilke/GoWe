package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/me/gowe/internal/execution"
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
// Heartbeat runs in a separate goroutine to keep the worker alive during long tasks.
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

	// Start heartbeat in a separate goroutine so it continues during task execution.
	go w.heartbeatLoop(ctx)

	// Run task polling loop.
	return w.taskLoop(ctx)
}

// heartbeatLoop sends heartbeats at regular intervals until context is cancelled.
func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.client.Heartbeat(ctx); err != nil {
				w.logger.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

// taskLoop polls for tasks and executes them until context is cancelled.
func (w *Worker) taskLoop(ctx context.Context) error {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("shutting down, deregistering...")
			// Use a fresh context for deregistration.
			deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := w.client.Deregister(deregCtx)
			cancel() // Call cancel explicitly, not via defer
			if err != nil {
				w.logger.Error("deregister failed", "error", err)
			}
			return nil

		case <-ticker.C:
			if err := w.pollAndExecute(ctx); err != nil {
				w.logger.Error("poll error", "error", err)
			}
		}
	}
}

// pollAndExecute checks for work and executes if available.
// Heartbeat is handled by a separate goroutine, so this can block on task execution.
func (w *Worker) pollAndExecute(ctx context.Context) error {
	// Check for work.
	task, err := w.client.Checkout(ctx)
	if err != nil {
		return fmt.Errorf("checkout: %w", err)
	}
	if task == nil {
		return nil // No work available
	}

	w.logger.Info("task received", "task_id", task.ID, "step_id", task.StepID)

	// Execute the task (blocking). Heartbeat continues in background goroutine.
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

	// Check if task has Tool+Job (new format) or legacy _base_command.
	if task.HasTool() {
		return w.executeWithEngine(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	return w.executeLegacy(ctx, task, taskDir)
}

// executeWithEngine executes a task using the execution.Engine with full CWL support.
func (w *Worker) executeWithEngine(ctx context.Context, task *model.Task, taskDir string) error {
	w.logger.Debug("executing with engine", "task_id", task.ID, "has_tool", true)

	// Convert task.Tool (map[string]any) to *cwl.CommandLineTool.
	tool, err := parseToolFromMap(task.Tool)
	if err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("parse tool: %w", err))
	}

	// Build engine configuration.
	var expressionLib []string
	var namespaces map[string]string
	if task.RuntimeHints != nil {
		expressionLib = task.RuntimeHints.ExpressionLib
		namespaces = task.RuntimeHints.Namespaces
	}

	// Create the execution engine with the worker's stager.
	engine := execution.NewEngine(execution.Config{
		Logger:        w.logger,
		Stager:        w.stager,
		ExpressionLib: expressionLib,
		Namespaces:    namespaces,
	})

	// Execute the tool.
	result, err := engine.ExecuteTool(ctx, tool, task.Job, taskDir)

	// Handle execution errors.
	if err != nil {
		// Check if it's an execution error with exit code.
		var execErr *execution.ExecutionError
		if result != nil {
			return w.client.ReportComplete(ctx, task.ID, TaskResult{
				State:    model.TaskStateFailed,
				ExitCode: &result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr + "\n" + err.Error(),
				Outputs:  result.Outputs,
			})
		}
		return w.reportFailure(ctx, task, fmt.Errorf("execute: %w (%v)", err, execErr))
	}

	// Stage out output files.
	stagedOutputs := make(map[string]any)
	for outputID, output := range result.Outputs {
		stagedOutputs[outputID] = w.stageOutputValue(ctx, output, task.ID)
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
		Outputs:  stagedOutputs,
	})
}

// executeLegacy executes a task using the legacy _base_command approach.
func (w *Worker) executeLegacy(ctx context.Context, task *model.Task, taskDir string) error {
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

	w.logger.Debug("executing task (legacy)",
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

// stageOutputValue recursively stages File objects in output values.
func (w *Worker) stageOutputValue(ctx context.Context, v any, taskID string) any {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" {
			// Stage the file and update location.
			if path, ok := val["path"].(string); ok {
				loc, err := w.stager.StageOut(ctx, path, taskID)
				if err == nil {
					val["location"] = loc
				}
			}
			// Also stage secondary files.
			if secFiles, ok := val["secondaryFiles"].([]any); ok {
				for i, sf := range secFiles {
					secFiles[i] = w.stageOutputValue(ctx, sf, taskID)
				}
			}
			return val
		}
		// Recurse into other maps.
		for k, v := range val {
			val[k] = w.stageOutputValue(ctx, v, taskID)
		}
		return val
	case []any:
		for i, item := range val {
			val[i] = w.stageOutputValue(ctx, item, taskID)
		}
		return val
	case []map[string]any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = w.stageOutputValue(ctx, item, taskID)
		}
		return result
	default:
		return v
	}
}

// parseToolFromMap converts a map[string]any to *cwl.CommandLineTool.
func parseToolFromMap(toolMap map[string]any) (*cwl.CommandLineTool, error) {
	// Marshal to JSON then unmarshal to struct.
	// This handles all the field conversions properly.
	data, err := json.Marshal(toolMap)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}

	var tool cwl.CommandLineTool
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("unmarshal tool: %w", err)
	}

	// Handle baseCommand which may be string or []string in YAML.
	if bc, ok := toolMap["baseCommand"]; ok {
		switch cmd := bc.(type) {
		case string:
			tool.BaseCommand = []string{cmd}
		case []any:
			var baseCmd []string
			for _, c := range cmd {
				if s, ok := c.(string); ok {
					baseCmd = append(baseCmd, s)
				}
			}
			tool.BaseCommand = baseCmd
		}
	}

	// Handle inputs map conversion.
	if inputs, ok := toolMap["inputs"].(map[string]any); ok {
		tool.Inputs = make(map[string]cwl.ToolInputParam)
		for id, inp := range inputs {
			param, err := parseInputParam(id, inp)
			if err != nil {
				return nil, fmt.Errorf("input %s: %w", id, err)
			}
			tool.Inputs[id] = param
		}
	}

	// Handle outputs map conversion.
	if outputs, ok := toolMap["outputs"].(map[string]any); ok {
		tool.Outputs = make(map[string]cwl.ToolOutputParam)
		for id, out := range outputs {
			param, err := parseOutputParam(id, out)
			if err != nil {
				return nil, fmt.Errorf("output %s: %w", id, err)
			}
			tool.Outputs[id] = param
		}
	}

	// Copy requirements and hints as raw maps.
	if reqs, ok := toolMap["requirements"].(map[string]any); ok {
		tool.Requirements = reqs
	}
	if hints, ok := toolMap["hints"].(map[string]any); ok {
		tool.Hints = hints
	}

	return &tool, nil
}

// parseInputParam parses a CWL input parameter from map.
func parseInputParam(id string, v any) (cwl.ToolInputParam, error) {
	param := cwl.ToolInputParam{}

	switch val := v.(type) {
	case string:
		// Shorthand: just the type.
		param.Type = val
	case map[string]any:
		if t, ok := val["type"]; ok {
			switch tv := t.(type) {
			case string:
				param.Type = tv
			case map[string]any:
				// Array type like {type: array, items: string}
				if typeStr, ok := tv["type"].(string); ok && typeStr == "array" {
					if items, ok := tv["items"].(string); ok {
						param.Type = items + "[]"
					}
				}
			}
		}

		if ib, ok := val["inputBinding"].(map[string]any); ok {
			binding := &cwl.InputBinding{}
			if pos, ok := ib["position"]; ok {
				binding.Position = pos
			}
			if prefix, ok := ib["prefix"].(string); ok {
				binding.Prefix = prefix
			}
			if sep, ok := ib["separate"].(bool); ok {
				binding.Separate = &sep
			}
			if vf, ok := ib["valueFrom"].(string); ok {
				binding.ValueFrom = vf
			}
			if is, ok := ib["itemSeparator"].(string); ok {
				binding.ItemSeparator = is
			}
			param.InputBinding = binding
		}

		if def, ok := val["default"]; ok {
			param.Default = def
		}
	}

	return param, nil
}

// parseOutputParam parses a CWL output parameter from map.
func parseOutputParam(id string, v any) (cwl.ToolOutputParam, error) {
	param := cwl.ToolOutputParam{}

	switch val := v.(type) {
	case string:
		// Shorthand: just the type (e.g., "stdout").
		param.Type = val
	case map[string]any:
		if t, ok := val["type"]; ok {
			switch tv := t.(type) {
			case string:
				param.Type = tv
			case map[string]any:
				if typeStr, ok := tv["type"].(string); ok && typeStr == "array" {
					if items, ok := tv["items"].(string); ok {
						param.Type = items + "[]"
					}
				}
			}
		}

		if ob, ok := val["outputBinding"].(map[string]any); ok {
			binding := &cwl.OutputBinding{}
			if glob, ok := ob["glob"]; ok {
				binding.Glob = glob
			}
			if oe, ok := ob["outputEval"].(string); ok {
				binding.OutputEval = oe
			}
			if lc, ok := ob["loadContents"].(bool); ok {
				binding.LoadContents = lc
			}
			param.OutputBinding = binding
		}
	}

	return param, nil
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
