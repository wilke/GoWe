package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// getHosts GETs /api/v1/hosts with an optional bearer token and decodes the
// hosts list.
func getHosts(t *testing.T, srv *Server, token string) ([]model.Host, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/hosts", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var data hostsData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return data.Hosts, w.Body.String()
}

// TestHandleListHosts registers workers and sends a usage heartbeat through
// the worker handlers, checks out a task, and verifies the per-host
// aggregate, including the /fleet hostname policy for non-admin callers.
func TestHandleListHosts(t *testing.T) {
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	srv := testServer(WithAdminConfig(adminCfg))

	const hostID = "0123456789abcdef"
	w1 := registerWorkerID(t, srv, registerBody(t, map[string]any{
		"name": "w1", "hostname": "node-1.internal", "max_cpus": 64, "max_mem_mb": 500000, "slots": 1,
		"host_id": hostID, "host_cpus": 192, "host_mem_mb": 1547000, "gpus": testH200GPUs,
		// The checked-out task requests nothing, so it is charged this share.
		"default_task_cores": 4, "default_task_ram_mb": 8000,
	}))
	w2 := registerWorkerID(t, srv, registerBody(t, map[string]any{
		"name": "w2", "hostname": "node-1.internal", "max_cpus": 32, "max_mem_mb": 200000,
		"host_id": hostID, "host_cpus": 192, "host_mem_mb": 1547000,
	}))
	legacy := registerWorkerID(t, srv, registerBody(t, map[string]any{"name": "old", "hostname": "old-host.internal"}))

	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_hosts_1")
	if checked := checkoutTask(t, srv, w1); checked.ID != taskID {
		t.Fatalf("checked out %s, want %s", checked.ID, taskID)
	}
	if w, _ := doPut(t, srv, "/api/v1/workers/"+w1+"/heartbeat", usageHeartbeatBody(taskID)); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: status=%d, body=%s", w.Code, w.Body.String())
	}

	tests := []struct {
		name          string
		token         string
		wantHostnames bool
		legacyID      string
	}{
		{"admin sees hostnames", adminAuthToken, true, model.LegacyHostIDPrefix + "old-host.internal"},
		{"non-admin user sees no hostnames", userAuthToken, false, "legacy:" + legacy},
		{"anonymous sees no hostnames", "", false, "legacy:" + legacy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts, body := getHosts(t, srv, tt.token)
			if !tt.wantHostnames && strings.Contains(body, ".internal") {
				t.Errorf("response leaked a hostname: %s", body)
			}
			if len(hosts) != 2 {
				t.Fatalf("hosts = %d, want 2: %s", len(hosts), body)
			}

			h := hosts[0]
			if h.HostID != hostID || h.Legacy {
				t.Errorf("hosts[0] id = %q legacy=%v, want %q, not legacy", h.HostID, h.Legacy, hostID)
			}
			if tt.wantHostnames && (len(h.Hostnames) != 1 || h.Hostnames[0] != "node-1.internal") {
				t.Errorf("hostnames = %v, want [node-1.internal]", h.Hostnames)
			}
			if h.CPUs != 192 || h.MemMB != 1547000 {
				t.Errorf("physical = %d cpus / %d MiB, want 192 / 1547000", h.CPUs, h.MemMB)
			}
			if h.ClaimedCPUs != 96 || h.ClaimedMemMB != 700000 {
				t.Errorf("claimed = %d cpus / %d MiB, want 96 / 700000", h.ClaimedCPUs, h.ClaimedMemMB)
			}
			if h.ChargedCPUs != 4 || h.ChargedMemMB != 8000 {
				t.Errorf("charged = %d cpus / %d MiB, want 4 / 8000", h.ChargedCPUs, h.ChargedMemMB)
			}
			if l := hosts[1]; l.ChargedCPUs != 0 || l.ChargedMemMB != 0 {
				t.Errorf("legacy host charged = %d cpus / %d MiB, want 0 / 0", l.ChargedCPUs, l.ChargedMemMB)
			}
			if !strings.Contains(body, `"charged_cpus":4`) || !strings.Contains(body, `"charged_mem_mb":8000`) {
				t.Errorf("response lacks charged_cpus / charged_mem_mb: %s", body)
			}
			if h.Usage == nil || h.UsageAt == nil || h.Usage.Load1 != 1.5 || h.Usage.MemAvailableMB != 1024 {
				t.Errorf("usage = %+v at %v, want load1 1.5, 1024 MiB available", h.Usage, h.UsageAt)
			}
			if len(h.GPUs) != 2 {
				t.Fatalf("gpus = %+v, want 2", h.GPUs)
			}
			if g := h.GPUs[0]; g.UUID != testH200GPUs[0].UUID || g.MemUsedMB == nil || *g.MemUsedMB != 1024 || g.UtilPct == nil || *g.UtilPct != 42 {
				t.Errorf("gpu 0 = %+v, want %s with 1024 MiB used, 42%%", g, testH200GPUs[0].UUID)
			}
			if g := h.GPUs[1]; g.Index != 1 || g.MemUsedMB != nil || g.UtilPct != nil {
				t.Errorf("gpu 1 = %+v, want index 1 without usage", g)
			}
			if len(h.Workers) != 2 || h.Workers[0].ID != w1 || h.Workers[1].ID != w2 {
				t.Fatalf("workers = %+v, want [%s %s]", h.Workers, w1, w2)
			}
			if h.Workers[0].RunningTasks != 1 || h.Workers[1].RunningTasks != 0 {
				t.Errorf("worker running tasks = %d, %d, want 1, 0", h.Workers[0].RunningTasks, h.Workers[1].RunningTasks)
			}
			if h.WorkersOnline != 2 || h.WorkersTotal != 2 || h.RunningTasks != 1 {
				t.Errorf("online/total/running = %d/%d/%d, want 2/2/1", h.WorkersOnline, h.WorkersTotal, h.RunningTasks)
			}

			l := hosts[1]
			if l.HostID != tt.legacyID || !l.Legacy {
				t.Errorf("hosts[1] id = %q legacy=%v, want %q, legacy", l.HostID, l.Legacy, tt.legacyID)
			}
			if len(l.Workers) != 1 || l.Workers[0].ID != legacy {
				t.Errorf("legacy workers = %+v, want [%s]", l.Workers, legacy)
			}
			if (len(l.Hostnames) > 0) != tt.wantHostnames {
				t.Errorf("legacy hostnames = %v, want shown=%v", l.Hostnames, tt.wantHostnames)
			}
		})
	}
}

// TestHandleListHosts_Empty verifies an empty fleet yields an empty (not
// null) hosts list.
func TestHandleListHosts_Empty(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/hosts")
	if got := string(env.Data); got != `{"hosts":[]}` {
		t.Errorf("data = %s, want {\"hosts\":[]}", got)
	}
}

// TestHosts_AuthMatchesFleet runs the TestFleetRoster_Auth cases against both
// endpoints: /hosts must sit behind exactly the same user auth as /fleet.
func TestHosts_AuthMatchesFleet(t *testing.T) {
	newKeyedServer := func() *Server {
		cfg := &WorkerKeyConfig{Keys: map[string]WorkerKeyEntry{
			"a-worker-key": {ID: "node-a", Groups: []string{"default"}},
		}}
		cfg.build()
		return testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false}), WithWorkerKeyConfig(cfg))
	}
	tests := []struct {
		name   string
		srv    func() *Server
		header string
		value  string
		want   int
	}{
		{"no credentials, anonymous disabled -> 401",
			func() *Server { return testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false})) }, "", "", http.StatusUnauthorized},
		{"user token works even with worker keys configured", newKeyedServer, "Authorization", "Bearer " + userAuthToken, http.StatusOK},
		{"plain X-Worker-Key alone does not satisfy user auth", newKeyedServer, "X-Worker-Key", "a-worker-key", http.StatusUnauthorized},
	}
	for _, path := range []string{"/api/v1/fleet", "/api/v1/hosts"} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				req := httptest.NewRequest("GET", path, nil)
				if tt.header != "" {
					req.Header.Set(tt.header, tt.value)
				}
				w := httptest.NewRecorder()
				tt.srv().ServeHTTP(w, req)
				if w.Code != tt.want {
					t.Fatalf("status=%d, want %d, body=%s", w.Code, tt.want, w.Body.String())
				}
			})
		}
	}
}
