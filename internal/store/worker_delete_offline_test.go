package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// DeleteOfflineWorker deletes only an offline worker that has been silent
// for silentFor (and, with a group, is in that group), and requeues its
// RUNNING task in the same transaction; otherwise nothing changes.
func TestDeleteOfflineWorker(t *testing.T) {
	const silentFor = 30 * time.Second
	old := time.Now().UTC().Add(-time.Hour)
	recent := time.Now().UTC().Add(-time.Second)
	tests := []struct {
		name        string
		state       model.WorkerState
		lastSeen    time.Time
		storedGroup string // "" stores an empty worker_group
		group       string // DeleteOfflineWorker's group argument
		wantDeleted bool
	}{
		{"online worker is not deleted", model.WorkerStateOnline, old, "gpu", "", false},
		{"draining worker is not deleted", model.WorkerStateDraining, old, "gpu", "", false},
		{"offline but recently seen is not deleted", model.WorkerStateOffline, recent, "gpu", "", false},
		{"offline and silent in another group is not deleted", model.WorkerStateOffline, old, "gpu", "cpu", false},
		{"offline and silent, any group", model.WorkerStateOffline, old, "gpu", "", true},
		{"offline and silent in the group", model.WorkerStateOffline, old, "gpu", "gpu", true},
		{"offline and silent in default", model.WorkerStateOffline, old, "default", "default", true},
		{"empty stored group is default", model.WorkerStateOffline, old, "", "default", true},
		{"empty stored group is not gpu", model.WorkerStateOffline, old, "", "gpu", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()

			wf := sampleWorkflow()
			if err := st.CreateWorkflow(ctx, wf); err != nil {
				t.Fatal(err)
			}
			sub := sampleSubmission(wf.ID)
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatal(err)
			}

			w := sampleWorker()
			w.State = tt.state
			w.LastSeen = tt.lastSeen
			w.Group = tt.storedGroup
			if err := st.CreateWorker(ctx, w); err != nil {
				t.Fatal(err)
			}
			if tt.storedGroup == "" {
				// CreateWorker stores "" as "default"; rows written before
				// that may hold "".
				if _, err := st.db.ExecContext(ctx, `UPDATE workers SET worker_group = '' WHERE id = ?`, w.ID); err != nil {
					t.Fatal(err)
				}
			}

			started := time.Now().UTC()
			task := sampleTask(sub.ID)
			task.ID = "task_running"
			task.State = model.TaskStateRunning
			task.ExecutorType = model.ExecutorTypeWorker
			task.ExternalID = w.ID
			task.StartedAt = &started
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}

			deleted, err := st.DeleteOfflineWorker(ctx, w.ID, tt.group, silentFor)
			if err != nil {
				t.Fatalf("DeleteOfflineWorker: %v", err)
			}
			if deleted != tt.wantDeleted {
				t.Fatalf("deleted = %v, want %v", deleted, tt.wantDeleted)
			}

			got, err := st.GetWorker(ctx, w.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != tt.wantDeleted {
				t.Errorf("worker row present = %v, want %v", got != nil, !tt.wantDeleted)
			}

			gotTask, err := st.GetTask(ctx, task.ID)
			if err != nil || gotTask == nil {
				t.Fatalf("GetTask: %v %v", gotTask, err)
			}
			if tt.wantDeleted {
				if gotTask.State != model.TaskStateQueued || gotTask.ExternalID != "" || gotTask.StartedAt != nil {
					t.Errorf("task = %s external_id=%q started_at=%v, want QUEUED, '' and nil",
						gotTask.State, gotTask.ExternalID, gotTask.StartedAt)
				}
			} else if gotTask.State != model.TaskStateRunning || gotTask.ExternalID != w.ID {
				t.Errorf("task = %s external_id=%q, want untouched RUNNING on %s", gotTask.State, gotTask.ExternalID, w.ID)
			}
		})
	}
}

// peer_addr is added idempotently and round-trips through CreateWorker.
func TestWorkerPeerAddrColumn(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if !hasColumn(t, st, "workers", "peer_addr") {
		t.Fatal("column workers.peer_addr missing")
	}
	w := sampleWorker()
	w.PeerAddr = "192.0.2.8"
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil || got.PeerAddr != "192.0.2.8" {
		t.Errorf("GetWorker = %+v, %v; want peer_addr 192.0.2.8", got, err)
	}
}

// A checkout for a worker whose row is gone (removed after the caller's
// authorisation read) hands out nothing and leaves the task QUEUED and
// claimable by another worker.
func TestCheckoutTask_DeletedWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, chargeWorker("wrk_gone", nil))
	mustCreateWorker(t, st, chargeWorker("wrk_other", nil))
	mustCreateTask(t, st, capTask("t1", 1, 100, time.Minute))
	if err := st.DeleteWorker(ctx, "wrk_gone"); err != nil {
		t.Fatal(err)
	}

	got, err := st.CheckoutTask(ctx, "wrk_gone", "", model.RuntimeNone)
	if err != nil || got != nil {
		t.Fatalf("checkout for a deleted worker = (%v, %v), want (nil, nil)", got, err)
	}
	task, err := st.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("GetTask = (%v, %v)", task, err)
	}
	if task.State != model.TaskStateQueued || task.ExternalID != "" || task.StartedAt != nil ||
		task.ChargedCores != nil || task.ChargedRamMB != nil || task.ChargedGPUs != nil {
		t.Errorf("task after the refused checkout = %s external_id=%q started_at=%v charge=%s/%s, want untouched QUEUED",
			task.State, task.ExternalID, task.StartedAt, ptrStr(task.ChargedCores), ptrStr(task.ChargedRamMB))
	}

	got, err = st.CheckoutTask(ctx, "wrk_other", "", model.RuntimeNone)
	if err != nil || got == nil || got.ID != "t1" || got.ExternalID != "wrk_other" {
		t.Fatalf("checkout by another worker = (%+v, %v), want t1", got, err)
	}
}

// A heartbeat with an unknown (empty) peer address keeps the stored one; a
// known one replaces it.
func TestTouchWorker_EmptyPeerAddrKeepsStored(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	w.PeerAddr = "192.0.2.8"
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	peer := func() string {
		t.Helper()
		got, err := st.GetWorker(ctx, w.ID)
		if err != nil || got == nil {
			t.Fatalf("GetWorker: %v %v", got, err)
		}
		return got.PeerAddr
	}
	for _, usage := range []*model.WorkerUsage{nil, testUsage(1)} {
		if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOnline, time.Now().UTC(), "", usage); err != nil {
			t.Fatal(err)
		}
		if got := peer(); got != "192.0.2.8" {
			t.Errorf("peer_addr after a heartbeat without one (usage %v) = %q, want 192.0.2.8", usage != nil, got)
		}
	}
	if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOnline, time.Now().UTC(), "198.51.100.1", nil); err != nil {
		t.Fatal(err)
	}
	if got := peer(); got != "198.51.100.1" {
		t.Errorf("peer_addr after a heartbeat with one = %q, want 198.51.100.1", got)
	}
}

func TestDeleteOfflineWorker_Missing(t *testing.T) {
	st := testStore(t)
	deleted, err := st.DeleteOfflineWorker(context.Background(), "wrk_missing", "", time.Second)
	if err != nil || deleted {
		t.Errorf("DeleteOfflineWorker(missing) = %v, %v; want false, nil", deleted, err)
	}
}
