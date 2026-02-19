package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// handleListUsers returns all registered users.
// GET /api/v1/admin/users
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	respondOK(w, reqID, users)
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
