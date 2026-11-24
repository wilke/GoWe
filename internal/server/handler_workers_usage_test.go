package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// workerUsageJSON renders usage as JSON for readable, pointer-free comparison.
func workerUsageJSON(t *testing.T, u *model.WorkerUsage) string {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// listedWorker returns worker id as returned by GET /api/v1/workers.
func listedWorker(t *testing.T, srv *Server, id string) model.Worker {
	t.Helper()
	var listed []model.Worker
	if err := json.Unmarshal(doGet(t, srv, "/api/v1/workers/").Data, &listed); err != nil {
		t.Fatalf("decode workers: %v", err)
	}
	for _, w := range listed {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("worker %s not listed", id)
	return model.Worker{}
}

// usageHeartbeatBody is a heartbeat with valid usage for the first GPU of
// testH200GPUs.
func usageHeartbeatBody(running ...string) string {
	tasks, _ := json.Marshal(running)
	if running == nil {
		tasks = []byte("[]")
	}
	return fmt.Sprintf(`{"running_tasks":%s,"host":{"load1":1.5,"load5":0.5,"mem_total_mb":4096,"mem_available_mb":1024,"psi_cpu_some_avg10":2.5,"sampled_at":"2026-11-24T10:00:00Z"},"gpu_usage":[{"uuid":%q,"mem_used_mb":1024,"util_pct":42}]}`,
		tasks, testH200GPUs[0].UUID)
}

func TestWorkerHeartbeat_Usage(t *testing.T) {
	gpu0, gpu1 := testH200GPUs[0].UUID, testH200GPUs[1].UUID
	sampledAt := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body string
		want *model.WorkerUsage
	}{
		{"stored as sent", usageHeartbeatBody(), &model.WorkerUsage{
			Host: &model.HostUsage{Load1: 1.5, Load5: 0.5, MemTotalMB: 4096, MemAvailableMB: 1024,
				PSICPUSomeAvg10: ptrTo(2.5), SampledAt: sampledAt},
			GPUs: []model.GPUUsage{{UUID: gpu0, MemUsedMB: ptrTo[int64](1024), UtilPct: ptrTo(42.0)}},
		}},
		{"bad values sanitised",
			// Overflowing, negative and mistyped numbers are dropped; unknown
			// and repeated uuids are dropped; util_pct is clamped; control
			// characters are stripped from the uuid.
			fmt.Sprintf(`{"host":{"load1":1e999,"load5":-1,"mem_total_mb":-5,"psi_mem_some_avg10":-3,"sampled_at":"yesterday","mem_available_mb":"x"},`+
				`"gpu_usage":[{"uuid":"GPU-unknown","mem_used_mb":1},{"uuid":%q,"mem_used_mb":-1,"util_pct":150},{"uuid":%q,"mem_used_mb":7},{"uuid":"%s\n","util_pct":1e999,"mem_used_mb":10}]}`,
				gpu0, gpu0, gpu1),
			&model.WorkerUsage{
				Host: &model.HostUsage{},
				GPUs: []model.GPUUsage{
					{UUID: gpu0, UtilPct: ptrTo(100.0)},
					{UUID: gpu1, MemUsedMB: ptrTo[int64](10)},
				},
			}},
		{"wrongly typed usage ignored", `{"host":"busy","gpu_usage":{"uuid":"x"}}`, nil},
		{"only unknown gpus", `{"gpu_usage":[{"uuid":"GPU-unknown","mem_used_mb":1}]}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			id := registerWorkerID(t, srv, fullInventoryBody(t))

			w, env := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", tt.body)
			if w.Code != http.StatusOK {
				t.Fatalf("heartbeat: status=%d, body=%s", w.Code, w.Body.String())
			}
			var resp model.HeartbeatResponse
			if err := json.Unmarshal(env.Data, &resp); err != nil || resp.WorkerID != id {
				t.Errorf("response = %+v, %v; want worker_id %s", resp, err, id)
			}

			want := workerUsageJSON(t, tt.want)
			listed := listedWorker(t, srv, id)
			if got := workerUsageJSON(t, listed.Usage); got != want {
				t.Errorf("listed usage = %s, want %s", got, want)
			}
			if (listed.UsageAt == nil) != (tt.want == nil) {
				t.Errorf("listed usage_at = %v, want set=%v", listed.UsageAt, tt.want != nil)
			}
			stored, err := st.GetWorker(context.Background(), id)
			if err != nil || stored == nil {
				t.Fatalf("GetWorker: %v %v", stored, err)
			}
			if got := workerUsageJSON(t, stored.Usage); got != want {
				t.Errorf("stored usage = %s, want %s", got, want)
			}
		})
	}
}

func TestWorkerHeartbeat_WithoutUsageKeepsUsage(t *testing.T) {
	srv, st := testServerWithStore()
	id := registerWorkerID(t, srv, fullInventoryBody(t))
	if w, _ := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", usageHeartbeatBody()); w.Code != http.StatusOK {
		t.Fatalf("heartbeat with usage: status=%d, body=%s", w.Code, w.Body.String())
	}
	before, err := st.GetWorker(context.Background(), id)
	if err != nil || before == nil || before.Usage == nil || before.UsageAt == nil {
		t.Fatalf("usage not stored: %+v %v", before, err)
	}

	for _, body := range []string{`{}`, ``, `{"running_tasks":["task_x"]}`} {
		t.Run(fmt.Sprintf("body %q", body), func(t *testing.T) {
			if w, _ := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", body); w.Code != http.StatusOK {
				t.Fatalf("heartbeat: status=%d, body=%s", w.Code, w.Body.String())
			}
			after, err := st.GetWorker(context.Background(), id)
			if err != nil || after == nil {
				t.Fatalf("GetWorker: %v %v", after, err)
			}
			if got, want := workerUsageJSON(t, after.Usage), workerUsageJSON(t, before.Usage); got != want {
				t.Errorf("usage = %s, want unchanged %s", got, want)
			}
			if after.UsageAt == nil || !after.UsageAt.Equal(*before.UsageAt) {
				t.Errorf("usage_at = %v, want unchanged %v", after.UsageAt, before.UsageAt)
			}
			if after.LastSeen.Before(before.LastSeen) {
				t.Errorf("last_seen went backwards: %v < %v", after.LastSeen, before.LastSeen)
			}
		})
	}
}

func TestWorkerHeartbeat_InvalidBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"oversized", `{"running_tasks":["` + strings.Repeat("x", maxWorkerHeartbeatBody) + `"]}`},
		{"invalid JSON", `{"running_tasks":`},
		{"running_tasks wrong type", `{"running_tasks":"task_1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := testServerWithStore()
			id := registerTestWorker(t, srv)
			w, env := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
			}
			if env.Error == nil || env.Error.Code != model.ErrValidation {
				t.Errorf("error = %+v, want VALIDATION_ERROR", env.Error)
			}
		})
	}
}

func TestWorkerHeartbeat_BringsOfflineWorkerOnline(t *testing.T) {
	srv, st := testServerWithStore()
	ctx := context.Background()
	id := registerTestWorker(t, srv)
	wk, err := st.GetWorker(ctx, id)
	if err != nil || wk == nil {
		t.Fatalf("GetWorker: %v %v", wk, err)
	}
	wk.State = model.WorkerStateOffline
	if err := st.UpdateWorker(ctx, wk); err != nil {
		t.Fatal(err)
	}

	w, env := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
	}
	var resp model.HeartbeatResponse
	if err := json.Unmarshal(env.Data, &resp); err != nil || resp.State != model.WorkerStateOnline {
		t.Errorf("response state = %q, %v; want online", resp.State, err)
	}
	stored, err := st.GetWorker(ctx, id)
	if err != nil || stored == nil || stored.State != model.WorkerStateOnline {
		t.Errorf("stored state = %+v, %v; want online", stored, err)
	}
}

// TestWorkerHeartbeat_KeepsCheckedOutCurrentTask guards the race fix: the
// heartbeat no longer rewrites the whole worker row, so it cannot reset the
// current_task a checkout just set.
func TestWorkerHeartbeat_KeepsCheckedOutCurrentTask(t *testing.T) {
	srv, st := testServerWithStore()
	id := registerWorkerID(t, srv, fullInventoryBody(t))
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_hb_1")
	if checked := checkoutTask(t, srv, id); checked.ID != taskID {
		t.Fatalf("checked out %s, want %s", checked.ID, taskID)
	}

	if w, _ := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", usageHeartbeatBody(taskID)); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: status=%d, body=%s", w.Code, w.Body.String())
	}
	stored, err := st.GetWorker(context.Background(), id)
	if err != nil || stored == nil {
		t.Fatalf("GetWorker: %v %v", stored, err)
	}
	if stored.CurrentTask != taskID {
		t.Errorf("current_task = %q, want %q", stored.CurrentTask, taskID)
	}
	if stored.Usage == nil {
		t.Error("usage not stored")
	}
}

func TestSanitizeUsage_NonFiniteAndCap(t *testing.T) {
	h := sanitizeHostUsage(hostUsageWire{Load1: "NaN", Load5: "+Inf", MemTotalMB: "1.5", MemAvailableMB: "-1", PSICPUSomeAvg10: "NaN", PSIMemSomeAvg10: "-Inf"})
	if got, want := workerUsageJSON(t, &model.WorkerUsage{Host: h}), workerUsageJSON(t, &model.WorkerUsage{Host: &model.HostUsage{}}); got != want {
		t.Errorf("host = %s, want %s", got, want)
	}

	inventory := []model.GPUInfo{{Index: 0, UUID: "GPU-a"}}
	gpus, dropped := sanitizeGPUUsage([]gpuUsageWire{{UUID: "GPU-a", UtilPct: "NaN"}, {UUID: "GPU-a"}, {UUID: "GPU-b"}}, inventory)
	if len(gpus) != 1 || gpus[0].UUID != "GPU-a" || gpus[0].UtilPct != nil || dropped != 2 {
		t.Errorf("gpus = %+v dropped %d, want only GPU-a without util, 2 dropped", gpus, dropped)
	}
}
