package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// seedFleetWorker creates a worker directly in the store (bypassing the
// worker-key-gated registration endpoint) so fleet roster tests can seed
// fixtures independent of worker auth configuration.
func seedFleetWorker(t *testing.T, srv *Server, id, name string, state model.WorkerState, hostname string) *model.Worker {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	w := &model.Worker{
		ID:           id,
		Name:         name,
		Hostname:     hostname,
		Group:        "default",
		State:        state,
		Runtime:      model.RuntimeApptainer,
		Version:      "abc123",
		GPUEnabled:   true,
		CurrentTask:  "task_xyz",
		LastSeen:     now,
		RegisteredAt: now,
	}
	if err := srv.store.CreateWorker(context.Background(), w); err != nil {
		t.Fatalf("seed worker %s: %v", id, err)
	}
	return w
}

// TestHandleFleetRoster_SlimProjection verifies the roster returns every
// seeded worker as a slim member, computes correct summary counts, and never
// leaks hostname (the field the admin/worker-key GET /workers view keeps but
// this user-facing roster deliberately omits).
func TestHandleFleetRoster_SlimProjection(t *testing.T) {
	srv := testServer()
	seedFleetWorker(t, srv, "wrk_fleet_1", "worker-a", model.WorkerStateOnline, "secret-host-a.internal")
	seedFleetWorker(t, srv, "wrk_fleet_2", "worker-b", model.WorkerStateOnline, "secret-host-b.internal")
	seedFleetWorker(t, srv, "wrk_fleet_3", "worker-c", model.WorkerStateOffline, "secret-host-c.internal")

	req := httptest.NewRequest("GET", "/api/v1/fleet", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if strings.Contains(body, "secret-host") {
		t.Errorf("response leaked hostname: %s", body)
	}

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var data fleetRosterData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}

	if len(data.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(data.Members))
	}
	// Sorted by name: worker-a, worker-b, worker-c.
	if data.Members[0].ID != "wrk_fleet_1" || data.Members[0].Name != "worker-a" {
		t.Errorf("members[0] = %+v, want wrk_fleet_1/worker-a", data.Members[0])
	}
	for _, m := range data.Members {
		if m.State == "" || m.Group == "" || m.Runtime == "" {
			t.Errorf("member %+v missing expected slim fields", m)
		}
		if m.GPUEnabled != true {
			t.Errorf("member %s: gpu_enabled = %v, want true", m.ID, m.GPUEnabled)
		}
		if m.CurrentTask != "task_xyz" {
			t.Errorf("member %s: current_task = %q, want task_xyz", m.ID, m.CurrentTask)
		}
	}

	if data.Summary.Total != 3 {
		t.Errorf("summary.total = %d, want 3", data.Summary.Total)
	}
	if data.Summary.ByState["online"] != 2 {
		t.Errorf("summary.by_state[online] = %d, want 2", data.Summary.ByState["online"])
	}
	if data.Summary.ByState["offline"] != 1 {
		t.Errorf("summary.by_state[offline] = %d, want 1", data.Summary.ByState["offline"])
	}
}

// TestHandleFleetRoster_Empty verifies an empty worker set yields an empty
// (not nil-that-serializes-to-null) member list and zeroed summary.
func TestHandleFleetRoster_Empty(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/fleet")
	var data fleetRosterData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.Members) != 0 {
		t.Errorf("members = %d, want 0", len(data.Members))
	}
	if data.Summary.Total != 0 {
		t.Errorf("summary.total = %d, want 0", data.Summary.Total)
	}
}

// TestFleetRoster_Auth verifies GET /api/v1/fleet sits behind user auth (not
// worker-key auth): unauthenticated requests are refused, and a normal user
// token is accepted even when worker keys are configured on the server (the
// whole point of #251 — enabling --worker-keys must not 401 this endpoint).
func TestFleetRoster_Auth(t *testing.T) {
	t.Run("no credentials, anonymous disabled -> 401", func(t *testing.T) {
		srv := testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false}))
		req := httptest.NewRequest("GET", "/api/v1/fleet", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("user token works even with worker keys configured", func(t *testing.T) {
		cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
			"a-worker-key": {ID: "node-a", Groups: []string{"default"}},
		}}
		cfg.build()
		srv := testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false}), WithWorkerKeyConfig(cfg))
		seedFleetWorker(t, srv, "wrk_fleet_auth", "worker-auth", model.WorkerStateOnline, "host")

		req := httptest.NewRequest("GET", "/api/v1/fleet", nil)
		req.Header.Set("Authorization", "Bearer "+userAuthToken)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200 (user token, no worker key needed), body=%s", w.Code, w.Body.String())
		}

		var env envelope
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		var data fleetRosterData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if len(data.Members) != 1 {
			t.Fatalf("members = %d, want 1", len(data.Members))
		}
	})

	t.Run("plain X-Worker-Key alone does not satisfy user auth", func(t *testing.T) {
		cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
			"a-worker-key": {ID: "node-a", Groups: []string{"default"}},
		}}
		cfg.build()
		srv := testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false}), WithWorkerKeyConfig(cfg))

		req := httptest.NewRequest("GET", "/api/v1/fleet", nil)
		req.Header.Set("X-Worker-Key", "a-worker-key")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d, want 401 (worker key does not authenticate the user-auth group), body=%s", w.Code, w.Body.String())
		}
	})
}
