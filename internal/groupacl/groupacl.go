// Package groupacl decides which submitters may send tasks to a worker group
// (#261). Worker keys decide which workers may join a group; this policy,
// stored in the database (worker_group_policies and the
// worker_groups.default_deny server setting) and changed at runtime by server
// admins, decides which users may target one with
// gowe:Execution.worker_group or the worker_group label. A rule may also name
// managers (IsManager): they may always use their group, and the server lets
// them change its users and its group-only worker keys.
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
	"log/slog"
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
	users    map[string]bool
	managers map[string]bool
	admins   bool
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
	logger   *slog.Logger             // Reload's WARNs; nil = slog.Default(); guarded by reloadMu
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
	return validateNames("users", users)
}

// ValidateManagers checks a managers list with the rules of ValidateUsers
// (errors name "managers") and returns it without duplicates, in the order
// given (never nil). A username in the reserved service realm
// (model.IsServiceUsername) is refused, naming the entry: a manager can issue
// worker keys for the group, and a worker with such a key receives every
// task sent to it with the submitters' staging credentials. (A service
// identity may be in users.) Whether the group may have managers at all is
// CheckManagersGroup's question.
func ValidateManagers(managers []string) ([]string, error) {
	out, err := validateNames("managers", managers)
	if err != nil {
		return nil, err
	}
	for _, m := range out {
		if model.IsServiceUsername(m) {
			return nil, fmt.Errorf("managers holds %q: a service identity (@%s) cannot be a worker group manager", m, model.ServiceRealm)
		}
	}
	return out, nil
}

// splitServiceManagers returns the stored managers outside the service realm
// and, separately, those in it (which load ignores).
func splitServiceManagers(managers []string) (kept, service []string) {
	kept = make([]string, 0, len(managers))
	for _, m := range managers {
		if model.IsServiceUsername(m) {
			service = append(service, m)
			continue
		}
		kept = append(kept, m)
	}
	return kept, service
}

// validateNames is ValidateUsers for the list called field.
func validateNames(field string, users []string) ([]string, error) {
	if len(users) > MaxUsers {
		return nil, fmt.Errorf("%s holds %d entries; at most %d are allowed", field, len(users), MaxUsers)
	}
	out := make([]string, 0, len(users))
	seen := make(map[string]bool, len(users))
	for _, u := range users {
		switch {
		case u == "":
			return nil, fmt.Errorf("%s holds an empty username", field)
		case len(u) > MaxUsernameBytes:
			return nil, fmt.Errorf("%s holds a username of %d bytes; at most %d are allowed", field, len(u), MaxUsernameBytes)
		case strings.TrimSpace(u) != u:
			return nil, fmt.Errorf("username %q has leading or trailing whitespace", u)
		case strings.IndexFunc(u, unicode.IsControl) >= 0:
			return nil, fmt.Errorf("username %q contains a control character", u)
		case u == anonymousUser:
			return nil, fmt.Errorf("%s holds %q: anonymous submitters can never be granted a restricted group", field, anonymousUser)
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

// CheckManagersGroup refuses managers on the default group ("default" or
// ""): its workers receive every untargeted task of every user, so whoever
// could add a worker to it would see all of them. Any other group, and the
// default group without managers, is fine.
func CheckManagersGroup(group string, managers []string) error {
	if len(managers) > 0 && normalizeGroup(group) == DefaultGroup {
		return fmt.Errorf("the %q group cannot have managers: its workers receive every untargeted task of every user", DefaultGroup)
	}
	return nil
}

// SetLogger sets the logger Reload reports to (nil: slog.Default()), for p
// and every copy WithAdmins made of it.
func (p *Policy) SetLogger(l *slog.Logger) {
	if p == nil || p.state == nil {
		return
	}
	p.state.reloadMu.Lock()
	defer p.state.reloadMu.Unlock()
	p.state.logger = l
}

// Reload reads the rules from src and makes them the rules of p and of every
// copy WithAdmins made of it, in one swap. On error nothing changes and the
// error is returned. A stored rule that fails ValidateGroupName,
// ValidateUsers or ValidateManagers (other than duplicates) or
// CheckManagersGroup, or a default_deny value other than "true"/"false", is
// an error: the policy fails closed instead of skipping it. The one
// exception: a stored manager in the reserved service realm (which
// ValidateManagers refuses on write) is ignored, the rule's other fields
// apply, and one WARN per Reload lists the ignored entries.
func (p *Policy) Reload(ctx context.Context, src Source) error {
	if p == nil || p.state == nil {
		return errors.New("worker group policy: Reload needs a policy made by groupacl.New")
	}
	p.state.reloadMu.Lock()
	defer p.state.reloadMu.Unlock()

	s, ignored, err := load(ctx, src)
	if err != nil {
		return err
	}
	if len(ignored) > 0 {
		logger := p.state.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("worker group policy: ignoring stored managers in the reserved service realm (a service identity cannot manage a worker group); the rules' other fields apply. Set the rule's managers again to remove them",
			"entries", ignored)
	}
	p.state.current.Store(s)
	return nil
}

// invalidStoredRuleError marks an error of load caused by a stored rule that
// fails validation; its message is the cause's.
type invalidStoredRuleError struct{ err error }

func (e *invalidStoredRuleError) Error() string { return e.err.Error() }
func (e *invalidStoredRuleError) Unwrap() error { return e.err }

// IsInvalidStoredRule reports whether err (of Reload) was caused by a stored
// rule that fails validation, which removing that rule repairs; false for a
// read error or an invalid default_deny value.
func IsInvalidStoredRule(err error) bool {
	var e *invalidStoredRuleError
	return errors.As(err, &e)
}

// load builds a snapshot from src. ignored lists the stored managers in the
// service realm that were left out ("<group>: <username>").
func load(ctx context.Context, src Source) (s *snapshot, ignored []string, err error) {
	s = &snapshot{}
	value, found, err := src.GetServerSetting(ctx, DefaultDenyKey)
	if err != nil {
		return nil, nil, fmt.Errorf("worker group policy: read %s: %w", DefaultDenyKey, err)
	}
	if found {
		if s.defaultDeny, err = ParseDefaultDeny(value); err != nil {
			return nil, nil, fmt.Errorf("worker group policy: %w", err)
		}
	}
	rows, err := src.ListWorkerGroupPolicies(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("worker group policy: list rules: %w", err)
	}
	s.groups = make(map[string]rule, len(rows))
	for _, gp := range rows {
		if err := ValidateGroupName(gp.Group); err != nil {
			return nil, nil, fmt.Errorf("worker group policy: stored rule: %w", &invalidStoredRuleError{err})
		}
		users, err := ValidateUsers(gp.Users)
		if err != nil {
			return nil, nil, fmt.Errorf("worker group policy: stored rule for group %q: %w", gp.Group, &invalidStoredRuleError{err})
		}
		// A service identity never manages a group: such a stored entry
		// (written before the realm was refused, or by SQL) is ignored, not
		// a start-up failure.
		storedManagers, serviceManagers := splitServiceManagers(gp.Managers)
		for _, m := range serviceManagers {
			ignored = append(ignored, gp.Group+": "+m)
		}
		managers, err := ValidateManagers(storedManagers)
		if err != nil {
			return nil, nil, fmt.Errorf("worker group policy: stored rule for group %q: %w", gp.Group, &invalidStoredRuleError{err})
		}
		if err := CheckManagersGroup(gp.Group, managers); err != nil {
			return nil, nil, fmt.Errorf("worker group policy: stored rule: %w", &invalidStoredRuleError{err})
		}
		r := rule{users: make(map[string]bool, len(users)), managers: make(map[string]bool, len(managers)), admins: gp.Admins}
		for _, u := range users {
			r.users[u] = true
		}
		for _, m := range managers {
			r.managers[m] = true
		}
		s.groups[gp.Group] = r
	}
	return s, ignored, nil
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
// list, never an admin. A manager of a group may always use it.
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
	if r.users[username] || r.managers[username] {
		return true
	}
	return r.admins && p.userIsAdmin(username)
}

// IsManager reports whether username manages group ("" and "default" are the
// default group, which never has managers). "", "anonymous" and a username
// in the service realm never manage a group.
func (p *Policy) IsManager(username, group string) bool {
	if username == "" || username == anonymousUser || model.IsServiceUsername(username) {
		return false
	}
	s := p.rules()
	if s == nil {
		return false
	}
	r, listed := s.groups[normalizeGroup(group)]
	return listed && r.managers[username]
}

// ManagedGroups returns the groups username manages, sorted (nil when none;
// always nil for a username in the service realm).
func (p *Policy) ManagedGroups(username string) []string {
	if username == "" || username == anonymousUser || model.IsServiceUsername(username) {
		return nil
	}
	s := p.rules()
	if s == nil {
		return nil
	}
	var names []string
	for name, r := range s.groups {
		if r.managers[username] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
