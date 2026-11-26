package groupacl

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// memSource is an in-memory Source.
type memSource struct {
	defaultDeny string // stored value; "" = no row
	rules       []*model.WorkerGroupPolicy
	listErr     error
	settingErr  error
}

func (m *memSource) ListWorkerGroupPolicies(context.Context) ([]*model.WorkerGroupPolicy, error) {
	return m.rules, m.listErr
}

func (m *memSource) GetServerSetting(_ context.Context, key string) (string, bool, error) {
	if m.settingErr != nil {
		return "", false, m.settingErr
	}
	if key != DefaultDenyKey || m.defaultDeny == "" {
		return "", false, nil
	}
	return m.defaultDeny, true, nil
}

func gp(group string, admins bool, users ...string) *model.WorkerGroupPolicy {
	return &model.WorkerGroupPolicy{Group: group, Users: users, Admins: admins}
}

// testRules: model-router for two users and admins, gpu for alice, nobody
// for nobody.
var testRules = []*model.WorkerGroupPolicy{
	gp("model-router", true, "awilke@bvbrc", "svc-router@bvbrc"),
	gp("gpu", false, "alice"),
	gp("nobody", false),
}

func mustPolicy(t *testing.T, src *memSource) *Policy {
	t.Helper()
	p := New()
	if err := p.Reload(context.Background(), src); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return p
}

func adminsOf(names ...string) IsAdminFunc {
	return func(u string) bool {
		for _, n := range names {
			if n == u {
				return true
			}
		}
		return false
	}
}

func TestAllowed(t *testing.T) {
	base := mustPolicy(t, &memSource{rules: testRules}).WithAdmins(adminsOf("root@bvbrc"))
	denyCfg := mustPolicy(t, &memSource{defaultDeny: "true", rules: []*model.WorkerGroupPolicy{gp("gpu", false, "alice")}}).
		WithAdmins(adminsOf("root@bvbrc"))
	defaultListed := mustPolicy(t, &memSource{rules: []*model.WorkerGroupPolicy{gp("default", false, "alice")}})
	// An admin lookup that (wrongly) calls "anonymous" an admin: the policy
	// must still never treat the anonymous submitter as one (F4).
	anonAdmin := mustPolicy(t, &memSource{rules: testRules}).WithAdmins(adminsOf("anonymous", ""))
	anonAdminDeny := mustPolicy(t, &memSource{defaultDeny: "true"}).WithAdmins(adminsOf("anonymous", ""))

	tests := []struct {
		name  string
		p     *Policy
		user  string
		group string
		want  bool
	}{
		{"listed user", base, "awilke@bvbrc", "model-router", true},
		{"second listed user", base, "svc-router@bvbrc", "model-router", true},
		{"unlisted user", base, "mallory@bvbrc", "model-router", false},
		{"case-sensitive username", base, "AWILKE@bvbrc", "model-router", false},
		{"admin with admins true", base, "root@bvbrc", "model-router", true},
		{"admin with admins false", base, "root@bvbrc", "gpu", false},
		{"empty users list, nobody", base, "awilke@bvbrc", "nobody", false},
		{"anonymous never listed", base, "", "gpu", false},
		{"unlisted group open", base, "mallory@bvbrc", "esmfold", true},
		{"default group open (empty)", base, "mallory@bvbrc", "", true},
		{"default group open (named)", base, "mallory@bvbrc", "default", true},
		{"default_deny: unlisted group denied", denyCfg, "alice", "esmfold", false},
		{"default_deny: admin may use unlisted group", denyCfg, "root@bvbrc", "esmfold", true},
		{"default_deny: anonymous denied", denyCfg, "", "esmfold", false},
		{"default_deny: default group still open", denyCfg, "mallory", "", true},
		{"default_deny: default spelled out still open", denyCfg, "mallory", "default", true},
		{"default_deny: listed user", denyCfg, "alice", "gpu", true},
		{"default listed: empty spelling restricted", defaultListed, "mallory", "", false},
		{"default listed: named spelling restricted", defaultListed, "mallory", "default", false},
		{"default listed: listed user (empty spelling)", defaultListed, "alice", "", true},
		{"no admin func: nobody is admin", mustPolicy(t, &memSource{rules: testRules}), "root@bvbrc", "model-router", false},
		{"anonymous username: listed group denied", base, "anonymous", "gpu", false},
		{"anonymous username: unlisted group open", base, "anonymous", "esmfold", true},
		{"anonymous username: default group open", base, "anonymous", "", true},
		{"anonymous username never admin (admins true)", anonAdmin, "anonymous", "model-router", false},
		{"anonymous username never admin (default_deny)", anonAdminDeny, "anonymous", "esmfold", false},
		{"anonymous username: default_deny keeps default open", anonAdminDeny, "anonymous", "default", true},
		{"nil policy", nil, "mallory", "model-router", true},
		{"zero policy", &Policy{}, "mallory", "model-router", true},
		{"new policy, never reloaded", New(), "mallory", "model-router", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Allowed(tt.user, tt.group); got != tt.want {
				t.Errorf("Allowed(%q, %q) = %v, want %v", tt.user, tt.group, got, tt.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	p := mustPolicy(t, &memSource{rules: testRules})
	tests := []struct {
		name    string
		p       *Policy
		user    string
		groups  []string
		wantErr string
	}{
		{"all allowed", p, "alice", []string{"gpu", "esmfold", ""}, ""},
		{"first denied named", p, "mallory", []string{"esmfold", "gpu", "model-router"}, `user "mallory" may not use worker group "gpu"`},
		{"no groups", p, "mallory", nil, ""},
		{"nil policy", nil, "mallory", []string{"gpu"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Check(tt.user, tt.groups)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("Check error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestReload(t *testing.T) {
	dbErr := errors.New("database is gone")
	tests := []struct {
		name            string
		src             *memSource
		wantErr         string // substring; "" = success
		wantRestricted  bool
		wantDefaultDeny bool
	}{
		{"nothing stored", &memSource{}, "", false, false},
		{"default_deny false", &memSource{defaultDeny: "false"}, "", false, false},
		{"default_deny only", &memSource{defaultDeny: "true"}, "", true, true},
		{"default_deny not a bool", &memSource{defaultDeny: "yes"}, `worker_groups.default_deny holds "yes"`, false, false},
		{"default_deny wrong case", &memSource{defaultDeny: "True"}, `holds "True"`, false, false},
		{"rules", &memSource{rules: testRules}, "", true, false},
		{"rules and default_deny", &memSource{defaultDeny: "true", rules: testRules}, "", true, true},
		{"stored duplicates are tolerated", &memSource{rules: []*model.WorkerGroupPolicy{gp("gpu", false, "alice", "alice")}}, "", true, false},
		{"valid group name characters", &memSource{rules: []*model.WorkerGroupPolicy{gp("GPU_a100.v2-x", false, "a")}}, "", true, false},
		{"empty group name", &memSource{rules: []*model.WorkerGroupPolicy{gp("", false, "a")}}, "group name is empty", false, false},
		{"group name with leading space", &memSource{rules: []*model.WorkerGroupPolicy{gp(" gpu", false, "a")}}, `" gpu" is not a valid group name`, false, false},
		{"group name with slash", &memSource{rules: []*model.WorkerGroupPolicy{gp("a/b", false, "a")}}, "is not a valid group name", false, false},
		{"group name starting with a dot", &memSource{rules: []*model.WorkerGroupPolicy{gp(".gpu", false, "a")}}, "is not a valid group name", false, false},
		{"empty username", &memSource{rules: []*model.WorkerGroupPolicy{gp("g", false, "")}}, `group "g": users holds an empty username`, false, false},
		{"anonymous in users", &memSource{rules: []*model.WorkerGroupPolicy{gp("gpu", false, "alice", "anonymous")}}, `users holds "anonymous"`, false, false},
		{"username with whitespace", &memSource{rules: []*model.WorkerGroupPolicy{gp("gpu", false, "alice ")}}, "leading or trailing whitespace", false, false},
		{"list error", &memSource{listErr: dbErr}, "list rules: database is gone", false, false},
		{"setting error", &memSource{settingErr: dbErr}, "read worker_groups.default_deny: database is gone", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New()
			err := p.Reload(context.Background(), tt.src)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Reload error = %v, want it to contain %q", err, tt.wantErr)
				}
				if p.Restricted() {
					t.Error("a failed first Reload must leave the policy without rules")
				}
				return
			}
			if err != nil {
				t.Fatalf("Reload: %v", err)
			}
			if got := p.Restricted(); got != tt.wantRestricted {
				t.Errorf("Restricted() = %v, want %v", got, tt.wantRestricted)
			}
			if got := p.DefaultDeny(); got != tt.wantDefaultDeny {
				t.Errorf("DefaultDeny() = %v, want %v", got, tt.wantDefaultDeny)
			}
		})
	}
}

// gpm is gp with managers.
func gpm(group string, managers []string, users ...string) *model.WorkerGroupPolicy {
	r := gp(group, false, users...)
	r.Managers = managers
	return r
}

// Managers: always allowed on their group, never on another; "" and
// anonymous never manage; stored managers are validated like users and
// refused on the default group.
func TestManagers(t *testing.T) {
	p := mustPolicy(t, &memSource{defaultDeny: "true", rules: []*model.WorkerGroupPolicy{
		gpm("gpu", []string{"mgr", "both"}, "alice"),
		gpm("cpu", []string{"both"}),
		gp("nobody", false),
	}}).WithAdmins(adminsOf("root"))

	for _, c := range []struct {
		user, group string
		manager     bool
		allowed     bool
	}{
		// mgr is not in the users of gpu, yet allowed as its manager; not
		// on other groups (esmfold: unlisted under default_deny).
		{"mgr", "gpu", true, true},
		{"mgr", "cpu", false, false},
		{"mgr", "nobody", false, false},
		{"mgr", "esmfold", false, false},
		{"both", "cpu", true, true},
		{"alice", "gpu", false, true},
		// The admins switch is off and an admin is not a manager.
		{"root", "gpu", false, false},
		{"", "gpu", false, false},
		{"anonymous", "gpu", false, false},
	} {
		if got := p.IsManager(c.user, c.group); got != c.manager {
			t.Errorf("IsManager(%q, %q) = %v, want %v", c.user, c.group, got, c.manager)
		}
		if got := p.Allowed(c.user, c.group); got != c.allowed {
			t.Errorf("Allowed(%q, %q) = %v, want %v", c.user, c.group, got, c.allowed)
		}
	}
	if got := p.ManagedGroups("both"); !reflect.DeepEqual(got, []string{"cpu", "gpu"}) {
		t.Errorf("ManagedGroups(both) = %v, want [cpu gpu]", got)
	}
	if got := p.ManagedGroups("alice"); got != nil {
		t.Errorf("ManagedGroups(alice) = %v, want nil", got)
	}
	if got := p.ManagedGroups(""); got != nil {
		t.Errorf("ManagedGroups(\"\") = %v, want nil", got)
	}
	var nilPolicy *Policy
	if nilPolicy.IsManager("mgr", "gpu") || nilPolicy.ManagedGroups("mgr") != nil {
		t.Error("a nil policy has no managers")
	}

	for _, c := range []struct {
		name    string
		rule    *model.WorkerGroupPolicy
		wantErr string
	}{
		{"managers on default", gpm("default", []string{"mgr"}), `"default" group cannot have managers`},
		{"anonymous manager", gpm("gpu", []string{"anonymous"}), `managers holds "anonymous"`},
		{"empty manager", gpm("gpu", []string{""}), "managers holds an empty username"},
		{"manager with whitespace", gpm("gpu", []string{" mgr"}), "leading or trailing whitespace"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := New().Reload(context.Background(), &memSource{rules: []*model.WorkerGroupPolicy{c.rule}})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Reload error = %v, want it to contain %q", err, c.wantErr)
			}
		})
	}
	if err := New().Reload(context.Background(), &memSource{rules: []*model.WorkerGroupPolicy{gpm("default", []string{}, "a")}}); err != nil {
		t.Errorf("default with an empty managers list: %v", err)
	}
}

func TestCheckManagersGroup(t *testing.T) {
	for _, c := range []struct {
		group    string
		managers []string
		wantErr  bool
	}{
		{"default", []string{"mgr"}, true},
		{"", []string{"mgr"}, true},
		{"default", nil, false},
		{"default", []string{}, false},
		{"gpu", []string{"mgr"}, false},
	} {
		if err := CheckManagersGroup(c.group, c.managers); (err != nil) != c.wantErr {
			t.Errorf("CheckManagersGroup(%q, %v) = %v, want error %v", c.group, c.managers, err, c.wantErr)
		}
	}
}

// A Reload replaces the whole rule set; a failed one keeps the old rules.
func TestReload_SwapAndFailure(t *testing.T) {
	ctx := context.Background()
	p := mustPolicy(t, &memSource{rules: testRules})
	if p.Allowed("mallory", "gpu") {
		t.Fatal("setup: gpu must be restricted")
	}

	// A failing source changes nothing.
	if err := p.Reload(ctx, &memSource{listErr: errors.New("boom")}); err == nil {
		t.Fatal("Reload with a failing source succeeded")
	}
	if err := p.Reload(ctx, &memSource{rules: []*model.WorkerGroupPolicy{gp("gpu", false, "anonymous")}}); err == nil {
		t.Fatal("Reload with an invalid stored rule succeeded")
	}
	if p.Allowed("mallory", "gpu") || !p.Allowed("alice", "gpu") {
		t.Error("a failed Reload must keep the previous rules")
	}
	if got, want := p.Groups(), []string{"gpu", "model-router", "nobody"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Groups() after failed Reload = %v, want %v", got, want)
	}

	// The new set replaces the old one entirely: gpu is no longer listed.
	if err := p.Reload(ctx, &memSource{defaultDeny: "true", rules: []*model.WorkerGroupPolicy{gp("esmfold", false, "bob")}}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got, want := p.Groups(), []string{"esmfold"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Groups() = %v, want %v", got, want)
	}
	if p.Allowed("alice", "gpu") {
		t.Error("gpu is unlisted under default_deny now: alice must be denied")
	}
	if !p.Allowed("bob", "esmfold") || !p.Allowed("mallory", "") {
		t.Error("new rules not applied")
	}

	// Back to nothing stored: everything open.
	if err := p.Reload(ctx, &memSource{}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if p.Restricted() || !p.Allowed("mallory", "esmfold") {
		t.Error("an empty store must open every group")
	}
}

// The copies WithAdmins returns share the rules: one Reload changes all.
func TestReload_SharedByWithAdminsCopies(t *testing.T) {
	ctx := context.Background()
	p := New()
	q := p.WithAdmins(adminsOf("root"))
	if err := p.Reload(ctx, &memSource{rules: []*model.WorkerGroupPolicy{gp("gpu", true, "alice")}}); err != nil {
		t.Fatal(err)
	}
	if !q.Restricted() || q.Allowed("mallory", "gpu") || !q.Allowed("root", "gpu") {
		t.Error("the WithAdmins copy must see the reloaded rules, with its admin lookup")
	}
	if p.Allowed("root", "gpu") {
		t.Error("the original has no admin lookup")
	}
	if err := q.Reload(ctx, &memSource{}); err != nil {
		t.Fatal(err)
	}
	if p.Restricted() {
		t.Error("a Reload through the copy must change the original too")
	}
}

func TestReload_NilAndZeroPolicy(t *testing.T) {
	var p *Policy
	if err := p.Reload(context.Background(), &memSource{}); err == nil {
		t.Error("Reload on a nil policy must fail")
	}
	if err := (&Policy{}).Reload(context.Background(), &memSource{}); err == nil {
		t.Error("Reload on a zero policy must fail")
	}
	if !p.Allowed("anyone", "gpu") || p.Restricted() || p.DefaultDeny() || p.Groups() != nil || p.Check("anyone", []string{"gpu"}) != nil {
		t.Error("nil policy must allow everything")
	}
	if p.WithAdmins(adminsOf("x")) != nil {
		t.Error("WithAdmins on nil must stay nil")
	}
}

// Readers run while the rules are swapped (meaningful under -race).
func TestReload_ConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	p := New().WithAdmins(adminsOf("root"))
	open := &memSource{}
	closed := &memSource{defaultDeny: "true", rules: testRules}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = p.Allowed("alice", "gpu")
				_ = p.Check("mallory", []string{"gpu", "esmfold"})
				_ = p.Groups()
				_ = p.Restricted()
			}
		}()
	}
	for i := 0; i < 200; i++ {
		src := open
		if i%2 == 0 {
			src = closed
		}
		if err := p.Reload(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestValidateGroupName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr string
	}{
		{"gpu", ""},
		{"default", ""},
		{"GPU_a100.v2-x", ""},
		{"0gpu", ""},
		{"", "group name is empty"},
		{" gpu", "is not a valid group name"},
		{"gpu ", "is not a valid group name"},
		{"gpu group", "is not a valid group name"},
		{"a/b", "is not a valid group name"},
		{".gpu", "is not a valid group name"},
		{"-gpu", "is not a valid group name"},
		{strings.Repeat("g", MaxGroupNameBytes), ""},
		{strings.Repeat("g", MaxGroupNameBytes+1), "65 bytes long; at most 64 are allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGroupName(tt.name)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateGroupName(%q) = %v", tt.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateGroupName(%q) = %v, want %q", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestValidateUsers(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{"nil", nil, []string{}, ""},
		{"empty", []string{}, []string{}, ""},
		{"kept in order", []string{"bob", "alice"}, []string{"bob", "alice"}, ""},
		{"duplicates removed", []string{"alice", "bob", "alice"}, []string{"alice", "bob"}, ""},
		{"case is significant", []string{"alice", "Alice"}, []string{"alice", "Alice"}, ""},
		{"empty name", []string{"alice", ""}, nil, "empty username"},
		{"anonymous", []string{"anonymous"}, nil, `users holds "anonymous"`},
		{"leading space", []string{" alice"}, nil, "leading or trailing whitespace"},
		{"trailing tab", []string{"alice\t"}, nil, "leading or trailing whitespace"},
		{"whitespace only", []string{" "}, nil, "leading or trailing whitespace"},
		{"newline inside", []string{"ali\nce"}, nil, "contains a control character"},
		{"NUL inside", []string{"ali\x00ce"}, nil, "contains a control character"},
		{"DEL inside", []string{"ali\x7fce"}, nil, "contains a control character"},
		{"C1 control inside", []string{"ali\u0085ce"}, nil, "contains a control character"},
		{"longest name", []string{strings.Repeat("u", MaxUsernameBytes)}, []string{strings.Repeat("u", MaxUsernameBytes)}, ""},
		{"name too long", []string{strings.Repeat("u", MaxUsernameBytes+1)}, nil, "username of 257 bytes; at most 256 are allowed"},
		{"name too long in bytes", []string{strings.Repeat("é", MaxUsernameBytes/2) + "x"}, nil, "at most 256 are allowed"},
		{"most users", manyUsers(MaxUsers), manyUsers(MaxUsers), ""},
		{"too many users", manyUsers(MaxUsers + 1), nil, "1001 entries; at most 1000 are allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateUsers(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ValidateUsers(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateUsers(%q): %v", tt.in, err)
			}
			if got == nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ValidateUsers(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// manyUsers returns n distinct usernames.
func manyUsers(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "user" + strconv.Itoa(i)
	}
	return out
}

func TestParseDefaultDeny(t *testing.T) {
	for in, want := range map[string]bool{"true": true, "false": false} {
		if got, err := ParseDefaultDeny(in); err != nil || got != want {
			t.Errorf("ParseDefaultDeny(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "1", "TRUE", " true"} {
		if _, err := ParseDefaultDeny(in); err == nil {
			t.Errorf("ParseDefaultDeny(%q) must fail", in)
		}
	}
}

func TestDeniedReason(t *testing.T) {
	if got, want := DeniedReason("mallory", "gpu"), `submitter "mallory" may not use worker group "gpu"`; got != want {
		t.Errorf("DeniedReason = %q, want %q", got, want)
	}
	if got, want := DeniedReason("mallory", ""), `submitter "mallory" may not use worker group "default"`; got != want {
		t.Errorf("DeniedReason(default) = %q, want %q", got, want)
	}
}

func TestGroups(t *testing.T) {
	p := mustPolicy(t, &memSource{rules: testRules})
	if got, want := p.Groups(), []string{"gpu", "model-router", "nobody"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Groups() = %v, want %v", got, want)
	}
}

func TestSubmissionGroups(t *testing.T) {
	steps := []model.Step{
		{ID: "a", Hints: &model.StepHints{WorkerGroup: "gpu"}},
		{ID: "b"},
		{ID: "c", Hints: &model.StepHints{WorkerGroup: "esmfold"}},
		{ID: "d", Hints: &model.StepHints{WorkerGroup: "gpu"}},
	}
	got := SubmissionGroups(map[string]string{"worker_group": "model-router"}, steps)
	if want := []string{"model-router", "gpu", "esmfold"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SubmissionGroups = %v, want %v", got, want)
	}
	if got := SubmissionGroups(nil, nil); len(got) != 0 {
		t.Errorf("SubmissionGroups(nil, nil) = %v, want empty", got)
	}
}
