package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")

	// Ownership check: verify the caller can access the parent submission.
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err == nil && sub != nil {
		userCtx := UserFromContext(r.Context())
		if !requireSubmissionAccess(sub, userCtx) {
			respondError(w, reqID, http.StatusForbidden, &model.APIError{
				Code: model.ErrForbidden, Message: "access denied",
			})
			return
		}
	}

	opts := parseListOptions(r)
	tasks, total, err := s.store.ListTasksBySubmissionPaged(r.Context(), subID, opts)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	for _, t := range tasks {
		sanitizeTaskCredentials(t)
	}

	respondList(w, reqID, tasks, &model.Pagination{
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: opts.Offset+opts.Limit < total,
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	// Ownership check: verify the caller can access the parent submission.
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err == nil && sub != nil {
		userCtx := UserFromContext(r.Context())
		if !requireSubmissionAccess(sub, userCtx) {
			respondError(w, reqID, http.StatusForbidden, &model.APIError{
				Code: model.ErrForbidden, Message: "access denied",
			})
			return
		}
	}

	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if task == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}
	sanitizeTaskCredentials(task)
	respondOK(w, reqID, task)
}

// sanitizeTaskCredentials strips sensitive credentials from task data before
// returning it in API responses. Workers receive credentials through the
// checkout endpoint; they must not leak through the public task API.
func sanitizeTaskCredentials(t *model.Task) {
	if t.RuntimeHints != nil && t.RuntimeHints.StagerOverrides != nil {
		t.RuntimeHints.StagerOverrides.HTTPCredential = nil
	}
}

func (s *Server) handleGetTaskLogs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	// Ownership check: verify the caller can access the parent submission.
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err == nil && sub != nil {
		userCtx := UserFromContext(r.Context())
		if !requireSubmissionAccess(sub, userCtx) {
			respondError(w, reqID, http.StatusForbidden, &model.APIError{
				Code: model.ErrForbidden, Message: "access denied",
			})
			return
		}
	}

	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if task == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}

	respondOK(w, reqID, map[string]any{
		"task_id":   task.ID,
		"step_id":   task.StepID,
		"stdout":    task.Stdout,
		"stderr":    task.Stderr,
		"exit_code": task.ExitCode,
	})
}
