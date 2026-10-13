package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Worker key <-> worker binding tests (see authorizeWorker).

// keyedServer returns a server with three static worker keys: secretA and
// secretB are unrestricted (like the shared fleet key); secretG1 may only join
// group "g1".
func keyedServer(t *testing.T) (*Server, store.Store) {
	t.Helper()
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		"secretA":  {ID: "keyA"},
		"secretB":  {ID: "keyB"},
		"secretG1": {ID: "keyG1", Groups: []string{"g1"}},
	}}
	return testServerWithStore(WithWorkerKeyConfig(cfg))
}

func keyedDo(t *testing.T, srv *Server, secret, method, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Worker-Key", secret)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func keyedRegister(t *testing.T, srv *Server, secret, group string) string {
	t.Helper()
	body := `{"name":"w","hostname":"h","runtime":"none","group":"` + group + `"}`
	w, env := keyedDo(t, srv, secret, "POST", "/api/v1/workers/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status=%d body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	return data["id"].(string)
}

func TestWorkerKeyBinding_ForeignKeyForbidden(t *testing.T) {
	srv, _ := keyedServer(t)
	wid := keyedRegister(t, srv, "secretA", "default")
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_bind1")

	routes := []struct {
		name, method, path, body string
	}{
		{"heartbeat", "PUT", "/api/v1/workers/" + wid + "/heartbeat", `{}`},
		{"checkout", "GET", "/api/v1/workers/" + wid + "/work", ``},
		{"status", "PUT", "/api/v1/workers/" + wid + "/tasks/" + taskID + "/status", `{"state":"RUNNING"}`},
		{"complete", "PUT", "/api/v1/workers/" + wid + "/tasks/" + taskID + "/complete", `{"state":"FAILED","exit_code":1}`},
		{"deregister", "DELETE", "/api/v1/workers/" + wid, ``},
	}
	for _, rt := range routes {
		t.Run(rt.name+"/foreign key", func(t *testing.T) {
			w, _ := keyedDo(t, srv, "secretB", rt.method, rt.path, rt.body)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403, body=%s", w.Code, w.Body.String())
			}
		})
	}
	// Foreign attempts changed nothing.
	got, _ := srv.store.GetTask(context.Background(), taskID)
	if got.State != model.TaskStateQueued {
		t.Errorf("task state = %s, want QUEUED", got.State)
	}
	if wk, _ := srv.store.GetWorker(context.Background(), wid); wk == nil {
		t.Fatal("worker was deleted by a foreign key")
	}

	// A task already assigned to the worker, so the owner can post a status.
	statusTask := &model.Task{ID: "task_bind_status", SubmissionID: subID, StepID: "step2",
		State: model.TaskStateQueued, ExecutorType: model.ExecutorTypeWorker, ExternalID: wid,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1}
	if err := srv.store.CreateTask(context.Background(), statusTask); err != nil {
		t.Fatalf("seed status task: %v", err)
	}

	// The owning key works on the same routes.
	ownerRoutes := []struct {
		name, method, path, body string
	}{
		{"heartbeat", "PUT", "/api/v1/workers/" + wid + "/heartbeat", `{}`},
		{"status", "PUT", "/api/v1/workers/" + wid + "/tasks/task_bind_status/status", `{"state":"RUNNING"}`},
		{"checkout", "GET", "/api/v1/workers/" + wid + "/work", ``},
		{"complete", "PUT", "/api/v1/workers/" + wid + "/tasks/" + taskID + "/complete", `{"state":"SUCCESS","exit_code":0}`},
		{"deregister", "DELETE", "/api/v1/workers/" + wid, ``},
	}
	for _, rt := range ownerRoutes {
		t.Run(rt.name+"/owner key", func(t *testing.T) {
			w, _ := keyedDo(t, srv, "secretA", rt.method, rt.path, rt.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWorkerKeyBinding_KeyIDStoredAtRegistration(t *testing.T) {
	srv, st := keyedServer(t)
	wid := keyedRegister(t, srv, "secretA", "default")
	wk, _ := st.GetWorker(context.Background(), wid)
	if wk == nil || wk.KeyID != "keyA" {
		t.Fatalf("worker key_id = %+v, want keyA", wk)
	}
}

func TestSharedKey_TwoWorkers_BothWork(t *testing.T) {
	srv, _ := keyedServer(t)
	w1 := keyedRegister(t, srv, "secretA", "default")
	w2 := keyedRegister(t, srv, "secretA", "default")
	for _, id := range []string{w1, w2} {
		if w, _ := keyedDo(t, srv, "secretA", "PUT", "/api/v1/workers/"+id+"/heartbeat", `{}`); w.Code != http.StatusOK {
			t.Errorf("heartbeat %s status=%d, want 200", id, w.Code)
		}
		if w, _ := keyedDo(t, srv, "secretA", "GET", "/api/v1/workers/"+id+"/work", ""); w.Code != http.StatusNoContent {
			t.Errorf("checkout %s status=%d, want 204", id, w.Code)
		}
	}
}

func TestLegacyWorker_Adoption(t *testing.T) {
	tests := []struct {
		name       string
		group      string
		secret     string
		wantStatus int
		wantKeyID  string
	}{
		{"allowed group adopts", "g1", "secretG1", http.StatusOK, "keyG1"},
		{"unrestricted key adopts", "g2", "secretA", http.StatusOK, "keyA"},
		{"disallowed group refused", "g2", "secretG1", http.StatusForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := keyedServer(t)
			now := time.Now().UTC()
			legacy := &model.Worker{
				ID: "wrk_legacy", Name: "legacy", Group: tc.group, State: model.WorkerStateOnline,
				Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now,
				Labels: map[string]string{}, Datasets: map[string]string{},
			}
			if err := st.CreateWorker(context.Background(), legacy); err != nil {
				t.Fatalf("create legacy worker: %v", err)
			}
			w, _ := keyedDo(t, srv, tc.secret, "PUT", "/api/v1/workers/wrk_legacy/heartbeat", `{}`)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d, body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			got, _ := st.GetWorker(context.Background(), "wrk_legacy")
			if got.KeyID != tc.wantKeyID {
				t.Errorf("key_id = %q, want %q", got.KeyID, tc.wantKeyID)
			}
			if tc.wantKeyID != "" {
				// Once adopted, a different key is locked out.
				if w, _ := keyedDo(t, srv, "secretB", "PUT", "/api/v1/workers/wrk_legacy/heartbeat", `{}`); w.Code != http.StatusForbidden {
					t.Errorf("post-adoption foreign status=%d, want 403", w.Code)
				}
			}
		})
	}
}

func TestCheckout_NarrowedGroupKeyRefused(t *testing.T) {
	srv, st := testServerWithStore()
	ctx := context.Background()
	raw, hash, prefix, _ := model.GenerateWorkerKey()
	key := &model.WorkerKey{ID: "wk_narrow", Label: "n", KeyHash: hash, KeyPrefix: prefix,
		Groups: []string{"g1", "g2"}, CreatedAt: time.Now().UTC()}
	if err := st.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("create key: %v", err)
	}
	wid := keyedRegister(t, srv, raw, "g2")
	if w, _ := keyedDo(t, srv, raw, "GET", "/api/v1/workers/"+wid+"/work", ""); w.Code != http.StatusNoContent {
		t.Fatalf("checkout before narrowing status=%d, want 204", w.Code)
	}

	key.Groups = []string{"g1"}
	if err := st.UpdateWorkerKey(ctx, key); err != nil {
		t.Fatalf("narrow key: %v", err)
	}
	if w, _ := keyedDo(t, srv, raw, "GET", "/api/v1/workers/"+wid+"/work", ""); w.Code != http.StatusForbidden {
		t.Fatalf("checkout after narrowing status=%d, want 403, body=%s", w.Code, w.Body.String())
	}
}

func TestListWorkers_ScopedToKey(t *testing.T) {
	srv, _ := keyedServer(t)
	a1 := keyedRegister(t, srv, "secretA", "default")
	b1 := keyedRegister(t, srv, "secretB", "default")

	ids := func(secret string) []string {
		w, env := keyedDo(t, srv, secret, "GET", "/api/v1/workers/", "")
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d", w.Code)
		}
		var list []map[string]any
		json.Unmarshal(env.Data, &list)
		var out []string
		for _, m := range list {
			out = append(out, m["id"].(string))
		}
		return out
	}
	if got := ids("secretA"); len(got) != 1 || got[0] != a1 {
		t.Errorf("key A list = %v, want [%s]", got, a1)
	}
	if got := ids("secretB"); len(got) != 1 || got[0] != b1 {
		t.Errorf("key B list = %v, want [%s]", got, b1)
	}
}

func TestLoadWorkerKeyConfig_RejectsReservedAndDuplicateIDs(t *testing.T) {
	tests := []struct {
		name string
		json string
		bad  bool
	}{
		{"reserved none", `{"keys":{"k1":{"id":"none"}}}`, true},
		{"duplicate ids", `{"keys":{"k1":{"id":"dup"},"k2":{"id":"dup"}}}`, true},
		{"empty ids may repeat", `{"keys":{"k1":{},"k2":{}}}`, false},
		{"distinct ids", `{"keys":{"k1":{"id":"a"},"k2":{"id":"b"}}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadWorkerKeyConfig(path)
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v, want error=%v", err, tc.bad)
			}
		})
	}
}

// A key whose id is "none" must not be treated as the anonymous context.
func TestWorkerKeyBinding_KeyNamedNoneIsNotAnonymous(t *testing.T) {
	cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		"secretN": {ID: "none"},
		"secretB": {ID: "keyB"},
	}}
	srv, _ := testServerWithStore(WithWorkerKeyConfig(cfg))
	wid := keyedRegister(t, srv, "secretB", "default")
	if w, _ := keyedDo(t, srv, "secretN", "PUT", "/api/v1/workers/"+wid+"/heartbeat", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", w.Code)
	}
}

func TestAdoptWorkerKey_CAS(t *testing.T) {
	_, st := testServerWithStore()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_cas", Name: "c", Group: "default",
		State: model.WorkerStateOnline, Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now,
		Labels: map[string]string{}, Datasets: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.AdoptWorkerKey(ctx, "wrk_cas", "k1"); err != nil || !ok {
		t.Fatalf("first adopt = %v, %v; want true", ok, err)
	}
	if ok, _ := st.AdoptWorkerKey(ctx, "wrk_cas", "k2"); ok {
		t.Fatal("second adopt succeeded; must not overwrite")
	}
	if w, _ := st.GetWorker(ctx, "wrk_cas"); w.KeyID != "k1" {
		t.Errorf("key_id = %q, want k1", w.KeyID)
	}
}

func TestOpenAccessRegistration_UnboundThenAdopted(t *testing.T) {
	srv, st := testServerWithStore()
	wid := registerTestWorker(t, srv)
	if w, _ := st.GetWorker(context.Background(), wid); w.KeyID != "" {
		t.Fatalf("open-access key_id = %q, want empty", w.KeyID)
	}
	// Keys get enabled later: swap in an authenticator over the same store.
	// (mutate in place: the router captured the authenticator pointer.)
	srv.workerAuth.static = &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
		"secretA": {ID: "keyA"}, "secretB": {ID: "keyB"}}}
	if w, _ := keyedDo(t, srv, "secretA", "PUT", "/api/v1/workers/"+wid+"/heartbeat", `{}`); w.Code != http.StatusOK {
		t.Fatalf("adopting heartbeat status=%d, want 200", w.Code)
	}
	if w, _ := keyedDo(t, srv, "secretB", "PUT", "/api/v1/workers/"+wid+"/heartbeat", `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("foreign heartbeat status=%d, want 403", w.Code)
	}
}
