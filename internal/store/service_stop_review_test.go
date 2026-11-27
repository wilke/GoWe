package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Review fixes for "stopping a service", on the store side: a service task
// is stored and handed out only while its submission exists and is not
// terminal, the heartbeat's cancel list covers a worker's stopping or
// orphaned services by worker id, and force release also covers a stop
// request older than model.ServiceStopReleaseAfter and a missing submission.

// setSubmissionState returns a step that writes the state of sub_1 directly.
func setSubmissionState(state model.SubmissionState) func(t *testing.T, st *SQLStore) {
	return func(t *testing.T, st *SQLStore) {
		t.Helper()
		if _, err := st.db.ExecContext(context.Background(),
			`UPDATE submissions SET state = ? WHERE id = ?`, string(state), "sub_1"); err != nil {
			t.Fatal(err)
		}
	}
}

// deleteSubmissionRow removes the row of sub_1 only (its tasks stay, as
// after a delete that committed while the scheduler was creating a task).
func deleteSubmissionRow(t *testing.T, st *SQLStore) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(), `DELETE FROM submissions WHERE id = ?`, "sub_1"); err != nil {
		t.Fatal(err)
	}
}

// setStopRequested writes service_stop_requested_at of task id directly.
func setStopRequested(t *testing.T, st *SQLStore, id string, at time.Time) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE tasks SET service_stop_requested_at = ? WHERE id = ?`, formatTime(at), id); err != nil {
		t.Fatal(err)
	}
}

// goneSubmissions are the ways the submission of a service task can be gone.
var goneSubmissions = []struct {
	name string
	end  func(t *testing.T, st *SQLStore)
}{
	{"submission row missing", deleteSubmissionRow},
	{"submission COMPLETED", setSubmissionState(model.SubmissionStateCompleted)},
	{"submission FAILED", setSubmissionState(model.SubmissionStateFailed)},
	{"submission CANCELLED", setSubmissionState(model.SubmissionStateCancelled)},
}

// F1a: a QUEUED service task whose submission row is missing or terminal is
// not offered to a service-mode worker; a batch task in the same situation
// is still offered to an ordinary worker, as before. A live submission's
// service (RUNNING or PENDING) is offered.
func TestCheckoutTask_ServiceSubmissionGone(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		end     func(t *testing.T, st *SQLStore)
		offered bool
	}{
		{"submission RUNNING", func(*testing.T, *SQLStore) {}, true},
		{"submission PENDING", setSubmissionState(model.SubmissionStatePending), true},
	}
	for _, g := range goneSubmissions {
		tests = append(tests, struct {
			name    string
			end     func(t *testing.T, st *SQLStore)
			offered bool
		}{g.name, g.end, false})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
			mustCreateWorker(t, st, svcWorker("wrk_b", false, nil))
			mustCreateTask(t, st, svcQueued("svc", true, 0, 0, time.Minute))
			mustCreateTask(t, st, svcQueued("batch", false, 0, 0, time.Minute))
			tt.end(t, st)

			got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.offered {
				t.Fatalf("service checked out = %v, want %v", got != nil, tt.offered)
			}
			if !tt.offered {
				if tk := mustGetTask(t, st, "svc"); tk.State != model.TaskStateQueued || tk.ExternalID != "" {
					t.Errorf("service = %s on %q, want QUEUED, unowned", tk.State, tk.ExternalID)
				}
			}
			if got, err := st.CheckoutTask(ctx, "wrk_b", svcGroup, model.RuntimeNone); err != nil || got == nil || got.ID != "batch" {
				t.Fatalf("batch checkout = (%v, %v), want batch (as before)", got, err)
			}
		})
	}
}

// F1b: a service task is not stored, by either insert path, for a
// submission whose row is missing or terminal (ErrServiceSubmissionGone, no
// row); a batch task still is, and a service of a live submission is.
func TestCreateTask_ServiceSubmissionGone(t *testing.T) {
	ctx := context.Background()
	for _, g := range goneSubmissions {
		t.Run(g.name, func(t *testing.T) {
			st := testStore(t)
			ensureLiveSubmission(t, st, "sub_1")
			g.end(t, st)

			if err := st.CreateTask(ctx, svcQueued("svc", true, 0, 0, time.Minute)); !errors.Is(err, ErrServiceSubmissionGone) {
				t.Fatalf("CreateTask = %v, want ErrServiceSubmissionGone", err)
			}
			si := &model.StepInstance{ID: "si_1", SubmissionID: "sub_1", StepID: "step", State: model.StepStateDispatched}
			if err := st.CreateTasksAndDispatchStep(ctx, []*model.Task{svcQueued("svc_batch_path", true, 0, 0, time.Minute)}, si); !errors.Is(err, ErrServiceSubmissionGone) {
				t.Fatalf("CreateTasksAndDispatchStep = %v, want ErrServiceSubmissionGone", err)
			}
			for _, id := range []string{"svc", "svc_batch_path"} {
				if got, err := st.GetTask(ctx, id); err != nil || got != nil {
					t.Errorf("%s stored = (%v, %v), want no row", id, got, err)
				}
			}
			if err := st.CreateTask(ctx, svcQueued("batch", false, 0, 0, time.Minute)); err != nil {
				t.Errorf("batch task: %v, want stored as before", err)
			}
		})
	}
	t.Run("submission live", func(t *testing.T) {
		st := testStore(t)
		ensureLiveSubmission(t, st, "sub_1")
		if err := st.CreateTask(ctx, svcQueued("svc", true, 0, 0, time.Minute)); err != nil {
			t.Fatalf("CreateTask = %v", err)
		}
	})
}

// F1c: a RUNNING service whose submission row is missing is in the cancel
// list of its worker, listed or not; listed by any worker it is returned
// too. A batch task in the same situation is not (list-based rule only).
func TestCancelledTasksForWorker_SubmissionMissing(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc_orphan", "wrk_s", true, 0, 0)
	seedRunningOn(t, st, "batch_orphan", "wrk_s", false, 0, 0)
	deleteSubmissionRow(t, st)

	for _, c := range []struct {
		name     string
		workerID string
		listed   []string
		want     []string
	}{
		{"own worker, listed", "wrk_s", []string{"svc_orphan", "batch_orphan"}, []string{"svc_orphan"}},
		{"own worker, running_tasks absent", "wrk_s", nil, []string{"svc_orphan"}},
		{"listed by another worker", "wrk_other", []string{"svc_orphan"}, []string{"svc_orphan"}},
		{"another worker, not listed", "wrk_other", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := st.CancelledTasksForWorker(ctx, c.workerID, c.listed)
			if err != nil {
				t.Fatal(err)
			}
			checkSameIDs(t, got, c.want)
		})
	}
}

// checkSameIDs fails unless got and want hold the same ids, in any order.
func checkSameIDs(t *testing.T, got, want []string) {
	t.Helper()
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

// F2a: by worker id, whatever the heartbeat listed (nothing, an empty list,
// other tasks), the cancel list holds every RUNNING service of the worker
// with a stop request or whose submission is CANCELLED; not a running
// service of a live submission without one, nor another worker's. A batch
// task of a CANCELLED submission is returned only when listed, as before.
func TestCancelledTasksForWorker_ByWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	mustCreateWorker(t, st, svcWorker("wrk_o", true, nil))
	seedRunningOn(t, st, "svc_stop", "wrk_s", true, 0, 0)
	setStopRequested(t, st, "svc_stop", time.Now().UTC())
	seedRunningOn(t, st, "svc_live", "wrk_s", true, 0, 0)
	seedRunningOn(t, st, "svc_other", "wrk_o", true, 0, 0)
	setStopRequested(t, st, "svc_other", time.Now().UTC())
	for _, c := range []struct {
		id      string
		service bool
	}{{"svc_cancelled", true}, {"batch_cancelled", false}} {
		tk := inSubmission(svcQueued(c.id, c.service, 0, 0, time.Hour), "sub_c")
		tk.State, tk.ExternalID = model.TaskStateRunning, "wrk_s"
		ensureLiveSubmission(t, st, "sub_c")
		mustCreateTask(t, st, tk)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE submissions SET state = 'CANCELLED' WHERE id = ?`, "sub_c"); err != nil {
		t.Fatal(err)
	}

	byWorker := []string{"svc_stop", "svc_cancelled"}
	for _, c := range []struct {
		name   string
		listed []string
		want   []string
	}{
		{"running_tasks absent", nil, byWorker},
		{"empty list", []string{}, byWorker},
		{"other tasks listed", []string{"svc_live"}, byWorker},
		{"batch task listed", []string{"batch_cancelled"}, append(slices.Clone(byWorker), "batch_cancelled")},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := st.CancelledTasksForWorker(ctx, "wrk_s", c.listed)
			if err != nil {
				t.Fatal(err)
			}
			checkSameIDs(t, got, c.want)
		})
	}
}

// Round 2, item 2: by worker id, a RUNNING service without a stop request
// whose submission has ended in any state (FAILED and COMPLETED as well as
// CANCELLED) is in the cancel list, listed or not; under a live submission
// (RUNNING, PENDING) it is not. A listed batch task under a FAILED or
// COMPLETED submission is still not returned (the list-based rule counts
// CANCELLED only).
func TestCancelledTasksForWorker_ServiceUnderEndedSubmission(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		state     model.SubmissionState
		svcWanted bool
		batchToo  bool
	}{
		{model.SubmissionStateFailed, true, false},
		{model.SubmissionStateCompleted, true, false},
		{model.SubmissionStateCancelled, true, true},
		{model.SubmissionStateRunning, false, false},
		{model.SubmissionStatePending, false, false},
	} {
		t.Run("submission "+string(tt.state), func(t *testing.T) {
			st := testStore(t)
			mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
			seedRunningOn(t, st, "svc_run", "wrk_s", true, 0, 0)
			seedRunningOn(t, st, "batch_run", "wrk_s", false, 0, 0)
			setSubmissionState(tt.state)(t, st)
			if tk := mustGetTask(t, st, "svc_run"); tk.ServiceStopRequestedAt != nil {
				t.Fatalf("svc_run has a stop request %v, want none", tk.ServiceStopRequestedAt)
			}

			var byWorker []string
			if tt.svcWanted {
				byWorker = []string{"svc_run"}
			}
			listedWant := slices.Clone(byWorker)
			if tt.batchToo {
				listedWant = append(listedWant, "batch_run")
			}
			for _, c := range []struct {
				name   string
				listed []string
				want   []string
			}{
				{"running_tasks absent", nil, byWorker},
				{"both listed", []string{"svc_run", "batch_run"}, listedWant},
			} {
				t.Run(c.name, func(t *testing.T) {
					got, err := st.CancelledTasksForWorker(ctx, "wrk_s", c.listed)
					if err != nil {
						t.Fatal(err)
					}
					checkSameIDs(t, got, c.want)
				})
			}
		})
	}
}

// F2b: a service whose worker is online and not lost is force-released once
// its stop request is older than model.ServiceStopReleaseAfter (SKIPPED, the
// stderr says the worker will be told to stop it); a younger stop request is
// refused with the reason naming the wait.
func TestForceReleaseService_StopRequestAge(t *testing.T) {
	if want := fmt.Sprintf("wait %d minutes", int(model.ServiceStopReleaseAfter/time.Minute)); !strings.Contains(ReleaseWorkerOnline, want) {
		t.Fatalf("ReleaseWorkerOnline = %q, want it to say %q", ReleaseWorkerOnline, want)
	}
	for _, tt := range []struct {
		name     string
		age      time.Duration
		released bool
	}{
		{"stop requested 16 minutes ago", 16 * time.Minute, true},
		{"stop requested 14 minutes ago", 14 * time.Minute, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
			seedForceRelease(t, st, "wrk_s")
			setStopRequested(t, st, "svc", time.Now().UTC().Add(-tt.age))

			got, err := st.ForceReleaseService(ctx, "sub_1", "svc", "mgr")
			if !tt.released {
				var re *ServiceReleaseError
				if got != nil || !errors.As(err, &re) || re.Reason != ReleaseWorkerOnline {
					t.Fatalf("ForceReleaseService = (%v, %v), want refused %q", got, err, ReleaseWorkerOnline)
				}
				if tk := mustGetTask(t, st, "svc"); tk.State != model.TaskStateRunning || tk.Stderr != "serving" {
					t.Errorf("task = %s %q, want unchanged", tk.State, tk.Stderr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ForceReleaseService = %v", err)
			}
			want := "serving\n" + ForceReleasedStderr("mgr") + "; " + ForceReleaseNotifyWorker
			for _, tk := range []*model.Task{got, mustGetTask(t, st, "svc")} {
				if tk.State != model.TaskStateSkipped || tk.Stderr != want {
					t.Errorf("task = %s %q, want SKIPPED %q", tk.State, tk.Stderr, want)
				}
			}
			if charges, err := st.SumChargedByWorker(ctx); err != nil || len(charges) != 0 {
				t.Errorf("charges = %v, %v; want none", charges, err)
			}
		})
	}
}

// F1d: a service whose submission row is missing is force-released even
// with its worker online and not lost (the caller allows that to admins).
func TestForceReleaseService_SubmissionMissing(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
	seedForceRelease(t, st, "wrk_s")
	deleteSubmissionRow(t, st)

	got, err := st.ForceReleaseService(ctx, "sub_1", "svc", "admin")
	if err != nil {
		t.Fatalf("ForceReleaseService = %v", err)
	}
	want := "serving\n" + ForceReleasedStderr("admin") + "; " + ForceReleaseNotifyWorker
	if stored := mustGetTask(t, st, "svc"); stored.State != model.TaskStateFailed || stored.Stderr != want || got.State != model.TaskStateFailed {
		t.Errorf("task = %s %q (answer %s), want FAILED %q", stored.State, stored.Stderr, got.State, want)
	}
	if charges, err := st.SumChargedByWorker(ctx); err != nil || len(charges) != 0 {
		t.Errorf("charges = %v, %v; want none", charges, err)
	}
}

// F8: the descendant walk only follows real parent links: a task with an
// empty id (a malformed row) does not make every top-level submission a
// child, so another submission's running service is not found.
func TestHasRunningService_EmptyParentLinkIgnored(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	subID := seedServiceSubmission(t, st)
	odd := svcQueued("", false, 0, 0, time.Hour)
	odd.SubmissionID, odd.State = subID, model.TaskStateSuccess
	mustCreateTask(t, st, odd)
	// Another top-level submission (parent_task_id '') with a running service.
	tk := inSubmission(svcQueued("svc_other", true, 0, 0, time.Hour), "sub_other")
	tk.State, tk.ExternalID = model.TaskStateRunning, "wrk_s"
	mustCreateTask(t, st, tk)

	if running, err := st.HasRunningService(ctx, subID); err != nil || running {
		t.Fatalf("HasRunningService = (%v, %v), want false", running, err)
	}
	if running, err := st.HasRunningService(ctx, "sub_other"); err != nil || !running {
		t.Fatalf("HasRunningService(sub_other) = (%v, %v), want true", running, err)
	}
}
