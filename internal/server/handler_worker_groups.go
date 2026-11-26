package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// Worker-group access policy admin API (#261). The rules live in the
// database; every change is written to the store and then loaded into the
// live policy (s.groupPolicy) shared with the UI and the scheduler, so it
// applies at once to new submissions, dispatch and re-queue. The handlers
// decode the request, call the operation (group_admin.go, shared with the
// web UI) and encode its answer.

// workerGroupRoutes mounts the worker-group policy endpoints on the /admin
// router (behind requireAdmin).
func (s *Server) workerGroupRoutes(r chi.Router) {
	r.Get("/worker-groups", s.handleListWorkerGroups)
	r.Put("/worker-groups/{group}", s.handlePutWorkerGroup)
	r.Delete("/worker-groups/{group}", s.handleDeleteWorkerGroup)
	r.Put("/worker-groups-default-deny", s.handleSetWorkerGroupsDefaultDeny)
}

// putWorkerGroupRequest is the body of PUT /admin/worker-groups/{group}.
// Managers absent (nil) keeps the stored managers; a list, also [],
// replaces them; null is refused. Kept raw to tell absent from null.
type putWorkerGroupRequest struct {
	Users    []string        `json:"users"`
	Managers json.RawMessage `json:"managers"`
	Admins   bool            `json:"admins"`
}

// workerGroupResponse is a rule as answered by the admin write routes, with
// the ids of the worker keys whose groups are exactly [group] when a change
// took managers away (those keys stay valid; the admin decides). nil omits
// the field; an empty list is sent as [].
type workerGroupResponse struct {
	*model.WorkerGroupPolicy
	GroupKeyIDs *[]string `json:"group_key_ids,omitempty"`
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
	return writeContextFrom(r.Context())
}

// writeContextFrom is writeContext for the request context ctx.
func writeContextFrom(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), workerGroupWriteTimeout)
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

// storedWorkerGroup returns the stored rule of group, nil when it has none.
func (s *Server) storedWorkerGroup(ctx context.Context, group string) (*model.WorkerGroupPolicy, error) {
	groups, err := s.store.ListWorkerGroupPolicies(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Group == group {
			return g, nil
		}
	}
	return nil, nil
}

// isGroupKey reports whether k is a key of group alone: its groups are
// exactly [group] (an empty list means any group, so it is not).
func isGroupKey(k *model.WorkerKey, group string) bool {
	return len(k.Groups) == 1 && k.Groups[0] == group
}

// groupKeys returns the worker keys whose groups are exactly [group], newest
// first (never nil).
func (s *Server) groupKeys(ctx context.Context, group string) ([]*model.WorkerKey, error) {
	keys, err := s.store.ListWorkerKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := []*model.WorkerKey{}
	for _, k := range keys {
		if isGroupKey(k, group) {
			out = append(out, k)
		}
	}
	return out, nil
}

// groupKeyIDs returns the ids of the worker keys whose groups are exactly
// [group], for the admin's answer after managers were taken away. On error
// it logs a WARN and returns nil: the change is already done.
func (s *Server) groupKeyIDs(ctx context.Context, group string) []string {
	keys, err := s.groupKeys(ctx, group)
	if err != nil {
		s.logger.Warn("cannot list the worker keys of the group", "group", group, "error", err)
		return nil
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	return ids
}

// handleListWorkerGroups returns default_deny and every rule, sorted by
// group name, after reloading the live policy (see listAllRules); if the
// reload fails it answers 500.
// GET /api/v1/admin/worker-groups
func (s *Server) handleListWorkerGroups(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	list, e := s.listAllRules(r.Context(), requestActor(r))
	if e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	respondOK(w, reqID, map[string]any{
		"default_deny": list.DefaultDeny,
		"groups":       list.Rules,
	})
}

// handlePutWorkerGroup creates or replaces the rule of a group (see
// putRule).
// PUT /api/v1/admin/worker-groups/{group}
func (s *Server) handlePutWorkerGroup(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group := chi.URLParam(r, "group")
	// The group name and the users are checked here before the managers'
	// JSON form, so that a request with several faults is answered with the
	// same message as before the operation was shared; putRule checks them
	// again with the same functions.
	if err := groupacl.ValidateGroupName(group); err != nil {
		validationErr(w, reqID, err.Error())
		return
	}
	var req putWorkerGroupRequest
	if err := decodeStrict(w, r, &req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	if _, err := groupacl.ValidateUsers(req.Users); err != nil {
		validationErr(w, reqID, err.Error())
		return
	}
	// Absent = keep the stored managers.
	in := ui.PutRuleInput{Users: req.Users, Admins: req.Admins, KeepManagers: req.Managers == nil}
	if !in.KeepManagers {
		if string(req.Managers) == "null" {
			validationErr(w, reqID, `"managers": null is not allowed: use [] to clear, omit to keep`)
			return
		}
		var list []string
		if err := json.Unmarshal(req.Managers, &list); err != nil {
			validationErr(w, reqID, "invalid JSON body: managers: "+err.Error())
			return
		}
		in.Managers = list
	}
	res, e := s.putRule(r.Context(), requestActor(r), group, in)
	if e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	respondOK(w, reqID, workerGroupResponse{WorkerGroupPolicy: res.Rule, GroupKeyIDs: res.GroupKeyIDs})
}

// handleDeleteWorkerGroup removes the rule of a group (see deleteRule).
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
	res, e := s.deleteRule(r.Context(), requestActor(r), group)
	if e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	resp := map[string]any{
		"group":   res.Group,
		"deleted": true,
	}
	if res.GroupKeyIDs != nil {
		resp["group_key_ids"] = *res.GroupKeyIDs
	}
	respondOK(w, reqID, resp)
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
	if e := s.setDefaultDeny(r.Context(), requestActor(r), *req.DefaultDeny); e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	respondOK(w, reqID, map[string]any{"default_deny": *req.DefaultDeny})
}
