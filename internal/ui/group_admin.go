package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/me/gowe/pkg/model"
)

// GroupAdmin is the worker group access policy (#261) as the UI may change
// it: the same operations as /api/v1/admin/worker-groups and
// /api/v1/worker-groups, implemented once by the server (*server.Server),
// which also serves the REST routes through them. Every method checks the
// actor's right itself (server admin, or manager of the group by the live
// policy, re-checked after a reload under the server's write lock for
// writes). Whether the actor is a server admin is the caller's input
// (GroupActor.IsAdmin): the UI derives it on every request by the REST
// API's rule (actorFromContext, LiveRoleMiddleware, AdminMiddleware), never
// from the role recorded at login. Beyond that the UI holds no authorization
// of its own: it renders what the methods return and the errors they give.
//
// Every error a method returns is a *GroupError (see AsGroupError) whose
// Message may be shown to that actor: internal details are given to server
// admins only.
type GroupAdmin interface {
	// ListRules returns the rules the actor may see: every rule and
	// default_deny for a server admin (after reloading the live policy, so
	// the listing is what is in force), the rules of the groups the actor
	// manages for anyone else.
	ListRules(ctx context.Context, actor GroupActor) (*RuleList, error)
	// GetRule returns the rule of group (admins and managers of group).
	GetRule(ctx context.Context, actor GroupActor, group string) (*model.WorkerGroupPolicy, error)
	// PutRule creates or replaces the rule of group (admins only).
	PutRule(ctx context.Context, actor GroupActor, group string, in PutRuleInput) (*PutRuleResult, error)
	// DeleteRule removes the rule of group (admins only).
	DeleteRule(ctx context.Context, actor GroupActor, group string) (*DeleteRuleResult, error)
	// SetDefaultDeny sets default_deny (admins only).
	SetDefaultDeny(ctx context.Context, actor GroupActor, deny bool) error
	// AddGroupUser adds username to the users of group and returns the rule.
	AddGroupUser(ctx context.Context, actor GroupActor, group, username string) (*model.WorkerGroupPolicy, error)
	// RemoveGroupUser removes username from the users of group and returns
	// the rule.
	RemoveGroupUser(ctx context.Context, actor GroupActor, group, username string) (*model.WorkerGroupPolicy, error)
	// ListGroupKeys returns the worker keys whose groups are exactly
	// [group], newest first.
	ListGroupKeys(ctx context.Context, actor GroupActor, group string) ([]*model.WorkerKey, error)
	// CreateGroupKey issues a worker key valid for exactly [group]; the raw
	// key is in the result this once and never again.
	CreateGroupKey(ctx context.Context, actor GroupActor, group string, in GroupKeyInput) (*CreatedGroupKey, error)
	// RevokeGroupKey deletes a worker key whose groups are exactly [group].
	RevokeGroupKey(ctx context.Context, actor GroupActor, group, id string) error
	// ListGroupWorkers returns the workers of group, offline ones included,
	// sorted by name.
	ListGroupWorkers(ctx context.Context, actor GroupActor, group string) ([]GroupWorker, error)
	// RemoveGroupWorker removes an offline worker of group that has sent no
	// heartbeat for 3 x the worker timeout.
	RemoveGroupWorker(ctx context.Context, actor GroupActor, group, id string) error
	// MayChangeWorkflow returns nil when actor may change or delete wf by
	// the service workflow rule: a service workflow (gowe:Service) only by a
	// server admin or a manager of its worker group (else forbidden); any
	// other workflow passes. It does not check ownership.
	MayChangeWorkflow(ctx context.Context, actor GroupActor, wf *model.Workflow) error
}

// GroupActor is the user a GroupAdmin operation acts for. An empty Username
// (or "anonymous") is refused with GroupErrUnauthorized by the operations on
// one group.
type GroupActor struct {
	Username string
	IsAdmin  bool
}

// actorFromContext returns the session user of ctx as a GroupActor (the zero
// actor for no session). IsAdmin is set only when the user is a server admin
// now: the session's role was derived for this request (LiveRoleMiddleware or
// AdminMiddleware, by the REST API's rule) and is admin. A session whose role
// is the one recorded at login never makes an admin actor.
func actorFromContext(ctx context.Context) GroupActor {
	sess := SessionFromContext(ctx)
	if sess == nil {
		return GroupActor{}
	}
	live, _ := ctx.Value(liveRoleContextKey).(bool)
	return GroupActor{Username: sess.Username, IsAdmin: live && sess.IsAdmin()}
}

// GroupErrorKind classifies a GroupError.
type GroupErrorKind string

// The kinds of GroupError. Unauthorized and Unavailable exist so that the
// REST routes, which are served through the same methods, keep their 401 and
// 503 answers.
const (
	GroupErrInvalid      GroupErrorKind = "invalid"
	GroupErrUnauthorized GroupErrorKind = "unauthorized"
	GroupErrForbidden    GroupErrorKind = "forbidden"
	GroupErrNotFound     GroupErrorKind = "not_found"
	GroupErrConflict     GroupErrorKind = "conflict"
	GroupErrUnavailable  GroupErrorKind = "unavailable"
	GroupErrInternal     GroupErrorKind = "internal"
)

// GroupError is the error of a GroupAdmin operation. Message is safe to show
// to the actor the operation ran for.
type GroupError struct {
	Kind    GroupErrorKind
	Message string
}

func (e *GroupError) Error() string { return e.Message }

// HTTPStatus is the HTTP status that answers an error of this kind.
func (e *GroupError) HTTPStatus() int {
	switch e.Kind {
	case GroupErrInvalid:
		return http.StatusBadRequest
	case GroupErrUnauthorized:
		return http.StatusUnauthorized
	case GroupErrForbidden:
		return http.StatusForbidden
	case GroupErrNotFound:
		return http.StatusNotFound
	case GroupErrConflict:
		return http.StatusConflict
	case GroupErrUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// AsGroupError returns err as a *GroupError (nil for nil). An error of
// another type is reported as an internal error without its text, which is
// not known to be safe to show.
func AsGroupError(err error) *GroupError {
	if err == nil {
		return nil
	}
	var ge *GroupError
	if errors.As(err, &ge) {
		return ge
	}
	return &GroupError{Kind: GroupErrInternal, Message: "internal error; ask a server admin"}
}

// RuleList is the answer of ListRules.
type RuleList struct {
	// DefaultDeny is set for a server admin only (DefaultDenyKnown).
	DefaultDeny      bool
	DefaultDenyKnown bool
	// Rules are sorted by group name (never nil).
	Rules []*model.WorkerGroupPolicy
}

// PutRuleInput is the rule PutRule stores.
type PutRuleInput struct {
	Users []string
	// Managers replaces the stored managers, unless KeepManagers is set (an
	// existing rule keeps its managers, a new one has none).
	Managers     []string
	KeepManagers bool
	Admins       bool
}

// PutRuleResult is the answer of PutRule.
type PutRuleResult struct {
	// Rule is the rule as stored.
	Rule *model.WorkerGroupPolicy
	// GroupKeyIDs, when not nil, names the worker keys whose groups are
	// exactly [group] after the change took managers away: they stay valid.
	GroupKeyIDs *[]string
}

// DeleteRuleResult is the answer of DeleteRule.
type DeleteRuleResult struct {
	Group string
	// GroupKeyIDs, when not nil, names the worker keys whose groups are
	// exactly [group] (they outlive the rule and stay valid); set when the
	// rule had managers or the group has such keys.
	GroupKeyIDs *[]string
}

// GroupKeyInput is a group key to issue.
type GroupKeyInput struct {
	Label       string
	Description string
	ExpiresAt   *time.Time
}

// CreatedGroupKey is a key CreateGroupKey issued. Raw is the key itself,
// available this once only.
type CreatedGroupKey struct {
	Key *model.WorkerKey
	Raw string
}

// GroupWorkerOtherKey is GroupWorker.KeyID for a worker whose key is not one
// of the group's own keys.
const GroupWorkerOtherKey = "other"

// GroupWorker is a worker as listed to the managers of its group (and server
// admins): a projection of model.Worker. Its JSON form is the REST answer of
// GET /api/v1/worker-groups/{group}/workers.
type GroupWorker struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Hostname     string                 `json:"hostname"`
	PeerAddr     string                 `json:"peer_addr"`
	State        model.WorkerState      `json:"state"`
	LastSeen     time.Time              `json:"last_seen"`
	RegisteredAt time.Time              `json:"registered_at"`
	Version      string                 `json:"version"`
	Runtime      model.ContainerRuntime `json:"runtime"`
	GPUEnabled   bool                   `json:"gpu_enabled"`
	CurrentTask  string                 `json:"current_task"`
	// KeyID is the id of the registering key when its groups are exactly
	// [group], GroupWorkerOtherKey for any other key, "" when the worker is
	// bound to no key (worker auth was off when it registered).
	KeyID string `json:"key_id"`
	// Removable reports whether the server's removal rule (offline and no
	// heartbeat for 3 x the worker timeout) held when the list was made. A
	// hint for the UI only: the removal checks again inside its delete.
	Removable bool `json:"-"`
}
