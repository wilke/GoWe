package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/timing"
	"github.com/me/gowe/pkg/model"
)

// handleSubmissionTiming serves GET /api/v1/submissions/{id}/timing: a
// timing-only projection of a submission's steps and tasks. With
// ?include_children=true it recurses into sub-workflow child submissions.
//
// The actual computation lives in internal/timing so the web UI's timing
// panel (internal/ui) can reuse the exact same math instead of duplicating
// it — internal/ui cannot import this package (internal/server already
// imports internal/ui, so the reverse would cycle).
func (s *Server) handleSubmissionTiming(w http.ResponseWriter, r *http.Request) {
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

	// Ownership check: non-admin users can only view their own submissions.
	userCtx := UserFromContext(r.Context())
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	includeChildren := r.URL.Query().Get("include_children") == "true"
	now := time.Now().UTC()
	report, err := timing.BuildReport(r.Context(), s.store, s.logger, sub, now, includeChildren, map[string]bool{}, 0)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondOK(w, reqID, report)
}
