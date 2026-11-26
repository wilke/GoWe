// Package groupacl decides which submitters may send tasks to a worker group
// (#261). Worker keys decide which workers may join a group; this policy, the
// "worker_groups" block of the server --config file, decides which users may
// target one with gowe:Execution.worker_group or the worker_group label.
//
// The scheduler enforces it at dispatch (authoritative) and when a FAILED
// task is re-queued, as do the UI resume/recompute handlers; the submission
// handlers use it to refuse early with a readable error.
package groupacl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/me/gowe/pkg/model"
)

// DefaultGroup is the key of the default worker group. An empty group name
// means the same group.
const DefaultGroup = "default"

// maxConfigFileSize bounds how much of the --config file is read (same bound
// as internal/dbconfig).
const maxConfigFileSize = 1 << 20

// knownTopLevelKeys are the top-level keys of the server --config file:
// admins (internal/server/admin_config.go), database (internal/dbconfig) and
// worker_groups (this package). Any other key is a start-up error, so a
// misspelt block cannot silently leave the policy unset.
var knownTopLevelKeys = map[string]bool{"admins": true, "database": true, "worker_groups": true}

// groupNameRe is the worker group name rule, mirrored from
// internal/blessing/blessing.go (groupNameRe): letters, digits, '.', '_' or
// '-', not starting with a punctuation character (so no whitespace).
var groupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// anonymousUser is the username unauthenticated REST submissions are stored
// with (model.AnonymousUser). The policy treats it exactly like "".
var anonymousUser = model.AnonymousUser.Username

// groupRule is one entry of worker_groups.groups in the config file.
type groupRule struct {
	Users  []string `json:"users"`
	Admins bool     `json:"admins"`
}

// fileConfig is the "worker_groups" block of the config file.
type fileConfig struct {
	DefaultDeny bool                 `json:"default_deny"`
	Groups      map[string]groupRule `json:"groups"`
}

type rule struct {
	users  map[string]bool
	admins bool
}

// IsAdminFunc reports whether a username is a server admin.
type IsAdminFunc func(username string) bool

// Policy decides whether a submitter may send tasks to a worker group.
// The zero value / nil *Policy allows everything.
type Policy struct {
	defaultDeny bool
	groups      map[string]rule // key: group name, "" folded into DefaultGroup
	isAdmin     IsAdminFunc
}

// normalizeGroup folds the two spellings of the default group into one key.
func normalizeGroup(group string) string {
	if group == "" {
		return DefaultGroup
	}
	return group
}

// Load reads the worker_groups block of the server config file at path
// ("" = no policy). The block is read strictly: an unreadable file, invalid
// JSON, an unknown top-level key, an unknown field inside the block, a
// wrongly typed value, an invalid group name or "anonymous" in a users list
// is an error. The other known top-level keys are left to their own readers.
// A missing or null block yields a policy that allows everything.
func Load(path string) (*Policy, error) {
	if path == "" {
		return nil, nil
	}
	// O_NONBLOCK so a FIFO at this path cannot block start-up (as in
	// internal/dbconfig).
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("config file %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	if len(data) > maxConfigFileSize {
		return nil, fmt.Errorf("config file %s is larger than %d bytes", path, maxConfigFileSize)
	}
	return parse(path, data)
}

// parse decodes the worker_groups block of the config file contents data.
// path is used in messages only.
func parse(path string, data []byte) (*Policy, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, jsonError(path, false, err)
	}
	var unknown []string
	for key := range top {
		if !knownTopLevelKeys[key] {
			unknown = append(unknown, fmt.Sprintf("%q", key))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("config file %s: unknown top-level key %s (allowed: admins, database, worker_groups)", path, strings.Join(unknown, ", "))
	}
	block := top["worker_groups"]
	if len(block) == 0 || string(block) == "null" {
		return nil, nil
	}

	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(block))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return nil, jsonError(path, true, err)
	}

	p := &Policy{defaultDeny: fc.DefaultDeny, groups: make(map[string]rule, len(fc.Groups))}
	for name, gr := range fc.Groups {
		if name == "" {
			return nil, fmt.Errorf("config file %s: worker_groups.groups has an empty group name (the default group is \"default\")", path)
		}
		if !groupNameRe.MatchString(name) {
			return nil, fmt.Errorf("config file %s: worker_groups.groups: %q is not a valid group name (letters, digits, '.', '_' or '-', starting with a letter or digit)", path, name)
		}
		r := rule{users: make(map[string]bool, len(gr.Users)), admins: gr.Admins}
		for _, u := range gr.Users {
			if u == "" {
				return nil, fmt.Errorf("config file %s: worker_groups.groups.%s.users holds an empty username", path, name)
			}
			if u == anonymousUser {
				return nil, fmt.Errorf("config file %s: worker_groups.groups.%s.users holds %q: anonymous submitters can never be granted a restricted group", path, name, anonymousUser)
			}
			r.users[u] = true
		}
		p.groups[normalizeGroup(name)] = r
	}
	return p, nil
}

// jsonError turns a decoding error of the config file into a message naming
// the position or field, in the style of internal/dbconfig. inBlock is false
// while the top level is decoded, true for the worker_groups block.
func jsonError(path string, inBlock bool, err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Errorf("config file %s is not valid JSON (error at byte %d)", path, syn.Offset)
	case errors.As(err, &typ):
		switch {
		case !inBlock:
			return fmt.Errorf("config file %s: the top level must be a JSON object", path)
		case typ.Field == "":
			return fmt.Errorf("config file %s: worker_groups must be a JSON object", path)
		}
		return fmt.Errorf("config file %s: worker_groups.%s must be a %s", path, typ.Field, typ.Type)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return fmt.Errorf("config file %s is not valid JSON (unexpected end of input)", path)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return fmt.Errorf("config file %s: unknown field %s in worker_groups (allowed: default_deny, groups; per group: users, admins)", path, name)
	default:
		return fmt.Errorf("config file %s: invalid worker_groups block: %v", path, err)
	}
}

// WithAdmins returns the policy with the admin lookup set (may be nil =
// nobody is admin). A nil policy stays nil (everything allowed).
func (p *Policy) WithAdmins(f IsAdminFunc) *Policy {
	if p == nil {
		return nil
	}
	cp := *p
	cp.isAdmin = f
	return &cp
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
	if p == nil {
		return true
	}
	if username == anonymousUser {
		username = ""
	}
	key := normalizeGroup(group)
	r, listed := p.groups[key]
	if !listed {
		if !p.defaultDeny || key == DefaultGroup {
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

// Restricted reports whether any restriction is configured (for a start-up
// log line).
func (p *Policy) Restricted() bool {
	return p != nil && (p.defaultDeny || len(p.groups) > 0)
}

// DefaultDeny reports whether unlisted groups other than the default group
// are denied to everyone but server admins.
func (p *Policy) DefaultDeny() bool {
	return p != nil && p.defaultDeny
}

// Groups returns the names of the listed (restricted) groups, sorted. It
// never exposes the user lists.
func (p *Policy) Groups() []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.groups))
	for name := range p.groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Check returns nil when username may use every group in groups, else an
// error naming the first denied group: `user "X" may not use worker group "Y"`.
func (p *Policy) Check(username string, groups []string) error {
	if p == nil {
		return nil
	}
	for _, g := range groups {
		if !p.Allowed(username, g) {
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
