package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// loadAccessibleSubmission checks that submission subID exists and that the
// caller may access it. Otherwise it answers the request and returns false:
// 500 when the submission cannot be read (never a skipped check), 404 when it
// does not exist, 403 without access.
func (s *Server) loadAccessibleSubmission(w http.ResponseWriter, r *http.Request, subID string) bool {
	reqID := RequestIDFromContext(r.Context())
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return false
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", subID))
		return false
	}
	if !requireSubmissionAccess(sub, UserFromContext(r.Context())) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied",
		})
		return false
	}
	return true
}

// loadSubmissionTask returns task tid of submission subID for a task read
// endpoint. It answers the request and returns nil unless the submission
// passes loadAccessibleSubmission and the task exists and belongs to it (404
// otherwise, the same answer for an unknown task and for a task of another
// submission).
func (s *Server) loadSubmissionTask(w http.ResponseWriter, r *http.Request, subID, tid string) *model.Task {
	if !s.loadAccessibleSubmission(w, r, subID) {
		return nil
	}
	reqID := RequestIDFromContext(r.Context())
	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return nil
	}
	if task == nil || task.SubmissionID != subID {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return nil
	}
	return task
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")

	// The submission must exist and be the caller's (or the caller an admin).
	if !s.loadAccessibleSubmission(w, r, subID) {
		return
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
	// The submission must exist and be accessible, and the task must belong
	// to it: a task id alone (visible in /api/v1/fleet) reads nothing.
	task := s.loadSubmissionTask(w, r, chi.URLParam(r, "id"), chi.URLParam(r, "tid"))
	if task == nil {
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
	// Same checks as handleGetTask.
	task := s.loadSubmissionTask(w, r, chi.URLParam(r, "id"), chi.URLParam(r, "tid"))
	if task == nil {
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

// handleListTaskAttempts returns the archived earlier attempts of a task
// (exit code, stdout/stderr, worker, times), oldest first. Same checks as
// handleGetTask (loadSubmissionTask): a submission that cannot be read is an
// error, never a skipped check; 404 for an unknown submission and when the
// task does not belong to the submission. stdout/stderr go out as the task logs
// handler sends the task's own: as stored, no redaction.
// GET /api/v1/submissions/{id}/tasks/{tid}/attempts
func (s *Server) handleListTaskAttempts(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	// Ownership check: verify the caller can access the parent submission.
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", subID))
		return
	}
	if !requireSubmissionAccess(sub, UserFromContext(r.Context())) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied",
		})
		return
	}

	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if task == nil || task.SubmissionID != subID {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}

	attempts, err := s.store.ListTaskAttempts(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if attempts == nil {
		attempts = []model.TaskAttempt{}
	}
	respondOK(w, reqID, map[string]any{"attempts": attempts})
}
