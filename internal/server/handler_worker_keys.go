package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// createWorkerKeyRequest is the body for minting a new worker key.
type createWorkerKeyRequest struct {
	Label       string     `json:"label"`
	Groups      []string   `json:"groups"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// handleCreateWorkerKey mints a new per-worker key. The raw secret is returned
// exactly once in the response; only its hash is persisted.
// POST /api/v1/admin/worker-keys
func (s *Server) handleCreateWorkerKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req createWorkerKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "expires_at must be in the future",
		})
		return
	}

	raw, hash, prefix, err := model.GenerateWorkerKey()
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	var createdBy string
	if uc := UserFromContext(r.Context()); uc != nil && uc.User != nil {
		createdBy = uc.User.Username
	}

	key := &model.WorkerKey{
		ID:          "wk_" + uuid.New().String(),
		Label:       req.Label,
		KeyHash:     hash,
		KeyPrefix:   prefix,
		Groups:      req.Groups,
		Description: req.Description,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   req.ExpiresAt,
	}

	if err := s.store.CreateWorkerKey(r.Context(), key); err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("worker key issued", "key_id", key.ID, "label", key.Label,
		"groups", key.Groups, "created_by", createdBy)

	// Return the raw key ONCE alongside the metadata. It is never retrievable again.
	respondCreated(w, reqID, map[string]any{
		"key":        raw,
		"worker_key": key,
		"warning":    "store this key now; it cannot be retrieved again",
	})
}

// handleListWorkerKeys lists all worker keys. Hashes and raw secrets are never
// exposed (model.WorkerKey.KeyHash is json:"-").
// GET /api/v1/admin/worker-keys
func (s *Server) handleListWorkerKeys(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	keys, err := s.store.ListWorkerKeys(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	respondOK(w, reqID, map[string]any{
		"total":       len(keys),
		"worker_keys": keys,
	})
}

// updateWorkerKeyRequest is the body for enabling/disabling or editing a key.
// Pointer fields distinguish "omitted" from a zero value.
type updateWorkerKeyRequest struct {
	Disabled    *bool      `json:"disabled"`
	Label       *string    `json:"label"`
	Description *string    `json:"description"`
	Groups      *[]string  `json:"groups"`
	ExpiresAt   *time.Time `json:"expires_at"`
	ClearExpiry bool       `json:"clear_expiry"`
}

// handleUpdateWorkerKey enables/disables or edits a worker key's metadata.
// Disabling is the reversible form of revocation.
// PATCH /api/v1/admin/worker-keys/{id}
func (s *Server) handleUpdateWorkerKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	key, err := s.store.GetWorkerKeyByID(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	if key == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker key", id))
		return
	}

	var req updateWorkerKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.Disabled != nil {
		key.Disabled = *req.Disabled
	}
	if req.Label != nil {
		key.Label = *req.Label
	}
	if req.Description != nil {
		key.Description = *req.Description
	}
	if req.Groups != nil {
		key.Groups = *req.Groups
	}
	if req.ClearExpiry {
		key.ExpiresAt = nil
	} else if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(time.Now()) {
			respondError(w, reqID, http.StatusBadRequest, &model.APIError{
				Code:    model.ErrValidation,
				Message: "expires_at must be in the future",
			})
			return
		}
		key.ExpiresAt = req.ExpiresAt
	}

	if err := s.store.UpdateWorkerKey(r.Context(), key); err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("worker key updated", "key_id", key.ID, "disabled", key.Disabled)
	respondOK(w, reqID, key)
}

// handleRevokeWorkerKey permanently revokes (deletes) a worker key.
// DELETE /api/v1/admin/worker-keys/{id}
func (s *Server) handleRevokeWorkerKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	// Confirm existence first so a genuine store fault surfaces as 500, not 404.
	existing, err := s.store.GetWorkerKeyByID(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	if existing == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker key", id))
		return
	}

	if err := s.store.DeleteWorkerKey(r.Context(), id); err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("worker key revoked", "key_id", id)
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"revoked": true,
	})
}
