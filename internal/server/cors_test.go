package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/store"
)

// newCORSTestServer builds a Server with the given --cors-origins value
// (nil/empty reproduces the flag being unset, i.e. CORS disabled).
func newCORSTestServer(t *testing.T, origins []string) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	cfg := config.DefaultServerConfig()
	cfg.CORSOrigins = origins
	return New(cfg, st, nil, logger, WithAnonymousConfig(&AnonymousConfig{Enabled: true}))
}

const allowedOrigin = "https://app.example.com"
const unlistedOrigin = "https://evil.example.com"

func TestCORS_PreflightAllowedOrigin(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/health", nil)
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("Access-Control-Allow-Methods missing")
	} else {
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			if !containsToken(got, m) {
				t.Errorf("Access-Control-Allow-Methods = %q, missing %s", got, m)
			}
		}
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "Authorization, Content-Type")
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Errorf("Access-Control-Max-Age missing")
	}
}

// TestCORS_PreflightAuthenticatedRoute is the regression test for the bug
// that motivated this feature: OPTIONS against a route sitting behind
// apiAuthMiddleware (here /api/v1/workflows/) must still preflight
// successfully — the CORS middleware must win the race and short-circuit
// before auth ever runs, not fall through to a 401/405 because there's no
// Authorization header on a preflight request. This is deliberately built
// without WithAnonymousConfig's blanket auth bypass so a regression in
// middleware ordering (CORS mounted after, or inside, the auth group) would
// actually be caught here.
func TestCORS_PreflightAuthenticatedRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	cfg := config.DefaultServerConfig()
	cfg.CORSOrigins = []string{allowedOrigin}
	srv := New(cfg, st, nil, logger) // no anonymous config: /workflows requires real auth

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/workflows/", nil)
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (preflight must precede auth); body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("Access-Control-Allow-Methods missing")
	}
}

func TestCORS_PreflightUnlistedOrigin(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/health", nil)
	req.Header.Set("Origin", unlistedOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
	assertNoCORSHeaders(t, w)
}

func TestCORS_DisabledFlag(t *testing.T) {
	// Flag unset entirely: origins is nil, reproducing today's behavior even
	// when a browser sends an Origin header that would otherwise match.
	srv := newCORSTestServer(t, nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/health", nil)
	req.Header.Set("Origin", allowedOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", w.Code, w.Body.String())
	}
	assertNoCORSHeaders(t, w)
}

func TestCORS_NormalGETListedOrigin(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := w.Header().Values("Vary"); !containsExact(got, "Origin") {
		t.Errorf("Vary = %v, want to include %q", got, "Origin")
	}
}

func TestCORS_NormalGETNoOriginHeader(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	assertNoCORSHeaders(t, w)
}

func TestCORS_NormalGETUnlistedOrigin(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Origin", unlistedOrigin)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	assertNoCORSHeaders(t, w)
}

// TestCORS_UIUntouched confirms CORS is scoped to /api/v1 only: the UI
// (mounted at "/") never receives CORS headers even when the flag is set
// and the request carries a listed Origin.
func TestCORS_UIUntouched(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assertNoCORSHeaders(t, w)
}

// TestCORS_MetricsUntouched confirms /metrics is not part of this router at
// all (it is served by a separate, unauthenticated http.Server bound to
// --metrics-addr in cmd/server/main.go) — CORS on the API router cannot leak
// onto it, and this router doesn't recognize the path either.
func TestCORS_MetricsUntouched(t *testing.T) {
	srv := newCORSTestServer(t, []string{allowedOrigin})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not part of the API router); body=%s", w.Code, w.Body.String())
	}
	assertNoCORSHeaders(t, w)
}

func assertNoCORSHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, h := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Max-Age",
	} {
		if v := w.Header().Get(h); v != "" {
			t.Errorf("%s = %q, want unset", h, v)
		}
	}
	if got := w.Header().Values("Vary"); containsExact(got, "Origin") {
		t.Errorf("Vary = %v, want no Origin entry", got)
	}
}

func containsToken(haystack, token string) bool {
	for i := 0; i+len(token) <= len(haystack); i++ {
		if haystack[i:i+len(token)] == token {
			// Ensure it's a whole token (bounded by ", " or string edges).
			before := i == 0 || haystack[i-1] == ' ' || haystack[i-1] == ','
			after := i+len(token) == len(haystack) || haystack[i+len(token)] == ',' || haystack[i+len(token)] == ' '
			if before && after {
				return true
			}
		}
	}
	return false
}

func containsExact(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
