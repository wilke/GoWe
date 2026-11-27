package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Service mode (gowe-worker --service-mode).
//
// A service-mode worker runs gowe:Service tasks only, each as a long-lived
// process: the task's goroutine holds its slot, its GPU devices and its entry
// in w.active from checkout until the service has ended and been reported,
// so --concurrency is the most services it runs at once. A service gets the
// lowest free port of --service-port-range in its port input, runs through
// the ordinary Apptainer launch (toolexec.ServiceLaunch: host network, log
// files in the task dir, own process group, no time limit) and is checked
// over HTTP on 127.0.0.1 (servicehealth.go), counted as passed only while
// the listener on its port belongs to its own process tree. It ends FAILED
// when the process exits or never becomes ready, SKIPPED when the server asks
// to stop it (heartbeat cancel_tasks), and FAILED when the worker shuts down
// (services are not kept across a worker restart in this version; the state
// file and start-up sweep of servicestate.go stop what a dead worker left
// running). Its task dir (mode 0700) is always kept, with a kept-dir marker.

// Port range of services (--service-port-range).
const (
	DefaultServicePortRange = "20000-20999"
	DefaultServicePortLo    = 20000
	DefaultServicePortHi    = 20999
	minServicePort          = 1024
	maxServicePort          = 65535
)

// Log files of a service, in its task dir.
const (
	serviceStdoutFile = "gowe-service.stdout.log"
	serviceStderrFile = "gowe-service.stderr.log"
)

// serviceLogMaskMargin is read before the reported tail of a service log so
// that a secret straddling the cut is masked before cutting.
const serviceLogMaskMargin = 64 * 1024

// serviceForeignPortChecks is the number of consecutive checks, before the
// service was ever ready, in which the HTTP check passed but the port's
// listener was not the service's that end the service FAILED: another
// process holds its port, so it can never become ready.
const serviceForeignPortChecks = 3

// Durations of service supervision. Variables so tests can shrink them.
var (
	// serviceTimeUnit is one second of a gowe:Service spec (start timeout,
	// check interval, stop grace).
	serviceTimeUnit = time.Second
	// serviceShutdownWait bounds the shutdown wait of a service-mode worker:
	// the largest stop grace a service can have (600 s) plus 30 s. Fixed, not
	// computed from the services running at the time.
	serviceShutdownWait = 630 * time.Second
)

// serviceModeGOOS is the system service mode checks for (Linux only). A
// variable so tests can pretend another one.
var serviceModeGOOS = runtime.GOOS

// Test hooks; nil in production.
var (
	// testHookBeforeServiceLaunch runs right before the last cancellation
	// check before a service is launched.
	testHookBeforeServiceLaunch func(taskID string)
	// testHookAfterServiceLaunch runs right after the handle of a launched
	// service is obtained (and its stop deferred), with the task.
	testHookAfterServiceLaunch func(task *model.Task)
	// testHookAbandonService, when it returns true at the worker's shutdown,
	// leaves the service running without stopping or reporting it and keeps
	// its state file, as if the worker had died.
	testHookAbandonService func(taskID string) bool
)

// errServiceAbandoned is returned by superviseService when
// testHookAbandonService abandoned the service.
var errServiceAbandoned = errors.New("service abandoned (test hook)")

// serviceSettings are the resolved service-mode settings (New).
type serviceSettings struct {
	advertiseHost  string
	portLo, portHi int
}

// ParseServicePortRange parses --service-port-range "LO-HI" and checks it:
// 1024 <= LO <= HI <= 65535 and at least concurrency ports.
func ParseServicePortRange(s string, concurrency int) (lo, hi int, err error) {
	los, his, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("--service-port-range %q: want LO-HI, e.g. %s", s, DefaultServicePortRange)
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(los))
	hi, err2 := strconv.Atoi(strings.TrimSpace(his))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("--service-port-range %q: want LO-HI with two port numbers, e.g. %s", s, DefaultServicePortRange)
	}
	if err := validateServicePorts(lo, hi, concurrency); err != nil {
		return 0, 0, err
	}
	return lo, hi, nil
}

// validateServicePorts checks a service port range against the number of
// services the worker may run at once (one port each).
func validateServicePorts(lo, hi, slots int) error {
	if lo < minServicePort || hi > maxServicePort || lo > hi {
		return fmt.Errorf("--service-port-range %d-%d: ports must lie within %d-%d, LO <= HI", lo, hi, minServicePort, maxServicePort)
	}
	if n := hi - lo + 1; n < max(1, slots) {
		return fmt.Errorf("--service-port-range %d-%d has %d ports, fewer than --concurrency %d (each service needs one)", lo, hi, n, slots)
	}
	return nil
}

// ServiceConcurrencyError is the refusal of --service-mode with more than
// model.MaxHeartbeatServices slots, or nil.
func ServiceConcurrencyError(concurrency int) error {
	if concurrency > model.MaxHeartbeatServices {
		return fmt.Errorf("--service-mode allows at most --concurrency %d (got %d): the server reads at most %d service statuses per heartbeat", model.MaxHeartbeatServices, concurrency, model.MaxHeartbeatServices)
	}
	return nil
}

// resolveServiceSettings checks the service-mode requirements at start-up
// and resolves the port range and the advertised host. Without ServiceMode it
// only refuses service settings given anyway. hostname is the name sent at
// registration, the default advertised host.
func resolveServiceSettings(cfg Config, hostname, hostID string, inventory []model.GPUInfo, slots int) (serviceSettings, error) {
	if !cfg.ServiceMode {
		if cfg.AdvertiseHost != "" || cfg.ServicePortLo != 0 || cfg.ServicePortHi != 0 {
			return serviceSettings{}, errors.New("--service-port-range and --advertise-host require --service-mode")
		}
		return serviceSettings{}, nil
	}
	if serviceModeGOOS != "linux" {
		return serviceSettings{}, fmt.Errorf("--service-mode runs on Linux only (this system is %s): the worker verifies through /proc that a service's port is held by the service", serviceModeGOOS)
	}
	if strings.TrimSpace(cfg.Runtime) != string(model.RuntimeApptainer) {
		return serviceSettings{}, fmt.Errorf("--service-mode requires --runtime apptainer and no other runtime (got %q): a service runs only in an Apptainer container", cfg.Runtime)
	}
	if err := ServiceConcurrencyError(slots); err != nil {
		return serviceSettings{}, err
	}
	if cfg.WorkerKey == "" {
		return serviceSettings{}, errors.New("--service-mode requires a worker key (--worker-key-file): the server accepts service mode only from an authenticated worker")
	}
	if hostID == "" {
		return serviceSettings{}, errors.New("--service-mode requires a host id, and none could be derived (no /etc/machine-id, boot id or hostname): set --host-id")
	}
	if cfg.InheritHostEnv {
		return serviceSettings{}, errors.New("--service-mode cannot be combined with --inherit-host-env")
	}
	if cfg.ApptainerNoContainall {
		return serviceSettings{}, errors.New("--service-mode cannot be combined with --apptainer-no-containall")
	}
	if cfg.GPU.Enabled {
		// The server charges a service's GPUs by inventory UUID, also at
		// --concurrency 1, and protects them from other workers of the host.
		if len(inventory) == 0 {
			return serviceSettings{}, errors.New("--service-mode with --gpu needs the GPU inventory (nvidia-smi), and none was found (check nvidia-smi and --gpu-id)")
		}
		for _, g := range inventory {
			if g.UUID == "" {
				return serviceSettings{}, fmt.Errorf("--service-mode with --gpu: GPU %d has no UUID in the nvidia-smi inventory", g.Index)
			}
		}
	}
	lo, hi := cfg.ServicePortLo, cfg.ServicePortHi
	if lo == 0 && hi == 0 {
		lo, hi = DefaultServicePortLo, DefaultServicePortHi
	}
	if err := validateServicePorts(lo, hi, slots); err != nil {
		return serviceSettings{}, err
	}
	host := cfg.AdvertiseHost
	if host == "" {
		host = hostname
		if host == "" {
			return serviceSettings{}, errors.New("--service-mode: the hostname is unknown; set --advertise-host")
		}
		if err := model.ValidateServiceHost(host); err != nil {
			return serviceSettings{}, fmt.Errorf("--service-mode: the hostname %q cannot be advertised (%v); set --advertise-host", host, err)
		}
	} else if err := model.ValidateServiceHost(host); err != nil {
		return serviceSettings{}, fmt.Errorf("invalid --advertise-host %q: %w", host, err)
	}
	return serviceSettings{advertiseHost: host, portLo: lo, portHi: hi}, nil
}

// resolveGPUAndServiceSettings checks the GPU concurrency and service-mode
// settings at start-up (New) and returns the service settings and the GPU
// ledger (nil = every task gets the static --gpu configuration).
//
// Batch mode, unchanged: validateGPUConcurrency, a ledger only above one
// slot, then the refusal of service settings without --service-mode.
//
// Service mode: its own checks come first, so that their GPU inventory
// message is the one given (not validateGPUConcurrency's advice to use
// --concurrency 1, which would not help); with --gpu the ledger is built at
// every slot count, so a service gets exactly the devices the server charged
// (taskGPUCount), none when it charged none.
func resolveGPUAndServiceSettings(cfg Config, hostname, hostID string, inventory []model.GPUInfo, slots int) (serviceSettings, *gpuLedger, error) {
	var svc serviceSettings
	if cfg.ServiceMode {
		var err error
		if svc, err = resolveServiceSettings(cfg, hostname, hostID, inventory, slots); err != nil {
			return serviceSettings{}, nil, err
		}
	}
	if err := validateGPUConcurrency(slots, cfg.GPU.Enabled, inventory); err != nil {
		return serviceSettings{}, nil, err
	}
	var ledger *gpuLedger
	if cfg.GPU.Enabled && (slots > 1 || cfg.ServiceMode) {
		ledger = newGPULedger(inventory)
	}
	if !cfg.ServiceMode {
		var err error
		if svc, err = resolveServiceSettings(cfg, hostname, hostID, inventory, slots); err != nil {
			return serviceSettings{}, nil, err
		}
	}
	return svc, ledger, nil
}

// isServiceTask reports whether task is a service (gowe:Service).
func isServiceTask(task *model.Task) bool {
	return task.IsService || (task.RuntimeHints != nil && task.RuntimeHints.Service != nil)
}

// taskModeRefusal is the error a task is reported FAILED with, without
// running, when it does not match the worker's mode; nil when it does.
func (w *Worker) taskModeRefusal(task *model.Task) error {
	svc := isServiceTask(task)
	switch {
	case w.serviceMode && !svc:
		return errors.New("this worker runs services only")
	case !w.serviceMode && svc:
		return errors.New("this worker does not run services")
	}
	return nil
}

// serviceEntry is the registry's state of one service.
type serviceEntry struct {
	port        int
	scheme      string
	stopGrace   time.Duration
	gpus        []model.ServiceGPU
	imageSHA256 string
	pid         int
	done        <-chan struct{} // the handle's Done, set with pid
	state       string
	readyAt     *time.Time
	detail      string
}

// serviceRegistry holds the services of a service-mode worker, by task id:
// their ports (so two services never get the same one) and their status for
// the heartbeat.
type serviceRegistry struct {
	mu      sync.Mutex
	lo, hi  int
	entries map[string]*serviceEntry
	canBind func(port int) bool
}

func newServiceRegistry(lo, hi int) *serviceRegistry {
	return &serviceRegistry{lo: lo, hi: hi, entries: map[string]*serviceEntry{}, canBind: portBindable}
}

// portBindable reports whether a TCP listener can be opened on port (all
// interfaces) right now; the listener is closed at once.
func portBindable(port int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// reserve gives taskID the lowest port of the range that no other service
// of this worker holds and that can be bound now, and records e (state
// starting) under it.
func (r *serviceRegistry) reserve(taskID string, e *serviceEntry) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	held := make(map[int]bool, len(r.entries))
	for id, other := range r.entries {
		if id != taskID {
			held[other.port] = true
		}
	}
	for p := r.lo; p <= r.hi; p++ {
		if held[p] || !r.canBind(p) {
			continue
		}
		e.port = p
		e.state = model.ServiceStateStarting
		r.entries[taskID] = e
		return p, nil
	}
	return 0, fmt.Errorf("no free port in the service port range %d-%d", r.lo, r.hi)
}

// update applies fn to the entry of taskID, if there is one.
func (r *serviceRegistry) update(taskID string, fn func(e *serviceEntry)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[taskID]; ok {
		fn(e)
	}
}

// remove drops taskID's entry, releasing its port.
func (r *serviceRegistry) remove(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, taskID)
}

// runningPIDs returns the process ids (= process group ids) of the services
// whose process has been launched and not yet reaped (nil on a nil registry).
func (r *serviceRegistry) runningPIDs() []int {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var pids []int
	for _, e := range r.entries {
		if e.pid <= 1 || e.done == nil {
			continue
		}
		select {
		case <-e.done:
		default:
			pids = append(pids, e.pid)
		}
	}
	sort.Ints(pids)
	return pids
}

// reports is the heartbeat's services list: one entry per registered
// service, sorted by task id, with host as the advertised host. Never nil.
// At most model.MaxHeartbeatServices entries (the server ignores a longer
// list as a whole).
func (r *serviceRegistry) reports(host string) []model.ServiceReport {
	out := []model.ServiceReport{}
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.entries {
		st := model.ServiceStatus{
			State:       e.state,
			Host:        host,
			Port:        e.port,
			Scheme:      e.scheme,
			GPUs:        append([]model.ServiceGPU(nil), e.gpus...),
			ImageSHA256: e.imageSHA256,
			PID:         e.pid,
			Detail:      e.detail,
		}
		if e.readyAt != nil {
			t := *e.readyAt
			st.ReadyAt = &t
		}
		out = append(out, model.ServiceReport{TaskID: id, ServiceStatus: st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	if len(out) > model.MaxHeartbeatServices {
		out = out[:model.MaxHeartbeatServices]
	}
	return out
}

// KillServices sends SIGKILL to the process group of every running service
// and returns how many groups were signalled. For the second shutdown signal,
// right before the process exits: the services are not reported (their state
// files stay, and the next start of a worker on this work directory finds
// their processes gone). A no-op without service mode.
func (w *Worker) KillServices() int {
	if !w.serviceMode || w.services == nil {
		return 0
	}
	n := 0
	for _, pid := range w.services.runningPIDs() {
		if syscall.Kill(-pid, syscall.SIGKILL) == nil {
			n++
		}
	}
	return n
}

// serviceGPUs lists the GPUs a service was given: its own devices (per-task
// assignment, matched to the inventory by gpuDeviceID), or without a ledger
// the worker's whole configured set; none without GPU access.
func (w *Worker) serviceGPUs(gpu GPUConfig) []model.ServiceGPU {
	if !gpu.HasAccess() {
		return nil
	}
	var out []model.ServiceGPU
	if len(gpu.Devices) > 0 {
		for _, d := range gpu.Devices {
			for _, g := range w.gpuInventory {
				if gpuDeviceID(g) == d {
					out = append(out, model.ServiceGPU{UUID: g.UUID, Index: g.Index})
					break
				}
			}
		}
		return out
	}
	for _, g := range w.gpuInventory {
		out = append(out, model.ServiceGPU{UUID: g.UUID, Index: g.Index})
	}
	return out
}

// serviceImage resolves a local .sif image of tool and hashes it with the
// worker's digest cache. It returns the digest and the symlink-resolved path
// that was hashed (launched instead of re-resolving, so the reported digest
// is the launched file's); both empty for a registry image or when the file
// cannot be resolved or read (logged; Apptainer then reports the problem).
func (w *Worker) serviceImage(taskID string, tool *cwl.CommandLineTool) (digest, resolved string) {
	ref := cwltool.GetDockerImage(tool)
	if !strings.HasSuffix(ref, ".sif") {
		return "", ""
	}
	path := toolexec.ResolveApptainerImage(ref, w.imageDir)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		w.logger.Warn("service image not resolved; no image digest reported", "task_id", taskID, "image", path, "error", err)
		return "", ""
	}
	d, err := w.imageDigests.digest(real)
	if err != nil {
		w.logger.Warn("service image not hashed; no image digest reported", "task_id", taskID, "image", real, "error", err)
		return "", ""
	}
	return d, real
}

// serviceRefusal is the error a service task is refused with before
// anything runs, or nil.
func serviceRefusal(task *model.Task) error {
	hints := task.RuntimeHints
	if hints == nil || hints.Service == nil {
		return errors.New("service task without a gowe:Service spec")
	}
	if len(hints.SecretInputs) > 0 {
		return errors.New("a service does not accept secret inputs (cwltool:Secrets): their values can reach the command line; pass secrets through gowe:Execution secret_env, which sets environment variables only")
	}
	if hints.InjectBVBRCToken {
		return errors.New("a service cannot receive the submitter's token (token injection is not supported for services)")
	}
	return nil
}

// makePrivateDirs creates each of dirs with mode 0700, or sets an existing one
// (a directory, never through a symlink) to 0700.
func makePrivateDirs(dirs ...string) error {
	for _, d := range dirs {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create %s: %w", d, err)
		}
		fi, err := os.Lstat(d)
		if err != nil {
			return fmt.Errorf("restrict %s: %w", d, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("restrict %s: not a directory (%s)", d, fi.Mode().Type())
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("restrict %s: %w", d, err)
		}
	}
	return nil
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// executeService runs a service task in its task dir (runTask) until the
// service ends, and reports the end. gpu is the task's GPU configuration
// (taskGPU), held by the caller until this returns.
func (w *Worker) executeService(ctx context.Context, task *model.Task, taskDir string, gpu GPUConfig) error {
	if err := serviceRefusal(task); err != nil {
		return w.reportFailure(ctx, task, err)
	}
	// The task dir is the container's writable home and holds the service's
	// logs: this user only. So are its siblings (_tmp is the container's
	// /tmp, _staging holds staged inputs and file literals, _output a
	// dockerOutputDirectory), created here before anything else does.
	if err := os.Chmod(taskDir, 0o700); err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("restrict the task dir: %w", err))
	}
	if err := makePrivateDirs(taskDir+"_tmp", taskStagingDir(taskDir), taskDir+"_output"); err != nil {
		return w.reportFailure(ctx, task, err)
	}
	spec := *task.RuntimeHints.Service
	spec.ApplyDefaults()
	if err := spec.Validate(); err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("invalid gowe:Service hint: %w", err))
	}
	tool, err := w.parser.ParseToolFromMap(task.Tool)
	if err != nil {
		return w.reportFailure(ctx, task, fmt.Errorf("parse tool: %w", err))
	}
	if !cwltool.HasDockerRequirement(tool) || cwltool.GetDockerImage(tool) == "" {
		return w.reportFailure(ctx, task, errors.New("a service must run in a container: the tool has no DockerRequirement image"))
	}

	// The batch preparation: file literals, configuration, secrets (env
	// only, secret inputs were refused above), remapping, stage-in.
	prep, err := w.prepareCWLJob(ctx, task, taskDir, gpu)
	if err != nil {
		var sie *stageInError
		if errors.As(err, &sie) {
			return w.endServiceBeforeLaunch(ctx, task, err)
		}
		return w.reportFailure(ctx, task, err)
	}
	// The inputs are staged: the service needs the task's credentials no
	// longer, so they are not kept for its whole life. The redaction mask
	// (tokenMask, in prep) still holds the token, so log tails stay masked.
	// runTask's deferred scrub stays for every other path.
	scrubTaskCredentials(task)
	cfg, tokenMask := prep.cfg, prep.tokenMask
	tool = prep.tool
	// The port goes into a copy: the prepared job may be task.Job itself.
	job := make(map[string]any, len(prep.job)+1)
	for k, v := range prep.job {
		job[k] = v
	}
	prep = nil

	imageSHA, resolvedImage := w.serviceImage(task.ID, tool)
	if resolvedImage != "" {
		cfg.ResolvedImage = resolvedImage
	}
	gpus := w.serviceGPUs(gpu)
	stopGrace := time.Duration(spec.StopGraceSeconds) * serviceTimeUnit
	// A service cancelled or shut down meanwhile is never started: checked
	// right before the port is reserved and again right before the launch.
	if ctx.Err() != nil {
		return w.endServiceBeforeLaunch(ctx, task, ctx.Err())
	}
	port, err := w.services.reserve(task.ID, &serviceEntry{
		scheme:      spec.Scheme,
		stopGrace:   stopGrace,
		gpus:        gpus,
		imageSHA256: imageSHA,
	})
	if err != nil {
		return w.reportFailure(ctx, task, err)
	}
	// Removed after the final report (deferred), which releases the port.
	defer w.services.remove(task.ID)

	job[spec.PortInput] = port
	stderrPath := filepath.Join(taskDir, serviceStderrFile)
	// The process is in the registry (KillServices) from the moment it has
	// started, before ExecuteTool even returns.
	registerProcess := func(pid int, done <-chan struct{}) {
		w.services.update(task.ID, func(e *serviceEntry) {
			e.pid = pid
			e.done = done
		})
	}
	cfg.Service = &toolexec.ServiceLaunch{
		StdoutPath: filepath.Join(taskDir, serviceStdoutFile),
		StderrPath: stderrPath,
		StopGrace:  stopGrace,
		OnStart:    registerProcess,
	}
	if testHookBeforeServiceLaunch != nil {
		testHookBeforeServiceLaunch(task.ID)
	}
	if ctx.Err() != nil {
		return w.endServiceBeforeLaunch(ctx, task, ctx.Err())
	}
	result, err := cwltool.ExecuteTool(ctx, cfg, tool, job, taskDir)
	if result != nil && result.Service != nil {
		// Again from the handle, before any other work (a no-op when OnStart
		// already did it).
		registerProcess(result.Service.PID(), result.Service.Done())
	}

	// Mask for anything reported from here on; then drop the configuration's
	// references to the secret maps (the values stay in the process
	// environment of the service, where they belong).
	maskSet := mergeSecretsForRedaction(mergeSecretsForRedaction(cfg.SecretEnvVars, cfg.SecretValues), tokenMask)
	cfg.SecretEnvVars = nil
	cfg.SecretValues = nil

	if err != nil {
		if ctx.Err() != nil {
			return w.endServiceBeforeLaunch(ctx, task, err)
		}
		return w.reportFailure(ctx, task, fmt.Errorf("start service: %w", newRedactedError(err, maskSet)))
	}
	if result == nil || result.Service == nil {
		return w.reportFailure(ctx, task, errors.New("start service: no process handle"))
	}
	h := result.Service
	stateWritten, abandoned := false, false
	// Every path from here on, a panic or an early return included, stops
	// the process (a no-op once it has exited), then removes its state file
	// (it has ended and been reaped) and releases the stderr log descriptor.
	defer func() {
		if !abandoned {
			h.Stop()
			if stateWritten {
				if err := removeServiceState(w.svcStateDir, task.ID); err != nil {
					w.logger.Warn("service state file not removed", "task_id", task.ID, "error", err)
				}
			}
		}
		_ = h.Close()
	}()
	if testHookAfterServiceLaunch != nil {
		testHookAfterServiceLaunch(task)
	}
	launched := time.Now()
	if err := w.recordServiceState(task.ID, h.PID(), port, launched, h.Done()); err != nil {
		if isClosed(h.Done()) {
			w.logger.Debug("service state file not written: the service has already exited", "task_id", task.ID)
		} else {
			w.logger.Warn("service state file not written: if this worker died, its next start would not stop this service", "task_id", task.ID, "error", err)
		}
	} else {
		stateWritten = true
	}
	image := cfg.ResolvedImage
	if image == "" {
		image = cwltool.GetDockerImage(tool)
	}
	w.logger.Info("service launched",
		"task_id", task.ID,
		"port", port,
		"pid", h.PID(),
		"devices", serviceGPUList(gpus),
		"image", image,
		"logs", filepath.Dir(stderrPath),
	)
	err = w.superviseService(ctx, task, spec, port, h, maskSet)
	if errors.Is(err, errServiceAbandoned) {
		abandoned = true
		return nil
	}
	return err
}

// superviseService checks the service's health until it ends: its process
// exits (FAILED), it does not become ready in time (stopped, FAILED), another
// process turns out to hold its port (stopped, FAILED), the server asks to
// stop it (stopped, SKIPPED) or the worker shuts down (stopped, FAILED). It
// returns the report's error.
func (w *Worker) superviseService(ctx context.Context, task *model.Task, spec model.ServiceSpec, port int, h toolexec.ServiceHandle, mask map[string]string) error {
	checker := newHealthChecker(spec.Scheme, port, spec.Health)
	tracker := newHealthTracker(spec.Health.FailureThreshold)
	launched := time.Now()
	pid := h.PID()

	ticker := time.NewTicker(time.Duration(spec.Health.IntervalSeconds) * serviceTimeUnit)
	defer ticker.Stop()
	startTimer := time.NewTimer(time.Duration(spec.Health.StartTimeoutSeconds) * serviceTimeUnit)
	defer startTimer.Stop()
	// Checks run on their own goroutine, one at a time, so an exit or a stop
	// request is seen at once; the one in flight is abandoned on return.
	checkCtx, cancelChecks := context.WithCancel(context.Background())
	defer cancelChecks()
	results := make(chan healthResult, 1)
	checking := false
	lastDetail := ""
	foreignChecks := 0 // consecutive checks answered by another process, before the first ready

	for {
		select {
		case <-h.Done():
			// Exited by itself: no longer advertised as ready while the report
			// (with its retries) is under way.
			w.services.update(task.ID, func(e *serviceEntry) { e.state = model.ServiceStateStopping })
			if w.active.wasCancelled(task.ID) {
				// Exited by itself just as the server asked to stop it.
				return w.reportServiceStopped(ctx, task, h, false)
			}
			return w.reportServiceExited(ctx, task, h, mask)

		case <-ctx.Done():
			if testHookAbandonService != nil && testHookAbandonService(task.ID) {
				return errServiceAbandoned
			}
			if w.active.wasCancelled(task.ID) {
				killed := w.stopService(task.ID, h, "stop requested by the server", spec.StopGraceSeconds)
				return w.reportServiceStopped(ctx, task, h, killed)
			}
			killed := w.stopService(task.ID, h, "worker shutting down", spec.StopGraceSeconds)
			if w.active.wasCancelled(task.ID) {
				// The server asked to stop it while the worker was stopping it
				// (heartbeats go on during the shutdown).
				return w.reportServiceStopped(ctx, task, h, killed)
			}
			msg := "worker shut down; service stopped"
			if killed {
				msg += "; SIGKILL was needed after the grace period"
			}
			code := h.ExitCode()
			return w.reportComplete(ctx, task.ID, TaskResult{State: model.TaskStateFailed, ExitCode: &code, Stderr: msg})

		case <-startTimer.C:
			if tracker.everReady() {
				continue
			}
			w.logger.Warn("service did not become ready in time", "task_id", task.ID,
				"start_timeout_s", spec.Health.StartTimeoutSeconds, "last_check", lastDetail)
			w.stopService(task.ID, h, "start timeout", spec.StopGraceSeconds)
			msg := fmt.Sprintf("service did not become ready within %d s", spec.Health.StartTimeoutSeconds)
			if lastDetail != "" {
				msg += " (last health check: " + redactSecrets(lastDetail, mask) + ")"
			}
			if tail := readServiceLogTail(h.StderrLog(), mask); tail != "" {
				msg += "\nservice stderr (tail):\n" + tail
			}
			code := h.ExitCode()
			return w.reportComplete(ctx, task.ID, TaskResult{State: model.TaskStateFailed, ExitCode: &code, Stderr: msg})

		case <-ticker.C:
			if !checking {
				checking = true
				go func() { results <- serviceCheck(checkCtx, checker, listenerOwnedBy, pid, port) }()
			}

		case r := <-results:
			checking = false
			if !r.ok {
				lastDetail = r.detail
			}
			if r.foreign && !tracker.everReady() {
				foreignChecks++
			} else {
				foreignChecks = 0
			}
			prev := tracker.state
			state, changed := tracker.observe(r.ok, time.Now())
			readyAt := tracker.readyAt
			w.services.update(task.ID, func(e *serviceEntry) {
				if e.state == model.ServiceStateStopping {
					return
				}
				e.state = state
				e.readyAt = readyAt
				if r.ok {
					e.detail = ""
				} else {
					e.detail = serviceDetail(redactSecrets(r.detail, mask))
				}
			})
			switch {
			case !changed:
			case state == model.ServiceStateReady:
				w.logger.Info("service ready", "task_id", task.ID, "port", port, "from", prev,
					"since_launch", time.Since(launched).Round(time.Millisecond))
			default:
				w.logger.Info("service state changed", "task_id", task.ID, "from", prev, "to", state,
					"detail", redactSecrets(r.detail, mask))
			}
			if foreignChecks >= serviceForeignPortChecks {
				w.logger.Warn("another process holds the service's port; it can never become ready", "task_id", task.ID, "port", port,
					"detail", redactSecrets(r.detail, mask))
				w.stopService(task.ID, h, "another process holds its port", spec.StopGraceSeconds)
				msg := fmt.Sprintf("another process holds port %d", port)
				if tail := readServiceLogTail(h.StderrLog(), mask); tail != "" {
					msg += "\nservice stderr (tail):\n" + tail
				}
				code := h.ExitCode()
				return w.reportComplete(ctx, task.ID, TaskResult{State: model.TaskStateFailed, ExitCode: &code, Stderr: msg})
			}
		}
	}
}

// stopService marks the service stopping (reported as such meanwhile), stops
// its process group (SIGTERM, SIGKILL after the stop grace) and waits until
// it has exited. It returns whether SIGKILL was needed.
func (w *Worker) stopService(taskID string, h toolexec.ServiceHandle, reason string, graceSeconds int) bool {
	prev := ""
	w.services.update(taskID, func(e *serviceEntry) {
		prev = e.state
		e.state = model.ServiceStateStopping
	})
	w.logger.Info("service state changed", "task_id", taskID, "from", prev, "to", model.ServiceStateStopping)
	w.logger.Info("stopping service", "task_id", taskID, "pid", h.PID(), "reason", reason, "stop_grace_s", graceSeconds)
	killed := h.Stop()
	if killed {
		w.logger.Warn("service did not exit on SIGTERM within its stop grace: SIGKILL sent", "task_id", taskID, "pid", h.PID())
	}
	w.logger.Info("service stopped", "task_id", taskID, "exit_code", h.ExitCode(), "killed", killed)
	return killed
}

// reportServiceStopped reports a service stopped on the server's request:
// SKIPPED.
func (w *Worker) reportServiceStopped(ctx context.Context, task *model.Task, h toolexec.ServiceHandle, killed bool) error {
	msg := "service stopped on request"
	if killed {
		msg += "; SIGKILL was needed after the grace period"
	}
	code := h.ExitCode()
	return w.reportComplete(ctx, task.ID, TaskResult{State: model.TaskStateSkipped, ExitCode: &code, Stderr: msg})
}

// reportServiceExited reports a service whose process exited on its own:
// FAILED with its exit code and the tail of its stderr log.
func (w *Worker) reportServiceExited(ctx context.Context, task *model.Task, h toolexec.ServiceHandle, mask map[string]string) error {
	code := h.ExitCode()
	w.logger.Warn("service exited", "task_id", task.ID, "pid", h.PID(), "exit_code", code)
	tail := readServiceLogTail(h.StderrLog(), mask)
	if tail == "" {
		tail = fmt.Sprintf("service exited with code %d and wrote nothing to stderr", code)
	}
	return w.reportComplete(ctx, task.ID, TaskResult{State: model.TaskStateFailed, ExitCode: &code, Stderr: tail})
}

// endServiceBeforeLaunch reports a service task that ends before its process
// started: SKIPPED on a server cancel, FAILED at worker shutdown (a service
// is never requeued) or for err.
func (w *Worker) endServiceBeforeLaunch(ctx context.Context, task *model.Task, err error) error {
	switch {
	case w.active.wasCancelled(task.ID):
		w.logger.Info("service cancelled by server before it started, reporting SKIPPED", "task_id", task.ID)
		return w.reportCancelled(ctx, task)
	case ctx.Err() != nil:
		w.logger.Info("worker shutting down before the service started, reporting FAILED", "task_id", task.ID)
		exitCode := -1
		return w.reportComplete(ctx, task.ID, TaskResult{
			State:    model.TaskStateFailed,
			ExitCode: &exitCode,
			Stderr:   "worker shut down before the service started",
		})
	default:
		return w.reportFailure(ctx, task, err)
	}
}

// readServiceLogTail returns the last toolexec.ServiceLogTailBytes of a
// service log, read through f (the handle's StderrLog descriptor: the file
// the worker created, whatever its name points at now), with mask applied
// (before cutting, so a secret across the cut is masked), cut on a rune
// boundary and prefixed with the truncation marker of a tool's captured
// output when earlier bytes were dropped. "" when the file is empty; a read
// error is reported in the text.
func readServiceLogTail(f *os.File, mask map[string]string) string {
	limit := int64(toolexec.ServiceLogTailBytes)
	data, start, err := readFileTail(f, limit+serviceLogMaskMargin)
	if err != nil {
		return fmt.Sprintf("(service log could not be read: %v)", err)
	}
	s := redactSecrets(string(data), mask)
	truncated := start > 0
	if int64(len(s)) > limit {
		cut := len(s) - int(limit)
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		s = s[cut:]
		truncated = true
	}
	if truncated {
		return "... [truncated] ...\n" + s
	}
	return s
}

// readFileTail reads the last n bytes of f through ReadAt and returns them
// with the offset they start at.
func readFileTail(f *os.File, n int64) ([]byte, int64, error) {
	if f == nil {
		return nil, 0, errors.New("no log descriptor")
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	start := max(0, size-n)
	buf := make([]byte, size-start)
	m, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	return buf[:m], start, nil
}

// serviceDetail makes a health error storable as ServiceStatus.Detail: valid
// UTF-8, control characters as spaces, at most model.MaxServiceDetailLen
// bytes (cut on a rune boundary).
func serviceDetail(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= model.MaxServiceDetailLen {
		return s
	}
	cut := model.MaxServiceDetailLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// serviceGPUList renders GPUs for a log line: "index:uuid,...", "" for none.
func serviceGPUList(gpus []model.ServiceGPU) string {
	parts := make([]string, len(gpus))
	for i, g := range gpus {
		parts[i] = strconv.Itoa(g.Index) + ":" + g.UUID
	}
	return strings.Join(parts, ",")
}
