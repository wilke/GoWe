package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// ago returns the time d before now, as stored timestamps are (UTC, ms).
func ago(d time.Duration) time.Time {
	return time.Now().UTC().Add(-d).Truncate(time.Millisecond)
}

// checkoutID checks out for workerID (default group, no container runtime)
// and returns the task id, "" when nothing is checked out.
func checkoutID(t *testing.T, st *SQLiteStore, workerID string) string {
	t.Helper()
	return checkoutGroupID(t, st, workerID, "")
}

// checkoutGroupID is checkoutID for the given worker group.
func checkoutGroupID(t *testing.T, st *SQLiteStore, workerID, group string) string {
	t.Helper()
	got, err := st.CheckoutTask(context.Background(), workerID, group, model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got == nil {
		return ""
	}
	return got.ID
}

// holdStore returns a store with the hold rule at hold and worker wrk_a
// (8 cores / 8000 MiB, then mod applied).
func holdStore(t *testing.T, hold time.Duration, mod func(w *model.Worker)) *SQLiteStore {
	t.Helper()
	st := testStore(t)
	st.SetCheckoutHoldAfter(hold)
	mustCreateWorker(t, st, chargeWorker("wrk_a", func(w *model.Worker) {
		w.MaxCPUs, w.MaxMemMB = intP(8), int64P(8000)
		if mod != nil {
			mod(w)
		}
	}))
	return st
}

// TestCheckoutTask_Hold: worker wrk_a has 8 cores / 8000 MiB and runs a task
// charged 6 cores, so 2 cores are free on its own gate. B ("big", 8 cores, 30
// min old) cannot fit until that task ends; by default S ("small", 2 cores, 5
// min old) is queued behind it.
func TestCheckoutTask_Hold(t *testing.T) {
	const hold = 10 * time.Minute
	priority := func(tk *model.Task, p int) *model.Task {
		tk.Priority = p
		return tk
	}
	tests := []struct {
		name   string
		hold   time.Duration
		worker func(w *model.Worker)
		big    func(b *model.Task)
		others []*model.Task // queued besides B; nil = S
		want   string        // "" = nothing checked out
	}{
		{"hold off", 0, nil, nil, nil, "small"},
		{"hold", hold, nil, nil, nil, ""},
		{"holder younger than hold-after", hold, nil, func(b *model.Task) { b.CreatedAt = ago(8 * time.Minute) }, nil, "small"},
		{"aged by dispatched_at", hold, nil, func(b *model.Task) {
			b.CreatedAt = ago(2 * time.Minute)
			d := ago(30 * time.Minute)
			b.DispatchedAt = &d
		}, []*model.Task{capTask("small", 2, 0, 30*time.Second)}, ""},
		{"recent dispatch is not aged", hold, nil, func(b *model.Task) {
			d := ago(time.Minute)
			b.DispatchedAt = &d
		}, nil, "small"},
		{"higher priority task ahead of holder", hold, nil, nil,
			[]*model.Task{priority(capTask("small", 2, 0, 5*time.Minute), 1)}, "small"},
		{"older task ahead of holder", hold, nil, nil, []*model.Task{capTask("small", 2, 0, 40*time.Minute)}, "small"},
		{"only charge-0 candidates", hold, nil, nil, []*model.Task{capTask("zero", 0, 0, 5*time.Minute)}, "zero"},
		{"charge-0 task behind holder is not held", hold, nil, nil,
			[]*model.Task{capTask("small", 2, 0, 5*time.Minute), capTask("zero", 0, 0, time.Minute)}, "zero"},
		{"default share is held", hold, func(w *model.Worker) { w.DefaultTaskCores = intP(1) }, nil,
			[]*model.Task{capTask("zero", 0, 0, 5*time.Minute)}, ""},
		{"RAM-only task behind cores-short holder", hold, nil, nil,
			[]*model.Task{capTask("ram", 0, 1000, 5*time.Minute)}, "ram"},

		// B is not eligible for this worker: no hold.
		{"holder needs a container", hold, nil, func(b *model.Task) { b.RuntimeHints.DockerImage = "img:1" }, nil, "small"},
		{"holder needs a missing prestage dataset", hold, nil, func(b *model.Task) {
			b.RuntimeHints.RequiredDatasets = []model.DatasetRequirement{{ID: "db", Mode: "prestage"}}
		}, nil, "small"},
		{"holder in another group", hold, nil, func(b *model.Task) { b.RuntimeHints.WorkerGroup = "x" }, nil, "small"},
		{"holder requires a GPU", hold, nil, func(b *model.Task) { b.RuntimeHints.RequiresGPU = true }, nil, "small"},
		{"holder exceeds the cap", hold, nil, func(b *model.Task) { b.RuntimeHints.Cores = 16 }, nil, "small"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := holdStore(t, tt.hold, tt.worker)
			seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
			big := capTask("big", 8, 0, 30*time.Minute)
			if tt.big != nil {
				tt.big(big)
			}
			mustCreateTask(t, st, big)
			others := tt.others
			if others == nil {
				others = []*model.Task{capTask("small", 2, 0, 5*time.Minute)}
			}
			for _, tk := range others {
				mustCreateTask(t, st, tk)
			}

			if got := checkoutID(t, st, "wrk_a"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckoutTask_HoldReleasesToHolder(t *testing.T) {
	st := holdStore(t, 10*time.Minute, nil)
	seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
	mustCreateTask(t, st, capTask("big", 8, 0, 30*time.Minute))
	mustCreateTask(t, st, capTask("small", 2, 0, 5*time.Minute))

	if got := checkoutID(t, st, "wrk_a"); got != "" {
		t.Fatalf("checked out %q while big waits, want nothing", got)
	}
	// The running task ends: the freed capacity goes to the holder.
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE tasks SET state = ? WHERE id = ?`, string(model.TaskStateSuccess), "run1"); err != nil {
		t.Fatal(err)
	}
	if got := checkoutID(t, st, "wrk_a"); got != "big" {
		t.Fatalf("checked out %q after the running task ended, want big", got)
	}
}

// TestCheckoutTask_HoldDimensions: wrk_a (8 cores / 8000 MiB) runs a task
// charged run; B (30 min old) requests big; the other tasks are queued
// behind it.
func TestCheckoutTask_HoldDimensions(t *testing.T) {
	tests := []struct {
		name   string
		run    [2]int64 // cores, MiB
		big    [2]int64
		others []*model.Task
		want   string
	}{
		{"RAM-short holder holds RAM", [2]int64{0, 6000}, [2]int64{0, 8000},
			[]*model.Task{capTask("ram", 0, 2000, 5*time.Minute)}, ""},
		{"RAM-short holder: cores-only task taken", [2]int64{0, 6000}, [2]int64{0, 8000},
			[]*model.Task{capTask("cores", 2, 0, 5*time.Minute)}, "cores"},
		{"short on both: cores-only task held", [2]int64{6, 6000}, [2]int64{8, 8000},
			[]*model.Task{capTask("cores", 2, 0, 5*time.Minute)}, ""},
		{"short on both: RAM-only task held", [2]int64{6, 6000}, [2]int64{8, 8000},
			[]*model.Task{capTask("ram", 0, 2000, 5*time.Minute)}, ""},
		{"short on both: charge-0 task taken", [2]int64{6, 6000}, [2]int64{8, 8000},
			[]*model.Task{
				capTask("cores", 2, 0, 5*time.Minute),
				capTask("ram", 0, 2000, 4*time.Minute),
				capTask("zero", 0, 0, 3*time.Minute),
			}, "zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := holdStore(t, 10*time.Minute, nil)
			seedRunning(t, st, "run1", "wrk_a", intP(int(tt.run[0])), int64P(tt.run[1]))
			mustCreateTask(t, st, capTask("big", int(tt.big[0]), tt.big[1], 30*time.Minute))
			for _, tk := range tt.others {
				mustCreateTask(t, st, tk)
			}

			if got := checkoutID(t, st, "wrk_a"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckoutTask_HoldWorkerGateOnly: wrk_a and wrk_b share a host and key.
// wrk_a runs a task charged run (none when 0), wrk_b one charged other. B
// requests big; S (2 cores) fits both gates in every case. Only a shortfall
// on wrk_a's own gate that wrk_a draining would resolve on the host holds.
func TestCheckoutTask_HoldWorkerGateOnly(t *testing.T) {
	tests := []struct {
		name               string
		maxCPUs, hostCPUs  int
		run, other, bigCPU int
		want               string
	}{
		// Free on wrk_a's gate 16, on the host 4: short on the host only.
		{"host capacity used by another worker, worker idle", 16, 16, 0, 12, 8, "small"},
		// Free on wrk_a's gate 14 >= 8: not short on its own gate.
		{"not short on the worker gate", 16, 16, 2, 12, 8, "small"},
		// Short on wrk_a's gate (4 < 9), but drained the host leaves 20-12 = 8 < 9.
		{"worker draining cannot fit the host", 10, 20, 6, 12, 9, "small"},
		// Short on wrk_a's gate (4 < 9), drained the host leaves 20-11 = 9.
		{"worker draining fits the host", 10, 20, 6, 11, 9, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			st.SetCheckoutHoldAfter(10 * time.Minute)
			mk := func(id string, maxCPUs int) *model.Worker {
				return chargeWorker(id, func(w *model.Worker) {
					w.HostID, w.KeyID = "host-1", "key-1"
					w.HostCPUs, w.MaxCPUs = intP(tt.hostCPUs), intP(maxCPUs)
				})
			}
			mustCreateWorker(t, st, mk("wrk_a", tt.maxCPUs))
			mustCreateWorker(t, st, mk("wrk_b", tt.hostCPUs))
			if tt.run > 0 {
				seedRunning(t, st, "run_a", "wrk_a", intP(tt.run), int64P(0))
			}
			seedRunning(t, st, "run_b", "wrk_b", intP(tt.other), int64P(0))
			mustCreateTask(t, st, capTask("big", tt.bigCPU, 0, 30*time.Minute))
			mustCreateTask(t, st, capTask("small", 2, 0, 5*time.Minute))

			if got := checkoutID(t, st, "wrk_a"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckoutTask_HoldWindow: ten charged tasks behind the holder are all
// held; they must not fill the candidate window and hide the charge-0 task
// queued after them.
func TestCheckoutTask_HoldWindow(t *testing.T) {
	st := holdStore(t, 10*time.Minute, nil)
	seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
	mustCreateTask(t, st, capTask("big", 8, 0, 30*time.Minute))
	for i := 0; i < 10; i++ {
		mustCreateTask(t, st, capTask(fmt.Sprintf("small%d", i), 2, 0, 5*time.Minute-time.Duration(i)*10*time.Second))
	}
	mustCreateTask(t, st, capTask("zero", 0, 0, time.Minute))

	if got := checkoutID(t, st, "wrk_a"); got != "zero" {
		t.Errorf("checked out %q, want zero", got)
	}
}

// TestCheckoutTask_HoldProtectedTaskWins: capacity is free for B (8 cores, 30
// min old); a task behind it scores higher. B is taken regardless of score
// only when the worker held for it at its previous checkout.
func TestCheckoutTask_HoldProtectedTaskWins(t *testing.T) {
	cached := func() *model.Task {
		tk := capTask("scored", 2, 0, 5*time.Minute)
		tk.RuntimeHints.RequiredDatasets = []model.DatasetRequirement{{ID: "db", Mode: "cache"}}
		return tk
	}
	gpu := func() *model.Task {
		tk := capTask("scored", 2, 0, 5*time.Minute)
		tk.RuntimeHints.RequiresGPU = true
		return tk
	}
	withDataset := func(w *model.Worker) { w.Datasets = map[string]string{"db": "/data/db"} }
	withGPU := func(w *model.Worker) { w.GPUEnabled = true }
	tests := []struct {
		name   string
		hold   time.Duration
		held   bool // the worker held for big at its previous checkout
		worker func(w *model.Worker)
		scored func() *model.Task
		want   string
	}{
		{"cache match, held", 10 * time.Minute, true, withDataset, cached, "big"},
		{"cache match, never held", 10 * time.Minute, false, withDataset, cached, "scored"},
		{"cache match, hold off", 0, true, withDataset, cached, "scored"},
		{"GPU match, held", 10 * time.Minute, true, withGPU, gpu, "big"},
		{"GPU match, never held", 10 * time.Minute, false, withGPU, gpu, "scored"},
		{"GPU match, hold off", 0, true, withGPU, gpu, "scored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := holdStore(t, tt.hold, tt.worker)
			mustCreateTask(t, st, capTask("big", 8, 0, 30*time.Minute))
			if tt.held {
				// A running task leaves 2 cores free: with the hold on, the
				// first checkout holds for big and takes nothing.
				seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
				mustCreateTask(t, st, capTask("filler", 2, 0, time.Minute))
				want := ""
				if tt.hold == 0 {
					want = "filler"
				}
				if got := checkoutID(t, st, "wrk_a"); got != want {
					t.Fatalf("first checkout = %q, want %q", got, want)
				}
				if _, err := st.db.ExecContext(context.Background(),
					`UPDATE tasks SET state = ? WHERE state = 'RUNNING'`, string(model.TaskStateSuccess)); err != nil {
					t.Fatal(err)
				}
			}
			mustCreateTask(t, st, tt.scored())

			if got := checkoutID(t, st, "wrk_a"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckoutTask_HoldNonDefaultGroup: the base scenario of
// TestCheckoutTask_Hold on a worker of group "x" with tasks tagged "x".
func TestCheckoutTask_HoldNonDefaultGroup(t *testing.T) {
	tests := []struct {
		name string
		hold time.Duration
		want string
	}{
		{"hold off", 0, "small"},
		{"hold", 10 * time.Minute, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := holdStore(t, tt.hold, func(w *model.Worker) { w.Group = "x" })
			seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
			for _, tk := range []*model.Task{
				capTask("big", 8, 0, 30*time.Minute),
				capTask("small", 2, 0, 5*time.Minute),
			} {
				tk.RuntimeHints.WorkerGroup = "x"
				mustCreateTask(t, st, tk)
			}

			if got := checkoutGroupID(t, st, "wrk_a", "x"); got != tt.want {
				t.Errorf("checked out %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckoutTask_HoldProtectedTaskKeepsQueueOrder: a task ahead of the
// held-for task in queue order still goes first once capacity frees.
func TestCheckoutTask_HoldProtectedTaskKeepsQueueOrder(t *testing.T) {
	st := holdStore(t, 10*time.Minute, nil)
	seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
	mustCreateTask(t, st, capTask("big", 8, 0, 30*time.Minute))
	mustCreateTask(t, st, capTask("small", 2, 0, 5*time.Minute))
	if got := checkoutID(t, st, "wrk_a"); got != "" {
		t.Fatalf("first checkout = %q, want nothing (hold)", got)
	}
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE tasks SET state = ? WHERE id = ?`, string(model.TaskStateSuccess), "run1"); err != nil {
		t.Fatal(err)
	}
	urgent := capTask("urgent", 8, 0, time.Minute)
	urgent.Priority = 5
	mustCreateTask(t, st, urgent)

	if got := checkoutID(t, st, "wrk_a"); got != "urgent" {
		t.Errorf("checked out %q, want urgent (higher priority than the held-for task)", got)
	}
}

// TestCheckoutTask_HoldByPriority: a holder with a higher priority holds an
// older, lower-priority charged task.
func TestCheckoutTask_HoldByPriority(t *testing.T) {
	st := holdStore(t, 10*time.Minute, nil)
	seedRunning(t, st, "run1", "wrk_a", intP(6), int64P(0))
	big := capTask("big", 8, 0, 30*time.Minute)
	big.Priority = 5
	mustCreateTask(t, st, big)
	mustCreateTask(t, st, capTask("older", 2, 0, time.Hour))

	if got := checkoutID(t, st, "wrk_a"); got != "" {
		t.Errorf("checked out %q, want nothing (older task has lower priority than the holder)", got)
	}
}
