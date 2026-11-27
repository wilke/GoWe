package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// A service-mode worker reports a status for each running service in its
// heartbeat; UpdateServiceStatuses stores it on RUNNING services of that
// worker only, and every read shows a state that cannot look live for a
// task that is not.

// svcReport returns a report for task id in state.
func svcReport(id, state string) model.ServiceReport {
	return model.ServiceReport{TaskID: id, ServiceStatus: model.ServiceStatus{
		State: state, Host: "gpu01", Port: 8000, Scheme: "http", GPUs: []model.ServiceGPU{{Index: 1, UUID: "GPU-1"}},
	}}
}

// rawServiceStatus reads tasks.service_status of id as stored (nil = NULL).
func rawServiceStatus(t *testing.T, st *SQLStore, id string) *model.ServiceStatus {
	t.Helper()
	var raw *string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT service_status FROM tasks WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read service_status of %s: %v", id, err)
	}
	if raw == nil {
		return nil
	}
	var s model.ServiceStatus
	if err := json.Unmarshal([]byte(*raw), &s); err != nil {
		t.Fatalf("decode service_status of %s: %v (%s)", id, err, *raw)
	}
	return &s
}

func mustUpdateStatuses(t *testing.T, st *SQLStore, workerID string, now time.Time, reports ...model.ServiceReport) []string {
	t.Helper()
	ignored, err := st.UpdateServiceStatuses(context.Background(), workerID, reports, now)
	if err != nil {
		t.Fatalf("UpdateServiceStatuses: %v", err)
	}
	return ignored
}

func TestServiceStatusColumn(t *testing.T) {
	st := testStore(t)
	if !hasColumn(t, st, "tasks", "service_status") {
		t.Fatal("column tasks.service_status missing")
	}
	// The insert writes it (a task created with a status reads it back).
	tk := svcQueued("svc", true, 0, 0, time.Minute)
	tk.State, tk.ExternalID = model.TaskStateRunning, "wrk_s"
	updated := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	tk.ServiceStatus = &model.ServiceStatus{State: model.ServiceStateStarting, Host: "h", UpdatedAt: updated}
	mustCreateTask(t, st, tk)
	got := mustGetTask(t, st, "svc")
	if got.ServiceStatus == nil || got.ServiceStatus.State != model.ServiceStateStarting || got.ServiceStatus.Host != "h" ||
		!got.ServiceStatus.UpdatedAt.Equal(updated) {
		t.Errorf("status = %+v, want the inserted one", got.ServiceStatus)
	}
	// A task without a status has none (NULL).
	mustCreateTask(t, st, svcQueued("plain", false, 0, 0, time.Minute))
	if got := mustGetTask(t, st, "plain"); got.ServiceStatus != nil {
		t.Errorf("status = %+v, want nil", got.ServiceStatus)
	}
	if raw := rawServiceStatus(t, st, "plain"); raw != nil {
		t.Errorf("stored status = %+v, want NULL", raw)
	}
}

// Only a RUNNING worker service of the reporting worker is written; every
// other report is returned in ignored and changes nothing.
func TestUpdateServiceStatuses_MatchRules(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	mustCreateWorker(t, st, svcWorker("wrk_o", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)
	seedRunningOn(t, st, "other", "wrk_o", true, 0, 0)
	seedRunningOn(t, st, "batch", "wrk_s", false, 0, 0)
	mustCreateTask(t, st, svcQueued("queued", true, 0, 0, time.Minute))
	done := svcQueued("done", true, 0, 0, time.Minute)
	done.State, done.ExternalID = model.TaskStateFailed, "wrk_s"
	mustCreateTask(t, st, done)

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ignored := mustUpdateStatuses(t, st, "wrk_s", now,
		svcReport("svc", model.ServiceStateStarting),
		svcReport("other", model.ServiceStateStarting),
		svcReport("batch", model.ServiceStateStarting),
		svcReport("queued", model.ServiceStateStarting),
		svcReport("done", model.ServiceStateStarting),
		svcReport("nope", model.ServiceStateStarting),
	)
	if want := []string{"other", "batch", "queued", "done", "nope"}; !reflect.DeepEqual(ignored, want) {
		t.Errorf("ignored = %v, want %v", ignored, want)
	}
	got := rawServiceStatus(t, st, "svc")
	if got == nil || got.State != model.ServiceStateStarting || got.Host != "gpu01" || got.Port != 8000 ||
		len(got.GPUs) != 1 || got.GPUs[0].Index != 1 || !got.UpdatedAt.Equal(now) {
		t.Errorf("svc status = %+v, want the report with updated_at %v", got, now)
	}
	for _, id := range []string{"other", "batch", "queued", "done"} {
		if raw := rawServiceStatus(t, st, id); raw != nil {
			t.Errorf("%s status = %+v, want NULL (never written)", id, raw)
		}
	}

	// The other worker writes its own task, not ours.
	ignored = mustUpdateStatuses(t, st, "wrk_o", now, svcReport("svc", model.ServiceStateUnhealthy), svcReport("other", model.ServiceStateReady))
	if want := []string{"svc"}; !reflect.DeepEqual(ignored, want) {
		t.Errorf("ignored = %v, want %v", ignored, want)
	}
	if got := rawServiceStatus(t, st, "svc"); got == nil || got.State != model.ServiceStateStarting {
		t.Errorf("svc status after another worker's report = %+v, want unchanged starting", got)
	}

	// Nothing to do: no error, nothing ignored.
	if ignored := mustUpdateStatuses(t, st, "wrk_s", now); ignored != nil {
		t.Errorf("empty batch: ignored = %v, want nil", ignored)
	}
}

// updated_at is the server's time, whatever the worker sent.
func TestUpdateServiceStatuses_UpdatedAtFromServer(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)

	r := svcReport("svc", model.ServiceStateStarting)
	r.UpdatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 27, 12, 0, 0, 123456789, time.FixedZone("x", 7200))
	mustUpdateStatuses(t, st, "wrk_s", now, r)
	if got := rawServiceStatus(t, st, "svc"); got == nil || !got.UpdatedAt.Equal(now) {
		t.Fatalf("updated_at = %+v, want %v", got, now)
	}
	if got := mustGetTask(t, st, "svc"); got.ServiceStatus == nil || !got.ServiceStatus.UpdatedAt.Equal(now) {
		t.Errorf("read updated_at = %+v, want %v", got.ServiceStatus, now)
	}
}

// ready_at is set once and never cleared or changed afterwards.
func TestUpdateServiceStatuses_ReadyAtSticky(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)
	seedRunningOn(t, st, "svc2", "wrk_s", true, 0, 0)
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	readyAt := func(id string) *time.Time {
		t.Helper()
		got := rawServiceStatus(t, st, id)
		if got == nil {
			t.Fatalf("%s has no status", id)
		}
		return got.ReadyAt
	}

	// starting without ready_at: none.
	mustUpdateStatuses(t, st, "wrk_s", t0, svcReport("svc", model.ServiceStateStarting))
	if got := readyAt("svc"); got != nil {
		t.Fatalf("ready_at after starting = %v, want none", got)
	}
	// ready without ready_at: the server's time.
	t1 := t0.Add(time.Minute)
	mustUpdateStatuses(t, st, "wrk_s", t1, svcReport("svc", model.ServiceStateReady))
	if got := readyAt("svc"); got == nil || !got.Equal(t1) {
		t.Fatalf("ready_at after the first ready = %v, want %v", got, t1)
	}
	// A later report with another ready_at does not change it.
	r := svcReport("svc", model.ServiceStateReady)
	other := t0.Add(-time.Hour)
	r.ReadyAt = &other
	mustUpdateStatuses(t, st, "wrk_s", t1.Add(time.Minute), r)
	if got := readyAt("svc"); got == nil || !got.Equal(t1) {
		t.Fatalf("ready_at after a report with another one = %v, want %v kept", got, t1)
	}
	// An unhealthy report without one does not clear it.
	mustUpdateStatuses(t, st, "wrk_s", t1.Add(2*time.Minute), svcReport("svc", model.ServiceStateUnhealthy))
	if got := readyAt("svc"); got == nil || !got.Equal(t1) {
		t.Fatalf("ready_at after unhealthy = %v, want %v kept", got, t1)
	}

	// Without a stored one, the report's own ready_at is taken (in UTC) when
	// it is not earlier than the task's start.
	if _, err := st.db.ExecContext(context.Background(), `UPDATE tasks SET started_at = ? WHERE id = ?`,
		formatTime(t0.Add(-time.Hour)), "svc2"); err != nil {
		t.Fatal(err)
	}
	r2 := svcReport("svc2", model.ServiceStateReady)
	reported := time.Date(2026, 11, 27, 13, 30, 0, 0, time.FixedZone("x", 3600))
	r2.ReadyAt = &reported
	mustUpdateStatuses(t, st, "wrk_s", t1, r2)
	if got := readyAt("svc2"); got == nil || !got.Equal(reported) {
		t.Fatalf("ready_at = %v, want the reported %v", got, reported)
	}
}

// A reported ready_at is taken only with state ready; one earlier than the
// task's started_at is replaced by now; once set, no later report changes it.
func TestUpdateServiceStatuses_ReadyAtOnlyWhenReady(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0) // started an hour ago
	epoch := time.Unix(0, 0).UTC()
	readyAt := func() *time.Time {
		t.Helper()
		got := rawServiceStatus(t, st, "svc")
		if got == nil {
			t.Fatal("svc has no status")
		}
		return got.ReadyAt
	}
	report := func(state string, at *time.Time) model.ServiceReport {
		r := svcReport("svc", state)
		r.ReadyAt = at
		return r
	}
	now := time.Now().UTC()

	// Not ready: a reported ready_at is dropped.
	for _, state := range []string{model.ServiceStateStarting, model.ServiceStateUnhealthy, model.ServiceStateStopping} {
		mustUpdateStatuses(t, st, "wrk_s", now, report(state, &epoch))
		if got := readyAt(); got != nil {
			t.Fatalf("ready_at after %s with a 1970 value = %v, want none", state, got)
		}
	}
	recent := now.Add(-time.Minute) // after started_at: would be taken in state ready
	mustUpdateStatuses(t, st, "wrk_s", now, report(model.ServiceStateStarting, &recent))
	if got := readyAt(); got != nil {
		t.Fatalf("ready_at after starting with a valid value = %v, want none", got)
	}

	// Ready with a value earlier than started_at: now.
	t1 := now.Add(time.Minute)
	mustUpdateStatuses(t, st, "wrk_s", t1, report(model.ServiceStateReady, &epoch))
	if got := readyAt(); got == nil || !got.Equal(t1) {
		t.Fatalf("ready_at after ready with a 1970 value = %v, want now %v", got, t1)
	}

	// Later reports cannot change it.
	later := t1.Add(30 * time.Second)
	for i, r := range []model.ServiceReport{
		report(model.ServiceStateReady, &later),
		report(model.ServiceStateReady, &epoch),
		report(model.ServiceStateReady, nil),
		report(model.ServiceStateStarting, &later),
	} {
		mustUpdateStatuses(t, st, "wrk_s", t1.Add(time.Duration(i+1)*time.Minute), r)
		if got := readyAt(); got == nil || !got.Equal(t1) {
			t.Fatalf("ready_at after later report %d (%s) = %v, want %v kept", i, r.State, got, t1)
		}
	}
}

// The shown state never looks live for a task that is not: every reader
// (GetTask, list, submission) rewrites it, the stored document keeps what the
// worker reported, and the status is kept when the task ends.
func TestServiceStatus_ShownState(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)
	mustUpdateStatuses(t, st, "wrk_s", time.Now().UTC(), svcReport("svc", model.ServiceStateReady))

	shown := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		if tk := mustGetTask(t, st, "svc"); tk.ServiceStatus != nil {
			out["get"] = tk.ServiceStatus.State
		}
		tasks, err := st.ListTasksBySubmission(ctx, "sub_1")
		if err != nil || len(tasks) != 1 {
			t.Fatalf("ListTasksBySubmission = %d, %v", len(tasks), err)
		}
		if tasks[0].ServiceStatus != nil {
			out["list"] = tasks[0].ServiceStatus.State
		}
		sub, err := st.GetSubmission(ctx, "sub_1")
		if err != nil || sub == nil || len(sub.Tasks) != 1 {
			t.Fatalf("GetSubmission = %+v, %v", sub, err)
		}
		if sub.Tasks[0].ServiceStatus != nil {
			out["submission"] = sub.Tasks[0].ServiceStatus.State
		}
		return out
	}
	check := func(what, want string) {
		t.Helper()
		got := shown()
		for _, reader := range []string{"get", "list", "submission"} {
			if got[reader] != want {
				t.Errorf("%s: %s shows %q, want %q", what, reader, got[reader], want)
			}
		}
		if raw := rawServiceStatus(t, st, "svc"); raw == nil || raw.State != model.ServiceStateReady {
			t.Errorf("%s: stored status = %+v, want the reported ready", what, raw)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	check("running", model.ServiceStateReady)

	exec(`UPDATE tasks SET service_lost_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), "svc")
	check("worker lost", model.ServiceStateLost)

	exec(`UPDATE tasks SET service_lost_at = NULL, service_stop_requested_at = ? WHERE id = ?`, formatTime(time.Now().UTC()), "svc")
	check("stop requested", model.ServiceStateStopping)

	// The task ends through the full-row write (execTaskUpdate): the stored
	// document is kept as reported (not overwritten with the shown state),
	// and shown stopped.
	tk := mustGetTask(t, st, "svc")
	now := time.Now().UTC()
	tk.State, tk.CompletedAt = model.TaskStateSkipped, &now
	if applied, err := st.TerminalizeTask(ctx, tk); err != nil || !applied {
		t.Fatalf("TerminalizeTask = %v, %v", applied, err)
	}
	check("ended", model.ServiceStateStopped)

	// An ended task takes no new status.
	if ignored := mustUpdateStatuses(t, st, "wrk_s", time.Now().UTC(), svcReport("svc", model.ServiceStateStarting)); len(ignored) != 1 {
		t.Errorf("ignored = %v, want [svc]", ignored)
	}
	check("report after the end", model.ServiceStateStopped)
}

// A stored document that cannot be decoded never hides the task: it is read
// without a status.
func TestServiceStatus_UndecodableDocument(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET service_status = ? WHERE id = ?`, "{not json", "svc"); err != nil {
		t.Fatal(err)
	}
	if tk := mustGetTask(t, st, "svc"); tk.ServiceStatus != nil {
		t.Errorf("status = %+v, want nil", tk.ServiceStatus)
	}
	if tasks, err := st.ListTasksBySubmission(ctx, "sub_1"); err != nil || len(tasks) != 1 {
		t.Errorf("list = %d tasks, %v; want the task", len(tasks), err)
	}
	// A report replaces it.
	mustUpdateStatuses(t, st, "wrk_s", time.Now().UTC(), svcReport("svc", model.ServiceStateReady))
	if got := rawServiceStatus(t, st, "svc"); got == nil || got.State != model.ServiceStateReady || got.ReadyAt == nil {
		t.Errorf("status = %+v, want ready with ready_at", got)
	}
}

// A stored null or {} document is no status (a status always has a state),
// on every reader; the task is read without an error.
func TestServiceStatus_NullAndEmptyDocument(t *testing.T) {
	for _, doc := range []string{"null", "{}"} {
		t.Run(doc, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
			seedRunningOn(t, st, "svc", "wrk_s", true, 0, 0)
			if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET service_status = ? WHERE id = ?`, doc, "svc"); err != nil {
				t.Fatal(err)
			}
			if tk := mustGetTask(t, st, "svc"); tk.ServiceStatus != nil {
				t.Errorf("GetTask status = %+v, want nil", tk.ServiceStatus)
			}
			tasks, err := st.ListTasksBySubmission(ctx, "sub_1")
			if err != nil || len(tasks) != 1 {
				t.Fatalf("list = %d tasks, %v; want the task", len(tasks), err)
			}
			if tasks[0].ServiceStatus != nil {
				t.Errorf("list status = %+v, want nil", tasks[0].ServiceStatus)
			}
			// A report replaces it, with ready_at set by the first ready.
			mustUpdateStatuses(t, st, "wrk_s", time.Now().UTC(), svcReport("svc", model.ServiceStateReady))
			if got := rawServiceStatus(t, st, "svc"); got == nil || got.State != model.ServiceStateReady || got.ReadyAt == nil {
				t.Errorf("status after a report = %+v, want ready with ready_at", got)
			}
		})
	}
}

// A stored document that cannot be decoded, null or {} never fails the
// checkout path (CheckoutTask -> scanCheckoutRow): the task is handed out
// without a status and without an error.
func TestCheckoutTask_UnusableServiceStatusDocument(t *testing.T) {
	for _, doc := range []string{"{not json", "null", "{}"} {
		t.Run(doc, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
			mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
			if _, err := st.db.ExecContext(context.Background(),
				`UPDATE tasks SET service_status = ? WHERE id = ?`, doc, "svc"); err != nil {
				t.Fatal(err)
			}
			got, err := st.CheckoutTask(context.Background(), "wrk_s", svcGroup, model.RuntimeNone)
			if err != nil || got == nil || got.ID != "svc" {
				t.Fatalf("checkout = (%v, %v), want svc and no error", got, err)
			}
			if got.ServiceStatus != nil {
				t.Errorf("checked-out task status = %+v, want nil", got.ServiceStatus)
			}
		})
	}
}

// The checkout hands out a QUEUED service, which never has a status: none is
// in the answer.
func TestCheckoutTask_ServiceHasNoStatus(t *testing.T) {
	st := testStore(t)
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
	got, err := st.CheckoutTask(context.Background(), "wrk_s", svcGroup, model.RuntimeNone)
	if err != nil || got == nil || got.ID != "svc" {
		t.Fatalf("checkout = (%v, %v), want svc", got, err)
	}
	if got.ServiceStatus != nil {
		t.Errorf("checked-out task status = %+v, want none", got.ServiceStatus)
	}
}
