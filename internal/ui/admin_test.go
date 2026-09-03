package ui

import (
	"context"
	"encoding/json"
	"fmt"
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

// newTestSession creates and persists a session with the given role,
// returning the session and a cookie that authenticates as it.
func newTestSession(t *testing.T, u *UI, username, role string) (*model.Session, *http.Cookie) {
	t.Helper()
	sess, err := u.sessions.CreateSession(context.Background(), "user_"+username, username, role, "tok-"+username, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess, &http.Cookie{Name: SessionCookieName, Value: sess.ID}
}

// --- 1. Admin gating: new /admin/* routes reuse the existing AdminMiddleware. ---

func TestAdminNewRoutes_GatedOnAdminRole(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	r := chi.NewRouter()
	u.RegisterRoutes(r)

	_, userCookie := newTestSession(t, u, "regular", string(model.RoleUser))
	_, adminCookie := newTestSession(t, u, "boss", string(model.RoleAdmin))

	routes := []string{"/admin/fleet", "/admin/worker-keys", "/admin/outputs"}

	for _, path := range routes {
		t.Run(path+"/unauthenticated", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			// No session at all: AuthMiddleware redirects to /login, matching
			// every other protected page (and the API's 401-for-no-token
			// convention, just expressed as a redirect since this is HTML).
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("unauthenticated %s: status = %d, want %d", path, rec.Code, http.StatusSeeOther)
			}
		})

		t.Run(path+"/non-admin", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(userCookie)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			// Authenticated but not admin: 403, exactly like requireAdmin on
			// the API side (internal/server/auth.go) and like AdminMiddleware
			// already does for /admin/stats etc.
			if rec.Code != http.StatusForbidden {
				t.Fatalf("non-admin %s: status = %d, want %d", path, rec.Code, http.StatusForbidden)
			}
		})

		t.Run(path+"/admin", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(adminCookie)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("admin %s: status = %d, want %d, body: %s", path, rec.Code, http.StatusOK, rec.Body.String())
			}
		})
	}
}

// --- 2. Worker fleet: renders every worker, proving the >20 pagination gotcha is bypassed. ---

func seedWorkers(t *testing.T, st interface {
	CreateWorker(ctx context.Context, w *model.Worker) error
}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		w := &model.Worker{
			ID:           fmt.Sprintf("worker_%03d", i),
			Name:         fmt.Sprintf("fleet-worker-%03d", i),
			Hostname:     fmt.Sprintf("host-%03d", i),
			Group:        "default",
			State:        model.WorkerStateOnline,
			Runtime:      model.RuntimeDocker,
			Version:      "abc123def456",
			LastSeen:     time.Now().UTC(),
			RegisteredAt: time.Now().UTC(),
		}
		if err := st.CreateWorker(context.Background(), w); err != nil {
			t.Fatalf("create worker %d: %v", i, err)
		}
	}
}

func TestHandleAdminFleet_RendersAllWorkersBeyondPageSize(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	// The API's default page size for /workers is 20 — seed comfortably past
	// that to prove HandleAdminFleet (which calls store.ListWorkers directly,
	// not the paginated API) returns every row.
	const total = 27
	seedWorkers(t, st, total)

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}
	req := withSession(httptest.NewRequest(http.MethodGet, "/admin/fleet", nil), adminSess)
	rec := httptest.NewRecorder()

	u.HandleAdminFleet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if got := strings.Count(body, "fleet-worker-"); got < total {
		t.Fatalf("expected all %d workers rendered, found %d occurrences of the name prefix", total, got)
	}
	if !strings.Contains(body, fmt.Sprintf("%d worker", total)) {
		t.Errorf("expected worker count %d in body, got: %s", total, body)
	}
	// Each row emits "fleet-worker-" twice (the row id attribute and the name
	// cell), so the Count assertion above alone would also pass if the range
	// loop stopped halfway. Confirm the *last* seeded worker (index total-1)
	// actually made it into the rendered table.
	lastName := fmt.Sprintf("fleet-worker-%03d", total-1)
	if !strings.Contains(body, lastName) {
		t.Fatalf("expected last seeded worker %q in rendered body (proves the full set rendered, not just a prefix)", lastName)
	}
}

// --- 3. Worker keys: issue/revoke round trip. ---

func TestAdminWorkerKey_IssueRevokeRoundTrip(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}

	// Issue.
	form := url.Values{}
	form.Set("label", "test-key")
	form.Set("groups", "gpu, default")
	form.Set("description", "issued in test")
	req := withSession(httptest.NewRequest(http.MethodPost, "/admin/worker-keys", strings.NewReader(form.Encode())), adminSess)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	u.HandleAdminWorkerKeyCreate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gwk_") {
		t.Fatalf("expected raw key (gwk_ prefix) in create response, got: %s", rec.Body.String())
	}

	keys, err := st.ListWorkerKeys(context.Background())
	if err != nil {
		t.Fatalf("list worker keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 worker key, got %d", len(keys))
	}
	key := keys[0]
	if key.Label != "test-key" {
		t.Errorf("label = %q, want test-key", key.Label)
	}
	if len(key.Groups) != 2 || key.Groups[0] != "gpu" || key.Groups[1] != "default" {
		t.Errorf("groups = %v, want [gpu default]", key.Groups)
	}
	if key.KeyHash == "" {
		t.Error("expected key hash to be persisted")
	}

	// Revoke.
	revokeReq := httptest.NewRequest(http.MethodDelete, "/admin/worker-keys/"+key.ID, nil)
	revokeReq.SetPathValue("id", key.ID)
	revokeRec := httptest.NewRecorder()

	u.HandleAdminWorkerKeyRevoke(revokeRec, revokeReq)

	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d", revokeRec.Code)
	}

	after, err := st.ListWorkerKeys(context.Background())
	if err != nil {
		t.Fatalf("list worker keys after revoke: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 worker keys after revoke, got %d", len(after))
	}
}

func TestAdminWorkerKey_DefaultsGroupWhenOmitted(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}
	form := url.Values{}
	form.Set("label", "no-groups")
	req := withSession(httptest.NewRequest(http.MethodPost, "/admin/worker-keys", strings.NewReader(form.Encode())), adminSess)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	u.HandleAdminWorkerKeyCreate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	keys, err := st.ListWorkerKeys(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || len(keys[0].Groups) != 1 || keys[0].Groups[0] != "default" {
		t.Fatalf("expected key defaulted to [default] group, got %+v", keys)
	}
}

// --- 4. Label vocabulary ops (existing handlers; previously untested). ---

func TestAdminLabels_CreateListDeleteRoundTrip(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}

	form := url.Values{}
	form.Set("key", "domain")
	form.Set("value", "genomics")
	form.Set("description", "genomics workflows")
	createReq := withSession(httptest.NewRequest(http.MethodPost, "/admin/labels", strings.NewReader(form.Encode())), adminSess)
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()

	u.HandleAdminLabelCreate(createRec, createReq)

	if createRec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want %d (redirect)", createRec.Code, http.StatusSeeOther)
	}

	listReq := withSession(httptest.NewRequest(http.MethodGet, "/admin/labels", nil), adminSess)
	listRec := httptest.NewRecorder()
	u.HandleAdminLabels(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}
	if !strings.Contains(listRec.Body.String(), "genomics") {
		t.Fatalf("expected created label in list body")
	}

	entries, err := st.ListLabelVocabulary(context.Background())
	if err != nil {
		t.Fatalf("list label vocab: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 label entry, got %d", len(entries))
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/admin/labels/"+entries[0].ID, nil)
	delReq.SetPathValue("id", entries[0].ID)
	delRec := httptest.NewRecorder()
	u.HandleAdminLabelDelete(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", delRec.Code)
	}

	after, err := st.ListLabelVocabulary(context.Background())
	if err != nil {
		t.Fatalf("list label vocab after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 label entries after delete, got %d", len(after))
	}
}

// --- 5. Output verification/redelivery: page renders, and the loopback call reaches a stub API. ---

func TestHandleAdminOutputs_RendersForm(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}
	req := withSession(httptest.NewRequest(http.MethodGet, "/admin/outputs", nil), adminSess)
	rec := httptest.NewRecorder()

	u.HandleAdminOutputs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Verify Outputs") {
		t.Fatalf("expected verify form in body")
	}
}

func TestHandleAdminOutputsVerify_CallsAdminAPIAndRendersResult(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	var gotAuth, gotPath string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		resp := model.Response{
			Status:    "ok",
			RequestID: "req_test",
			Data: map[string]any{
				"submission_id": "sub_1",
				"state":         "COMPLETED",
				"output_state":  "delivered",
				"submissions":   []string{"sub_1"},
				"files":         []any{},
				"summary":       map[string]any{"total": 0, "ok": 0, "mismatched": 0, "errors": 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer stub.Close()
	u.apiBaseURL = stub.URL

	adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin), Token: "un=boss|tokenid=abc|expiry=9999999999"}
	form := url.Values{}
	form.Set("submission_id", "sub_1")
	req := withSession(httptest.NewRequest(http.MethodPost, "/admin/outputs/verify", strings.NewReader(form.Encode())), adminSess)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	u.HandleAdminOutputsVerify(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/admin/submissions/sub_1/verify-outputs" {
		t.Errorf("called path = %q", gotPath)
	}
	if gotAuth != adminSess.Token {
		t.Errorf("Authorization header = %q, want session token %q", gotAuth, adminSess.Token)
	}
	if !strings.Contains(rec.Body.String(), "COMPLETED") {
		t.Fatalf("expected rendered report in body, got: %s", rec.Body.String())
	}
}
