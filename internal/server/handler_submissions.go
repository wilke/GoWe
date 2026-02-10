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

	// Dry-run mode (keep canned for now — Phase 6 will implement real dry-run).
	if r.URL.Query().Get("dry_run") == "true" {
		respondOK(w, reqID, cannedDryRunReport())
		return
	}

	// Verify workflow exists.
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

func cannedDryRunReport() map[string]any {
	return map[string]any{
		"dry_run":      true,
		"valid":        true,
		"workflow":     map[string]any{"id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890", "name": "assembly-annotation-pipeline"},
		"inputs_valid": true,
		"steps": []map[string]any{
			{"id": "assemble", "executor_type": "bvbrc", "bvbrc_app_id": "GenomeAssembly2", "depends_on": []string{}, "app_schema_valid": true, "inputs_compatible": true},
			{"id": "annotate", "executor_type": "bvbrc", "bvbrc_app_id": "GenomeAnnotation", "depends_on": []string{"assemble"}, "app_schema_valid": true, "inputs_compatible": true},
		},
		"dag_acyclic":           true,
		"execution_order":       []string{"assemble", "annotate"},
		"executor_availability": map[string]string{"bvbrc": "available"},
		"errors":                []any{},
		"warnings":              []any{},
	}
}
