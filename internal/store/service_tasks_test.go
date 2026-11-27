package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// A service task (gowe:Service, tasks.is_service) is never started a second
// time: checkout matches it only with a service-mode worker, no requeue path
// puts it back in the queue, a silent worker's services stay RUNNING with
// their charges (marked lost), and a worker holding one is not deleted.

const svcGroup = "llm"

// svcSpec is a minimal service spec.
func svcSpec() *model.ServiceSpec { return &model.ServiceSpec{PortInput: "port"} }

// svcQueued returns a QUEUED worker task of group svcGroup asking for cores
// and gpus; a service task when service is set.
func svcQueued(id string, service bool, cores, gpus int, age time.Duration) *model.Task {
	tk := capTask(id, cores, 0, age)
	tk.RuntimeHints.WorkerGroup = svcGroup
	if gpus > 0 {
		tk.RuntimeHints.RequiresGPU, tk.RuntimeHints.GPUs = true, gpus
	}
	if service {
		tk.RuntimeHints.Service = svcSpec()
	}
	return tk
}

// svcWorker returns an online worker of group svcGroup, in service mode
// when service is set, modified by mod.
func svcWorker(id string, service bool, mod func(w *model.Worker)) *model.Worker {
	return chargeWorker(id, func(w *model.Worker) {
		w.Group = svcGroup
		w.ServiceMode = service
		if mod != nil {
			mod(w)
		}
	})
}

// oneGPU makes w a GPU worker with an inventory of one GPU.
func oneGPU(w *model.Worker) {
	w.GPUEnabled = true
	w.GPUs = []model.GPUInfo{{Index: 0, UUID: "GPU-0", Name: "H200", MemTotalMB: 141000}}
}

// seedRunningOn stores a RUNNING worker task of workerID, started an hour ago
// and charged cores and gpus; a service task when service is set.
func seedRunningOn(t *testing.T, st *SQLStore, id, workerID string, service bool, cores, gpus int) {
	t.Helper()
	tk := svcQueued(id, service, cores, gpus, 2*time.Hour)
	tk.State = model.TaskStateRunning
	tk.ExternalID = workerID
	started := time.Now().UTC().Add(-time.Hour)
	tk.StartedAt = &started
	tk.ChargedCores, tk.ChargedRamMB, tk.ChargedGPUs = intP(cores), int64P(0), intP(gpus)
	mustCreateTask(t, st, tk)
}

func mustGetTask(t *testing.T, st *SQLStore, id string) *model.Task {
	t.Helper()
	tk, err := st.GetTask(context.Background(), id)
	if err != nil || tk == nil {
		t.Fatalf("GetTask(%s) = (%v, %v)", id, tk, err)
	}
	return tk
}

// checkServiceRunning fails unless task id is still RUNNING on workerID with
// its GPU charge and has no archived attempt.
func checkServiceRunning(t *testing.T, st *SQLStore, id, workerID string, gpus int) *model.Task {
	t.Helper()
	tk := mustGetTask(t, st, id)
	if tk.State != model.TaskStateRunning || tk.ExternalID != workerID || tk.StartedAt == nil {
		t.Errorf("service %s = %s on %q (started %v), want RUNNING on %s", id, tk.State, tk.ExternalID, tk.StartedAt, workerID)
	}
	if tk.ChargedGPUs == nil || *tk.ChargedGPUs != gpus || tk.ChargedCores == nil {
		t.Errorf("service %s charge = cores %s gpus %s, want gpus %d kept", id, ptrStr(tk.ChargedCores), ptrStr(tk.ChargedGPUs), gpus)
	}
	if attempts, err := st.ListTaskAttempts(context.Background(), id); err != nil || len(attempts) != 0 {
		t.Errorf("service %s attempts = %+v, %v; want none archived", id, attempts, err)
	}
	return tk
}

func checkRequeued(t *testing.T, st *SQLStore, id string) {
	t.Helper()
	tk := mustGetTask(t, st, id)
	if tk.State != model.TaskStateQueued || tk.ExternalID != "" || tk.ChargedGPUs != nil {
		t.Errorf("ordinary task %s = %s on %q (gpus %s), want QUEUED, unowned, uncharged", id, tk.State, tk.ExternalID, ptrStr(tk.ChargedGPUs))
	}
}

func TestServiceColumns_RoundTrip(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// is_service is derived from the hints at insert, whatever IsService says.
	svc := svcQueued("svc", true, 0, 0, time.Minute)
	svc.IsService = false
	mustCreateTask(t, st, svc)
	plain := svcQueued("plain", false, 0, 0, time.Minute)
	plain.IsService = true
	mustCreateTask(t, st, plain)

	if got := mustGetTask(t, st, "svc"); !got.IsService || got.ServiceLostAt != nil {
		t.Errorf("service task: is_service %v, lost %v; want true, nil", got.IsService, got.ServiceLostAt)
	}
	if got := mustGetTask(t, st, "plain"); got.IsService {
		t.Error("ordinary task read back as a service")
	}
	tasks, err := st.ListTasksBySubmission(ctx, "sub_1")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("ListTasksBySubmission = %d tasks, %v", len(tasks), err)
	}
	for _, tk := range tasks {
		if tk.IsService != (tk.ID == "svc") {
			t.Errorf("list: %s is_service = %v", tk.ID, tk.IsService)
		}
	}

	mustCreateWorker(t, st, svcWorker("wrk_svc", true, nil))
	mustCreateWorker(t, st, svcWorker("wrk_plain", false, nil))
	for id, want := range map[string]bool{"wrk_svc": true, "wrk_plain": false} {
		w, err := st.GetWorker(ctx, id)
		if err != nil || w == nil || w.ServiceMode != want {
			t.Errorf("GetWorker(%s) = %+v, %v; want service_mode %v", id, w, err, want)
		}
	}

	for _, c := range []struct{ table, col string }{
		{"tasks", "is_service"}, {"tasks", "service_lost_at"}, {"tasks", "service_missing_at"},
		{"tasks", "service_stop_requested_at"}, {"workers", "service_mode"},
	} {
		if !hasColumn(t, st, c.table, c.col) {
			t.Errorf("column %s.%s missing", c.table, c.col)
		}
	}
}

// A service task is offered only to a service-mode worker, and a
// service-mode worker only service tasks.
func TestCheckoutTask_ServiceMatch(t *testing.T) {
	tests := []struct {
		name          string
		serviceTask   bool
		serviceWorker bool
		want          bool
	}{
		{"service task, service-mode worker", true, true, true},
		{"service task, ordinary worker", true, false, false},
		{"ordinary task, service-mode worker", false, true, false},
		{"ordinary task, ordinary worker", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_a", tt.serviceWorker, nil))
			mustCreateTask(t, st, svcQueued("t1", tt.serviceTask, 0, 0, time.Minute))
			got, err := st.CheckoutTask(context.Background(), "wrk_a", svcGroup, model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Fatalf("checked out = %v, want %v", got != nil, tt.want)
			}
			if got != nil && got.IsService != tt.serviceTask {
				t.Errorf("returned task is_service = %v, want %v", got.IsService, tt.serviceTask)
			}
		})
	}

	// The filter is in SQL: tasks of the other kind ahead in the queue do
	// not fill the candidate window.
	for _, serviceWorker := range []bool{false, true} {
		t.Run(fmt.Sprintf("other kind ahead does not hide a match (service worker %v)", serviceWorker), func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_a", serviceWorker, nil))
			for i := 0; i < 11; i++ {
				mustCreateTask(t, st, svcQueued(fmt.Sprintf("other_%02d", i), !serviceWorker, 0, 0, time.Hour+time.Duration(11-i)*time.Minute))
			}
			mustCreateTask(t, st, svcQueued("match", serviceWorker, 0, 0, time.Minute))
			got, err := st.CheckoutTask(context.Background(), "wrk_a", svcGroup, model.RuntimeNone)
			if err != nil || got == nil || got.ID != "match" {
				t.Fatalf("checkout = (%v, %v), want match", got, err)
			}
		})
	}
}

// Every requeue path leaves a RUNNING service task RUNNING, owned, charged
// and unarchived, while the ordinary task of the same worker is requeued.
func TestRequeuePaths_ServiceStaysRunning(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour)
	tests := []struct {
		name    string
		state   model.WorkerState
		requeue func(t *testing.T, st *SQLStore)
	}{
		{"RequeueWorkerTasks", model.WorkerStateOnline, func(t *testing.T, st *SQLStore) {
			if n, err := st.RequeueWorkerTasks(context.Background(), "wrk_s"); err != nil || n != 1 {
				t.Fatalf("RequeueWorkerTasks = %d, %v; want 1", n, err)
			}
		}},
		{"ReapStaleWorkers, newly offline", model.WorkerStateOnline, func(t *testing.T, st *SQLStore) {
			reaped, _, err := st.ReapStaleWorkers(context.Background(), time.Minute)
			if err != nil || len(reaped) != 1 || reaped[0].Requeued != 1 {
				t.Fatalf("ReapStaleWorkers = %+v, %v; want one worker, 1 requeued", reaped, err)
			}
		}},
		{"ReapStaleWorkers, already offline (stranded)", model.WorkerStateOffline, func(t *testing.T, st *SQLStore) {
			if _, stranded, err := st.ReapStaleWorkers(context.Background(), time.Minute); err != nil || stranded != 1 {
				t.Fatalf("ReapStaleWorkers stranded = %d, %v; want 1", stranded, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
				oneGPU(w)
				w.State, w.LastSeen = tt.state, old
			}))
			seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
			seedRunningOn(t, st, "plain", "wrk_s", false, 2, 0)

			tt.requeue(t, st)

			checkServiceRunning(t, st, "svc", "wrk_s", 1)
			checkRequeued(t, st, "plain")
			if attempts, err := st.ListTaskAttempts(context.Background(), "plain"); err != nil || len(attempts) != 1 {
				t.Errorf("ordinary task attempts = %+v, %v; want one archived", attempts, err)
			}
		})
	}
}

// The reaper marks the services of an offline worker lost once and keeps
// current_task on them; a heartbeat (TouchWorker) clears the mark.
func TestReapStaleWorkers_ServiceLostAndFound(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
		oneGPU(w)
		w.LastSeen, w.CurrentTask = old, "svc"
	}))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	// A service of an online worker is not touched.
	mustCreateWorker(t, st, svcWorker("wrk_live", true, nil))
	seedRunningOn(t, st, "svc_live", "wrk_live", true, 4, 0)

	before := time.Now().UTC().Add(-time.Second)
	if _, _, err := st.ReapStaleWorkers(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	lost := checkServiceRunning(t, st, "svc", "wrk_s", 1)
	if lost.ServiceLostAt == nil || lost.ServiceLostAt.Before(before) {
		t.Fatalf("service_lost_at = %v, want set by the reaper", lost.ServiceLostAt)
	}
	if live := mustGetTask(t, st, "svc_live"); live.ServiceLostAt != nil {
		t.Errorf("service of an online worker marked lost: %v", live.ServiceLostAt)
	}
	w, err := st.GetWorker(ctx, "wrk_s")
	if err != nil || w == nil {
		t.Fatalf("GetWorker = (%v, %v)", w, err)
	}
	if w.State != model.WorkerStateOffline || w.CurrentTask != "svc" {
		t.Errorf("worker = %s, current_task %q; want offline, current_task svc (still running it)", w.State, w.CurrentTask)
	}

	// A second pass keeps the first mark.
	if _, _, err := st.ReapStaleWorkers(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if again := mustGetTask(t, st, "svc"); again.ServiceLostAt == nil || !again.ServiceLostAt.Equal(*lost.ServiceLostAt) {
		t.Errorf("service_lost_at after a second pass = %v, want %v", again.ServiceLostAt, lost.ServiceLostAt)
	}

	// The worker is seen again.
	if err := st.TouchWorker(ctx, "wrk_s", model.WorkerStateOnline, time.Now().UTC(), "", nil); err != nil {
		t.Fatal(err)
	}
	if found := checkServiceRunning(t, st, "svc", "wrk_s", 1); found.ServiceLostAt != nil {
		t.Errorf("service_lost_at after a heartbeat = %v, want cleared", found.ServiceLostAt)
	}
}

// A lost service keeps its GPU: its worker is not offered a second task that
// needs the GPU.
func TestCheckoutTask_LostServiceKeepsGPU(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
		oneGPU(w)
		w.LastSeen = time.Now().UTC().Add(-time.Hour)
	}))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 1)
	mustCreateTask(t, st, svcQueued("svc2", true, 0, 1, time.Minute))

	if _, _, err := st.ReapStaleWorkers(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone)
	if err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want (nil, nil): the lost service still holds the only GPU", got, err)
	}
}

// The host sums count a RUNNING service of an offline worker: another worker
// of the same host and key is not offered what would not fit beside it.
func TestCheckoutTask_LostServiceCountsOnHost(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	onHost := func(w *model.Worker) {
		w.HostID, w.KeyID = "host-1", "key-1"
		w.HostCPUs, w.HostMemMB = intP(16), int64P(64000)
		w.MaxCPUs, w.MaxMemMB = intP(16), int64P(64000)
	}
	mustCreateWorker(t, st, svcWorker("wrk_s", true, onHost))
	mustCreateWorker(t, st, svcWorker("wrk_b", false, onHost))
	seedRunningOn(t, st, "svc", "wrk_s", true, 12, 0)
	if _, err := st.db.ExecContext(ctx, `UPDATE workers SET state = 'offline' WHERE id = ?`, "wrk_s"); err != nil {
		t.Fatal(err)
	}
	mustCreateTask(t, st, svcQueued("big", false, 8, 0, time.Minute))

	got, err := st.CheckoutTask(ctx, "wrk_b", svcGroup, model.RuntimeNone)
	if err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want (nil, nil): 12 of 16 host cores are held by the lost service", got, err)
	}

	// Control: an ordinary task of the offline worker is not counted.
	st2 := testStore(t)
	mustCreateWorker(t, st2, svcWorker("wrk_a", false, onHost))
	mustCreateWorker(t, st2, svcWorker("wrk_b", false, onHost))
	seedRunningOn(t, st2, "plain", "wrk_a", false, 12, 0)
	if _, err := st2.db.ExecContext(ctx, `UPDATE workers SET state = 'offline' WHERE id = ?`, "wrk_a"); err != nil {
		t.Fatal(err)
	}
	mustCreateTask(t, st2, svcQueued("big", false, 8, 0, time.Minute))
	if got, err := st2.CheckoutTask(ctx, "wrk_b", svcGroup, model.RuntimeNone); err != nil || got == nil {
		t.Fatalf("control checkout = (%v, %v), want big", got, err)
	}
}

// A worker that holds a RUNNING service is not deleted: the store returns
// the typed sentinel and changes nothing.
func TestDeleteWorker_RefusedWithServices(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour)
	tests := []struct {
		name   string
		delete func(st *SQLStore) (bool, error)
	}{
		{"DeleteWorker", func(st *SQLStore) (bool, error) {
			err := st.DeleteWorker(context.Background(), "wrk_s")
			return err == nil, err
		}},
		{"DeleteOfflineWorker", func(st *SQLStore) (bool, error) {
			return st.DeleteOfflineWorker(context.Background(), "wrk_s", svcGroup, time.Minute)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
				oneGPU(w)
				w.State, w.LastSeen = model.WorkerStateOffline, old
			}))
			seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
			seedRunningOn(t, st, "plain", "wrk_s", false, 2, 0)

			deleted, err := tt.delete(st)
			if deleted || !errors.Is(err, ErrWorkerHasServices) {
				t.Fatalf("delete = (%v, %v), want ErrWorkerHasServices", deleted, err)
			}
			var svcErr *WorkerServicesError
			if !errors.As(err, &svcErr) || svcErr.Count != 1 || svcErr.WorkerID != "wrk_s" {
				t.Errorf("error = %#v, want a WorkerServicesError for wrk_s with count 1", err)
			}
			const want = "worker still holds 1 running service(s); cancel their submissions, or force-release them if the worker is gone"
			if err.Error() != want {
				t.Errorf("message = %q, want %q", err.Error(), want)
			}
			if w, err := st.GetWorker(context.Background(), "wrk_s"); err != nil || w == nil {
				t.Errorf("worker = (%v, %v), want kept", w, err)
			}
			checkServiceRunning(t, st, "svc", "wrk_s", 1)
			// Rolled back as a whole: the ordinary task is not requeued
			// or archived either.
			if plain := mustGetTask(t, st, "plain"); plain.State != model.TaskStateRunning || plain.ExternalID != "wrk_s" {
				t.Errorf("ordinary task = %s on %q, want untouched", plain.State, plain.ExternalID)
			}
			if attempts, err := st.ListTaskAttempts(context.Background(), "plain"); err != nil || len(attempts) != 0 {
				t.Errorf("ordinary task attempts = %+v, %v; want none", attempts, err)
			}
		})
	}

	// A worker that is not removable for another reason keeps that answer.
	t.Run("online worker: not removable, no error", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) { w.LastSeen = old }))
		seedRunningOn(t, st, "svc", "wrk_s", true, 4, 0)
		if deleted, err := st.DeleteOfflineWorker(context.Background(), "wrk_s", "", time.Minute); deleted || err != nil {
			t.Fatalf("DeleteOfflineWorker = (%v, %v), want (false, nil)", deleted, err)
		}
	})
}

// setServiceMissingAt writes tasks.service_missing_at of task id directly.
func setServiceMissingAt(t *testing.T, st *SQLStore, id string, at time.Time) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE tasks SET service_missing_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
		t.Fatal(err)
	}
}

// A live worker whose heartbeats (with running_tasks) no longer list a
// RUNNING service get it marked missing, then, once the mark is older than
// the grace window, FAILED (never requeued); its ordinary orphan is requeued
// as before.
func TestReconcileWorkerTasks_MissingServiceFailed(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
		oneGPU(w)
		w.CurrentTask = "svc"
	}))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	seedRunningOn(t, st, "plain", "wrk_s", false, 2, 0)

	requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_s", []string{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(requeued) != 1 || requeued[0] != "plain" {
		t.Errorf("requeued = %v, want [plain]", requeued)
	}
	checkRequeued(t, st, "plain")
	if marked := checkServiceRunning(t, st, "svc", "wrk_s", 1); marked.ServiceMissingAt == nil {
		t.Fatal("first omission: service_missing_at not set")
	}

	setServiceMissingAt(t, st, "svc", time.Now().UTC().Add(-2*time.Minute))
	if _, err := st.ReconcileWorkerTasks(ctx, "wrk_s", []string{}, time.Minute); err != nil {
		t.Fatal(err)
	}

	svc := mustGetTask(t, st, "svc")
	if svc.State != model.TaskStateFailed || svc.Stderr != ServiceMissingStderr || svc.ExitCode != nil || svc.CompletedAt == nil {
		t.Fatalf("service = %s stderr %q exit %v completed %v, want FAILED %q, no exit code",
			svc.State, svc.Stderr, svc.ExitCode, svc.CompletedAt, ServiceMissingStderr)
	}
	if svc.ExternalID != "wrk_s" || svc.RetryCount != svc.MaxRetries {
		t.Errorf("service owner %q, retry %d/%d; want wrk_s, no retries left", svc.ExternalID, svc.RetryCount, svc.MaxRetries)
	}
	if attempts, err := st.ListTaskAttempts(ctx, "svc"); err != nil || len(attempts) != 0 {
		t.Errorf("service attempts = %+v, %v; want none", attempts, err)
	}
	if w, err := st.GetWorker(ctx, "wrk_s"); err != nil || w == nil || w.CurrentTask != "" {
		t.Errorf("worker = %+v, %v; want current_task moved off the failed service", w, err)
	}

	// A service the heartbeat lists, or one within the grace window, stays.
	st2 := testStore(t)
	mustCreateWorker(t, st2, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st2, "listed", "wrk_s", true, 0, 0)
	seedRunningOn(t, st2, "fresh", "wrk_s", true, 0, 0)
	if _, err := st2.ReconcileWorkerTasks(ctx, "wrk_s", []string{"listed"}, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"listed", "fresh"} {
		if tk := mustGetTask(t, st2, id); tk.State != model.TaskStateRunning {
			t.Errorf("%s = %s, want RUNNING", id, tk.State)
		}
	}
}
