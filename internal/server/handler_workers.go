package server

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// handleRegisterWorker creates a new worker record.
// POST /api/v1/workers
func (s *Server) handleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	// Get worker auth context.
	workerAuth := WorkerAuthFromContext(r.Context())
	if workerAuth == nil && s.workerKeyConfig != nil && s.workerKeyConfig.IsEnabled() {
		respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
			Code:    model.ErrUnauthorized,
			Message: "worker authentication required",
		})
		return
	}

	var req struct {
		Name       string            `json:"name"`
		Hostname   string            `json:"hostname"`
		Group      string            `json:"group"`
		Runtime    string            `json:"runtime"`
		Version    string            `json:"version"`
		Labels     map[string]string `json:"labels"`
		GPUEnabled bool              `json:"gpu_enabled"`
		GPUDevice  string            `json:"gpu_device"`
		Datasets   map[string]string `json:"datasets"`
		MaxCPUs    *int              `json:"max_cpus"`
		MaxMemMB   *int64            `json:"max_mem_mb"`
		// Host identity and inventory (#323); all optional.
		HostID       string          `json:"host_id"`
		HostCPUs     *int            `json:"host_cpus"`
		HostMemMB    *int64          `json:"host_mem_mb"`
		Slots        *int            `json:"slots"`
		CgroupCPU    *bool           `json:"cgroup_cpu"`
		CgroupMemory *bool           `json:"cgroup_memory"`
		GPUs         []model.GPUInfo `json:"gpus"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerRegisterBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.Name == "" {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("missing required field",
				model.FieldError{Field: "name", Message: "name is required"}))
		return
	}

	// Default group to "default".
	group := req.Group
	if group == "" {
		group = "default"
	}

	// Validate the worker can join the requested group.
	if workerAuth != nil && !workerAuth.CanJoinGroup(group) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code:    model.ErrForbidden,
			Message: "worker key does not allow joining group: " + group,
		})
		return
	}

	runtimeStr := req.Runtime
	if runtimeStr == "" {
		runtimeStr = string(model.RuntimeNone)
	}
	if err := model.ValidateRuntimes(runtimeStr); err != nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError(err.Error(),
				model.FieldError{Field: "runtime", Message: err.Error()}))
		return
	}
	runtime := model.ContainerRuntime(runtimeStr)

	// host_id is optional ("" = not reported) but must be well-formed if sent.
	if req.HostID != "" {
		if err := model.ValidateHostID(req.HostID); err != nil {
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError(err.Error(),
					model.FieldError{Field: "host_id", Message: err.Error()}))
			return
		}
	}

	now := time.Now().UTC()
	worker := &model.Worker{
		ID:           "wrk_" + uuid.New().String(),
		Name:         req.Name,
		Hostname:     req.Hostname,
		Group:        group,
		State:        model.WorkerStateOnline,
		Runtime:      runtime,
		Version:      req.Version,
		GPUEnabled:   req.GPUEnabled,
		GPUDevice:    req.GPUDevice,
		Datasets:     req.Datasets,
		Labels:       req.Labels,
		LastSeen:     now,
		RegisteredAt: now,
	}
	// Self-reported capacity; non-positive or above the sane range means "not
	// reported" (legacy), so per-host claimed sums cannot overflow.
	if req.MaxCPUs != nil && *req.MaxCPUs > 0 && *req.MaxCPUs <= maxWorkerHostCPUs {
		worker.MaxCPUs = req.MaxCPUs
	}
	if req.MaxMemMB != nil && *req.MaxMemMB > 0 && *req.MaxMemMB <= maxWorkerHostMemMB {
		worker.MaxMemMB = req.MaxMemMB
	}
	// Host identity and inventory (#323); non-positive numbers mean "not
	// reported", like the capacity above.
	worker.HostID = req.HostID
	// Values outside a sane range are treated as not reported too, so one
	// buggy worker cannot poison per-host sums built on them later.
	if req.HostCPUs != nil && *req.HostCPUs > 0 && *req.HostCPUs <= maxWorkerHostCPUs {
		worker.HostCPUs = req.HostCPUs
	}
	if req.HostMemMB != nil && *req.HostMemMB > 0 && *req.HostMemMB <= maxWorkerHostMemMB {
		worker.HostMemMB = req.HostMemMB
	}
	if req.Slots != nil && *req.Slots > 0 && *req.Slots <= maxWorkerSlots {
		worker.Slots = req.Slots
	}
	worker.CgroupCPU = req.CgroupCPU
	worker.CgroupMemory = req.CgroupMemory
	if req.GPUs != nil {
		gpus, invalid, overflow := sanitizeWorkerGPUs(req.GPUs)
		if invalid > 0 || overflow > 0 {
			s.logger.Warn("worker registration: dropped GPU entries",
				"name", req.Name, "invalid", invalid, "over_limit", overflow, "limit", maxWorkerGPUs)
		}
		worker.GPUs = gpus
	}
	// Bind the worker to the registering key. "none" is the anonymous context
	// used when worker auth is disabled; leave those rows unbound ('') so they
	// are adopted if auth is enabled later.
	if workerAuth != nil && !workerAuth.Anonymous {
		worker.KeyID = workerAuth.KeyID
	}
	if worker.Labels == nil {
		worker.Labels = map[string]string{}
	}
	if worker.Datasets == nil {
		worker.Datasets = map[string]string{}
	}

	if err := s.store.CreateWorker(r.Context(), worker); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("worker registered", "id", worker.ID, "name", worker.Name, "group", worker.Group, "runtime", worker.Runtime, "gpu", worker.GPUEnabled, "gpu_device", worker.GPUDevice, "datasets", len(worker.Datasets),
		"max_cpus", capLog(worker.MaxCPUs), "max_mem_mb", capLog(worker.MaxMemMB),
		"host_id", worker.HostID, "host_cpus", capLog(worker.HostCPUs), "host_mem_mb", capLog(worker.HostMemMB), "gpus", len(worker.GPUs))
	respondCreated(w, reqID, worker)
}

// Limits on worker-reported GPU metadata (#323). Oversized input is trimmed,
// never rejected: GPU metadata must not fail a registration.
const (
	maxWorkerGPUs     = 64
	maxWorkerGPUIndex = 1023
	// Upper bounds for self-reported host capacity; anything above is treated
	// as not reported.
	maxWorkerHostCPUs  = 16384
	maxWorkerHostMemMB = 1 << 30 // 1 PiB
	maxWorkerSlots     = 1024
	// maxWorkerRegisterBody caps the registration request body.
	maxWorkerRegisterBody = 1 << 20
	maxGPUFieldLength     = 128
	// maxWorkerHeartbeatBody caps the heartbeat request body (#323).
	maxWorkerHeartbeatBody = 64 << 10
)

// sanitizeWorkerGPUs drops entries with a negative index or an empty uuid,
// keeps at most maxWorkerGPUs of the rest and truncates their strings to
// maxGPUFieldLength bytes. It returns the kept entries (never nil) and the
// number dropped as invalid and as over the limit.
func sanitizeWorkerGPUs(in []model.GPUInfo) (out []model.GPUInfo, invalid, overflow int) {
	out = make([]model.GPUInfo, 0, min(len(in), maxWorkerGPUs))
	for _, g := range in {
		g.UUID = printableASCII(g.UUID)
		g.Name = printableASCII(g.Name)
		if g.Index < 0 || g.Index > maxWorkerGPUIndex || g.UUID == "" ||
			g.MemTotalMB < 0 || g.MemTotalMB > maxWorkerHostMemMB {
			invalid++
			continue
		}
		if len(out) == maxWorkerGPUs {
			overflow++
			continue
		}
		g.UUID = truncateUTF8(g.UUID, maxGPUFieldLength)
		g.Name = truncateUTF8(g.Name, maxGPUFieldLength)
		out = append(out, g)
	}
	return out, invalid, overflow
}

// printableASCII drops every byte of s outside printable ASCII, so worker-
// supplied GPU strings are safe to show in logs, the UI and metric labels.
func printableASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, s)
}

// truncateUTF8 cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// handleWorkerHeartbeat updates a worker's last_seen timestamp and, when the
// body carries it, its live usage (#323).
// PUT /api/v1/workers/{id}/heartbeat
func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	worker, ok := s.authorizeWorker(w, r, id)
	if !ok {
		return
	}

	// If worker was marked offline (e.g., by heartbeat timeout), bring it back online.
	if worker.State == model.WorkerStateOffline {
		worker.State = model.WorkerStateOnline
		s.logger.Info("worker back online after heartbeat", "id", worker.ID, "name", worker.Name)
	}

	// Parse the optional heartbeat body. An empty/missing body is valid and means
	// the worker is reporting no in-flight tasks (backward compatible). An
	// oversized or malformed body still counts as a heartbeat (last_seen is
	// updated, as before) and is answered with 400 after the update.
	var req heartbeatBody
	var bodyErr error
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxWorkerHeartbeatBody)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			bodyErr = err
		}
	}
	var usage *model.WorkerUsage
	if bodyErr == nil {
		usage = s.heartbeatUsage(worker, req)
	}

	// One targeted UPDATE of state, last_seen and (when sent) usage: a
	// full-row UpdateWorker here races with CheckoutTask's current_task write.
	worker.LastSeen = time.Now().UTC()
	if err := s.store.TouchWorker(r.Context(), worker.ID, worker.State, worker.LastSeen, usage); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	if bodyErr != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid heartbeat body: " + bodyErr.Error(),
		})
		return
	}

	// Reconcile orphaned tasks: any task the DB still attributes to this worker
	// (RUNNING) that the worker no longer reports — and that is past the grace
	// window — is requeued. This recovers tasks orphaned by a server restart.
	if requeued, err := s.store.ReconcileWorkerTasks(r.Context(), worker.ID, req.RunningTasks, defaultWorkerTimeout); err != nil {
		s.logger.Error("heartbeat: reconcile worker tasks", "worker_id", worker.ID, "error", err)
	} else if len(requeued) > 0 {
		s.logger.Warn("requeued orphaned tasks from heartbeat reconcile",
			"worker_id", worker.ID, "tasks", requeued)
	}

	// Tell the worker which of its running tasks have been cancelled so it can
	// kill the underlying process and free resources.
	cancelTasks, err := s.store.CancelledTasksForWorker(r.Context(), req.RunningTasks)
	if err != nil {
		s.logger.Error("heartbeat: cancelled tasks lookup", "worker_id", worker.ID, "error", err)
	}

	respondOK(w, reqID, model.HeartbeatResponse{
		WorkerID:    worker.ID,
		State:       worker.State,
		CancelTasks: cancelTasks,
	})
}

// heartbeatBody is the server-side decode target for model.HeartbeatRequest.
// The usage fields stay raw so that a malformed usage value is dropped by
// heartbeatUsage instead of failing the heartbeat (#323); running_tasks is
// decoded strictly, as before.
type heartbeatBody struct {
	RunningTasks []string        `json:"running_tasks"`
	Host         json.RawMessage `json:"host"`
	GPUs         json.RawMessage `json:"gpu_usage"`
}

// hostUsageWire and gpuUsageWire mirror model.HostUsage and model.GPUUsage
// with numbers as json.Number, so a value out of float64 range (1e999) is
// dropped by the sanitiser rather than failing the decode.
type hostUsageWire struct {
	Load1           json.Number `json:"load1"`
	Load5           json.Number `json:"load5"`
	MemTotalMB      json.Number `json:"mem_total_mb"`
	MemAvailableMB  json.Number `json:"mem_available_mb"`
	PSICPUSomeAvg10 json.Number `json:"psi_cpu_some_avg10"`
	PSIMemSomeAvg10 json.Number `json:"psi_mem_some_avg10"`
	SampledAt       string      `json:"sampled_at"`
}

type gpuUsageWire struct {
	UUID      string      `json:"uuid"`
	MemUsedMB json.Number `json:"mem_used_mb"`
	UtilPct   json.Number `json:"util_pct"`
}

// heartbeatUsage decodes and sanitises the usage in a heartbeat body. It
// returns nil when the body carries no usable usage, which leaves the stored
// usage untouched. Usage is telemetry, so bad values never fail the
// heartbeat: wrongly typed fields are ignored and sanitizeHostUsage /
// sanitizeGPUUsage drop the rest. Details are logged at DEBUG only.
func (s *Server) heartbeatUsage(worker *model.Worker, req heartbeatBody) *model.WorkerUsage {
	var usage model.WorkerUsage
	if isJSONKind(req.Host, '{') {
		var hw hostUsageWire
		if err := json.Unmarshal(req.Host, &hw); err != nil {
			s.logger.Debug("heartbeat: ignoring undecodable host usage fields", "worker_id", worker.ID, "error", err)
		}
		usage.Host = sanitizeHostUsage(hw)
	}
	if isJSONKind(req.GPUs, '[') {
		var gw []gpuUsageWire
		if err := json.Unmarshal(req.GPUs, &gw); err != nil {
			s.logger.Debug("heartbeat: ignoring undecodable GPU usage fields", "worker_id", worker.ID, "error", err)
		}
		gpus, dropped := sanitizeGPUUsage(gw, worker.GPUs)
		if dropped > 0 {
			s.logger.Debug("heartbeat: dropped GPU usage entries", "worker_id", worker.ID, "dropped", dropped)
		}
		usage.GPUs = gpus
	}
	if usage.Host == nil && len(usage.GPUs) == 0 {
		return nil
	}
	return &usage
}

// isJSONKind reports whether raw is a JSON value starting with open ('{' for
// an object, '[' for an array).
func isJSONKind(raw json.RawMessage, open byte) bool {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == open
}

// sanitizeHostUsage converts a decoded host sample, leaving every absent,
// non-finite, negative or out-of-range number at zero (nil for the optional
// PSI values). It returns nil when sampled_at is missing or unparsable or when
// no number could be parsed, so a malformed host object never replaces a good
// stored sample.
func sanitizeHostUsage(in hostUsageWire) *model.HostUsage {
	sampledAt, err := time.Parse(time.RFC3339Nano, in.SampledAt)
	if err != nil {
		return nil
	}
	h := &model.HostUsage{SampledAt: sampledAt.UTC()}
	var ok, parsed bool
	h.Load1, ok = usageFloat(in.Load1)
	parsed = parsed || ok
	h.Load5, ok = usageFloat(in.Load5)
	parsed = parsed || ok
	h.MemTotalMB, ok = usageInt(in.MemTotalMB, maxWorkerHostMemMB)
	parsed = parsed || ok
	h.MemAvailableMB, ok = usageInt(in.MemAvailableMB, maxWorkerHostMemMB)
	parsed = parsed || ok
	if v, ok := usageFloat(in.PSICPUSomeAvg10); ok {
		h.PSICPUSomeAvg10 = &v
		parsed = true
	}
	if v, ok := usageFloat(in.PSIMemSomeAvg10); ok {
		h.PSIMemSomeAvg10 = &v
		parsed = true
	}
	if !parsed {
		return nil
	}
	return h
}

// sanitizeGPUUsage keeps the entries whose uuid (reduced to printable ASCII)
// is in the worker's registered inventory, once per uuid and at most
// len(inventory) entries. Invalid numbers make that field absent and
// util_pct is clamped to 100. It returns the kept entries (nil if none) and
// the number dropped.
func sanitizeGPUUsage(in []gpuUsageWire, inventory []model.GPUInfo) (out []model.GPUUsage, dropped int) {
	known := make(map[string]bool, len(inventory))
	for _, g := range inventory {
		known[g.UUID] = true
	}
	seen := make(map[string]bool, len(inventory))
	for _, g := range in {
		uuid := truncateUTF8(printableASCII(g.UUID), maxGPUFieldLength)
		if !known[uuid] || seen[uuid] || len(out) >= len(inventory) {
			dropped++
			continue
		}
		seen[uuid] = true
		u := model.GPUUsage{UUID: uuid}
		if v, ok := usageInt(g.MemUsedMB, maxWorkerHostMemMB); ok {
			u.MemUsedMB = &v
		}
		if v, ok := usageFloat(g.UtilPct); ok {
			v = min(v, 100)
			u.UtilPct = &v
		}
		out = append(out, u)
	}
	return out, dropped
}

// usageFloat parses a usage number, reporting false for an absent,
// non-finite or negative value.
func usageFloat(n json.Number) (float64, bool) {
	if n == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

// usageInt parses a usage integer in 0..limit, reporting false otherwise.
func usageInt(n json.Number, limit int64) (int64, bool) {
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || v < 0 || v > limit {
		return 0, false
	}
	return v, true
}

// handleWorkerCheckout finds and assigns a QUEUED worker task to the worker.
// GET /api/v1/workers/{id}/work
// Returns 200 with task or 204 No Content if no work available.
func (s *Server) handleWorkerCheckout(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	worker, ok := s.authorizeWorker(w, r, id)
	if !ok {
		return
	}

	// A key whose groups were narrowed after registration must stop receiving
	// tasks for the group it can no longer join.
	if wa := WorkerAuthFromContext(r.Context()); wa != nil && !wa.CanJoinGroup(worker.Group) {
		s.logger.Warn("worker checkout denied: key no longer allows group",
			"worker_id", id, "key_id", wa.KeyID, "group", worker.Group)
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code:    model.ErrForbidden,
			Message: "worker key does not allow group: " + worker.Group,
		})
		return
	}

	task, err := s.store.CheckoutTask(r.Context(), id, worker.Group, worker.Runtime)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.logger.Debug("task checked out", "worker_id", id, "task_id", task.ID, "group", worker.Group)
	respondOK(w, reqID, task)
}

// handleWorkerTaskStatus allows a worker to report task state updates.
// PUT /api/v1/workers/{id}/tasks/{tid}/status
//
// Guards: the task must not already be terminal, the target state must be
// non-terminal (terminal results go through /complete, which stamps
// completed_at and outputs — this endpoint could otherwise terminalize with
// neither, F-L), and the reporting worker must own the task (external_id).
// The calling key must be the one bound to the worker id (authorizeWorker);
// the task checks defend against stale or duplicate workers.
func (s *Server) handleWorkerTaskStatus(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	workerID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	if _, ok := s.authorizeWorker(w, r, workerID); !ok {
		return
	}

	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if task == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}

	newState := model.TaskState(req.State)
	if task.State.IsTerminal() {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " is already terminal (" + string(task.State) + ")",
		})
		return
	}
	if newState.IsTerminal() {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "terminal state " + req.State + " must be reported via the complete endpoint",
		})
		return
	}
	// Ownership: external_id = the assigned worker id after checkout, '' after
	// a requeue, and the new worker's id after re-checkout — a late report
	// from a previous owner is always distinguishable (F-K).
	if task.ExternalID != workerID {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " is not assigned to worker " + workerID,
		})
		return
	}
	if !task.State.CanTransitionTo(newState) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "cannot transition task from " + string(task.State) + " to " + req.State,
		})
		return
	}

	// State-column-only CAS: a full-row write here could clobber concurrent
	// checkout/requeue writes (external_id, started_at — the F-J clobber class).
	applied, err := s.store.CASTaskState(r.Context(), tid, task.State, newState)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if !applied {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " changed state concurrently",
		})
		return
	}

	respondOK(w, reqID, map[string]any{"task_id": task.ID, "state": newState})
}

// handleWorkerTaskComplete allows a worker to report task completion.
// PUT /api/v1/workers/{id}/tasks/{tid}/complete
func (s *Server) handleWorkerTaskComplete(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	workerID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	if _, ok := s.authorizeWorker(w, r, workerID); !ok {
		return
	}

	var req struct {
		State      string         `json:"state"`
		ExitCode   *int           `json:"exit_code"`
		Stdout     string         `json:"stdout"`
		Stderr     string         `json:"stderr"`
		Outputs    map[string]any `json:"outputs"`
		StageInMs  *int64         `json:"stage_in_ms"`
		StageOutMs *int64         `json:"stage_out_ms"`
		// Permanent (#273): the worker's execution error wrapped
		// validate.ErrInputValidation. Absent (an older worker, or a normal
		// failure) leaves retry behavior unchanged.
		Permanent bool `json:"permanent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if task == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}

	// The guards below defend against stale or duplicate workers — a requeued
	// task re-reported by its former owner, or a double report after a retry.
	// Key-to-worker binding is enforced by authorizeWorker above.
	if task.State.IsTerminal() {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " is already terminal (" + string(task.State) + ")",
		})
		return
	}
	// Ownership: external_id = the assigned worker id after checkout, '' after
	// a requeue, and the new worker's id after re-checkout — a late report
	// from a previous owner is always distinguishable (F-K).
	if task.ExternalID != workerID {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " is not assigned to worker " + workerID,
		})
		return
	}

	newState := model.TaskState(req.State)
	if newState == "" {
		// Default to SUCCESS if exit code is 0, FAILED otherwise.
		if req.ExitCode != nil && *req.ExitCode == 0 {
			newState = model.TaskStateSuccess
		} else {
			newState = model.TaskStateFailed
		}
	}

	now := time.Now().UTC()
	task.State = newState
	task.ExitCode = req.ExitCode
	task.Stdout = req.Stdout
	task.Stderr = req.Stderr
	task.CompletedAt = &now
	// #273: the worker identified this failure as non-retryable (a bad
	// input value fails identically on every retry) — apply the same
	// MaxRetries=RetryCount idiom used by the synchronous executor path
	// (scheduler/loop.go) to make the task terminal instead of consuming
	// its retry budget.
	if req.Permanent && newState == model.TaskStateFailed {
		task.MaxRetries = task.RetryCount
	}
	if req.Outputs != nil {
		task.Outputs = req.Outputs
	}
	// nil on an old worker (version skew: the field is simply absent from its
	// JSON body) leaves the columns NULL, same as a worker attempt that never
	// staged anything.
	task.StageInMs = req.StageInMs
	task.StageOutMs = req.StageOutMs

	// The task has reached a terminal state via the worker report path (the only
	// path a worker task terminalizes on). The injected provider token and any
	// delivered submission-time secrets are no longer needed, so scrub both
	// before persisting rather than retaining them at rest — the scheduler's
	// scrubTaskToken (loop.go) only fires for the local/docker/bvbrc dispatch
	// paths, never for worker completions (#260 H2).
	// (SPECIFICATION.md §13.5: the token is needed only while the task is in flight.)
	if task.RuntimeHints != nil {
		task.RuntimeHints.ScrubSecrets()
	}

	// CAS write: refuse to overwrite a terminal state written between the read
	// above and this write (e.g. a cancel fan-out that SKIPPED the task), so a
	// double report is applied exactly once. A requeue + re-checkout landing in
	// that same statements-wide window would still be accepted (the ownership
	// check above covers the practical, tick-wide races — F-K).
	applied, err := s.store.TerminalizeTask(r.Context(), task)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if !applied {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "task " + tid + " reached a terminal state concurrently",
		})
		return
	}

	// This is the single observation point for a worker-reported terminal
	// task: gated on the CAS write above actually applying, so a duplicate
	// report (refused with 409 above) can never double-observe. reason
	// "worker" only matters for a FAILED outcome (ObserveTaskTerminal
	// ignores it for SUCCESS).
	workflowName := s.workflowNameFor(r.Context(), task.SubmissionID)
	s.metrics.ObserveTaskTerminal(task, workflowName, "worker")

	// Clear worker's current_task.
	worker, err := s.store.GetWorker(r.Context(), workerID)
	if err == nil && worker != nil {
		worker.CurrentTask = ""
		worker.LastSeen = now
		s.store.UpdateWorker(r.Context(), worker)
	}

	// task.ExitCode is *int: log the value (or "<nil>") rather than the
	// pointer address.
	exitCode := "<nil>"
	if task.ExitCode != nil {
		exitCode = strconv.Itoa(*task.ExitCode)
	}
	s.logger.Info("task completed by worker",
		"task_id", task.ID,
		"worker_id", workerID,
		"state", task.State,
		"exit_code", exitCode,
	)

	respondOK(w, reqID, map[string]any{"task_id": task.ID, "state": task.State})
}

// handleDeregisterWorker removes a worker record.
// DELETE /api/v1/workers/{id}
func (s *Server) handleDeregisterWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if _, ok := s.authorizeWorker(w, r, id); !ok {
		return
	}

	if err := s.store.DeleteWorker(r.Context(), id); err != nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker", id))
		return
	}

	s.logger.Info("worker deregistered", "id", id)
	respondOK(w, reqID, map[string]any{"id": id, "deleted": true})
}

// handleListWorkers returns registered workers with optional filtering and pagination.
// GET /api/v1/workers
func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	// Worker-key callers see only the workers bound to their key. With worker
	// auth disabled (anonymous context) the full list is returned.
	var workers []*model.Worker
	var err error
	if wa := WorkerAuthFromContext(r.Context()); wa != nil && !wa.Anonymous {
		workers, err = s.store.ListWorkersByKey(r.Context(), wa.KeyID)
	} else {
		workers, err = s.store.ListWorkers(r.Context())
	}
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	opts := parseListOptions(r)

	// In-memory filtering.
	searchLower := strings.ToLower(opts.Search)
	filtered := workers[:0:0]
	for _, w := range workers {
		if opts.State != "" && !strings.EqualFold(string(w.State), opts.State) {
			continue
		}
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(w.Name), searchLower) &&
				!strings.Contains(strings.ToLower(w.Hostname), searchLower) {
				continue
			}
		}
		filtered = append(filtered, w)
	}

	total := len(filtered)
	start, end := paginateBounds(total, opts.Offset, opts.Limit)
	page := filtered[start:end]

	respondList(w, reqID, page, &model.Pagination{
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: end < total,
	})
}

// authorizeWorker loads worker id and verifies the calling worker key is the
// one bound to it. On failure it writes the response and returns ok=false.
//
//   - worker auth disabled (anonymous context): allowed, as before;
//   - worker.KeyID == caller key id: allowed;
//   - legacy row (KeyID == ""): the caller adopts it, but only if its key may
//     join the worker's group (CAS, so exactly one adopter wins);
//   - anything else: 403 (404 is returned for unknown ids, as before).
func (s *Server) authorizeWorker(w http.ResponseWriter, r *http.Request, id string) (*model.Worker, bool) {
	reqID := RequestIDFromContext(r.Context())

	worker, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return nil, false
	}
	if worker == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker", id))
		return nil, false
	}

	wa := WorkerAuthFromContext(r.Context())
	if wa == nil {
		// Worker routes always sit behind workerAuthMiddleware; a missing
		// context means a wiring bug, so fail closed.
		respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
			Code:    model.ErrUnauthorized,
			Message: "worker authentication required",
		})
		return nil, false
	}
	if wa.Anonymous {
		return worker, true
	}
	if worker.KeyID == wa.KeyID {
		return worker, true
	}

	if worker.KeyID == "" && wa.CanJoinGroup(worker.Group) {
		adopted, err := s.store.AdoptWorkerKey(r.Context(), id, wa.KeyID)
		if err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				model.NewInternalError(err.Error()))
			return nil, false
		}
		if adopted {
			worker.KeyID = wa.KeyID
			s.logger.Info("legacy worker adopted by key",
				"worker_id", id, "key_id", wa.KeyID, "group", worker.Group)
			return worker, true
		}
		// Lost the race: re-read and compare against the winner.
		if cur, err := s.store.GetWorker(r.Context(), id); err == nil && cur != nil && cur.KeyID == wa.KeyID {
			return cur, true
		}
	}

	s.logger.Warn("worker access denied: key not bound to worker",
		"worker_id", id, "key_id", wa.KeyID, "bound_key_id", worker.KeyID, "group", worker.Group)
	respondError(w, reqID, http.StatusForbidden, &model.APIError{
		Code:    model.ErrForbidden,
		Message: "worker key is not authorized for worker " + id,
	})
	return nil, false
}

// capLog renders an optional capacity for logging ("unreported" when nil).
func capLog[T int | int64](p *T) any {
	if p == nil {
		return "unreported"
	}
	return *p
}
