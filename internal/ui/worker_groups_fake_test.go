package ui

import (
	"context"
	"time"

	"github.com/me/gowe/pkg/model"
)

// fakeGroupAdmin is a GroupAdmin answering fixed data, so the worker group
// templates can be rendered in this package (the real implementation, and
// the tests of who may do what, are in internal/server, which this package
// cannot import). RemoveGroupWorker refuses, to render an operation error.
type fakeGroupAdmin struct{}

var _ GroupAdmin = fakeGroupAdmin{}

var fakeGroupTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func fakeGroupRule() *model.WorkerGroupPolicy {
	return &model.WorkerGroupPolicy{
		Group: "g", Users: []string{"alice", "svc/robot"}, Managers: []string{"mgr"}, Admins: true,
		UpdatedBy: "admin", CreatedAt: fakeGroupTime, UpdatedAt: fakeGroupTime,
	}
}

func fakeGroupKey() *model.WorkerKey {
	exp := fakeGroupTime.Add(24 * time.Hour)
	return &model.WorkerKey{
		ID: "wk_fake", Label: "node-1", KeyPrefix: "gwk_abcdef", Groups: []string{"g"},
		Description: "fake key", CreatedBy: "mgr", CreatedAt: fakeGroupTime, ExpiresAt: &exp,
	}
}

func (fakeGroupAdmin) ListRules(_ context.Context, actor GroupActor) (*RuleList, error) {
	return &RuleList{DefaultDeny: true, DefaultDenyKnown: actor.IsAdmin, Rules: []*model.WorkerGroupPolicy{fakeGroupRule()}}, nil
}

func (fakeGroupAdmin) GetRule(context.Context, GroupActor, string) (*model.WorkerGroupPolicy, error) {
	return fakeGroupRule(), nil
}

func (fakeGroupAdmin) PutRule(context.Context, GroupActor, string, PutRuleInput) (*PutRuleResult, error) {
	ids := []string{"wk_fake"}
	return &PutRuleResult{Rule: fakeGroupRule(), GroupKeyIDs: &ids}, nil
}

func (fakeGroupAdmin) DeleteRule(_ context.Context, _ GroupActor, group string) (*DeleteRuleResult, error) {
	ids := []string{"wk_fake"}
	return &DeleteRuleResult{Group: group, GroupKeyIDs: &ids}, nil
}

func (fakeGroupAdmin) SetDefaultDeny(context.Context, GroupActor, bool) error { return nil }

func (fakeGroupAdmin) AddGroupUser(context.Context, GroupActor, string, string) (*model.WorkerGroupPolicy, error) {
	return fakeGroupRule(), nil
}

func (fakeGroupAdmin) RemoveGroupUser(context.Context, GroupActor, string, string) (*model.WorkerGroupPolicy, error) {
	return fakeGroupRule(), nil
}

func (fakeGroupAdmin) ListGroupKeys(context.Context, GroupActor, string) ([]*model.WorkerKey, error) {
	return []*model.WorkerKey{fakeGroupKey()}, nil
}

func (fakeGroupAdmin) CreateGroupKey(context.Context, GroupActor, string, GroupKeyInput) (*CreatedGroupKey, error) {
	return &CreatedGroupKey{Key: fakeGroupKey(), Raw: "gwk_fake-raw-secret"}, nil
}

func (fakeGroupAdmin) RevokeGroupKey(context.Context, GroupActor, string, string) error { return nil }

func (fakeGroupAdmin) ListGroupWorkers(context.Context, GroupActor, string) ([]GroupWorker, error) {
	return []GroupWorker{
		{ID: "wrk_off", Name: "off-node", Hostname: "off.example", PeerAddr: "192.0.2.7", State: model.WorkerStateOffline,
			LastSeen: fakeGroupTime, RegisteredAt: fakeGroupTime, Version: "v1", Runtime: model.RuntimeNone, Removable: true},
		{ID: "wrk_on", Name: "on-node", Hostname: "on.example", PeerAddr: "192.0.2.8", State: model.WorkerStateOnline,
			LastSeen: time.Now().UTC(), RegisteredAt: fakeGroupTime, Version: "v1", Runtime: model.RuntimeNone, KeyID: "wk_fake"},
	}, nil
}

func (fakeGroupAdmin) RemoveGroupWorker(_ context.Context, _ GroupActor, _ string, id string) error {
	return &GroupError{Kind: GroupErrConflict, Message: "worker " + id + " is online: stop it first"}
}
