package ui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// countingGroupAdmin is fakeGroupAdmin counting the users it was asked to
// add, to tell whether a form reached the group operation.
type countingGroupAdmin struct {
	fakeGroupAdmin
	added *int
}

func (g countingGroupAdmin) AddGroupUser(ctx context.Context, actor GroupActor, group, username string) (*model.WorkerGroupPolicy, error) {
	*g.added++
	return g.fakeGroupAdmin.AddGroupUser(ctx, actor, group, username)
}

// sameOriginFixture is a UI with its routes, an admin session, the log and
// the count of group users added.
type sameOriginFixture struct {
	u      *UI
	r      chi.Router
	cookie *http.Cookie
	logs   *bytes.Buffer
	added  *int
}

func newSameOriginFixture(t *testing.T, behindProxy bool) *sameOriginFixture {
	t.Helper()
	st := setupTestStore(t)
	t.Cleanup(func() { st.Close() })
	f := &sameOriginFixture{logs: &bytes.Buffer{}, added: new(int)}
	f.u = New(st, slog.New(slog.NewTextHandler(f.logs, nil)), Config{
		GroupAdmin:  countingGroupAdmin{added: f.added},
		BehindProxy: behindProxy,
	})
	f.r = chi.NewRouter()
	f.u.RegisterRoutes(f.r)
	_, f.cookie = newTestSession(t, f.u, "boss", string(model.RoleAdmin))
	return f
}

// do sends method path (a form body when form is not nil) with the admin
// cookie and headers. httptest requests have Host "example.com".
func (f *sameOriginFixture) do(method, path string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

func (f *sameOriginFixture) keyCount(t *testing.T) int {
	t.Helper()
	keys, err := f.u.store.ListWorkerKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

// State-changing UI requests that a browser sent from another site are
// refused with 403 and change nothing, on an existing admin form (worker
// keys) and on a worker group form; same-origin requests and requests
// without the browser headers pass; GET is never affected.
func TestSameOriginMiddleware(t *testing.T) {
	const (
		publicHost = "gowe.example.org"
		evil       = "https://evil.example"
	)
	cases := []struct {
		name        string
		behindProxy bool
		headers     map[string]string
		allowed     bool
	}{
		{"Sec-Fetch-Site cross-site", false, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"Sec-Fetch-Site same-site", false, map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"Origin of another host", false, map[string]string{"Origin": evil}, false},
		{"Origin of another port", false, map[string]string{"Origin": "http://example.com:8443"}, false},
		{"Origin null", false, map[string]string{"Origin": "null"}, false},
		{"Sec-Fetch-Site same-origin wins over a rewritten Host", false, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://" + publicHost}, true},
		{"X-Forwarded-Host without behind-proxy", false, map[string]string{"Origin": "https://" + publicHost, "X-Forwarded-Host": publicHost}, false},
		{"behind-proxy, Origin matches neither", true, map[string]string{"Origin": evil, "X-Forwarded-Host": publicHost}, false},
		{"matching Origin", false, map[string]string{"Origin": "http://example.com"}, true},
		{"Sec-Fetch-Site same-origin with matching Origin", false, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, true},
		{"Sec-Fetch-Site none", false, map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"behind-proxy, Origin matches X-Forwarded-Host", true, map[string]string{"Origin": "https://" + publicHost, "X-Forwarded-Host": publicHost + ", proxy.internal"}, true},
		{"behind-proxy, Origin matches Host", true, map[string]string{"Origin": "http://example.com"}, true},
		{"no headers (non-browser client)", false, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSameOriginFixture(t, c.behindProxy)

			// Existing admin form: issue a worker key.
			before := f.keyCount(t)
			f.logs.Reset()
			rec := f.do(http.MethodPost, "/admin/worker-keys", url.Values{"label": {"n"}}, c.headers)
			after := f.keyCount(t)
			if c.allowed {
				if rec.Code != http.StatusOK || after != before+1 {
					t.Errorf("worker key form: status %d, keys %d -> %d; want 200 and one key more: %s", rec.Code, before, after, rec.Body.String())
				}
			} else {
				if rec.Code != http.StatusForbidden || after != before {
					t.Errorf("worker key form: status %d, keys %d -> %d; want 403 and no change", rec.Code, before, after)
				}
				if !strings.Contains(rec.Body.String(), "cross-site request refused") {
					t.Errorf("403 body = %q", rec.Body.String())
				}
				logs := f.logs.String()
				if strings.Count(logs, "cross-site request refused") != 1 || !strings.Contains(logs, "level=WARN") ||
					!strings.Contains(logs, "path=/admin/worker-keys") {
					t.Errorf("want one WARN with the path, got: %s", logs)
				}
				if strings.Contains(logs, f.cookie.Value) || strings.Contains(logs, SessionCookieName) {
					t.Errorf("the log line carries the session cookie: %s", logs)
				}
			}

			// New form: add a user to a worker group.
			*f.added = 0
			rec = f.do(http.MethodPost, "/worker-groups/g/users", url.Values{"username": {"carol"}}, c.headers)
			if c.allowed {
				if rec.Code != http.StatusSeeOther || *f.added != 1 {
					t.Errorf("group form: status %d, added %d; want 303 and the user added", rec.Code, *f.added)
				}
			} else if rec.Code != http.StatusForbidden || *f.added != 0 {
				t.Errorf("group form: status %d, added %d; want 403 and nothing added", rec.Code, *f.added)
			}
		})
	}
}

// GET is never refused, whatever the headers say.
func TestSameOriginMiddleware_GetUnaffected(t *testing.T) {
	f := newSameOriginFixture(t, false)
	cross := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	for _, path := range []string{"/admin/worker-keys", "/worker-groups/g", "/workers"} {
		if rec := f.do(http.MethodGet, path, nil, cross); rec.Code != http.StatusOK {
			t.Errorf("GET %s with cross-site headers: status %d, want 200", path, rec.Code)
		}
	}
}

// What the browser sends for the existing htmx requests (same-origin XHR:
// Sec-Fetch-Site same-origin, a matching Origin, HX-Request) passes; the
// same DELETE sent cross-site is refused. The login form is outside the
// authenticated routes and keeps working with its same-origin headers.
func TestSameOriginMiddleware_HTMXAndLogin(t *testing.T) {
	f := newSameOriginFixture(t, false)
	if rec := f.do(http.MethodPost, "/admin/worker-keys", url.Values{"label": {"n"}}, nil); rec.Code != http.StatusOK {
		t.Fatalf("create key: %d", rec.Code)
	}
	keys, err := f.u.store.ListWorkerKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	path := "/admin/worker-keys/" + keys[0].ID

	if rec := f.do(http.MethodDelete, path, nil, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example", "HX-Request": "true"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site htmx DELETE: status %d, want 403", rec.Code)
	}
	if f.keyCount(t) != 1 {
		t.Fatal("the cross-site DELETE revoked the key")
	}
	if rec := f.do(http.MethodDelete, path, nil, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com", "HX-Request": "true"}); rec.Code != http.StatusOK {
		t.Errorf("same-origin htmx DELETE: status %d, want 200", rec.Code)
	}
	if f.keyCount(t) != 0 {
		t.Error("the same-origin DELETE did not revoke the key")
	}

	// Login POST (empty credentials: answered without contacting BV-BRC).
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?error=") {
		t.Errorf("login POST: status %d, Location %q; want the login form's own redirect", rec.Code, rec.Header().Get("Location"))
	}
}
