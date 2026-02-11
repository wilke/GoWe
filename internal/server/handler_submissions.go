package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		WorkflowID string            `json:"workflow_id"`
		Inputs     map[string]any    `json:"inputs"`
		Labels     map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.WorkflowID == "" {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("missing required field",
				model.FieldError{Field: "workflow_id", Message: "workflow_id is required"}))
		return
	}

	// Verify workflow exists (needed by both dry-run and real submission).
	wf, err := s.store.GetWorkflow(r.Context(), req.WorkflowID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if wf == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", req.WorkflowID))
		return
	}

	// Dry-run: validate without creating a submission.
	if r.URL.Query().Get("dry_run") == "true" {
		respondOK(w, reqID, s.buildDryRunReport(wf, req.Inputs))
		return
	}

	now := time.Now().UTC()
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       req.Inputs,
		Outputs:      map[string]any{},
		Labels:       req.Labels,
		SubmittedBy:  "", // TODO: populate from auth context in Phase 7
		CreatedAt:    now,
	}
	if sub.Inputs == nil {
		sub.Inputs = map[string]any{}
	}
	if sub.Labels == nil {
		sub.Labels = map[string]string{}
	}

	if err := s.store.CreateSubmission(r.Context(), sub); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Create a Task for each workflow step.
	for _, step := range wf.Steps {
		execType := model.ExecutorTypeLocal
		bvbrcAppID := ""
		if step.Hints != nil {
			if step.Hints.ExecutorType != "" {
				execType = step.Hints.ExecutorType
			}
			bvbrcAppID = step.Hints.BVBRCAppID
		}

		task := &model.Task{
			ID:           "task_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.TaskStatePending,
			ExecutorType: execType,
			BVBRCAppID:   bvbrcAppID,
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			DependsOn:    step.DependsOn,
			MaxRetries:   3,
			CreatedAt:    now,
		}

		if err := s.store.CreateTask(r.Context(), task); err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
	}

	s.logger.Info("submission created", "id", sub.ID, "workflow_id", wf.ID, "tasks", len(wf.Steps))

	// Re-read to include tasks in response.
	full, err := s.store.GetSubmission(r.Context(), sub.ID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondCreated(w, reqID, full)
}

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	opts := model.DefaultListOptions()
	if state := r.URL.Query().Get("state"); state != "" {
		opts.State = state
	}

	subs, total, err := s.store.ListSubmissions(r.Context(), opts)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondList(w, reqID, subs, &model.Pagination{
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: opts.Offset+opts.Limit < total,
	})
}

func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := s.store.GetSubmission(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", id))
		return
	}
	respondOK(w, reqID, sub)
}

func (s *Server) handleCancelSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := s.store.GetSubmission(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", id))
		return
	}

	if !sub.State.CanTransitionTo(model.SubmissionStateCancelled) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrValidation,
			Message: "cannot cancel submission in state " + string(sub.State),
		})
		return
	}

	now := time.Now().UTC()
	sub.State = model.SubmissionStateCancelled
	sub.CompletedAt = &now

	if err := s.store.UpdateSubmission(r.Context(), sub); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Cancel pending/scheduled tasks.
	tasksCancelled := 0
	tasksAlreadyCompleted := 0
	for i := range sub.Tasks {
		t := &sub.Tasks[i]
		if t.State.IsTerminal() {
			tasksAlreadyCompleted++
			continue
		}
		t.State = model.TaskStateSkipped
		t.CompletedAt = &now
		if err := s.store.UpdateTask(r.Context(), t); err != nil {
			s.logger.Error("cancel task", "task_id", t.ID, "error", err)
		}
		tasksCancelled++
	}

	respondOK(w, reqID, map[string]any{
		"id":                      sub.ID,
		"state":                   sub.State,
		"tasks_cancelled":         tasksCancelled,
		"tasks_already_completed": tasksAlreadyCompleted,
	})
}

// buildDryRunReport validates a workflow and inputs without creating a submission.
func (s *Server) buildDryRunReport(wf *model.Workflow, inputs map[string]any) map[string]any {
	var errors []map[string]string
	var warnings []map[string]string

	if inputs == nil {
		inputs = map[string]any{}
	}

	// --- Input validation ---
	inputsValid := true
	provided := make(map[string]bool, len(inputs))
	for k := range inputs {
		provided[k] = true
	}

	// Check for missing required inputs.
	for _, inp := range wf.Inputs {
		if inp.Required && inp.Default == nil {
			if !provided[inp.ID] {
				inputsValid = false
				errors = append(errors, map[string]string{
					"field":   "inputs." + inp.ID,
					"message": "required input " + inp.ID + " is missing",
				})
			}
		}
		delete(provided, inp.ID)
	}

	// Check for unknown inputs.
	for k := range provided {
		warnings = append(warnings, map[string]string{
			"field":   "inputs." + k,
			"message": "unknown input " + k + " (not declared in workflow)",
		})
	}

	// --- Step analysis ---
	steps := make([]map[string]any, 0, len(wf.Steps))
	executorSet := make(map[model.ExecutorType]bool)

	for _, step := range wf.Steps {
		execType := model.ExecutorTypeLocal
		if step.Hints != nil && step.Hints.ExecutorType != "" {
			execType = step.Hints.ExecutorType
		}
		executorSet[execType] = true

		available := s.registry != nil && s.registry.Has(execType)
		if !available {
			errors = append(errors, map[string]string{
				"field":   "steps." + step.ID,
				"message": "executor " + string(execType) + " is not available",
			})
		}

		stepInfo := map[string]any{
			"id":                 step.ID,
			"executor_type":     string(execType),
			"depends_on":        step.DependsOn,
			"executor_available": available,
		}
		if step.Hints != nil && step.Hints.BVBRCAppID != "" {
			stepInfo["bvbrc_app_id"] = step.Hints.BVBRCAppID
		}
		steps = append(steps, stepInfo)
	}

	// --- Execution order (topological sort from DependsOn) ---
	order := topoSort(wf.Steps)
	dagAcyclic := len(order) == len(wf.Steps)
	if !dagAcyclic {
		errors = append(errors, map[string]string{
			"field":   "steps",
			"message": "cyclic dependency detected",
		})
	}

	// --- Executor availability summary ---
	execAvail := make(map[string]string, len(executorSet))
	for et := range executorSet {
		if s.registry != nil && s.registry.Has(et) {
			execAvail[string(et)] = "available"
		} else {
			execAvail[string(et)] = "unavailable"
		}
	}

	valid := len(errors) == 0

	// Ensure non-nil slices for clean JSON.
	if errors == nil {
		errors = []map[string]string{}
	}
	if warnings == nil {
		warnings = []map[string]string{}
	}

	return map[string]any{
		"dry_run":  true,
		"valid":    valid,
		"workflow": map[string]any{"id": wf.ID, "name": wf.Name, "step_count": len(wf.Steps)},
		"inputs_valid":          inputsValid,
		"steps":                 steps,
		"dag_acyclic":           dagAcyclic,
		"execution_order":       order,
		"executor_availability": execAvail,
		"errors":                errors,
		"warnings":              warnings,
	}
}

// topoSort returns step IDs in topological order using Kahn's algorithm.
// Returns a partial result if a cycle exists.
func topoSort(steps []model.Step) []string {
	inDegree := make(map[string]int, len(steps))
	forward := make(map[string][]string, len(steps))

	for _, s := range steps {
		if _, ok := inDegree[s.ID]; !ok {
			inDegree[s.ID] = 0
		}
		for _, dep := range s.DependsOn {
			forward[dep] = append(forward[dep], s.ID)
			inDegree[s.ID]++
		}
	}

	var queue, order []string
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		for _, succ := range forward[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	return order
}
