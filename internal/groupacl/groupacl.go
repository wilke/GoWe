// Package groupacl decides which submitters may send tasks to a worker group
// (#261). Worker keys decide which workers may join a group; this policy,
// stored in the database (worker_group_policies and the
// worker_groups.default_deny server setting) and changed at runtime by server
// admins, decides which users may target one with
// gowe:Execution.worker_group or the worker_group label.
//
// The scheduler enforces it at dispatch (authoritative) and when a FAILED
// task is re-queued, as do the UI resume/recompute handlers; the submission
// handlers use it to refuse early with a readable error. All of them hold the
// same *Policy; Reload swaps its rules for every holder at once.
package groupacl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/me/gowe/pkg/model"
)

// DefaultGroup is the key of the default worker group. An empty group name
// means the same group.
const DefaultGroup = "default"

// DefaultDenyKey is the server setting holding default_deny ("true" or
// "false"; no row = false).
const DefaultDenyKey = "worker_groups.default_deny"

// Size limits of a rule, checked by ValidateGroupName and ValidateUsers.
const (
	// MaxGroupNameBytes is the longest group name a rule may have.
	MaxGroupNameBytes = 64
	// MaxUsernameBytes is the longest username a users list may hold.
	MaxUsernameBytes = 256
	// MaxUsers is the most entries a users list may hold.
	MaxUsers = 1000
)

// groupNameRe is the worker group name rule, mirrored from
// internal/blessing/blessing.go (groupNameRe): letters, digits, '.', '_' or
// '-', not starting with a punctuation character (so no whitespace).
var groupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// anonymousUser is the username unauthenticated REST submissions are stored
// with (model.AnonymousUser). The policy treats it exactly like "".
var anonymousUser = model.AnonymousUser.Username

type rule struct {
	users  map[string]bool
	admins bool
}

// snapshot is one immutable set of rules. It is never modified after Reload
// built it, so readers need no lock.
type snapshot struct {
	defaultDeny bool
	groups      map[string]rule // key: group name, the default group as DefaultGroup
}

// live is the state shared by a Policy and the copies WithAdmins returns.
type live struct {
	// reloadMu serialises Reload, so the snapshot stored last is the one
	// read last: two changes reloading at once cannot leave the older rules
	// in force.
	reloadMu sync.Mutex
	current  atomic.Pointer[snapshot] // nil = no rules
}

// IsAdminFunc reports whether a username is a server admin.
type IsAdminFunc func(username string) bool

// Source is where Reload reads the rules from; *store.SQLStore satisfies it.
type Source interface {
	ListWorkerGroupPolicies(ctx context.Context) ([]*model.WorkerGroupPolicy, error)
	GetServerSetting(ctx context.Context, key string) (value string, found bool, err error)
}

// Policy decides whether a submitter may send tasks to a worker group.
// A nil *Policy, the zero value and a Policy that was never reloaded allow
// everything.
type Policy struct {
	state   *live
	isAdmin IsAdminFunc
}

// New returns a policy without rules (everything open). Load the stored
// rules with Reload.
func New() *Policy {
	return &Policy{state: &live{}}
}

// normalizeGroup folds the two spellings of the default group into one key.
func normalizeGroup(group string) string {
	if group == "" {
		return DefaultGroup
	}
	return group
}

// ValidateGroupName checks a group name as an admin gives it: letters,
// digits, '.', '_' or '-', starting with a letter or digit. The default group
// is written "default"; "" is refused, and so is a name longer than
// MaxGroupNameBytes.
func ValidateGroupName(name string) error {
	if name == "" {
		return errors.New(`group name is empty (the default group is "default")`)
	}
	if len(name) > MaxGroupNameBytes {
		return fmt.Errorf("group name is %d bytes long; at most %d are allowed", len(name), MaxGroupNameBytes)
	}
	if !groupNameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid group name (letters, digits, '.', '_' or '-', starting with a letter or digit)", name)
	}
	return nil
}

// ValidateUsers checks a users list and returns it without duplicates, in
// the order given (never nil). A list of more than MaxUsers entries
// (duplicates included), an empty name, a name longer than MaxUsernameBytes,
// a name with leading or trailing whitespace or with a control character
// anywhere, and "anonymous" are refused.
func ValidateUsers(users []string) ([]string, error) {
	if len(users) > MaxUsers {
		return nil, fmt.Errorf("users holds %d entries; at most %d are allowed", len(users), MaxUsers)
	}
	out := make([]string, 0, len(users))
	seen := make(map[string]bool, len(users))
	for _, u := range users {
		switch {
		case u == "":
			return nil, errors.New("users holds an empty username")
		case len(u) > MaxUsernameBytes:
			return nil, fmt.Errorf("users holds a username of %d bytes; at most %d are allowed", len(u), MaxUsernameBytes)
		case strings.TrimSpace(u) != u:
			return nil, fmt.Errorf("username %q has leading or trailing whitespace", u)
		case strings.IndexFunc(u, unicode.IsControl) >= 0:
			return nil, fmt.Errorf("username %q contains a control character", u)
		case u == anonymousUser:
			return nil, fmt.Errorf("users holds %q: anonymous submitters can never be granted a restricted group", anonymousUser)
		}
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out, nil
}

// ParseDefaultDeny parses the stored value of DefaultDenyKey: exactly "true"
// or "false".
func ParseDefaultDeny(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("server setting %s holds %q, want \"true\" or \"false\"", DefaultDenyKey, value)
}

// Reload reads the rules from src and makes them the rules of p and of every
// copy WithAdmins made of it, in one swap. On error nothing changes and the
// error is returned. A stored rule that fails ValidateGroupName or
// ValidateUsers (other than duplicates), or a default_deny value other than
// "true"/"false", is an error: the policy fails closed instead of skipping it.
func (p *Policy) Reload(ctx context.Context, src Source) error {
	if p == nil || p.state == nil {
		return errors.New("worker group policy: Reload needs a policy made by groupacl.New")
	}
	p.state.reloadMu.Lock()
	defer p.state.reloadMu.Unlock()

	s, err := load(ctx, src)
	if err != nil {
		return err
	}
	p.state.current.Store(s)
	return nil
}

// load builds a snapshot from src.
func load(ctx context.Context, src Source) (*snapshot, error) {
	s := &snapshot{}
	value, found, err := src.GetServerSetting(ctx, DefaultDenyKey)
	if err != nil {
		return nil, fmt.Errorf("worker group policy: read %s: %w", DefaultDenyKey, err)
	}
	if found {
		if s.defaultDeny, err = ParseDefaultDeny(value); err != nil {
			return nil, fmt.Errorf("worker group policy: %w", err)
		}
	}
	rows, err := src.ListWorkerGroupPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("worker group policy: list rules: %w", err)
	}
	s.groups = make(map[string]rule, len(rows))
	for _, gp := range rows {
		if err := ValidateGroupName(gp.Group); err != nil {
			return nil, fmt.Errorf("worker group policy: stored rule: %w", err)
		}
		users, err := ValidateUsers(gp.Users)
		if err != nil {
			return nil, fmt.Errorf("worker group policy: stored rule for group %q: %w", gp.Group, err)
		}
		r := rule{users: make(map[string]bool, len(users)), admins: gp.Admins}
		for _, u := range users {
			r.users[u] = true
		}
		s.groups[gp.Group] = r
	}
	return s, nil
}

// WithAdmins returns the policy with the admin lookup set (may be nil =
// nobody is admin). The returned policy shares p's rules: a Reload of either
// changes both. A nil policy stays nil (everything allowed).
func (p *Policy) WithAdmins(f IsAdminFunc) *Policy {
	if p == nil {
		return nil
	}
	return &Policy{state: p.state, isAdmin: f}
}

// rules returns the current snapshot, nil when there are no rules.
func (p *Policy) rules() *snapshot {
	if p == nil || p.state == nil {
		return nil
	}
	return p.state.current.Load()
}

// userIsAdmin reports whether username is a server admin. The anonymous
// (empty) username never is.
func (p *Policy) userIsAdmin(username string) bool {
	return username != "" && p.isAdmin != nil && p.isAdmin(username)
}

// Allowed reports whether username may use group ("" and "default" are the
// default group). The anonymous username ("anonymous", as unauthenticated
// REST submissions are stored) is treated exactly like "": never in a users
// list, never an admin.
func (p *Policy) Allowed(username, group string) bool {
	return p.allowedIn(p.rules(), username, group)
}

// allowedIn is Allowed against the snapshot s (nil = no rules).
func (p *Policy) allowedIn(s *snapshot, username, group string) bool {
	if s == nil {
		return true
	}
	if username == anonymousUser {
		username = ""
	}
	key := normalizeGroup(group)
	r, listed := s.groups[key]
	if !listed {
		if !s.defaultDeny || key == DefaultGroup {
			return true
		}
		return p.userIsAdmin(username)
	}
	if username == "" {
		return false
	}
	if r.users[username] {
		return true
	}
	return r.admins && p.userIsAdmin(username)
}

// Restricted reports whether any restriction is in force (for a start-up
// log line).
func (p *Policy) Restricted() bool {
	s := p.rules()
	return s != nil && (s.defaultDeny || len(s.groups) > 0)
}

// DefaultDeny reports whether unlisted groups other than the default group
// are denied to everyone but server admins.
func (p *Policy) DefaultDeny() bool {
	s := p.rules()
	return s != nil && s.defaultDeny
}

// Groups returns the names of the listed (restricted) groups, sorted. It
// never exposes the user lists.
func (p *Policy) Groups() []string {
	s := p.rules()
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.groups))
	for name := range s.groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Check returns nil when username may use every group in groups, else an
// error naming the first denied group: `user "X" may not use worker group "Y"`.
// Every group is checked against the same rules, even when a Reload happens
// meanwhile.
func (p *Policy) Check(username string, groups []string) error {
	s := p.rules()
	if s == nil {
		return nil
	}
	for _, g := range groups {
		if !p.allowedIn(s, username, g) {
			return fmt.Errorf("user %q may not use worker group %q", username, normalizeGroup(g))
		}
	}
	return nil
}

// DeniedReason is the reason recorded when the task of a submission by
// username is refused group: `submitter "X" may not use worker group "Y"`
// ("" is reported as "default"). Used where an existing task is re-queued
// (scheduler retry, UI resume/recompute), same text as the dispatch check.
func DeniedReason(username, group string) string {
	return fmt.Sprintf("submitter %q may not use worker group %q", username, normalizeGroup(group))
}

// SubmissionGroups returns the worker groups a submission is known to use at
// submission time, deduplicated, in order: the worker_group label, then the
// gowe:Execution.worker_group hint of every stored workflow step (the parser
// has already merged tool-level hints of the step's tool into it). Tool hints
// inside nested sub-workflows are not looked at here; the dispatch check
// covers them.
func SubmissionGroups(labels map[string]string, steps []model.Step) []string {
	var out []string
	seen := map[string]bool{}
	add := func(g string) {
		if g == "" || seen[g] {
			return
		}
		seen[g] = true
		out = append(out, g)
	}
	add(labels["worker_group"])
	for _, st := range steps {
		if st.Hints != nil {
			add(st.Hints.WorkerGroup)
		}
	}
	return out
}
