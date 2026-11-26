package groupacl

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, content string) *Policy {
	t.Helper()
	p, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
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

const testConfig = `{
  "admins": ["root@bvbrc"],
  "database": {"url": "postgres://db.example/gowe"},
  "worker_groups": {
    "groups": {
      "model-router": {"users": ["awilke@bvbrc", "svc-router@bvbrc"], "admins": true},
      "gpu": {"users": ["alice"]},
      "nobody": {"users": []}
    }
  }
}`

func TestAllowed(t *testing.T) {
	base := mustLoad(t, testConfig).WithAdmins(adminsOf("root@bvbrc"))
	denyCfg := mustLoad(t, `{"worker_groups": {"default_deny": true, "groups": {"gpu": {"users": ["alice"]}}}}`).
		WithAdmins(adminsOf("root@bvbrc"))
	defaultListed := mustLoad(t, `{"worker_groups": {"groups": {"default": {"users": ["alice"]}}}}`)
	// An admin lookup that (wrongly) calls "anonymous" an admin: the policy
	// must still never treat the anonymous submitter as one (F4).
	anonAdmin := mustLoad(t, testConfig).WithAdmins(adminsOf("anonymous", ""))
	anonAdminDeny := mustLoad(t, `{"worker_groups": {"default_deny": true}}`).WithAdmins(adminsOf("anonymous", ""))

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
		{"no admin func: nobody is admin", mustLoad(t, testConfig), "root@bvbrc", "model-router", false},
		{"anonymous username: listed group denied", base, "anonymous", "gpu", false},
		{"anonymous username: unlisted group open", base, "anonymous", "esmfold", true},
		{"anonymous username: default group open", base, "anonymous", "", true},
		{"anonymous username never admin (admins true)", anonAdmin, "anonymous", "model-router", false},
		{"anonymous username never admin (default_deny)", anonAdminDeny, "anonymous", "esmfold", false},
		{"anonymous username: default_deny keeps default open", anonAdminDeny, "anonymous", "default", true},
		{"nil policy", nil, "mallory", "model-router", true},
		{"zero policy", &Policy{}, "mallory", "model-router", true},
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
	p := mustLoad(t, testConfig)
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

func TestLoad(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantErr        string // substring; "" = success
		wantRestricted bool
	}{
		{"missing block", `{"admins": ["a"]}`, "", false},
		{"null block", `{"worker_groups": null}`, "", false},
		{"empty block", `{"worker_groups": {}}`, "", false},
		{"default_deny only", `{"worker_groups": {"default_deny": true}}`, "", true},
		{"groups", testConfig, "", true},
		{"unknown field in block", `{"worker_groups": {"defaultDeny": true}}`, `unknown field "defaultDeny" in worker_groups`, false},
		{"unknown field in group", `{"worker_groups": {"groups": {"g": {"user": ["a"]}}}}`, `unknown field "user" in worker_groups`, false},
		{"wrong type default_deny", `{"worker_groups": {"default_deny": "yes"}}`, "worker_groups.default_deny must be a bool", false},
		{"wrong type users", `{"worker_groups": {"groups": {"g": {"users": "alice"}}}}`, "must be a []string", false},
		{"wrong type block", `{"worker_groups": []}`, "worker_groups must be a JSON object", false},
		{"top level not an object", `[1, 2]`, "the top level must be a JSON object", false},
		{"invalid JSON", `{"worker_groups": {`, "not valid JSON", false},
		{"empty group name", `{"worker_groups": {"groups": {"": {"users": ["a"]}}}}`, "empty group name", false},
		{"empty username", `{"worker_groups": {"groups": {"g": {"users": [""]}}}}`, "empty username", false},
		{"known top-level keys only", `{"admins": [], "database": {}, "worker_groups": {"groups": {"g": {"users": ["a"]}}}}`, "", true},
		{"unknown top-level key", `{"worker_group": {"groups": {"gpu": {"users": ["alice"]}}}}`, `unknown top-level key "worker_group" (allowed: admins, database, worker_groups)`, false},
		{"unknown top-level key next to the block", `{"worker_groups": {}, "workers": 1}`, `unknown top-level key "workers"`, false},
		{"top-level keys are case-sensitive", `{"Worker_Groups": {"default_deny": true}}`, `unknown top-level key "Worker_Groups"`, false},
		{"several unknown top-level keys, sorted", `{"zz": 1, "aa": 2}`, `unknown top-level key "aa", "zz"`, false},
		{"group name with leading space", `{"worker_groups": {"groups": {" gpu": {"users": ["a"]}}}}`, `" gpu" is not a valid group name`, false},
		{"group name with trailing space", `{"worker_groups": {"groups": {"gpu ": {"users": ["a"]}}}}`, `"gpu " is not a valid group name`, false},
		{"group name with inner space", `{"worker_groups": {"groups": {"gpu group": {"users": ["a"]}}}}`, "is not a valid group name", false},
		{"group name with slash", `{"worker_groups": {"groups": {"a/b": {"users": ["a"]}}}}`, "is not a valid group name", false},
		{"group name starting with a dot", `{"worker_groups": {"groups": {".gpu": {"users": ["a"]}}}}`, "is not a valid group name", false},
		{"valid group name characters", `{"worker_groups": {"groups": {"GPU_a100.v2-x": {"users": ["a"]}}}}`, "", true},
		{"anonymous in users", `{"worker_groups": {"groups": {"gpu": {"users": ["alice", "anonymous"]}}}}`, `worker_groups.groups.gpu.users holds "anonymous"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Load(writeConfig(t, tt.content))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := p.Restricted(); got != tt.wantRestricted {
				t.Errorf("Restricted() = %v, want %v", got, tt.wantRestricted)
			}
		})
	}
}

func TestLoad_NoPathAndUnreadable(t *testing.T) {
	p, err := Load("")
	if err != nil || p != nil {
		t.Fatalf("Load(\"\") = %v, %v; want nil, nil", p, err)
	}
	if !p.Allowed("anyone", "gpu") || p.Restricted() || p.Check("anyone", []string{"gpu"}) != nil {
		t.Error("nil policy must allow everything")
	}
	if p.WithAdmins(adminsOf("x")) != nil {
		t.Error("WithAdmins on nil must stay nil")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file must be an error")
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("a directory must be an error")
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
	p := mustLoad(t, testConfig)
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
