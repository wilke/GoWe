package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// handleListUsers returns registered users with optional filtering and pagination.
// GET /api/v1/admin/users
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	opts := parseListOptions(r)

	// In-memory filtering.
	searchLower := strings.ToLower(opts.Search)
	filtered := users[:0:0]
	for _, u := range users {
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(u.Username), searchLower) {
				continue
			}
		}
		filtered = append(filtered, u)
	}

	total := len(filtered)
	start, end := paginateBounds(total, opts.Offset, opts.Limit)
	page := filtered[start:end]

	respondList(w, reqID, page, &model.Pagination{
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: end < total,
	})
}

// handleSetUserRole updates a user's role.
// PUT /api/v1/admin/users/{username}/role
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	username := chi.URLParam(r, "username")

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	// Validate role.
	role := model.UserRole(req.Role)
	if role != model.RoleUser && role != model.RoleAdmin {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "role must be 'user' or 'admin'",
		})
		return
	}

	// Get user.
	user, err := s.store.GetUser(r.Context(), username)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	if user == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("user", username))
		return
	}

	// Don't allow changing anonymous user role.
	if user.IsAnonymous() {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "cannot change role of anonymous user",
		})
		return
	}

	// Update role.
	user.Role = role
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("user role updated", "username", username, "role", role)
	respondOK(w, reqID, map[string]any{
		"username": username,
		"role":     role,
	})
}

// handleListActiveTasks returns all QUEUED and RUNNING tasks across all submissions.
// GET /api/v1/admin/tasks/active
func (s *Server) handleListActiveTasks(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var active []*model.Task
	for _, state := range []model.TaskState{model.TaskStateQueued, model.TaskStateRunning, model.TaskStatePending, model.TaskStateScheduled} {
		tasks, err := s.store.GetTasksByState(r.Context(), state)
		if err != nil {
			respondError(w, reqID, http.StatusInternalServerError,
				model.NewInternalError(err.Error()))
			return
		}
		active = append(active, tasks...)
	}

	for _, t := range active {
		sanitizeTaskCredentials(t)
	}

	respondOK(w, reqID, map[string]any{
		"total": len(active),
		"tasks": active,
	})
}
