package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CWL         string `json:"cwl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.CWL == "" {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("missing required field",
				model.FieldError{Field: "cwl", Message: "cwl field is required"}))
		return
	}

	// Parse the packed CWL.
	graph, err := s.parser.ParseGraph([]byte(req.CWL))
	if err != nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("CWL parse error: "+err.Error()))
		return
	}

	// Validate.
	if apiErr := s.validator.Validate(graph); apiErr != nil {
		respondError(w, reqID, http.StatusUnprocessableEntity, apiErr)
		return
	}

	// Convert to model.
	name := req.Name
	if name == "" {
		name = "unnamed-workflow"
	}
	mw, err := s.parser.ToModel(graph, name)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Override description if provided in request.
	if req.Description != "" {
		mw.Description = req.Description
	}

	// Assign ID and store.
	mw.ID = "wf_" + uuid.New().String()
	mw.RawCWL = req.CWL

	s.mu.Lock()
	s.workflows[mw.ID] = mw
	s.mu.Unlock()

	s.logger.Info("workflow created", "id", mw.ID, "name", mw.Name, "steps", len(mw.Steps))
	respondCreated(w, reqID, mw)
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	s.mu.RLock()
	workflows := make([]*model.Workflow, 0, len(s.workflows))
	for _, wf := range s.workflows {
		workflows = append(workflows, wf)
	}
	s.mu.RUnlock()

	// Sort by creation time (newest first).
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].CreatedAt.After(workflows[j].CreatedAt)
	})

	// Build summary list (omit RawCWL and step details).
	type workflowSummary struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Description string    `json:"description,omitempty"`
		CWLVersion  string    `json:"cwl_version"`
		StepCount   int       `json:"step_count"`
		CreatedAt   time.Time `json:"created_at"`
	}
	summaries := make([]workflowSummary, len(workflows))
	for i, wf := range workflows {
		summaries[i] = workflowSummary{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			CWLVersion:  wf.CWLVersion,
			StepCount:   len(wf.Steps),
			CreatedAt:   wf.CreatedAt,
		}
	}

	respondList(w, reqID, summaries, &model.Pagination{
		Total:   len(summaries),
		Limit:   20,
		Offset:  0,
		HasMore: false,
	})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	s.mu.RLock()
	wf, ok := s.workflows[id]
	s.mu.RUnlock()

	if !ok {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}
	respondOK(w, reqID, wf)
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	s.mu.RLock()
	existing, ok := s.workflows[id]
	s.mu.RUnlock()

	if !ok {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CWL         string `json:"cwl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	// If CWL is updated, re-parse and re-validate.
	if req.CWL != "" {
		graph, err := s.parser.ParseGraph([]byte(req.CWL))
		if err != nil {
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError("CWL parse error: "+err.Error()))
			return
		}
		if apiErr := s.validator.Validate(graph); apiErr != nil {
			respondError(w, reqID, http.StatusUnprocessableEntity, apiErr)
			return
		}

		name := req.Name
		if name == "" {
			name = existing.Name
		}
		updated, err := s.parser.ToModel(graph, name)
		if err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
		updated.ID = id
		updated.RawCWL = req.CWL
		updated.CreatedAt = existing.CreatedAt
		updated.UpdatedAt = time.Now().UTC()
		if req.Description != "" {
			updated.Description = req.Description
		}

		s.mu.Lock()
		s.workflows[id] = updated
		s.mu.Unlock()

		respondOK(w, reqID, updated)
		return
	}

	// Only metadata update (name/description).
	s.mu.Lock()
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	existing.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()

	respondOK(w, reqID, existing)
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	s.mu.Lock()
	_, ok := s.workflows[id]
	if ok {
		delete(s.workflows, id)
	}
	s.mu.Unlock()

	if !ok {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}
	respondOK(w, reqID, map[string]any{"deleted": true})
}

func (s *Server) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	s.mu.RLock()
	wf, ok := s.workflows[id]
	s.mu.RUnlock()

	if !ok {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}

	graph, err := s.parser.ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		respondOK(w, reqID, map[string]any{
			"valid":  false,
			"errors": []model.FieldError{{Message: err.Error()}},
		})
		return
	}

	if apiErr := s.validator.Validate(graph); apiErr != nil {
		respondOK(w, reqID, map[string]any{
			"valid":    false,
			"errors":   apiErr.Details,
			"warnings": []any{},
		})
		return
	}

	respondOK(w, reqID, map[string]any{
		"valid":    true,
		"errors":   []any{},
		"warnings": []any{},
	})
}
