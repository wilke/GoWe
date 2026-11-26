package server

import (
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// Worker group manager routes (/api/v1/worker-groups, user auth). A manager
// of a group, appointed by a server admin in the group's rule, manages for
// that group only its users list, the worker keys valid for exactly that
// group and the group's workers (list, remove offline ones; see
// handler_group_workers.go); server admins may use the same routes on every
// group. Managers,
// admins, default_deny and deleting a rule stay on the admin routes.
//
// Every route on one group checks, in this order: an authenticated,
// non-anonymous caller (401), a valid {group} (400), a live policy (503),
// the caller is a server admin or a manager of {group} in the live policy
// (403), the group has a rule (404). Writes take groupPolicyMu, reload the
// live policy and check the right again before writing, so a manager removed
// a moment ago cannot write, and reload once more after the write. The
// checks are those of the operations in group_admin.go, which the web UI
// uses too; a route with a body runs checkGroupAccess before decoding it, so
// a caller without the right is refused before the body is looked at.

// managedWorkerGroupRoutes mounts the manager routes on /worker-groups.
func (s *Server) managedWorkerGroupRoutes(r chi.Router) {
	r.Get("/", s.handleListManagedWorkerGroups)
	r.Route("/{group}", func(r chi.Router) {
		r.Get("/", s.handleGetManagedWorkerGroup)
		r.Post("/users", s.handleAddWorkerGroupUser)
		r.Delete("/users/{username}", s.handleRemoveWorkerGroupUser)
		r.Get("/keys", s.handleListWorkerGroupKeys)
		r.Post("/keys", s.handleCreateWorkerGroupKey)
		r.Delete("/keys/{id}", s.handleRevokeWorkerGroupKey)
		r.Get("/workers", s.handleListWorkerGroupWorkers)
		r.Delete("/workers/{id}", s.handleRemoveWorkerGroupWorker)
	})
}

// addWorkerGroupUserRequest is the body of POST /worker-groups/{group}/users.
type addWorkerGroupUserRequest struct {
	Username string `json:"username"`
}

// createWorkerGroupKeyRequest is the body of POST /worker-groups/{group}/keys:
// the admin create request without groups (forced to [group]).
type createWorkerGroupKeyRequest struct {
	Label       string     `json:"label"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// maxGroupWorkerKeys bounds the worker keys whose groups are exactly [group]
// that POST /worker-groups/{group}/keys issues (the admin route is not
// bounded by it).
const maxGroupWorkerKeys = 100

// internalErrorMessage is what a caller who is not a server admin is told
// about an internal error on a route managers reach: the detail (a store
// error, or a reload error naming another group and its users) is for
// server admins and the server log.
const internalErrorMessage = "internal error; ask a server admin"

// handleListManagedWorkerGroups returns the rules the caller manages (a
// server admin: every rule), each with users and managers, sorted by group.
// GET /api/v1/worker-groups
func (s *Server) handleListManagedWorkerGroups(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	list, e := s.listManagedRules(r.Context(), requestActor(r))
	if e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	respondOK(w, reqID, map[string]any{"groups": list.Rules})
}

// handleGetManagedWorkerGroup returns the rule of a group.
// GET /api/v1/worker-groups/{group}
func (s *Server) handleGetManagedWorkerGroup(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	rule, err := s.GetRule(r.Context(), requestActor(r), chi.URLParam(r, "group"))
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, rule)
}

// handleAddWorkerGroupUser adds a user to the users of a group and answers
// with the rule.
// POST /api/v1/worker-groups/{group}/users
func (s *Server) handleAddWorkerGroupUser(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	actor, group := requestActor(r), chi.URLParam(r, "group")
	if e := s.checkGroupAccess(actor, group); e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	var req addWorkerGroupUserRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	rule, err := s.AddGroupUser(r.Context(), actor, group, req.Username)
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, rule)
}

// handleRemoveWorkerGroupUser removes a user from the users of a group and
// answers with the rule; 404 when the user is not listed.
// DELETE /api/v1/worker-groups/{group}/users/{username}
func (s *Server) handleRemoveWorkerGroupUser(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	actor, group := requestActor(r), chi.URLParam(r, "group")
	if e := s.checkGroupAccess(actor, group); e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	username := chi.URLParam(r, "username")
	// chi matches on the escaped path when it differs from the default
	// encoding and then hands the parameter over still escaped.
	if r.URL.RawPath != "" {
		unescaped, err := url.PathUnescape(username)
		if err != nil {
			validationErr(w, reqID, "invalid username in the path: "+err.Error())
			return
		}
		username = unescaped
	}
	rule, err := s.RemoveGroupUser(r.Context(), actor, group, username)
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, rule)
}

// handleListWorkerGroupKeys lists the worker keys whose groups are exactly
// [group], in the admin key list format (never the hash).
// GET /api/v1/worker-groups/{group}/keys
func (s *Server) handleListWorkerGroupKeys(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	keys, err := s.ListGroupKeys(r.Context(), requestActor(r), chi.URLParam(r, "group"))
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{
		"total":       len(keys),
		"worker_keys": keys,
	})
}

// handleCreateWorkerGroupKey issues a worker key valid for exactly [group]
// (see CreateGroupKey); the raw key is answered this once. 409 while worker
// authentication is not enabled or the group has maxGroupWorkerKeys keys.
// POST /api/v1/worker-groups/{group}/keys
func (s *Server) handleCreateWorkerGroupKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	actor, group := requestActor(r), chi.URLParam(r, "group")
	if e := s.checkGroupAccess(actor, group); e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	var req createWorkerGroupKeyRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	created, err := s.CreateGroupKey(r.Context(), actor, group, ui.GroupKeyInput{
		Label:       req.Label,
		Description: req.Description,
		ExpiresAt:   req.ExpiresAt,
	})
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondIssuedKey(w, reqID, created.Key, created.Raw)
}

// handleRevokeWorkerGroupKey deletes a worker key whose groups are exactly
// [group]; 404 when the key does not exist or has other groups.
// DELETE /api/v1/worker-groups/{group}/keys/{id}
func (s *Server) handleRevokeWorkerGroupKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.RevokeGroupKey(r.Context(), requestActor(r), chi.URLParam(r, "group"), id); err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"revoked": true,
	})
}

// respondIssuedKey answers 201 with a newly issued worker key and its raw
// value, returned this once.
func respondIssuedKey(w http.ResponseWriter, reqID string, key *model.WorkerKey, raw string) {
	respondCreated(w, reqID, map[string]any{
		"key":        raw,
		"worker_key": key,
		"warning":    "store this key now; it cannot be retrieved again",
	})
}
