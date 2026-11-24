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

// TestHandleAdminFleet_HostsUnknownValues verifies the fleet page renders
// hosts with missing usage, a GPU without a usage sample, and only offline
// workers, showing a placeholder ("-" or "?") instead of a fabricated zero.
func TestHandleAdminFleet_HostsUnknownValues(t *testing.T) {
	now := time.Now().UTC()
	cpus, memMB, maxCPUs, maxMemMB := 8, int64(16384), 2, int64(2048)
	gpus := []model.GPUInfo{{Index: 0, UUID: "GPU-test-0", Name: "NVIDIA A100", MemTotalMB: 40960}}

	tests := []struct {
		name    string
		worker  *model.Worker
		want    []string
		wantNot []string
	}{
		{
			name: "host without usage and with unknown physical capacity",
			worker: &model.Worker{
				ID:       "wrk_nousage",
				Name:     "nousage",
				State:    model.WorkerStateOnline,
				HostID:   "aaaaaaaaaaaaaaaa",
				MaxCPUs:  &maxCPUs,
				MaxMemMB: &maxMemMB,
			},
			want: []string{
				"aaaaaaaaaaaaaaaa",
				"1 / 1 workers online",
				"2 / ?",
				"2.0 / ? GiB",
				// Load (1m) and available memory.
				`text-gray-900">-</dd>`,
			},
			wantNot: []string{`text-gray-900">0.00</dd>`, `text-gray-900">0.0 GiB</dd>`, "NVIDIA"},
		},
		{
			name: "GPU without a usage sample",
			worker: &model.Worker{
				ID:        "wrk_nogpusample",
				Name:      "nogpusample",
				State:     model.WorkerStateOnline,
				HostID:    "bbbbbbbbbbbbbbbb",
				HostCPUs:  &cpus,
				HostMemMB: &memMB,
				GPUs:      gpus,
				Usage:     &model.WorkerUsage{Host: &model.HostUsage{Load1: 1.5, MemTotalMB: 16384, MemAvailableMB: 4096}},
				UsageAt:   &now,
			},
			want: []string{
				"bbbbbbbbbbbbbbbb",
				"NVIDIA A100",
				"1.50",
				"- / 40.0",
				`text-gray-500">-</td>`,
			},
			wantNot: []string{`text-gray-500">0%</td>`, "0.0 / 40.0"},
		},
		{
			name: "host whose workers are all offline",
			worker: &model.Worker{
				ID:        "wrk_offline",
				Name:      "offline",
				State:     model.WorkerStateOffline,
				HostID:    "cccccccccccccccc",
				HostCPUs:  &cpus,
				HostMemMB: &memMB,
				GPUs:      gpus,
				Usage:     &model.WorkerUsage{Host: &model.HostUsage{Load1: 9.5, MemTotalMB: 16384, MemAvailableMB: 4096}},
				UsageAt:   &now,
			},
			want: []string{
				"cccccccccccccccc",
				"0 / 1 workers online",
				"0 / ?",
				"0.0 / ? GiB",
				`text-gray-900">-</dd>`,
			},
			wantNot: []string{"NVIDIA", "9.50"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			u := New(st, slog.Default(), Config{})

			tt.worker.Group = "default"
			tt.worker.Runtime = model.RuntimeApptainer
			tt.worker.LastSeen = now
			tt.worker.RegisteredAt = now
			if err := st.CreateWorker(context.Background(), tt.worker); err != nil {
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
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("body lacks %q", want)
				}
			}
			for _, wantNot := range tt.wantNot {
				if strings.Contains(body, wantNot) {
					t.Errorf("body contains %q", wantNot)
				}
			}
		})
	}
}
