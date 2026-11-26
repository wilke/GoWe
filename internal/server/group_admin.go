package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// The worker group operations (#261), implemented once and used by both the
// REST routes (handler_worker_groups.go, handler_worker_group_managers.go,
// handler_group_workers.go: decode, call, encode) and the web UI (through
// ui.GroupAdmin, which *Server implements). Every check lives here: the
// admin right, the manager right (re-checked against a freshly reloaded
// policy under groupPolicyMu before a manager's write), the limits and the
// conflicts, so the two front ends cannot diverge. Whether the actor is a
// server admin is the front end's input (ui.GroupActor.IsAdmin); both derive
// it per request by the same rule: the REST API in apiAuthMiddleware, the UI
// in its LiveRoleMiddleware and AdminMiddleware (configured admin or stored
// role admin; the UI does not consult the auth denylist).

var _ ui.GroupAdmin = (*Server)(nil)

// groupErr returns a *ui.GroupError of kind with msg.
func groupErr(kind ui.GroupErrorKind, msg string) *ui.GroupError {
	return &ui.GroupError{Kind: kind, Message: msg}
}

// asError returns e as an error, nil (an untyped nil) when e is nil.
func asError(e *ui.GroupError) error {
	if e == nil {
		return nil
	}
	return e
}

// groupErrorCode is the API error code of a GroupError kind.
func groupErrorCode(kind ui.GroupErrorKind) model.ErrorCode {
	switch kind {
	case ui.GroupErrInvalid:
		return model.ErrValidation
	case ui.GroupErrUnauthorized:
		return model.ErrUnauthorized
	case ui.GroupErrForbidden:
		return model.ErrForbidden
	case ui.GroupErrNotFound:
		return model.ErrNotFound
	case ui.GroupErrConflict:
		return model.ErrConflict
	case ui.GroupErrUnavailable:
		return model.ErrUnavailable
	default:
		return model.ErrInternal
	}
}

// respondGroupError answers err (an error of a group operation) in the API
// envelope.
func respondGroupError(w http.ResponseWriter, reqID string, err error) {
	ge := ui.AsGroupError(err)
	respondError(w, reqID, ge.HTTPStatus(), &model.APIError{Code: groupErrorCode(ge.Kind), Message: ge.Message})
}

// requestActor returns the caller of r as the actor of a group operation;
// no user or the anonymous user is the zero actor (refused with 401 by the
// operations on one group). A service identity is never an admin here, and
// mayManageGroup gives it no manager right.
func requestActor(r *http.Request) ui.GroupActor {
	uc := UserFromContext(r.Context())
	if uc == nil || uc.User == nil || uc.User.IsAnonymous() {
		return ui.GroupActor{}
	}
	if isServiceCaller(uc) {
		return ui.GroupActor{Username: uc.User.Username}
	}
	return ui.GroupActor{Username: uc.User.Username, IsAdmin: uc.User.IsAdmin()}
}

// internalErrorFor returns an internal error with msg for a server admin and
// internalErrorMessage for anyone else. The caller has logged msg.
func internalErrorFor(actor ui.GroupActor, msg string) *ui.GroupError {
	if !actor.IsAdmin {
		msg = internalErrorMessage
	}
	return groupErr(ui.GroupErrInternal, msg)
}

// internalErrFor logs err at ERROR and returns it as internalErrorFor does.
func (s *Server) internalErrFor(actor ui.GroupActor, group string, err error) *ui.GroupError {
	s.logger.Error("request failed with an internal error", "actor", actor.Username,
		"group", group, "error", err)
	return internalErrorFor(actor, err.Error())
}

// errNoGroupPolicy is answered when the server has no live policy to load a
// change into, before anything is written.
func (s *Server) errNoGroupPolicy() *ui.GroupError {
	if s.groupPolicy != nil {
		return nil
	}
	return groupErr(ui.GroupErrUnavailable, "worker group policy is not enabled on this server")
}

// requireAdminActor refuses an actor who is not a server admin.
func requireAdminActor(actor ui.GroupActor) *ui.GroupError {
	if actor.IsAdmin {
		return nil
	}
	return groupErr(ui.GroupErrForbidden, "admin access required")
}

// errUnauthenticated refuses no user and the anonymous user.
func errUnauthenticated(actor ui.GroupActor) *ui.GroupError {
	if actor.Username == "" || actor.Username == model.AnonymousUser.Username {
		return groupErr(ui.GroupErrUnauthorized, "authentication required")
	}
	return nil
}

// mayManageGroup reports whether actor may manage group by the live policy:
// a server admin, or a manager of group. A service identity never may (it
// cannot be an admin, and groupacl never loads it as a manager).
func (s *Server) mayManageGroup(actor ui.GroupActor, group string) bool {
	if model.IsServiceUsername(actor.Username) {
		return false
	}
	return actor.IsAdmin || s.groupPolicy.IsManager(actor.Username, group)
}

// errForbiddenGroup refuses a caller who may not manage group.
func errForbiddenGroup(group string) *ui.GroupError {
	return groupErr(ui.GroupErrForbidden, fmt.Sprintf("only server admins and managers of worker group %q may do this", group))
}

// errRuleNotFound is the 404 of a group without a rule.
func errRuleNotFound(group string) *ui.GroupError {
	return groupErr(ui.GroupErrNotFound, model.NewNotFoundError("worker group policy", group).Message)
}

// checkGroupAccess runs the checks every operation on one group starts with,
// in this order: an authenticated, non-anonymous actor (401), a valid group
// name (400), a live policy (503), the actor is a server admin or a manager
// of group in the live policy as it is (403).
func (s *Server) checkGroupAccess(actor ui.GroupActor, group string) *ui.GroupError {
	if e := errUnauthenticated(actor); e != nil {
		return e
	}
	if err := groupacl.ValidateGroupName(group); err != nil {
		return groupErr(ui.GroupErrInvalid, err.Error())
	}
	if e := s.errNoGroupPolicy(); e != nil {
		return e
	}
	if !s.mayManageGroup(actor, group) {
		return errForbiddenGroup(group)
	}
	return nil
}

// beginGroupWrite takes groupPolicyMu, reloads the live policy and checks
// the actor's right on group again against it (403), then that the group has
// a rule (404). On success the caller writes with the returned context
// (writeContextFrom(ctx)) and must call done, which releases the mutex; on
// failure nothing is held.
func (s *Server) beginGroupWrite(ctx context.Context, actor ui.GroupActor, group string) (context.Context, func(), *ui.GroupError) {
	s.groupPolicyMu.Lock()
	wctx, cancel := writeContextFrom(ctx)
	done := func() {
		cancel()
		s.groupPolicyMu.Unlock()
	}
	if err := s.groupPolicy.Reload(wctx, s.store); err != nil {
		s.logger.Error("worker group policy could not be reloaded; the previous policy stays in force",
			"actor", actor.Username, "error", err)
		done()
		return nil, nil, internalErrorFor(actor,
			"the live worker group policy could not be reloaded from the database (the previous policy stays in force): "+err.Error())
	}
	if !s.mayManageGroup(actor, group) {
		done()
		return nil, nil, errForbiddenGroup(group)
	}
	if e := s.requireGroupRule(wctx, actor, group); e != nil {
		done()
		return nil, nil, e
	}
	return wctx, done, nil
}

// reloadAfterWrite loads the stored rules into the live policy after a
// write, with the write's context. On failure it logs ERROR and returns an
// internal error (the detail to server admins only); the previous rules stay
// in force until a later reload succeeds (the next change or the admin
// listing).
func (s *Server) reloadAfterWrite(ctx context.Context, actor ui.GroupActor) *ui.GroupError {
	if err := s.groupPolicy.Reload(ctx, s.store); err != nil {
		s.logger.Error("worker group policy stored but not reloaded; the previous policy stays in force",
			"actor", actor.Username, "error", err)
		return internalErrorFor(actor,
			"the change was stored but the live worker group policy could not be reloaded (the previous policy stays in force): "+err.Error())
	}
	return nil
}

// requireGroupRule returns 404 when group has no rule (500 when the rules
// cannot be read).
func (s *Server) requireGroupRule(ctx context.Context, actor ui.GroupActor, group string) *ui.GroupError {
	_, e := s.groupRule(ctx, actor, group)
	return e
}

// groupRule returns the stored rule of group (404 when there is none).
func (s *Server) groupRule(ctx context.Context, actor ui.GroupActor, group string) (*model.WorkerGroupPolicy, *ui.GroupError) {
	rule, err := s.storedWorkerGroup(ctx, group)
	if err != nil {
		return nil, s.internalErrFor(actor, group, err)
	}
	if rule == nil {
		return nil, errRuleNotFound(group)
	}
	return rule, nil
}

// ListRules implements ui.GroupAdmin: listAllRules for a server admin,
// listManagedRules for anyone else.
func (s *Server) ListRules(ctx context.Context, actor ui.GroupActor) (*ui.RuleList, error) {
	var (
		list *ui.RuleList
		e    *ui.GroupError
	)
	if actor.IsAdmin {
		list, e = s.listAllRules(ctx, actor)
	} else {
		list, e = s.listManagedRules(ctx, actor)
	}
	if e != nil {
		return nil, e
	}
	return list, nil
}

// listAllRules returns default_deny and every rule, sorted by group name
// (admins only). It first reloads the live policy (when the server has one),
// under the write lock so no change lands between the reload and the read,
// so the listing is what is in force and a policy left behind by a failed
// reload is brought in line with the store.
func (s *Server) listAllRules(ctx context.Context, actor ui.GroupActor) (*ui.RuleList, *ui.GroupError) {
	if e := requireAdminActor(actor); e != nil {
		return nil, e
	}
	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	if s.groupPolicy != nil {
		if err := s.groupPolicy.Reload(ctx, s.store); err != nil {
			s.logger.Error("worker group policy could not be reloaded; the previous policy stays in force",
				"admin", actor.Username, "error", err)
			msg := "the live worker group policy could not be reloaded from the database (the previous policy stays in force): " + err.Error()
			if groupacl.IsInvalidStoredRule(err) {
				// The admin page is an error page then; say how to repair it.
				msg += `. Remove the invalid rule with "gowe admin worker-groups delete <group>".`
			}
			return nil, internalErrorFor(actor, msg)
		}
	}
	deny, groups, err := s.storedWorkerGroups(ctx)
	if err != nil {
		return nil, internalErrorFor(actor, err.Error())
	}
	return &ui.RuleList{DefaultDeny: deny, DefaultDenyKnown: true, Rules: groups}, nil
}

// listManagedRules returns the rules the actor may manage by the live
// policy (a server admin: every rule), sorted by group name, never nil.
func (s *Server) listManagedRules(ctx context.Context, actor ui.GroupActor) (*ui.RuleList, *ui.GroupError) {
	if e := errUnauthenticated(actor); e != nil {
		return nil, e
	}
	rules, err := s.store.ListWorkerGroupPolicies(ctx)
	if err != nil {
		return nil, s.internalErrFor(actor, "", err)
	}
	out := []*model.WorkerGroupPolicy{}
	for _, g := range rules {
		if s.mayManageGroup(actor, g.Group) {
			out = append(out, g)
		}
	}
	return &ui.RuleList{Rules: out}, nil
}

// GetRule implements ui.GroupAdmin.
func (s *Server) GetRule(ctx context.Context, actor ui.GroupActor, group string) (*model.WorkerGroupPolicy, error) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	rule, e := s.groupRule(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	return rule, nil
}

// PutRule implements ui.GroupAdmin: it creates or replaces the rule of group
// (admins only). Checks, in order: admin (403), group name, users, managers
// and managers on the default group (400), a live policy (503).
func (s *Server) PutRule(ctx context.Context, actor ui.GroupActor, group string, in ui.PutRuleInput) (*ui.PutRuleResult, error) {
	res, e := s.putRule(ctx, actor, group, in)
	if e != nil {
		return nil, e
	}
	return res, nil
}

func (s *Server) putRule(ctx context.Context, actor ui.GroupActor, group string, in ui.PutRuleInput) (*ui.PutRuleResult, *ui.GroupError) {
	if e := requireAdminActor(actor); e != nil {
		return nil, e
	}
	if err := groupacl.ValidateGroupName(group); err != nil {
		return nil, groupErr(ui.GroupErrInvalid, err.Error())
	}
	users, err := groupacl.ValidateUsers(in.Users)
	if err != nil {
		return nil, groupErr(ui.GroupErrInvalid, err.Error())
	}
	keepManagers := in.KeepManagers
	var managers []string
	if !keepManagers {
		if managers, err = groupacl.ValidateManagers(in.Managers); err != nil {
			return nil, groupErr(ui.GroupErrInvalid, err.Error())
		}
		if err := groupacl.CheckManagersGroup(group, managers); err != nil {
			return nil, groupErr(ui.GroupErrInvalid, err.Error())
		}
	}
	if e := s.errNoGroupPolicy(); e != nil {
		return nil, e
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	wctx, cancel := writeContextFrom(ctx)
	defer cancel()

	// The managers before the change, to tell the admin about the keys of
	// the group when some are taken away. Best effort: an unreadable row
	// must not block the PUT that repairs it.
	var oldManagers []string
	if !keepManagers {
		prev, err := s.storedWorkerGroup(wctx, group)
		if err != nil {
			s.logger.Warn("cannot read the managers before the change; removed managers will not be reported",
				"group", group, "error", err)
		} else if prev != nil {
			oldManagers = prev.Managers
		}
	}

	admin := actor.Username
	now := time.Now().UTC()
	rule := &model.WorkerGroupPolicy{Group: group, Users: users, Managers: managers, Admins: in.Admins, UpdatedBy: admin, CreatedAt: now, UpdatedAt: now}
	if err := s.store.UpsertWorkerGroupPolicy(wctx, rule); err != nil {
		return nil, internalErrorFor(actor, err.Error())
	}
	if e := s.reloadAfterWrite(wctx, actor); e != nil {
		return nil, e
	}

	// Answer with the rule as stored (created_at of a replaced rule is
	// kept, managers may have been kept). The change is done: a failed read
	// falls back to the request when it gave the managers; when they were
	// kept the request does not know them, so the answer is an internal
	// error rather than a rule claiming no managers.
	stored, err := s.storedWorkerGroup(wctx, group)
	if err != nil || stored == nil {
		if keepManagers {
			reason := "it is no longer stored"
			if err != nil {
				reason = err.Error()
			}
			s.logger.Error("worker group policy set and reloaded but not read back", "admin", admin, "group", group, "error", reason)
			return nil, internalErrorFor(actor, fmt.Sprintf(
				"the rule of worker group %q was stored and loaded into the live policy, but could not be read back (%s), so its kept managers cannot be shown; GET /api/v1/admin/worker-groups lists it",
				group, reason))
		}
		stored = rule
		if stored.Managers == nil {
			stored.Managers = []string{}
		}
	}
	s.logger.Info("worker group policy set", "admin", admin, "group", group, "users", len(users),
		"managers", len(stored.Managers), "admins", in.Admins)

	res := &ui.PutRuleResult{Rule: stored}
	removed := 0
	for _, m := range oldManagers {
		if !slices.Contains(managers, m) {
			removed++
		}
	}
	if removed > 0 {
		ids := s.groupKeyIDs(wctx, group)
		if ids != nil {
			res.GroupKeyIDs = &ids
		}
		s.logger.Info("worker group managers removed; the group's worker keys stay valid",
			"admin", admin, "group", group, "managers_removed", removed, "group_keys", len(ids))
	}
	return res, nil
}

// DeleteRule implements ui.GroupAdmin: it removes the rule of group (admins
// only); the group becomes open, or admin-only under default_deny. The name
// is not validated beyond being non-empty, so a stored row with an invalid
// name (which makes every reload fail) can be removed; the store decides
// 404.
func (s *Server) DeleteRule(ctx context.Context, actor ui.GroupActor, group string) (*ui.DeleteRuleResult, error) {
	res, e := s.deleteRule(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	return res, nil
}

func (s *Server) deleteRule(ctx context.Context, actor ui.GroupActor, group string) (*ui.DeleteRuleResult, *ui.GroupError) {
	if e := requireAdminActor(actor); e != nil {
		return nil, e
	}
	if group == "" {
		return nil, groupErr(ui.GroupErrInvalid, `group name is empty (the default group is "default")`)
	}
	if e := s.errNoGroupPolicy(); e != nil {
		return nil, e
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	wctx, cancel := writeContextFrom(ctx)
	defer cancel()

	// The number of users of the removed rule, for the log line; -1 when the
	// rules cannot be read (an unreadable row must still be removable).
	removedUsers := -1
	hadManagers := false
	if groups, err := s.store.ListWorkerGroupPolicies(wctx); err == nil {
		removedUsers = 0
		for _, g := range groups {
			if g.Group == group {
				removedUsers = len(g.Users)
				hadManagers = len(g.Managers) > 0
			}
		}
	}
	existed, err := s.store.DeleteWorkerGroupPolicy(wctx, group)
	if err != nil {
		return nil, internalErrorFor(actor, err.Error())
	}
	if !existed {
		return nil, errRuleNotFound(group)
	}
	if e := s.reloadAfterWrite(wctx, actor); e != nil {
		return nil, e
	}
	s.logger.Info("worker group policy deleted", "admin", actor.Username, "group", group, "users", removedUsers)
	res := &ui.DeleteRuleResult{Group: group}
	// Keys issued for this group alone outlive its rule (and its managers);
	// name them so the admin can decide.
	if ids := s.groupKeyIDs(wctx, group); ids != nil && (hadManagers || len(ids) > 0) {
		res.GroupKeyIDs = &ids
		s.logger.Info("deleted worker group rule had managers or group keys; the keys stay valid",
			"admin", actor.Username, "group", group, "group_keys", len(ids))
	}
	return res, nil
}

// SetDefaultDeny implements ui.GroupAdmin (admins only).
func (s *Server) SetDefaultDeny(ctx context.Context, actor ui.GroupActor, deny bool) error {
	return asError(s.setDefaultDeny(ctx, actor, deny))
}

func (s *Server) setDefaultDeny(ctx context.Context, actor ui.GroupActor, deny bool) *ui.GroupError {
	if e := requireAdminActor(actor); e != nil {
		return e
	}
	if e := s.errNoGroupPolicy(); e != nil {
		return e
	}

	s.groupPolicyMu.Lock()
	defer s.groupPolicyMu.Unlock()
	wctx, cancel := writeContextFrom(ctx)
	defer cancel()

	admin := actor.Username
	if err := s.store.SetServerSetting(wctx, groupacl.DefaultDenyKey, strconv.FormatBool(deny), admin); err != nil {
		return internalErrorFor(actor, err.Error())
	}
	if e := s.reloadAfterWrite(wctx, actor); e != nil {
		return e
	}
	s.logger.Info("worker group policy default_deny set", "admin", admin, "default_deny", deny)
	return nil
}

// AddGroupUser implements ui.GroupAdmin.
func (s *Server) AddGroupUser(ctx context.Context, actor ui.GroupActor, group, username string) (*model.WorkerGroupPolicy, error) {
	rule, e := s.addGroupUser(ctx, actor, group, username)
	if e != nil {
		return nil, e
	}
	return rule, nil
}

func (s *Server) addGroupUser(ctx context.Context, actor ui.GroupActor, group, username string) (*model.WorkerGroupPolicy, *ui.GroupError) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	names, err := groupacl.ValidateUsers([]string{username})
	if err != nil {
		return nil, groupErr(ui.GroupErrInvalid, err.Error())
	}

	wctx, done, e := s.beginGroupWrite(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	defer done()
	changed, err := s.store.AddWorkerGroupUser(wctx, group, names[0], actor.Username)
	switch {
	case errors.Is(err, store.ErrWorkerGroupPolicyNotFound):
		return nil, errRuleNotFound(group)
	case errors.Is(err, store.ErrWorkerGroupUsersFull):
		return nil, groupErr(ui.GroupErrInvalid, err.Error())
	case err != nil:
		return nil, s.internalErrFor(actor, group, err)
	}
	if e := s.reloadAfterWrite(wctx, actor); e != nil {
		return nil, e
	}
	s.logger.Info("worker group changed", "actor", actor.Username, "group", group, "action", "add-user", "changed", changed)
	return s.groupRule(wctx, actor, group)
}

// RemoveGroupUser implements ui.GroupAdmin; not found when the user is not
// listed.
func (s *Server) RemoveGroupUser(ctx context.Context, actor ui.GroupActor, group, username string) (*model.WorkerGroupPolicy, error) {
	rule, e := s.removeGroupUser(ctx, actor, group, username)
	if e != nil {
		return nil, e
	}
	return rule, nil
}

func (s *Server) removeGroupUser(ctx context.Context, actor ui.GroupActor, group, username string) (*model.WorkerGroupPolicy, *ui.GroupError) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	if username == "" {
		return nil, groupErr(ui.GroupErrInvalid, "username is empty")
	}

	wctx, done, e := s.beginGroupWrite(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	defer done()
	changed, err := s.store.RemoveWorkerGroupUser(wctx, group, username, actor.Username)
	switch {
	case errors.Is(err, store.ErrWorkerGroupPolicyNotFound):
		return nil, errRuleNotFound(group)
	case err != nil:
		return nil, s.internalErrFor(actor, group, err)
	}
	if !changed {
		return nil, groupErr(ui.GroupErrNotFound, fmt.Sprintf("the user is not in the users of worker group %q", group))
	}
	if e := s.reloadAfterWrite(wctx, actor); e != nil {
		return nil, e
	}
	s.logger.Info("worker group changed", "actor", actor.Username, "group", group, "action", "remove-user")
	return s.groupRule(wctx, actor, group)
}

// ListGroupKeys implements ui.GroupAdmin.
func (s *Server) ListGroupKeys(ctx context.Context, actor ui.GroupActor, group string) ([]*model.WorkerKey, error) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	if e := s.requireGroupRule(ctx, actor, group); e != nil {
		return nil, e
	}
	keys, err := s.groupKeys(ctx, group)
	if err != nil {
		return nil, s.internalErrFor(actor, group, err)
	}
	return keys, nil
}

// CreateGroupKey implements ui.GroupAdmin: it issues a worker key valid for
// exactly [group], created_by the actor, through the admin create code path
// (mintWorkerKey). Refused as a conflict while worker authentication is not
// enabled (a key would then not restrict anything, and enabling worker auth
// for the whole fleet is an admin's decision) and while the group already
// has maxGroupWorkerKeys keys whose groups are exactly [group].
func (s *Server) CreateGroupKey(ctx context.Context, actor ui.GroupActor, group string, in ui.GroupKeyInput) (*ui.CreatedGroupKey, error) {
	created, e := s.createGroupKey(ctx, actor, group, in)
	if e != nil {
		return nil, e
	}
	return created, nil
}

func (s *Server) createGroupKey(ctx context.Context, actor ui.GroupActor, group string, in ui.GroupKeyInput) (*ui.CreatedGroupKey, *ui.GroupError) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	wctx, done, e := s.beginGroupWrite(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	defer done()
	if s.workerAuth == nil || !s.workerAuth.Enabled(wctx) {
		return nil, groupErr(ui.GroupErrConflict, "worker authentication is not enabled on this server: an admin must create the first worker key")
	}
	// Counted under groupPolicyMu, so concurrent requests cannot pass the
	// bound together.
	existing, err := s.groupKeys(wctx, group)
	if err != nil {
		return nil, s.internalErrFor(actor, group, err)
	}
	if len(existing) >= maxGroupWorkerKeys {
		return nil, groupErr(ui.GroupErrConflict, fmt.Sprintf("worker group %q has %d worker keys valid for exactly this group; at most %d are allowed: revoke unused keys first",
			group, len(existing), maxGroupWorkerKeys))
	}
	key, raw, e := s.mintWorkerKey(wctx, actor, group, createWorkerKeyRequest{
		Label:       in.Label,
		Groups:      []string{group},
		Description: in.Description,
		ExpiresAt:   in.ExpiresAt,
	}, actor.Username)
	if e != nil {
		return nil, e
	}
	s.logger.Info("worker group changed", "actor", actor.Username, "group", group, "action", "create-key", "key_id", key.ID)
	return &ui.CreatedGroupKey{Key: key, Raw: raw}, nil
}

// RevokeGroupKey implements ui.GroupAdmin; not found when the key does not
// exist or has other groups.
func (s *Server) RevokeGroupKey(ctx context.Context, actor ui.GroupActor, group, id string) error {
	return asError(s.revokeGroupKey(ctx, actor, group, id))
}

func (s *Server) revokeGroupKey(ctx context.Context, actor ui.GroupActor, group, id string) *ui.GroupError {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return e
	}
	wctx, done, e := s.beginGroupWrite(ctx, actor, group)
	if e != nil {
		return e
	}
	defer done()
	deleted, err := s.store.DeleteWorkerKeyForGroup(wctx, id, group)
	if err != nil {
		return s.internalErrFor(actor, group, err)
	}
	if !deleted {
		return groupErr(ui.GroupErrNotFound, model.NewNotFoundError("worker key of group "+group, id).Message)
	}
	s.logger.Info("worker group changed", "actor", actor.Username, "group", group, "action", "revoke-key", "key_id", id)
	return nil
}

// ListGroupWorkers implements ui.GroupAdmin: the workers of group (whatever
// key they registered with), offline ones included, sorted by name, with the
// peer address (the actor is a server admin or a manager of group).
func (s *Server) ListGroupWorkers(ctx context.Context, actor ui.GroupActor, group string) ([]ui.GroupWorker, error) {
	workers, e := s.listGroupWorkers(ctx, actor, group)
	if e != nil {
		return nil, e
	}
	return workers, nil
}

func (s *Server) listGroupWorkers(ctx context.Context, actor ui.GroupActor, group string) ([]ui.GroupWorker, *ui.GroupError) {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return nil, e
	}
	if e := s.requireGroupRule(ctx, actor, group); e != nil {
		return nil, e
	}
	keys, err := s.groupKeys(ctx, group)
	if err != nil {
		return nil, s.internalErrFor(actor, group, err)
	}
	own := make(map[string]bool, len(keys))
	for _, k := range keys {
		own[k.ID] = true
	}
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return nil, s.internalErrFor(actor, group, err)
	}
	now := time.Now()
	out := []ui.GroupWorker{}
	for _, wk := range workers {
		if workerGroupName(wk) != group {
			continue
		}
		keyID := wk.KeyID
		if keyID != "" && !own[keyID] {
			keyID = groupWorkerOtherKey
		}
		out = append(out, ui.GroupWorker{
			ID:           wk.ID,
			Name:         wk.Name,
			Hostname:     wk.Hostname,
			PeerAddr:     wk.PeerAddr,
			State:        wk.State,
			LastSeen:     wk.LastSeen,
			RegisteredAt: wk.RegisteredAt,
			Version:      wk.Version,
			Runtime:      wk.Runtime,
			GPUEnabled:   wk.GPUEnabled,
			CurrentTask:  wk.CurrentTask,
			KeyID:        keyID,
			Removable:    workerRemovable(wk, workerRemovalSilence, now),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// RemoveGroupWorker implements ui.GroupAdmin: it removes an offline, silent
// worker of group (see removeWorker) under the same lock-reload-recheck as
// the other manager writes.
func (s *Server) RemoveGroupWorker(ctx context.Context, actor ui.GroupActor, group, id string) error {
	if e := s.checkGroupAccess(actor, group); e != nil {
		return e
	}
	wctx, done, e := s.beginGroupWrite(ctx, actor, group)
	if e != nil {
		return e
	}
	defer done()
	return asError(s.removeWorker(wctx, actor, id, group))
}
