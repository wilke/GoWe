package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	}
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

	s.logger.Info("worker registered", "id", worker.ID, "name", worker.Name, "group", worker.Group, "runtime", worker.Runtime, "gpu", worker.GPUEnabled, "gpu_device", worker.GPUDevice, "datasets", len(worker.Datasets))
	respondCreated(w, reqID, worker)
}

// handleWorkerHeartbeat updates a worker's last_seen timestamp.
// PUT /api/v1/workers/{id}/heartbeat
func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	worker, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if worker == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker", id))
		return
	}

	// If worker was marked offline (e.g., by heartbeat timeout), bring it back online.
	if worker.State == model.WorkerStateOffline {
		worker.State = model.WorkerStateOnline
		s.logger.Info("worker back online after heartbeat", "id", worker.ID, "name", worker.Name)
	}

	worker.LastSeen = time.Now().UTC()
	if err := s.store.UpdateWorker(r.Context(), worker); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	// Parse the optional heartbeat body. An empty/missing body is valid and means
	// the worker is reporting no in-flight tasks (backward compatible).
	var req model.HeartbeatRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			respondError(w, reqID, http.StatusBadRequest, &model.APIError{
				Code:    model.ErrValidation,
				Message: "invalid heartbeat body: " + err.Error(),
			})
			return
		}
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

// handleWorkerCheckout finds and assigns a QUEUED worker task to the worker.
// GET /api/v1/workers/{id}/work
// Returns 200 with task or 204 No Content if no work available.
func (s *Server) handleWorkerCheckout(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	worker, err := s.store.GetWorker(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if worker == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker", id))
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
// These defend against stale or duplicate workers — NOT adversaries: the
// worker key is not bound to the worker id, so a valid key can report under
// any worker id (S2, documented limitation).
func (s *Server) handleWorkerTaskStatus(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	workerID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

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

	var req struct {
		State    string         `json:"state"`
		ExitCode *int           `json:"exit_code"`
		Stdout   string         `json:"stdout"`
		Stderr   string         `json:"stderr"`
		Outputs  map[string]any `json:"outputs"`
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
	// task re-reported by its former owner, or a double report after a retry —
	// NOT against adversaries: the worker key is not bound to the worker id,
	// so a valid key can report under any worker id (S2, documented limitation).
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
	if req.Outputs != nil {
		task.Outputs = req.Outputs
	}

	// The task has reached a terminal state via the worker report path (the only
	// path a worker task terminalizes on). The injected provider token is no
	// longer needed, so drop it before persisting rather than retaining it at
	// rest — the scheduler's scrubTaskToken never fires for worker completions.
	// (SPECIFICATION.md §13.5: the token is needed only while the task is in flight.)
	if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
		task.RuntimeHints.StagerOverrides.HTTPCredential = nil
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

	workers, err := s.store.ListWorkers(r.Context())
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
