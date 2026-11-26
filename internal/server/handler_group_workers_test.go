package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Worker peer address, the manager worker list and worker removal.

// spoofHeaders are the headers middleware.RealIP takes the client address
// from; none of them may become the peer address.
var spoofHeaders = map[string]string{
	"X-Forwarded-For": "203.0.113.1",
	"X-Real-IP":       "203.0.113.2",
	"True-Client-IP":  "203.0.113.3",
}

// peerDo sends a worker request from remoteAddr with the spoof headers set.
func peerDo(t *testing.T, srv *Server, method, path, body, remoteAddr string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	for k, v := range spoofHeaders {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

// assertNoPeerAddr fails when body carries a peer_addr field or one of addrs.
func assertNoPeerAddr(t *testing.T, what, body string, addrs ...string) {
	t.Helper()
	if strings.Contains(body, "peer_addr") {
		t.Errorf("%s carries peer_addr: %s", what, body)
	}
	for _, a := range addrs {
		if strings.Contains(body, a) {
			t.Errorf("%s shows the peer address %s: %s", what, a, body)
		}
	}
}

// The peer address is taken from the connection, never from the headers
// middleware.RealIP honours; registration stores it, every heartbeat updates
// it, and neither the worker answers nor the user-facing roster views show it.
func TestWorkerPeerAddr_FromConnection(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newWorkerGroupServer(t)

	w, env := peerDo(t, srv, "POST", "/api/v1/workers/", `{"name":"w","hostname":"h","runtime":"none"}`, "192.0.2.10:4242")
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	assertNoPeerAddr(t, "registration answer", string(env.Data), "192.0.2.10")
	var reg model.Worker
	if err := json.Unmarshal(env.Data, &reg); err != nil || reg.ID == "" {
		t.Fatalf("registration answer: %v %s", err, env.Data)
	}
	got, err := st.GetWorker(ctx, reg.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	if got.PeerAddr != "192.0.2.10" {
		t.Errorf("stored peer_addr after registration = %q, want 192.0.2.10 (the connection, not a header)", got.PeerAddr)
	}

	// A heartbeat from another address updates it; IPv6 loses its brackets.
	for _, c := range []struct{ remote, want string }{
		{"198.51.100.7:5555", "198.51.100.7"},
		{"[2001:db8::5]:6000", "2001:db8::5"},
	} {
		if w, _ := peerDo(t, srv, "PUT", "/api/v1/workers/"+reg.ID+"/heartbeat", `{}`, c.remote); w.Code != http.StatusOK {
			t.Fatalf("heartbeat from %s: %d %s", c.remote, w.Code, w.Body.String())
		}
		got, err = st.GetWorker(ctx, reg.ID)
		if err != nil || got == nil {
			t.Fatalf("GetWorker: %v %v", got, err)
		}
		if got.PeerAddr != c.want {
			t.Errorf("stored peer_addr after heartbeat from %s = %q, want %q", c.remote, got.PeerAddr, c.want)
		}
	}

	// GET /api/v1/workers (worker callers) never shows it.
	w, env = peerDo(t, srv, "GET", "/api/v1/workers/", "", "192.0.2.10:1")
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), reg.ID) {
		t.Fatalf("list workers: %d %s", w.Code, w.Body.String())
	}
	assertNoPeerAddr(t, "GET /api/v1/workers", string(env.Data), "2001:db8::5")

	// Nor do the roster views, for a plain user or an admin.
	for _, path := range []string{"/api/v1/fleet", "/api/v1/hosts"} {
		for _, token := range []string{plainToken, adminAuthToken} {
			w, env := wgDo(t, srv, "GET", path, "", token)
			if w.Code != http.StatusOK || !strings.Contains(string(env.Data), reg.ID) {
				t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
			}
			assertNoPeerAddr(t, "GET "+path, string(env.Data), "2001:db8::5")
		}
	}
}

func TestPeerAddr_Sanitised(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"192.0.2.1:80", "192.0.2.1"},
		{"[::1]:80", "::1"},
		{"192.0.2.1", "192.0.2.1"},
		{"bad\x00addr\n", "badaddr"},
		{strings.Repeat("a", 100), strings.Repeat("a", maxPeerAddrBytes)},
		{"", ""},
		{"@", ""}, // unix-socket listener: unknown
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKeyPeerAddr, tt.raw))
		if got := peerAddr(req); got != tt.want {
			t.Errorf("peerAddr(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
	// Without the middleware nothing is trusted.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:80"
	if got := peerAddr(req); got != "" {
		t.Errorf("peerAddr without the middleware = %q, want empty", got)
	}
}

// An unknown peer address (empty, or "@" from a unix-socket listener) is
// stored as "" at registration, and a heartbeat with an unknown one keeps
// the stored address.
func TestWorkerPeerAddr_UnknownKeepsStored(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newWorkerGroupServer(t)

	w, env := peerDo(t, srv, "POST", "/api/v1/workers/", `{"name":"w","hostname":"h","runtime":"none"}`, "@")
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var reg model.Worker
	if err := json.Unmarshal(env.Data, &reg); err != nil || reg.ID == "" {
		t.Fatalf("registration answer: %v %s", err, env.Data)
	}
	stored := func() string {
		t.Helper()
		got, err := st.GetWorker(ctx, reg.ID)
		if err != nil || got == nil {
			t.Fatalf("GetWorker: %v %v", got, err)
		}
		return got.PeerAddr
	}
	if got := stored(); got != "" {
		t.Errorf("peer_addr after a registration over a unix socket = %q, want empty", got)
	}

	for _, c := range []struct{ remote, want string }{
		{"192.0.2.30:1234", "192.0.2.30"},
		{"@", "192.0.2.30"},
		{"", "192.0.2.30"},
		{"198.51.100.30:1", "198.51.100.30"},
	} {
		if w, _ := peerDo(t, srv, "PUT", "/api/v1/workers/"+reg.ID+"/heartbeat", `{}`, c.remote); w.Code != http.StatusOK {
			t.Fatalf("heartbeat from %q: %d %s", c.remote, w.Code, w.Body.String())
		}
		if got := stored(); got != c.want {
			t.Errorf("peer_addr after a heartbeat from %q = %q, want %q", c.remote, got, c.want)
		}
	}
}

// workerListRecordingStore remembers the workers its list methods returned.
type workerListRecordingStore struct {
	store.Store
	listed []*model.Worker
}

func (s *workerListRecordingStore) ListWorkers(ctx context.Context) ([]*model.Worker, error) {
	ws, err := s.Store.ListWorkers(ctx)
	s.listed = append(s.listed, ws...)
	return ws, err
}

func (s *workerListRecordingStore) ListWorkersByKey(ctx context.Context, keyID string) ([]*model.Worker, error) {
	ws, err := s.Store.ListWorkersByKey(ctx, keyID)
	s.listed = append(s.listed, ws...)
	return ws, err
}

// GET /api/v1/workers blanks the peer address on copies: the objects the
// store returned keep it.
func TestListWorkers_PeerAddrBlankedOnCopies(t *testing.T) {
	srv, st, _ := newWorkerGroupServer(t)
	now := time.Now().UTC()
	seedWorker(t, st, "wrk_p", "p-node", "default", model.WorkerStateOnline, now, "", "192.0.2.60")
	rec := &workerListRecordingStore{Store: st}
	srv.store = rec

	w, env := peerDo(t, srv, "GET", "/api/v1/workers/", "", "192.0.2.61:1")
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), "wrk_p") {
		t.Fatalf("list workers: %d %s", w.Code, w.Body.String())
	}
	assertNoPeerAddr(t, "GET /api/v1/workers", string(env.Data), "192.0.2.60")
	if len(rec.listed) != 1 || rec.listed[0].ID != "wrk_p" {
		t.Fatalf("listed = %+v, want wrk_p", rec.listed)
	}
	if got := rec.listed[0].PeerAddr; got != "192.0.2.60" {
		t.Errorf("the store's worker object was modified: peer_addr = %q, want 192.0.2.60", got)
	}
}

// seedWorker stores a worker directly.
func seedWorker(t *testing.T, st store.Store, id, name, group string, state model.WorkerState, lastSeen time.Time, keyID, peer string) {
	t.Helper()
	if err := st.CreateWorker(context.Background(), &model.Worker{
		ID: id, Name: name, Hostname: name + ".example", Group: group, State: state,
		Runtime: model.RuntimeNone, Version: "v1", LastSeen: lastSeen, RegisteredAt: lastSeen,
		KeyID: keyID, PeerAddr: peer,
	}); err != nil {
		t.Fatal(err)
	}
}

type groupWorkersBody struct {
	Total   int           `json:"total"`
	Workers []groupWorker `json:"workers"`
}

// A manager lists the workers of their group, whatever key registered them,
// sorted by name, with the peer address; key ids of keys that are not the
// group's own are "other".
func TestGroupWorkers_List(t *testing.T) {
	srv, st, _ := newManagedGroupServer(t)
	storeKey(t, st, "wk_g", []string{"g"})
	storeKey(t, st, "wk_gh", []string{"g", "h"})
	old := time.Now().UTC().Add(-time.Hour)
	now := time.Now().UTC()
	seedWorker(t, st, "wrk_b", "b-node", "g", model.WorkerStateOnline, now, "wk_g", "192.0.2.2")
	seedWorker(t, st, "wrk_a", "a-node", "g", model.WorkerStateOffline, old, "wk_gh", "192.0.2.1")
	seedWorker(t, st, "wrk_c", "c-node", "g", model.WorkerStateOnline, now, "", "192.0.2.3")
	seedWorker(t, st, "wrk_h", "h-node", "h", model.WorkerStateOnline, now, "", "192.0.2.4")
	seedWorker(t, st, "wrk_d", "d-node", "default", model.WorkerStateOnline, now, "", "192.0.2.5")

	w, env := mgDo(t, srv, "GET", managedGroupsPath+"/g/workers", "", mgrToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	body := decode[groupWorkersBody](t, env)
	if body.Total != 3 || len(body.Workers) != 3 {
		t.Fatalf("workers = %+v, want 3", body)
	}
	want := []struct{ id, key, peer string }{
		{"wrk_a", groupWorkerOtherKey, "192.0.2.1"},
		{"wrk_b", "wk_g", "192.0.2.2"},
		{"wrk_c", "", "192.0.2.3"},
	}
	for i, wnt := range want {
		g := body.Workers[i]
		if g.ID != wnt.id || g.KeyID != wnt.key || g.PeerAddr != wnt.peer {
			t.Errorf("worker %d = %+v, want id %s key_id %q peer_addr %s", i, g, wnt.id, wnt.key, wnt.peer)
		}
	}
	if a := body.Workers[0]; a.State != model.WorkerStateOffline || a.Hostname != "a-node.example" || a.Version != "v1" || a.Runtime != model.RuntimeNone {
		t.Errorf("projection = %+v", a)
	}
	for _, leak := range []string{"wk_gh", "wrk_h", "wrk_d", "labels", "datasets"} {
		if strings.Contains(string(env.Data), leak) {
			t.Errorf("the list must not contain %q: %s", leak, env.Data)
		}
	}

	// A server admin may list any group with a rule.
	w, env = mgDo(t, srv, "GET", managedGroupsPath+"/h/workers", "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list h: %d %s", w.Code, w.Body.String())
	}
	if body := decode[groupWorkersBody](t, env); body.Total != 1 || body.Workers[0].ID != "wrk_h" || body.Workers[0].PeerAddr != "192.0.2.4" {
		t.Errorf("admin list h = %+v", body)
	}
	if !strings.Contains(string(env.Data), `"workers":[`) {
		t.Errorf("workers must be a list: %s", env.Data)
	}
}

// The worker routes refuse a manager of another group, a plain user and an
// anonymous caller.
func TestGroupWorkers_Refused(t *testing.T) {
	srv, st, _ := newManagedGroupServer(t)
	old := time.Now().UTC().Add(-time.Hour)
	seedWorker(t, st, "wrk_h", "h-node", "h", model.WorkerStateOffline, old, "", "192.0.2.4")
	seedWorker(t, st, "wrk_g", "g-node", "g", model.WorkerStateOffline, old, "", "192.0.2.5")

	for _, c := range []struct {
		who, token, method, path string
		want                     int
	}{
		{"manager of g", mgrToken, "GET", managedGroupsPath + "/h/workers", http.StatusForbidden},
		{"manager of g", mgrToken, "DELETE", managedGroupsPath + "/h/workers/wrk_h", http.StatusForbidden},
		{"plain user", plainToken, "GET", managedGroupsPath + "/g/workers", http.StatusForbidden},
		{"plain user", plainToken, "DELETE", managedGroupsPath + "/g/workers/wrk_g", http.StatusForbidden},
		{"anonymous", "", "GET", managedGroupsPath + "/g/workers", http.StatusUnauthorized},
		{"anonymous", "", "DELETE", managedGroupsPath + "/g/workers/wrk_g", http.StatusUnauthorized},
		{"manager of g", mgrToken, "DELETE", "/api/v1/admin/workers/wrk_g", http.StatusForbidden},
		{"plain user", plainToken, "DELETE", "/api/v1/admin/workers/wrk_g", http.StatusForbidden},
	} {
		w, env := mgDo(t, srv, c.method, c.path, "", c.token)
		if w.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d: %s", c.who, c.method, c.path, w.Code, c.want, w.Body.String())
		}
		if c.method == "GET" {
			assertNoPeerAddr(t, c.who+" "+c.path, string(env.Data), "192.0.2.4", "192.0.2.5")
		}
	}
	for _, id := range []string{"wrk_h", "wrk_g"} {
		if wk, _ := st.GetWorker(context.Background(), id); wk == nil {
			t.Errorf("%s was removed by a refused request", id)
		}
	}
}

// seedRunningTask stores a RUNNING worker task checked out by workerID.
func seedRunningTask(t *testing.T, srv *Server, subID, id, workerID string) {
	t.Helper()
	started := time.Now().UTC()
	if err := srv.store.CreateTask(context.Background(), &model.Task{
		ID: id, SubmissionID: subID, StepID: "step1", State: model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker, ExternalID: workerID, StartedAt: &started,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
	}); err != nil {
		t.Fatal(err)
	}
}

// A manager removes an offline worker of their group that has been silent
// for 3 x the worker timeout; its RUNNING task is requeued. An online
// worker, or one seen within 3 x the timeout (even past one timeout, when
// the reaper has marked it offline), is refused with 409; a worker of
// another group, or none, is 404.
func TestGroupWorkers_Remove(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	const subID = "sub_remove" // tasks do not reference their submission row
	old := time.Now().UTC().Add(-time.Hour)
	recent := time.Now().UTC().Add(-time.Second)
	pastTimeout := time.Now().UTC().Add(-(defaultWorkerTimeout + defaultWorkerTimeout/2))
	seedWorker(t, st, "wrk_online", "on", "g", model.WorkerStateOnline, old, "", "")
	seedWorker(t, st, "wrk_recent", "rec", "g", model.WorkerStateOffline, recent, "", "")
	seedWorker(t, st, "wrk_past", "past", "g", model.WorkerStateOffline, pastTimeout, "", "")
	seedWorker(t, st, "wrk_gone", "gone", "g", model.WorkerStateOffline, old, "", "")
	seedWorker(t, st, "wrk_h", "h", "h", model.WorkerStateOffline, old, "", "")
	seedRunningTask(t, srv, subID, "task_on_online", "wrk_online")
	seedRunningTask(t, srv, subID, "task_on_gone", "wrk_gone")
	logs := captureLogs(srv)

	for _, c := range []struct {
		id      string
		want    int
		message string
	}{
		{"wrk_online", http.StatusConflict, "is online"},
		{"wrk_recent", http.StatusConflict, "last seen"},
		{"wrk_past", http.StatusConflict, "no heartbeat for 1m30s"},
		{"wrk_h", http.StatusNotFound, ""},
		{"wrk_missing", http.StatusNotFound, ""},
	} {
		w, env := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/workers/"+c.id, "", mgrToken)
		if w.Code != c.want {
			t.Errorf("remove %s: %d, want %d: %s", c.id, w.Code, c.want, w.Body.String())
			continue
		}
		if c.want == http.StatusConflict && (env.Error == nil || env.Error.Code != model.ErrConflict || !strings.Contains(env.Error.Message, c.message)) {
			t.Errorf("remove %s: error = %+v, want CONFLICT containing %q", c.id, env.Error, c.message)
		}
	}
	for _, id := range []string{"wrk_online", "wrk_recent", "wrk_past", "wrk_h"} {
		if wk, _ := st.GetWorker(ctx, id); wk == nil {
			t.Errorf("%s was removed", id)
		}
	}
	if task, _ := st.GetTask(ctx, "task_on_online"); task == nil || task.State != model.TaskStateRunning || task.ExternalID != "wrk_online" {
		t.Errorf("the task of the online worker = %+v, want untouched", task)
	}

	w, env := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/workers/wrk_gone", "", mgrToken)
	if w.Code != http.StatusOK || !strings.Contains(string(env.Data), `"removed":true`) {
		t.Fatalf("remove wrk_gone: %d %s", w.Code, w.Body.String())
	}
	if wk, _ := st.GetWorker(ctx, "wrk_gone"); wk != nil {
		t.Error("wrk_gone is still stored")
	}
	if task, _ := st.GetTask(ctx, "task_on_gone"); task == nil || task.State != model.TaskStateQueued || task.ExternalID != "" {
		t.Errorf("the task of the removed worker = %+v, want QUEUED", task)
	}
	out := logs.String()
	for _, want := range []string{"worker removed", "actor=mgr", "worker_id=wrk_gone", "group=g"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	// A second time it is gone.
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/workers/wrk_gone", "", mgrToken); w.Code != http.StatusNotFound {
		t.Errorf("remove wrk_gone again: %d, want 404", w.Code)
	}
}

// A manager removed in the store behind the API's back cannot remove a
// worker: the write reloads the policy first.
func TestGroupWorkers_RemoveRechecksManager(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newManagedGroupServer(t)
	seedWorker(t, st, "wrk_g", "g", "g", model.WorkerStateOffline, time.Now().UTC().Add(-time.Hour), "", "")
	if !policy.IsManager("mgr", "g") {
		t.Fatal("mgr must manage g in the live policy")
	}
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "g", Users: []string{"alice"}, Managers: []string{}}); err != nil {
		t.Fatal(err)
	}
	if w, _ := mgDo(t, srv, "DELETE", managedGroupsPath+"/g/workers/wrk_g", "", mgrToken); w.Code != http.StatusForbidden {
		t.Errorf("remove by a manager removed in the store: %d, want 403", w.Code)
	}
	if wk, _ := st.GetWorker(ctx, "wrk_g"); wk == nil {
		t.Error("wrk_g was removed")
	}
}

// A server admin removes an offline, silent worker of any group through the
// admin route; the same refusals apply.
func TestAdminRemoveWorker(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	old := time.Now().UTC().Add(-time.Hour)
	seedWorker(t, st, "wrk_x", "x", "nogroup", model.WorkerStateOffline, old, "", "")
	seedWorker(t, st, "wrk_on", "on", "nogroup", model.WorkerStateOnline, old, "", "")
	seedWorker(t, st, "wrk_rec", "rec", "nogroup", model.WorkerStateOffline, time.Now().UTC(), "", "")
	seedWorker(t, st, "wrk_past", "past", "nogroup", model.WorkerStateOffline, time.Now().UTC().Add(-2*defaultWorkerTimeout), "", "")
	logs := captureLogs(srv)

	for _, c := range []struct {
		id   string
		want int
	}{
		{"wrk_on", http.StatusConflict},
		{"wrk_rec", http.StatusConflict},
		{"wrk_past", http.StatusConflict},
		{"wrk_missing", http.StatusNotFound},
		{"wrk_x", http.StatusOK},
	} {
		if w, _ := wgDo(t, srv, "DELETE", "/api/v1/admin/workers/"+c.id, "", adminAuthToken); w.Code != c.want {
			t.Errorf("admin remove %s: %d, want %d: %s", c.id, w.Code, c.want, w.Body.String())
		}
	}
	if wk, _ := st.GetWorker(ctx, "wrk_x"); wk != nil {
		t.Error("wrk_x is still stored")
	}
	for _, id := range []string{"wrk_on", "wrk_rec", "wrk_past"} {
		if wk, _ := st.GetWorker(ctx, id); wk == nil {
			t.Errorf("%s was removed", id)
		}
	}
	out := logs.String()
	for _, want := range []string{"worker removed", "actor=admin", "worker_id=wrk_x", "group=nogroup"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
