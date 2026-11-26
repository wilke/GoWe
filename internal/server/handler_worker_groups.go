package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
)

// Worker-group access policy admin API (#261). The rules live in the
// database; every change is written to the store and then loaded into the
// live policy (s.groupPolicy) shared with the UI and the scheduler, so it
// applies at once to new submissions, dispatch and re-queue.

// workerGroupRoutes mounts the worker-group policy endpoints on the /admin
// router (behind requireAdmin).
func (s *Server) workerGroupRoutes(r chi.Router) {
	r.Get("/worker-groups", s.handleListWorkerGroups)
	r.Put("/worker-groups/{group}", s.handlePutWorkerGroup)
	r.Delete("/worker-groups/{group}", s.handleDeleteWorkerGroup)
	r.Put("/worker-groups-default-deny", s.handleSetWorkerGroupsDefaultDeny)
}

// putWorkerGroupRequest is the body of PUT /admin/worker-groups/{group}.
type putWorkerGroupRequest struct {
	Users  []string `json:"users"`
	Admins bool     `json:"admins"`
}

// defaultDenyRequest is the body of PUT /admin/worker-groups-default-deny.
type defaultDenyRequest struct {
	DefaultDeny *bool `json:"default_deny"`
}

// maxWorkerGroupBody bounds the body of a write request.
const maxWorkerGroupBody = 1 << 20

// workerGroupWriteTimeout bounds the store write and the reload of a change.
const workerGroupWriteTimeout = 30 * time.Second

// decodeStrict decodes a JSON body of at most maxWorkerGroupBody bytes,
// refusing unknown fields (a misspelt "users" must not store a rule that lets
// nobody in) and anything after the JSON value.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkerGroupBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// writeContext returns the context a change is stored and reloaded with:
// detached from the request, so a client that disconnects after the store
// write cannot leave the stored rule unenforced, and bounded by
// workerGroupWriteTimeout.
func writeContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), workerGroupWriteTimeout)
}

// storedWorkerGroups reads default_deny and every rule from the store.
func (s *Server) storedWorkerGroups(ctx context.Context) (bool, []*model.WorkerGroupPolicy, error) {
	value, found, err := s.store.GetServerSetting(ctx, groupacl.DefaultDenyKey)
	if err != nil {
		return false, nil, err
	}
	deny := false
	if found {
		if deny, err = groupacl.ParseDefaultDeny(value); err != nil {
			return false, nil, err
		}
	}
	groups, err := s.store.ListWorkerGroupPolicies(ctx)
	if err != nil {
		return false, nil, err
	}
	if groups == nil {
		groups = []*model.WorkerGroupPolicy{}
	}
	return deny, groups, nil
}

// requireGroupPolicy answers 503 when the server has no live policy to load
// a change into, before anything is written.
func (s *Server) requireGroupPolicy(w http.ResponseWriter, reqID string) bool {
	if s.groupPolicy != nil {
		return true
	}
	respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
		Code:    model.ErrUnavailable,
		Message: "worker group policy is not enabled on this server",
	})
	return false
}

// reloadGroupPolicy loads the stored rules into the live policy after a
// write, with the write's context. On failure it logs ERROR and answers 500;
// the previous rules stay in force until a later reload succeeds (the next
// change or GET /admin/worker-groups).
func (s *Server) reloadGroupPolicy(ctx context.Context, w http.ResponseWriter, r *http.Request, reqID string) bool {
	if err := s.groupPolicy.Reload(ctx, s.store); err != nil {
		s.logger.Error("worker group policy stored but not reloaded; the previous policy stays in force",
			"admin", adminName(r), "error", err)
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(
			"the change was stored but the live worker group policy could not be reloaded (the previous policy stays in force): "+err.Error()))
		return false
	}
	return true
}

// handleListWorkerGroups returns default_deny and every rule, sorted by
// group name. It first reloads the live policy (when the server has one), so
// the listing is what is in force, and a policy left behind by a failed
// reload is brought in line with the store; if the reload fails it answers
// 500.
// GET /api/v1/admin/worker-groups
func (s *Server) handleListWorkerGroups(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	// Under the write lock, so no change lands between the reload and the
	// read below.
	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	if s.groupPolicy != nil {
		if err := s.groupPolicy.Reload(r.Context(), s.store); err != nil {
			s.logger.Error("worker group policy could not be reloaded; the previous policy stays in force",
				"admin", adminName(r), "error", err)
			respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(
				"the live worker group policy could not be reloaded from the database (the previous policy stays in force): "+err.Error()))
			return
		}
	}
	deny, groups, err := s.storedWorkerGroups(r.Context())
	if err != nil {
		internalErr(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{
		"default_deny": deny,
		"groups":       groups,
	})
}

// handlePutWorkerGroup creates or replaces the rule of a group.
// PUT /api/v1/admin/worker-groups/{group}
func (s *Server) handlePutWorkerGroup(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group := chi.URLParam(r, "group")
	if err := groupacl.ValidateGroupName(group); err != nil {
		validationErr(w, reqID, err.Error())
		return
	}
	var req putWorkerGroupRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	users, err := groupacl.ValidateUsers(req.Users)
	if err != nil {
		validationErr(w, reqID, err.Error())
		return
	}
	if !s.requireGroupPolicy(w, reqID) {
		return
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	ctx, cancel := writeContext(r)
	defer cancel()

	admin := adminName(r)
	now := time.Now().UTC()
	rule := &model.WorkerGroupPolicy{Group: group, Users: users, Admins: req.Admins, UpdatedBy: admin, CreatedAt: now, UpdatedAt: now}
	if err := s.store.UpsertWorkerGroupPolicy(ctx, rule); err != nil {
		internalErr(w, reqID, err)
		return
	}
	if !s.reloadGroupPolicy(ctx, w, r, reqID) {
		return
	}
	s.logger.Info("worker group policy set", "admin", admin, "group", group, "users", len(users), "admins", req.Admins)

	// Answer with the rule as stored (created_at of a replaced rule is
	// kept); the change is done, so a failed read falls back to the request.
	if groups, err := s.store.ListWorkerGroupPolicies(ctx); err == nil {
		for _, g := range groups {
			if g.Group == group {
				respondOK(w, reqID, g)
				return
			}
		}
	}
	respondOK(w, reqID, rule)
}

// handleDeleteWorkerGroup removes the rule of a group: the group becomes
// open, or admin-only under default_deny. The name is not validated beyond
// being non-empty, so a stored row with an invalid name (which makes every
// reload fail) can be removed; the store decides 404.
// DELETE /api/v1/admin/worker-groups/{group}
func (s *Server) handleDeleteWorkerGroup(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group := chi.URLParam(r, "group")
	// chi matches on the escaped path when it differs from the default
	// encoding (a name holding '/', ';' or ','), and then hands the
	// parameter over still escaped.
	if r.URL.RawPath != "" {
		unescaped, err := url.PathUnescape(group)
		if err != nil {
			validationErr(w, reqID, "invalid group name in the path: "+err.Error())
			return
		}
		group = unescaped
	}
	if group == "" {
		validationErr(w, reqID, `group name is empty (the default group is "default")`)
		return
	}
	if !s.requireGroupPolicy(w, reqID) {
		return
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	ctx, cancel := writeContext(r)
	defer cancel()

	// The number of users of the removed rule, for the log line; -1 when the
	// rules cannot be read (an unreadable row must still be removable).
	removedUsers := -1
	if groups, err := s.store.ListWorkerGroupPolicies(ctx); err == nil {
		removedUsers = 0
		for _, g := range groups {
			if g.Group == group {
				removedUsers = len(g.Users)
			}
		}
	}
	existed, err := s.store.DeleteWorkerGroupPolicy(ctx, group)
	if err != nil {
		internalErr(w, reqID, err)
		return
	}
	if !existed {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("worker group policy", group))
		return
	}
	if !s.reloadGroupPolicy(ctx, w, r, reqID) {
		return
	}
	s.logger.Info("worker group policy deleted", "admin", adminName(r), "group", group, "users", removedUsers)
	respondOK(w, reqID, map[string]any{
		"group":   group,
		"deleted": true,
	})
}

// handleSetWorkerGroupsDefaultDeny sets default_deny.
// PUT /api/v1/admin/worker-groups-default-deny
func (s *Server) handleSetWorkerGroupsDefaultDeny(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	var req defaultDenyRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	if req.DefaultDeny == nil {
		validationErr(w, reqID, "default_deny is required (true or false)")
		return
	}
	if !s.requireGroupPolicy(w, reqID) {
		return
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	ctx, cancel := writeContext(r)
	defer cancel()

	admin := adminName(r)
	if err := s.store.SetServerSetting(ctx, groupacl.DefaultDenyKey, strconv.FormatBool(*req.DefaultDeny), admin); err != nil {
		internalErr(w, reqID, err)
		return
	}
	if !s.reloadGroupPolicy(ctx, w, r, reqID) {
		return
	}
	s.logger.Info("worker group policy default_deny set", "admin", admin, "default_deny", *req.DefaultDeny)
	respondOK(w, reqID, map[string]any{"default_deny": *req.DefaultDeny})
}
