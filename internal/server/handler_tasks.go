package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")

	tasks, err := s.store.ListTasksBySubmission(r.Context(), subID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondList(w, reqID, tasks, &model.Pagination{
		Total:   len(tasks),
		Limit:   20,
		Offset:  0,
		HasMore: false,
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	tid := chi.URLParam(r, "tid")

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
	respondOK(w, reqID, task)
}

func (s *Server) handleGetTaskLogs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	tid := chi.URLParam(r, "tid")

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
