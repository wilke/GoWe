package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Stopping a service keeps its GPUs until the worker confirms, on the HTTP
// side: cancel -> heartbeat cancel_tasks -> /complete, a submission with a
// running service is not deleted, and force release of a service whose
// worker cannot confirm.

// seedGPUServiceWorker stores a service-mode worker of group g on host-1
// with one GPU, last seen at lastSeen.
func seedGPUServiceWorker(t *testing.T, st store.Store, id string, lastSeen time.Time) {
	t.Helper()
	if err := st.CreateWorker(context.Background(), &model.Worker{
		ID: id, Name: id, Hostname: id + ".example", Group: "g", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, LastSeen: lastSeen, RegisteredAt: lastSeen, ServiceMode: true, HostID: "host-1",
		GPUEnabled: true, GPUs: []model.GPUInfo{{Index: 0, UUID: "GPU-0", Name: "H200"}},
		Labels: map[string]string{}, Datasets: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
}

// serviceTask returns a service task of group g in subID needing one GPU.
func serviceTask(subID, id string) *model.Task {
	return &model.Task{
		ID: id, SubmissionID: subID, StepID: "s", State: model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker, ScatterIndex: -1, CreatedAt: time.Now().UTC().Add(-time.Hour),
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{},
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "g", RequiresGPU: true, GPUs: 1,
			Service: &model.ServiceSpec{PortInput: "port"}},
	}
}

// ensureLiveSubmission stores a RUNNING submission id of user anonymous
// unless one exists: a service task is stored only for a live submission
// (store.ErrServiceSubmissionGone).
func ensureLiveSubmission(t *testing.T, st store.Store, id string) {
	t.Helper()
	if sub, err := st.GetSubmission(context.Background(), id); err != nil {
		t.Fatal(err)
	} else if sub == nil {
		seedOwnedSubmission(t, st, id, "anonymous")
	}
}

// seedChargedService stores a RUNNING service task of workerID in subID
// (stored RUNNING unless it exists), charged one GPU, started an hour ago.
func seedChargedService(t *testing.T, st store.Store, subID, id, workerID string) {
	t.Helper()
	ensureLiveSubmission(t, st, subID)
	tk := serviceTask(subID, id)
	started := time.Now().UTC().Add(-time.Hour)
	zero, one, zero64 := 0, 1, int64(0)
	tk.State, tk.ExternalID, tk.StartedAt = model.TaskStateRunning, workerID, &started
	tk.ChargedCores, tk.ChargedRamMB, tk.ChargedGPUs = &zero, &zero64, &one
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
}

// checkout asks for work as workerID and returns the HTTP status and the
// task id handed out ("" for none).
func checkout(t *testing.T, srv *Server, workerID string) (int, string) {
	t.Helper()
	w, env := mgDo(t, srv, "GET", "/api/v1/workers/"+workerID+"/work", "", "")
	if w.Code != http.StatusOK {
		return w.Code, ""
	}
	var tk model.Task
	if err := json.Unmarshal(env.Data, &tk); err != nil {
		t.Fatal(err)
	}
	return w.Code, tk.ID
}

// Cancel keeps the running service RUNNING and charged; every heartbeat that
// lists it gets it in cancel_tasks; the worker's SKIPPED report ends it and
// frees the GPU; a FAILED report after a cancel is stored FAILED.
func TestServiceCancel_StopConfirmedByWorker(t *testing.T) {
	for _, report := range []model.TaskState{model.TaskStateSkipped, model.TaskStateFailed} {
		t.Run("report "+string(report), func(t *testing.T) {
			srv, st := testServerWithStore()
			seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
			_, subID := createTestSubmission(t, srv)
			seedChargedService(t, st, subID, "task_svc", "wrk_svc")
			// A GPU service of another submission, to probe the GPU charge.
			ensureLiveSubmission(t, st, "sub_probe")
			if err := st.CreateTask(context.Background(), serviceTask("sub_probe", "task_probe")); err != nil {
				t.Fatal(err)
			}

			if w, _ := doPut(t, srv, "/api/v1/submissions/"+subID+"/cancel", ""); w.Code != http.StatusOK {
				t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
			}
			if sub, err := st.GetSubmission(context.Background(), subID); err != nil || sub == nil || sub.State != model.SubmissionStateCancelled {
				t.Fatalf("submission = %+v, %v; want CANCELLED at once", sub, err)
			}
			tk := mustTask(t, st, "task_svc")
			if tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt == nil || tk.ChargedGPUs == nil || *tk.ChargedGPUs != 1 {
				t.Fatalf("after cancel: %s, stop requested %v, gpus %v; want RUNNING, stop requested, still charged",
					tk.State, tk.ServiceStopRequestedAt, tk.ChargedGPUs)
			}
			if code, got := checkout(t, srv, "wrk_svc"); code != http.StatusNoContent {
				t.Fatalf("checkout = %d %q, want 204: the stopping service still holds the GPU", code, got)
			}

			for i := 0; i < 3; i++ {
				resp := heartbeat(t, srv, "wrk_svc", `{"running_tasks":["task_svc"]}`)
				if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "task_svc" {
					t.Fatalf("heartbeat %d: cancel_tasks = %v, want [task_svc]", i+1, resp.CancelTasks)
				}
				if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning {
					t.Fatalf("heartbeat %d: task = %s, want RUNNING until the worker reports", i+1, tk.State)
				}
			}

			body := `{"state":"` + string(report) + `","exit_code":143,"stderr":"stopped"}`
			if code, env := completeTask(t, srv, "wrk_svc", "task_svc", body); code != http.StatusOK {
				t.Fatalf("complete = %d %+v, want 200", code, env.Error)
			}
			if tk := mustTask(t, st, "task_svc"); tk.State != report || tk.Stderr != "stopped" {
				t.Errorf("task = %s stderr %q, want %s %q", tk.State, tk.Stderr, report, "stopped")
			}
			if code, got := checkout(t, srv, "wrk_svc"); code != http.StatusOK || got != "task_probe" {
				t.Fatalf("checkout = %d %q, want task_probe: the GPU is free again", code, got)
			}
			if sub, err := st.GetSubmission(context.Background(), subID); err != nil || sub == nil || sub.State != model.SubmissionStateCancelled {
				t.Errorf("submission = %+v, %v; want still CANCELLED", sub, err)
			}
		})
	}
}

// seedOwnedSubmission stores a RUNNING submission of owner.
func seedOwnedSubmission(t *testing.T, st store.Store, id, owner string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateSubmission(context.Background(), &model.Submission{
		ID: id, WorkflowID: "wf_svc", WorkflowName: "svc", State: model.SubmissionStateRunning,
		SubmittedBy: owner, Inputs: map[string]any{}, Outputs: map[string]any{},
		Labels: map[string]string{}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// DELETE of a submission with a RUNNING service is refused with 409 and
// changes nothing (not even the cancel of its other tasks); once the service
// has ended the submission is deleted.
func TestDeleteSubmission_RunningServiceConflict(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	seedOwnedSubmission(t, st, "sub_del", "admin")
	seedChargedService(t, st, "sub_del", "task_svc", "wrk_svc")
	batch := serviceTask("sub_del", "task_batch")
	batch.RuntimeHints.Service = nil
	if err := st.CreateTask(ctx, batch); err != nil {
		t.Fatal(err)
	}

	w, env := mgDo(t, srv, "DELETE", "/api/v1/submissions/sub_del", "", adminAuthToken)
	if w.Code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict ||
		env.Error.Message != store.ErrSubmissionHasRunningService.Error() {
		t.Fatalf("delete = %d %+v, want 409 %q", w.Code, env.Error, store.ErrSubmissionHasRunningService.Error())
	}
	if sub, err := st.GetSubmission(ctx, "sub_del"); err != nil || sub == nil || sub.State != model.SubmissionStateRunning {
		t.Errorf("submission = %+v, %v; want kept, RUNNING", sub, err)
	}
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt != nil {
		t.Errorf("service = %s (stop %v), want RUNNING, untouched", tk.State, tk.ServiceStopRequestedAt)
	}
	if tk := mustTask(t, st, "task_batch"); tk.State != model.TaskStateQueued {
		t.Errorf("batch task = %s, want QUEUED (nothing cancelled)", tk.State)
	}

	// The service ended: the delete goes through.
	svc := mustTask(t, st, "task_svc")
	now := time.Now().UTC()
	svc.State, svc.CompletedAt = model.TaskStateSkipped, &now
	if err := st.UpdateTask(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if w, _ := mgDo(t, srv, "DELETE", "/api/v1/submissions/sub_del", "", adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("delete after the end = %d %s, want 200", w.Code, w.Body.String())
	}
	if sub, _ := st.GetSubmission(ctx, "sub_del"); sub != nil {
		t.Error("submission still there")
	}
}

const forceReleasePath = "/api/v1/submissions/sub_fr/tasks/task_svc/force-release"

// forceReleaseFixture is a server with groups g (manager mgr) and h, a
// submission sub_fr of user plain holding task_svc, a submission sub_other,
// and a minted service token.
type forceReleaseFixture struct {
	srv    *Server
	st     store.Store
	svcTok string
}

// newForceReleaseFixture builds the fixture; seed adds the task (and its
// worker).
func newForceReleaseFixture(t *testing.T, seed func(t *testing.T, st store.Store)) forceReleaseFixture {
	t.Helper()
	srv, st, _ := newManagedGroupServer(t)
	seedOwnedSubmission(t, st, "sub_fr", "plain")
	seedOwnedSubmission(t, st, "sub_other", "plain")
	seed(t, st)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	return forceReleaseFixture{srv: srv, st: st, svcTok: raw}
}

// seedLostService stores worker wrk_lost (silent for an hour, reaped:
// offline, its service marked lost) holding task_svc of sub_fr.
func seedLostService(t *testing.T, st store.Store) {
	t.Helper()
	seedGPUServiceWorker(t, st, "wrk_lost", time.Now().UTC().Add(-time.Hour))
	seedChargedService(t, st, "sub_fr", "task_svc", "wrk_lost")
	if _, _, err := st.ReapStaleWorkers(context.Background(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if tk := mustTask(t, st, "task_svc"); tk.ServiceLostAt == nil {
		t.Fatal("service not marked lost by the reaper")
	}
}

// Who may force-release: refused (403, task unchanged) for the submission
// owner who manages nothing, a service identity and an anonymous caller.
func TestForceReleaseService_Refused(t *testing.T) {
	callers := []struct {
		name  string
		token func(f forceReleaseFixture) string
		want  string
	}{
		{"submission owner, not a manager", func(forceReleaseFixture) string { return plainToken }, ForceReleaseRefused},
		{"service identity", func(f forceReleaseFixture) string { return f.svcTok }, ForceReleaseRefused},
		{"anonymous", func(forceReleaseFixture) string { return "" }, ForceReleaseRefused},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			f := newForceReleaseFixture(t, seedLostService)
			w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", c.token(f))
			if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
				!strings.Contains(env.Error.Message, c.want) {
				t.Fatalf("force-release = %d %+v, want 403 containing %q", w.Code, env.Error, c.want)
			}
			if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateRunning {
				t.Errorf("task = %s, want RUNNING", tk.State)
			}
		})
	}

	conflicts := []struct {
		name string
		seed func(t *testing.T, st store.Store)
		want string
	}{
		{"worker online", func(t *testing.T, st store.Store) {
			seedGPUServiceWorker(t, st, "wrk_live", time.Now().UTC())
			seedChargedService(t, st, "sub_fr", "task_svc", "wrk_live")
		}, store.ReleaseWorkerOnline},
		{"not a service", func(t *testing.T, st store.Store) {
			tk := serviceTask("sub_fr", "task_svc")
			tk.RuntimeHints.Service = nil
			tk.State, tk.ExternalID = model.TaskStateRunning, "wrk_gone"
			if err := st.CreateTask(context.Background(), tk); err != nil {
				t.Fatal(err)
			}
		}, store.ReleaseNotService},
		{"not RUNNING", func(t *testing.T, st store.Store) {
			if err := st.CreateTask(context.Background(), serviceTask("sub_fr", "task_svc")); err != nil {
				t.Fatal(err)
			}
		}, store.ReleaseNotRunning},
	}
	for _, c := range conflicts {
		t.Run(c.name, func(t *testing.T) {
			f := newForceReleaseFixture(t, c.seed)
			before := mustTask(t, f.st, "task_svc")
			w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", adminAuthToken)
			if w.Code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict || env.Error.Message != c.want {
				t.Fatalf("force-release = %d %+v, want 409 %q", w.Code, env.Error, c.want)
			}
			if tk := mustTask(t, f.st, "task_svc"); tk.State != before.State {
				t.Errorf("task = %s, want unchanged %s", tk.State, before.State)
			}
		})
	}

	notFound := []struct{ name, path string }{
		{"task of another submission", "/api/v1/submissions/sub_other/tasks/task_svc/force-release"},
		{"unknown task", "/api/v1/submissions/sub_fr/tasks/task_nope/force-release"},
		{"unknown submission", "/api/v1/submissions/sub_nope/tasks/task_svc/force-release"},
	}
	for _, c := range notFound {
		t.Run(c.name, func(t *testing.T) {
			f := newForceReleaseFixture(t, seedLostService)
			w, env := mgDo(t, f.srv, "POST", c.path, "", adminAuthToken)
			if w.Code != http.StatusNotFound || env.Error == nil || env.Error.Code != model.ErrNotFound {
				t.Fatalf("force-release = %d %+v, want 404", w.Code, env.Error)
			}
			if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateRunning {
				t.Errorf("task = %s, want RUNNING", tk.State)
			}
		})
	}
}

// checkForceReleased fails unless the answer and the row show task_svc in
// want with the force-release line naming actor, and nothing is charged to
// workerID any more.
func checkForceReleased(t *testing.T, f forceReleaseFixture, env envelope, want model.TaskState, actor, workerID string) {
	t.Helper()
	var answer model.Task
	if err := json.Unmarshal(env.Data, &answer); err != nil || answer.ID != "task_svc" || answer.State != want {
		t.Errorf("answer = %+v (%v), want task_svc %s", answer, err, want)
	}
	tk := mustTask(t, f.st, "task_svc")
	if tk.State != want || !strings.HasSuffix(tk.Stderr, store.ForceReleasedStderr(actor)) {
		t.Errorf("task = %s stderr %q, want %s ending in %q", tk.State, tk.Stderr, want, store.ForceReleasedStderr(actor))
	}
	charges, err := f.st.SumChargedByWorker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := charges[workerID]; ok {
		t.Errorf("charges = %v, want nothing charged to %s", charges, workerID)
	}
}

// Force release succeeds for an admin or a manager of the group when the
// worker is lost or its row is gone: FAILED, or SKIPPED after a cancel; one
// WARN names actor, task, worker and host; the worker, when it comes back
// and lists the task, is told to stop it.
func TestForceReleaseService_Released(t *testing.T) {
	t.Run("admin, lost worker; the worker comes back", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLostService)
		logs := captureLogs(f.srv)
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", adminAuthToken)
		if w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s, want 200", w.Code, w.Body.String())
		}
		checkForceReleased(t, f, env, model.TaskStateFailed, "admin", "wrk_lost")
		out := logs.String()
		if strings.Count(out, "level=WARN") != 1 {
			t.Errorf("want exactly one WARN:\n%s", out)
		}
		for _, want := range []string{"force-released", "actor=admin", "task_id=task_svc", "worker_id=wrk_lost", "host_id=host-1"} {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
		}

		// The worker comes back and still lists the task: it is told to stop.
		resp := heartbeat(t, f.srv, "wrk_lost", `{"running_tasks":["task_svc"]}`)
		if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "task_svc" {
			t.Errorf("cancel_tasks = %v, want [task_svc]", resp.CancelTasks)
		}
		if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateFailed {
			t.Errorf("task after the heartbeat = %s, want still FAILED", tk.State)
		}
	})

	t.Run("manager, worker row missing", func(t *testing.T) {
		f := newForceReleaseFixture(t, func(t *testing.T, st store.Store) {
			seedChargedService(t, st, "sub_fr", "task_svc", "wrk_gone")
		})
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", mgrToken)
		if w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s, want 200", w.Code, w.Body.String())
		}
		checkForceReleased(t, f, env, model.TaskStateFailed, "mgr", "wrk_gone")
	})

	t.Run("manager, lost worker, after a cancel", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLostService)
		if w, _ := mgDo(t, f.srv, "PUT", "/api/v1/submissions/sub_fr/cancel", "", adminAuthToken); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
		if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt == nil {
			t.Fatalf("after cancel: %s (stop %v), want RUNNING with a stop request", tk.State, tk.ServiceStopRequestedAt)
		}
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", mgrToken)
		if w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s, want 200", w.Code, w.Body.String())
		}
		checkForceReleased(t, f, env, model.TaskStateSkipped, "mgr", "wrk_lost")
	})
}
