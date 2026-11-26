package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// The "Worker groups" navigation link is shown to server admins and to the
// managers of a group in the live policy, to nobody else; the /workers page
// has a Peer address column for server admins only. (Who may use the worker
// group pages is tested in internal/server, against the real operations.)
func TestWorkerGroupsNavAndPeerColumn(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	now := time.Now().UTC()
	if err := st.CreateWorker(context.Background(), &model.Worker{
		ID: "wrk_off", Name: "off-node", Hostname: "off.example", Group: "g", State: model.WorkerStateOffline,
		Runtime: model.RuntimeNone, LastSeen: now.Add(-time.Hour), RegisteredAt: now.Add(-time.Hour), PeerAddr: "192.0.2.50",
	}); err != nil {
		t.Fatal(err)
	}
	policy := loadUIGroupPolicy(t, &model.WorkerGroupPolicy{Group: "g", Users: []string{"alice"}, Managers: []string{"mgr"}})
	u := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{GroupPolicy: policy, GroupAdmin: fakeGroupAdmin{}})
	r := chi.NewRouter()
	u.RegisterRoutes(r)

	const navLink = `href="/worker-groups"`
	for _, c := range []struct {
		name, user, role string
		nav, peer        bool
	}{
		{"plain user", "alice", string(model.RoleUser), false, false},
		{"manager", "mgr", string(model.RoleUser), true, false},
		{"admin", "boss", string(model.RoleAdmin), true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, cookie := newTestSession(t, u, c.user, c.role)
			req := httptest.NewRequest(http.MethodGet, "/workers", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if got := strings.Contains(body, navLink); got != c.nav {
				t.Errorf("nav link shown = %v, want %v", got, c.nav)
			}
			if got := strings.Contains(body, "Peer address"); got != c.peer {
				t.Errorf("Peer address column shown = %v, want %v", got, c.peer)
			}
			if got := strings.Contains(body, "192.0.2.50"); got != c.peer {
				t.Errorf("peer address shown = %v, want %v", got, c.peer)
			}
			// Hostname and last seen are shown for an offline worker too.
			if !strings.Contains(body, "off.example") || !strings.Contains(body, "1h ago") {
				t.Error("the offline worker's hostname or last seen is missing")
			}
		})
	}
}

// The worker group pages answer 503 when the UI has no GroupAdmin, and the
// navigation link to them is not shown, not even to a server admin.
func TestWorkerGroupPages_NoGroupAdmin(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	r := chi.NewRouter()
	u.RegisterRoutes(r)
	_, cookie := newTestSession(t, u, "boss", string(model.RoleAdmin))
	for _, path := range []string{"/worker-groups", "/worker-groups/g", "/admin/worker-groups"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `href="/worker-groups"`) {
		t.Errorf("workers page without a GroupAdmin: status %d, nav link shown = %v", rec.Code, strings.Contains(rec.Body.String(), `href="/worker-groups"`))
	}
}

// groupPageServe sends a request through a UI built with groups (and an
// optional base path) as username with role, and returns the answer.
func groupPageServe(t *testing.T, groups GroupAdmin, basePath, username, role, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	st := setupTestStore(t)
	t.Cleanup(func() { st.Close() })
	u := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{GroupAdmin: groups, BasePath: basePath})
	r := chi.NewRouter()
	u.RegisterRoutes(r)
	_, cookie := newTestSession(t, u, username, role)
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// readFailsAfterCreate is fakeGroupAdmin whose read named by failRead
// ("rule", "keys" or "workers") fails once a key was created.
type readFailsAfterCreate struct {
	fakeGroupAdmin
	created  *bool
	failRead string
}

var errReadAfterCreate = &GroupError{Kind: GroupErrInternal, Message: "database is locked"}

func (g readFailsAfterCreate) CreateGroupKey(ctx context.Context, actor GroupActor, group string, in GroupKeyInput) (*CreatedGroupKey, error) {
	*g.created = true
	return g.fakeGroupAdmin.CreateGroupKey(ctx, actor, group, in)
}

func (g readFailsAfterCreate) GetRule(ctx context.Context, actor GroupActor, group string) (*model.WorkerGroupPolicy, error) {
	if *g.created && g.failRead == "rule" {
		return nil, errReadAfterCreate
	}
	return g.fakeGroupAdmin.GetRule(ctx, actor, group)
}

func (g readFailsAfterCreate) ListGroupKeys(ctx context.Context, actor GroupActor, group string) ([]*model.WorkerKey, error) {
	if *g.created && g.failRead == "keys" {
		return nil, errReadAfterCreate
	}
	return g.fakeGroupAdmin.ListGroupKeys(ctx, actor, group)
}

func (g readFailsAfterCreate) ListGroupWorkers(ctx context.Context, actor GroupActor, group string) ([]GroupWorker, error) {
	if *g.created && g.failRead == "workers" {
		return nil, errReadAfterCreate
	}
	return g.fakeGroupAdmin.ListGroupWorkers(ctx, actor, group)
}

// When a read that follows a successful key creation fails, the raw key is
// still shown (once, never cached) on a minimal page with the error as a
// warning.
func TestWorkerGroupKeyCreate_ReadFailsAfterCreate(t *testing.T) {
	const raw = "gwk_fake-raw-secret"
	for _, failRead := range []string{"rule", "keys", "workers"} {
		t.Run(failRead, func(t *testing.T) {
			groups := readFailsAfterCreate{created: new(bool), failRead: failRead}
			rec := groupPageServe(t, groups, basePathUnderTest, "mgr", string(model.RoleUser),
				http.MethodPost, "/worker-groups/g/keys", url.Values{"label": {"n"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			body := rec.Body.String()
			if n := strings.Count(body, raw); n != 1 {
				t.Errorf("the raw key appears %d times, want once: %s", n, body)
			}
			for _, want := range []string{`id="raw-key"`, "database is locked", "The key was issued", "wk_fake", "reloading submits the form again"} {
				if !strings.Contains(body, want) {
					t.Errorf("the page lacks %q", want)
				}
			}
			scanForUnprefixedURLs(t, "worker_groups/key_created", body, basePathUnderTest)
		})
	}
}

// listRulesUnknownDeny is fakeGroupAdmin answering ListRules without
// default_deny, as the server does for a non-admin.
type listRulesUnknownDeny struct{ fakeGroupAdmin }

func (listRulesUnknownDeny) ListRules(context.Context, GroupActor) (*RuleList, error) {
	return &RuleList{Rules: []*model.WorkerGroupPolicy{fakeGroupRule()}}, nil
}

// The admin page refuses (403) an actor who is not a server admin now (a
// session whose role was not derived for the request is not one, whatever
// it records), and a listing without default_deny.
func TestAdminWorkerGroups_RefusesNonAdminListing(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{GroupAdmin: fakeGroupAdmin{}})
	sess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}
	rec := httptest.NewRecorder()
	u.HandleAdminWorkerGroups(rec, withSession(httptest.NewRequest(http.MethodGet, "/admin/worker-groups", nil), sess))
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), `id="rule-form"`) {
		t.Errorf("handler without the live role: status %d, want the 403 page", rec.Code)
	}

	rec = groupPageServe(t, listRulesUnknownDeny{}, "", "boss", string(model.RoleAdmin), http.MethodGet, "/admin/worker-groups", nil)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), `id="rule-form"`) {
		t.Errorf("listing without default_deny: status %d, want the 403 page", rec.Code)
	}
}

// The texts of the forms: when a key expires, that reloading the answer to
// the key form issues another key, and that saving a rule replaces both
// lists.
func TestWorkerGroupFormTexts(t *testing.T) {
	const expiry = "The key expires at 00:00 UTC at the start of the chosen day."
	const reload = "reloading submits the form again and issues another key"
	for _, c := range []struct {
		name, method, path string
		form               url.Values
		want               []string
	}{
		{"group page", http.MethodGet, "/worker-groups/g", nil, []string{expiry}},
		{"group key created", http.MethodPost, "/worker-groups/g/keys", url.Values{"label": {"n"}}, []string{reload}},
		{"admin keys page", http.MethodGet, "/admin/worker-keys", nil, []string{expiry}},
		{"admin key created", http.MethodPost, "/admin/worker-keys", url.Values{"label": {"n"}}, []string{reload}},
		{"admin rule form", http.MethodGet, "/admin/worker-groups", nil, []string{"BOTH lists", "Users a manager added or removed since this page was loaded are overwritten"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := groupPageServe(t, fakeGroupAdmin{}, "", "boss", string(model.RoleAdmin), c.method, c.path, c.form)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			for _, want := range c.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("the page lacks %q", want)
				}
			}
		})
	}
}
