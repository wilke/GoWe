package worker

import (
	"context"
	"errors"
	"fmt"
	"github.com/me/gowe/internal/hostres"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/internal/validate"
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
	containerRuntime  string                   // "docker", "apptainer", or "none"
	gpu               GPUWorkerConfig          // GPU configuration
	resources         ResourceWorkerConfig     // Resource limits
	hostID            string                   // Host identity reported at registration (#323)
	hostCPUs          int                      // Physical host CPUs (0 = unknown)
	hostMemMB         int64                    // Physical host memory in MiB (0 = unknown)
	gpuInventory      []model.GPUInfo          // GPUs reported at registration (nil unless --gpu)
	gpuLedger         *gpuLedger               // Per-task GPU assignment (--gpu with --concurrency > 1); nil = static config for every task
	usage             *usageSampler            // Host/GPU usage for heartbeats (#323); nil until Run
	imageDigests      digestCache              // sha256 cache for blessed-image checks
	imageDir          string                   // Base directory for resolving .sif image paths
	dockerHostPathMap map[string]string        // Docker-in-Docker path mapping
	dockerVolume      string                   // Named Docker volume shared with tool containers
	inputPathMap      map[string]string        // Input path mapping for host->container translation
	extraBinds        []toolexec.ExtraBind     // Extra bind mounts for containers
	datasets          map[string]string        // Merged dataset map: ID → host path
	secrets           map[string]string        // Secret env vars injected into containers
	envVars           map[string]string        // Non-secret env vars injected into containers
	wsStager          *staging.WorkspaceStager // Workspace stager for ws:// URIs (nil if disabled)
	active            *activeTaskSet           // In-flight tasks → per-task cancel funcs
	concurrency       int                      // Max tasks run at once (--concurrency; 0 or 1 = one)
	shareCores        int                      // Registered charge for a task requesting no cores (0 = not sent)
	shareRamMB        int64                    // Registered charge for a task requesting no RAM (0 = not sent)
	singleTask        atomic.Bool              // Server cannot account for several tasks: run one at a time
	keepTaskDirs      bool                     // Retain task working dirs after successful completion
	keptTaskDirTTL    time.Duration            // Remove marked kept task dirs older than this (0 = never)
	keptMu            sync.Mutex               // Serializes the kept-dir sweep with archiving/creating a task dir
	wdLock            *workDirLock             // Owner or sharer flock on the workdir (workdirlock.go; nil: none, no sweep)
	inheritHostEnv    bool                     // Degraded: local/bare tools inherit the full worker env
	noContainall      bool                     // Degraded: apptainer runs without --containall
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
	ImageDir  string               // Base directory for resolving relative .sif image paths

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

	// PreStageDir is a directory containing pre-staged reference datasets.
	// Subdirectories are auto-discovered as datasets (dirname = dataset ID).
	// Bind-mounted into every container at the same path.
	PreStageDir string

	// ExtraBinds are additional paths to bind-mount into every container.
	// Pure sysadmin escape hatch — not reported to server, not used for scheduling.
	ExtraBinds []string

	// Datasets are explicit dataset aliases: id → path.
	// Additive with auto-discovered datasets from PreStageDir.
	Datasets map[string]string

	// Secrets are environment variables injected into every container.
	// Never sent to the server or stored in task data.
	Secrets map[string]string

	// EnvVars are non-secret environment variables injected into every container.
	EnvVars map[string]string

	// Version is the build version (git commit hash), sent during registration.
	Version string

	// HostID overrides the derived host identity sent during registration
	// (--host-id, 1-64 characters of [A-Za-z0-9._-]). Empty means derive it
	// from /etc/machine-id, the boot id or the hostname (#323).
	HostID string

	// Concurrency is the maximum number of tasks run at once (--concurrency).
	// 0 means 1 (one task at a time).
	Concurrency int

	// DefaultTaskCores / DefaultTaskRamMB override the share charged to and
	// given to a task that requests no cores / no RAM when Concurrency > 1
	// (--default-task-cores, --default-task-ram). 0 means the effective
	// capacity divided by Concurrency. Ignored at Concurrency 1.
	DefaultTaskCores int
	DefaultTaskRamMB int64

	// KeepTaskDirs retains task working directories after successful
	// completion instead of deleting them (debugging aid). Directories of
	// FAILED tasks are always kept regardless of this setting.
	KeepTaskDirs bool

	// KeptTaskDirTTL removes kept task directories (failed, cancelled, debug,
	// --keep-task-dirs, earlier attempts; those with a "<base>.kept" marker)
	// once their marker is older than this (--kept-task-dir-ttl). 0 never
	// removes them; otherwise at least 1h (ValidateKeptTaskDirTTL).
	KeptTaskDirTTL time.Duration

	// InheritHostEnv makes tools that run directly on the host (local and the
	// legacy bare runtime) inherit the worker's full environment instead of
	// the allow-list. Degraded mode; reported in the registration labels.
	InheritHostEnv bool

	// ApptainerNoContainall drops --containall (keeping --cleanenv) from
	// apptainer invocations. Degraded mode; reported in the registration
	// labels.
	ApptainerNoContainall bool

	// WorkspaceStager enables the ws:// workspace stager for BV-BRC.
	WorkspaceStager bool

	// WorkspaceURL is the BV-BRC Workspace service URL (empty = default production).
	WorkspaceURL string
}

// GPUWorkerConfig holds GPU settings for the worker.
type GPUWorkerConfig struct {
	Enabled  bool   // Enable GPU support for this worker
	DeviceID string // Specific GPU device ID (e.g., "0", "1") - empty means use all/auto
}

// ResourceWorkerConfig holds resource limit settings for the worker.
type ResourceWorkerConfig struct {
	MaxCPUs          int   // Max CPUs for containers (0 = auto-detect)
	MaxMemMB         int64 // Max memory in MiB for containers (0 = auto-detect)
	ApptainerCgroups bool  // System supports cgroups v2 unified (detected at startup)
	// CgroupCPU / CgroupMemory report whether the cpu / memory controllers are
	// usable by this process (delegated to the user's systemd service when
	// rootless). Apptainer --cpus / --memory fail at container creation when
	// the matching controller is not delegated, so each flag is gated on its own.
	CgroupCPU    bool
	CgroupMemory bool
	// ApptainerCgroupsMode is auto (detect), on or off (--apptainer-cgroups).
	ApptainerCgroupsMode string
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
	if err := ValidateKeptTaskDirTTL(cfg.KeptTaskDirTTL); err != nil {
		return nil, err
	}

	// Auto-detect system resources if not explicitly set. Total-memory detection
	// is platform-specific (see worker_meminfo_*.go); it returns 0 where the OS
	// isn't supported, leaving MaxMemMB unset (no limit) — the prior behavior.
	if cfg.Resources.MaxMemMB == 0 {
		cfg.Resources.MaxMemMB = sysTotalMemMB()
	}
	if cfg.Resources.MaxCPUs == 0 {
		cfg.Resources.MaxCPUs = runtime.NumCPU()
	}
	// Decide which Apptainer resource limits (--cpus/--memory) can be enforced.
	selfCgroup, _ := os.ReadFile("/proc/self/cgroup")
	lim, err := detectCgroupLimits("/sys/fs/cgroup", os.Getuid(), cfg.Resources.ApptainerCgroupsMode, string(selfCgroup))
	if err != nil {
		return nil, err
	}
	cfg.Resources.ApptainerCgroups = lim.CPU || lim.Memory
	cfg.Resources.CgroupCPU = lim.CPU
	cfg.Resources.CgroupMemory = lim.Memory
	for _, w := range lim.Warnings {
		logger.Warn(w)
	}
	logger.Info("worker resource limits",
		"max_cpus", cfg.Resources.MaxCPUs,
		"max_mem_mb", cfg.Resources.MaxMemMB,
		"apptainer_cgroups", cfg.Resources.ApptainerCgroups,
		"cgroup_cpu", cfg.Resources.CgroupCPU,
		"cgroup_memory", cfg.Resources.CgroupMemory,
	)

	// Host identity, physical capacity and GPU inventory, reported at
	// registration only (#323). The physical totals are independent of the
	// --max-cpus / --max-mem caps above.
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	hostID, err := resolveHostID(cfg.HostID, machineIDPath, bootIDPath, hostname)
	if err != nil {
		return nil, err
	}
	var gpuInventory []model.GPUInfo
	if cfg.GPU.Enabled {
		gpuInventory = collectGPUInventory(context.Background(), logger, cfg.GPU.DeviceID)
	}
	hostCPUs, hostMemMB := hostCPUCount("/sys/devices/system/cpu/online", runtime.NumCPU()), sysTotalMemMB()
	logger.Info("worker host inventory",
		"host_id", hostID,
		"host_cpus", hostCPUs,
		"host_mem_mb", hostMemMB,
		"gpus", len(gpuInventory),
	)

	// Concurrency and the default per-task share, validated against the
	// effective capacity (max bounded by the host where known).
	slots, shareCores, shareRamMB, err := resolveConcurrency(cfg, hostCPUs, hostMemMB)
	if err != nil {
		return nil, err
	}
	if slots == 1 && (cfg.DefaultTaskCores != 0 || cfg.DefaultTaskRamMB != 0) {
		logger.Warn("--default-task-cores / --default-task-ram are ignored at --concurrency 1: a task that requests nothing gets the whole worker budget")
	}
	if slots > 1 {
		logger.Info("worker concurrency",
			"concurrency", slots,
			"default_task_cores", shareCores,
			"default_task_ram_mb", shareRamMB,
		)
	}
	// Several tasks at once on a GPU worker: each task gets its own devices
	// from the inventory (a task that needs none gets no GPU access).
	if err := validateGPUConcurrency(slots, cfg.GPU.Enabled, gpuInventory); err != nil {
		return nil, err
	}
	var ledger *gpuLedger
	if slots > 1 && cfg.GPU.Enabled {
		ledger = newGPULedger(gpuInventory)
		logger.Info("per-task GPU assignment", "devices", strings.Join(ledger.devices, ","))
	}

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

	// Add Workspace stager if enabled.
	var wsStager *staging.WorkspaceStager
	if cfg.WorkspaceStager {
		wsStager = staging.NewWorkspaceStager(staging.WorkspaceConfig{
			WorkspaceURL: cfg.WorkspaceURL,
			Timeout:      5 * time.Minute,
			MaxRetries:   3,
		}, logger)
		handlers["ws"] = wsStager
		logger.Info("Workspace stager enabled", "url", wsStager.Config().WorkspaceURL)
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
	} else if strings.HasPrefix(cfg.StageOut, "ws://") {
		stageOutStager = handlers["ws"]
	} else {
		stageOutStager = fileStager
	}

	stager := execution.NewCompositeStager(handlers, stageOutStager)

	client := NewClient(cfg.ServerURL, tlsCfg)
	if cfg.WorkerKey != "" {
		client.SetWorkerKey(cfg.WorkerKey)
	}

	// Build datasets map and extra binds from PreStageDir, Datasets, and ExtraBinds.
	datasets, extraBinds, err := hostres.ResolveBinds(logger, cfg.PreStageDir, cfg.Datasets, cfg.ExtraBinds)
	if err != nil {
		return nil, err
	}

	if len(datasets) > 0 {
		logger.Info("registered datasets", "datasets", datasets)
	}

	return &Worker{
		client:            client,
		runtime:           rt,
		containerRuntime:  model.PreferredContainerRuntime(model.ContainerRuntime(cfg.Runtime)),
		stager:            stager,
		httpStager:        httpStager,
		wsStager:          wsStager,
		parser:            parser.New(logger),
		workDir:           cfg.WorkDir,
		stageOut:          cfg.StageOut,
		stagerCfg:         cfg.Stager,
		poll:              cfg.Poll,
		gpu:               cfg.GPU,
		resources:         cfg.Resources,
		hostID:            hostID,
		hostCPUs:          hostCPUs,
		hostMemMB:         hostMemMB,
		gpuInventory:      gpuInventory,
		gpuLedger:         ledger,
		imageDir:          cfg.ImageDir,
		dockerHostPathMap: cfg.DockerHostPathMap,
		dockerVolume:      cfg.DockerVolume,
		inputPathMap:      cfg.InputPathMap,
		extraBinds:        extraBinds,
		datasets:          datasets,
		secrets:           cfg.Secrets,
		envVars:           cfg.EnvVars,
		keepTaskDirs:      cfg.KeepTaskDirs,
		keptTaskDirTTL:    cfg.KeptTaskDirTTL,
		inheritHostEnv:    cfg.InheritHostEnv,
		noContainall:      cfg.ApptainerNoContainall,
		active:            newActiveTaskSet(),
		concurrency:       slots,
		shareCores:        shareCores,
		shareRamMB:        shareRamMB,
		logger:            logger.With("component", "worker"),
	}, nil
}

// ValidateConcurrency checks --concurrency on its own: at least 1. Above 1
// together with --gpu, New also requires the GPU inventory
// (validateGPUConcurrency).
func ValidateConcurrency(n int) error {
	if n < 1 {
		return fmt.Errorf("--concurrency must be at least 1 (got %d)", n)
	}
	return nil
}

// validateGPUConcurrency refuses --gpu with more than one slot when the GPU
// inventory is empty: per-task device assignment hands out the inventory's
// devices, so without it a task would get either no GPU or every GPU.
func validateGPUConcurrency(slots int, gpu bool, inventory []model.GPUInfo) error {
	if slots > 1 && gpu && len(inventory) == 0 {
		return fmt.Errorf("--concurrency %d with --gpu: per-task GPU assignment needs the GPU inventory (nvidia-smi), and none was found (check nvidia-smi and --gpu-id, or use --concurrency 1)", slots)
	}
	if slots > 1 && gpu {
		// The server counts only inventory entries with a UUID; a device
		// without one would be handed out here but not accounted for there.
		for _, g := range inventory {
			if g.UUID == "" {
				return fmt.Errorf("--concurrency %d with --gpu: GPU %d has no UUID in the nvidia-smi inventory; per-task GPU assignment needs one for every device (use --concurrency 1)", slots, g.Index)
			}
		}
	}
	return nil
}

// resolveConcurrency validates the concurrency settings against the worker's
// effective capacity — its max CPUs / memory bounded by the host's where the
// host value is known (> 0) — and returns the slot count and the default
// share charged to and given to a task that requests no cores / no RAM. The
// share is 0 at one slot (a task then gets the whole budget, as before
// --concurrency existed); otherwise cores = max(1, CPUs/slots) and RAM =
// memory/slots (0 when memory is unknown), replaced by the override flags.
func resolveConcurrency(cfg Config, hostCPUs int, hostMemMB int64) (slots, shareCores int, shareRamMB int64, err error) {
	slots = cfg.Concurrency
	if slots == 0 {
		slots = 1 // unset (library callers): one task at a time
	}
	if err := ValidateConcurrency(slots); err != nil {
		return 0, 0, 0, err
	}
	if cfg.DefaultTaskCores < 0 || cfg.DefaultTaskRamMB < 0 {
		return 0, 0, 0, fmt.Errorf("--default-task-cores and --default-task-ram must not be negative")
	}
	if slots == 1 {
		return 1, 0, 0, nil
	}
	effCPUs := minKnown(cfg.Resources.MaxCPUs, hostCPUs)
	effMemMB := minKnown(cfg.Resources.MaxMemMB, hostMemMB)
	if slots > effCPUs {
		return 0, 0, 0, fmt.Errorf("--concurrency %d exceeds the worker's %d CPUs", slots, effCPUs)
	}
	shareCores = max(1, effCPUs/slots)
	if effMemMB > 0 {
		shareRamMB = effMemMB / int64(slots)
	}
	if cfg.DefaultTaskCores > 0 {
		if cfg.DefaultTaskCores > effCPUs {
			return 0, 0, 0, fmt.Errorf("--default-task-cores %d exceeds the worker's %d CPUs", cfg.DefaultTaskCores, effCPUs)
		}
		shareCores = cfg.DefaultTaskCores
	}
	if cfg.DefaultTaskRamMB > 0 {
		if effMemMB > 0 && cfg.DefaultTaskRamMB > effMemMB {
			return 0, 0, 0, fmt.Errorf("--default-task-ram %d MiB exceeds the worker's %d MiB", cfg.DefaultTaskRamMB, effMemMB)
		}
		shareRamMB = cfg.DefaultTaskRamMB
	}
	return slots, shareCores, shareRamMB, nil
}

// minKnown is the worker's max bounded by the host value when that is known
// (> 0); an unset max (0) takes the host value.
func minKnown[T int | int64](maxVal, host T) T {
	if host > 0 && (maxVal <= 0 || host < maxVal) {
		return host
	}
	return maxVal
}

// slotLimit is the number of tasks the worker may run at once right now: the
// configured concurrency, or 1 once the server turned out unable to account
// for several tasks (old server at registration, or a checkout without a
// charge).
func (w *Worker) slotLimit() int {
	if w.concurrency <= 1 || w.singleTask.Load() {
		return 1
	}
	return w.concurrency
}

// dropToSingleTask makes the worker run one task at a time for the rest of
// the process; tasks already running continue. It logs the WARN only on the
// first call.
func (w *Worker) dropToSingleTask(msg string, args ...any) {
	if w.concurrency > 1 && w.singleTask.CompareAndSwap(false, true) {
		w.logger.Warn(msg, append(args, "concurrency", w.concurrency)...)
	}
}

// taskShare is the per-task default given to a tool that requests no cores /
// no RAM (cwltool.Config.DefaultCores / DefaultRamMB): what the server
// charged for the task at checkout, so the worker grants exactly what was
// accounted for. Zero (whole worker budget, as at concurrency 1) unless
// several tasks may run at once and the server sent a positive charge.
// executeLegacy tasks and tools run with --runtime none are charged the same
// way but their limits are not enforced.
func (w *Worker) taskShare(task *model.Task) (cores int, ramMB int64) {
	if w.slotLimit() <= 1 {
		return 0, 0
	}
	if task.ChargedCores != nil && *task.ChargedCores > 0 {
		cores = *task.ChargedCores
	}
	if task.ChargedRamMB != nil && *task.ChargedRamMB > 0 {
		ramMB = *task.ChargedRamMB
	}
	return cores, ramMB
}

// degradedLabels returns the registration labels that flag a worker running
// with reduced isolation, so the fleet roster/UI can flag it (see server handler_fleet.go). Nil when fully isolated.
func degradedLabels(cfg Config) map[string]string {
	var labels map[string]string
	if cfg.InheritHostEnv {
		labels = map[string]string{"inherit_host_env": "true"}
	}
	if cfg.ApptainerNoContainall {
		if labels == nil {
			labels = map[string]string{}
		}
		labels["apptainer_containall"] = "false"
	}
	return labels
}

// Run starts the main work loop. It registers with the server, then
// loops polling for tasks until the context is cancelled.
// Heartbeat runs in a separate goroutine to keep the worker alive during long tasks.
func (w *Worker) Run(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(w.workDir, 0o755); err != nil {
		return fmt.Errorf("create workdir %s: %w", w.workDir, err)
	}
	// Lock on the workdir for the life of the process, with or without
	// --kept-task-dir-ttl: the owner lock if no other worker process holds it,
	// otherwise the sharer lock, which the owner's sweep sees and skips
	// (workdirlock.go).
	w.lockWorkDir()

	regOpts := RegisterOptions{
		GPUEnabled: w.gpu.Enabled,
		GPUDevice:  w.gpu.DeviceID,
		Version:    cfg.Version,
		Datasets:   w.datasets,
		Labels:     degradedLabels(cfg),
		MaxCPUs:    w.resources.MaxCPUs,
		MaxMemMB:   w.resources.MaxMemMB,
		// Host identity and inventory (#323).
		HostID:       w.hostID,
		HostCPUs:     w.hostCPUs,
		HostMemMB:    w.hostMemMB,
		Slots:        max(1, w.concurrency),
		CgroupCPU:    &w.resources.CgroupCPU,
		CgroupMemory: &w.resources.CgroupMemory,
		GPUs:         w.gpuInventory,
	}
	if w.concurrency > 1 {
		// The charge for a task that requests nothing. Not sent at one slot,
		// keeping the registration exactly as before --concurrency.
		regOpts.DefaultTaskCores = w.shareCores
		regOpts.DefaultTaskRamMB = w.shareRamMB
	}
	worker, err := w.client.Register(ctx, cfg.Name, cfg.Hostname, cfg.Group, cfg.Runtime, regOpts)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if w.concurrency > 1 && worker.DefaultTaskCores == nil {
		// A server that does not echo the share does not charge tasks at
		// checkout either, so it cannot keep several tasks within capacity.
		w.dropToSingleTask("server does not account for concurrent tasks (no default_task_cores in its registration response): running one task at a time with the whole budget per task")
	}
	w.logger.Info("registered with server",
		"worker_id", worker.ID,
		"name", worker.Name,
		"group", worker.Group,
		"runtime", worker.Runtime,
		"gpu", worker.GPUEnabled,
		"gpu_device", worker.GPUDevice,
		"max_cpus", w.resources.MaxCPUs,
		"max_mem_mb", w.resources.MaxMemMB,
		"concurrency", w.slotLimit(),
	)

	// Sample host/GPU usage in the background; heartbeats attach the latest
	// sample without waiting for one (#323).
	w.usage = newUsageSampler(w.logger, w.gpuInventory)
	go w.usage.run(ctx)

	// Start heartbeat in a separate goroutine so it continues during task execution.
	go w.heartbeatLoop(ctx)

	// Kept task dirs older than --kept-task-dir-ttl: removed in the background,
	// first right away, then hourly; never delays polling or task starts.
	if w.keptTaskDirTTL > 0 {
		go w.keptDirSweepLoop(ctx)
	}

	// Run task polling loop.
	return w.taskLoop(ctx)
}

// heartbeatLoop sends heartbeats at regular intervals until context is cancelled.
func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	var sentUsage usageSendTracker
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			usage := sentUsage.pending(w.usage.latest())
			resp, err := w.client.Heartbeat(ctx, w.active.ids(), usage)
			if err != nil {
				w.logger.Warn("heartbeat failed", "error", err)
				continue
			}
			sentUsage.sent(usage)
			// Cancel any tasks the server reports as cancelled. Cancelling the
			// task's execution context kills the underlying process and frees
			// resources (e.g. GPU) without waiting for the tool to finish.
			for _, taskID := range resp.CancelTasks {
				if w.active.cancel(taskID) {
					w.logger.Info("cancelling task on server request", "task_id", taskID)
				}
			}
		}
	}
}

// shutdownTaskWait bounds how long shutdown waits for running tasks (whose
// contexts are cancelled, so their tools are being terminated) before
// deregistering anyway. A variable so tests can shrink it.
var shutdownTaskWait = 60 * time.Second

// taskLoop polls for tasks and runs up to slotLimit() of them at once until
// the context is cancelled. On cancellation it stops polling, waits (bounded)
// for the running tasks — their contexts derive from ctx, so their tools are
// being terminated — and then deregisters.
func (w *Worker) taskLoop(ctx context.Context) error {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	// slots holds one token per running task: acquired before checkout,
	// released when the task goroutine returns. Only this goroutine acquires.
	slots := make(chan struct{}, max(1, w.concurrency))
	// freed wakes the loop when a task finishes so a free slot is refilled
	// without waiting for the next tick.
	freed := make(chan struct{}, 1)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("shutting down: waiting for running tasks", "task_ids", w.active.ids())
			w.waitForTasks(&wg)
			w.logger.Info("deregistering...")
			// Use a fresh context for deregistration.
			deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := w.client.Deregister(deregCtx)
			cancel() // Call cancel explicitly, not via defer
			if err != nil {
				w.logger.Error("deregister failed", "error", err)
			}
			return nil

		case <-ticker.C:
			w.fillSlots(ctx, slots, freed, &wg)
		case <-freed:
			w.fillSlots(ctx, slots, freed, &wg)
		}
	}
}

// waitForTasks waits for the task goroutines, at most shutdownTaskWait.
func (w *Worker) waitForTasks(wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTaskWait):
		w.logger.Warn("tasks still running after the shutdown wait; deregistering anyway (the server requeues them)",
			"task_ids", w.active.ids(), "waited", shutdownTaskWait)
	}
}

// fillSlots checks out tasks while a slot is free, starting each on its own
// goroutine. It stops at "no task", a checkout error or cancellation. At one
// slot the next checkout happens only after the running task has reported
// and its goroutine returned, as before --concurrency.
func (w *Worker) fillSlots(ctx context.Context, slots chan struct{}, freed chan<- struct{}, wg *sync.WaitGroup) {
	for ctx.Err() == nil {
		// Task goroutines only release, so a free slot seen here stays free
		// and the send below never blocks.
		if len(slots) >= w.slotLimit() {
			return
		}
		slots <- struct{}{}

		task, err := w.client.Checkout(ctx)
		if err != nil || task == nil {
			<-slots
			if err != nil && ctx.Err() == nil {
				w.logger.Error("poll error", "error", fmt.Errorf("checkout: %w", err))
			}
			return
		}

		if w.active.has(task.ID) {
			// The server requeued a task this worker is still running (its
			// heartbeats were missed) and handed it back. The run in progress
			// owns it again; a second run would share its task dir.
			<-slots
			w.logger.Warn("checked out a task this worker is already running; keeping the run in progress", "task_id", task.ID)
			return
		}

		w.logger.Info("task received", "task_id", task.ID, "step_id", task.StepID)
		if task.ChargedCores == nil {
			// The server that handed out this task does not charge resources
			// (e.g. rolled back under a running worker).
			w.dropToSingleTask("checked-out task carries no resource charge: the server cannot account for concurrent tasks, running one task at a time from now on",
				"task_id", task.ID)
		}
		if w.gpuLedger != nil && task.ChargedGPUs == nil {
			// The server does not count GPUs, so it cannot keep concurrent
			// tasks within this worker's GPUs. Tasks keep going through the
			// ledger (each gets only its own devices): others may still hold
			// devices, so no task may get every GPU.
			w.dropToSingleTask("checked-out task carries no GPU charge: the server does not count GPUs, running one task at a time from now on (each task still gets only its own GPU devices)",
				"task_id", task.ID)
		}

		// Register the task before its goroutine starts so the next
		// heartbeat lists it and the in-flight count is exact.
		runCtx, cancel := context.WithCancel(ctx)
		w.active.add(task.ID, cancel)
		wg.Add(1)
		go func() {
			// GPU devices this task acquires (runTask); released first on
			// every exit path, panic included, before the slot is freed.
			hold := &gpuHold{}
			defer func() {
				w.releaseTaskGPUs(task.ID, hold)
				<-slots
				select {
				case freed <- struct{}{}:
				default:
				}
				wg.Done()
			}()
			defer func() {
				if r := recover(); r != nil {
					w.logger.Error("task panicked", "task_id", task.ID, "panic", r, "stack", string(debug.Stack()))
					w.logTaskError(task, w.reportFailure(context.WithoutCancel(ctx), task, fmt.Errorf("worker panic: %v", r)))
				}
			}()
			w.logTaskError(task, w.runTask(runCtx, cancel, task, hold))
		}()
	}
}

// logTaskError logs a task's execution error.
func (w *Worker) logTaskError(task *model.Task, err error) {
	if err == nil || errors.Is(err, errReportDeclined) {
		// errReportDeclined: the server deliberately dropped the report (stale
		// ownership or already-terminal task); reportComplete logged it at Info.
		return
	}
	w.logger.Error("task execution failed", "task_id", task.ID, "error", err)
}

// executeTask registers task as active and runs it (see runTask).
func (w *Worker) executeTask(ctx context.Context, task *model.Task) error {
	// Per-task execution context so the heartbeat loop can cancel this specific
	// task — killing its process and freeing resources (e.g. GPU) — without
	// affecting the worker or other tasks.
	runCtx, cancel := context.WithCancel(ctx)
	w.active.add(task.ID, cancel)
	hold := &gpuHold{}
	defer w.releaseTaskGPUs(task.ID, hold)
	return w.runTask(runCtx, cancel, task, hold)
}

// runTask runs a single task already registered in w.active with cancel, the
// cancel func of runCtx: GPU devices → stage-in → run → collect outputs →
// stage-out → report. It removes the task from w.active and cancels runCtx
// on return. The GPU devices it acquires are recorded in hold; the caller
// releases them (releaseTaskGPUs) once runTask has returned.
func (w *Worker) runTask(runCtx context.Context, cancel context.CancelFunc, task *model.Task, hold *gpuHold) error {
	// The worker holds the submitter's credentials only for the lifetime of
	// this task. Deferred first so it runs after every other deferred func and
	// on every exit path: success, failure, cancellation and panic.
	defer scrubTaskCredentials(task)
	defer func() {
		w.active.remove(task.ID)
		cancel()
	}()
	// Every exit of a run that created its task dir passes here, panic
	// included: a dir kept because the run did not end with SUCCESS gets its
	// kept-dir marker. Deferred after the removal above so it runs first,
	// while the outcome recorded at the report is still in w.active.
	taskDirCreated := false
	defer func() {
		if taskDirCreated {
			w.markUnsuccessfulRun(task.ID)
		}
	}()

	// GPU access for this task: the worker's static config, or devices of its
	// own (possibly after a wait), or none.
	gpu, err := w.taskGPU(runCtx, task, hold)
	if err != nil {
		// Cancelled while waiting for devices: same decision as a cancelled
		// execution (SKIPPED for a server cancel, left for requeue on
		// shutdown); otherwise the task fails.
		if handled, cerr := w.handleExecCancellation(runCtx, task); handled {
			return cerr
		}
		return w.reportFailure(runCtx, task, err)
	}

	// The devices go back to the ledger as soon as the tool has exited, before
	// stage-out and the report: the server frees the task's GPU charge at the
	// report and may hand this worker its next GPU task right away.
	releaseGPU := func() { w.releaseTaskGPUs(task.ID, hold) }

	taskDir := filepath.Join(w.workDir, task.ID)
	// A retried or requeued task keeps its id: start in fresh directories and
	// keep what an earlier attempt left as "<id>.attempt-N" for debugging.
	archived, err := w.prepareTaskDir(task.ID)
	if archived != "" {
		w.logger.Info("previous attempt's task dir kept", "task_id", task.ID, "archived", archived)
	}
	if err != nil {
		return w.reportFailure(runCtx, task, err)
	}
	taskDirCreated = true

	// Check if task has Tool+Job (new format) or legacy _base_command.
	if task.HasTool() {
		return w.executeWithCWLTool(runCtx, task, taskDir, gpu, releaseGPU)
	}

	// Legacy path: use _base_command from task.Inputs.
	return w.executeLegacy(runCtx, task, taskDir, gpu, releaseGPU)
}

// gpuAcquireWait bounds how long a task waits for its GPU devices to be
// released by the tasks still holding them. A variable so tests can shrink it.
var gpuAcquireWait = 60 * time.Second

// taskGPUCount is the number of GPUs a checked-out task gets: the server's
// charge when present (authoritative), else what the task asks for.
func taskGPUCount(task *model.Task) int {
	if task.ChargedGPUs != nil {
		return max(0, *task.ChargedGPUs)
	}
	return task.RuntimeHints.GPUCount()
}

// taskGPU returns the GPU configuration task runs with. Without a device
// ledger (one task at a time, or no --gpu) every task gets the worker's
// static configuration, as before per-task assignment. With the ledger, a
// task needing k > 0 GPUs (taskGPUCount) first acquires k devices into hold,
// waiting at most gpuAcquireWait (a WARN is logged once when it has to wait);
// a task needing none gets GPU access denied. An error means the task cannot
// run: ctx was cancelled (ctx.Err()), the wait timed out, or the task needs
// more GPUs than the worker has.
func (w *Worker) taskGPU(ctx context.Context, task *model.Task, hold *gpuHold) (GPUConfig, error) {
	if w.gpuLedger == nil {
		return toolexecGPU(w.gpu), nil
	}
	k := taskGPUCount(task)
	if k == 0 {
		return GPUConfig{AccessDenied: true}, nil
	}
	if n := w.gpuLedger.size(); k > n {
		return GPUConfig{}, fmt.Errorf("task needs %d GPUs but this worker has %d", k, n)
	}
	actx, cancel := context.WithTimeout(ctx, gpuAcquireWait)
	defer cancel()
	devices, err := w.gpuLedger.acquire(actx, k, func() {
		w.logger.Warn("waiting for GPU devices still held by another task of this worker (its process outlived the server's charge)",
			"task_id", task.ID, "gpus", k, "max_wait", gpuAcquireWait)
	})
	if err != nil {
		if ctx.Err() != nil {
			return GPUConfig{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return GPUConfig{}, fmt.Errorf("no %d free GPU devices after waiting %s: other tasks of this worker still hold them", k, gpuAcquireWait)
		}
		return GPUConfig{}, fmt.Errorf("acquire GPU devices: %w", err)
	}
	hold.devices = devices
	w.logger.Info("GPU devices assigned", "task_id", task.ID, "devices", strings.Join(devices, ","))
	return GPUConfig{Enabled: true, Devices: devices}, nil
}

// releaseTaskGPUs returns the devices recorded in hold to the ledger.
func (w *Worker) releaseTaskGPUs(taskID string, hold *gpuHold) {
	if w.gpuLedger == nil || hold == nil || len(hold.devices) == 0 {
		return
	}
	w.gpuLedger.release(hold.devices)
	w.logger.Debug("GPU devices released", "task_id", taskID, "devices", strings.Join(hold.devices, ","))
	hold.devices = nil
}

// cleanupTaskDir removes a completed task's working directories (the task dir
// and its "_tmp" / "_staging" siblings). Called only after a SUCCESS result was
// accepted by the server — FAILED and SKIPPED (cancelled) tasks deliberately
// never reach it, so their directories stay around for debugging. The
// directories are also kept when --keep-task-dirs is set, when the task
// belongs to a debug submission (RuntimeHints.Debug, from `gowe submit
// --debug`), and when any reported output still resolves into the task dir —
// which happens with in-place stage-out (e.g. FileStager "local" mode) or
// when a StageOut failed and left the local path; deleting the dir then would
// destroy outputs that downstream tasks reference. Earlier attempts'
// directories ("<id>.attempt-N", see archivePreviousAttempt) are always kept.
//
// Kept-dir markers (keptdirs.go): a dir kept for --keep-task-dirs or a debug
// submission gets one (so --kept-task-dir-ttl may remove it later), but only
// if it is still the directory this run created (ownsTaskDir); a dir kept
// because outputs are referenced in place never does (an old marker is
// removed), whatever the other settings; a leftover marker goes once the
// directories were removed.
func (w *Worker) cleanupTaskDir(task *model.Task, outputs map[string]any) {
	taskID := task.ID
	taskDir := filepath.Join(w.workDir, taskID)
	if abs, err := filepath.Abs(taskDir); err == nil {
		taskDir = abs
	}
	// The "_staging" sibling holds reconstructed input directories and file
	// literals; a passthrough output may reference them in place.
	if outputsReferenceDir(outputs, taskDir) || outputsReferenceDir(outputs, taskDir+"_staging") {
		w.logger.Debug("keeping task dir: outputs are referenced in place", "task_id", taskID)
		w.removeKeptMarker(taskID)
		return
	}
	if w.keepTaskDirs {
		if w.ownsTaskDir(taskID) {
			w.markKeptDir(taskID, taskID, keptReasonKeepTaskDirs)
		}
		return
	}
	if task.RuntimeHints != nil && task.RuntimeHints.Debug {
		w.logger.Debug("keeping task dir: debug submission", "task_id", taskID)
		if w.ownsTaskDir(taskID) {
			w.markKeptDir(taskID, taskID, keptReasonDebug)
		}
		return
	}
	removed := true
	for _, d := range []string{taskDir, taskDir + "_tmp", taskDir + "_staging"} {
		if err := os.RemoveAll(d); err != nil {
			w.logger.Warn("cleanup task dir", "dir", d, "error", err)
			removed = false
		}
	}
	if removed {
		w.removeKeptMarker(taskID)
		w.logger.Debug("task dir removed", "task_id", taskID)
	}
}

// outputsReferenceDir reports whether any output location resolves into dir.
// File/Directory objects are judged by their effective location ("location" if
// staged, else the local "path"); bare strings (legacy staged locations) are
// checked directly. The stale "path" left behind on a File whose "location"
// was staged elsewhere is deliberately ignored.
func outputsReferenceDir(v any, dir string) bool {
	underDir := func(loc string) bool {
		p := strings.TrimPrefix(loc, "file://")
		return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch val := v.(type) {
		case string:
			return underDir(val)
		case map[string]any:
			if class, _ := val["class"].(string); class == "File" || class == "Directory" {
				loc, _ := val["location"].(string)
				if loc == "" {
					loc, _ = val["path"].(string)
				}
				if underDir(loc) {
					return true
				}
				return walk(val["secondaryFiles"]) || walk(val["listing"])
			}
			for _, item := range val {
				if walk(item) {
					return true
				}
			}
		case []any:
			for _, item := range val {
				if walk(item) {
					return true
				}
			}
		case []map[string]any:
			for _, item := range val {
				if walk(item) {
					return true
				}
			}
		case []string:
			for _, item := range val {
				if underDir(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

// redactSecretsMinLength is the minimum secret value length eligible for
// redaction, tied to model.MinSecretValueBytes (matching
// toolexec.secretMaskMinLength) — submission secrets are validated to be at
// least this long (model.ValidateSecrets, #260 fix round), so every real
// secret is coverable.
const redactSecretsMinLength = model.MinSecretValueBytes

// redactSecrets replaces every non-trivial secret value in s with a placeholder,
// so an injected credential a tool echoed into its output (e.g. via `env` or
// `set -x`) is never transmitted to the server or shown in logs. Values
// shorter than redactSecretsMinLength bytes are skipped to avoid
// pathological over-redaction of common substrings; real tokens are far
// longer.
func redactSecrets(s string, secrets map[string]string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	for _, v := range secrets {
		if len(v) >= redactSecretsMinLength {
			s = strings.ReplaceAll(s, v, "***REDACTED***")
		}
	}
	return s
}

// mergeSecretsForRedaction unions a and b into one map for use with
// redactSecrets, so callers can cover both cfg.SecretEnvVars (worker-level
// --secret defaults, injected BVBRC_TOKEN/KB_AUTH_TOKEN, env-exposed task
// secrets) and cfg.SecretValues (the full task secret set, including
// job-only cwltool:Secrets values ApplySecrets never puts in SecretEnvVars)
// in one redaction pass without either map having to be a superset of the
// other.
func mergeSecretsForRedaction(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		merged[k] = v
	}
	for k, v := range b {
		merged[k] = v
	}
	return merged
}

// reportRetryBackoff is the base delay between report attempts (attempt n
// waits (n-1)×this). A variable so tests can shrink it.
var reportRetryBackoff = 500 * time.Millisecond

// errReportDeclined marks a report the server deliberately refused (HTTP 409):
// the task is already terminal, or it is owned by another worker (it was
// requeued and re-checked-out while this worker was still finishing). The
// result is dropped and the task dir kept — callers skip cleanup on error —
// and the report is never retried.
var errReportDeclined = errors.New("server declined report")

// reportComplete reports a final task result, detaching from ctx cancellation so a
// cancelled or killed task can still be reported (bounded by a short timeout).
// Transient failures (network errors, 5xx, 429, 408) are retried up to 3
// attempts with a short backoff; a 409 is a deliberate server-side drop and
// is never retried.
//
// Every final result of a run passes here, so it records the run's outcome
// for the kept-dir marker (markUnsuccessfulRun) before reporting: SUCCESS
// (cleanupTaskDir decides; a SUCCESS the server did not accept stays without a
// marker, as the server may still hold it with outputs in place), SKIPPED
// ("cancelled") or FAILED ("failed").
func (w *Worker) reportComplete(ctx context.Context, taskID string, result TaskResult) error {
	switch result.State {
	case model.TaskStateSuccess:
		w.active.setOutcome(taskID, runOutcomeSuccess)
	case model.TaskStateSkipped:
		w.active.setOutcome(taskID, keptReasonCancelled)
	default:
		w.active.setOutcome(taskID, keptReasonFailed)
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	const attempts = 3
	var lastErr error
	for i := 1; i <= attempts; i++ {
		if i > 1 {
			select {
			case <-time.After(time.Duration(i-1) * reportRetryBackoff):
			case <-rctx.Done():
				return lastErr
			}
		}
		err := w.client.ReportComplete(rctx, taskID, result)
		if err == nil {
			return nil
		}
		var se *StatusError
		if errors.As(err, &se) {
			if se.StatusCode == http.StatusConflict {
				// Deliberate drop: keep the task dir, drop the result.
				w.logger.Info("server declined report: dropping result, keeping task dir",
					"task_id", taskID, "detail", se.Body)
				return fmt.Errorf("%w: %s", errReportDeclined, se.Body)
			}
			if se.StatusCode < 500 && se.StatusCode != http.StatusTooManyRequests && se.StatusCode != http.StatusRequestTimeout {
				// Other 4xx (e.g. 404): retrying cannot help. 429 and 408 are
				// transient (rate limiting, request timeout) and fall through
				// to the retry below alongside network errors and 5xx.
				return err
			}
		}
		lastErr = err
		if i < attempts {
			w.logger.Warn("report failed, will retry", "task_id", taskID,
				"attempt", i, "max_attempts", attempts, "error", err)
		}
	}
	return lastErr
}

// reportCancelled reports a task as SKIPPED (the scheduler's terminal state for a
// cancelled task), used when the server has cancelled the task mid-execution.
func (w *Worker) reportCancelled(ctx context.Context, task *model.Task) error {
	exitCode := -1
	err := w.reportComplete(ctx, task.ID, TaskResult{
		State:    model.TaskStateSkipped,
		ExitCode: &exitCode,
		Stderr:   "task cancelled by server",
	})
	if errors.Is(err, errReportDeclined) {
		// Routine after a cancel fan-out: the server's cancel already SKIPPED
		// the task, so its 409 to our SKIPPED report is the expected outcome,
		// not an error.
		w.logger.Info("cancel report declined: task already terminal on server",
			"task_id", task.ID)
		return nil
	}
	return err
}

// handleExecCancellation reports the right terminal outcome when tool execution
// returns an error and the run context was cancelled. It returns (handled, error):
// handled=false means the cancellation did not apply and the caller should fall
// back to normal failure reporting.
func (w *Worker) handleExecCancellation(runCtx context.Context, task *model.Task) (bool, error) {
	switch {
	case w.active.wasCancelled(task.ID):
		// Explicit server-driven cancellation → report SKIPPED (terminal).
		w.logger.Info("task cancelled by server, reporting SKIPPED", "task_id", task.ID)
		return true, w.reportCancelled(runCtx, task)
	case runCtx.Err() != nil:
		// Context cancelled but not an explicit task cancel → the worker is
		// shutting down. Leave the task non-terminal so deregistration (or the
		// server reaper) requeues it rather than marking it FAILED.
		w.logger.Info("worker shutting down mid-task, leaving for requeue", "task_id", task.ID)
		w.active.setOutcome(task.ID, keptReasonCancelled)
		return true, runCtx.Err()
	default:
		return false, nil
	}
}

// executeWithCWLTool executes a task using the cwltool package with full CWL
// support. gpu is the task's GPU configuration (taskGPU).
func (w *Worker) executeWithCWLTool(ctx context.Context, task *model.Task, taskDir string, gpu GPUConfig, releaseGPU func()) error {
	w.logger.Debug("executing with cwltool", "task_id", task.ID, "has_tool", true)

	// Rematerialize file literals in job inputs if paths don't exist locally.
	for k, v := range task.Job {
		if rematerialized, err := fileliteral.RematerializeRecursive(v, taskDir); err != nil {
			w.logger.Warn("rematerialize file literal", "input", k, "error", err)
		} else {
			task.Job[k] = rematerialized
		}
	}

	// Build cwltool configuration.
	cfg := cwltool.Config{
		Logger:                w.logger,
		ContainerRuntime:      w.containerRuntime,
		DockerHostPathMap:     w.dockerHostPathMap,
		DockerVolume:          w.dockerVolume,
		GPU:                   gpu,
		MaxCPUs:               w.resources.MaxCPUs,
		MaxMemMB:              w.resources.MaxMemMB,
		ApptainerCgroups:      w.resources.ApptainerCgroups,
		CgroupCPU:             w.resources.CgroupCPU,
		CgroupMemory:          w.resources.CgroupMemory,
		ImageDir:              w.imageDir,
		ResolveSecondary:      task.StepInstanceID == "",
		RemoveDefaultListings: true,
		ExtraBinds:            w.extraBinds,
		SecretEnvVars:         w.secrets,
		EnvVars:               w.envVars,
		InheritHostEnv:        w.inheritHostEnv,
		ApptainerNoContainall: w.noContainall,
	}
	// File literals go below the task's staging dir, not the shared
	// $TMPDIR/cwl-literals: concurrent tasks may carry literals with the same
	// basename and different contents.
	cfg.LiteralDir = taskStagingDir(taskDir)
	// With several tasks at once, a tool that requests no cores / no RAM
	// gets what the server charged for it instead of the whole budget.
	cfg.DefaultCores, cfg.DefaultRamMB = w.taskShare(task)
	if task.RuntimeHints != nil {
		cfg.ExpressionLib = task.RuntimeHints.ExpressionLib
		cfg.Namespaces = task.RuntimeHints.Namespaces
		cfg.CWLDir = task.RuntimeHints.CWLDir
		cfg.InputValidation = validate.Mode(task.RuntimeHints.InputValidation)
	}

	// The delegated token is always part of the redaction mask for the
	// reported stdout/stderr/outputs, even for staging-only tasks, and is
	// captured before staging. On-disk task files are NOT rewritten.
	stageToken := taskToken(task.RuntimeHints)
	var tokenMask map[string]string
	if len(stageToken) >= redactSecretsMinLength {
		tokenMask = map[string]string{"_delegated_token": stageToken}
	}

	// Parse the tool up front: the image check below needs its image ref, and
	// a malformed tool should fail before any data is staged.
	tool, err := w.parser.ParseToolFromMap(task.Tool)
	if err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("parse tool: %w", err))
	}

	// Token injection into the tool environment: only for a scheduler-blessed
	// task whose image hashes to a blessed digest (verified here, before
	// launch, and the same resolved path is what gets launched). Otherwise
	// the token stays staging-only.
	if wantsTokenInjection(task.RuntimeHints, w.logger, task.ID) {
		if err := prepareTokenImage(&cfg, tool, task.RuntimeHints, &w.imageDigests); err != nil {
			return w.reportFailure(ctx, task, err)
		}
		cfg.SecretEnvVars = injectBVBRCTokenEnv(cfg.SecretEnvVars, task.RuntimeHints)
	}

	// Deliver the task's opted-in submission secrets (gowe:Execution
	// secret_env/inject_secrets, limited to SecretEnvNames — M13) as
	// container env vars, and re-inject cwltool:Secrets-declared inputs
	// stripped from the persisted job at submission time into a COPY of
	// task.Job (task.Job itself is never mutated) — this is the same
	// internal/cwltool.ApplySecrets path local/docker executors now use
	// (H4), so all three deliver secrets identically. Must happen before
	// tool evaluation (parameter references, JS,
	// InitialWorkDirRequirement) sees the job below.
	job, err := cwltool.ApplySecrets(&cfg, task, w.logger)
	if err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("apply secrets: %w", err))
	}
	// The delegated token also joins the in-execution mask set (redaction
	// only; SecretValues is never delivered to the tool), so it is masked
	// before toolexec cuts the stderr tail for an output-collection error.
	// Redacting only afterwards would miss a token straddling that cut.
	cfg.SecretValues = mergeSecretsForRedaction(cfg.SecretValues, tokenMask)

	// Remap input paths if configured (host->container translation).
	if len(w.inputPathMap) > 0 {
		job = cwltool.RemapInputPaths(job, w.inputPathMap)
		w.logger.Debug("remapped input paths", "path_map", w.inputPathMap)
	}

	// Stage-in remote files (worker owns this step).
	stager := w.stager
	if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
		stager = w.stagerWithOverrides(task.RuntimeHints.StagerOverrides)
	}
	stageInStart := time.Now()
	if err := stageRemoteInputs(ctx, stager, job, taskDir, w.logger); err != nil {
		// Interrupted by a cancel: SKIPPED for a server cancel, left for
		// requeue on worker shutdown — not a FAILED that consumes a retry.
		if handled, cerr := w.handleExecCancellation(ctx, task); handled {
			return cerr
		}
		return w.reportFailure(ctx, task, fmt.Errorf("stage-in: %w", err))
	}
	stageInMs := time.Since(stageInStart).Milliseconds()

	// Execute the tool.
	result, err := cwltool.ExecuteTool(ctx, cfg, tool, job, taskDir)
	releaseGPU()

	// Build the redaction mask now, then drop the config's references to the
	// secret maps. Go strings cannot be zeroed; this releases the last
	// references this function holds so the values become collectable.
	maskSet := mergeSecretsForRedaction(mergeSecretsForRedaction(cfg.SecretEnvVars, cfg.SecretValues), tokenMask)
	cfg.SecretEnvVars = nil
	cfg.SecretValues = nil

	// Redact any injected secret values (e.g. BVBRC_TOKEN/KB_AUTH_TOKEN, task
	// secrets, and job-only cwltool:Secrets values) a tool may have echoed
	// into its captured stdout/stderr before those logs leave the worker for
	// the server. cfg.SecretValues (populated by ApplySecrets) is the full
	// set in play, a superset of cfg.SecretEnvVars — using it here keeps
	// redaction coverage exactly as broad as before M13 narrowed
	// SecretEnvVars to the env-exposed subset. The trust-boundary invariant
	// (SPECIFICATION.md §13.2) requires injected secrets never be
	// transmitted to or persisted by the server.
	if result != nil {
		result.Stdout = redactSecrets(result.Stdout, maskSet)
		result.Stderr = redactSecrets(result.Stderr, maskSet)
	}

	// Handle execution errors.
	if err != nil {
		// If the run context was cancelled, report the cancellation outcome
		// (SKIPPED for an explicit server cancel; leave for requeue on shutdown)
		// rather than a spurious FAILED.
		if handled, cerr := w.handleExecCancellation(ctx, task); handled {
			return cerr
		}
		errText := redactSecrets(err.Error(), maskSet)
		if result != nil {
			// This attempt did stage in (we got as far as running the tool),
			// so attach StageInMs to the report even though it's a failure.
			return w.reportComplete(ctx, task.ID, TaskResult{
				State:     model.TaskStateFailed,
				ExitCode:  &result.ExitCode,
				Stdout:    result.Stdout,
				Stderr:    result.Stderr + "\n" + errText,
				Outputs:   w.redactOutputsForReport(task.ID, result.Outputs, stageToken),
				StageInMs: &stageInMs,
			})
		}
		return w.reportFailure(ctx, task, fmt.Errorf("execute: %w", newRedactedError(err, maskSet)))
	}

	// Stage out output files (worker owns this step).
	// Use per-task stager if overrides exist (e.g., user token for ws:// uploads).
	outStager := w.stager
	if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
		outStager = w.stagerWithOverrides(task.RuntimeHints.StagerOverrides)
		w.logger.Info("using per-task stager with overrides", "task_id", task.ID,
			"has_ws_dest", task.RuntimeHints.OutputDestination != "")
	}
	stageOutStart := time.Now()
	stagedOutputs := make(map[string]any)
	walker := w.newOutputStager(ctx, task, outStager, taskDir)
	for outputID, output := range result.Outputs {
		w.logger.Debug("staging output", "task_id", task.ID, "output_id", outputID)
		stagedOutputs[outputID] = walker.stage(output, "")
	}
	stageOutMs := time.Since(stageOutStart).Milliseconds()

	// A cancel during stage-out leaves outputs at their local paths; never
	// report that as SUCCESS. Same decision as a cancelled execution. A
	// shutdown that lands after every output staged changes nothing: the
	// finished task is reported below.
	if walker.failed > 0 || w.active.wasCancelled(task.ID) {
		if handled, cerr := w.handleExecCancellation(ctx, task); handled {
			return cerr
		}
	}

	// Two distinct sources mapping to one destination would silently
	// overwrite each other; fail the task instead. The task dir is kept.
	// The collision is a property of the tool's output layout, so a retry
	// would fail identically: report it as permanent.
	if stageErr := walker.err(); stageErr != nil {
		return w.reportComplete(ctx, task.ID, TaskResult{
			State:      model.TaskStateFailed,
			ExitCode:   &result.ExitCode,
			Stdout:     result.Stdout,
			Stderr:     result.Stderr + "\n" + stageErr.Error(),
			Outputs:    w.redactOutputsForReport(task.ID, stagedOutputs, stageToken),
			StageInMs:  &stageInMs,
			StageOutMs: &stageOutMs,
			Permanent:  true,
		})
	}

	if err := w.reportComplete(ctx, task.ID, TaskResult{
		State:      model.TaskStateSuccess,
		ExitCode:   &result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		Outputs:    w.redactOutputsForReport(task.ID, stagedOutputs, stageToken),
		StageInMs:  &stageInMs,
		StageOutMs: &stageOutMs,
	}); err != nil {
		return err
	}
	w.cleanupTaskDir(task, stagedOutputs)
	return nil
}

// redactOutputsForReport scans outputs for the exact delegated token and
// replaces any occurrence with "***REDACTED***" before they leave the worker,
// logging a WARN with the task id (never the value).
func (w *Worker) redactOutputsForReport(taskID string, outputs map[string]any, token string) map[string]any {
	if len(token) < redactSecretsMinLength {
		return outputs
	}
	redacted, found := redactTokenInOutputs(outputs, token)
	if found {
		w.logger.Warn("delegated token found in task outputs; redacted before reporting", "task_id", taskID)
	}
	return redacted
}

// executeLegacy executes a task using the legacy _base_command approach.
// The server charges such a task like any other, but no cores/RAM limit is
// applied to the command (as before --concurrency). gpu is the task's GPU
// configuration (taskGPU).
func (w *Worker) executeLegacy(ctx context.Context, task *model.Task, taskDir string, gpu GPUConfig, releaseGPU func()) error {
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
		Image:                 image,
		Command:               command,
		WorkDir:               taskDir,
		Volumes:               volumes,
		GPU:                   gpu,
		InheritHostEnv:        w.inheritHostEnv,
		ApptainerNoContainall: w.noContainall,
	}

	// Bare (host) execution: parity with executeLocal -- HOME is the task dir
	// and TMPDIR a private sibling, since the allow-listed env has neither.
	if _, bare := w.runtime.(*BareRuntime); bare {
		tmpDir := taskDir + "_tmp"
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			return w.reportFailure(ctx, task, fmt.Errorf("create tmp dir: %w", err))
		}
		spec.Env = map[string]string{"HOME": taskDir, "TMPDIR": tmpDir}
	}

	w.logger.Debug("executing task (legacy)",
		"task_id", task.ID,
		"image", image,
		"command", command,
	)

	result, runErr := w.runtime.Run(ctx, spec)
	releaseGPU()
	if runErr != nil {
		// Cancellation takes precedence over a generic runtime error.
		if handled, cerr := w.handleExecCancellation(ctx, task); handled {
			return cerr
		}
		// Runtime infrastructure error (e.g., binary not found).
		return w.reportFailure(ctx, task, runErr)
	}

	// Collect outputs via glob patterns.
	outputs := make(map[string]any)
	stageOutFailed := false
	for outputID, pattern := range outputGlobs {
		matches, err := filepath.Glob(filepath.Join(taskDir, pattern))
		if err != nil {
			continue
		}

		// Stage-out matched files.
		var staged []string
		for _, m := range matches {
			if abs, err := filepath.Abs(m); err == nil {
				m = abs
			}
			opts := execution.StageOptions{}
			if absTaskDir, err := filepath.Abs(taskDir); err == nil {
				if rel, err := filepath.Rel(absTaskDir, m); err == nil && rel != "." && filepath.IsLocal(rel) {
					opts.RelPath = filepath.ToSlash(rel)
				}
			}
			loc, err := w.stager.StageOut(ctx, m, task.ID, opts)
			if err != nil {
				stageOutFailed = true
				// Report the local location instead of dropping the output:
				// the file in the task dir is the only copy, and referencing
				// it keeps cleanupTaskDir from deleting it.
				w.logger.Warn("stage-out failed, reporting local location", "file", m, "error", err)
				loc = cwl.BuildLocation(cwl.SchemeFile, m)
			}
			staged = append(staged, loc)
		}

		if len(staged) == 1 {
			outputs[outputID] = staged[0]
		} else if len(staged) > 1 {
			outputs[outputID] = staged
		}
	}

	// A cancel after the command finished (killed with a non-zero exit, or
	// during stage-out, leaving local locations) must not be reported as
	// FAILED or SUCCESS. Same decision as a cancelled run. A shutdown that
	// lands after a clean exit and a complete stage-out changes nothing.
	if result.ExitCode != 0 || stageOutFailed || w.active.wasCancelled(task.ID) {
		if handled, cerr := w.handleExecCancellation(ctx, task); handled {
			return cerr
		}
	}

	// Determine state from exit code.
	state := model.TaskStateSuccess
	if result.ExitCode != 0 {
		state = model.TaskStateFailed
	}

	if err := w.reportComplete(ctx, task.ID, TaskResult{
		State:    state,
		ExitCode: &result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Outputs:  outputs,
	}); err != nil {
		return err
	}
	if state == model.TaskStateSuccess {
		w.cleanupTaskDir(task, outputs)
	}
	return nil
}

// reportFailure sends a FAILED completion with the given error as stderr.
// Permanent is set when execErr wraps validate.ErrInputValidation (#273): a
// bad input value fails identically on every retry, so the server should
// make this FAILED terminal instead of consuming the task's retry budget.
func (w *Worker) reportFailure(ctx context.Context, task *model.Task, execErr error) error {
	// The delegated token is unconditionally part of the mask for anything
	// reported, whichever path failed.
	if tok := taskToken(task.RuntimeHints); len(tok) >= redactSecretsMinLength {
		execErr = newRedactedError(execErr, map[string]string{"_delegated_token": tok})
	}
	exitCode := -1
	reportErr := w.reportComplete(ctx, task.ID, TaskResult{
		State:     model.TaskStateFailed,
		ExitCode:  &exitCode,
		Stderr:    execErr.Error(),
		Permanent: validate.IsPermanent(execErr),
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

	// Override workspace stager with per-task token if available.
	if w.wsStager != nil {
		if overrides.HTTPCredential != nil && overrides.HTTPCredential.Token != "" {
			handlers["ws"] = w.wsStager.WithToken(overrides.HTTPCredential.Token)
		} else {
			handlers["ws"] = w.wsStager
		}
	}

	// Determine fallback stager for stage-out.
	var stageOutStager execution.Stager
	if strings.HasPrefix(w.stageOut, "http://") || strings.HasPrefix(w.stageOut, "https://") {
		stageOutStager = overriddenHTTP
	} else if strings.HasPrefix(w.stageOut, "ws://") && handlers["ws"] != nil {
		stageOutStager = handlers["ws"]
	} else {
		stageOutStager = fileStager
	}

	return execution.NewCompositeStager(handlers, stageOutStager)
}
