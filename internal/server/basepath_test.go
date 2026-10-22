package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// chdirRepoRoot temporarily changes the working directory to the repository
// root so the UI's relative "ui/assets" static handler (registered in
// routes()) resolves during a test, restoring the original directory on
// cleanup. Tests running from `go test ./...` start with cwd set to this
// package's own directory (internal/server).
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..")
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// TestRootModeLoginRedirect is a regression check: with no base path
// configured, GET / still redirects to /login exactly as before #250.
func TestRootModeLoginRedirect(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code < 300 || rec.Code >= 400 {
		t.Fatalf("GET / status = %d, want a redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("GET / Location = %q, want /login", loc)
	}
}

// TestBasePathDualMount exercises the full contract of the base-path
// wrapper: root paths keep working unchanged, the prefix is dual-mounted,
// boundary stripping is segment-aware (not a bare string prefix match), and
// the bare mount point redirects to its trailing-slash form.
func TestBasePathDualMount(t *testing.T) {
	chdirRepoRoot(t)
	srv := testServer(WithBasePath("/x/y"))
	h := srv.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("bare mount point redirects to trailing slash", func(t *testing.T) {
		rec := get("/x/y")
		if rec.Code < 300 || rec.Code >= 400 {
			t.Fatalf("GET /x/y status = %d, want a redirect", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/x/y/" {
			t.Fatalf("GET /x/y Location = %q, want /x/y/", loc)
		}
	})

	t.Run("mount point with trailing slash redirects to login under prefix", func(t *testing.T) {
		rec := get("/x/y/")
		if rec.Code < 300 || rec.Code >= 400 {
			t.Fatalf("GET /x/y/ status = %d, want a redirect, body=%s", rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); loc != "/x/y/login" {
			t.Fatalf("GET /x/y/ Location = %q, want /x/y/login", loc)
		}
	})

	t.Run("segment boundary: lookalike path is not stripped and 404s", func(t *testing.T) {
		rec := get("/x/yfoo")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET /x/yfoo status = %d, want 404, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("static asset served under prefix", func(t *testing.T) {
		rec := get("/x/y/static/js/app.js")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /x/y/static/js/app.js status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("api reachable under prefix", func(t *testing.T) {
		rec := get("/x/y/api/v1/health")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /x/y/api/v1/health status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("api still reachable unprefixed (dual mount)", func(t *testing.T) {
		rec := get("/api/v1/health")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/health status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("static asset still reachable unprefixed (dual mount)", func(t *testing.T) {
		rec := get("/static/js/app.js")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /static/js/app.js status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})
}

// TestBasePathAdminOutputsSelfCallThroughPrefix proves adminAPIBaseURL (the
// admin-outputs page's server-calls-itself pattern, which derives this
// server's own address from http.LocalAddrContextKey rather than trusting
// the Host header) keeps working when the originating browser request
// arrived through the base-path-stripped mount. It must reach the real
// /api/v1/admin/... endpoint either way — under the dual mount those root
// API paths always answer, so the self-call is unaffected by which mount
// the outer page request came in on.
//
// This uses a real httptest.Server (not httptest.NewRecorder) specifically
// so http.LocalAddrContextKey is populated the same way it is in
// production, exercising the exact code path in admin_outputs.go rather
// than a stubbed apiBaseURL.
func TestBasePathAdminOutputsSelfCallThroughPrefix(t *testing.T) {
	srv, st := testServerWithStore(WithBasePath("/x/y"))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Seed an admin session directly in the store, the same way the ui
	// package's own tests do (ui.NewSessionManager / ui.SessionCookieName).
	sm := ui.NewSessionManager(st)
	sess, err := sm.CreateSession(context.Background(), "user_admin", "admin", string(model.RoleAdmin), "tok-admin", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/x/y/admin/outputs/verify",
		strings.NewReader("submission_id=sub-does-not-exist"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: ui.SessionCookieName, Value: sess.ID})

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /x/y/admin/outputs/verify: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the admin/outputs page always renders 200, surfacing errors inline)", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	body := string(bodyBytes)

	// The self-call must have actually round-tripped to the real
	// /api/v1/admin/... handler through the dual-mounted root path. This
	// test session carries a synthetic (non-BV-BRC-format) token, so the
	// real handler's own auth middleware rejects it — but that rejection is
	// exactly the proof: it's a structured "API error (HTTP ...)" response
	// decoded from a real envelope returned by the real handler, not a
	// "Call failed" CallError, which is what adminAPIBaseURL would produce
	// if it couldn't resolve this server's own address (the failure mode a
	// broken LocalAddrContextKey lookup under the prefixed mount would
	// cause — see admin_outputs.go's errNoLocalAddr).
	if strings.Contains(body, "cannot determine this server") {
		t.Fatalf("admin outputs self-call failed to resolve its own address through the prefixed mount; body=%s", body)
	}
	if strings.Contains(body, "Call failed:") {
		t.Fatalf("admin outputs self-call errored before reaching the real admin API; body=%s", body)
	}
	if !strings.Contains(body, "API error (HTTP") {
		t.Fatalf("admin outputs self-call did not round-trip to a real admin API response through the prefixed mount; body=%s", body)
	}
}
