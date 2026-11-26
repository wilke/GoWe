package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Worker-group policy admin API (#261): stored in the database, reloaded
// into the live policy after every change.

const workerGroupsPath = "/api/v1/admin/worker-groups"

// newWorkerGroupServer returns a server whose only admin is "admin", with a
// live (empty) policy, its store and the policy handle.
func newWorkerGroupServer(t *testing.T) (*Server, store.Store, *groupacl.Policy) {
	t.Helper()
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	policy := groupacl.New().WithAdmins(adminCfg.IsAdmin)
	srv, st := testServerWithStore(WithAdminConfig(adminCfg), WithGroupPolicy(policy))
	return srv, st, policy
}

// captureLogs sends the handlers' log lines to a buffer.
func captureLogs(srv *Server) *bytes.Buffer {
	var buf bytes.Buffer
	srv.logger = slog.New(slog.NewTextHandler(&buf, nil))
	return &buf
}

// wgDo sends a request with an optional JSON body as the user of token.
func wgDo(t *testing.T, srv *Server, method, path, body, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

type workerGroupsBody struct {
	DefaultDeny bool                      `json:"default_deny"`
	Groups      []model.WorkerGroupPolicy `json:"groups"`
}

func TestAdminWorkerGroups_Lifecycle(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)
	logs := captureLogs(srv)

	// Nothing stored yet.
	w, env := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(string(env.Data), `"groups":[]`) {
		t.Errorf("empty list must be [], got %s", env.Data)
	}
	list := decode[workerGroupsBody](t, env)
	if list.DefaultDeny || len(list.Groups) != 0 {
		t.Fatalf("empty policy = %+v", list)
	}

	// Create: duplicates are removed, the acting admin is recorded.
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["alice","bob","alice"],"admins":true}`, adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT gpu: %d %s", w.Code, w.Body.String())
	}
	gpu := decode[model.WorkerGroupPolicy](t, env)
	if gpu.Group != "gpu" || !reflect.DeepEqual(gpu.Users, []string{"alice", "bob"}) || !gpu.Admins || gpu.UpdatedBy != "admin" || gpu.CreatedAt.IsZero() {
		t.Fatalf("PUT gpu response = %+v", gpu)
	}
	created := gpu.CreatedAt
	if !policy.Allowed("alice", "gpu") || policy.Allowed("mallory", "gpu") || !policy.Allowed("admin", "gpu") {
		t.Error("the new rule must be in force at once (alice and admins only)")
	}

	// Replace: everything but created_at changes.
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["carol"]}`, adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT gpu again: %d %s", w.Code, w.Body.String())
	}
	gpu = decode[model.WorkerGroupPolicy](t, env)
	if !reflect.DeepEqual(gpu.Users, []string{"carol"}) || gpu.Admins || !gpu.CreatedAt.Equal(created) || gpu.UpdatedAt.Before(created) {
		t.Errorf("replaced rule = %+v (created_at must stay %v)", gpu, created)
	}
	if policy.Allowed("alice", "gpu") || !policy.Allowed("carol", "gpu") || policy.Allowed("admin", "gpu") {
		t.Error("the replaced rule must be in force at once")
	}

	// The default group is written "default" and covers the empty spelling.
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/default", `{"users":["carol"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT default: %d %s", w.Code, w.Body.String())
	}
	if policy.Allowed("mallory", "") || !policy.Allowed("carol", "") {
		t.Error("a rule for default must restrict the default group")
	}

	w, env = wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	list = decode[workerGroupsBody](t, env)
	if len(list.Groups) != 2 || list.Groups[0].Group != "default" || list.Groups[1].Group != "gpu" {
		t.Errorf("GET groups = %+v, want default, gpu", list.Groups)
	}

	// default_deny.
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"-default-deny", `{"default_deny":true}`, adminAuthToken)
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"default_deny":true`) {
		t.Fatalf("PUT default-deny: %d %s", w.Code, w.Body.String())
	}
	if !policy.DefaultDeny() || policy.Allowed("mallory", "esmfold") || !policy.Allowed("admin", "esmfold") {
		t.Error("default_deny must be in force at once")
	}
	if _, env = wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken); !decode[workerGroupsBody](t, env).DefaultDeny {
		t.Error("GET must report default_deny true")
	}

	// Delete: gpu becomes unlisted, so admin-only under default_deny.
	w, env = wgDo(t, srv, "DELETE", workerGroupsPath+"/gpu", "", adminAuthToken)
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"deleted":true`) {
		t.Fatalf("DELETE gpu: %d %s", w.Code, w.Body.String())
	}
	if policy.Allowed("carol", "gpu") || !policy.Allowed("admin", "gpu") {
		t.Error("a deleted rule under default_deny leaves the group to admins")
	}
	if w, _ := wgDo(t, srv, "DELETE", workerGroupsPath+"/gpu", "", adminAuthToken); w.Code != http.StatusNotFound {
		t.Errorf("DELETE missing rule: %d, want 404", w.Code)
	}

	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"-default-deny", `{"default_deny":false}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT default-deny false: %d", w.Code)
	}
	if !policy.Allowed("mallory", "gpu") || policy.DefaultDeny() {
		t.Error("default_deny false must open unlisted groups at once")
	}

	// The store holds what the API reported.
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 1 || rules[0].Group != "default" {
		t.Errorf("stored rules = %+v, %v; want only default", rules, err)
	}
	if v, found, err := st.GetServerSetting(ctx, groupacl.DefaultDenyKey); err != nil || !found || v != "false" {
		t.Errorf("stored default_deny = %q, %v, %v", v, found, err)
	}

	// One INFO line per change, with admin, group and user count, never names.
	out := logs.String()
	if n := strings.Count(out, "worker group policy set"); n != 3 {
		t.Errorf("%d 'set' lines, want 3:\n%s", n, out)
	}
	for _, want := range []string{"admin=admin", "group=gpu", "users=2", "users=1", "worker group policy deleted", "default_deny=true", "default_deny=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if strings.Contains(out, name) {
			t.Errorf("the log must not name users (%s):\n%s", name, out)
		}
	}
}

// Invalid requests are refused with 400 before anything is written.
func TestAdminWorkerGroups_Validation(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		wantMsg                  string
	}{
		{"group starting with a dot", "PUT", workerGroupsPath + "/.gpu", `{"users":["a"]}`, "is not a valid group name"},
		{"group with leading space", "PUT", workerGroupsPath + "/%20gpu", `{"users":["a"]}`, `" gpu" is not a valid group name`},
		{"anonymous in users", "PUT", workerGroupsPath + "/gpu", `{"users":["alice","anonymous"]}`, `users holds "anonymous"`},
		{"empty username", "PUT", workerGroupsPath + "/gpu", `{"users":[""]}`, "empty username"},
		{"username with whitespace", "PUT", workerGroupsPath + "/gpu", `{"users":[" alice"]}`, "leading or trailing whitespace"},
		{"unknown field", "PUT", workerGroupsPath + "/gpu", `{"user":["alice"]}`, `unknown field "user"`},
		{"users not a list", "PUT", workerGroupsPath + "/gpu", `{"users":"alice"}`, "invalid JSON body"},
		{"invalid JSON", "PUT", workerGroupsPath + "/gpu", `{`, "invalid JSON body"},
		{"default_deny missing", "PUT", workerGroupsPath + "-default-deny", `{}`, "default_deny is required"},
		{"default_deny not a bool", "PUT", workerGroupsPath + "-default-deny", `{"default_deny":"yes"}`, "invalid JSON body"},
		{"default_deny misspelt", "PUT", workerGroupsPath + "-default-deny", `{"defaultDeny":true}`, `unknown field "defaultDeny"`},
		{"trailing garbage", "PUT", workerGroupsPath + "/gpu", `{"users":["alice"]} x`, "trailing data after the JSON value"},
		{"second JSON value", "PUT", workerGroupsPath + "/gpu", `{"users":["alice"]}{"users":[]}`, "trailing data after the JSON value"},
		{"default_deny trailing value", "PUT", workerGroupsPath + "-default-deny", `{"default_deny":false} true`, "trailing data after the JSON value"},
		{"body too large", "PUT", workerGroupsPath + "/gpu", `{"users":["` + strings.Repeat("a", 1<<20) + `"]}`, "request body too large"},
		{"username with a control character", "PUT", workerGroupsPath + "/gpu", `{"users":["ali\nce"]}`, "contains a control character"},
		{"group name too long", "PUT", workerGroupsPath + "/" + strings.Repeat("g", groupacl.MaxGroupNameBytes+1), `{"users":["a"]}`, "at most 64 are allowed"},
		{"managers null", "PUT", workerGroupsPath + "/gpu", `{"users":["a"],"managers":null}`, "use [] to clear, omit to keep"},
		{"managers not a list", "PUT", workerGroupsPath + "/gpu", `{"users":["a"],"managers":"mgr"}`, "invalid JSON body: managers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st, policy := newWorkerGroupServer(t)
			w, env := wgDo(t, srv, tt.method, tt.path, tt.body, adminAuthToken)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if env.Error == nil || env.Error.Code != model.ErrValidation || !strings.Contains(env.Error.Message, tt.wantMsg) {
				t.Errorf("error = %+v, want %s containing %q", env.Error, model.ErrValidation, tt.wantMsg)
			}
			if rules, _ := st.ListWorkerGroupPolicies(context.Background()); len(rules) != 0 {
				t.Errorf("a refused request stored %+v", rules)
			}
			if _, found, _ := st.GetServerSetting(context.Background(), groupacl.DefaultDenyKey); found {
				t.Error("a refused request stored default_deny")
			}
			if policy.Restricted() {
				t.Error("a refused request changed the live policy")
			}
		})
	}
}

// Only server admins reach the endpoints.
func TestAdminWorkerGroups_AdminOnly(t *testing.T) {
	srv, st, policy := newWorkerGroupServer(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", workerGroupsPath, ""},
		{"PUT", workerGroupsPath + "/gpu", `{"users":["tester"]}`},
		{"DELETE", workerGroupsPath + "/gpu", ""},
		{"PUT", workerGroupsPath + "-default-deny", `{"default_deny":true}`},
	} {
		if w, _ := wgDo(t, srv, c.method, c.path, c.body, userAuthToken); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a normal user: %d, want 403", c.method, c.path, w.Code)
		}
	}
	if rules, _ := st.ListWorkerGroupPolicies(context.Background()); len(rules) != 0 || policy.Restricted() {
		t.Errorf("a normal user changed the policy: %+v", rules)
	}
}

// Without a live policy handle the write endpoints refuse with 503 and
// write nothing; reading still works.
func TestAdminWorkerGroups_NoPolicyHandle(t *testing.T) {
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	srv, st := testServerWithStore(WithAdminConfig(adminCfg))
	for _, c := range []struct{ method, path, body string }{
		{"PUT", workerGroupsPath + "/gpu", `{"users":["alice"]}`},
		{"DELETE", workerGroupsPath + "/gpu", ""},
		{"PUT", workerGroupsPath + "-default-deny", `{"default_deny":true}`},
	} {
		w, env := wgDo(t, srv, c.method, c.path, c.body, adminAuthToken)
		if w.Code != http.StatusServiceUnavailable || env.Error == nil || env.Error.Code != model.ErrUnavailable ||
			env.Error.Message != "worker group policy is not enabled on this server" {
			t.Errorf("%s %s: %d %+v, want 503 %s", c.method, c.path, w.Code, env.Error, model.ErrUnavailable)
		}
	}
	if rules, _ := st.ListWorkerGroupPolicies(context.Background()); len(rules) != 0 {
		t.Errorf("stored without a policy handle: %+v", rules)
	}
	if w, _ := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken); w.Code != http.StatusOK {
		t.Errorf("GET: %d, want 200", w.Code)
	}
}

// A stored row the policy cannot load makes the reload fail: 500, ERROR
// logged, the previous rules stay in force.
func TestAdminWorkerGroups_ReloadFailureKeepsOldPolicy(t *testing.T) {
	srv, st, policy := newWorkerGroupServer(t)
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["alice"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT gpu: %d %s", w.Code, w.Body.String())
	}
	// A row the API would refuse, written behind its back (as a direct SQL
	// edit could).
	if err := st.UpsertWorkerGroupPolicy(context.Background(), &model.WorkerGroupPolicy{Group: "bad group", Users: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(srv)

	w, env := wgDo(t, srv, "PUT", workerGroupsPath+"/esmfold", `{"users":["bob"]}`, adminAuthToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "could not be reloaded") {
		t.Errorf("error = %+v", env.Error)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "not reloaded") {
		t.Errorf("want an ERROR line, got:\n%s", out)
	}
	if strings.Contains(out, "worker group policy set") {
		t.Errorf("a failed change must not log the INFO line:\n%s", out)
	}
	if got := policy.Groups(); !reflect.DeepEqual(got, []string{"gpu"}) {
		t.Errorf("live groups = %v, want the previous [gpu]", got)
	}
	if !policy.Allowed("mallory", "esmfold") || policy.Allowed("mallory", "gpu") {
		t.Error("the previous rules must stay in force")
	}
}

// readBackFailStore fails ListWorkerGroupPolicies, while armed, once passes
// calls have succeeded.
type readBackFailStore struct {
	store.Store
	armed  bool
	passes int
}

func (s *readBackFailStore) ListWorkerGroupPolicies(ctx context.Context) ([]*model.WorkerGroupPolicy, error) {
	if s.armed {
		if s.passes == 0 {
			return nil, errors.New("read-back broken")
		}
		s.passes--
	}
	return s.Store.ListWorkerGroupPolicies(ctx)
}

// When the rule cannot be read back after the PUT stored and loaded it, a
// PUT that kept the managers answers 500 saying the rule is stored (it
// cannot claim managers it does not know); a PUT that gave them answers
// with the rule as sent.
func TestAdminWorkerGroups_PutReadBackFailure(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["alice"],"managers":["mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT g: %d %s", w.Code, w.Body.String())
	}
	fs := &readBackFailStore{Store: st}
	srv.store = fs

	// Managers kept: List is called by the reload (passes), then by the
	// read-back (fails).
	fs.armed, fs.passes = true, 1
	w, env := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["bob"]}`, adminAuthToken)
	fs.armed = false
	if w.Code != http.StatusInternalServerError || env.Error == nil || !strings.Contains(env.Error.Message, "was stored") {
		t.Fatalf("PUT keeping managers with a failed read-back: %d %+v, want 500 saying the rule was stored", w.Code, env.Error)
	}
	if strings.Contains(w.Body.String(), `"managers":[]`) {
		t.Errorf("the answer must not claim no managers: %s", w.Body.String())
	}
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 1 || !reflect.DeepEqual(rules[0].Users, []string{"bob"}) || !reflect.DeepEqual(rules[0].Managers, []string{"mgr"}) {
		t.Errorf("stored = %+v, %v; want users [bob], managers kept [mgr]", rules, err)
	}
	if !policy.Allowed("bob", "g") || !policy.IsManager("mgr", "g") {
		t.Error("the stored rule must be in force")
	}

	// Managers given: List is called for the old managers and by the reload
	// (both pass), then by the read-back (fails).
	fs.armed, fs.passes = true, 2
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["carol"],"managers":["mgr2"]}`, adminAuthToken)
	fs.armed = false
	if w.Code != http.StatusOK {
		t.Fatalf("PUT with managers and a failed read-back: %d %s, want 200", w.Code, w.Body.String())
	}
	if rule := decode[model.WorkerGroupPolicy](t, env); !reflect.DeepEqual(rule.Users, []string{"carol"}) || !reflect.DeepEqual(rule.Managers, []string{"mgr2"}) {
		t.Errorf("answer = %+v, want the rule as sent", rule)
	}
}

// callHandler calls h directly as the admin "admin", with the route
// parameter group ("" = none), optionally on an already cancelled context
// (going through ServeHTTP would stop at the auth middleware, which reads
// the store with the request context).
func callHandler(t *testing.T, h http.HandlerFunc, method, path, group, body string, cancelled bool) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	if group != "" {
		rctx.URLParams.Add("group", group)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, ctxKeyUserAuth, &UserContext{User: &model.User{Username: "admin"}})
	if cancelled {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	}
	w := httptest.NewRecorder()
	h(w, req.WithContext(ctx))
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

// A client that is gone (its request context cancelled) once the handler
// runs still gets its change stored and enforced.
func TestAdminWorkerGroups_CancelledRequestStillEnforced(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)

	w, _ := callHandler(t, srv.handlePutWorkerGroup, "PUT", workerGroupsPath+"/gpu", "gpu", `{"users":["alice"]}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT gpu: %d %s", w.Code, w.Body.String())
	}
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 1 || rules[0].Group != "gpu" || !reflect.DeepEqual(rules[0].Users, []string{"alice"}) {
		t.Fatalf("stored rules = %+v, %v; want gpu: alice", rules, err)
	}
	if !policy.Allowed("alice", "gpu") || policy.Allowed("mallory", "gpu") {
		t.Error("the stored rule must be enforced")
	}

	w, _ = callHandler(t, srv.handleSetWorkerGroupsDefaultDeny, "PUT", workerGroupsPath+"-default-deny", "", `{"default_deny":true}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT default-deny: %d %s", w.Code, w.Body.String())
	}
	if v, found, err := st.GetServerSetting(ctx, groupacl.DefaultDenyKey); err != nil || !found || v != "true" {
		t.Errorf("stored default_deny = %q, %v, %v; want true", v, found, err)
	}
	if !policy.DefaultDeny() {
		t.Error("the stored default_deny must be enforced")
	}

	w, _ = callHandler(t, srv.handleDeleteWorkerGroup, "DELETE", workerGroupsPath+"/gpu", "gpu", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE gpu: %d %s", w.Code, w.Body.String())
	}
	if rules, err := st.ListWorkerGroupPolicies(ctx); err != nil || len(rules) != 0 {
		t.Errorf("stored rules = %+v, %v; want none", rules, err)
	}
	if policy.Allowed("alice", "gpu") || !policy.Allowed("admin", "gpu") {
		t.Error("the deletion must be enforced (gpu admin-only under default_deny)")
	}
}

// Whitespace after the JSON value is not trailing data.
func TestAdminWorkerGroups_TrailingWhitespaceAccepted(t *testing.T) {
	srv, _, policy := newWorkerGroupServer(t)
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", "{\"users\":[\"alice\"]}\n \t\r\n", adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if !policy.Allowed("alice", "gpu") || policy.Allowed("mallory", "gpu") {
		t.Error("the rule must be in force")
	}
}

// GET reloads the live policy first: a row written behind the API's back is
// in force once listed, and a row the policy cannot load makes GET answer
// 500.
func TestAdminWorkerGroups_ListReloads(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if policy.Restricted() {
		t.Fatal("the rule must not be in force before a reload")
	}
	if w, _ := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	if !policy.Allowed("alice", "gpu") || policy.Allowed("mallory", "gpu") {
		t.Error("GET must load the stored rule into the live policy")
	}

	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "bad group", Users: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(srv)
	w, env := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET with an unloadable row: %d, want 500: %s", w.Code, w.Body.String())
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "could not be reloaded") {
		t.Errorf("error = %+v", env.Error)
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") {
		t.Errorf("want an ERROR line, got:\n%s", out)
	}
	if got := policy.Groups(); !reflect.DeepEqual(got, []string{"gpu"}) {
		t.Errorf("live groups = %v, want the previous [gpu]", got)
	}
}

// DELETE removes a stored row whose name the API would refuse, which makes
// every reload fail; only an empty name is refused, the store decides 404.
func TestAdminWorkerGroups_DeleteInvalidStoredName(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["alice"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT gpu: %d %s", w.Code, w.Body.String())
	}

	for _, c := range []struct{ name, path string }{
		{"bad group", workerGroupsPath + "/bad%20group"},
		{"a/b", workerGroupsPath + "/a%2Fb"},
		{".gpu", workerGroupsPath + "/.gpu"},
	} {
		if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: c.name, Users: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
		if err := policy.Reload(ctx, st); err == nil {
			t.Fatalf("%q: Reload succeeded with an invalid stored name", c.name)
		}
		w, env := wgDo(t, srv, "DELETE", c.path, "", adminAuthToken)
		if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"deleted":true`) {
			t.Fatalf("DELETE %q: %d %s", c.name, w.Code, w.Body.String())
		}
		rules, err := st.ListWorkerGroupPolicies(ctx)
		if err != nil || len(rules) != 1 || rules[0].Group != "gpu" {
			t.Fatalf("after DELETE %q stored rules = %+v, %v; want only gpu", c.name, rules, err)
		}
		if err := policy.Reload(ctx, st); err != nil {
			t.Fatalf("Reload after DELETE %q: %v", c.name, err)
		}
		if !policy.Allowed("alice", "gpu") || policy.Allowed("mallory", "gpu") {
			t.Errorf("after DELETE %q the gpu rule must be in force", c.name)
		}
	}

	// A name that is not stored is the store's 404, valid or not.
	for _, path := range []string{workerGroupsPath + "/.gpu", workerGroupsPath + "/esmfold"} {
		if w, _ := wgDo(t, srv, "DELETE", path, "", adminAuthToken); w.Code != http.StatusNotFound {
			t.Errorf("DELETE %s: %d, want 404", path, w.Code)
		}
	}
	// An empty name is refused (chi does not route it; call the handler).
	w, env := callHandler(t, srv.handleDeleteWorkerGroup, "DELETE", workerGroupsPath+"/", "", "", false)
	if w.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, "group name is empty") {
		t.Errorf("DELETE empty name: %d %+v, want 400 group name is empty", w.Code, env.Error)
	}
}

// When the reload after a change that restricts access fails, the change is
// stored but the previous, more open policy stays in force (the limit
// documented in docs/tools/server.md); the next successful GET brings the
// live policy in line with the store.
func TestAdminWorkerGroups_ReloadFailureFailOpenResync(t *testing.T) {
	const badGroup = "bad group"
	type call struct{ method, path, body string }
	// stale holds while the reload is broken, fresh once it succeeded.
	tests := []struct {
		name   string
		setup  []call
		change call
		stale  func(p *groupacl.Policy) bool
		fresh  func(p *groupacl.Policy) bool
	}{
		{
			name: "DELETE under default_deny",
			setup: []call{
				{"PUT", workerGroupsPath + "-default-deny", `{"default_deny":true}`},
				{"PUT", workerGroupsPath + "/gpu", `{"users":["alice"]}`},
			},
			change: call{"DELETE", workerGroupsPath + "/gpu", ""},
			stale:  func(p *groupacl.Policy) bool { return p.Allowed("alice", "gpu") },
			fresh:  func(p *groupacl.Policy) bool { return !p.Allowed("alice", "gpu") && p.Allowed("admin", "gpu") },
		},
		{
			name:   "PUT that removes a user",
			setup:  []call{{"PUT", workerGroupsPath + "/gpu", `{"users":["alice","bob"]}`}},
			change: call{"PUT", workerGroupsPath + "/gpu", `{"users":["alice"]}`},
			stale:  func(p *groupacl.Policy) bool { return p.Allowed("bob", "gpu") },
			fresh:  func(p *groupacl.Policy) bool { return !p.Allowed("bob", "gpu") && p.Allowed("alice", "gpu") },
		},
		{
			name:   "default_deny true",
			change: call{"PUT", workerGroupsPath + "-default-deny", `{"default_deny":true}`},
			stale:  func(p *groupacl.Policy) bool { return !p.DefaultDeny() && p.Allowed("mallory", "esmfold") },
			fresh:  func(p *groupacl.Policy) bool { return p.DefaultDeny() && !p.Allowed("mallory", "esmfold") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			srv, st, policy := newWorkerGroupServer(t)
			for _, c := range tt.setup {
				if w, _ := wgDo(t, srv, c.method, c.path, c.body, adminAuthToken); w.Code != http.StatusOK {
					t.Fatalf("setup %s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
				}
			}
			// Break the reload with a row the policy cannot load.
			if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: badGroup, Users: []string{"x"}}); err != nil {
				t.Fatal(err)
			}

			w, env := wgDo(t, srv, tt.change.method, tt.change.path, tt.change.body, adminAuthToken)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("change: %d, want 500: %s", w.Code, w.Body.String())
			}
			if env.Error == nil || !strings.Contains(env.Error.Message, "stored but the live worker group policy could not be reloaded") {
				t.Errorf("error = %+v", env.Error)
			}
			// The documented limit: stored, but the more open policy is
			// still enforced.
			if !tt.stale(policy) {
				t.Error("expected the previous policy to stay in force after the failed reload")
			}
			if w, _ := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken); w.Code != http.StatusInternalServerError {
				t.Errorf("GET while the reload is broken: %d, want 500", w.Code)
			}

			// Repair the store; the next GET re-syncs.
			if _, err := st.DeleteWorkerGroupPolicy(ctx, badGroup); err != nil {
				t.Fatal(err)
			}
			if w, _ := wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken); w.Code != http.StatusOK {
				t.Fatalf("GET after repair: %d %s", w.Code, w.Body.String())
			}
			if !tt.fresh(policy) {
				t.Error("after a successful GET the live policy must match the store")
			}
		})
	}
}
