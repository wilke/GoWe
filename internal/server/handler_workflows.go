package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// parseSchemaVersion is folded into the workflow content-hash (see
// computeContentHash) alongside the packed CWL text. Bump this whenever a
// parser change alters what internal/parser produces for otherwise-identical
// CWL input (e.g. #199). Bumping it invalidates dedup against all
// previously-registered rows, so the next registration of "unchanged"
// content is guaranteed a fresh parse instead of risking a dedup hit that
// resurrects a stale, pre-fix parse (#197/#201).
const parseSchemaVersion = "2"

// computeContentHash returns the dedup hash for a workflow registration:
// sha256(version + "\n" + cwl). Including the parser schema version means a
// server upgrade that changes parse semantics automatically misses old rows
// and produces a fresh parse, without requiring any manual row cleanup.
func computeContentHash(version, cwlText string) string {
	hash := sha256.Sum256([]byte(version + "\n" + cwlText))
	return hex.EncodeToString(hash[:])
}

// workflowCreateResponse wraps a workflow with a Deduplicated flag so
// clients (e.g. `gowe register`) can distinguish "returned the existing row
// because the content already exists" from "created a brand-new row",
// which both currently look identical in the payload otherwise.
type workflowCreateResponse struct {
	*model.Workflow
	Deduplicated bool `json:"deduplicated"`
}

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		CWL         string            `json:"cwl"`
		Labels      map[string]string `json:"labels"`
		Force       bool              `json:"force"`
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

	// Compute content hash for deduplication. The hash folds in
	// parseSchemaVersion so a server upgrade that changes parse semantics
	// automatically invalidates dedup against pre-upgrade rows (#201).
	mw.RawCWL = req.CWL
	mw.ContentHash = computeContentHash(parseSchemaVersion, req.CWL)

	// Check for existing workflow with same content, unless the caller
	// explicitly asked to bypass dedup (force: true) — an escape hatch
	// that always produces a new row with a fresh parse.
	if !req.Force {
		existing, err := s.store.GetWorkflowByHash(r.Context(), mw.ContentHash)
		if err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
		if existing != nil {
			s.logger.Info("workflow deduplicated", "id", existing.ID, "name", existing.Name, "hash", mw.ContentHash[:12])
			respondOK(w, reqID, &workflowCreateResponse{Workflow: existing, Deduplicated: true})
			return
		}
	}

	// Set ownership from authenticated user.
	userCtx := UserFromContext(r.Context())
	if userCtx != nil {
		mw.CreatedBy = userCtx.User.Username
	}

	// Assign ID and persist.
	mw.ID = "wf_" + uuid.New().String()

	if err := s.store.CreateWorkflow(r.Context(), mw); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	s.logger.Info("workflow created", "id", mw.ID, "name", mw.Name, "steps", len(mw.Steps))
	respondCreated(w, reqID, &workflowCreateResponse{Workflow: mw, Deduplicated: false})
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

func (s *Server) handleGetWorkflowInputs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	idOrName := chi.URLParam(r, "id")

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
	respondOK(w, reqID, wf.Inputs)
}

func (s *Server) handleGetWorkflowOutputs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	idOrName := chi.URLParam(r, "id")

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
	respondOK(w, reqID, wf.Outputs)
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

	// Ownership check: non-admin users can only modify workflows they created.
	userCtx := UserFromContext(r.Context())
	if userCtx != nil && !userCtx.User.IsAdmin() && existing.CreatedBy != "" && existing.CreatedBy != userCtx.User.Username {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "you can only modify workflows you created",
		})
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

	// If CWL is updated, re-parse, re-validate, and publish as a NEW row
	// rather than mutating the existing one in place.
	//
	// BEHAVIOR CHANGE (#201): a PUT with a non-empty `cwl` field used to
	// overwrite the row addressed by {id} in place — same id, new
	// RawCWL/steps/inputs/outputs, and (bug) a never-recomputed
	// ContentHash left silently stale. That violated the retrieval
	// contract (by-ID must be immutable) two ways: (1) the scheduler
	// re-reads workflows by id every tick, so mutating a workflow with
	// RUNNING submissions changed the definition under them mid-flight;
	// (2) the stale ContentHash corrupted dedup for that row going
	// forward. Now: a PUT with `cwl` always creates a brand-new row (new
	// "wf_" id, same name unless overridden, fresh parse, fresh content
	// hash via computeContentHash) and returns it — i.e. it publishes a
	// new version. The response's "id" therefore differs from the {id}
	// in the URL; callers that want "latest" should resolve by name
	// (GetWorkflowByName already returns newest-created-at). The
	// original row at {id} is left untouched, so anything already
	// pinned to it (e.g. a RUNNING submission's WorkflowID) keeps seeing
	// its original definition. Metadata-only PUT (no `cwl`) is
	// unaffected and still updates in place below.
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
		// New row: fresh id, fresh timestamps, fresh content hash.
		updated.ID = "wf_" + uuid.New().String()
		updated.RawCWL = req.CWL
		updated.ContentHash = computeContentHash(parseSchemaVersion, req.CWL)
		now := time.Now().UTC()
		updated.CreatedAt = now
		updated.UpdatedAt = now
		// Set the original CWL class from the new graph.
		updated.Class = graph.OriginalClass
		if updated.Class == "" {
			updated.Class = "Workflow"
		}
		if req.Description != "" {
			updated.Description = req.Description
		} else {
			updated.Description = existing.Description
		}
		if req.Labels != nil {
			updated.Labels = req.Labels
		} else {
			updated.Labels = existing.Labels
		}
		// Ownership of the new row belongs to whoever published it.
		if userCtx != nil {
			updated.CreatedBy = userCtx.User.Username
		}

		if err := s.store.CreateWorkflow(r.Context(), updated); err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
			return
		}
		s.logger.Info("workflow republished as new version", "old_id", id, "new_id", updated.ID, "name", updated.Name)
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

// getOwnedWorkflow fetches a workflow by ID and checks ownership.
// Returns the workflow or writes an error response and returns nil.
func (s *Server) getOwnedWorkflow(w http.ResponseWriter, r *http.Request, reqID, id string) *model.Workflow {
	existing, err := s.store.GetWorkflow(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return nil
	}
	if existing == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", id))
		return nil
	}
	userCtx := UserFromContext(r.Context())
	if userCtx != nil && !userCtx.User.IsAdmin() && existing.CreatedBy != "" && existing.CreatedBy != userCtx.User.Username {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "you can only modify workflows you created",
		})
		return nil
	}
	return existing
}

func (s *Server) handlePatchWorkflowLabels(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	existing := s.getOwnedWorkflow(w, r, reqID, id)
	if existing == nil {
		return
	}

	var req struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("invalid JSON: "+err.Error()))
		return
	}

	if req.Labels == nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("labels field is required"))
		return
	}

	if apiErr := s.validateLabelsAgainstCV(r.Context(), req.Labels); apiErr != nil {
		respondError(w, reqID, http.StatusUnprocessableEntity, apiErr)
		return
	}

	// Merge: new labels are added/overwritten, existing labels preserved.
	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	for k, v := range req.Labels {
		existing.Labels[k] = v
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

	existing := s.getOwnedWorkflow(w, r, reqID, id)
	if existing == nil {
		return
	}

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
	// Multi-step workflows: use the workflow ID (skip "main" — synthetic ID from $graph packing).
	if graph.OriginalClass == "Workflow" && graph.Workflow != nil && graph.Workflow.ID != "" && graph.Workflow.ID != "main" {
		return graph.Workflow.ID
	}

	// Try workflow label, then doc first line (slugified).
	if graph.Workflow != nil && graph.Workflow.Label != "" {
		return graph.Workflow.Label
	}
	if graph.Workflow != nil && graph.Workflow.Doc != "" {
		name := strings.SplitN(graph.Workflow.Doc, "\n", 2)[0]
		name = strings.TrimSpace(name)
		name = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				return r
			}
			if r == ' ' || r == ':' {
				return '-'
			}
			return -1
		}, name)
		name = strings.Trim(name, "-")
		if name != "" && len(name) <= 80 {
			return strings.ToLower(name)
		}
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
