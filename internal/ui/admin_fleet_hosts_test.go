package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestHandleAdminFleet_HostsSection verifies the fleet page renders the
// per-host view (#323) with a host's values, and flags over-claimed capacity
// only when claimed exceeds physical.
func TestHandleAdminFleet_HostsSection(t *testing.T) {
	tests := []struct {
		name         string
		maxCPUs      int
		wantClaimed  string
		wantOverflow bool
	}{
		{"within capacity", 4, "4 / 8", false},
		{"claimed CPUs exceed physical", 16, "16 / 8", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			u := New(st, slog.Default(), Config{})

			now := time.Now().UTC()
			cpus, memMB, maxMemMB := 8, int64(16384), int64(8192)
			gpuUsedMB, gpuUtil := int64(10240), 37.0
			w := &model.Worker{
				ID:           "wrk_host_1",
				Name:         "host-worker-1",
				Hostname:     "node-ok.internal",
				Group:        "default",
				State:        model.WorkerStateOnline,
				Runtime:      model.RuntimeApptainer,
				LastSeen:     now,
				RegisteredAt: now,
				HostID:       "0123456789abcdef",
				HostCPUs:     &cpus,
				HostMemMB:    &memMB,
				MaxCPUs:      &tt.maxCPUs,
				MaxMemMB:     &maxMemMB,
				GPUs:         []model.GPUInfo{{Index: 0, UUID: "GPU-test-0", Name: "NVIDIA A100", MemTotalMB: 40960}},
				Usage: &model.WorkerUsage{
					Host: &model.HostUsage{Load1: 2.25, MemTotalMB: 16384, MemAvailableMB: 4096},
					GPUs: []model.GPUUsage{{UUID: "GPU-test-0", MemUsedMB: &gpuUsedMB, UtilPct: &gpuUtil}},
				},
				UsageAt: &now,
			}
			if err := st.CreateWorker(context.Background(), w); err != nil {
				t.Fatalf("create worker: %v", err)
			}

			adminSess := &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}
			req := withSession(httptest.NewRequest(http.MethodGet, "/admin/fleet", nil), adminSess)
			rec := httptest.NewRecorder()
			u.HandleAdminFleet(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()

			// Host id, hostname, workers, running tasks, CPUs and memory
			// claimed / physical, load1, available memory, GPU name, GPU
			// memory used / total and utilisation.
			for _, want := range []string{
				`id="fleet-hosts"`,
				"0123456789abcdef",
				"node-ok.internal",
				"1 / 1 workers online",
				"0 running tasks",
				tt.wantClaimed,
				"8.0 / 16.0 GiB",
				"2.25",
				"4.0 GiB",
				"NVIDIA A100",
				"10.0 / 40.0",
				"37%",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("body lacks %q", want)
				}
			}
			if got := strings.Contains(body, "over-claimed"); got != tt.wantOverflow {
				t.Errorf("over-claimed warning shown = %v, want %v", got, tt.wantOverflow)
			}
		})
	}
}
