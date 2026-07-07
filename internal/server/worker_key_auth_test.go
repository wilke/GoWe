package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func testAuthStore(t *testing.T) store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Authenticator: static config ---

func TestAuthenticator_StaticKey(t *testing.T) {
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		"secret-abc": {ID: "node-a", Groups: []string{"esmfold"}},
	}}
	cfg.build()
	auth := NewWorkerKeyAuthenticator(cfg, nil, discardLogger())
	ctx := context.Background()

	if !auth.Enabled(ctx) {
		t.Fatal("Enabled() = false with static keys configured")
	}
	got := auth.Authenticate(ctx, "secret-abc")
	if got == nil {
		t.Fatal("Authenticate(valid static key) = nil")
	}
	if got.KeyID != "node-a" {
		t.Errorf("KeyID = %q, want node-a", got.KeyID)
	}
	if !got.CanJoinGroup("esmfold") || got.CanJoinGroup("other") {
		t.Errorf("group scoping wrong: %v", got.Groups)
	}
	if auth.Authenticate(ctx, "wrong") != nil {
		t.Errorf("Authenticate(wrong key) != nil")
	}
}

func TestAuthenticator_StaticDisabledAndExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		"disabled-key": {ID: "d", Groups: []string{"g"}, Disabled: true},
		"expired-key":  {ID: "e", Groups: []string{"g"}, ExpiresAt: &past},
	}}
	cfg.build()
	auth := NewWorkerKeyAuthenticator(cfg, nil, discardLogger())
	ctx := context.Background()

	if auth.Authenticate(ctx, "disabled-key") != nil {
		t.Errorf("disabled static key authenticated")
	}
	if auth.Authenticate(ctx, "expired-key") != nil {
		t.Errorf("expired static key authenticated")
	}
}

func TestWorkerKeyConfig_HashSentinel(t *testing.T) {
	// Operators can store the hash instead of the raw secret.
	raw := "super-secret"
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		hashSentinelPrefix + model.HashWorkerKey(raw): {ID: "h", Groups: []string{"g"}},
	}}
	cfg.build()
	if entry := cfg.ValidateKey(raw); entry == nil || entry.ID != "h" {
		t.Errorf("hash-sentinel key not resolved from raw input: %v", entry)
	}
}

// --- Authenticator: DB-backed keys ---

func TestAuthenticator_DBKeyLifecycle(t *testing.T) {
	st := testAuthStore(t)
	auth := NewWorkerKeyAuthenticator(nil, st, discardLogger())
	ctx := context.Background()

	// No keys anywhere => open access.
	if auth.Enabled(ctx) {
		t.Fatal("Enabled() = true with no keys configured")
	}

	raw, hash, prefix, _ := model.GenerateWorkerKey()
	key := &model.WorkerKey{
		ID: "wk_db1", Label: "db-node", KeyHash: hash, KeyPrefix: prefix,
		Groups: []string{"esmfold"}, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("CreateWorkerKey: %v", err)
	}

	// Now enforcement is on and the key authenticates.
	if !auth.Enabled(ctx) {
		t.Fatal("Enabled() = false after minting a DB key")
	}
	got := auth.Authenticate(ctx, raw)
	if got == nil || got.KeyID != "wk_db1" {
		t.Fatalf("Authenticate(db key) = %v, want wk_db1", got)
	}

	// Usage is recorded.
	stored, _ := st.GetWorkerKeyByID(ctx, "wk_db1")
	if stored.LastUsedAt == nil {
		t.Errorf("LastUsedAt not updated after auth")
	}

	// Disable => revoked.
	key.Disabled = true
	if err := st.UpdateWorkerKey(ctx, key); err != nil {
		t.Fatalf("UpdateWorkerKey: %v", err)
	}
	if auth.Authenticate(ctx, raw) != nil {
		t.Errorf("disabled DB key still authenticates")
	}

	// A leaked key can be revoked without touching others.
	raw2, hash2, prefix2, _ := model.GenerateWorkerKey()
	key2 := &model.WorkerKey{ID: "wk_db2", KeyHash: hash2, KeyPrefix: prefix2, Groups: []string{"g"}, CreatedAt: time.Now().UTC()}
	if err := st.CreateWorkerKey(ctx, key2); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWorkerKey(ctx, "wk_db2"); err != nil {
		t.Fatal(err)
	}
	if auth.Authenticate(ctx, raw2) != nil {
		t.Errorf("revoked DB key still authenticates")
	}
}

// --- Middleware behavior ---

func testMiddlewareRouter(auth *WorkerKeyAuthenticator) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(workerAuthMiddleware(auth, discardLogger()))
	r.Get("/work", func(w http.ResponseWriter, r *http.Request) {
		wc := WorkerAuthFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wc.KeyID))
	})
	return r
}

func TestWorkerAuthMiddleware(t *testing.T) {
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{"good": {ID: "n1", Groups: []string{"g"}}}}
	cfg.build()
	auth := NewWorkerKeyAuthenticator(cfg, nil, discardLogger())
	router := testMiddlewareRouter(auth)

	// Missing header => 401.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/work", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing key: status = %d, want 401", rec.Code)
	}

	// Wrong header => 401.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/work", nil)
	req.Header.Set("X-Worker-Key", "bad")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad key: status = %d, want 401", rec.Code)
	}

	// Correct header => 200 and attributes to key ID.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/work", nil)
	req.Header.Set("X-Worker-Key", "good")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "n1" {
		t.Errorf("good key: status=%d body=%q, want 200/n1", rec.Code, rec.Body.String())
	}
}

func TestWorkerAuthMiddleware_OpenAccess(t *testing.T) {
	// No static config, no DB keys => open access, no header required.
	st := testAuthStore(t)
	auth := NewWorkerKeyAuthenticator(nil, st, discardLogger())
	router := testMiddlewareRouter(auth)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/work", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "none" {
		t.Errorf("open access: status=%d body=%q, want 200/none", rec.Code, rec.Body.String())
	}
}

// --- Admin HTTP endpoints (mint / list / disable / revoke) ---

// adminRouter mounts the worker-key admin routes with an injected admin user,
// bypassing token auth so the handlers can be exercised directly.
func adminRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			admin := &model.User{Username: "admin", Role: model.RoleAdmin}
			ctx := context.WithValue(req.Context(), ctxKeyUserAuth, &UserContext{User: admin})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/v1/admin/worker-keys", func(r chi.Router) {
		r.Get("/", s.handleListWorkerKeys)
		r.Post("/", s.handleCreateWorkerKey)
		r.Route("/{id}", func(r chi.Router) {
			r.Patch("/", s.handleUpdateWorkerKey)
			r.Delete("/", s.handleRevokeWorkerKey)
		})
	})
	return r
}

func doAdmin(t *testing.T, router http.Handler, method, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var env envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

func TestAdminWorkerKeyEndpoints(t *testing.T) {
	srv := testServer()
	router := adminRouter(srv)

	// Mint a key.
	rec, env := doAdmin(t, router, "POST", "/api/v1/admin/worker-keys",
		`{"label":"esmfold-1","groups":["esmfold"],"description":"gpu box"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Key       string          `json:"key"`
		WorkerKey model.WorkerKey `json:"worker_key"`
	}
	if err := json.Unmarshal(env.Data, &minted); err != nil {
		t.Fatalf("decode mint response: %v", err)
	}
	if minted.Key == "" {
		t.Fatal("mint response did not include the raw key")
	}
	if minted.WorkerKey.KeyHash != "" {
		t.Errorf("KeyHash leaked in JSON response: %q", minted.WorkerKey.KeyHash)
	}
	keyID := minted.WorkerKey.ID

	// The minted raw key authenticates via the server's authenticator.
	if got := srv.workerAuth.Authenticate(context.Background(), minted.Key); got == nil || got.KeyID != keyID {
		t.Fatalf("minted key does not authenticate: %v", got)
	}

	// List shows the key but never the hash or raw secret.
	rec, env = doAdmin(t, router, "GET", "/api/v1/admin/worker-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status=%d", rec.Code)
	}
	if bytes.Contains(env.Data, []byte(minted.Key)) {
		t.Errorf("list response leaked the raw key")
	}

	// Disable the key.
	rec, _ = doAdmin(t, router, "PATCH", "/api/v1/admin/worker-keys/"+keyID, `{"disabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if srv.workerAuth.Authenticate(context.Background(), minted.Key) != nil {
		t.Errorf("disabled key still authenticates")
	}

	// Re-enable.
	doAdmin(t, router, "PATCH", "/api/v1/admin/worker-keys/"+keyID, `{"disabled":false}`)
	if srv.workerAuth.Authenticate(context.Background(), minted.Key) == nil {
		t.Errorf("re-enabled key does not authenticate")
	}

	// Revoke (delete).
	rec, _ = doAdmin(t, router, "DELETE", "/api/v1/admin/worker-keys/"+keyID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if srv.workerAuth.Authenticate(context.Background(), minted.Key) != nil {
		t.Errorf("revoked key still authenticates")
	}

	// Revoking a missing key => 404.
	rec, _ = doAdmin(t, router, "DELETE", "/api/v1/admin/worker-keys/wk_missing", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("revoke missing: status=%d, want 404", rec.Code)
	}

	// Past expiry is rejected.
	rec, _ = doAdmin(t, router, "POST", "/api/v1/admin/worker-keys",
		`{"label":"bad","expires_at":"2000-01-01T00:00:00Z"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("past expiry: status=%d, want 400", rec.Code)
	}
}

// TestLoadWorkerKeyConfig_FailsClosed verifies that a configured-but-unloadable
// key source is a hard error rather than a silent degrade to open access (F1).
func TestLoadWorkerKeyConfig_FailsClosed(t *testing.T) {
	// A configured file that does not exist must error.
	if _, err := LoadWorkerKeyConfig("/nonexistent/worker-keys.json"); err == nil {
		t.Error("missing key file: expected error, got nil (would leave auth open)")
	}

	// A configured file with malformed JSON must error.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if _, err := LoadWorkerKeyConfig(bad); err == nil {
		t.Error("malformed key file: expected error, got nil")
	}

	// No file configured is fine (returns an empty, disabled config).
	cfg, err := LoadWorkerKeyConfig("")
	if err != nil {
		t.Errorf("no file configured: unexpected error %v", err)
	}
	if cfg.IsEnabled() {
		t.Error("empty config should not be enabled")
	}
}

// TestCreateWorkerKey_EmptyGroupsDefaultsToDefault verifies that minting a key
// without groups yields a least-privilege ["default"] scope, not a wildcard (F2).
func TestCreateWorkerKey_EmptyGroupsDefaultsToDefault(t *testing.T) {
	router := adminRouter(testServer())

	rec, env := doAdmin(t, router, "POST", "/api/v1/admin/worker-keys", `{"label":"no-groups"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var minted struct {
		WorkerKey model.WorkerKey `json:"worker_key"`
	}
	if err := json.Unmarshal(env.Data, &minted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(minted.WorkerKey.Groups) != 1 || minted.WorkerKey.Groups[0] != "default" {
		t.Errorf("empty groups should default to [default], got %v", minted.WorkerKey.Groups)
	}
}

// TestUpdateWorkerKey_RejectsEmptyGroups verifies a PATCH cannot widen a key to
// wildcard by clearing its groups (F2).
func TestUpdateWorkerKey_RejectsEmptyGroups(t *testing.T) {
	router := adminRouter(testServer())

	_, env := doAdmin(t, router, "POST", "/api/v1/admin/worker-keys",
		`{"label":"scoped","groups":["esmfold"]}`)
	var minted struct {
		WorkerKey model.WorkerKey `json:"worker_key"`
	}
	if err := json.Unmarshal(env.Data, &minted); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec, _ := doAdmin(t, router, "PATCH", "/api/v1/admin/worker-keys/"+minted.WorkerKey.ID, `{"groups":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH groups=[]: status=%d, want 400", rec.Code)
	}
}
