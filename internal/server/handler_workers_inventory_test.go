package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func ptrTo[T any](v T) *T { return &v }

// workerInventory is the #323 subset of a worker, for comparisons.
type workerInventory struct {
	HostID       string
	HostCPUs     *int
	HostMemMB    *int64
	Slots        *int
	CgroupCPU    *bool
	CgroupMemory *bool
	GPUs         []model.GPUInfo
}

func workerInventoryOf(w *model.Worker) workerInventory {
	return workerInventory{w.HostID, w.HostCPUs, w.HostMemMB, w.Slots, w.CgroupCPU, w.CgroupMemory, w.GPUs}
}

// String renders the pointers as values so failures are readable.
func (h workerInventory) String() string {
	b, _ := json.Marshal(h)
	return string(b)
}

var testH200GPUs = []model.GPUInfo{
	{Index: 0, UUID: "GPU-1a2b3c4d-0000-1111-2222-333344445555", Name: "NVIDIA H200 NVL", MemTotalMB: 143771},
	{Index: 1, UUID: "GPU-1a2b3c4d-0000-1111-2222-666677778888", Name: "NVIDIA H200 NVL", MemTotalMB: 143771},
}

var fullTestInventory = workerInventory{
	HostID:       "0123456789abcdef",
	HostCPUs:     ptrTo(192),
	HostMemMB:    ptrTo[int64](1547000),
	Slots:        ptrTo(1),
	CgroupCPU:    ptrTo(true),
	CgroupMemory: ptrTo(false),
	GPUs:         testH200GPUs,
}

// registerBody builds a registration body from a base plus extra fields.
func registerBody(t *testing.T, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"name": "w", "runtime": "none"}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fullInventoryBody(t *testing.T) string {
	return registerBody(t, map[string]any{
		"host_id": "0123456789abcdef", "host_cpus": 192, "host_mem_mb": 1547000, "slots": 1,
		"cgroup_cpu": true, "cgroup_memory": false, "gpus": testH200GPUs,
	})
}

// registerWorkerID registers body and returns the new worker id.
func registerWorkerID(t *testing.T, srv *Server, body string) string {
	t.Helper()
	w, env := doPost(t, srv, "/api/v1/workers/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data model.Worker
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode worker: %v", err)
	}
	return data.ID
}

func TestRegisterWorker_HostInventory(t *testing.T) {
	tests := []struct {
		name string
		body string
		want workerInventory
	}{
		{"all fields", fullInventoryBody(t), fullTestInventory},
		{"legacy body", `{"name":"w","runtime":"none"}`, workerInventory{}},
		{"non-positive numbers not reported", `{"name":"w","runtime":"none","host_cpus":0,"host_mem_mb":-1,"slots":-2}`, workerInventory{}},
		{"empty gpu list kept", `{"name":"w","runtime":"none","gpus":[]}`, workerInventory{GPUs: []model.GPUInfo{}}},
		{"invalid gpu entries dropped",
			`{"name":"w","runtime":"none","gpus":[{"index":-1,"uuid":"GPU-neg"},{"index":0,"uuid":""},{"index":2,"uuid":"GPU-ok","name":"n","mem_total_mb":1}]}`,
			workerInventory{GPUs: []model.GPUInfo{{Index: 2, UUID: "GPU-ok", Name: "n", MemTotalMB: 1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			w, env := doPost(t, srv, "/api/v1/workers/", tt.body)
			if w.Code != http.StatusCreated {
				t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
			}

			// The API omits an empty gpus list (omitempty); the store keeps it.
			wantAPI := tt.want
			if len(wantAPI.GPUs) == 0 {
				wantAPI.GPUs = nil
			}
			var resp model.Worker
			if err := json.Unmarshal(env.Data, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if got := workerInventoryOf(&resp); !reflect.DeepEqual(got, wantAPI) {
				t.Errorf("response inventory = %v, want %v", got, wantAPI)
			}

			stored, err := st.GetWorker(context.Background(), resp.ID)
			if err != nil || stored == nil {
				t.Fatalf("GetWorker: %v %v", stored, err)
			}
			if got := workerInventoryOf(stored); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stored inventory = %v, want %v", got, tt.want)
			}

			var listed []model.Worker
			if err := json.Unmarshal(doGet(t, srv, "/api/v1/workers/").Data, &listed); err != nil || len(listed) != 1 {
				t.Fatalf("list workers = %d, %v", len(listed), err)
			}
			if got := workerInventoryOf(&listed[0]); !reflect.DeepEqual(got, wantAPI) {
				t.Errorf("listed inventory = %v, want %v", got, wantAPI)
			}
		})
	}
}

func TestRegisterWorker_InvalidHostID(t *testing.T) {
	tests := []struct {
		name   string
		hostID string
	}{
		{"space and bang", "bad id!"},
		{"slash", "a/b"},
		{"non-ascii", "höst"},
		{"too long", strings.Repeat("a", model.MaxHostIDLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			w, env := doPost(t, srv, "/api/v1/workers/", registerBody(t, map[string]any{"host_id": tt.hostID}))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
			}
			if env.Error == nil || env.Error.Code != model.ErrValidation {
				t.Errorf("error = %+v, want VALIDATION_ERROR", env.Error)
			}
			if all, err := st.ListWorkers(context.Background()); err != nil || len(all) != 0 {
				t.Errorf("workers stored = %d, %v; want none", len(all), err)
			}
		})
	}
}

func TestRegisterWorker_GPUListCapped(t *testing.T) {
	srv, st := testServerWithStore()
	gpus := make([]model.GPUInfo, 70)
	for i := range gpus {
		gpus[i] = model.GPUInfo{Index: i, UUID: fmt.Sprintf("GPU-%04d", i), Name: strings.Repeat("x", 200), MemTotalMB: 1024}
	}
	id := registerWorkerID(t, srv, registerBody(t, map[string]any{"gpus": gpus}))

	stored, err := st.GetWorker(context.Background(), id)
	if err != nil || stored == nil {
		t.Fatalf("GetWorker: %v %v", stored, err)
	}
	if len(stored.GPUs) != 64 {
		t.Fatalf("stored %d GPUs, want 64", len(stored.GPUs))
	}
	if first, last := stored.GPUs[0], stored.GPUs[63]; first.Index != 0 || last.Index != 63 {
		t.Errorf("kept indices %d..%d, want 0..63", first.Index, last.Index)
	}
	if n := len(stored.GPUs[0].Name); n != 128 {
		t.Errorf("name length = %d, want truncated to 128", n)
	}
}

func TestWorkerHeartbeat_PreservesHostInventory(t *testing.T) {
	srv, st := testServerWithStore()
	id := registerWorkerID(t, srv, fullInventoryBody(t))

	for i := 0; i < 2; i++ {
		if w, _ := doPut(t, srv, "/api/v1/workers/"+id+"/heartbeat", `{}`); w.Code != http.StatusOK {
			t.Fatalf("heartbeat #%d: status=%d, body=%s", i+1, w.Code, w.Body.String())
		}
	}

	stored, err := st.GetWorker(context.Background(), id)
	if err != nil || stored == nil {
		t.Fatalf("GetWorker: %v %v", stored, err)
	}
	if got := workerInventoryOf(stored); !reflect.DeepEqual(got, fullTestInventory) {
		t.Errorf("inventory after heartbeat = %v, want %v", got, fullTestInventory)
	}
}

// TestRegisterWorker_InventoryBounds: out-of-range capacity is treated as not
// reported and unusable GPU entries are dropped, so one buggy worker cannot
// poison per-host sums or labels built on these values.
func TestRegisterWorker_InventoryBounds(t *testing.T) {
	srv, st := testServerWithStore()
	id := registerWorkerID(t, srv, registerBody(t, map[string]any{
		"host_cpus":   maxWorkerHostCPUs + 1,
		"host_mem_mb": int64(maxWorkerHostMemMB) + 1,
		"slots":       maxWorkerSlots + 1,
		"gpus": []map[string]any{
			{"index": 0, "uuid": "GPU-ok\n\x1b[31m", "name": "H200\tNVL", "mem_total_mb": 1024},
			{"index": 1, "uuid": "GPU-negmem", "mem_total_mb": -5},
			{"index": maxWorkerGPUIndex + 1, "uuid": "GPU-bigindex", "mem_total_mb": 1},
			{"index": 2, "uuid": "\n\t", "mem_total_mb": 1},
		},
	}))

	stored, err := st.GetWorker(context.Background(), id)
	if err != nil || stored == nil {
		t.Fatalf("GetWorker: %v %v", stored, err)
	}
	if stored.HostCPUs != nil || stored.HostMemMB != nil || stored.Slots != nil {
		t.Errorf("out-of-range capacity stored: cpus=%v mem=%v slots=%v, want all nil",
			stored.HostCPUs, stored.HostMemMB, stored.Slots)
	}
	if len(stored.GPUs) != 1 {
		t.Fatalf("stored %d GPUs, want only the valid one: %+v", len(stored.GPUs), stored.GPUs)
	}
	if g := stored.GPUs[0]; g.UUID != "GPU-ok[31m" || g.Name != "H200NVL" {
		t.Errorf("GPU strings not reduced to printable ASCII: uuid=%q name=%q", g.UUID, g.Name)
	}
}

func TestRegisterWorker_BodyTooLarge(t *testing.T) {
	srv, _ := testServerWithStore()
	body := registerBody(t, map[string]any{"version": strings.Repeat("x", maxWorkerRegisterBody)})
	if w, _ := doPost(t, srv, "/api/v1/workers/", body); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: status=%d, want 400", w.Code)
	}
}
