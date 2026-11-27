package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Review fixes for "a service task is never started a second time": the
// store refuses a non-worker service task, the heartbeat reconcile follows
// a two-strike rule, GPUs of a lost service are protected on every worker
// row sharing them, and the re-run paths never reset a service.

// R1: a service task whose executor is not the worker executor is refused.
func TestCreateTask_ServiceNeedsWorkerExecutor(t *testing.T) {
	ctx := context.Background()
	for _, et := range []model.ExecutorType{model.ExecutorTypeLocal, model.ExecutorTypeContainer, model.ExecutorTypeBVBRC, ""} {
		t.Run("executor "+string(et), func(t *testing.T) {
			st := testStore(t)
			tk := svcQueued("svc", true, 0, 0, time.Minute)
			tk.ExecutorType = et
			if err := st.CreateTask(ctx, tk); !errors.Is(err, ErrServiceNotWorker) {
				t.Fatalf("CreateTask = %v, want ErrServiceNotWorker", err)
			}
			if got, err := st.GetTask(ctx, "svc"); err != nil || got != nil {
				t.Errorf("stored = (%v, %v), want no row", got, err)
			}
			// The batch path refuses it too, as a whole.
			si := &model.StepInstance{ID: "si_1", SubmissionID: "sub_1", StepID: "step", State: model.StepStateDispatched}
			if err := st.CreateTasksAndDispatchStep(ctx, []*model.Task{tk}, si); !errors.Is(err, ErrServiceNotWorker) {
				t.Errorf("CreateTasksAndDispatchStep = %v, want ErrServiceNotWorker", err)
			}
			// An ordinary task with the same executor is stored.
			plain := svcQueued("plain", false, 0, 0, time.Minute)
			plain.ExecutorType = et
			if err := st.CreateTask(ctx, plain); err != nil {
				t.Errorf("ordinary task: %v", err)
			}
		})
	}
	t.Run("worker executor", func(t *testing.T) {
		st := testStore(t)
		mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
	})
}

// R5: a heartbeat without running_tasks (nil) never touches a service, even
// one already marked missing long ago.
func TestReconcileWorkerTasks_ServiceAbsentFieldUntouched(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	seedRunningOn(t, st, "marked", "wrk_s", true, 0, 0)
	old := time.Now().UTC().Add(-time.Hour)
	setServiceMissingAt(t, st, "marked", old)

	for i := 0; i < 5; i++ {
		if requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_s", nil, 0); err != nil || len(requeued) != 0 {
			t.Fatalf("ReconcileWorkerTasks = %v, %v; want nothing", requeued, err)
		}
	}
	if tk := checkServiceRunning(t, st, "svc", "wrk_s", 1); tk.ServiceMissingAt != nil {
		t.Errorf("svc service_missing_at = %v, want untouched (nil)", tk.ServiceMissingAt)
	}
	if tk := checkServiceRunning(t, st, "marked", "wrk_s", 0); tk.ServiceMissingAt == nil || !tk.ServiceMissingAt.Equal(old) {
		t.Errorf("marked service_missing_at = %v, want untouched (%v)", tk.ServiceMissingAt, old)
	}
}

// R5: the two-strike rule. A first omission marks the service (still
// RUNNING and charged), a listing clears the mark, an omission before the
// mark is older than the grace window leaves it RUNNING, one after fails it.
func TestReconcileWorkerTasks_ServiceTwoStrikes(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	reconcile := func(running []string) {
		t.Helper()
		if requeued, err := st.ReconcileWorkerTasks(ctx, "wrk_s", running, time.Minute); err != nil || len(requeued) != 0 {
			t.Fatalf("ReconcileWorkerTasks = %v, %v; want nothing requeued", requeued, err)
		}
	}

	before := time.Now().UTC().Add(-time.Second)
	reconcile([]string{})
	first := checkServiceRunning(t, st, "svc", "wrk_s", 1)
	if first.ServiceMissingAt == nil || first.ServiceMissingAt.Before(before) {
		t.Fatalf("first omission: service_missing_at = %v, want set now", first.ServiceMissingAt)
	}

	reconcile([]string{"svc"})
	if tk := checkServiceRunning(t, st, "svc", "wrk_s", 1); tk.ServiceMissingAt != nil {
		t.Fatalf("listed again: service_missing_at = %v, want cleared", tk.ServiceMissingAt)
	}

	// Omitted twice within the grace window of the mark: still RUNNING.
	reconcile([]string{"other"})
	reconcile([]string{})
	if tk := checkServiceRunning(t, st, "svc", "wrk_s", 1); tk.ServiceMissingAt == nil {
		t.Fatal("second omission before minAge: service_missing_at was cleared, want kept")
	}

	// The mark is older than the grace window: the next omission fails it.
	setServiceMissingAt(t, st, "svc", time.Now().UTC().Add(-2*time.Minute))
	reconcile([]string{})
	if tk := mustGetTask(t, st, "svc"); tk.State != model.TaskStateFailed || tk.Stderr != ServiceMissingStderr {
		t.Fatalf("after the second strike: %s %q, want FAILED %q", tk.State, tk.Stderr, ServiceMissingStderr)
	}
}

// R5: neither the reconcile nor failMissingService of another worker id
// ever touches a service.
func TestReconcileWorkerTasks_ServiceOfAnotherWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
	mustCreateWorker(t, st, svcWorker("wrk_other", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	old := time.Now().UTC().Add(-time.Hour)
	setServiceMissingAt(t, st, "svc", old)

	for i := 0; i < 3; i++ {
		if _, err := st.ReconcileWorkerTasks(ctx, "wrk_other", []string{}, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The compare-and-set of the failing write requires the owner.
	if err := st.failMissingService(ctx, "wrk_other", "svc"); err != nil {
		t.Fatal(err)
	}
	if tk := checkServiceRunning(t, st, "svc", "wrk_s", 1); tk.ServiceMissingAt == nil || !tk.ServiceMissingAt.Equal(old) {
		t.Errorf("service_missing_at = %v, want untouched (%v)", tk.ServiceMissingAt, old)
	}
}

// R5: a listed service the server has ended (terminal) is returned for the
// worker to stop; a listed terminal ordinary task and a running service are
// not.
func TestCancelledTasksForWorker_TerminalService(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedRunningOn(t, st, "svc_running", "wrk_s", true, 0, 0)
	for _, c := range []struct {
		id      string
		service bool
		state   model.TaskState
	}{
		{"svc_failed", true, model.TaskStateFailed},
		{"svc_skipped", true, model.TaskStateSkipped},
		{"batch_failed", false, model.TaskStateFailed},
		{"batch_success", false, model.TaskStateSuccess},
	} {
		tk := svcQueued(c.id, c.service, 0, 0, time.Hour)
		tk.State, tk.ExternalID = c.state, "wrk_s"
		now := time.Now().UTC()
		tk.CompletedAt = &now
		mustCreateTask(t, st, tk)
	}
	got, err := st.CancelledTasksForWorker(ctx, []string{"svc_running", "svc_failed", "svc_skipped", "batch_failed", "batch_success"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"svc_failed": true, "svc_skipped": true}
	if len(got) != len(want) {
		t.Fatalf("cancel = %v, want the two terminal services", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("cancel lists %s", id)
		}
	}
}

// gpusOnHost makes w a GPU worker on host with the GPUs of the given UUIDs.
func gpusOnHost(host string, uuids ...string) func(w *model.Worker) {
	return func(w *model.Worker) {
		w.HostID = host
		w.GPUEnabled = true
		w.GPUs = nil
		for i, u := range uuids {
			w.GPUs = append(w.GPUs, model.GPUInfo{Index: i, UUID: u, Name: "H200", MemTotalMB: 141000})
		}
	}
}

// R6: a lost service's GPUs are protected on every worker row of its host
// whose inventory overlaps the lost worker's, not only on its own row.
func TestCheckoutTask_ServiceGPUsByInventoryOverlap(t *testing.T) {
	ctx := context.Background()
	// setup stores W1 (host-1, GPUs a and b) running a 2-GPU service, then
	// lost (offline), and a queued 1-GPU service.
	setup := func(t *testing.T) *SQLStore {
		t.Helper()
		st := testStore(t)
		mustCreateWorker(t, st, svcWorker("wrk_1", true, gpusOnHost("host-1", "GPU-a", "GPU-b")))
		seedRunningOn(t, st, "svc_lost", "wrk_1", true, 0, 2)
		if _, err := st.db.ExecContext(ctx, `UPDATE workers SET state = 'offline' WHERE id = ?`, "wrk_1"); err != nil {
			t.Fatal(err)
		}
		mustCreateTask(t, st, svcQueued("svc_new", true, 0, 1, time.Minute))
		return st
	}
	tests := []struct {
		name   string
		worker func(w *model.Worker)
		want   bool
	}{
		{"same host, same GPUs (restarted worker)", gpusOnHost("host-1", "GPU-a", "GPU-b"), false},
		{"same host, one shared GPU", gpusOnHost("host-1", "GPU-b", "GPU-c"), false},
		{"same host, disjoint GPUs", gpusOnHost("host-1", "GPU-c"), true},
		{"other host, same GPU UUIDs", gpusOnHost("host-2", "GPU-a", "GPU-b"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setup(t)
			mustCreateWorker(t, st, svcWorker("wrk_2", true, tt.worker))
			got, err := st.CheckoutTask(ctx, "wrk_2", svcGroup, model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Fatalf("checked out = %v, want %v", got, tt.want)
			}
			if got != nil && got.ID != "svc_new" {
				t.Errorf("checked out %s, want svc_new", got.ID)
			}
			checkServiceRunning(t, st, "svc_lost", "wrk_1", 2)
		})
	}

	// The overlap only limits tasks that need GPUs.
	t.Run("same GPUs, a service needing none", func(t *testing.T) {
		st := setup(t)
		mustCreateTask(t, st, svcQueued("svc_cpu", true, 0, 0, 2*time.Minute))
		mustCreateWorker(t, st, svcWorker("wrk_2", true, gpusOnHost("host-1", "GPU-a", "GPU-b")))
		got, err := st.CheckoutTask(ctx, "wrk_2", svcGroup, model.RuntimeNone)
		if err != nil || got == nil || got.ID != "svc_cpu" {
			t.Fatalf("checkout = (%v, %v), want svc_cpu", got, err)
		}
	})

	// The overlap counts services only: an ordinary task of the lost worker
	// is about to be requeued and holds nothing.
	t.Run("same GPUs, the lost row holds an ordinary task", func(t *testing.T) {
		st := testStore(t)
		mustCreateWorker(t, st, svcWorker("wrk_1", false, gpusOnHost("host-1", "GPU-a", "GPU-b")))
		seedRunningOn(t, st, "batch", "wrk_1", false, 0, 2)
		if _, err := st.db.ExecContext(ctx, `UPDATE workers SET state = 'offline' WHERE id = ?`, "wrk_1"); err != nil {
			t.Fatal(err)
		}
		mustCreateTask(t, st, svcQueued("svc_new", true, 0, 1, time.Minute))
		mustCreateWorker(t, st, svcWorker("wrk_2", true, gpusOnHost("host-1", "GPU-a", "GPU-b")))
		if got, err := st.CheckoutTask(ctx, "wrk_2", svcGroup, model.RuntimeNone); err != nil || got == nil {
			t.Fatalf("checkout = (%v, %v), want svc_new", got, err)
		}
	})
}

// R6: a service-mode GPU worker without a GPU inventory (unknown capacity)
// is offered no task that needs a GPU; it still takes a service needing
// none.
func TestCheckoutTask_ServiceWorkerUnknownGPUCapacity(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) { w.GPUEnabled = true }))
	mustCreateTask(t, st, svcQueued("svc_gpu", true, 0, 1, 2*time.Minute))
	if got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone); err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want nothing: the worker's GPU capacity is unknown", got, err)
	}
	mustCreateTask(t, st, svcQueued("svc_cpu", true, 0, 0, time.Minute))
	if got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone); err != nil || got == nil || got.ID != "svc_cpu" {
		t.Fatalf("checkout = (%v, %v), want svc_cpu", got, err)
	}
}

// seedFailedTask stores a FAILED worker task of submission sub_1 (a service
// task when service is set), hand-made with MaxRetries 3 and output worth
// archiving.
func seedFailedTask(t *testing.T, st *SQLStore, id string, service bool) {
	t.Helper()
	tk := svcQueued(id, service, 0, 0, time.Hour)
	tk.State, tk.ExternalID, tk.MaxRetries = model.TaskStateFailed, "wrk_s", 3
	started, done := time.Now().UTC().Add(-time.Hour), time.Now().UTC()
	tk.StartedAt, tk.CompletedAt = &started, &done
	tk.Stdout, tk.Stderr = "out of "+id, "boom"
	exit := 1
	tk.ExitCode = &exit
	mustCreateTask(t, st, tk)
}

// checkFailedServiceKept fails unless task id is still the FAILED service
// seedFailedTask stored: not reset, not blanked, nothing archived.
func checkFailedServiceKept(t *testing.T, st *SQLStore, id string) {
	t.Helper()
	tk := mustGetTask(t, st, id)
	if tk.State != model.TaskStateFailed || tk.Stderr != "boom" || tk.Stdout != "out of "+id ||
		tk.ExitCode == nil || tk.StartedAt == nil || tk.CompletedAt == nil {
		t.Errorf("service %s = %s stdout %q stderr %q exit %v; want the FAILED row unchanged", id, tk.State, tk.Stdout, tk.Stderr, tk.ExitCode)
	}
	if attempts, err := st.ListTaskAttempts(context.Background(), id); err != nil || len(attempts) != 0 {
		t.Errorf("service %s attempts = %+v, %v; want none archived", id, attempts, err)
	}
}

// R7: a submission retry (ResetFailedTasks) never resets nor archives a
// service task; the ordinary task of the same submission is reset as before.
func TestResetFailedTasks_SkipsService(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedFailedTask(t, st, "svc", true)
	seedFailedTask(t, st, "plain", false)

	n, err := st.ResetFailedTasks(ctx, "sub_1")
	if err != nil || n != 1 {
		t.Fatalf("ResetFailedTasks = %d, %v; want 1", n, err)
	}
	checkFailedServiceKept(t, st, "svc")
	if plain := mustGetTask(t, st, "plain"); plain.State != model.TaskStatePending {
		t.Errorf("ordinary task = %s, want PENDING", plain.State)
	}
	if attempts, err := st.ListTaskAttempts(ctx, "plain"); err != nil || len(attempts) != 1 {
		t.Errorf("ordinary task attempts = %+v, %v; want one", attempts, err)
	}
}

// R7: ArchiveAndResetTask refuses a service task (error, nothing archived or
// written); a snapshot that lost its service markers still cannot reset a
// row stored as a service.
func TestArchiveAndResetTask_RefusesService(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	seedFailedTask(t, st, "svc", true)
	tk := mustGetTask(t, st, "svc")
	tk.State, tk.Stdout, tk.Stderr, tk.ExitCode, tk.StartedAt, tk.CompletedAt = model.TaskStateQueued, "", "", nil, nil, nil

	if applied, err := st.ArchiveAndResetTask(ctx, tk, model.TaskStateFailed, model.AttemptReasonReset); applied || !errors.Is(err, ErrServiceNotReset) {
		t.Fatalf("ArchiveAndResetTask = (%v, %v), want ErrServiceNotReset", applied, err)
	}
	checkFailedServiceKept(t, st, "svc")

	tk.IsService, tk.RuntimeHints.Service = false, nil
	if applied, err := st.ArchiveAndResetTask(ctx, tk, model.TaskStateFailed, model.AttemptReasonReset); applied || err != nil {
		t.Fatalf("ArchiveAndResetTask (markers stripped) = (%v, %v), want (false, nil)", applied, err)
	}
	checkFailedServiceKept(t, st, "svc")
}

// Second review, item 1: the hold rule is never applied to a service-mode
// worker. Its running service never finishes, so holding for the older
// 4-GPU service would keep the newer 2-GPU one (which fits the two free
// GPUs) waiting forever.
func TestCheckoutTask_ServiceWorkerNeverHolds(t *testing.T) {
	st := testStore(t)
	st.SetCheckoutHoldAfter(10 * time.Minute)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, gpusOnHost("host-1", "GPU-a", "GPU-b", "GPU-c", "GPU-d")))
	seedRunningOn(t, st, "svc_run", "wrk_s", true, 0, 2)
	mustCreateTask(t, st, svcQueued("svc_big", true, 0, 4, 30*time.Minute))
	mustCreateTask(t, st, svcQueued("svc_small", true, 0, 2, 5*time.Minute))

	if got := checkoutGroupID(t, st, "wrk_s", svcGroup); got != "svc_small" {
		t.Fatalf("checked out %q, want svc_small (no hold on a service-mode worker)", got)
	}
	if tk := mustGetTask(t, st, "svc_big"); tk.State != model.TaskStateQueued || tk.ExternalID != "" {
		t.Errorf("svc_big = %s on %q, want QUEUED, unowned", tk.State, tk.ExternalID)
	}
	checkServiceRunning(t, st, "svc_run", "wrk_s", 2)
}

// Second review, item 7b: Migrate backfills is_service = 1 for a worker
// task whose hints carry a non-null service member, and only for those; it
// is safe on empty or malformed hints and idempotent.
func TestMigrate_BackfillsIsService(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
	mustCreateTask(t, st, svcQueued("svc_local", true, 0, 0, time.Minute))
	for _, id := range []string{"plain", "null_service", "empty_hints", "bad_hints"} {
		mustCreateTask(t, st, svcQueued(id, false, 0, 0, time.Minute))
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE tasks SET is_service = 0 WHERE id IN (?, ?)`, []any{"svc", "svc_local"}},
		{`UPDATE tasks SET executor_type = 'local' WHERE id = ?`, []any{"svc_local"}},
		{`UPDATE tasks SET runtime_hints = ? WHERE id = ?`, []any{`{"worker_group":"llm","service":null}`, "null_service"}},
		{`UPDATE tasks SET runtime_hints = '' WHERE id = ?`, []any{"empty_hints"}},
		{`UPDATE tasks SET runtime_hints = ? WHERE id = ?`, []any{`{"service":`, "bad_hints"}},
	} {
		if _, err := st.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	isService := func(id string) int {
		t.Helper()
		var v int
		if err := st.db.QueryRowContext(ctx, `SELECT is_service FROM tasks WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatalf("read is_service of %s: %v", id, err)
		}
		return v
	}
	if isService("svc") != 0 {
		t.Fatal("setup: svc is_service not cleared")
	}

	for run := 1; run <= 2; run++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate (run %d): %v", run, err)
		}
		for id, want := range map[string]int{
			"svc": 1, "svc_local": 0, "plain": 0, "null_service": 0, "empty_hints": 0, "bad_hints": 0,
		} {
			if got := isService(id); got != want {
				t.Errorf("run %d: %s is_service = %d, want %d", run, id, got, want)
			}
		}
	}
}

// R7: checkout treats a task carrying the service spec in its hints as a
// service even when its row says otherwise: an ordinary worker never gets it.
func TestCheckoutTask_ServiceHintsWithoutFlag(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_a", false, nil))
	mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET is_service = 0 WHERE id = ?`, "svc"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.CheckoutTask(ctx, "wrk_a", svcGroup, model.RuntimeNone); err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want nothing for an ordinary worker", got, err)
	}
	if tk := mustGetTask(t, st, "svc"); tk.State != model.TaskStateQueued || tk.ExternalID != "" {
		t.Errorf("task = %s on %q, want QUEUED, unowned", tk.State, tk.ExternalID)
	}
}
