package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		CWL         string            `json:"cwl"`
		Labels      map[string]string `json:"labels"`
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

	// Validate labels against controlled vocabulary.
	if apiErr := s.validateLabelsAgainstCV(r.Context(), req.Labels); apiErr != nil {
		status := http.StatusBadRequest
		if apiErr.Code == model.ErrInternal {
			status = http.StatusInternalServerError
		}
		respondError(w, reqID, status, apiErr)
		return
	}

	// Resolve gowe:// references to registered tools before parsing.
	resolvedCWL, err := resolveGoweRefs(r.Context(), s.store, req.CWL)
	if err != nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("gowe:// reference resolution error: "+err.Error()))
		return
	}
	req.CWL = resolvedCWL

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
		name = inferWorkflowName(graph)
	}
	mw, err := s.parser.ToModel(graph, name)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Set the original CWL class (CommandLineTool, Workflow, or ExpressionTool).
	mw.Class = graph.OriginalClass
	if mw.Class == "" {
		mw.Class = "Workflow"
	}

	// Override description if provided in request.
	if req.Description != "" {
		mw.Description = req.Description
	}

	// Assign labels from request.
	if req.Labels != nil {
		mw.Labels = req.Labels
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

	opts := parseListOptions(r)
	workflows, total, err := s.store.ListWorkflows(r.Context(), opts)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Build summary list (omit RawCWL and step details).
	type workflowSummary struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		Description string            `json:"description,omitempty"`
		Class       string            `json:"class"`
		CWLVersion  string            `json:"cwl_version"`
		ContentHash string            `json:"content_hash,omitempty"`
		Labels      map[string]string `json:"labels,omitempty"`
		StepCount   int               `json:"step_count"`
		CreatedAt   time.Time         `json:"created_at"`
	}
	summaries := make([]workflowSummary, len(workflows))
	for i, wf := range workflows {
		class := wf.Class
		if class == "" {
			class = "Workflow"
		}
		summaries[i] = workflowSummary{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			Class:       class,
			CWLVersion:  wf.CWLVersion,
			ContentHash: wf.ContentHash,
			Labels:      wf.Labels,
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
	idOrName := chi.URLParam(r, "id")

	// Try by ID first, then fall back to name lookup.
	wf, err := s.resolveWorkflow(r.Context(), idOrName)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if wf == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", idOrName))
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
		Name        string            `json:"name"`
		Description string            `json:"description"`
		CWL         string            `json:"cwl"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	// Validate labels against controlled vocabulary.
	if apiErr := s.validateLabelsAgainstCV(r.Context(), req.Labels); apiErr != nil {
		status := http.StatusBadRequest
		if apiErr.Code == model.ErrInternal {
			status = http.StatusInternalServerError
		}
		respondError(w, reqID, status, apiErr)
		return
	}

	// If CWL is updated, re-parse and re-validate.
	if req.CWL != "" {
		// Resolve gowe:// references before parsing.
		resolvedCWL, err := resolveGoweRefs(r.Context(), s.store, req.CWL)
		if err != nil {
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError("gowe:// reference resolution error: "+err.Error()))
			return
		}
		req.CWL = resolvedCWL

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
		// Set the original CWL class from the new graph.
		updated.Class = graph.OriginalClass
		if updated.Class == "" {
			updated.Class = "Workflow"
		}
		if req.Description != "" {
			updated.Description = req.Description
		}
		if req.Labels != nil {
			updated.Labels = req.Labels
		} else {
			updated.Labels = existing.Labels
		}

		if err := s.store.UpdateWorkflow(r.Context(), updated); err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
		respondOK(w, reqID, updated)
		return
	}

	// Only metadata update (name/description/labels).
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.Labels != nil {
		existing.Labels = req.Labels
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

// inferWorkflowName derives a workflow name from the parsed CWL graph.
// For Workflows (multi-step): use the workflow ID.
// For CommandLineTools: bvbrc_app_id > baseCommand > fallback.
func inferWorkflowName(graph *cwl.GraphDocument) string {
	// Multi-step workflows: use the workflow ID.
	if graph.OriginalClass == "Workflow" && graph.Workflow != nil && graph.Workflow.ID != "" {
		return graph.Workflow.ID
	}

	// Try bvbrc_app_id from the first tool's gowe:Execution hint (or legacy goweHint).
	for _, tool := range graph.Tools {
		goweMap, ok := tool.Hints["gowe:Execution"].(map[string]any)
		if !ok {
			goweMap, ok = tool.Hints["goweHint"].(map[string]any)
		}
		if ok {
			if appID, ok := goweMap["bvbrc_app_id"].(string); ok && appID != "" {
				return appID
			}
		}
	}

	// Fall back to baseCommand from the first tool.
	for _, tool := range graph.Tools {
		switch bc := tool.BaseCommand.(type) {
		case string:
			if bc != "" {
				return bc
			}
		case []any:
			if len(bc) > 0 {
				if s, ok := bc[0].(string); ok && s != "" {
					return s
				}
			}
		}
	}

	return "unnamed-workflow"
}
