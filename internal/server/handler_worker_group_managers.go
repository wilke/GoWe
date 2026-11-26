package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
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
// a moment ago cannot write, and reload once more after the write.

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

// callerIsAdmin reports whether the caller of r is a server admin.
func callerIsAdmin(r *http.Request) bool {
	uc := UserFromContext(r.Context())
	return uc != nil && uc.User != nil && uc.User.IsAdmin()
}

// respondInternal answers 500 with msg to a server admin and with
// internalErrorMessage to anyone else. The caller has logged msg at ERROR.
func respondInternal(w http.ResponseWriter, r *http.Request, reqID, msg string) {
	if !callerIsAdmin(r) {
		msg = internalErrorMessage
	}
	respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(msg))
}

// internalErrForCaller logs err at ERROR and answers as respondInternal.
func (s *Server) internalErrForCaller(w http.ResponseWriter, r *http.Request, reqID string, err error) {
	s.logger.Error("request failed with an internal error", "actor", adminName(r),
		"group", chi.URLParam(r, "group"), "error", err)
	respondInternal(w, r, reqID, err.Error())
}

// groupRouteCaller returns the authenticated caller, or answers 401 for no
// user or the anonymous user.
func groupRouteCaller(w http.ResponseWriter, r *http.Request, reqID string) (*model.User, bool) {
	uc := UserFromContext(r.Context())
	if uc == nil || uc.User == nil || uc.User.IsAnonymous() ||
		uc.User.Username == "" || uc.User.Username == model.AnonymousUser.Username {
		respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
			Code:    model.ErrUnauthorized,
			Message: "authentication required",
		})
		return nil, false
	}
	return uc.User, true
}

// mayManageGroup reports whether user may manage group by the live policy:
// a server admin, or a manager of group.
func (s *Server) mayManageGroup(user *model.User, group string) bool {
	return user.IsAdmin() || s.groupPolicy.IsManager(user.Username, group)
}

// forbidGroup answers 403 for a caller who may not manage group.
func forbidGroup(w http.ResponseWriter, reqID, group string) {
	respondError(w, reqID, http.StatusForbidden, &model.APIError{
		Code:    model.ErrForbidden,
		Message: fmt.Sprintf("only server admins and managers of worker group %q may do this", group),
	})
}

// startGroupRoute runs the checks every route on one group starts with
// (caller 401, {group} 400, live policy 503, right 403, against the live
// policy as it is) and returns the group and the caller.
func (s *Server) startGroupRoute(w http.ResponseWriter, r *http.Request, reqID string) (string, *model.User, bool) {
	user, ok := groupRouteCaller(w, r, reqID)
	if !ok {
		return "", nil, false
	}
	group := chi.URLParam(r, "group")
	if err := groupacl.ValidateGroupName(group); err != nil {
		validationErr(w, reqID, err.Error())
		return "", nil, false
	}
	if !s.requireGroupPolicy(w, reqID) {
		return "", nil, false
	}
	if !s.mayManageGroup(user, group) {
		forbidGroup(w, reqID, group)
		return "", nil, false
	}
	return group, user, true
}

// beginGroupWrite takes groupPolicyMu, reloads the live policy and checks
// the caller's right on group again against it (403), then that the group
// has a rule (404). On success the caller writes with ctx (writeContext) and
// must call done, which releases the mutex; on failure the answer is sent
// and nothing is held.
func (s *Server) beginGroupWrite(w http.ResponseWriter, r *http.Request, reqID string, user *model.User, group string) (context.Context, func(), bool) {
	s.groupPolicyMu.Lock()
	ctx, cancel := writeContext(r)
	done := func() {
		cancel()
		s.groupPolicyMu.Unlock()
	}
	if err := s.groupPolicy.Reload(ctx, s.store); err != nil {
		s.logger.Error("worker group policy could not be reloaded; the previous policy stays in force",
			"actor", user.Username, "error", err)
		respondInternal(w, r, reqID,
			"the live worker group policy could not be reloaded from the database (the previous policy stays in force): "+err.Error())
		done()
		return nil, nil, false
	}
	if !s.mayManageGroup(user, group) {
		forbidGroup(w, reqID, group)
		done()
		return nil, nil, false
	}
	if !s.requireGroupRule(ctx, w, r, reqID, group) {
		done()
		return nil, nil, false
	}
	return ctx, done, true
}

// requireGroupRule answers 404 when group has no rule (500 when the rules
// cannot be read).
func (s *Server) requireGroupRule(ctx context.Context, w http.ResponseWriter, r *http.Request, reqID, group string) bool {
	rule, err := s.storedWorkerGroup(ctx, group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return false
	}
	if rule == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker group policy", group))
		return false
	}
	return true
}

// respondGroupRule answers 200 with the stored rule of group (404 if it is
// gone).
func (s *Server) respondGroupRule(ctx context.Context, w http.ResponseWriter, r *http.Request, reqID, group string) {
	rule, err := s.storedWorkerGroup(ctx, group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if rule == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker group policy", group))
		return
	}
	respondOK(w, reqID, rule)
}

// handleListManagedWorkerGroups returns the rules the caller manages (a
// server admin: every rule), each with users and managers, sorted by group.
// GET /api/v1/worker-groups
func (s *Server) handleListManagedWorkerGroups(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	user, ok := groupRouteCaller(w, r, reqID)
	if !ok {
		return
	}
	rules, err := s.store.ListWorkerGroupPolicies(r.Context())
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	out := []*model.WorkerGroupPolicy{}
	for _, g := range rules {
		if s.mayManageGroup(user, g.Group) {
			out = append(out, g)
		}
	}
	respondOK(w, reqID, map[string]any{"groups": out})
}

// handleGetManagedWorkerGroup returns the rule of a group.
// GET /api/v1/worker-groups/{group}
func (s *Server) handleGetManagedWorkerGroup(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, _, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	s.respondGroupRule(r.Context(), w, r, reqID, group)
}

// handleAddWorkerGroupUser adds a user to the users of a group and answers
// with the rule.
// POST /api/v1/worker-groups/{group}/users
func (s *Server) handleAddWorkerGroupUser(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, user, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	var req addWorkerGroupUserRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	names, err := groupacl.ValidateUsers([]string{req.Username})
	if err != nil {
		validationErr(w, reqID, err.Error())
		return
	}

	ctx, done, ok := s.beginGroupWrite(w, r, reqID, user, group)
	if !ok {
		return
	}
	defer done()
	changed, err := s.store.AddWorkerGroupUser(ctx, group, names[0], user.Username)
	switch {
	case errors.Is(err, store.ErrWorkerGroupPolicyNotFound):
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker group policy", group))
		return
	case errors.Is(err, store.ErrWorkerGroupUsersFull):
		validationErr(w, reqID, err.Error())
		return
	case err != nil:
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if !s.reloadGroupPolicy(ctx, w, r, reqID) {
		return
	}
	s.logger.Info("worker group changed", "actor", user.Username, "group", group, "action", "add-user", "changed", changed)
	s.respondGroupRule(ctx, w, r, reqID, group)
}

// handleRemoveWorkerGroupUser removes a user from the users of a group and
// answers with the rule; 404 when the user is not listed.
// DELETE /api/v1/worker-groups/{group}/users/{username}
func (s *Server) handleRemoveWorkerGroupUser(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, user, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
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
	if username == "" {
		validationErr(w, reqID, "username is empty")
		return
	}

	ctx, done, ok := s.beginGroupWrite(w, r, reqID, user, group)
	if !ok {
		return
	}
	defer done()
	changed, err := s.store.RemoveWorkerGroupUser(ctx, group, username, user.Username)
	switch {
	case errors.Is(err, store.ErrWorkerGroupPolicyNotFound):
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker group policy", group))
		return
	case err != nil:
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if !changed {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    model.ErrNotFound,
			Message: fmt.Sprintf("the user is not in the users of worker group %q", group),
		})
		return
	}
	if !s.reloadGroupPolicy(ctx, w, r, reqID) {
		return
	}
	s.logger.Info("worker group changed", "actor", user.Username, "group", group, "action", "remove-user")
	s.respondGroupRule(ctx, w, r, reqID, group)
}

// handleListWorkerGroupKeys lists the worker keys whose groups are exactly
// [group], in the admin key list format (never the hash).
// GET /api/v1/worker-groups/{group}/keys
func (s *Server) handleListWorkerGroupKeys(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, _, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	if !s.requireGroupRule(r.Context(), w, r, reqID, group) {
		return
	}
	keys, err := s.groupKeys(r.Context(), group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{
		"total":       len(keys),
		"worker_keys": keys,
	})
}

// handleCreateWorkerGroupKey issues a worker key valid for exactly [group],
// created_by the caller, through the admin create code path (the raw key is
// answered this once). Refused with 409 while worker authentication is not
// enabled: a key would then not restrict anything, and enabling worker auth
// for the whole fleet is an admin's decision; and with 409 while the group
// already has maxGroupWorkerKeys keys whose groups are exactly [group].
// POST /api/v1/worker-groups/{group}/keys
func (s *Server) handleCreateWorkerGroupKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, user, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	var req createWorkerGroupKeyRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}

	ctx, done, ok := s.beginGroupWrite(w, r, reqID, user, group)
	if !ok {
		return
	}
	defer done()
	if s.workerAuth == nil || !s.workerAuth.Enabled(ctx) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "worker authentication is not enabled on this server: an admin must create the first worker key",
		})
		return
	}
	// Counted under groupPolicyMu, so concurrent requests on this route
	// cannot pass the bound together.
	existing, err := s.groupKeys(ctx, group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if len(existing) >= maxGroupWorkerKeys {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code: model.ErrConflict,
			Message: fmt.Sprintf("worker group %q has %d worker keys valid for exactly this group; at most %d are allowed: revoke unused keys first",
				group, len(existing), maxGroupWorkerKeys),
		})
		return
	}
	key, ok := s.issueWorkerKey(ctx, w, r, reqID, createWorkerKeyRequest{
		Label:       req.Label,
		Groups:      []string{group},
		Description: req.Description,
		ExpiresAt:   req.ExpiresAt,
	}, user.Username)
	if !ok {
		return
	}
	s.logger.Info("worker group changed", "actor", user.Username, "group", group, "action", "create-key", "key_id", key.ID)
}

// handleRevokeWorkerGroupKey deletes a worker key whose groups are exactly
// [group]; 404 when the key does not exist or has other groups.
// DELETE /api/v1/worker-groups/{group}/keys/{id}
func (s *Server) handleRevokeWorkerGroupKey(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, user, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	ctx, done, ok := s.beginGroupWrite(w, r, reqID, user, group)
	if !ok {
		return
	}
	defer done()
	deleted, err := s.store.DeleteWorkerKeyForGroup(ctx, id, group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if !deleted {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker key of group "+group, id))
		return
	}
	s.logger.Info("worker group changed", "actor", user.Username, "group", group, "action", "revoke-key", "key_id", id)
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"revoked": true,
	})
}
