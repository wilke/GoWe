package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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

	// An empty groups list means "any group" (see CanJoinGroup), which would
	// silently grant a key fleet-wide reach. Default to the least-privilege
	// "default" group so an omitted scope is narrow, not wildcard; an admin who
	// genuinely wants a broad key must name the groups explicitly.
	if len(req.Groups) == 0 {
		req.Groups = []string{"default"}
	}

	var createdBy string
	if uc := UserFromContext(r.Context()); uc != nil && uc.User != nil {
		createdBy = uc.User.Username
	}

	s.issueWorkerKey(r.Context(), w, r, reqID, req, createdBy)
}

// Bounds of the label and the description of a worker key issued through
// issueWorkerKey, in bytes.
const (
	maxWorkerKeyLabelBytes       = 200
	maxWorkerKeyDescriptionBytes = 1000
)

// issueWorkerKey validates req (label and description length, expires_at in
// the future), mints a key with req.Groups (which the caller has set),
// stores its hash and answers 201 with the raw key, returned this once. It
// reports whether the key was issued. Shared by the admin route and the
// worker-group key route; an internal error is detailed to server admins
// only (respondInternal).
func (s *Server) issueWorkerKey(ctx context.Context, w http.ResponseWriter, r *http.Request, reqID string, req createWorkerKeyRequest, createdBy string) (*model.WorkerKey, bool) {
	if len(req.Label) > maxWorkerKeyLabelBytes {
		validationErr(w, reqID, fmt.Sprintf("label is %d bytes long; at most %d are allowed", len(req.Label), maxWorkerKeyLabelBytes))
		return nil, false
	}
	if len(req.Description) > maxWorkerKeyDescriptionBytes {
		validationErr(w, reqID, fmt.Sprintf("description is %d bytes long; at most %d are allowed", len(req.Description), maxWorkerKeyDescriptionBytes))
		return nil, false
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "expires_at must be in the future",
		})
		return nil, false
	}

	raw, hash, prefix, err := model.GenerateWorkerKey()
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return nil, false
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

	// The store also sets worker_auth.required in the same transaction, so
	// worker authentication is on from this key onwards.
	if err := s.store.CreateWorkerKey(ctx, key); err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return nil, false
	}

	s.logger.Info("worker key issued", "key_id", key.ID, "label", key.Label,
		"groups", key.Groups, "created_by", createdBy)

	// Return the raw key ONCE alongside the metadata. It is never retrievable again.
	respondCreated(w, reqID, map[string]any{
		"key":        raw,
		"worker_key": key,
		"warning":    "store this key now; it cannot be retrieved again",
	})
	return key, true
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
		// Reject an explicit empty list: it would turn the key into a wildcard
		// (any group). To broaden a key, name the groups; to narrow it, use
		// ["default"].
		if len(*req.Groups) == 0 {
			respondError(w, reqID, http.StatusBadRequest, &model.APIError{
				Code:    model.ErrValidation,
				Message: "groups cannot be set to an empty list (empty means any group); name the groups explicitly",
			})
			return
		}
		// A key a non-admin (a worker group manager) created was scoped to
		// that one group; giving it other groups reaches beyond what its
		// creator was trusted with, and the creator may hold the raw key.
		// Allowed (the admin decides), but logged.
		if !sameGroups(key.Groups, *req.Groups) && !s.isServerAdmin(key.CreatedBy) {
			s.logger.Warn("groups of a worker key created by a non-admin changed; its creator may hold the raw key",
				"key_id", key.ID, "old_groups", key.Groups, "new_groups", *req.Groups)
		}
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

// sameGroups reports whether a and b hold the same group names, in any order.
func sameGroups(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// isServerAdmin reports whether username is a server admin by the server's
// admin configuration ("" never is).
func (s *Server) isServerAdmin(username string) bool {
	return username != "" && s.adminConfig != nil && s.adminConfig.IsAdmin(username)
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
