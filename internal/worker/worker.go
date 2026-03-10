package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// Worker is the core work loop that polls the server for tasks, executes
// them using the configured runtime, and reports results back.
type Worker struct {
	client            *Client
	runtime           Runtime
	stager            execution.Stager
	httpStager        *execution.HTTPStager // Base HTTP stager for creating per-task overrides
	parser            *parser.Parser
	workDir           string
	stageOut          string
	stagerCfg         StagerConfig
	poll              time.Duration
	containerRuntime  string               // "docker", "apptainer", or "none"
	gpu               GPUWorkerConfig     // GPU configuration
	resources         ResourceWorkerConfig // Resource limits
	dockerHostPathMap map[string]string // Docker-in-Docker path mapping
	dockerVolume      string            // Named Docker volume shared with tool containers
	inputPathMap      map[string]string // Input path mapping for host->container translation
	logger            *slog.Logger
}

// Config holds worker configuration.
type Config struct {
	ServerURL string
	Name      string
	Hostname  string
	Group     string // Worker group for task scheduling
	WorkerKey string // Shared secret for worker authentication
	Runtime   string
	WorkDir   string
	StageOut  string
	Poll      time.Duration
	Stager    StagerConfig
	GPU       GPUWorkerConfig      // GPU configuration
	Resources ResourceWorkerConfig // Resource limits

	// DockerHostPathMap maps container paths to Docker host paths.
	// Needed for Docker-in-Docker scenarios where the worker container
	// uses the host's Docker socket.
	// Format: container_path -> host_path
	DockerHostPathMap map[string]string

	// DockerVolume is a named Docker volume shared between the worker and
	// tool containers. When set, tool containers mount this volume instead
	// of using bind mounts with path translation. This eliminates the need
	// for DockerHostPathMap when the worker runs inside a container.
	DockerVolume string

	// InputPathMap maps host paths in task inputs to local container paths.
	// This allows workers running in containers to translate paths from the
	// original host filesystem to their local mount points.
	// Format: host_path -> container_path
	InputPathMap map[string]string
}

// GPUWorkerConfig holds GPU settings for the worker.
type GPUWorkerConfig struct {
	Enabled  bool   // Enable GPU support for this worker
	DeviceID string // Specific GPU device ID (e.g., "0", "1") - empty means use all/auto
}

// ResourceWorkerConfig holds resource limit settings for the worker.
type ResourceWorkerConfig struct {
	MaxCPUs  int   // Max CPUs for containers (0 = auto-detect)
	MaxMemMB int64 // Max memory in MiB for containers (0 = auto-detect)
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

	// Auto-detect system resources if not explicitly set.
	if cfg.Resources.MaxMemMB == 0 {
		var si syscall.Sysinfo_t
		if err := syscall.Sysinfo(&si); err == nil {
			cfg.Resources.MaxMemMB = int64(si.Totalram) / (1024 * 1024)
		}
	}
	if cfg.Resources.MaxCPUs == 0 {
		cfg.Resources.MaxCPUs = runtime.NumCPU()
	}
	logger.Info("worker resource limits",
		"max_cpus", cfg.Resources.MaxCPUs,
		"max_mem_mb", cfg.Resources.MaxMemMB,
	)

	// Build TLS config.
	tlsCfg, err := cfg.Stager.TLS.BuildTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("build TLS config: %w", err)
	}

	// Create the HTTP stager.
	httpCfg := execution.HTTPStagerConfig{
		Timeout:        cfg.Stager.HTTP.Timeout,
		MaxRetries:     cfg.Stager.HTTP.MaxRetries,
		RetryDelay:     cfg.Stager.HTTP.RetryDelay,
		DefaultHeaders: cfg.Stager.HTTP.DefaultHeaders,
		UploadMethod:   cfg.Stager.HTTP.UploadMethod,
		UploadPath:     cfg.Stager.HTTP.UploadPath,
	}

	// Convert credentials.
	if cfg.Stager.HTTP.Credentials != nil {
		httpCfg.Credentials = make(map[string]execution.CredentialSet)
		for host, cred := range cfg.Stager.HTTP.Credentials {
			httpCfg.Credentials[host] = execution.CredentialSet{
				Type:        cred.Type,
				Token:       cred.Token,
				Username:    cred.Username,
				Password:    cred.Password,
				HeaderName:  cred.HeaderName,
				HeaderValue: cred.HeaderValue,
			}
		}
	}

	httpStager := execution.NewHTTPStager(httpCfg, tlsCfg)

	// Create composite stager with scheme handlers.
	fileStager := execution.NewFileStager(cfg.StageOut)
	handlers := map[string]execution.Stager{
		"file":  fileStager,
		"":      fileStager, // Default for bare paths
		"http":  httpStager,
		"https": httpStager,
	}

	// Add S3 stager if configured.
	if cfg.Stager.S3.Endpoint != "" || cfg.Stager.S3.AccessKeyID != "" || cfg.Stager.S3.DefaultBucket != "" {
		s3Stager, err := staging.NewS3Stager(staging.S3Config{
			Endpoint:        cfg.Stager.S3.Endpoint,
			Region:          cfg.Stager.S3.Region,
			AccessKeyID:     cfg.Stager.S3.AccessKeyID,
			SecretAccessKey: cfg.Stager.S3.SecretAccessKey,
			UsePathStyle:    cfg.Stager.S3.UsePathStyle,
			DisableSSL:      cfg.Stager.S3.DisableSSL,
			DefaultBucket:   cfg.Stager.S3.DefaultBucket,
			StageOutPrefix:  cfg.Stager.S3.StageOutPrefix,
		})
		if err != nil {
			return nil, fmt.Errorf("create S3 stager: %w", err)
		}
		handlers["s3"] = s3Stager
		logger.Info("S3 stager enabled", "endpoint", cfg.Stager.S3.Endpoint, "bucket", cfg.Stager.S3.DefaultBucket)
	}

	// Add Shock stager if configured.
	if cfg.Stager.Shock.DefaultHost != "" {
		shockStager := staging.NewShockStager(staging.ShockConfig{
			DefaultHost: cfg.Stager.Shock.DefaultHost,
			Token:       cfg.Stager.Shock.Token,
			Timeout:     cfg.Stager.Shock.Timeout,
			MaxRetries:  cfg.Stager.Shock.MaxRetries,
			UseHTTP:     cfg.Stager.Shock.UseHTTP,
		})
		handlers["shock"] = shockStager
		logger.Info("Shock stager enabled", "host", cfg.Stager.Shock.DefaultHost, "https", !cfg.Stager.Shock.UseHTTP)
	}

	// Add shared filesystem stager if configured.
	if cfg.Stager.Shared.Enabled {
		sharedStager := staging.NewSharedFileStager(staging.SharedFileStagerConfig{
			PathMap:     cfg.Stager.Shared.PathMap,
			Mode:        cfg.Stager.StageMode,
			StageOutDir: cfg.Stager.Shared.StageOutDir,
		})
		// Shared stager handles file:// scheme, override the default.
		handlers["file"] = sharedStager
		handlers[""] = sharedStager
		logger.Info("Shared filesystem stager enabled",
			"mode", cfg.Stager.StageMode.String(),
			"paths", len(cfg.Stager.Shared.PathMap),
		)
	}

	// Determine fallback stager for stage-out based on StageOutMode.
	var stageOutStager execution.Stager
	if strings.HasPrefix(cfg.StageOut, "http://") || strings.HasPrefix(cfg.StageOut, "https://") {
		stageOutStager = httpStager
	} else if strings.HasPrefix(cfg.StageOut, "s3://") {
		stageOutStager = handlers["s3"]
	} else if strings.HasPrefix(cfg.StageOut, "shock://") {
		stageOutStager = handlers["shock"]
	} else {
		stageOutStager = fileStager
	}

	stager := execution.NewCompositeStager(handlers, stageOutStager)

	client := NewClient(cfg.ServerURL, tlsCfg)
	if cfg.WorkerKey != "" {
		client.SetWorkerKey(cfg.WorkerKey)
	}

	return &Worker{
		client:            client,
		runtime:           rt,
		containerRuntime:  cfg.Runtime,
		stager:            stager,
		httpStager:        httpStager,
		parser:            parser.New(logger),
		workDir:           cfg.WorkDir,
		stageOut:          cfg.StageOut,
		stagerCfg:         cfg.Stager,
		poll:              cfg.Poll,
		gpu:               cfg.GPU,
		resources:         cfg.Resources,
		dockerHostPathMap: cfg.DockerHostPathMap,
		dockerVolume:      cfg.DockerVolume,
		inputPathMap:      cfg.InputPathMap,
		logger:            logger.With("component", "worker"),
	}, nil
}

// Run starts the main work loop. It registers with the server, then
// loops polling for tasks until the context is cancelled.
// Heartbeat runs in a separate goroutine to keep the worker alive during long tasks.
func (w *Worker) Run(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(w.workDir, 0o755); err != nil {
		return fmt.Errorf("create workdir %s: %w", w.workDir, err)
	}

	worker, err := w.client.Register(ctx, cfg.Name, cfg.Hostname, cfg.Group, cfg.Runtime)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	w.logger.Info("registered with server",
		"worker_id", worker.ID,
		"name", worker.Name,
		"group", worker.Group,
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
		return w.executeWithCWLTool(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	return w.executeLegacy(ctx, task, taskDir)
}

// executeWithCWLTool executes a task using the cwltool package with full CWL support.
func (w *Worker) executeWithCWLTool(ctx context.Context, task *model.Task, taskDir string) error {
	w.logger.Debug("executing with cwltool", "task_id", task.ID, "has_tool", true)

	// Rematerialize file literals in job inputs if paths don't exist locally.
	for k, v := range task.Job {
		if rematerialized, err := fileliteral.RematerializeRecursive(v, taskDir); err != nil {
			w.logger.Warn("rematerialize file literal", "input", k, "error", err)
		} else {
			task.Job[k] = rematerialized
		}
	}

	// Remap input paths if configured (host->container translation).
	job := task.Job
	if len(w.inputPathMap) > 0 {
		job = cwltool.RemapInputPaths(job, w.inputPathMap)
		w.logger.Debug("remapped input paths", "path_map", w.inputPathMap)
	}

	// Stage-in remote files (worker owns this step).
	stager := w.stager
	if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
		stager = w.stagerWithOverrides(task.RuntimeHints.StagerOverrides)
	}
	if err := stageRemoteInputs(ctx, stager, job, taskDir, w.logger); err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("stage-in: %w", err))
	}

	// Parse tool from task.Tool map using the proper parser.
	tool, err := w.parser.ParseToolFromMap(task.Tool)
	if err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("parse tool: %w", err))
	}

	// Build cwltool configuration.
	cfg := cwltool.Config{
		Logger:                w.logger,
		ContainerRuntime:      w.containerRuntime,
		DockerHostPathMap:     w.dockerHostPathMap,
		DockerVolume:          w.dockerVolume,
		GPU:                   toolexecGPU(w.gpu),
		MaxCPUs:               w.resources.MaxCPUs,
		MaxMemMB:              w.resources.MaxMemMB,
		ResolveSecondary:      true,
		RemoveDefaultListings: true,
	}
	if task.RuntimeHints != nil {
		cfg.ExpressionLib = task.RuntimeHints.ExpressionLib
		cfg.Namespaces = task.RuntimeHints.Namespaces
		cfg.CWLDir = task.RuntimeHints.CWLDir
	}

	// Execute the tool.
	result, err := cwltool.ExecuteTool(ctx, cfg, tool, job, taskDir)

	// Handle execution errors.
	if err != nil {
		if result != nil {
			return w.client.ReportComplete(ctx, task.ID, TaskResult{
				State:    model.TaskStateFailed,
				ExitCode: &result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr + "\n" + err.Error(),
				Outputs:  result.Outputs,
			})
		}
		return w.reportFailure(ctx, task, fmt.Errorf("execute: %w", err))
	}

	// Stage out output files (worker owns this step).
	stagedOutputs := make(map[string]any)
	for outputID, output := range result.Outputs {
		stagedOutputs[outputID] = w.stageOutputValue(ctx, output, task.ID)
	}

	return w.client.ReportComplete(ctx, task.ID, TaskResult{
		State:    model.TaskStateSuccess,
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
		GPU: GPUConfig{
			Enabled:  w.gpu.Enabled,
			DeviceID: w.gpu.DeviceID,
		},
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
			opts := execution.StageOptions{}
			loc, err := w.stager.StageOut(ctx, m, task.ID, opts)
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
				opts := execution.StageOptions{}
				loc, err := w.stager.StageOut(ctx, path, taskID, opts)
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

// stagerWithOverrides creates a stager with per-task overrides applied.
func (w *Worker) stagerWithOverrides(overrides *model.StagerOverrides) execution.Stager {
	if overrides == nil {
		return w.stager
	}

	// Convert model.StagerOverrides to execution.StagerOverrides.
	execOverrides := &execution.StagerOverrides{
		HTTPHeaders: overrides.HTTPHeaders,
	}

	if overrides.HTTPTimeoutSeconds != nil {
		timeout := time.Duration(*overrides.HTTPTimeoutSeconds) * time.Second
		execOverrides.HTTPTimeout = &timeout
	}

	if overrides.HTTPCredential != nil {
		execOverrides.HTTPCredential = &execution.CredentialSet{
			Type:        overrides.HTTPCredential.Type,
			Token:       overrides.HTTPCredential.Token,
			Username:    overrides.HTTPCredential.Username,
			Password:    overrides.HTTPCredential.Password,
			HeaderName:  overrides.HTTPCredential.HeaderName,
			HeaderValue: overrides.HTTPCredential.HeaderValue,
		}
	}

	// Create overridden HTTP stager.
	overriddenHTTP := w.httpStager.WithOverrides(execOverrides)

	// Create new composite stager with overridden HTTP handler.
	fileStager := execution.NewFileStager(w.stageOut)
	handlers := map[string]execution.Stager{
		"file":  fileStager,
		"":      fileStager,
		"http":  overriddenHTTP,
		"https": overriddenHTTP,
	}

	// Determine fallback stager for stage-out.
	var stageOutStager execution.Stager
	if strings.HasPrefix(w.stageOut, "http://") || strings.HasPrefix(w.stageOut, "https://") {
		stageOutStager = overriddenHTTP
	} else {
		stageOutStager = fileStager
	}

	return execution.NewCompositeStager(handlers, stageOutStager)
}
