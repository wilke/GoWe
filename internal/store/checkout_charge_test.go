package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// chargeWorker returns an online test worker with the given id, modified by mod.
func chargeWorker(id string, mod func(w *model.Worker)) *model.Worker {
	w := sampleWorker()
	w.ID, w.Name = id, id
	if mod != nil {
		mod(w)
	}
	return w
}

// seedRunning inserts a RUNNING worker task owned by workerID with the given
// charge (nil = NULL, as for a task checked out by an older server).
func seedRunning(t *testing.T, st *SQLiteStore, id, workerID string, cores *int, ramMB *int64) {
	t.Helper()
	tk := capTask(id, 0, 0, 2*time.Hour)
	tk.State = model.TaskStateRunning
	tk.ExternalID = workerID
	started := time.Now().UTC().Add(-time.Hour)
	tk.StartedAt = &started
	tk.ChargedCores, tk.ChargedRamMB = cores, ramMB
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatalf("seed running task %s: %v", id, err)
	}
}

func mustCreateWorker(t *testing.T, st *SQLiteStore, w *model.Worker) {
	t.Helper()
	if err := st.CreateWorker(context.Background(), w); err != nil {
		t.Fatalf("create worker %s: %v", w.ID, err)
	}
}

func mustCreateTask(t *testing.T, st *SQLiteStore, tk *model.Task) {
	t.Helper()
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatalf("create task %s: %v", tk.ID, err)
	}
}

// checkCharge fails unless the task's charge is exactly (cores, ramMB).
func checkCharge(t *testing.T, what string, tk *model.Task, cores int, ramMB int64) {
	t.Helper()
	if tk.ChargedCores == nil || *tk.ChargedCores != cores || tk.ChargedRamMB == nil || *tk.ChargedRamMB != ramMB {
		t.Errorf("%s charge = %v/%v, want %d/%d", what, ptrStr(tk.ChargedCores), ptrStr(tk.ChargedRamMB), cores, ramMB)
	}
}

func ptrStr[T int | int64](p *T) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func TestCheckoutTask_ChargePersisted(t *testing.T) {
	tests := []struct {
		name      string
		defCores  *int
		defMem    *int64
		cores     int
		ram       int64
		wantCores int
		wantMem   int64
	}{
		{"explicit request", intP(2), int64P(1000), 4, 2000, 4, 2000},
		{"default share when request is 0", intP(2), int64P(1000), 0, 0, 2, 1000},
		{"request and default mixed", nil, int64P(1000), 4, 0, 4, 1000},
		{"0 when neither", nil, nil, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
				w.MaxCPUs, w.MaxMemMB = intP(16), int64P(64000)
				w.DefaultTaskCores, w.DefaultTaskRamMB = tt.defCores, tt.defMem
			}))
			mustCreateTask(t, st, capTask("t1", tt.cores, tt.ram, time.Minute))

			got, err := st.CheckoutTask(ctx, "wrk_a", "", model.RuntimeNone)
			if err != nil || got == nil {
				t.Fatalf("checkout = (%v, %v), want a task", got, err)
			}
			checkCharge(t, "returned", got, tt.wantCores, tt.wantMem)

			stored, err := st.GetTask(ctx, "t1")
			if err != nil || stored == nil {
				t.Fatalf("GetTask = (%v, %v)", stored, err)
			}
			checkCharge(t, "stored", stored, tt.wantCores, tt.wantMem)
		})
	}
}

func TestCheckoutTask_WorkerGate(t *testing.T) {
	tests := []struct {
		name  string
		cores int
		ram   int64
		want  bool
	}{
		{"cores over free", 4, 0, false},
		{"cores within free", 2, 0, true},
		{"ram over free", 0, 4000, false},
		{"ram within free", 0, 2000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
				w.MaxCPUs, w.MaxMemMB = intP(8), int64P(8000)
			}))
			seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(6000))
			mustCreateTask(t, st, capTask("t1", tt.cores, tt.ram, time.Minute))

			got, err := st.CheckoutTask(context.Background(), "wrk_a", "", model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Errorf("checked out = %v, want %v", got != nil, tt.want)
			}
		})
	}
}

// TestCheckoutTask_HostGate: worker A runs a task charged 12 cores / 48000
// MiB on a 16-core / 64000 MiB host; worker B (same host and key) asks.
func TestCheckoutTask_HostGate(t *testing.T) {
	tests := []struct {
		name   string
		mod    func(a, b *model.Worker)
		aCores int
		cores  int
		ram    int64
		want   bool
	}{
		{"cores over host free", nil, 12, 8, 0, false},
		{"cores within host free", nil, 12, 4, 0, true},
		{"ram over host free", nil, 12, 0, 32000, false},
		{"ram within host free", nil, 12, 0, 16000, true},

		// Not summed with A: B is bounded only by its own capacity.
		{"different key", func(a, b *model.Worker) { a.KeyID = "key-other" }, 12, 8, 32000, true},
		{"different host", func(a, b *model.Worker) { a.HostID = "host-other" }, 12, 8, 32000, true},
		{"A offline", func(a, b *model.Worker) { a.State = model.WorkerStateOffline }, 12, 8, 32000, true},
		{"A draining still counts", func(a, b *model.Worker) { a.State = model.WorkerStateDraining }, 12, 8, 0, false},
		{"legacy empty host id", func(a, b *model.Worker) { a.HostID, b.HostID = "", "" }, 12, 8, 32000, true},

		// Host over-committed (free -4 cores): only a task charging nothing passes.
		{"charge 0 passes negative free", nil, 20, 0, 0, true},
		{"1 core blocked by negative free", nil, 20, 1, 0, false},
		{"default share blocked by negative free", func(a, b *model.Worker) { b.DefaultTaskCores = intP(2) }, 20, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mk := func(id string) *model.Worker {
				return chargeWorker(id, func(w *model.Worker) {
					w.HostID, w.KeyID = "host-1", "key-1"
					w.HostCPUs, w.HostMemMB = intP(16), int64P(64000)
					w.MaxCPUs, w.MaxMemMB = intP(16), int64P(64000)
				})
			}
			a, b := mk("wrk_a"), mk("wrk_b")
			if tt.mod != nil {
				tt.mod(a, b)
			}
			mustCreateWorker(t, st, a)
			mustCreateWorker(t, st, b)
			seedRunning(t, st, "run_a", "wrk_a", intP(tt.aCores), int64P(48000))
			mustCreateTask(t, st, capTask("t1", tt.cores, tt.ram, time.Minute))

			got, err := st.CheckoutTask(context.Background(), "wrk_b", "", model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Errorf("checked out = %v, want %v", got != nil, tt.want)
			}
		})
	}
}

// The production shape: many single-task workers on one host, each claiming
// the whole machine, running tasks that request nothing. Every worker must
// still get a task, even while explicit requests over-commit the host.
func TestCheckoutTask_NoRequestTasksNeverHeldBack(t *testing.T) {
	st := testStore(t)
	const n = 5
	for i := 0; i < n; i++ {
		mustCreateWorker(t, st, chargeWorker(fmt.Sprintf("wrk_%d", i), func(w *model.Worker) {
			w.HostID, w.KeyID = "host-1", "key-1"
			w.HostCPUs, w.HostMemMB = intP(16), int64P(64000)
			w.MaxCPUs, w.MaxMemMB = intP(16), int64P(64000)
		}))
		mustCreateTask(t, st, capTask(fmt.Sprintf("t%d", i), 0, 0, time.Duration(n-i)*time.Minute))
	}
	// Explicit requests already exceed the host.
	mustCreateWorker(t, st, chargeWorker("wrk_big", func(w *model.Worker) {
		w.HostID, w.KeyID = "host-1", "key-1"
		w.HostCPUs, w.HostMemMB = intP(16), int64P(64000)
	}))
	seedRunning(t, st, "run_big", "wrk_big", intP(32), int64P(128000))

	for i := 0; i < n; i++ {
		got, err := st.CheckoutTask(context.Background(), fmt.Sprintf("wrk_%d", i), "", model.RuntimeNone)
		if err != nil || got == nil {
			t.Fatalf("worker %d: checkout = (%v, %v), want a task", i, got, err)
		}
		checkCharge(t, got.ID, got, 0, 0)
	}
}

func TestCheckoutTask_NullChargeCountsAsZero(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
		w.MaxCPUs, w.MaxMemMB = intP(8), int64P(8000)
	}))
	for i := 0; i < 3; i++ {
		seedRunning(t, st, fmt.Sprintf("old_%d", i), "wrk_a", nil, nil)
	}
	mustCreateTask(t, st, capTask("t1", 8, 8000, time.Minute))

	got, err := st.CheckoutTask(context.Background(), "wrk_a", "", model.RuntimeNone)
	if err != nil || got == nil || got.ID != "t1" {
		t.Fatalf("checkout = (%v, %v), want t1 (NULL charges count as 0)", got, err)
	}
}

func TestCheckoutTask_EffectiveCapBoundedByHost(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
		w.HostID = "host-1"
		w.MaxCPUs, w.HostCPUs = intP(64), intP(16)
	}))
	mustCreateTask(t, st, capTask("big", 32, 0, time.Minute))

	got, err := st.CheckoutTask(context.Background(), "wrk_a", "", model.RuntimeNone)
	if err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want (nil, nil): 32 cores exceed the 16-core host", got, err)
	}
}

func TestCheckoutTask_RequeueClearsCharge(t *testing.T) {
	tests := []struct {
		name    string
		requeue func(t *testing.T, st *SQLiteStore)
		// worker that checks out after the requeue
		next string
	}{
		{"RequeueWorkerTasks", func(t *testing.T, st *SQLiteStore) {
			if _, err := st.RequeueWorkerTasks(context.Background(), "wrk_a"); err != nil {
				t.Fatal(err)
			}
		}, "wrk_a"},
		{"ReconcileWorkerTasks", func(t *testing.T, st *SQLiteStore) {
			if _, err := st.ReconcileWorkerTasks(context.Background(), "wrk_a", nil, 0); err != nil {
				t.Fatal(err)
			}
		}, "wrk_a"},
		{"DeleteWorker", func(t *testing.T, st *SQLiteStore) {
			if err := st.DeleteWorker(context.Background(), "wrk_a"); err != nil {
				t.Fatal(err)
			}
		}, "wrk_b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			for _, id := range []string{"wrk_a", "wrk_b"} {
				mustCreateWorker(t, st, chargeWorker(id, func(w *model.Worker) { w.MaxCPUs = intP(8) }))
			}
			mustCreateTask(t, st, capTask("six", 6, 0, 2*time.Minute))
			mustCreateTask(t, st, capTask("four", 4, 0, time.Minute))

			got, err := st.CheckoutTask(ctx, "wrk_a", "", model.RuntimeNone)
			if err != nil || got == nil || got.ID != "six" {
				t.Fatalf("first checkout = (%v, %v), want six", got, err)
			}
			// 8 - 6 = 2 cores free: the 4-core task does not fit.
			if again, err := st.CheckoutTask(ctx, "wrk_a", "", model.RuntimeNone); err != nil || again != nil {
				t.Fatalf("second checkout = (%v, %v), want (nil, nil)", again, err)
			}

			tt.requeue(t, st)

			requeued, err := st.GetTask(ctx, "six")
			if err != nil || requeued == nil {
				t.Fatalf("GetTask = (%v, %v)", requeued, err)
			}
			if requeued.State != model.TaskStateQueued || requeued.ChargedCores != nil || requeued.ChargedRamMB != nil {
				t.Errorf("after requeue: state %s, charge %s/%s, want QUEUED nil/nil",
					requeued.State, ptrStr(requeued.ChargedCores), ptrStr(requeued.ChargedRamMB))
			}

			// Capacity is released (6 fits again) and the charge recomputed.
			next, err := st.CheckoutTask(ctx, tt.next, "", model.RuntimeNone)
			if err != nil || next == nil || next.ID != "six" {
				t.Fatalf("checkout after requeue = (%v, %v), want six", next, err)
			}
			checkCharge(t, "re-checked-out", next, 6, 0)
		})
	}
}

func TestUpdateTask_StaleSnapshotKeepsCharge(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) { w.MaxCPUs = intP(8) }))
	mustCreateTask(t, st, capTask("t1", 4, 1000, time.Minute))

	stale, err := st.GetTask(ctx, "t1") // QUEUED, no charge
	if err != nil || stale == nil {
		t.Fatalf("GetTask = (%v, %v)", stale, err)
	}
	if got, err := st.CheckoutTask(ctx, "wrk_a", "", model.RuntimeNone); err != nil || got == nil {
		t.Fatalf("checkout = (%v, %v)", got, err)
	}

	stale.State = model.TaskStateRunning
	stale.ExternalID = "wrk_a"
	stale.Stdout = "progress"
	if err := st.UpdateTask(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTask(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("GetTask = (%v, %v)", got, err)
	}
	checkCharge(t, "after stale UpdateTask", got, 4, 1000)
}

func TestCheckoutTask_GroupFilterInSQL(t *testing.T) {
	grouped := func(id, group string, age time.Duration) *model.Task {
		tk := capTask(id, 0, 0, age)
		tk.RuntimeHints.WorkerGroup = group
		return tk
	}
	setHints := func(t *testing.T, st *SQLiteStore, id, raw string) {
		t.Helper()
		if _, err := st.db.ExecContext(context.Background(),
			`UPDATE tasks SET runtime_hints = ? WHERE id = ?`, raw, id); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("group tasks ahead do not hide a default task", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, chargeWorker("wrk_a", nil))
		for i := 0; i < 11; i++ {
			mustCreateTask(t, st, grouped(fmt.Sprintf("x_%02d", i), "x", time.Hour+time.Duration(11-i)*time.Minute))
		}
		mustCreateTask(t, st, grouped("dflt", "", time.Minute))
		got, err := st.CheckoutTask(context.Background(), "wrk_a", "default", model.RuntimeNone)
		if err != nil || got == nil || got.ID != "dflt" {
			t.Fatalf("checkout = (%v, %v), want dflt", got, err)
		}
	})

	t.Run("default worker takes untagged and default tasks", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, chargeWorker("wrk_a", nil))
		mustCreateTask(t, st, grouped("untagged", "", 2*time.Minute))
		mustCreateTask(t, st, grouped("tagged", "default", time.Minute))
		for _, want := range []string{"untagged", "tagged"} {
			got, err := st.CheckoutTask(context.Background(), "wrk_a", "default", model.RuntimeNone)
			if err != nil || got == nil || got.ID != want {
				t.Fatalf("checkout = (%v, %v), want %s", got, err, want)
			}
		}
	})

	t.Run("group worker does not take untagged tasks", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, chargeWorker("wrk_x", func(w *model.Worker) { w.Group = "x" }))
		mustCreateTask(t, st, grouped("untagged", "", 2*time.Minute))
		mustCreateTask(t, st, grouped("dflt", "default", time.Minute))
		got, err := st.CheckoutTask(context.Background(), "wrk_x", "x", model.RuntimeNone)
		if err != nil || got != nil {
			t.Fatalf("checkout = (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("empty runtime_hints is untagged", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, chargeWorker("wrk_a", nil))
		mustCreateWorker(t, st, chargeWorker("wrk_x", func(w *model.Worker) { w.Group = "x" }))
		mustCreateTask(t, st, grouped("empty", "", time.Minute))
		setHints(t, st, "empty", "")
		if got, err := st.CheckoutTask(context.Background(), "wrk_x", "x", model.RuntimeNone); err != nil || got != nil {
			t.Fatalf("group worker checkout = (%v, %v), want (nil, nil)", got, err)
		}
		got, err := st.CheckoutTask(context.Background(), "wrk_a", "default", model.RuntimeNone)
		if err != nil || got == nil || got.ID != "empty" {
			t.Fatalf("default worker checkout = (%v, %v), want empty", got, err)
		}
	})

	t.Run("invalid runtime_hints rows are untagged and do not hide a group task", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, chargeWorker("wrk_x", func(w *model.Worker) { w.Group = "x" }))
		for i := 0; i < 11; i++ {
			id := fmt.Sprintf("bad_%02d", i)
			mustCreateTask(t, st, grouped(id, "x", time.Hour+time.Duration(11-i)*time.Minute))
			setHints(t, st, id, "{not json")
		}
		mustCreateTask(t, st, grouped("x_task", "x", time.Minute))
		got, err := st.CheckoutTask(context.Background(), "wrk_x", "x", model.RuntimeNone)
		if err != nil || got == nil || got.ID != "x_task" {
			t.Fatalf("checkout = (%v, %v), want x_task", got, err)
		}
	})
}

func TestCheckoutTask_GPUTasksDoNotHideCPUTask(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, chargeWorker("wrk_cpu", nil))
	for i := 0; i < 11; i++ {
		tk := capTask(fmt.Sprintf("gpu_%02d", i), 0, 0, time.Hour+time.Duration(11-i)*time.Minute)
		tk.RuntimeHints.RequiresGPU = true
		mustCreateTask(t, st, tk)
	}
	mustCreateTask(t, st, capTask("cpu", 0, 0, time.Minute))

	got, err := st.CheckoutTask(context.Background(), "wrk_cpu", "", model.RuntimeNone)
	if err != nil || got == nil || got.ID != "cpu" {
		t.Fatalf("checkout = (%v, %v), want cpu", got, err)
	}
	if again, err := st.CheckoutTask(context.Background(), "wrk_cpu", "", model.RuntimeNone); err != nil || again != nil {
		t.Errorf("second checkout = (%v, %v), want (nil, nil): GPU tasks stay queued", again, err)
	}
}

func TestTaskCharge(t *testing.T) {
	tests := []struct {
		name    string
		request int64
		def     *int64
		want    int64
	}{
		{"request wins over default", 4, int64P(2), 4},
		{"request without default", 4, nil, 4},
		{"default when no request", 0, int64P(2), 2},
		{"negative request uses default", -1, int64P(2), 2},
		{"0 when neither", 0, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskCharge(tt.request, tt.def); got != tt.want {
				t.Errorf("taskCharge(%d, %s) = %d, want %d", tt.request, ptrStr(tt.def), got, tt.want)
			}
		})
	}
}

func TestCheckoutBounds(t *testing.T) {
	tests := []struct {
		name          string
		hostID        string
		workerMax     *int64
		hostCap       *int64
		workerCharged int64
		hostCharged   int64
		wantTotal     *int64
		wantFree      *int64
	}{
		{"nothing set", "h", nil, nil, 5, 5, nil, nil},
		{"worker only", "", int64P(8), nil, 6, 0, int64P(8), int64P(2)},
		{"host ignored without host id", "", int64P(8), int64P(4), 0, 3, int64P(8), int64P(8)},
		{"host only", "h", nil, int64P(16), 0, 12, int64P(16), int64P(4)},
		{"host tighter", "h", int64P(16), int64P(16), 0, 12, int64P(16), int64P(4)},
		{"worker tighter", "h", int64P(8), int64P(16), 6, 6, int64P(8), int64P(2)},
		{"host cap below worker max", "h", int64P(64), int64P(16), 0, 0, int64P(16), int64P(16)},
		{"negative free", "h", int64P(16), int64P(16), 0, 20, int64P(16), int64P(-4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, free := checkoutBounds(tt.hostID, tt.workerMax, tt.hostCap, tt.workerCharged, tt.hostCharged)
			if ptrStr(total) != ptrStr(tt.wantTotal) || ptrStr(free) != ptrStr(tt.wantFree) {
				t.Errorf("checkoutBounds = (%s, %s), want (%s, %s)",
					ptrStr(total), ptrStr(free), ptrStr(tt.wantTotal), ptrStr(tt.wantFree))
			}
		})
	}
}

func TestSumChargedByWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedRunning(t, st, "a1", "wrk_a", intP(4), int64P(1000))
	seedRunning(t, st, "a2", "wrk_a", intP(2), nil)
	seedRunning(t, st, "b1", "wrk_b", nil, nil)
	// Excluded: not RUNNING, not a worker task, no owner.
	queued := capTask("q1", 0, 0, time.Minute)
	queued.ExternalID, queued.ChargedCores = "wrk_a", intP(100)
	mustCreateTask(t, st, queued)
	local := capTask("l1", 0, 0, time.Minute)
	local.State, local.ExecutorType, local.ExternalID, local.ChargedCores = model.TaskStateRunning, model.ExecutorTypeLocal, "wrk_a", intP(100)
	mustCreateTask(t, st, local)
	seedRunning(t, st, "n1", "", intP(100), nil)

	got, err := st.SumChargedByWorker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.WorkerCharge{"wrk_a": {Cores: 6, RamMB: 1000}, "wrk_b": {}}
	if len(got) != len(want) {
		t.Errorf("charges = %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("charge[%s] = %+v, want %+v", id, got[id], w)
		}
	}
}

func TestWorkerDefaultTaskShare_RoundTrip(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
		w.DefaultTaskCores, w.DefaultTaskRamMB = intP(2), int64P(4096)
	}))
	got, err := st.GetWorker(ctx, "wrk_a")
	if err != nil || got == nil {
		t.Fatalf("GetWorker = (%v, %v)", got, err)
	}
	if ptrStr(got.DefaultTaskCores) != "2" || ptrStr(got.DefaultTaskRamMB) != "4096" {
		t.Errorf("default share = %s/%s, want 2/4096", ptrStr(got.DefaultTaskCores), ptrStr(got.DefaultTaskRamMB))
	}

	got.DefaultTaskCores, got.DefaultTaskRamMB = nil, int64P(1024)
	if err := st.UpdateWorker(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetWorker(ctx, "wrk_a")
	if again.DefaultTaskCores != nil || ptrStr(again.DefaultTaskRamMB) != "1024" {
		t.Errorf("after update default share = %s/%s, want nil/1024", ptrStr(again.DefaultTaskCores), ptrStr(again.DefaultTaskRamMB))
	}
}

func TestMigrate_ChargeColumnsIdempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
	for _, c := range []struct{ table, col string }{
		{"tasks", "charged_cores"},
		{"tasks", "charged_ram_mb"},
		{"workers", "default_task_cores"},
		{"workers", "default_task_ram_mb"},
	} {
		var n int
		if err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('`+c.table+`') WHERE name = ?`, c.col).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s.%s count = %d, err %v", c.table, c.col, n, err)
		}
	}
}
