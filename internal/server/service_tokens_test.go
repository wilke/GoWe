package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

const svcTestName = "model-router"

var svcTestUser = model.ServiceUsername(svcTestName) // model-router@service.gowe

// mintServiceToken stores a new service token of name and returns the raw
// token with the stored record.
func mintServiceToken(t *testing.T, st store.Store, name string, expiresAt *time.Time) (string, *model.ServiceToken) {
	t.Helper()
	raw, hash, prefix, err := model.GenerateServiceToken()
	if err != nil {
		t.Fatal(err)
	}
	tok := &model.ServiceToken{
		ID:          "svt_" + uuid.New().String(),
		Name:        name,
		TokenHash:   hash,
		TokenPrefix: prefix,
		CreatedBy:   "admin",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}
	if err := st.CreateServiceToken(context.Background(), tok); err != nil {
		t.Fatalf("create service token: %v", err)
	}
	return raw, tok
}

// authProbeOpts are the apiAuthMiddleware arguments of authProbe.
type authProbeOpts struct {
	adminCfg    *AdminConfig
	anon        *AnonymousConfig
	verifier    *bvbrc.Verifier
	allowMGRAST bool
	denylist    *AuthDenylist
	logger      *slog.Logger
}

// authProbe wraps a handler that records the request's UserContext in the
// real apiAuthMiddleware.
func authProbe(st store.Store, o authProbeOpts) (http.Handler, func() *UserContext) {
	logger := o.logger
	if logger == nil {
		logger = discardLogger()
	}
	var got *UserContext
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := requestIDMiddleware(apiAuthMiddleware(st, o.adminCfg, o.anon, logger, o.verifier, o.allowMGRAST, o.denylist)(inner))
	return h, func() *UserContext { return got }
}

// probe sends one request with header: value ("" header: none) to h.
func probe(t *testing.T, h http.Handler, header, value string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// wantInvalidToken checks a 401 with the generic bad-token message.
func wantInvalidToken(t *testing.T, rec *httptest.ResponseRecorder, env envelope) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized || env.Error == nil || env.Error.Message != "invalid token format" {
		t.Errorf("status %d, error %+v; want 401 \"invalid token format\"", rec.Code, env.Error)
	}
}

// failingVerifier returns a BV-BRC verifier whose only trusted issuer is a
// key server that fails the test when it is contacted.
func failingVerifier(t *testing.T) *bvbrc.Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the BV-BRC verifier was used: its key server was contacted")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return bvbrc.NewVerifier(bvbrc.WithAllowlist(srv.URL), bvbrc.WithLogger(discardLogger()))
}

// countingKeyServer serves pub as a BV-BRC issuer key and counts requests.
func countingKeyServer(t *testing.T, pub *rsa.PublicKey) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"pubkey": pemText})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// svcStore wraps a store to count or fail the service token calls.
type svcStore struct {
	store.Store
	touches   int
	touchErr  error
	lookupErr error
}

func (s *svcStore) TouchServiceToken(ctx context.Context, id string, when time.Time) error {
	s.touches++
	if s.touchErr != nil {
		return s.touchErr
	}
	return s.Store.TouchServiceToken(ctx, id, when)
}

func (s *svcStore) GetServiceTokenByHash(ctx context.Context, hash string) (*model.ServiceToken, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return s.Store.GetServiceTokenByHash(ctx, hash)
}

// A valid token authenticates as <name>@service.gowe, role user, IsService,
// with no provider token, with and without verification and whether or not
// anonymous access is on. The BV-BRC verifier is never used.
func TestServiceToken_Authenticates(t *testing.T) {
	for _, verified := range []bool{false, true} {
		for _, anonymous := range []bool{false, true} {
			t.Run(fmt.Sprintf("verified=%v,anonymous=%v", verified, anonymous), func(t *testing.T) {
				st := testAuthStore(t)
				raw, _ := mintServiceToken(t, st, svcTestName, nil)
				o := authProbeOpts{anon: &AnonymousConfig{Enabled: anonymous}}
				if verified {
					o.verifier = failingVerifier(t)
				}
				h, got := authProbe(st, o)
				for _, header := range []string{"Bearer " + raw, raw} {
					rec, _ := probe(t, h, "Authorization", header)
					if rec.Code != http.StatusOK {
						t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
					}
					uc := got()
					if uc == nil || uc.User == nil {
						t.Fatal("no user context")
					}
					if uc.User.Username != svcTestUser || uc.User.Role != model.RoleUser || !uc.IsService ||
						uc.Provider != model.ProviderService || uc.Token != "" || !uc.Expiry.IsZero() || uc.User.IsAdmin() {
						t.Errorf("user context = %+v, user %+v", uc, uc.User)
					}
				}
				u, err := st.GetUser(context.Background(), svcTestUser)
				if err != nil || u == nil || u.Provider != model.ProviderService {
					t.Errorf("stored user = %+v, %v; want provider service", u, err)
				}
			})
		}
	}
}

// Unknown, malformed, expired and deleted tokens get the generic 401, with
// and without verification; the verifier is never used.
func TestServiceToken_Rejected(t *testing.T) {
	ctx := context.Background()
	st := testAuthStore(t)
	past := time.Now().Add(-time.Minute)
	expired, _ := mintServiceToken(t, st, svcTestName, &past)
	deleted, dtok := mintServiceToken(t, st, svcTestName, nil)
	if _, ok, err := st.DeleteServiceToken(ctx, dtok.ID); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	valid, _ := mintServiceToken(t, st, svcTestName, nil)
	unknown, _, _, _ := model.GenerateServiceToken()

	cases := []struct{ name, header string }{
		{"unknown", "Bearer " + unknown},
		{"expired", "Bearer " + expired},
		{"deleted", "Bearer " + deleted},
		{"one character short", "Bearer " + valid[:len(valid)-1]},
		{"one character long", "Bearer " + valid + "A"},
		{"invalid character", "Bearer " + valid[:len(valid)-1] + "="},
		{"prefix only", "Bearer " + model.ServiceTokenRawPrefix},
		{"OAuth form", "OAuth " + valid},
		{"inside a BV-BRC token", "Bearer un=alice|tokenid=x|expiry=4102444800|note=" + valid},
	}
	for _, verified := range []bool{false, true} {
		o := authProbeOpts{anon: &AnonymousConfig{Enabled: true}}
		if verified {
			o.verifier = failingVerifier(t)
		}
		h, got := authProbe(st, o)
		for _, c := range cases {
			t.Run(fmt.Sprintf("verified=%v/%s", verified, c.name), func(t *testing.T) {
				rec, env := probe(t, h, "Authorization", c.header)
				wantInvalidToken(t, rec, env)
				if bytes.Contains(rec.Body.Bytes(), []byte(model.ServiceTokenRawPrefix)) {
					t.Errorf("the answer echoes the token: %s", rec.Body.String())
				}
			})
		}
		// The valid token still works (the cases above are not refused for
		// another reason).
		if rec, _ := probe(t, h, "Authorization", "Bearer "+valid); rec.Code != http.StatusOK || got() == nil || !got().IsService {
			t.Errorf("verified=%v: valid token: status %d", verified, rec.Code)
		}
	}
}

// A credential carrying the service prefix never reaches the BV-BRC verifier
// or the unverified parser, even a correctly signed BV-BRC token that would
// otherwise authenticate.
func TestServiceToken_NeverReachesProviderPath(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keySrv, hits := countingKeyServer(t, &priv.PublicKey)
	st := testAuthStore(t)
	verifier := bvbrc.NewVerifier(bvbrc.WithAllowlist(keySrv.URL), bvbrc.WithLogger(discardLogger()))

	signed := func(clientID string) string {
		fields, order := standardTestFields("mallory@patricbrc.org", keySrv.URL, time.Now().Add(time.Hour))
		fields["client_id"] = clientID
		return mintTestToken(t, priv, fields, order)
	}
	svcLike := model.ServiceTokenRawPrefix + strings.Repeat("A", 43)

	// Verified mode: the control authenticates (the setup works) ...
	h, got := authProbe(st, authProbeOpts{verifier: verifier})
	if rec, _ := probe(t, h, "Authorization", signed("test-client")); rec.Code != http.StatusOK || got() == nil || got().User.Username != "mallory@patricbrc.org" {
		t.Fatalf("control token: status %d", rec.Code)
	}
	before := hits.Load()
	// ... the same token carrying the prefix is refused without the
	// verifier fetching anything.
	rec, env := probe(t, h, "Authorization", signed(svcLike))
	wantInvalidToken(t, rec, env)
	if hits.Load() != before {
		t.Errorf("the verifier handled a credential carrying the service prefix")
	}

	// Unverified mode (--insecure-skip-token-verify): the parser would
	// accept un= as claimed.
	h, got = authProbe(st, authProbeOpts{})
	plain := "un=mallory@patricbrc.org|tokenid=x|expiry=4102444800"
	if rec, _ := probe(t, h, "Authorization", plain); rec.Code != http.StatusOK || got() == nil {
		t.Fatalf("control token (unverified): status %d", rec.Code)
	}
	rec, env = probe(t, h, "Authorization", plain+"|client_id="+svcLike)
	wantInvalidToken(t, rec, env)
	if u, _ := st.GetUser(context.Background(), "mallory@patricbrc.org"); u == nil {
		t.Fatal("control user missing")
	}
}

// A BV-BRC or MG-RAST token whose username is in the service realm is
// refused, verified or not.
func TestServiceToken_ReservedRealm(t *testing.T) {
	st := testAuthStore(t)

	h, _ := authProbe(st, authProbeOpts{anon: &AnonymousConfig{Enabled: true}})
	for _, un := range []string{svcTestUser, "X@Service.GOWE"} {
		rec, env := probe(t, h, "Authorization", "un="+un+"|tokenid=t|expiry=4102444800|sig=x")
		wantInvalidToken(t, rec, env)
		rec, env = probe(t, h, "X-MG-RAST-Token", "un="+un+"|tokenid=t|expiry=4102444800")
		wantInvalidToken(t, rec, env)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keySrv, _ := countingKeyServer(t, &priv.PublicKey)
	verifier := bvbrc.NewVerifier(bvbrc.WithAllowlist(keySrv.URL), bvbrc.WithLogger(discardLogger()))
	h, _ = authProbe(st, authProbeOpts{verifier: verifier})
	fields, order := standardTestFields(svcTestUser, keySrv.URL, time.Now().Add(time.Hour))
	rec, env := probe(t, h, "Authorization", mintTestToken(t, priv, fields, order))
	wantInvalidToken(t, rec, env)

	if u, _ := st.GetUser(context.Background(), svcTestUser); u != nil {
		t.Errorf("a user row was created for the reserved realm: %+v", u)
	}
}

// A service token in the X-MG-RAST-Token header is never accepted as one.
func TestServiceToken_MGRASTHeaderNotAccepted(t *testing.T) {
	st := testAuthStore(t)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	for _, o := range []authProbeOpts{
		{anon: &AnonymousConfig{Enabled: true}},
		{verifier: failingVerifier(t), allowMGRAST: true},
	} {
		h, got := authProbe(st, o)
		rec, env := probe(t, h, "X-MG-RAST-Token", raw)
		wantInvalidToken(t, rec, env)
		if got() != nil {
			t.Errorf("the request went through as %+v", got())
		}
	}
}

// The denylist applies to the service username.
func TestServiceToken_Denylist(t *testing.T) {
	st := testAuthStore(t)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	h, _ := authProbe(st, authProbeOpts{denylist: NewAuthDenylist([]string{svcTestUser}, nil)})
	rec, env := probe(t, h, "Authorization", "Bearer "+raw)
	wantInvalidToken(t, rec, env)
}

// last_used_at is written at most once per minute; a failed write does not
// fail the request.
func TestServiceToken_LastUsedOncePerMinute(t *testing.T) {
	ctx := context.Background()
	st := testAuthStore(t)
	raw, tok := mintServiceToken(t, st, svcTestName, nil)
	ws := &svcStore{Store: st}
	h, _ := authProbe(ws, authProbeOpts{})
	lastUsed := func() *time.Time {
		t.Helper()
		got, err := st.GetServiceTokenByHash(ctx, tok.TokenHash)
		if err != nil || got == nil {
			t.Fatalf("read token: %v %v", got, err)
		}
		return got.LastUsedAt
	}
	send := func() {
		t.Helper()
		if rec, _ := probe(t, h, "Authorization", "Bearer "+raw); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}

	send()
	if ws.touches != 1 || lastUsed() == nil {
		t.Fatalf("first use: %d touches, last_used_at %v", ws.touches, lastUsed())
	}
	send()
	send()
	if ws.touches != 1 {
		t.Errorf("uses within a minute wrote last_used_at %d times, want once", ws.touches)
	}

	if err := st.TouchServiceToken(ctx, tok.ID, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	send()
	if lu := lastUsed(); ws.touches != 2 || lu == nil || time.Since(*lu) > time.Minute {
		t.Errorf("use after two minutes: %d touches, last_used_at %v", ws.touches, lu)
	}

	if err := st.TouchServiceToken(ctx, tok.ID, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ws.touchErr = errors.New("database is locked")
	send()
	if ws.touches != 3 {
		t.Errorf("%d touches, want 3", ws.touches)
	}
}

// A store failure answers 503; the log holds neither the token nor its hash,
// on failure or on success.
func TestServiceToken_StoreErrorAndLogs(t *testing.T) {
	st := testAuthStore(t)
	raw, tok := mintServiceToken(t, st, svcTestName, nil)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ws := &svcStore{Store: st, lookupErr: errors.New("connection refused")}
	h, _ := authProbe(ws, authProbeOpts{logger: logger})

	rec, env := probe(t, h, "Authorization", "Bearer "+raw)
	if rec.Code != http.StatusServiceUnavailable || env.Error == nil || env.Error.Code != model.ErrUnavailable {
		t.Errorf("store error: status %d, error %+v; want 503 UNAVAILABLE", rec.Code, env.Error)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the store error is not logged at ERROR:\n%s", logs.String())
	}

	ws.lookupErr = nil
	if rec, _ := probe(t, h, "Authorization", "Bearer "+raw); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(logs.String(), tok.ID) {
		t.Errorf("the use is not logged with the token id:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), raw) || strings.Contains(logs.String(), tok.TokenHash) {
		t.Errorf("the log holds the token or its hash:\n%s", logs.String())
	}
}

// svcServer is a full server with "admin" (and extra) as CLI admins, the
// admin lookup reading st, anonymous access on.
func svcServer(t *testing.T, extraAdmins []string, opts ...Option) (*Server, store.Store, *AdminConfig) {
	t.Helper()
	st := testAuthStore(t)
	adminCfg := NewAdminConfig(st, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins(append([]string{"admin"}, extraAdmins...))
	all := append([]Option{
		WithAnonymousConfig(&AnonymousConfig{Enabled: true}),
		WithAdminConfig(adminCfg),
	}, opts...)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(config.DefaultServerConfig(), st, nil, logger, all...), st, adminCfg
}

// Even listed in the admins and stored with role admin, a service identity
// is not an admin: admin routes answer 403, admin-only groups refuse it, and
// it cannot be made an admin.
func TestServiceToken_NeverAdmin(t *testing.T) {
	ctx := context.Background()
	srv, st, adminCfg := svcServer(t, []string{svcTestUser})
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	u, err := st.GetOrCreateUser(ctx, svcTestUser, model.ProviderService)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = model.RoleAdmin
	if err := st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}

	if adminCfg.IsAdmin(svcTestUser) {
		t.Error("AdminConfig.IsAdmin is true for a service identity")
	}
	h, got := authProbe(st, authProbeOpts{adminCfg: adminCfg})
	if rec, _ := probe(t, h, "Authorization", "Bearer "+raw); rec.Code != http.StatusOK || got() == nil ||
		got().User.Role != model.RoleUser || got().User.IsAdmin() {
		t.Errorf("service identity in the admin list: status %d, context %+v", rec.Code, got())
	}

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/service-tokens", ""},
		{"POST", "/api/v1/admin/service-tokens", `{"name":"other"}`},
		{"DELETE", "/api/v1/admin/service-tokens/svt_x", ""},
		{"GET", "/api/v1/admin/users", ""},
		{"GET", "/api/v1/admin/worker-keys", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as the service: status %d, want 403", c.method, c.path, rec.Code)
		}
	}

	// requireAdmin refuses IsService on its own.
	guarded := guardedWorkerKeyRouter(srv, &UserContext{User: &model.User{Username: "x", Role: model.RoleAdmin}, IsService: true})
	if rec, _ := doAdmin(t, guarded, "GET", "/api/v1/admin/worker-keys", ""); rec.Code != http.StatusForbidden {
		t.Errorf("requireAdmin with IsService: status %d, want 403", rec.Code)
	}

	// A group open to admins (admins: true) does not let it in.
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}, Admins: true}); err != nil {
		t.Fatal(err)
	}
	policy := groupacl.New().WithAdmins(adminCfg.IsAdmin)
	if err := policy.Reload(ctx, st); err != nil {
		t.Fatal(err)
	}
	if policy.Allowed(svcTestUser, "gpu") {
		t.Error("an admins-only group admits the service identity")
	}

	// It cannot be given the admin role.
	w, _ := doPutAs(t, srv, "/api/v1/admin/users/"+svcTestUser+"/role", `{"role":"admin"}`, adminAuthToken)
	if w.Code != http.StatusBadRequest {
		t.Errorf("set role admin on a service identity: status %d, want 400", w.Code)
	}
}

// The admin API: create returns the token once (no-store), list never shows
// it or its hash, delete makes the next request fail; validation, limits and
// the admin gate.
func TestServiceTokenAdminAPI(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := svcServer(t, nil)
	const path = "/api/v1/admin/service-tokens"

	before := time.Now()
	w, env := doPostAs(t, srv, path, `{"name":"model-router","description":"the router","expires_in":"2160h"}`, adminAuthToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var created struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		Username    string     `json:"username"`
		Token       string     `json:"token"`
		TokenPrefix string     `json:"token_prefix"`
		CreatedAt   time.Time  `json:"created_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "svt_") || created.Name != svcTestName || created.Username != svcTestUser ||
		!model.WellFormedServiceToken(created.Token) || created.TokenPrefix != created.Token[:16] || created.CreatedAt.IsZero() {
		t.Fatalf("create answer: id %q name %q username %q prefix %q", created.ID, created.Name, created.Username, created.TokenPrefix)
	}
	if created.ExpiresAt == nil || created.ExpiresAt.Before(before.Add(2159*time.Hour)) || created.ExpiresAt.After(time.Now().Add(2161*time.Hour)) {
		t.Errorf("expires_at = %v, want about 2160h from now", created.ExpiresAt)
	}
	if strings.Contains(w.Body.String(), model.HashServiceToken(created.Token)) {
		t.Error("the create answer holds the hash")
	}

	// The token authenticates.
	if w, _ := doGetAs(t, srv, "/api/v1/workflows/", created.Token); w.Code != http.StatusOK {
		t.Fatalf("use the token: %d %s", w.Code, w.Body.String())
	}

	// No expiry when expires_in is absent; the bounds 1h and 8760h are allowed.
	for _, body := range []string{`{"name":"a"}`, `{"name":"b","expires_in":"1h"}`, `{"name":"c","expires_in":"8760h"}`} {
		w, env := doPostAs(t, srv, path, body, adminAuthToken)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body.String())
		}
		var c struct {
			ExpiresAt *time.Time `json:"expires_at"`
		}
		_ = json.Unmarshal(env.Data, &c)
		if (body == `{"name":"a"}`) != (c.ExpiresAt == nil) {
			t.Errorf("create %s: expires_at = %v", body, c.ExpiresAt)
		}
	}

	// List: never the token or the hash.
	w, env = doGetAs(t, srv, path, adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, created.Token) || strings.Contains(body, model.HashServiceToken(created.Token)) ||
		strings.Contains(body, `"token":`) || strings.Contains(body, "token_hash") {
		t.Errorf("the list shows a token or a hash: %s", body)
	}
	var list struct {
		Total         int `json:"total"`
		ServiceTokens []struct {
			ID         string     `json:"id"`
			Username   string     `json:"username"`
			LastUsedAt *time.Time `json:"last_used_at"`
			Expired    bool       `json:"expired"`
		} `json:"service_tokens"`
	}
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 4 || len(list.ServiceTokens) != 4 {
		t.Fatalf("list holds %d tokens, want 4", len(list.ServiceTokens))
	}
	for _, lt := range list.ServiceTokens {
		if lt.ID == created.ID && (lt.Username != svcTestUser || lt.LastUsedAt == nil || lt.Expired) {
			t.Errorf("listed token %+v", lt)
		}
	}

	// Revoke: 200, then the token is refused; again: 404.
	if w := doDeleteAs(t, srv, path+"/"+created.ID, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w, env := doGetAs(t, srv, "/api/v1/workflows/", created.Token); w.Code != http.StatusUnauthorized || env.Error == nil || env.Error.Message != "invalid token format" {
		t.Errorf("revoked token: %d %s", w.Code, w.Body.String())
	}
	if w := doDeleteAs(t, srv, path+"/"+created.ID, adminAuthToken); w.Code != http.StatusNotFound {
		t.Errorf("revoke again: %d, want 404", w.Code)
	}

	// Validation errors: 400.
	for _, body := range []string{
		`{"name":""}`,
		`{"name":"Model-Router"}`,
		`{"name":"-router"}`,
		`{"name":"` + strings.Repeat("a", 65) + `"}`,
		`{"name":"r","description":"` + strings.Repeat("d", model.MaxServiceTokenDescriptionBytes+1) + `"}`,
		`{"name":"r","expires_in":"59m"}`,
		`{"name":"r","expires_in":"8761h"}`,
		`{"name":"r","expires_in":"soon"}`,
		`{"name":"r","expires":"720h"}`,
		`not json`,
	} {
		if w, _ := doPostAs(t, srv, path, body, adminAuthToken); w.Code != http.StatusBadRequest {
			t.Errorf("create %.60s: %d, want 400", body, w.Code)
		}
	}

	// The per-name limit: 409.
	for i := 0; i < model.MaxServiceTokensPerName; i++ {
		mintServiceToken(t, st, "full", nil)
	}
	if w, env := doPostAs(t, srv, path, `{"name":"full"}`, adminAuthToken); w.Code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict {
		t.Errorf("beyond the per-name limit: %d %s, want 409", w.Code, w.Body.String())
	}

	// Non-admin users and service identities: 403.
	svcRaw, svcTok := mintServiceToken(t, st, "router2", nil)
	for _, token := range []string{userAuthToken, svcRaw} {
		if w, _ := doPostAs(t, srv, path, `{"name":"x"}`, token); w.Code != http.StatusForbidden {
			t.Errorf("create as non-admin: %d, want 403", w.Code)
		}
		if w, _ := doGetAs(t, srv, path, token); w.Code != http.StatusForbidden {
			t.Errorf("list as non-admin: %d, want 403", w.Code)
		}
		if w := doDeleteAs(t, srv, path+"/"+svcTok.ID, token); w.Code != http.StatusForbidden {
			t.Errorf("revoke as non-admin: %d, want 403", w.Code)
		}
	}
	if got, _ := st.GetServiceTokenByHash(ctx, svcTok.TokenHash); got == nil {
		t.Error("a non-admin revoked a token")
	}
}

// End to end: a service token submits to a worker group restricted to its
// username (201) and is refused for a group that does not list it (403); the
// stored submission names the service and carries no token.
func TestServiceToken_SubmitToRestrictedGroup(t *testing.T) {
	ctx := context.Background()
	st := testAuthStore(t)
	for _, gp := range []*model.WorkerGroupPolicy{
		{Group: "router-gpu", Users: []string{svcTestUser}},
		{Group: "other", Users: []string{"alice"}},
	} {
		if err := st.UpsertWorkerGroupPolicy(ctx, gp); err != nil {
			t.Fatal(err)
		}
	}
	adminCfg := NewAdminConfig(st, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	policy := groupacl.New().WithAdmins(adminCfg.IsAdmin)
	if err := policy.Reload(ctx, st); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := New(config.DefaultServerConfig(), st, nil, logger,
		WithAnonymousConfig(&AnonymousConfig{Enabled: false}), WithAdminConfig(adminCfg), WithGroupPolicy(policy))
	raw, _ := mintServiceToken(t, st, svcTestName, nil)

	// It registers a workflow itself.
	regBody, _ := json.Marshal(map[string]any{"name": "svc-wf", "cwl": gpDoc("")})
	w, env := doPostAs(t, srv, "/api/v1/workflows", string(regBody), raw)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var wf struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &wf); err != nil || wf.ID == "" {
		t.Fatalf("register answer: %v %s", err, env.Data)
	}

	submit := func(group string) (*httptest.ResponseRecorder, envelope) {
		body, _ := json.Marshal(map[string]any{"workflow_id": wf.ID, "inputs": map[string]any{}, "labels": map[string]string{"worker_group": group}})
		return doPostAs(t, srv, "/api/v1/submissions", string(body), raw)
	}

	w, env = submit("router-gpu")
	if w.Code != http.StatusCreated {
		t.Fatalf("submit to its group: %d %s", w.Code, w.Body.String())
	}
	var sub struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &sub); err != nil || sub.ID == "" {
		t.Fatalf("submit answer: %v %s", err, env.Data)
	}
	stored, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored submission: %v %v", stored, err)
	}
	if stored.SubmittedBy != svcTestUser || stored.UserToken != "" || !stored.TokenExpiry.IsZero() || stored.AuthProvider != string(model.ProviderService) {
		t.Errorf("stored submission: submitted_by %q, token set %v, expiry %v, provider %q",
			stored.SubmittedBy, stored.UserToken != "", stored.TokenExpiry, stored.AuthProvider)
	}
	// It reads its own submission.
	if w, _ := doGetAs(t, srv, "/api/v1/submissions/"+sub.ID, raw); w.Code != http.StatusOK {
		t.Errorf("read its own submission: %d", w.Code)
	}

	w, env = submit("other")
	if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden {
		t.Errorf("submit to a group that does not list it: %d %s, want 403", w.Code, w.Body.String())
	}
}
