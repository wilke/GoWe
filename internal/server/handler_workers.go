package server

import (
	"encoding/json"
	"net/http"
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
		Name     string            `json:"name"`
		Hostname string            `json:"hostname"`
		Group    string            `json:"group"`
		Runtime  string            `json:"runtime"`
		Labels   map[string]string `json:"labels"`
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

	runtime := model.ContainerRuntime(req.Runtime)
	if runtime == "" {
		runtime = model.RuntimeNone
	}

	now := time.Now().UTC()
	worker := &model.Worker{
		ID:           "wrk_" + uuid.New().String(),
		Name:         req.Name,
		Hostname:     req.Hostname,
		Group:        group,
		State:        model.WorkerStateOnline,
		Runtime:      runtime,
		Labels:       req.Labels,
		LastSeen:     now,
		RegisteredAt: now,
	}
	if worker.Labels == nil {
		worker.Labels = map[string]string{}
	}

	if err := s.store.CreateWorker(r.Context(), worker); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("worker registered", "id", worker.ID, "name", worker.Name, "group", worker.Group, "runtime", worker.Runtime)
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

	worker.LastSeen = time.Now().UTC()
	if err := s.store.UpdateWorker(r.Context(), worker); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	respondOK(w, reqID, map[string]any{
		"worker_id": worker.ID,
		"state":     worker.State,
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
func (s *Server) handleWorkerTaskStatus(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
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
	if !task.State.CanTransitionTo(newState) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "cannot transition task from " + string(task.State) + " to " + req.State,
		})
		return
	}

	task.State = newState
	if err := s.store.UpdateTask(r.Context(), task); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	respondOK(w, reqID, map[string]any{"task_id": task.ID, "state": task.State})
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

	if err := s.store.UpdateTask(r.Context(), task); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	// Clear worker's current_task.
	worker, err := s.store.GetWorker(r.Context(), workerID)
	if err == nil && worker != nil {
		worker.CurrentTask = ""
		worker.LastSeen = now
		s.store.UpdateWorker(r.Context(), worker)
	}

	s.logger.Info("task completed by worker",
		"task_id", task.ID,
		"worker_id", workerID,
		"state", task.State,
		"exit_code", task.ExitCode,
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

// handleListWorkers returns all registered workers.
// GET /api/v1/workers
func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	respondOK(w, reqID, workers)
}
