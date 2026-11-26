package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Worker group managers: /api/v1/worker-groups (users and group-only worker
// keys of the groups a manager manages) and the managers field of the admin
// API.

const (
	managedGroupsPath = "/api/v1/worker-groups"
	mgrToken          = "un=mgr|tokenid=t-mgr|expiry=4102444800|sig=m"
	plainToken        = "un=plain|tokenid=t-plain|expiry=4102444800|sig=p"
)

// mgDo is wgDo, but sends no Authorization header for token "" (anonymous).
func mgDo(t *testing.T, srv *Server, method, path, body, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

// newManagedGroupServer is newWorkerGroupServer with two rules set by the
// admin: g (users alice, manager mgr) and h (users bob, no manager).
func newManagedGroupServer(t *testing.T) (*Server, store.Store, *groupacl.Policy) {
	t.Helper()
	srv, st, policy := newWorkerGroupServer(t)
	for _, c := range []struct{ path, body string }{
		{workerGroupsPath + "/g", `{"users":["alice"],"managers":["mgr"]}`},
		{workerGroupsPath + "/h", `{"users":["bob"]}`},
	} {
		if w, _ := wgDo(t, srv, "PUT", c.path, c.body, adminAuthToken); w.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", c.path, w.Code, w.Body.String())
		}
	}
	return srv, st, policy
}

// mintedKey is the answer of a key creation.
type mintedKey struct {
	Key       string          `json:"key"`
	WorkerKey model.WorkerKey `json:"worker_key"`
}

// adminCreateKey creates a worker key through the admin route.
func adminCreateKey(t *testing.T, srv *Server, groups string) mintedKey {
	t.Helper()
	w, env := wgDo(t, srv, "POST", "/api/v1/admin/worker-keys", `{"label":"boot","groups":`+groups+`}`, adminAuthToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin create key: %d %s", w.Code, w.Body.String())
	}
	return decode[mintedKey](t, env)
}

// storeKey stores a worker key with the given groups directly.
func storeKey(t *testing.T, st store.Store, id string, groups []string) {
	t.Helper()
	_, hash, prefix, err := model.GenerateWorkerKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkerKey(context.Background(), &model.WorkerKey{ID: id, KeyHash: hash, KeyPrefix: prefix,
		Groups: groups, CreatedBy: "admin", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

type managedGroupsBody struct {
	Groups []model.WorkerGroupPolicy `json:"groups"`
}

type groupKeysBody struct {
	Total      int               `json:"total"`
	WorkerKeys []model.WorkerKey `json:"worker_keys"`
}

// A manager of g can do every action on g, and a key issued through the
// group route registers workers into g only.
func TestManagedGroups_ManagerActions(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`) // worker auth on
	logs := captureLogs(srv)

	w, env := mgDo(t, srv, "GET", managedGroupsPath, "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list: %d %s", w.Code, w.Body.String())
	}
	if list := decode[managedGroupsBody](t, env); len(list.Groups) != 1 || list.Groups[0].Group != "g" ||
		!reflect.DeepEqual(list.Groups[0].Managers, []string{"mgr"}) {
		t.Errorf("manager's list = %+v, want only g", list.Groups)
	}

	w, env = mgDo(t, srv, "GET", managedGroupsPath+"/g", "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET g: %d %s", w.Code, w.Body.String())
	}
	if rule := decode[model.WorkerGroupPolicy](t, env); !reflect.DeepEqual(rule.Users, []string{"alice"}) {
		t.Errorf("GET g = %+v", rule)
	}

	// Add a user: in force at once.
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/g/users", `{"username":"carol"}`, mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("add carol: %d %s", w.Code, w.Body.String())
	}
	if rule := decode[model.WorkerGroupPolicy](t, env); !reflect.DeepEqual(rule.Users, []string{"alice", "carol"}) || rule.UpdatedBy != "mgr" {
		t.Errorf("after add = %+v", rule)
	}
	if !policy.Allowed("carol", "g") {
		t.Error("an added user must be allowed at once")
	}
	// A username holding '/' travels escaped.
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/g/users", `{"username":"svc/robot"}`, mgrToken); w.Code != http.StatusOK {
		t.Fatalf("add svc/robot: %d %s", w.Code, w.Body.String())
	}
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/users/svc%2Frobot", "", mgrToken); w.Code != http.StatusOK {
		t.Fatalf("remove svc/robot: %d %s", w.Code, w.Body.String())
	}

	// Remove a user: in force at once; a second time is 404.
	w, env = mgDo(t, srv, "DELETE", managedGroupsPath+"/g/users/alice", "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("remove alice: %d %s", w.Code, w.Body.String())
	}
	if rule := decode[model.WorkerGroupPolicy](t, env); !reflect.DeepEqual(rule.Users, []string{"carol"}) {
		t.Errorf("after remove = %+v", rule)
	}
	if policy.Allowed("alice", "g") {
		t.Error("a removed user must be denied at once")
	}
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/users/alice", "", mgrToken); w.Code != http.StatusNotFound {
		t.Errorf("remove alice again: %d, want 404", w.Code)
	}
	// The manager is not in users, yet may use the group.
	if !policy.Allowed("mgr", "g") {
		t.Error("a manager must be allowed on their group")
	}

	// Create a key: groups forced to [g], created_by the manager.
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"node1","description":"gpu box"}`, mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	key := decode[mintedKey](t, env)
	if key.Key == "" || !reflect.DeepEqual(key.WorkerKey.Groups, []string{"g"}) || key.WorkerKey.CreatedBy != "mgr" || key.WorkerKey.Label != "node1" {
		t.Fatalf("minted = %+v", key)
	}
	// groups cannot be passed.
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"x","groups":["h"]}`, mgrToken); w.Code != http.StatusBadRequest {
		t.Errorf("create key with groups: %d, want 400", w.Code)
	}

	w, env = mgDo(t, srv, "GET", managedGroupsPath+"/g/keys", "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list keys: %d %s", w.Code, w.Body.String())
	}
	if keys := decode[groupKeysBody](t, env); keys.Total != 1 || keys.WorkerKeys[0].ID != key.WorkerKey.ID {
		t.Errorf("group keys = %+v, want only %s", keys, key.WorkerKey.ID)
	}
	if strings.Contains(w.Body.String(), key.Key) || strings.Contains(w.Body.String(), model.HashWorkerKey(key.Key)) {
		t.Error("the key list must not carry the raw key or its hash")
	}

	// The key registers a worker into g, never into h.
	keyedRegister(t, srv, key.Key, "g")
	if w, _ := keyedDo(t, srv, key.Key, "POST", "/api/v1/workers/", `{"name":"w","hostname":"h","runtime":"none","group":"h"}`); w.Code != http.StatusForbidden {
		t.Errorf("register into h with a key of g: %d, want 403", w.Code)
	}

	// Revoke.
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/"+key.WorkerKey.ID, "", mgrToken); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if srv.workerAuth.Authenticate(ctx, key.Key) != nil {
		t.Error("a revoked key still authenticates")
	}
	if k, _ := st.GetWorkerKeyByID(ctx, key.WorkerKey.ID); k != nil {
		t.Error("the revoked key is still stored")
	}

	out := logs.String()
	for _, want := range []string{"actor=mgr", "group=g", "action=add-user", "action=remove-user", "action=create-key", "action=revoke-key"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"alice", "carol", key.Key} {
		if strings.Contains(out, secret) {
			t.Errorf("the log must not contain %q:\n%s", secret, out)
		}
	}
}

// A manager is refused on another group and on the admin routes; a plain
// user is refused and anonymous must authenticate.
func TestManagedGroups_Refused(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	storeKey(t, st, "wk_h", []string{"h"})

	groupRoutes := func(group string) []struct{ method, path, body string } {
		return []struct{ method, path, body string }{
			{"GET", managedGroupsPath + "/" + group, ""},
			{"POST", managedGroupsPath + "/" + group + "/users", `{"username":"zed"}`},
			{"DELETE", managedGroupsPath + "/" + group + "/users/bob", ""},
			{"GET", managedGroupsPath + "/" + group + "/keys", ""},
			{"POST", managedGroupsPath + "/" + group + "/keys", `{"label":"x"}`},
			{"DELETE", managedGroupsPath + "/" + group + "/keys/wk_h", ""},
		}
	}
	check := func(who, token string, routes []struct{ method, path, body string }, want int) {
		t.Helper()
		for _, c := range routes {
			if w, _ := mgDo(t, srv, c.method, c.path, c.body, token); w.Code != want {
				t.Errorf("%s: %s %s = %d, want %d: %s", who, c.method, c.path, w.Code, want, w.Body.String())
			}
		}
	}
	check("manager of g on h", mgrToken, groupRoutes("h"), http.StatusForbidden)
	check("plain user on g", plainToken, groupRoutes("g"), http.StatusForbidden)
	check("plain user on h", plainToken, groupRoutes("h"), http.StatusForbidden)
	check("anonymous on g", "", groupRoutes("g"), http.StatusUnauthorized)
	check("manager of g on a group without rule", mgrToken, groupRoutes("nope"), http.StatusForbidden)
	check("admin on a group without rule", adminAuthToken, groupRoutes("nope"), http.StatusNotFound)

	// The admin routes stay admin-only, including changing managers.
	check("manager on admin routes", mgrToken, []struct{ method, path, body string }{
		{"GET", workerGroupsPath, ""},
		{"PUT", workerGroupsPath + "/g", `{"users":["alice"],"managers":["mgr","mgr2"]}`},
		{"PUT", workerGroupsPath + "/g", `{"users":["alice"],"admins":true}`},
		{"DELETE", workerGroupsPath + "/g", ""},
		{"PUT", workerGroupsPath + "-default-deny", `{"default_deny":true}`},
		{"POST", "/api/v1/admin/worker-keys", `{"label":"x","groups":["g"]}`},
		{"DELETE", "/api/v1/admin/worker-keys/wk_h", ""},
	}, http.StatusForbidden)

	if w, _ := mgDo(t, srv, "GET", managedGroupsPath+"/.bad", "", mgrToken); w.Code != http.StatusBadRequest {
		t.Errorf("invalid group name: %d, want 400", w.Code)
	}
	if w, _ := mgDo(t, srv, "GET", managedGroupsPath, "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous list: %d, want 401", w.Code)
	}
	w, env := mgDo(t, srv, "GET", managedGroupsPath, "", plainToken)
	if w.Code != http.StatusOK || len(decode[managedGroupsBody](t, env).Groups) != 0 || !strings.Contains(string(env.Data), `"groups":[]`) {
		t.Errorf("plain user's list: %d %s, want 200 and no groups", w.Code, env.Data)
	}

	// Nothing changed.
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 2 || !reflect.DeepEqual(rules[0].Users, []string{"alice"}) ||
		!reflect.DeepEqual(rules[0].Managers, []string{"mgr"}) || !reflect.DeepEqual(rules[1].Users, []string{"bob"}) {
		t.Errorf("rules = %+v, %v; want unchanged", rules, err)
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_h"); k == nil {
		t.Error("the key of h was revoked")
	}
	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != 2 {
		t.Errorf("%d keys stored, want 2 (no key may have been created)", len(keys))
	}
}

// A server admin may use the group routes on every group.
func TestManagedGroups_AdminUsesGroupRoutes(t *testing.T) {
	srv, _, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)

	w, env := mgDo(t, srv, "GET", managedGroupsPath, "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list: %d", w.Code)
	}
	if list := decode[managedGroupsBody](t, env); len(list.Groups) != 2 {
		t.Errorf("admin's list = %+v, want g and h", list.Groups)
	}
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/h/users", `{"username":"dave"}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("admin add dave to h: %d %s", w.Code, w.Body.String())
	}
	if !policy.Allowed("dave", "h") {
		t.Error("dave must be allowed on h")
	}
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/h/keys", `{"label":"n"}`, adminAuthToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin create key on h: %d %s", w.Code, w.Body.String())
	}
	if k := decode[mintedKey](t, env); !reflect.DeepEqual(k.WorkerKey.Groups, []string{"h"}) {
		t.Errorf("groups = %v, want [h]", k.WorkerKey.Groups)
	}
}

// A manager cannot revoke a key that is not exactly [g]'s.
func TestManagedGroups_ManagerCannotRevokeForeignKeys(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	storeKey(t, st, "wk_gh", []string{"g", "h"})
	storeKey(t, st, "wk_h", []string{"h"})
	storeKey(t, st, "wk_any", []string{})
	storeKey(t, st, "wk_g", []string{"g"})

	for _, id := range []string{"wk_gh", "wk_h", "wk_any", "wk_missing"} {
		if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/"+id, "", mgrToken); w.Code != http.StatusNotFound {
			t.Errorf("revoke %s through g: %d, want 404", id, w.Code)
		}
		if id != "wk_missing" {
			if k, _ := st.GetWorkerKeyByID(ctx, id); k == nil {
				t.Errorf("%s was deleted", id)
			}
		}
	}
	w, env := mgDo(t, srv, "GET", managedGroupsPath+"/g/keys", "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	if keys := decode[groupKeysBody](t, env); keys.Total != 1 || keys.WorkerKeys[0].ID != "wk_g" {
		t.Errorf("g's keys = %+v, want only wk_g", keys)
	}
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/wk_g", "", mgrToken); w.Code != http.StatusOK {
		t.Errorf("revoke wk_g: %d, want 200", w.Code)
	}
}

// A manager removed by an admin loses access at once; one removed behind
// the API's back (a direct store write) cannot write either, because a
// write reloads the policy before checking the right.
func TestManagedGroups_RemovedManagerLosesAccess(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n"}`, mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	keyID := decode[mintedKey](t, env).WorkerKey.ID

	logs := captureLogs(srv)
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["alice"],"managers":[]}`, adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("remove manager: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Managers    []string  `json:"managers"`
		GroupKeyIDs *[]string `json:"group_key_ids"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Managers) != 0 || resp.GroupKeyIDs == nil || !reflect.DeepEqual(*resp.GroupKeyIDs, []string{keyID}) {
		t.Errorf("response = %s, want no managers and group_key_ids [%s]", env.Data, keyID)
	}
	if out := logs.String(); !strings.Contains(out, "managers removed") || !strings.Contains(out, "group_keys=1") {
		t.Errorf("want one INFO with the key count:\n%s", out)
	}
	// The key stays valid; the admin decides.
	if k, _ := st.GetWorkerKeyByID(ctx, keyID); k == nil {
		t.Error("removing a manager must not revoke keys")
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", managedGroupsPath + "/g", ""},
		{"POST", managedGroupsPath + "/g/users", `{"username":"zed"}`},
		{"DELETE", managedGroupsPath + "/g/keys/" + keyID, ""},
	} {
		if w, _ := mgDo(t, srv, c.method, c.path, c.body, mgrToken); w.Code != http.StatusForbidden {
			t.Errorf("removed manager: %s %s = %d, want 403", c.method, c.path, w.Code)
		}
	}

	// For each write: appoint again, then remove behind the API's back: the
	// live policy still names mgr, but the write reloads first and refuses.
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, method, path, body string }{
		{"add-user", "POST", managedGroupsPath + "/g/users", `{"username":"zed"}`},
		{"remove-user", "DELETE", managedGroupsPath + "/g/users/alice", ""},
		{"create-key", "POST", managedGroupsPath + "/g/keys", `{"label":"n2"}`},
		{"revoke-key", "DELETE", managedGroupsPath + "/g/keys/" + keyID, ""},
	} {
		if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["alice"],"managers":["mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
			t.Fatalf("%s: appoint: %d", c.name, w.Code)
		}
		if !policy.IsManager("mgr", "g") {
			t.Fatalf("%s: mgr must manage g in the live policy before the store write", c.name)
		}
		if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "g", Users: []string{"alice"}, Managers: []string{}}); err != nil {
			t.Fatal(err)
		}
		if w, _ := mgDo(t, srv, c.method, c.path, c.body, mgrToken); w.Code != http.StatusForbidden {
			t.Errorf("%s by a manager removed in the store: %d, want 403: %s", c.name, w.Code, w.Body.String())
		}
	}
	if rules, _ := st.ListWorkerGroupPolicies(ctx); !reflect.DeepEqual(rules[0].Users, []string{"alice"}) {
		t.Errorf("users = %v, want unchanged [alice]", rules[0].Users)
	}
	keysAfter, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keysAfter) != len(keysBefore) {
		t.Errorf("%d keys stored, want %d (no key created or revoked)", len(keysAfter), len(keysBefore))
	}
	if k, _ := st.GetWorkerKeyByID(ctx, keyID); k == nil {
		t.Error("the key was revoked by a manager removed in the store")
	}
}

// Creating a group key needs worker authentication to be on already.
func TestManagedGroups_CreateKeyNeedsWorkerAuth(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n"}`, mgrToken)
	if w.Code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict ||
		!strings.Contains(env.Error.Message, "an admin must create the first worker key") {
		t.Fatalf("create key without worker auth: %d %+v, want 409", w.Code, env.Error)
	}
	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != 0 {
		t.Errorf("a refused request stored %d keys", len(keys))
	}
	adminCreateKey(t, srv, `["default"]`)
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n"}`, mgrToken); w.Code != http.StatusCreated {
		t.Errorf("create key once worker auth is on: %d %s", w.Code, w.Body.String())
	}
	// Same validation as the admin route.
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n","expires_at":"2000-01-01T00:00:00Z"}`, mgrToken); w.Code != http.StatusBadRequest {
		t.Errorf("past expiry: %d, want 400", w.Code)
	}
}

// A manager's write that fails because the stored rule of another group
// cannot be loaded answers 500 with the generic message, naming neither
// that group nor its users; the detail goes to the server log, and a
// server admin gets it in the answer.
func TestManagedGroups_InternalErrorHidesDetail(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	storeKey(t, st, "wk_g", []string{"g"})
	const brokenGroup, brokenUser = "secretgrp", "leaky.user"
	// A row the API would refuse (trailing whitespace), written behind its
	// back: every reload fails, naming the group and the username.
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: brokenGroup, Users: []string{brokenUser + " "}}); err != nil {
		t.Fatal(err)
	}
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(srv)

	writes := []struct{ name, method, path, body string }{
		{"add-user", "POST", managedGroupsPath + "/g/users", `{"username":"zed"}`},
		{"remove-user", "DELETE", managedGroupsPath + "/g/users/alice", ""},
		{"create-key", "POST", managedGroupsPath + "/g/keys", `{"label":"n"}`},
		{"revoke-key", "DELETE", managedGroupsPath + "/g/keys/wk_g", ""},
	}
	for _, c := range writes {
		w, env := mgDo(t, srv, c.method, c.path, c.body, mgrToken)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("manager %s: %d, want 500: %s", c.name, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, brokenGroup) || strings.Contains(body, brokenUser) {
			t.Errorf("manager %s: the answer names the other group or its user: %s", c.name, body)
		}
		if env.Error == nil || env.Error.Code != model.ErrInternal || env.Error.Message != internalErrorMessage {
			t.Errorf("manager %s: error = %+v, want %s %q", c.name, env.Error, model.ErrInternal, internalErrorMessage)
		}
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, brokenGroup) {
		t.Errorf("want the detail logged at ERROR:\n%s", out)
	}

	// A server admin gets the detail.
	w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/users", `{"username":"zed"}`, adminAuthToken)
	if w.Code != http.StatusInternalServerError || env.Error == nil || !strings.Contains(env.Error.Message, brokenGroup) ||
		!strings.Contains(env.Error.Message, "could not be reloaded") {
		t.Errorf("admin: %d %+v, want 500 with the detail", w.Code, env.Error)
	}

	// Nothing was written.
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Group == "g" && !reflect.DeepEqual(r.Users, []string{"alice"}) {
			t.Errorf("users of g = %v, want unchanged [alice]", r.Users)
		}
	}
	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != len(keysBefore) {
		t.Errorf("%d keys stored, want %d", len(keys), len(keysBefore))
	}
}

// Label and description are bounded on both create routes; the manager
// route issues at most maxGroupWorkerKeys keys of exactly [group].
func TestManagedGroups_KeyLimits(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)

	long := func(n int) string { return strings.Repeat("x", n) }
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"label at the limit", `{"label":"` + long(maxWorkerKeyLabelBytes) + `"}`, http.StatusCreated},
		{"label too long", `{"label":"` + long(maxWorkerKeyLabelBytes+1) + `"}`, http.StatusBadRequest},
		{"description at the limit", `{"label":"n","description":"` + long(maxWorkerKeyDescriptionBytes) + `"}`, http.StatusCreated},
		{"description too long", `{"label":"n","description":"` + long(maxWorkerKeyDescriptionBytes+1) + `"}`, http.StatusBadRequest},
	} {
		w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", c.body, mgrToken)
		if w.Code != c.want {
			t.Errorf("manager route, %s: %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
		if c.want == http.StatusBadRequest && (env.Error == nil || env.Error.Code != model.ErrValidation || !strings.Contains(env.Error.Message, "at most")) {
			t.Errorf("manager route, %s: error = %+v", c.name, env.Error)
		}
	}
	// The admin create route has the same bounds.
	for _, body := range []string{
		`{"label":"` + long(maxWorkerKeyLabelBytes+1) + `","groups":["g"]}`,
		`{"label":"n","groups":["g"],"description":"` + long(maxWorkerKeyDescriptionBytes+1) + `"}`,
	} {
		if w, _ := wgDo(t, srv, "POST", "/api/v1/admin/worker-keys", body, adminAuthToken); w.Code != http.StatusBadRequest {
			t.Errorf("admin route with a too long field: %d, want 400", w.Code)
		}
	}

	// Fill g up to the bound (a key with other groups does not count).
	storeKey(t, st, "wk_gh", []string{"g", "h"})
	existing, err := srv.groupKeys(ctx, "g")
	if err != nil {
		t.Fatal(err)
	}
	for i := len(existing); i < maxGroupWorkerKeys; i++ {
		storeKey(t, st, fmt.Sprintf("wk_fill_%d", i), []string{"g"})
	}
	w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"one-too-many"}`, mgrToken)
	if w.Code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict ||
		!strings.Contains(env.Error.Message, fmt.Sprintf("at most %d", maxGroupWorkerKeys)) || !strings.Contains(env.Error.Message, "revoke") {
		t.Fatalf("create key over the bound: %d %+v, want 409", w.Code, env.Error)
	}
	if keys, _ := srv.groupKeys(ctx, "g"); len(keys) != maxGroupWorkerKeys {
		t.Errorf("g has %d keys, want %d (nothing created)", len(keys), maxGroupWorkerKeys)
	}
	// The admin route is not bounded by it.
	adminCreateKey(t, srv, `["g"]`)
	// Revoking one makes room again.
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/wk_fill_99", "", mgrToken); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/wk_fill_98", "", mgrToken); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w, _ := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"fits"}`, mgrToken); w.Code != http.StatusCreated {
		t.Errorf("create key below the bound: %d %s", w.Code, w.Body.String())
	}
}

// The managers field of the admin API.
func TestAdminWorkerGroups_Managers(t *testing.T) {
	srv, st, policy := newWorkerGroupServer(t)

	// Not on the default group, whichever spelling.
	w, env := wgDo(t, srv, "PUT", workerGroupsPath+"/default", `{"users":[],"managers":["mgr"]}`, adminAuthToken)
	if w.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, "cannot have managers") {
		t.Errorf("managers on default: %d %+v, want 400", w.Code, env.Error)
	}
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/default", `{"users":["carol"],"managers":[]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Errorf("default with an empty managers list: %d, want 200", w.Code)
	}
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":[],"managers":["anonymous"]}`, adminAuthToken)
	if w.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, `managers holds "anonymous"`) {
		t.Errorf("anonymous manager: %d %+v, want 400", w.Code, env.Error)
	}

	// Set, then a PUT without managers keeps them.
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["alice"],"managers":["mgr","mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT g: %d", w.Code)
	}
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["bob"]}`, adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT g without managers: %d", w.Code)
	}
	if rule := decode[model.WorkerGroupPolicy](t, env); !reflect.DeepEqual(rule.Managers, []string{"mgr"}) || !reflect.DeepEqual(rule.Users, []string{"bob"}) {
		t.Errorf("PUT without managers = %+v, want managers kept [mgr]", rule)
	}
	if strings.Contains(string(env.Data), "group_key_ids") {
		t.Errorf("no manager removed, no group_key_ids: %s", env.Data)
	}
	if !policy.IsManager("mgr", "g") || !policy.Allowed("mgr", "g") {
		t.Error("mgr must manage and may use g")
	}
	w, env = wgDo(t, srv, "GET", workerGroupsPath, "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d", w.Code)
	}
	if list := decode[workerGroupsBody](t, env); len(list.Groups) != 2 || !reflect.DeepEqual(list.Groups[1].Managers, []string{"mgr"}) ||
		list.Groups[0].Managers == nil || len(list.Groups[0].Managers) != 0 {
		t.Errorf("GET groups = %+v", list.Groups)
	}

	// Clearing them reports the group keys, [] when there are none.
	w, env = wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["bob"],"managers":[]}`, adminAuthToken)
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"group_key_ids":[]`) {
		t.Errorf("clear managers: %d %s, want group_key_ids []", w.Code, env.Data)
	}
	if policy.IsManager("mgr", "g") {
		t.Error("mgr must no longer manage g")
	}

	// DELETE of a rule with managers or group keys names the keys.
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":[],"managers":["mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT g: %d", w.Code)
	}
	storeKey(t, st, "wk_g", []string{"g"})
	w, env = wgDo(t, srv, "DELETE", workerGroupsPath+"/g", "", adminAuthToken)
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"group_key_ids":["wk_g"]`) {
		t.Errorf("DELETE g: %d %s, want group_key_ids [wk_g]", w.Code, env.Data)
	}
	if k, _ := st.GetWorkerKeyByID(context.Background(), "wk_g"); k == nil {
		t.Error("deleting a rule must not revoke its keys")
	}
}

// The admin PATCH logs a WARN when it changes the groups of a key a
// non-admin created; it does not refuse.
func TestUpdateWorkerKey_WarnsOnManagerKeyGroups(t *testing.T) {
	srv, _, _ := newManagedGroupServer(t)
	admin := adminCreateKey(t, srv, `["default"]`)
	w, env := mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n"}`, mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body.String())
	}
	mgrKey := decode[mintedKey](t, env).WorkerKey.ID

	logs := captureLogs(srv)
	if w, _ := wgDo(t, srv, "PATCH", "/api/v1/admin/worker-keys/"+admin.WorkerKey.ID, `{"groups":["default","g"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PATCH admin key: %d", w.Code)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("an admin-created key must not warn:\n%s", logs.String())
	}
	if w, _ := wgDo(t, srv, "PATCH", "/api/v1/admin/worker-keys/"+mgrKey, `{"label":"renamed"}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PATCH label: %d", w.Code)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a PATCH without groups must not warn:\n%s", logs.String())
	}
	if w, _ := wgDo(t, srv, "PATCH", "/api/v1/admin/worker-keys/"+mgrKey, `{"groups":["g","h"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PATCH manager key: %d", w.Code)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "key_id="+mgrKey) || !strings.Contains(out, "new_groups") {
		t.Errorf("want one WARN naming %s:\n%s", mgrKey, out)
	}
}
