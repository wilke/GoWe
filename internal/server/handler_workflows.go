package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
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

	// Compute content hash for deduplication.
	mw.RawCWL = req.CWL
	hash := sha256.Sum256([]byte(req.CWL))
	mw.ContentHash = hex.EncodeToString(hash[:])

	// Check for existing workflow with same content.
	existing, err := s.store.GetWorkflowByHash(r.Context(), mw.ContentHash)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if existing != nil {
		s.logger.Info("workflow deduplicated", "id", existing.ID, "name", existing.Name, "hash", mw.ContentHash[:12])
		respondOK(w, reqID, existing)
		return
	}

	// Assign ID and persist.
	mw.ID = "wf_" + uuid.New().String()

	if err := s.store.CreateWorkflow(r.Context(), mw); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	s.logger.Info("workflow created", "id", mw.ID, "name", mw.Name, "steps", len(mw.Steps))
	respondCreated(w, reqID, mw)
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	opts := model.DefaultListOptions()
	workflows, total, err := s.store.ListWorkflows(r.Context(), opts)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

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
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: opts.Offset+opts.Limit < total,
	})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	wf, err := s.store.GetWorkflow(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if wf == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}
	respondOK(w, reqID, wf)
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	existing, err := s.store.GetWorkflow(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if existing == nil {
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

		if err := s.store.UpdateWorkflow(r.Context(), updated); err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
		respondOK(w, reqID, updated)
		return
	}

	// Only metadata update (name/description).
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := s.store.UpdateWorkflow(r.Context(), existing); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	respondOK(w, reqID, existing)
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if err := s.store.DeleteWorkflow(r.Context(), id); err != nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return
	}
	respondOK(w, reqID, map[string]any{"deleted": true})
}

func (s *Server) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	wf, err := s.store.GetWorkflow(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if wf == nil {
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
