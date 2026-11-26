package server

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// The worker group pages of the web UI (/admin/worker-groups,
// /worker-groups[/{group}]), driven through the whole server so that they
// run the real operations (group_admin.go) behind the UI's real session
// middleware.

// uiCookie creates a UI session for username (server admin or not) and
// returns the cookie that carries it.
func uiCookie(t *testing.T, st store.Store, username string, admin bool) *http.Cookie {
	t.Helper()
	role := string(model.RoleUser)
	if admin {
		role = string(model.RoleAdmin)
	}
	sess, err := ui.NewSessionManager(st).CreateSession(context.Background(), "user_"+username, username, role, "tok-"+username, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: ui.SessionCookieName, Value: sess.ID}
}

// setUserRole stores username's account with role (created when missing),
// as PUT /api/v1/admin/users/{username}/role does.
func setUserRole(t *testing.T, st store.Store, username string, role model.UserRole) {
	t.Helper()
	ctx := context.Background()
	user, err := st.GetOrCreateUser(ctx, username, model.ProviderBVBRC)
	if err != nil {
		t.Fatalf("store user %s: %v", username, err)
	}
	user.Role = role
	if err := st.UpdateUser(ctx, user); err != nil {
		t.Fatalf("set role of %s: %v", username, err)
	}
}

// listRules returns the stored rules (fails the test on an error).
func listRules(t *testing.T, st store.Store) []*model.WorkerGroupPolicy {
	t.Helper()
	rules, err := st.ListWorkerGroupPolicies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// uiDo sends a UI request: a form POST when form is not nil, with cookie
// when it is not nil.
func uiDo(t *testing.T, srv *Server, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// navLink is the "Worker groups" navigation link of the layout.
const navLink = `href="/worker-groups"`

// rawKeyRe finds the raw key in the page answering a key creation.
var rawKeyRe = regexp.MustCompile(`select-all">([^<]+)</code>`)

// wantStatus fails when w does not have status want.
func wantStatus(t *testing.T, what string, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", what, w.Code, want, w.Body.String())
	}
}

// wantRedirect fails unless w redirects (303) to location.
func wantRedirect(t *testing.T, what string, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	wantStatus(t, what, w, http.StatusSeeOther)
	if got := w.Header().Get("Location"); got != location {
		t.Fatalf("%s: redirect to %q, want %q", what, got, location)
	}
}

// A plain user (no group managed) gets 403 on both pages and on every form,
// changes nothing, and is not shown the navigation link.
func TestUIWorkerGroups_PlainUser(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	plain := uiCookie(t, st, "plain", false)

	if w := uiDo(t, srv, "GET", "/workers", nil, plain); w.Code != http.StatusOK || strings.Contains(w.Body.String(), navLink) {
		t.Errorf("workers page for a plain user: %d, nav link shown = %v", w.Code, strings.Contains(w.Body.String(), navLink))
	}
	for _, path := range []string{"/worker-groups", "/worker-groups/g", "/worker-groups/h", "/admin/worker-groups"} {
		wantStatus(t, "GET "+path, uiDo(t, srv, "GET", path, nil, plain), http.StatusForbidden)
	}
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/worker-groups/g/users", url.Values{"username": {"zed"}}},
		{"/worker-groups/g/users/remove", url.Values{"username": {"alice"}}},
		{"/worker-groups/g/keys", url.Values{"label": {"x"}}},
		{"/admin/worker-groups", url.Values{"group": {"g"}, "users": {"plain"}}},
		{"/admin/worker-groups/delete", url.Values{"group": {"g"}}},
		{"/admin/worker-groups/default-deny", url.Values{"default_deny": {"true"}}},
	} {
		wantStatus(t, "POST "+c.path, uiDo(t, srv, "POST", c.path, c.form, plain), http.StatusForbidden)
	}
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 2 || !reflect.DeepEqual(rules[0].Users, []string{"alice"}) {
		t.Errorf("rules = %+v, %v; want unchanged", rules, err)
	}
	if policy.Allowed("zed", "g") || policy.DefaultDeny() {
		t.Error("a plain user changed the policy")
	}
}

// A manager of g sees the link, the list (g only) and g's page, and can add
// and remove users, issue a key (the raw key shown once, never cached, gone
// on reload), revoke it, and remove an offline worker; an online worker is
// refused with the server's message. Group h and the admin page are 403.
func TestUIWorkerGroups_Manager(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`) // worker auth on
	mgr := uiCookie(t, st, "mgr", false)
	old := time.Now().UTC().Add(-time.Hour)
	seedWorker(t, st, "wrk_off", "off-node", "g", model.WorkerStateOffline, old, "", "192.0.2.21")
	seedWorker(t, st, "wrk_on", "on-node", "g", model.WorkerStateOnline, time.Now().UTC(), "", "192.0.2.22")
	seedWorker(t, st, "wrk_h", "h-node", "h", model.WorkerStateOffline, old, "", "192.0.2.23")

	if w := uiDo(t, srv, "GET", "/workers", nil, mgr); !strings.Contains(w.Body.String(), navLink) {
		t.Error("the manager is not shown the Worker groups link")
	}
	if w := uiDo(t, srv, "GET", "/workers", nil, mgr); strings.Contains(w.Body.String(), "Peer address") {
		t.Error("the workers page shows the peer address column to a non-admin")
	}
	w := uiDo(t, srv, "GET", "/worker-groups", nil, mgr)
	wantStatus(t, "GET /worker-groups", w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `href="/worker-groups/g"`) || strings.Contains(w.Body.String(), `href="/worker-groups/h"`) {
		t.Errorf("manager's list must show g only: %s", w.Body.String())
	}

	w = uiDo(t, srv, "GET", "/worker-groups/g", nil, mgr)
	wantStatus(t, "GET /worker-groups/g", w, http.StatusOK)
	page := w.Body.String()
	for _, want := range []string{"alice", "off-node", "on-node", "192.0.2.21", "off-node.example", `name="id" value="wrk_off"`} {
		if !strings.Contains(page, want) {
			t.Errorf("g's page lacks %q", want)
		}
	}
	for _, leak := range []string{"h-node", "192.0.2.23", `name="id" value="wrk_on"`} {
		if strings.Contains(page, leak) {
			t.Errorf("g's page contains %q (another group's worker, or a Remove form for an online worker)", leak)
		}
	}

	wantStatus(t, "GET /worker-groups/h", uiDo(t, srv, "GET", "/worker-groups/h", nil, mgr), http.StatusForbidden)
	wantStatus(t, "GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, mgr), http.StatusForbidden)
	wantStatus(t, "add user to h", uiDo(t, srv, "POST", "/worker-groups/h/users", url.Values{"username": {"zed"}}, mgr), http.StatusForbidden)
	wantStatus(t, "remove worker of h", uiDo(t, srv, "POST", "/worker-groups/h/workers/remove", url.Values{"id": {"wrk_h"}}, mgr), http.StatusForbidden)
	if policy.Allowed("zed", "h") {
		t.Error("the manager of g changed h")
	}
	if wk, _ := st.GetWorker(ctx, "wrk_h"); wk == nil {
		t.Error("the manager of g removed a worker of h")
	}

	// Users.
	wantRedirect(t, "add carol", uiDo(t, srv, "POST", "/worker-groups/g/users", url.Values{"username": {"carol"}}, mgr), "/worker-groups/g?done=user-added")
	if !policy.Allowed("carol", "g") {
		t.Error("an added user must be allowed at once")
	}
	wantRedirect(t, "remove alice", uiDo(t, srv, "POST", "/worker-groups/g/users/remove", url.Values{"username": {"alice"}}, mgr), "/worker-groups/g?done=user-removed")
	if policy.Allowed("alice", "g") {
		t.Error("a removed user must be denied at once")
	}
	w = uiDo(t, srv, "POST", "/worker-groups/g/users/remove", url.Values{"username": {"alice"}}, mgr)
	wantStatus(t, "remove alice again", w, http.StatusNotFound)
	if !strings.Contains(w.Body.String(), "the user is not in the users of worker group") || !strings.Contains(w.Body.String(), `id="form-error"`) {
		t.Errorf("the refusal must show the server's message on the page: %s", w.Body.String())
	}

	// Keys: the raw key once.
	w = uiDo(t, srv, "POST", "/worker-groups/g/keys", url.Values{"label": {"node1"}, "description": {"gpu box"}}, mgr)
	wantStatus(t, "create key", w, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	m := rawKeyRe.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no raw key in the answer: %s", w.Body.String())
	}
	raw := m[1]
	if n := strings.Count(w.Body.String(), raw); n != 1 {
		t.Errorf("the raw key appears %d times, want once", n)
	}
	auth := srv.workerAuth.Authenticate(ctx, raw)
	if auth == nil {
		t.Fatal("the shown key does not authenticate")
	}
	keyID := auth.KeyID
	stored, err := st.GetWorkerKeyByID(ctx, keyID)
	if err != nil || stored == nil || !reflect.DeepEqual(stored.Groups, []string{"g"}) || stored.CreatedBy != "mgr" || stored.Label != "node1" {
		t.Fatalf("stored key = %+v, %v; want groups [g], created_by mgr", stored, err)
	}
	w = uiDo(t, srv, "GET", "/worker-groups/g", nil, mgr)
	if strings.Contains(w.Body.String(), raw) || !strings.Contains(w.Body.String(), "node1") {
		t.Error("on reload the page must list the key but not show the raw key")
	}
	wantRedirect(t, "revoke key", uiDo(t, srv, "POST", "/worker-groups/g/keys/revoke", url.Values{"id": {keyID}}, mgr), "/worker-groups/g?done=key-revoked")
	if k, _ := st.GetWorkerKeyByID(ctx, keyID); k != nil {
		t.Error("the revoked key is still stored")
	}

	// Workers.
	w = uiDo(t, srv, "POST", "/worker-groups/g/workers/remove", url.Values{"id": {"wrk_on"}}, mgr)
	wantStatus(t, "remove online worker", w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "wrk_on is online") {
		t.Errorf("the refusal must show the server's message: %s", w.Body.String())
	}
	if wk, _ := st.GetWorker(ctx, "wrk_on"); wk == nil {
		t.Error("the online worker was removed")
	}
	wantRedirect(t, "remove offline worker", uiDo(t, srv, "POST", "/worker-groups/g/workers/remove", url.Values{"id": {"wrk_off"}}, mgr), "/worker-groups/g?done=worker-removed")
	if wk, _ := st.GetWorker(ctx, "wrk_off"); wk != nil {
		t.Error("the offline worker is still stored")
	}
}

// An admin uses both pages: rules, the refusal of managers on default, the
// kept keys after removing managers or a rule, default_deny, and every
// group's page; the workers page shows admins the peer address.
func TestUIWorkerGroups_Admin(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	admin := uiCookie(t, st, "admin", true)
	plain := uiCookie(t, st, "plain", false)
	storeKey(t, st, "wk_g", []string{"g"})
	seedWorker(t, st, "wrk_peer", "p-node", "g", model.WorkerStateOnline, time.Now().UTC(), "", "192.0.2.31")

	w := uiDo(t, srv, "GET", "/admin/worker-groups", nil, admin)
	wantStatus(t, "GET /admin/worker-groups", w, http.StatusOK)
	for _, want := range []string{`href="/worker-groups/g"`, `href="/worker-groups/h"`, "mgr", "1 / 1", navLink} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("admin page lacks %q", want)
		}
	}
	if w := uiDo(t, srv, "GET", "/admin/worker-groups?edit=g", nil, admin); !strings.Contains(w.Body.String(), `value="g"`) ||
		!strings.Contains(w.Body.String(), ">mgr</textarea>") {
		t.Errorf("?edit=g must pre-fill the form: %s", w.Body.String())
	}
	for _, path := range []string{"/worker-groups", "/worker-groups/g", "/worker-groups/h"} {
		wantStatus(t, "GET "+path, uiDo(t, srv, "GET", path, nil, admin), http.StatusOK)
	}

	// Create.
	wantRedirect(t, "create gpu", uiDo(t, srv, "POST", "/admin/worker-groups",
		url.Values{"group": {"gpu"}, "users": {"alice\r\nbob\r\n"}, "managers": {"lead"}, "admins": {"1"}}, admin),
		"/admin/worker-groups?done=rule-saved")
	if !policy.Allowed("bob", "gpu") || !policy.IsManager("lead", "gpu") || policy.Allowed("carol", "gpu") {
		t.Error("the new rule must be in force")
	}

	// Managers on default: the server's message.
	w = uiDo(t, srv, "POST", "/admin/worker-groups", url.Values{"group": {"default"}, "managers": {"mgr"}}, admin)
	wantStatus(t, "managers on default", w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "cannot have managers") {
		t.Errorf("want the server's message: %s", w.Body.String())
	}

	// Removing g's manager shows the key that stays valid.
	w = uiDo(t, srv, "POST", "/admin/worker-groups", url.Values{"group": {"g"}, "users": {"alice"}, "managers": {""}}, admin)
	wantStatus(t, "remove g's manager", w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `id="kept-keys"`) || !strings.Contains(w.Body.String(), "wk_g") {
		t.Errorf("the page must list the kept key wk_g: %s", w.Body.String())
	}
	if policy.IsManager("mgr", "g") {
		t.Error("mgr must no longer manage g")
	}

	// default_deny.
	wantRedirect(t, "default_deny on", uiDo(t, srv, "POST", "/admin/worker-groups/default-deny", url.Values{"default_deny": {"true"}}, admin),
		"/admin/worker-groups?done=default-deny-on")
	if !policy.DefaultDeny() {
		t.Error("default_deny must be in force")
	}
	wantStatus(t, "default_deny garbage", uiDo(t, srv, "POST", "/admin/worker-groups/default-deny", url.Values{"default_deny": {"yes"}}, admin), http.StatusBadRequest)

	// Delete: h has no keys and no managers (redirect), g has a key (shown).
	wantRedirect(t, "delete h", uiDo(t, srv, "POST", "/admin/worker-groups/delete", url.Values{"group": {"h"}}, admin), "/admin/worker-groups?done=rule-deleted")
	w = uiDo(t, srv, "POST", "/admin/worker-groups/delete", url.Values{"group": {"g"}}, admin)
	wantStatus(t, "delete g", w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `id="kept-keys"`) || !strings.Contains(w.Body.String(), "wk_g") {
		t.Errorf("deleting g must list its kept key: %s", w.Body.String())
	}
	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 1 || rules[0].Group != "gpu" {
		t.Errorf("rules = %+v, %v; want only gpu", rules, err)
	}
	wantStatus(t, "delete missing", uiDo(t, srv, "POST", "/admin/worker-groups/delete", url.Values{"group": {"h"}}, admin), http.StatusNotFound)

	// Peer address column: admins only.
	if w := uiDo(t, srv, "GET", "/workers", nil, admin); !strings.Contains(w.Body.String(), "Peer address") || !strings.Contains(w.Body.String(), "192.0.2.31") {
		t.Error("the workers page must show admins the peer address")
	}
	if w := uiDo(t, srv, "GET", "/workers", nil, plain); strings.Contains(w.Body.String(), "Peer address") || strings.Contains(w.Body.String(), "192.0.2.31") {
		t.Error("the workers page shows a non-admin the peer address")
	}
}

// Every state-changing route of the pages needs the UI session, like the
// other admin forms: without the cookie it is redirected to the login page
// and changes nothing.
func TestUIWorkerGroups_WritesNeedSession(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	storeKey(t, st, "wk_g", []string{"g"})
	seedWorker(t, st, "wrk_off", "off-node", "g", model.WorkerStateOffline, time.Now().UTC().Add(-time.Hour), "", "")
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/admin/worker-groups", url.Values{"group": {"g"}, "users": {"mallory"}}},
		{"/admin/worker-groups/delete", url.Values{"group": {"g"}}},
		{"/admin/worker-groups/default-deny", url.Values{"default_deny": {"true"}}},
		{"/worker-groups/g/users", url.Values{"username": {"mallory"}}},
		{"/worker-groups/g/users/remove", url.Values{"username": {"alice"}}},
		{"/worker-groups/g/keys", url.Values{"label": {"x"}}},
		{"/worker-groups/g/keys/revoke", url.Values{"id": {"wk_g"}}},
		{"/worker-groups/g/workers/remove", url.Values{"id": {"wrk_off"}}},
	} {
		wantRedirect(t, "POST "+c.path+" without a session", uiDo(t, srv, "POST", c.path, c.form, nil), "/login")
		// A session id that is not one (a forged cookie) is no better.
		forged := &http.Cookie{Name: ui.SessionCookieName, Value: "sess_forged"}
		wantRedirect(t, "POST "+c.path+" with a forged session", uiDo(t, srv, "POST", c.path, c.form, forged), "/login")
	}

	rules, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil || len(rules) != 2 || !reflect.DeepEqual(rules[0].Users, []string{"alice"}) || !reflect.DeepEqual(rules[0].Managers, []string{"mgr"}) {
		t.Errorf("rules = %+v, %v; want unchanged", rules, err)
	}
	if policy.Allowed("mallory", "g") || policy.DefaultDeny() {
		t.Error("the live policy changed")
	}
	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != len(keysBefore) {
		t.Errorf("%d keys, want %d", len(keys), len(keysBefore))
	}
	if wk, _ := st.GetWorker(ctx, "wrk_off"); wk == nil {
		t.Error("the worker was removed")
	}
}

// The REST routes and the GroupAdmin methods are one implementation: for one
// operation of each answer kind they give the same status, code and message
// (and, for a success, the same rule).
func TestGroupAdmin_RESTAndMethodsAgree(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	mgr := ui.GroupActor{Username: "mgr"}

	same := func(name string, w *httptest.ResponseRecorder, env envelope, err error, kind ui.GroupErrorKind) {
		t.Helper()
		ge := ui.AsGroupError(err)
		if ge == nil {
			t.Fatalf("%s: the method succeeded; REST answered %d %s", name, w.Code, w.Body.String())
		}
		if ge.Kind != kind {
			t.Errorf("%s: kind %q, want %q", name, ge.Kind, kind)
		}
		if env.Error == nil || w.Code != ge.HTTPStatus() || env.Error.Code != groupErrorCode(ge.Kind) || env.Error.Message != ge.Message {
			t.Errorf("%s: REST %d %+v, method %d %s %q", name, w.Code, env.Error, ge.HTTPStatus(), groupErrorCode(ge.Kind), ge.Message)
		}
	}

	// Success.
	w, env := mgDo(t, srv, "GET", managedGroupsPath+"/g", "", mgrToken)
	rule, err := srv.GetRule(ctx, mgr, "g")
	if err != nil || w.Code != http.StatusOK {
		t.Fatalf("GetRule: %v; REST %d", err, w.Code)
	}
	if want, _ := json.Marshal(rule); string(env.Data) != string(want) {
		t.Errorf("REST rule %s, method rule %s", env.Data, want)
	}

	// Conflict (worker auth is not on yet).
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/g/keys", `{"label":"n"}`, mgrToken)
	_, err = srv.CreateGroupKey(ctx, mgr, "g", ui.GroupKeyInput{Label: "n"})
	same("conflict", w, env, err, ui.GroupErrConflict)

	// Invalid.
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/g/users", `{"username":"anonymous"}`, mgrToken)
	_, err = srv.AddGroupUser(ctx, mgr, "g", "anonymous")
	same("invalid", w, env, err, ui.GroupErrInvalid)

	// Unauthorized.
	w, env = mgDo(t, srv, "GET", managedGroupsPath+"/g", "", "")
	_, err = srv.GetRule(ctx, ui.GroupActor{}, "g")
	same("unauthorized", w, env, err, ui.GroupErrUnauthorized)

	// Forbidden.
	w, env = mgDo(t, srv, "GET", managedGroupsPath+"/h", "", mgrToken)
	_, err = srv.GetRule(ctx, mgr, "h")
	same("forbidden", w, env, err, ui.GroupErrForbidden)
	w, _ = wgDo(t, srv, "DELETE", workerGroupsPath+"/g", "", mgrToken)
	_, err = srv.DeleteRule(ctx, mgr, "g")
	if ge := ui.AsGroupError(err); ge == nil || ge.Kind != ui.GroupErrForbidden || w.Code != http.StatusForbidden {
		t.Errorf("DeleteRule by a manager: %v; REST %d", err, w.Code)
	}

	// Not found.
	w, env = mgDo(t, srv, "DELETE", managedGroupsPath+"/g/keys/wk_missing", "", mgrToken)
	err = srv.RevokeGroupKey(ctx, mgr, "g", "wk_missing")
	same("not found", w, env, err, ui.GroupErrNotFound)

	// Unavailable: no live policy.
	noPolicy, _ := testServerWithStore(WithAdminConfig(srv.adminConfig))
	w, env = wgDo(t, noPolicy, "PUT", workerGroupsPath+"-default-deny", `{"default_deny":true}`, adminAuthToken)
	err = noPolicy.SetDefaultDeny(ctx, ui.GroupActor{Username: "admin", IsAdmin: true}, true)
	same("unavailable", w, env, err, ui.GroupErrUnavailable)

	// Internal: a stored row the policy cannot load breaks every reload; a
	// manager gets the generic message from both.
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "bad group", Users: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	w, env = mgDo(t, srv, "POST", managedGroupsPath+"/g/users", `{"username":"zed"}`, mgrToken)
	_, err = srv.AddGroupUser(ctx, mgr, "g", "zed")
	same("internal", w, env, err, ui.GroupErrInternal)
	if ge := ui.AsGroupError(err); ge == nil || ge.Message != internalErrorMessage {
		t.Errorf("a manager's internal error = %v, want %q", err, internalErrorMessage)
	}
}

// Admin status on the UI pages is the REST API's rule applied on every
// request (a configured admin, or stored role admin), not the role recorded
// in the session at login: a demoted admin is refused at once and changes
// nothing; a promoted user gains access without logging in again; a
// configured admin is an admin whatever the session says.
func TestUIWorkerGroups_AdminStatusIsLive(t *testing.T) {
	srv, st, policy := newManagedGroupServer(t)
	setUserRole(t, st, "boss", model.RoleAdmin)
	boss := uiCookie(t, st, "boss", true)

	wantStatus(t, "admin GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, boss), http.StatusOK)
	wantStatus(t, "admin GET /worker-groups/h", uiDo(t, srv, "GET", "/worker-groups/h", nil, boss), http.StatusOK)

	// Demoted while the session lives: the stored role is user and boss is
	// not a configured admin.
	setUserRole(t, st, "boss", model.RoleUser)
	rulesBefore := listRules(t, st)

	wantStatus(t, "demoted GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, boss), http.StatusForbidden)
	wantStatus(t, "demoted GET /admin/worker-keys", uiDo(t, srv, "GET", "/admin/worker-keys", nil, boss), http.StatusForbidden)
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/admin/worker-groups", url.Values{"group": {"gpu"}, "users": {"boss"}}},
		{"/admin/worker-groups", url.Values{"group": {"h"}, "users": {"boss"}}},
		{"/admin/worker-groups/delete", url.Values{"group": {"g"}}},
		{"/admin/worker-groups/default-deny", url.Values{"default_deny": {"true"}}},
	} {
		wantStatus(t, "demoted POST "+c.path, uiDo(t, srv, "POST", c.path, c.form, boss), http.StatusForbidden)
	}
	// Not an admin for the group operations either (boss manages nothing).
	wantStatus(t, "demoted GET /worker-groups", uiDo(t, srv, "GET", "/worker-groups", nil, boss), http.StatusForbidden)
	wantStatus(t, "demoted GET /worker-groups/h", uiDo(t, srv, "GET", "/worker-groups/h", nil, boss), http.StatusForbidden)
	wantStatus(t, "demoted add user to h", uiDo(t, srv, "POST", "/worker-groups/h/users", url.Values{"username": {"boss"}}, boss), http.StatusForbidden)

	if rules := listRules(t, st); !reflect.DeepEqual(rules, rulesBefore) {
		t.Errorf("rules = %+v, want unchanged %+v", rules, rulesBefore)
	}
	if policy.DefaultDeny() || policy.Allowed("boss", "h") {
		t.Error("a demoted admin changed the live policy")
	}

	// Promoted after login (stored role admin): access on the next request,
	// with the same session.
	plain := uiCookie(t, st, "plain", false)
	wantStatus(t, "plain GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, plain), http.StatusForbidden)
	setUserRole(t, st, "plain", model.RoleAdmin)
	wantStatus(t, "promoted GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, plain), http.StatusOK)
	wantStatus(t, "promoted GET /worker-groups/h", uiDo(t, srv, "GET", "/worker-groups/h", nil, plain), http.StatusOK)

	// A configured admin whose session was recorded as a plain user.
	cfgAdmin := uiCookie(t, st, "admin", false)
	wantStatus(t, "configured admin GET /admin/worker-groups", uiDo(t, srv, "GET", "/admin/worker-groups", nil, cfgAdmin), http.StatusOK)
}

// invalidRuleHint ends the admin page's error when a stored rule is invalid.
const invalidRuleHint = `Remove the invalid rule with "gowe admin worker-groups delete <group>".`

// A stored rule the policy refuses turns the admin page into an error page
// whose message ends with how to remove the rule; a reload failure of
// another kind does not carry the hint.
func TestUIWorkerGroups_InvalidRuleHint(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	admin := uiCookie(t, st, "admin", true)
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "bad group", Users: []string{"x"}}); err != nil {
		t.Fatal(err)
	}

	w := uiDo(t, srv, "GET", "/admin/worker-groups", nil, admin)
	wantStatus(t, "GET /admin/worker-groups with an invalid rule", w, http.StatusInternalServerError)
	body := html.UnescapeString(w.Body.String())
	if !strings.Contains(body, invalidRuleHint) || !strings.Contains(body, `"bad group"`) {
		t.Errorf("the error page must name the rule and end with %q: %s", invalidRuleHint, body)
	}
	_, err := srv.ListRules(ctx, ui.GroupActor{Username: "admin", IsAdmin: true})
	if ge := ui.AsGroupError(err); ge == nil || !strings.HasSuffix(ge.Message, invalidRuleHint) {
		t.Errorf("admin listing error = %v, want it to end with %q", err, invalidRuleHint)
	}

	// Another cause (an invalid default_deny value): no hint.
	if _, err := st.DeleteWorkerGroupPolicy(ctx, "bad group"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetServerSetting(ctx, groupacl.DefaultDenyKey, "maybe", "test"); err != nil {
		t.Fatal(err)
	}
	_, err = srv.ListRules(ctx, ui.GroupActor{Username: "admin", IsAdmin: true})
	if ge := ui.AsGroupError(err); ge == nil || ge.Kind != ui.GroupErrInternal || strings.Contains(ge.Message, "Remove the invalid rule") {
		t.Errorf("default_deny error = %v, want an internal error without the rule hint", err)
	}
}

// The admin-only GroupAdmin operations refuse, when called directly (not
// behind AdminMiddleware), every actor who is not a server admin, and the
// zero actor, and change nothing.
func TestGroupAdmin_AdminOnlyMethodsRefuseNonAdmins(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	storeKey(t, st, "wk_g", []string{"g"})
	rulesBefore := listRules(t, st)
	denyBefore, denyFound, err := st.GetServerSetting(ctx, groupacl.DefaultDenyKey)
	if err != nil {
		t.Fatal(err)
	}

	ops := []struct {
		name string
		call func(ui.GroupActor) error
	}{
		{"PutRule existing", func(a ui.GroupActor) error {
			_, err := srv.PutRule(ctx, a, "g", ui.PutRuleInput{Users: []string{"mallory"}, Managers: []string{"mallory"}})
			return err
		}},
		{"PutRule new", func(a ui.GroupActor) error {
			_, err := srv.PutRule(ctx, a, "evil", ui.PutRuleInput{Users: []string{"mallory"}})
			return err
		}},
		{"SetDefaultDeny", func(a ui.GroupActor) error { return srv.SetDefaultDeny(ctx, a, true) }},
		{"DeleteRule", func(a ui.GroupActor) error {
			_, err := srv.DeleteRule(ctx, a, "g")
			return err
		}},
		{"listAllRules", func(a ui.GroupActor) error {
			_, e := srv.listAllRules(ctx, a)
			return asError(e)
		}},
	}
	actors := []struct {
		name  string
		actor ui.GroupActor
	}{
		{"manager of g", ui.GroupActor{Username: "mgr"}},
		{"plain user", ui.GroupActor{Username: "plain"}},
		{"zero actor", ui.GroupActor{}},
	}
	for _, op := range ops {
		for _, a := range actors {
			t.Run(op.name+"/"+a.name, func(t *testing.T) {
				ge := ui.AsGroupError(op.call(a.actor))
				if ge == nil || ge.Kind != ui.GroupErrForbidden {
					t.Errorf("error = %v, want %s", ge, ui.GroupErrForbidden)
				}
			})
		}
	}

	// ListRules (the entry point of the listing) never gives a non-admin
	// the full listing: the zero actor is unauthorized, a manager gets the
	// rules of the groups they manage, without default_deny.
	if _, err := srv.ListRules(ctx, ui.GroupActor{}); ui.AsGroupError(err) == nil || ui.AsGroupError(err).Kind != ui.GroupErrUnauthorized {
		t.Errorf("ListRules for the zero actor: %v, want %s", err, ui.GroupErrUnauthorized)
	}
	list, err := srv.ListRules(ctx, ui.GroupActor{Username: "mgr"})
	if err != nil || list.DefaultDenyKnown || len(list.Rules) != 1 || list.Rules[0].Group != "g" {
		t.Errorf("ListRules for a manager = %+v, %v; want g only, default_deny unknown", list, err)
	}

	if rules := listRules(t, st); !reflect.DeepEqual(rules, rulesBefore) {
		t.Errorf("rules = %+v, want unchanged %+v", rules, rulesBefore)
	}
	if deny, found, _ := st.GetServerSetting(ctx, groupacl.DefaultDenyKey); deny != denyBefore || found != denyFound {
		t.Errorf("default_deny setting = %q (found %v), want %q (found %v)", deny, found, denyBefore, denyFound)
	}
	if policy.DefaultDeny() || policy.IsManager("mallory", "g") || policy.Allowed("mallory", "g") {
		t.Error("the live policy changed")
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_g"); k == nil {
		t.Error("the group key is gone")
	}
}

// A manager of g is refused on h's key and user forms (issue, revoke,
// remove a user), and nothing changes.
func TestUIWorkerGroups_ManagerOfGCannotChangeH(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`) // worker auth on: a refusal is not the 409
	storeKey(t, st, "wk_h", []string{"h"})
	mgr := uiCookie(t, st, "mgr", false)
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rulesBefore := listRules(t, st)

	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/worker-groups/h/keys", url.Values{"label": {"x"}}},
		{"/worker-groups/h/keys/revoke", url.Values{"id": {"wk_h"}}},
		{"/worker-groups/h/users/remove", url.Values{"username": {"bob"}}},
	} {
		wantStatus(t, "manager of g: POST "+c.path, uiDo(t, srv, "POST", c.path, c.form, mgr), http.StatusForbidden)
	}

	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != len(keysBefore) {
		t.Errorf("%d keys, want %d", len(keys), len(keysBefore))
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_h"); k == nil {
		t.Error("h's key was revoked by the manager of g")
	}
	if rules := listRules(t, st); !reflect.DeepEqual(rules, rulesBefore) {
		t.Errorf("rules = %+v, want unchanged %+v", rules, rulesBefore)
	}
	if !policy.Allowed("bob", "h") {
		t.Error("the manager of g removed a user of h")
	}
}

// A GET on a POST-only route of the pages changes nothing: it is not routed
// to the form handler (405, or 404).
func TestUIWorkerGroups_GetOnPostRoutes(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	storeKey(t, st, "wk_g", []string{"g"})
	seedWorker(t, st, "wrk_off", "off-node", "g", model.WorkerStateOffline, time.Now().UTC().Add(-time.Hour), "", "")
	admin := uiCookie(t, st, "admin", true)
	mgr := uiCookie(t, st, "mgr", false)
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rulesBefore := listRules(t, st)

	paths := []string{
		"/worker-groups/g/users?username=mallory",
		"/worker-groups/g/users/remove?username=alice",
		"/worker-groups/g/keys?label=x",
		"/worker-groups/g/keys/revoke?id=wk_g",
		"/worker-groups/g/workers/remove?id=wrk_off",
		"/admin/worker-groups/delete?group=g",
		"/admin/worker-groups/default-deny?default_deny=true",
	}
	for _, path := range paths {
		cookies := []*http.Cookie{admin}
		if strings.HasPrefix(path, "/worker-groups/") {
			cookies = append(cookies, mgr)
		}
		for _, c := range cookies {
			w := uiDo(t, srv, "GET", path, nil, c)
			if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 405 or 404: %s", path, w.Code, w.Body.String())
			}
		}
	}

	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != len(keysBefore) {
		t.Errorf("%d keys, want %d", len(keys), len(keysBefore))
	}
	if rules := listRules(t, st); !reflect.DeepEqual(rules, rulesBefore) {
		t.Errorf("rules = %+v, want unchanged %+v", rules, rulesBefore)
	}
	if wk, _ := st.GetWorker(ctx, "wrk_off"); wk == nil {
		t.Error("the worker was removed")
	}
	if policy.DefaultDeny() || policy.Allowed("mallory", "g") {
		t.Error("the live policy changed")
	}
}

// A manager removed in the store after the page was loaded, while the live
// policy still names them (not reloaded yet), is refused on every form: the
// write reloads the policy and checks the right again before writing.
func TestUIWorkerGroups_ManagerRemovedBeforePost(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	adminCreateKey(t, srv, `["default"]`)
	storeKey(t, st, "wk_g", []string{"g"})
	seedWorker(t, st, "wrk_off", "off-node", "g", model.WorkerStateOffline, time.Now().UTC().Add(-time.Hour), "", "")
	mgr := uiCookie(t, st, "mgr", false)
	keysBefore, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storeG := func(managers []string) {
		t.Helper()
		if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "g", Users: []string{"alice"}, Managers: managers, UpdatedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/worker-groups/g/users", url.Values{"username": {"carol"}}},
		{"/worker-groups/g/users/remove", url.Values{"username": {"alice"}}},
		{"/worker-groups/g/keys", url.Values{"label": {"late"}}},
		{"/worker-groups/g/keys/revoke", url.Values{"id": {"wk_g"}}},
		{"/worker-groups/g/workers/remove", url.Values{"id": {"wrk_off"}}},
	} {
		// mgr manages g (stored and live) and loads the page...
		storeG([]string{"mgr"})
		if err := policy.Reload(ctx, st); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, "GET /worker-groups/g", uiDo(t, srv, "GET", "/worker-groups/g", nil, mgr), http.StatusOK)
		// ...then an admin removes mgr in the store; the live policy still
		// names mgr.
		storeG([]string{})
		if !policy.IsManager("mgr", "g") {
			t.Fatal("precondition: the live policy must still name mgr")
		}
		wantStatus(t, "removed manager: POST "+c.path, uiDo(t, srv, "POST", c.path, c.form, mgr), http.StatusForbidden)
	}

	var rule *model.WorkerGroupPolicy
	for _, r := range listRules(t, st) {
		if r.Group == "g" {
			rule = r
		}
	}
	if rule == nil || !reflect.DeepEqual(rule.Users, []string{"alice"}) {
		t.Errorf("g = %+v; want users [alice]", rule)
	}
	if keys, _ := st.ListWorkerKeys(ctx); len(keys) != len(keysBefore) {
		t.Errorf("%d keys, want %d", len(keys), len(keysBefore))
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_g"); k == nil {
		t.Error("the key was revoked")
	}
	if wk, _ := st.GetWorker(ctx, "wrk_off"); wk == nil {
		t.Error("the worker was removed")
	}
	if policy.IsManager("mgr", "g") || policy.Allowed("carol", "g") {
		t.Error("the live policy must reflect the store after the refused write")
	}
}
