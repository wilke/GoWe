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

	tasks = sanitizeTaskCredentialsSlice(tasks)

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
	respondOK(w, reqID, sanitizeTaskCredentials(task))
}

// sanitizeTaskCredentials returns a copy of t with sensitive runtime-hint
// data — the HTTP credential override and any submission-time secret values
// (RuntimeHints.Secrets; see RuntimeHints.ScrubSecrets) — stripped, safe for
// a user-facing API response. Workers receive credentials and secrets
// through the checkout endpoint (handleWorkerCheckout, deliberately NOT
// sanitized); they must not leak through any other task-serializing path.
//
// t itself (and anything it points to) is left untouched: this returns a
// copy rather than mutating in place, so a caller that still needs the
// original — or that got t from a layer that might reuse/cache the pointer —
// is never surprised by a scrubbed value appearing somewhere it shouldn't.
func sanitizeTaskCredentials(t *model.Task) *model.Task {
	if t == nil {
		return nil
	}
	out := *t
	out.RuntimeHints = scrubRuntimeHintsCopy(t.RuntimeHints)
	return &out
}

// sanitizeTaskCredentialsSlice applies sanitizeTaskCredentials across a list
// of tasks, returning a new slice (the input slice and its elements are
// untouched).
func sanitizeTaskCredentialsSlice(tasks []*model.Task) []*model.Task {
	out := make([]*model.Task, len(tasks))
	for i, t := range tasks {
		out[i] = sanitizeTaskCredentials(t)
	}
	return out
}

// sanitizeTaskValueSlice is sanitizeTaskCredentialsSlice for a []model.Task
// (value, not pointer, slice) — the shape model.Submission.Tasks uses.
func sanitizeTaskValueSlice(tasks []model.Task) []model.Task {
	out := make([]model.Task, len(tasks))
	for i := range tasks {
		out[i] = tasks[i]
		out[i].RuntimeHints = scrubRuntimeHintsCopy(tasks[i].RuntimeHints)
	}
	return out
}

// scrubRuntimeHintsCopy returns a copy of h with RuntimeHints.ScrubSecrets
// applied, safe to attach to a task that will be serialized to a client.
// Returns nil for a nil input. h and anything it points to (notably
// StagerOverrides) are left untouched.
func scrubRuntimeHintsCopy(h *model.RuntimeHints) *model.RuntimeHints {
	if h == nil {
		return nil
	}
	cp := *h
	if h.StagerOverrides != nil {
		so := *h.StagerOverrides
		cp.StagerOverrides = &so
	}
	cp.ScrubSecrets()
	return &cp
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
