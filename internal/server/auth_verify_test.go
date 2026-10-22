package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/store"
)

// mintTestToken signs a BV-BRC-style token payload with priv and returns the
// full wire-format token. Duplicated from internal/bvbrc's test helper of
// the same name: cross-package _test.go files cannot be shared in Go.
func mintTestToken(t *testing.T, priv *rsa.PrivateKey, fields map[string]string, order []string) string {
	t.Helper()
	var parts []string
	for _, k := range order {
		parts = append(parts, k+"="+fields[k])
	}
	payload := strings.Join(parts, "|")
	digest := sha1.Sum([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}
	return payload + "|sig=" + hex.EncodeToString(sig)
}

func standardTestFields(username, signingSubject string, expiry time.Time) (map[string]string, []string) {
	fields := map[string]string{
		"un":             username,
		"tokenid":        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"expiry":         strconv.FormatInt(expiry.Unix(), 10),
		"client_id":      "test-client",
		"token_type":     "Bearer",
		"realm":          "patricbrc.org",
		"SigningSubject": signingSubject,
	}
	order := []string{"un", "tokenid", "expiry", "client_id", "token_type", "realm", "SigningSubject"}
	return fields, order
}

func newTestKeyServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"pubkey": pemText})
	}))
}

// newVerifiedAuthTestServer builds a Server with token verification enabled
// against an httptest key server, optionally with a denylist and the
// allowUnverifiedMGRAST flag.
func newVerifiedAuthTestServer(t *testing.T, keyServerURL string, allowMGRAST bool, denylistUsers, denylistTokenIDs []string) *Server {
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

	verifier := bvbrc.NewVerifier(
		bvbrc.WithAllowlist(keyServerURL),
		bvbrc.WithLogger(logger),
	)

	opts := []Option{WithTokenVerifier(verifier)}
	if allowMGRAST {
		opts = append(opts, WithAllowUnverifiedMGRAST(true))
	}
	if len(denylistUsers) > 0 || len(denylistTokenIDs) > 0 {
		opts = append(opts, WithAuthDenylist(denylistUsers, denylistTokenIDs))
	}
	return New(cfg, st, nil, logger, opts...)
}

func TestApiAuthMiddleware_VerifiedToken_Allowed(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := newTestKeyServer(t, &priv.PublicKey)
	defer srv.Close()

	s := newVerifiedAuthTestServer(t, srv.URL, false, nil, nil)

	fields, order := standardTestFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestApiAuthMiddleware_ForgedToken_Rejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := newTestKeyServer(t, &priv.PublicKey)
	defer srv.Close()

	s := newVerifiedAuthTestServer(t, srv.URL, false, nil, nil)

	fields, order := standardTestFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)
	forged := strings.Replace(token, "un=alice@patricbrc.org", "un=mallory@patricbrc.org", 1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("Authorization", forged)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestApiAuthMiddleware_MGRASTWithoutAllowFlag_Rejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := newTestKeyServer(t, &priv.PublicKey)
	defer srv.Close()

	s := newVerifiedAuthTestServer(t, srv.URL, false, nil, nil)

	// Same wire format is fine here; identity is unverified regardless of
	// whether the signature would check out.
	fields, order := standardTestFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("X-MG-RAST-Token", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

func TestApiAuthMiddleware_DenylistedUser_Rejected(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := newTestKeyServer(t, &priv.PublicKey)
	defer srv.Close()

	s := newVerifiedAuthTestServer(t, srv.URL, false, []string{"alice@patricbrc.org"}, nil)

	fields, order := standardTestFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
}

// TestApiAuthMiddleware_KeyServerDown_Returns503 verifies that a token
// verification failure caused by an unreachable key server (as opposed to a
// rejected credential) surfaces as 503, not 401.
func TestApiAuthMiddleware_KeyServerDown_Returns503(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := newTestKeyServer(t, &priv.PublicKey)
	url := srv.URL
	srv.Close() // down before any fetch

	s := newVerifiedAuthTestServer(t, url, false, nil, nil)

	fields, order := standardTestFields("alice@patricbrc.org", url, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// TestApiAuthMiddleware_VerificationDisabled_LegacyBehaviorUnchanged is a
// regression guard: a Server built without WithTokenVerifier (the default
// for every pre-existing test) must keep trusting un= as claimed, with no
// signature check performed.
func TestApiAuthMiddleware_VerificationDisabled_LegacyBehaviorUnchanged(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	cfg := config.DefaultServerConfig()
	s := New(cfg, st, nil, logger) // no WithTokenVerifier

	expiry := time.Now().Add(time.Hour).Unix()
	unsignedToken := "un=alice@patricbrc.org|tokenid=x|expiry=" + strconv.FormatInt(expiry, 10)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/", nil)
	req.Header.Set("Authorization", unsignedToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (verification disabled); body=%s", w.Code, w.Body.String())
	}
}
