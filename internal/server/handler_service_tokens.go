package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Bounds of expires_in of a service token.
const (
	minServiceTokenLifetime = time.Hour
	maxServiceTokenLifetime = 8760 * time.Hour
)

// maxServiceTokenRequestBytes bounds the body of a create request.
const maxServiceTokenRequestBytes = 16 << 10

// createServiceTokenRequest is the body for creating a service token.
// ExpiresIn is a Go duration (1h to 8760h); absent means no expiry.
type createServiceTokenRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ExpiresIn   *string `json:"expires_in"`
}

// serviceTokenView is a service token as the admin list shows it: never the
// token or its hash.
type serviceTokenView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Username    string     `json:"username"`
	TokenPrefix string     `json:"token_prefix"`
	Description string     `json:"description"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	Expired     bool       `json:"expired"`
}

// parseServiceTokenLifetime parses expires_in: nil is no expiry.
func parseServiceTokenLifetime(expiresIn *string) (*time.Duration, error) {
	if expiresIn == nil {
		return nil, nil
	}
	d, err := time.ParseDuration(*expiresIn)
	if err != nil {
		return nil, fmt.Errorf("expires_in %q is not a duration (e.g. 2160h)", *expiresIn)
	}
	if d < minServiceTokenLifetime || d > maxServiceTokenLifetime {
		return nil, errors.New("expires_in must be between 1h and 8760h")
	}
	return &d, nil
}

// handleCreateServiceToken creates a service token. The raw token is in this
// response only (Cache-Control: no-store); only its hash is stored.
// POST /api/v1/admin/service-tokens
func (s *Server) handleCreateServiceToken(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	invalid := func(msg string) {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{Code: model.ErrValidation, Message: msg})
	}

	var req createServiceTokenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxServiceTokenRequestBytes))
	// An unknown field (a misspelt expires_in) must not silently create a
	// token that never expires.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid("invalid JSON body: " + err.Error())
		return
	}
	if err := model.ValidateServiceName(req.Name); err != nil {
		invalid(err.Error())
		return
	}
	if len(req.Description) > model.MaxServiceTokenDescriptionBytes {
		invalid(fmt.Sprintf("description is %d bytes long; at most %d are allowed", len(req.Description), model.MaxServiceTokenDescriptionBytes))
		return
	}
	lifetime, err := parseServiceTokenLifetime(req.ExpiresIn)
	if err != nil {
		invalid(err.Error())
		return
	}

	var createdBy string
	if uc := UserFromContext(r.Context()); uc != nil && uc.User != nil {
		createdBy = uc.User.Username
	}

	raw, hash, prefix, err := model.GenerateServiceToken()
	if err != nil {
		s.logger.Error("generate service token", "error", err)
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	now := time.Now().UTC()
	tok := &model.ServiceToken{
		ID:          "svt_" + uuid.New().String(),
		Name:        req.Name,
		TokenHash:   hash,
		TokenPrefix: prefix,
		Description: req.Description,
		CreatedBy:   createdBy,
		CreatedAt:   now,
	}
	if lifetime != nil {
		exp := now.Add(*lifetime)
		tok.ExpiresAt = &exp
	}

	if err := s.store.CreateServiceToken(r.Context(), tok); err != nil {
		if errors.Is(err, store.ErrServiceTokenLimit) {
			respondError(w, reqID, http.StatusConflict, &model.APIError{Code: model.ErrConflict, Message: err.Error()})
			return
		}
		s.logger.Error("create service token", "name", tok.Name, "error", err)
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("service token created", "token_id", tok.ID, "name", tok.Name,
		"username", tok.Username(), "expires_at", tok.ExpiresAt, "created_by", createdBy)

	// The raw token goes to the caller once; it cannot be retrieved again.
	w.Header().Set("Cache-Control", "no-store")
	respondCreated(w, reqID, map[string]any{
		"id":           tok.ID,
		"name":         tok.Name,
		"username":     tok.Username(),
		"token":        raw,
		"token_prefix": tok.TokenPrefix,
		"created_at":   tok.CreatedAt,
		"expires_at":   tok.ExpiresAt,
	})
}

// handleListServiceTokens lists every service token without the token or
// its hash.
// GET /api/v1/admin/service-tokens
func (s *Server) handleListServiceTokens(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	tokens, err := s.store.ListServiceTokens(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	now := time.Now()
	views := make([]serviceTokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, serviceTokenView{
			ID:          t.ID,
			Name:        t.Name,
			Username:    t.Username(),
			TokenPrefix: t.TokenPrefix,
			Description: t.Description,
			CreatedBy:   t.CreatedBy,
			CreatedAt:   t.CreatedAt,
			ExpiresAt:   t.ExpiresAt,
			LastUsedAt:  t.LastUsedAt,
			Expired:     t.Expired(now),
		})
	}
	respondOK(w, reqID, map[string]any{
		"total":          len(views),
		"service_tokens": views,
	})
}

// handleRevokeServiceToken deletes a service token; the next request with it
// is refused.
// DELETE /api/v1/admin/service-tokens/{id}
func (s *Server) handleRevokeServiceToken(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	name, found, err := s.store.DeleteServiceToken(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	if !found {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("service token", id))
		return
	}

	var revokedBy string
	if uc := UserFromContext(r.Context()); uc != nil && uc.User != nil {
		revokedBy = uc.User.Username
	}
	s.logger.Info("service token revoked", "token_id", id, "name", name, "revoked_by", revokedBy)
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"revoked": true,
	})
}
