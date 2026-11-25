package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// gpuInventory returns an inventory of n GPUs; n < 0 means not reported (nil).
func gpuInventory(n int) []model.GPUInfo {
	if n < 0 {
		return nil
	}
	gpus := make([]model.GPUInfo, n)
	for i := range gpus {
		gpus[i] = model.GPUInfo{Index: i, UUID: fmt.Sprintf("GPU-%d", i), Name: "Fake GPU", MemTotalMB: 1000}
	}
	return gpus
}

// gpuWorker returns a GPU-enabled test worker with an inventory of n GPUs
// (n < 0: not reported), modified by mod.
func gpuWorker(id string, n int, mod func(w *model.Worker)) *model.Worker {
	return chargeWorker(id, func(w *model.Worker) {
		w.GPUEnabled = true
		w.GPUs = gpuInventory(n)
		if mod != nil {
			mod(w)
		}
	})
}

// gpuTask returns a queued worker task needing k GPUs (RequiresGPU kept in
// sync, as the scheduler writes it).
func gpuTask(id string, k int, age time.Duration) *model.Task {
	tk := capTask(id, 0, 0, age)
	tk.RuntimeHints.GPUs = k
	tk.RuntimeHints.RequiresGPU = k > 0
	return tk
}

// seedRunningGPU inserts a RUNNING worker task owned by workerID charged
// gpus GPUs (nil = NULL) and no cores or RAM.
func seedRunningGPU(t *testing.T, st *SQLiteStore, id, workerID string, gpus *int) {
	t.Helper()
	tk := capTask(id, 0, 0, 2*time.Hour)
	tk.State = model.TaskStateRunning
	tk.ExternalID = workerID
	started := time.Now().UTC().Add(-time.Hour)
	tk.StartedAt = &started
	tk.ChargedCores, tk.ChargedRamMB, tk.ChargedGPUs = intP(0), int64P(0), gpus
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatalf("seed running task %s: %v", id, err)
	}
}

// TestCheckoutTask_GPUGate: a worker with 2 GPUs runs a task charged 1 GPU.
func TestCheckoutTask_GPUGate(t *testing.T) {
	boolOnly := func(id string) *model.Task {
		tk := capTask(id, 0, 0, time.Minute)
		tk.RuntimeHints.RequiresGPU = true
		return tk
	}
	tests := []struct {
		name    string
		running int
		task    *model.Task
		want    bool
	}{
		{"2-GPU task over free", 1, gpuTask("t1", 2, time.Minute), false},
		{"1-GPU task within free", 1, gpuTask("t1", 1, time.Minute), true},
		{"CPU task unaffected", 1, gpuTask("t1", 0, time.Minute), true},
		{"CPU task unaffected when all GPUs busy", 2, gpuTask("t1", 0, time.Minute), true},
		{"boolean-only task counts as 1", 1, boolOnly("t1"), true},
		{"boolean-only task blocked when all GPUs busy", 2, boolOnly("t1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, gpuWorker("wrk_g", 2, nil))
			seedRunningGPU(t, st, "run1", "wrk_g", intP(tt.running))
			mustCreateTask(t, st, tt.task)

			got, err := st.CheckoutTask(context.Background(), "wrk_g", "", model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Fatalf("checked out = %v, want %v", got != nil, tt.want)
			}
			if got != nil {
				want := tt.task.RuntimeHints.GPUCount()
				if got.ChargedGPUs == nil || *got.ChargedGPUs != want {
					t.Errorf("charged_gpus = %s, want %d", ptrStr(got.ChargedGPUs), want)
				}
			}
		})
	}
}

// A request above the worker's GPU capacity is never offered, and does not
// hide a fitting task behind it.
func TestCheckoutTask_GPURequestAboveCapacity(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, gpuWorker("wrk_g", 2, nil))
	for i := 0; i < 11; i++ {
		mustCreateTask(t, st, gpuTask(fmt.Sprintf("big_%02d", i), 3, time.Hour+time.Duration(11-i)*time.Minute))
	}
	mustCreateTask(t, st, gpuTask("two", 2, time.Minute))

	got, err := st.CheckoutTask(context.Background(), "wrk_g", "", model.RuntimeNone)
	if err != nil || got == nil || got.ID != "two" {
		t.Fatalf("checkout = (%v, %v), want two", got, err)
	}
	if again, err := st.CheckoutTask(context.Background(), "wrk_g", "", model.RuntimeNone); err != nil || again != nil {
		t.Errorf("second checkout = (%v, %v), want (nil, nil): 3-GPU tasks never fit 2 GPUs", again, err)
	}
}

// A GPU worker that reported no inventory (nil) or an empty one has an
// unknown capacity: tasks needing one GPU are offered with no free gate,
// tasks needing two never.
func TestCheckoutTask_GPUUnknownCapacity(t *testing.T) {
	for _, inv := range []struct {
		name string
		n    int
	}{{"inventory not reported", -1}, {"empty inventory", 0}} {
		t.Run(inv.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, gpuWorker("wrk_g", inv.n, nil))
			// Already charged 3: no free gate applies.
			seedRunningGPU(t, st, "run1", "wrk_g", intP(3))
			mustCreateTask(t, st, gpuTask("two", 2, 2*time.Minute))
			mustCreateTask(t, st, gpuTask("one", 1, time.Minute))

			got, err := st.CheckoutTask(context.Background(), "wrk_g", "", model.RuntimeNone)
			if err != nil || got == nil || got.ID != "one" {
				t.Fatalf("checkout = (%v, %v), want one", got, err)
			}
			if got.ChargedGPUs == nil || *got.ChargedGPUs != 1 {
				t.Errorf("charged_gpus = %s, want 1", ptrStr(got.ChargedGPUs))
			}
			if again, err := st.CheckoutTask(context.Background(), "wrk_g", "", model.RuntimeNone); err != nil || again != nil {
				t.Errorf("second checkout = (%v, %v), want (nil, nil): 2 GPUs never offered without an inventory", again, err)
			}
		})
	}
}

// A CPU-only worker never takes a task with a GPU count, even when
// requires_gpu is (wrongly) missing.
func TestCheckoutTask_GPUCountOnCPUWorker(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, chargeWorker("wrk_cpu", nil))
	tk := capTask("count_only", 0, 0, time.Minute)
	tk.RuntimeHints.GPUs = 1
	mustCreateTask(t, st, tk)

	if got, err := st.CheckoutTask(context.Background(), "wrk_cpu", "", model.RuntimeNone); err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestCheckoutTask_GPUChargePersisted(t *testing.T) {
	tests := []struct {
		name string
		task *model.Task
		want int
	}{
		{"CPU task charged 0", gpuTask("t1", 0, time.Minute), 0},
		{"2-GPU task charged 2", gpuTask("t1", 2, time.Minute), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			mustCreateWorker(t, st, gpuWorker("wrk_g", 4, nil))
			mustCreateTask(t, st, tt.task)

			got, err := st.CheckoutTask(ctx, "wrk_g", "", model.RuntimeNone)
			if err != nil || got == nil {
				t.Fatalf("checkout = (%v, %v), want a task", got, err)
			}
			if got.ChargedGPUs == nil || *got.ChargedGPUs != tt.want {
				t.Errorf("returned charged_gpus = %s, want %d", ptrStr(got.ChargedGPUs), tt.want)
			}
			stored, err := st.GetTask(ctx, "t1")
			if err != nil || stored == nil {
				t.Fatalf("GetTask = (%v, %v)", stored, err)
			}
			if stored.ChargedGPUs == nil || *stored.ChargedGPUs != tt.want {
				t.Errorf("stored charged_gpus = %s, want %d", ptrStr(stored.ChargedGPUs), tt.want)
			}
		})
	}
}

// Every requeue statement clears charged_gpus along with charged_cores.
func TestCheckoutTask_RequeueClearsGPUCharge(t *testing.T) {
	old := time.Now().UTC().Add(-5 * time.Minute)
	tests := []struct {
		name    string
		mod     func(w *model.Worker)
		requeue func(t *testing.T, st *SQLiteStore)
	}{
		{"RequeueWorkerTasks", nil, func(t *testing.T, st *SQLiteStore) {
			if _, err := st.RequeueWorkerTasks(context.Background(), "wrk_g"); err != nil {
				t.Fatal(err)
			}
		}},
		{"ReconcileWorkerTasks", nil, func(t *testing.T, st *SQLiteStore) {
			if _, err := st.ReconcileWorkerTasks(context.Background(), "wrk_g", nil, 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"DeleteWorker", nil, func(t *testing.T, st *SQLiteStore) {
			if err := st.DeleteWorker(context.Background(), "wrk_g"); err != nil {
				t.Fatal(err)
			}
		}},
		{"ReapStaleWorkers stale worker", func(w *model.Worker) { w.LastSeen = old }, func(t *testing.T, st *SQLiteStore) {
			if _, _, err := st.ReapStaleWorkers(context.Background(), time.Minute); err != nil {
				t.Fatal(err)
			}
		}},
		{"ReapStaleWorkers stranded task", func(w *model.Worker) {
			w.State, w.LastSeen = model.WorkerStateOffline, old
		}, func(t *testing.T, st *SQLiteStore) {
			if _, _, err := st.ReapStaleWorkers(context.Background(), time.Minute); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			mustCreateWorker(t, st, gpuWorker("wrk_g", 2, tt.mod))
			seedRunningGPU(t, st, "run1", "wrk_g", intP(2))

			tt.requeue(t, st)

			got, err := st.GetTask(ctx, "run1")
			if err != nil || got == nil {
				t.Fatalf("GetTask = (%v, %v)", got, err)
			}
			if got.State != model.TaskStateQueued || got.ChargedCores != nil || got.ChargedGPUs != nil {
				t.Errorf("after requeue: state %s, charge cores %s gpus %s, want QUEUED nil nil",
					got.State, ptrStr(got.ChargedCores), ptrStr(got.ChargedGPUs))
			}
		})
	}
}

func TestUpdateTask_StaleSnapshotKeepsGPUCharge(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, gpuWorker("wrk_g", 2, nil))
	mustCreateTask(t, st, gpuTask("t1", 2, time.Minute))

	stale, err := st.GetTask(ctx, "t1") // QUEUED, no charge
	if err != nil || stale == nil {
		t.Fatalf("GetTask = (%v, %v)", stale, err)
	}
	if got, err := st.CheckoutTask(ctx, "wrk_g", "", model.RuntimeNone); err != nil || got == nil {
		t.Fatalf("checkout = (%v, %v)", got, err)
	}

	stale.State = model.TaskStateRunning
	stale.ExternalID = "wrk_g"
	stale.Stdout = "progress"
	if err := st.UpdateTask(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTask(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("GetTask = (%v, %v)", got, err)
	}
	if got.ChargedGPUs == nil || *got.ChargedGPUs != 2 {
		t.Errorf("charged_gpus after stale UpdateTask = %s, want 2", ptrStr(got.ChargedGPUs))
	}
}

// TestCheckoutTask_HoldGPUs: worker wrk_g has 2 GPUs, one charged to a
// running task. B (2 GPUs, 30 min old) waits for it; a later task is held
// only when it charges GPUs.
func TestCheckoutTask_HoldGPUs(t *testing.T) {
	tests := []struct {
		name  string
		hold  time.Duration
		inv   int
		other *model.Task
		want  string
	}{
		{"1-GPU task held", 10 * time.Minute, 2, gpuTask("later", 1, 5*time.Minute), ""},
		{"CPU-only task not held", 10 * time.Minute, 2, gpuTask("later", 0, 5*time.Minute), "later"},
		{"CPU task with cores not held", 10 * time.Minute, 2, capTask("later", 2, 1000, 5*time.Minute), "later"},
		{"hold off", 0, 2, gpuTask("later", 1, 5*time.Minute), "later"},
		// Unknown capacity: B (2 GPUs) is never offered, so it cannot hold.
		{"unknown capacity never holds", 10 * time.Minute, -1, gpuTask("later", 1, 5*time.Minute), "later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			st.SetCheckoutHoldAfter(tt.hold)
			mustCreateWorker(t, st, gpuWorker("wrk_g", tt.inv, func(w *model.Worker) {
				w.MaxCPUs, w.MaxMemMB = intP(8), int64P(8000)
			}))
			seedRunningGPU(t, st, "run1", "wrk_g", intP(1))
			mustCreateTask(t, st, gpuTask("big", 2, 30*time.Minute))
			mustCreateTask(t, st, tt.other)

			if got := checkoutID(t, st, "wrk_g"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

// The GPU the running task held goes to the holder once that task ends.
func TestCheckoutTask_HoldGPUsReleasesToHolder(t *testing.T) {
	st := testStore(t)
	st.SetCheckoutHoldAfter(10 * time.Minute)
	mustCreateWorker(t, st, gpuWorker("wrk_g", 2, nil))
	seedRunningGPU(t, st, "run1", "wrk_g", intP(1))
	mustCreateTask(t, st, gpuTask("big", 2, 30*time.Minute))
	mustCreateTask(t, st, gpuTask("small", 1, 5*time.Minute))

	if got := checkoutID(t, st, "wrk_g"); got != "" {
		t.Fatalf("checked out %q while big waits, want nothing", got)
	}
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE tasks SET state = ? WHERE id = ?`, string(model.TaskStateSuccess), "run1"); err != nil {
		t.Fatal(err)
	}
	if got := checkoutID(t, st, "wrk_g"); got != "big" {
		t.Fatalf("checked out %q after the running task ended, want big", got)
	}
}

func TestMigrate_ChargedGPUsColumnIdempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'charged_gpus'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("column tasks.charged_gpus count = %d, err %v", n, err)
	}
}
