package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestReleaseWorkerTask(t *testing.T) {
	tests := []struct {
		name    string
		current string                   // worker's current_task before the call
		others  map[string]time.Duration // the worker's other RUNNING tasks, by started-ago
		want    string
	}{
		{"points at the most recently started other running task", "t_done",
			map[string]time.Duration{"t_old": 10 * time.Minute, "t_new": time.Minute}, "t_new"},
		{"clears when no other task is running", "t_done", nil, ""},
		{"leaves another current task unchanged", "t_old",
			map[string]time.Duration{"t_old": 10 * time.Minute, "t_new": time.Minute}, "t_old"},
		{"moves off a task the worker no longer runs", "t_finished",
			map[string]time.Duration{"t_old": 10 * time.Minute, "t_new": time.Minute}, "t_new"},
		{"clears a task the worker no longer runs when nothing else runs", "t_finished", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, sub := reconcileFixture(t)
			mustCreateWorker(t, st, chargeWorker("wrk_rel", func(w *model.Worker) {
				w.CurrentTask = tt.current
				w.MaxCPUs = intP(8)
				w.Usage = &model.WorkerUsage{Host: &model.HostUsage{Load1: 1.5, SampledAt: w.LastSeen}}
			}))
			before, err := st.GetWorker(ctx, "wrk_rel")
			if err != nil || before == nil {
				t.Fatalf("GetWorker = (%v, %v)", before, err)
			}

			// The released task is still RUNNING and the most recent: the id
			// exclusion must skip it.
			runningTask(t, st, sub, "t_done", "wrk_rel", 0)
			for id, ago := range tt.others {
				runningTask(t, st, sub, id, "wrk_rel", ago)
			}
			// More recent than every candidate but not eligible: another
			// worker's RUNNING task and this worker's finished task.
			runningTask(t, st, sub, "t_foreign", "wrk_other", 30*time.Second)
			finished := sampleTask(sub.ID)
			finished.ID = "t_finished"
			finished.State = model.TaskStateSuccess
			finished.ExecutorType = model.ExecutorTypeWorker
			finished.ExternalID = "wrk_rel"
			started := time.Now().UTC().Add(-30 * time.Second)
			finished.StartedAt = &started
			mustCreateTask(t, st, finished)

			if err := st.ReleaseWorkerTask(ctx, "wrk_rel", "t_done"); err != nil {
				t.Fatalf("ReleaseWorkerTask: %v", err)
			}

			got, err := st.GetWorker(ctx, "wrk_rel")
			if err != nil || got == nil {
				t.Fatalf("GetWorker = (%v, %v)", got, err)
			}
			if got.CurrentTask != tt.want {
				t.Errorf("current_task = %q, want %q", got.CurrentTask, tt.want)
			}
			// No other column is written.
			if !got.LastSeen.Equal(before.LastSeen) {
				t.Errorf("last_seen = %v, want %v (untouched)", got.LastSeen, before.LastSeen)
			}
			if got.State != before.State {
				t.Errorf("state = %s, want %s (untouched)", got.State, before.State)
			}
			if ptrStr(got.MaxCPUs) != "8" {
				t.Errorf("max_cpus = %s, want 8 (untouched)", ptrStr(got.MaxCPUs))
			}
			if got.Usage == nil || got.Usage.Host == nil || got.Usage.Host.Load1 != 1.5 {
				t.Errorf("host_usage = %+v, want load1 1.5 (untouched)", got.Usage)
			}
		})
	}
}

func TestReleaseWorkerTask_UnknownWorker(t *testing.T) {
	st := testStore(t)
	if err := st.ReleaseWorkerTask(context.Background(), "wrk_unknown", "t_1"); err != nil {
		t.Errorf("ReleaseWorkerTask(unknown worker) = %v, want nil", err)
	}
}

// ReconcileWorkerTasks applies the same rule when it requeues a subset of a
// worker's tasks: current_task moves to a task still running, never to empty.
func TestReconcileWorkerTasks_MovesCurrentTask(t *testing.T) {
	tests := []struct {
		name    string
		running []string // reported by the worker
		want    string
	}{
		{"moves to a task still running", []string{"task_kept"}, "task_kept"},
		{"clears when every task is orphaned", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, sub := reconcileFixture(t)
			mustCreateWorker(t, st, chargeWorker("wrk_alive", func(w *model.Worker) {
				w.CurrentTask = "task_orphan"
			}))
			runningTask(t, st, sub, "task_kept", "wrk_alive", 10*time.Minute)
			runningTask(t, st, sub, "task_orphan", "wrk_alive", 5*time.Minute)

			if _, err := st.ReconcileWorkerTasks(ctx, "wrk_alive", tt.running, time.Minute); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			got, err := st.GetWorker(ctx, "wrk_alive")
			if err != nil || got == nil {
				t.Fatalf("GetWorker = (%v, %v)", got, err)
			}
			if got.CurrentTask != tt.want {
				t.Errorf("current_task = %q, want %q", got.CurrentTask, tt.want)
			}
		})
	}
}

func TestReapStaleWorkers(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	old := time.Now().UTC().Add(-5 * time.Minute)

	mustCreateWorker(t, st, chargeWorker("wrk_stale", func(w *model.Worker) {
		w.LastSeen, w.CurrentTask = old, "s2"
	}))
	mustCreateWorker(t, st, chargeWorker("wrk_fresh", func(w *model.Worker) {
		w.CurrentTask = "f1"
	}))
	// Offline before this pass with its task still RUNNING (stranded).
	mustCreateWorker(t, st, chargeWorker("wrk_gone", func(w *model.Worker) {
		w.State, w.LastSeen, w.CurrentTask = model.WorkerStateOffline, old, "g1"
	}))
	// Offline but just checked out (checkout bumps last_seen): not stranded.
	mustCreateWorker(t, st, chargeWorker("wrk_back", func(w *model.Worker) {
		w.State, w.CurrentTask = model.WorkerStateOffline, "b1"
	}))
	owner := map[string]string{
		"s1": "wrk_stale", "s2": "wrk_stale", "f1": "wrk_fresh", "g1": "wrk_gone", "b1": "wrk_back",
	}
	for id, w := range owner {
		seedRunning(t, st, id, w, intP(2), int64P(1000))
	}

	reaped, stranded, err := st.ReapStaleWorkers(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ReapStaleWorkers: %v", err)
	}
	if len(reaped) != 1 || reaped[0].Worker == nil || reaped[0].Worker.ID != "wrk_stale" {
		t.Fatalf("reaped = %+v, want only wrk_stale", reaped)
	}
	if reaped[0].Requeued != 2 {
		t.Errorf("wrk_stale requeued = %d, want 2", reaped[0].Requeued)
	}
	if reaped[0].Worker.State != model.WorkerStateOffline {
		t.Errorf("returned wrk_stale state = %s, want offline", reaped[0].Worker.State)
	}
	if stranded != 1 {
		t.Errorf("stranded = %d, want 1 (g1)", stranded)
	}

	requeued := map[string]bool{"s1": true, "s2": true, "g1": true}
	for id, w := range owner {
		got, err := st.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s) = (%v, %v)", id, got, err)
		}
		if requeued[id] {
			if got.State != model.TaskStateQueued || got.ExternalID != "" || got.StartedAt != nil ||
				got.ChargedCores != nil || got.ChargedRamMB != nil {
				t.Errorf("task %s = %s/%q started %v charge %s/%s, want QUEUED/\"\" nil nil/nil",
					id, got.State, got.ExternalID, got.StartedAt, ptrStr(got.ChargedCores), ptrStr(got.ChargedRamMB))
			}
			continue
		}
		if got.State != model.TaskStateRunning || got.ExternalID != w || got.StartedAt == nil {
			t.Errorf("task %s = %s/%q, want RUNNING/%q (untouched)", id, got.State, got.ExternalID, w)
		}
		checkCharge(t, id, got, 2, 1000)
	}

	for _, tc := range []struct {
		id      string
		state   model.WorkerState
		current string
	}{
		{"wrk_stale", model.WorkerStateOffline, ""},
		{"wrk_fresh", model.WorkerStateOnline, "f1"},
		{"wrk_gone", model.WorkerStateOffline, ""},
		{"wrk_back", model.WorkerStateOffline, "b1"},
	} {
		got, err := st.GetWorker(ctx, tc.id)
		if err != nil || got == nil {
			t.Fatalf("GetWorker(%s) = (%v, %v)", tc.id, got, err)
		}
		if got.State != tc.state || got.CurrentTask != tc.current {
			t.Errorf("worker %s = %s/%q, want %s/%q", tc.id, got.State, got.CurrentTask, tc.state, tc.current)
		}
	}

	// A second pass finds nothing left to do.
	reaped, stranded, err = st.ReapStaleWorkers(ctx, time.Minute)
	if err != nil || len(reaped) != 0 || stranded != 0 {
		t.Errorf("second pass = (%+v, %d, %v), want (none, 0, nil)", reaped, stranded, err)
	}
}
