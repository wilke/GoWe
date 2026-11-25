package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func boolP(v bool) *bool { return &v }

// hostInventory is the #323 subset of a worker, for comparisons.
type hostInventory struct {
	HostID       string
	HostCPUs     *int
	HostMemMB    *int64
	Slots        *int
	CgroupCPU    *bool
	CgroupMemory *bool
	GPUs         []model.GPUInfo
}

func inventoryOf(w *model.Worker) hostInventory {
	return hostInventory{w.HostID, w.HostCPUs, w.HostMemMB, w.Slots, w.CgroupCPU, w.CgroupMemory, w.GPUs}
}

// String renders the pointers as values so failures are readable.
func (h hostInventory) String() string {
	b, _ := json.Marshal(h)
	return string(b)
}

func inventoryWorker(inv hostInventory) *model.Worker {
	w := sampleWorker()
	w.HostID, w.HostCPUs, w.HostMemMB, w.Slots = inv.HostID, inv.HostCPUs, inv.HostMemMB, inv.Slots
	w.CgroupCPU, w.CgroupMemory, w.GPUs = inv.CgroupCPU, inv.CgroupMemory, inv.GPUs
	return w
}

func TestWorkerHostInventory_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		inv  hostInventory
	}{
		{"all fields", hostInventory{
			HostID:       "0123456789abcdef",
			HostCPUs:     intP(192),
			HostMemMB:    int64P(1547000),
			Slots:        intP(1),
			CgroupCPU:    boolP(true),
			CgroupMemory: boolP(false),
			GPUs: []model.GPUInfo{
				{Index: 0, UUID: "GPU-1a2b3c4d-0000-1111-2222-333344445555", Name: "NVIDIA H200 NVL", MemTotalMB: 143771},
				{Index: 1, UUID: "GPU-1a2b3c4d-0000-1111-2222-666677778888", Name: "NVIDIA H200 NVL", MemTotalMB: 143771},
			},
		}},
		{"none reported", hostInventory{}},
		{"gpu worker with empty inventory", hostInventory{GPUs: []model.GPUInfo{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			w := inventoryWorker(tt.inv)
			if err := st.CreateWorker(ctx, w); err != nil {
				t.Fatal(err)
			}

			// Absence is stored as NULL, not as a zero value.
			var hostCPUsNull, gpusNull bool
			if err := st.db.QueryRowContext(ctx,
				`SELECT host_cpus IS NULL, gpus IS NULL FROM workers WHERE id = ?`, w.ID).
				Scan(&hostCPUsNull, &gpusNull); err != nil {
				t.Fatal(err)
			}
			if hostCPUsNull != (tt.inv.HostCPUs == nil) || gpusNull != (tt.inv.GPUs == nil) {
				t.Errorf("NULL columns: host_cpus=%v gpus=%v", hostCPUsNull, gpusNull)
			}

			got, err := st.GetWorker(ctx, w.ID)
			if err != nil || got == nil {
				t.Fatalf("GetWorker: %v %v", got, err)
			}
			if !reflect.DeepEqual(inventoryOf(got), tt.inv) {
				t.Errorf("GetWorker inventory = %v, want %v", inventoryOf(got), tt.inv)
			}

			all, err := st.ListWorkers(ctx)
			if err != nil || len(all) != 1 {
				t.Fatalf("ListWorkers = %d, %v", len(all), err)
			}
			if !reflect.DeepEqual(inventoryOf(all[0]), tt.inv) {
				t.Errorf("ListWorkers inventory = %v, want %v", inventoryOf(all[0]), tt.inv)
			}

			// The heartbeat's read-modify-write leaves every field intact.
			got.LastSeen = got.LastSeen.Add(time.Second)
			if err := st.UpdateWorker(ctx, got); err != nil {
				t.Fatal(err)
			}
			again, err := st.GetWorker(ctx, w.ID)
			if err != nil || again == nil {
				t.Fatalf("GetWorker after update: %v %v", again, err)
			}
			if !reflect.DeepEqual(inventoryOf(again), tt.inv) {
				t.Errorf("inventory after UpdateWorker = %v, want %v", inventoryOf(again), tt.inv)
			}
		})
	}
}

func TestWorkerHostInventory_UpdateClearsToNull(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := inventoryWorker(hostInventory{
		HostID:       "abc",
		HostCPUs:     intP(8),
		HostMemMB:    int64P(16000),
		Slots:        intP(1),
		CgroupCPU:    boolP(true),
		CgroupMemory: boolP(true),
		GPUs:         []model.GPUInfo{{Index: 0, UUID: "GPU-a"}},
	})
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	cleared := inventoryWorker(hostInventory{})
	if err := st.UpdateWorker(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	if !reflect.DeepEqual(inventoryOf(got), hostInventory{}) {
		t.Errorf("inventory = %v, want all unreported", inventoryOf(got))
	}
}

func TestMigrate_WorkerHostInventoryColumnsIdempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
	for _, col := range []string{"host_id", "host_cpus", "host_mem_mb", "slots", "cgroup_cpu", "cgroup_memory", "gpus"} {
		if !hasColumn(t, st, "workers", col) {
			t.Errorf("column %s missing", col)
		}
	}
	if !hasIndex(t, st, "idx_workers_host_id") {
		t.Errorf("index idx_workers_host_id missing")
	}
}
