package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Review fixes for "stopping a service", on the HTTP side: the heartbeat
// tells a worker to stop its stopping services whatever it lists, force
// release covers an old stop request and a missing submission without
// letting anyone probe, a refused delete leaves the submission cancelled,
// and the cancel answer counts the stopping services.

// hmgrToken authenticates hmgr, made manager of group h by some tests.
const hmgrToken = "un=hmgr|tokenid=t-hmgr|expiry=4102444800|sig=h"

// F2a: a RUNNING service with a stop request is in cancel_tasks of its
// worker's heartbeat whatever the worker lists, also when running_tasks is
// absent; it stays RUNNING until the worker reports.
func TestWorkerHeartbeat_StoppingServiceWithoutList(t *testing.T) {
	srv, st := testServerWithStore()
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	_, subID := createTestSubmission(t, srv)
	seedChargedService(t, st, subID, "task_svc", "wrk_svc")
	if w, _ := doPut(t, srv, "/api/v1/submissions/"+subID+"/cancel", ""); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}

	// The listing body last: it marks the service missing (first strike).
	for _, body := range []string{"", "{}", `{"running_tasks":null}`, `{"host":{"load1":1}}`, `{"running_tasks":["other"]}`} {
		resp := heartbeat(t, srv, "wrk_svc", body)
		if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "task_svc" {
			t.Errorf("heartbeat %q: cancel_tasks = %v, want [task_svc]", body, resp.CancelTasks)
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning {
			t.Fatalf("heartbeat %q: task = %s, want RUNNING until the worker reports", body, tk.State)
		}
	}
}

// F5 (missing test): SKIPPED for a service with a stop request is stored as
// sent although its submission is not CANCELLED.
func TestWorkerTaskComplete_ServiceSkippedWithStopRequest(t *testing.T) {
	ctx := context.Background()
	srv, st := testServerWithStore()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	seedServiceIn(t, st, subID, "task_svc", workerID)
	// The task step of a cancel alone: a stop request, the submission as it was.
	if _, err := st.CancelNonTerminalTasks(ctx, subID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if sub, err := st.GetSubmission(ctx, subID); err != nil || sub == nil || sub.State == model.SubmissionStateCancelled {
		t.Fatalf("submission = %+v, %v; want not CANCELLED", sub, err)
	}
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt == nil {
		t.Fatalf("task = %s (stop %v), want RUNNING with a stop request", tk.State, tk.ServiceStopRequestedAt)
	}

	if code, env := completeTask(t, srv, workerID, "task_svc", `{"state":"SKIPPED","exit_code":-1,"stderr":"stopped"}`); code != http.StatusOK {
		t.Fatalf("complete = %d %+v, want 200", code, env.Error)
	}
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateSkipped || tk.Stderr != "stopped" {
		t.Errorf("task = %s stderr %q, want SKIPPED %q", tk.State, tk.Stderr, "stopped")
	}
}

// F7: the cancel answer counts the RUNNING services left stopping, and not
// among the tasks already completed.
func TestCancelSubmission_ServicesStopping(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	seedOwnedSubmission(t, st, "sub_c", "admin")
	seedChargedService(t, st, "sub_c", "task_svc", "wrk_svc")
	queued := serviceTask("sub_c", "task_queued")
	queued.RuntimeHints.Service = nil
	done := serviceTask("sub_c", "task_done")
	done.RuntimeHints.Service = nil
	now := time.Now().UTC()
	done.State, done.CompletedAt = model.TaskStateSuccess, &now
	for _, tk := range []*model.Task{queued, done} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	w, env := mgDo(t, srv, "PUT", "/api/v1/submissions/sub_c/cancel", "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"tasks_cancelled": 1, "services_stopping": 1, "tasks_already_completed": 1} {
		if got, ok := data[key].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v (answer %v)", key, data[key], want, data)
		}
	}
}

// metricSum sums the values of counter name over its series whose label
// has value (every series when label is "").
func metricSum(t *testing.T, reg *metrics.Registry, name, label, value string) float64 {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := label == ""
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					match = true
				}
			}
			if match {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// F6 + F7: the force-release answer holds only id, state, external_id,
// completed_at and worker_notified, and the release is recorded in the task
// metrics: a FAILED one as a failure with reason force_release, a SKIPPED
// one in the skipped counter.
func TestForceReleaseService_SmallAnswerAndMetrics(t *testing.T) {
	t.Run("FAILED", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLostService)
		reg := metrics.NewRegistry(metrics.Config{})
		f.srv.metrics = reg
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", adminAuthToken)
		if w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s", w.Code, w.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if got, want := strings.Join(keys, ","), "completed_at,external_id,id,state,worker_notified"; got != want {
			t.Errorf("answer keys = %s, want %s", got, want)
		}
		for _, k := range []string{"inputs", "job", "tool", "stdout", "stderr"} {
			if _, ok := data[k]; ok {
				t.Errorf("answer has %q", k)
			}
		}
		if data["id"] != "task_svc" || data["state"] != string(model.TaskStateFailed) || data["external_id"] != "wrk_lost" ||
			data["worker_notified"] != ForceReleaseNotifiedIfBack || data["completed_at"] == nil {
			t.Errorf("answer = %v", data)
		}
		if n := metricSum(t, reg, "gowe_task_failures_total", "reason", "force_release"); n != 1 {
			t.Errorf("failures{reason=force_release} = %v, want 1", n)
		}
	})
	t.Run("SKIPPED", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLostService)
		reg := metrics.NewRegistry(metrics.Config{})
		f.srv.metrics = reg
		if w, _ := mgDo(t, f.srv, "PUT", "/api/v1/submissions/sub_fr/cancel", "", adminAuthToken); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
		before := metricSum(t, reg, "gowe_tasks_skipped_total", "", "")
		if w, _ := mgDo(t, f.srv, "POST", forceReleasePath, "", mgrToken); w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s", w.Code, w.Body.String())
		}
		if after := metricSum(t, reg, "gowe_tasks_skipped_total", "", ""); after != before+1 {
			t.Errorf("tasks skipped = %v, want %v", after, before+1)
		}
		if n := metricSum(t, reg, "gowe_task_failures_total", "reason", "force_release"); n != 0 {
			t.Errorf("failures{reason=force_release} = %v, want 0", n)
		}
	})
}

// F6: a caller who is neither a server admin nor a manager of any group
// gets the same 403 whether the submission and task exist or not, and a
// manager of another group gets it for an existing task; nothing changes.
func TestForceReleaseService_NoProbing(t *testing.T) {
	f := newForceReleaseFixture(t, seedLostService)
	if w, _ := wgDo(t, f.srv, "PUT", workerGroupsPath+"/h", `{"users":["bob"],"managers":["hmgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT h: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct{ name, token, path string }{
		{"ordinary user, existing task", plainToken, forceReleasePath},
		{"ordinary user, unknown task", plainToken, "/api/v1/submissions/sub_fr/tasks/task_nope/force-release"},
		{"ordinary user, unknown submission", plainToken, "/api/v1/submissions/sub_nope/tasks/task_nope/force-release"},
		{"manager of another group, existing task", hmgrToken, forceReleasePath},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, env := mgDo(t, f.srv, "POST", c.path, "", c.token)
			if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
				env.Error.Message != ForceReleaseRefused {
				t.Fatalf("force-release = %d %+v, want 403 %q", w.Code, env.Error, ForceReleaseRefused)
			}
			if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateRunning {
				t.Errorf("task = %s, want RUNNING", tk.State)
			}
		})
	}
}

// F2b: with its worker online and not lost, a service whose stop request is
// older than model.ServiceStopReleaseAfter is force-released (the live
// worker is then told to stop it); a younger one is refused with 409.
func TestForceReleaseService_OldStopRequest(t *testing.T) {
	seedLive := func(t *testing.T, st store.Store) {
		seedGPUServiceWorker(t, st, "wrk_live", time.Now().UTC())
		seedChargedService(t, st, "sub_fr", "task_svc", "wrk_live")
	}
	t.Run("stop requested 16 minutes ago", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLive)
		if _, err := f.st.CancelNonTerminalTasks(context.Background(), "sub_fr", time.Now().UTC().Add(-16*time.Minute)); err != nil {
			t.Fatal(err)
		}
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", mgrToken)
		if w.Code != http.StatusOK {
			t.Fatalf("force-release = %d %s, want 200", w.Code, w.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(env.Data, &data); err != nil || data["state"] != string(model.TaskStateSkipped) ||
			data["worker_notified"] != ForceReleaseNotifiedNextHeartbeat {
			t.Errorf("answer = %v (%v), want SKIPPED, worker told on its next heartbeat", data, err)
		}
		if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateSkipped ||
			!strings.HasSuffix(tk.Stderr, store.ForceReleasedStderr("mgr")+"; "+store.ForceReleaseNotifyWorker) {
			t.Errorf("task = %s %q, want SKIPPED with the release line", tk.State, tk.Stderr)
		}
		resp := heartbeat(t, f.srv, "wrk_live", `{"running_tasks":["task_svc"]}`)
		if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "task_svc" {
			t.Errorf("cancel_tasks = %v, want [task_svc]", resp.CancelTasks)
		}
	})
	t.Run("stop requested just now", func(t *testing.T) {
		f := newForceReleaseFixture(t, seedLive)
		if w, _ := mgDo(t, f.srv, "PUT", "/api/v1/submissions/sub_fr/cancel", "", adminAuthToken); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
		const want = "the service's worker is online; cancel the submission, then wait 15 minutes or until the worker is marked lost"
		w, env := mgDo(t, f.srv, "POST", forceReleasePath, "", mgrToken)
		if w.Code != http.StatusConflict || env.Error == nil || env.Error.Message != want {
			t.Fatalf("force-release = %d %+v, want 409 %q", w.Code, env.Error, want)
		}
		if tk := mustTask(t, f.st, "task_svc"); tk.State != model.TaskStateRunning {
			t.Errorf("task = %s, want RUNNING", tk.State)
		}
	})
}

// newFileManagedGroupServer is newManagedGroupServer on a SQLite file, with
// a second raw handle on the file for rows the store API never leaves (a
// task whose submission row is gone).
func newFileManagedGroupServer(t *testing.T) (*Server, store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gowe.db")
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(path, logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	policy := groupacl.New().WithAdmins(adminCfg.IsAdmin)
	srv := New(config.DefaultServerConfig(), st, nil, logger,
		WithAnonymousConfig(&AnonymousConfig{Enabled: true, AllowedExecutors: []model.ExecutorType{model.ExecutorTypeWorker}}),
		WithAdminConfig(adminCfg), WithGroupPolicy(policy))
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/g", `{"users":["alice"],"managers":["mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT g: %d %s", w.Code, w.Body.String())
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db file: %v", err)
	}
	t.Cleanup(func() { raw.Close() })
	return srv, st, raw
}

// F1d: a RUNNING service whose submission row is gone (its worker online) is
// released by a server admin only: a manager of its group gets 404 like for
// any unknown submission; the admin's release frees its charges, and the
// live worker is told to stop it on its next heartbeat.
func TestForceReleaseService_SubmissionRowMissing(t *testing.T) {
	ctx := context.Background()
	srv, st, raw := newFileManagedGroupServer(t)
	seedGPUServiceWorker(t, st, "wrk_live", time.Now().UTC())
	seedChargedService(t, st, "sub_gone", "task_svc", "wrk_live")
	if _, err := raw.ExecContext(ctx, `DELETE FROM submissions WHERE id = ?`, "sub_gone"); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/submissions/sub_gone/tasks/task_svc/force-release"

	for _, c := range []struct{ name, path, token string }{
		{"manager of the group", path, mgrToken},
		{"admin, task of another submission id", "/api/v1/submissions/sub_nope/tasks/task_svc/force-release", adminAuthToken},
	} {
		w, env := mgDo(t, srv, "POST", c.path, "", c.token)
		if w.Code != http.StatusNotFound || env.Error == nil || env.Error.Code != model.ErrNotFound {
			t.Fatalf("%s: force-release = %d %+v, want 404", c.name, w.Code, env.Error)
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning {
			t.Fatalf("%s: task = %s, want RUNNING", c.name, tk.State)
		}
	}

	logs := captureLogs(srv)
	w, env := mgDo(t, srv, "POST", path, "", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin force-release = %d %s, want 200", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil || data["state"] != string(model.TaskStateFailed) ||
		data["worker_notified"] != ForceReleaseNotifiedNextHeartbeat {
		t.Errorf("answer = %v (%v), want FAILED, worker told on its next heartbeat", data, err)
	}
	tk := mustTask(t, st, "task_svc")
	if tk.State != model.TaskStateFailed || !strings.Contains(tk.Stderr, store.ForceReleasedStderr("admin")) ||
		!strings.HasSuffix(tk.Stderr, store.ForceReleaseNotifyWorker) {
		t.Errorf("task = %s %q, want FAILED with the release line and the worker notice", tk.State, tk.Stderr)
	}
	if charges, err := st.SumChargedByWorker(ctx); err != nil {
		t.Fatal(err)
	} else if _, ok := charges["wrk_live"]; ok {
		t.Errorf("charges = %v, want nothing charged to wrk_live", charges)
	}
	if out := logs.String(); !strings.Contains(out, "submission_exists=false") {
		t.Errorf("log lacks submission_exists=false:\n%s", out)
	}
	resp := heartbeat(t, srv, "wrk_live", `{"running_tasks":["task_svc"]}`)
	if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "task_svc" {
		t.Errorf("cancel_tasks = %v, want [task_svc]", resp.CancelTasks)
	}
}

// checkoutAfterCheckStore checks the QUEUED service of group g out to
// wrk_svc right after the delete handler's HasRunningService check, as a
// worker polling at that moment would.
type checkoutAfterCheckStore struct {
	store.Store
}

func (s *checkoutAfterCheckStore) HasRunningService(ctx context.Context, id string) (bool, error) {
	running, err := s.Store.HasRunningService(ctx, id)
	if err != nil || running {
		return running, err
	}
	if _, err := s.Store.CheckoutTask(ctx, "wrk_svc", "g", model.RuntimeNone); err != nil {
		return false, err
	}
	return false, nil
}

// F5: a delete refused by the store after the handler's own cancel (a
// service checked out in between) completes the cancel: 409 saying so, the
// submission CANCELLED, the service RUNNING with a stop request, the other
// task SKIPPED; after the worker confirms, scheduler ticks never complete
// the submission, and the delete then goes through.
func TestDeleteSubmission_ServiceCheckedOutDuringDelete(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	seedOwnedSubmission(t, st, "sub_del", "admin")
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	batch := serviceTask("sub_del", "task_batch")
	batch.RuntimeHints.Service = nil
	for _, tk := range []*model.Task{serviceTask("sub_del", "task_svc"), batch} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	srv.store = &checkoutAfterCheckStore{Store: st}

	w, env := mgDo(t, srv, "DELETE", "/api/v1/submissions/sub_del", "", adminAuthToken)
	if w.Code != http.StatusConflict || env.Error == nil || env.Error.Message != store.DeleteRefusedServiceStopping {
		t.Fatalf("delete = %d %+v, want 409 %q", w.Code, env.Error, store.DeleteRefusedServiceStopping)
	}
	checkSubmission := func(when string) {
		t.Helper()
		if sub, err := st.GetSubmission(ctx, "sub_del"); err != nil || sub == nil || sub.State != model.SubmissionStateCancelled {
			t.Fatalf("%s: submission = %+v, %v; want CANCELLED", when, sub, err)
		}
	}
	checkSubmission("after the refused delete")
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ExternalID != "wrk_svc" || tk.ServiceStopRequestedAt == nil {
		t.Fatalf("service = %s on %q (stop %v), want RUNNING on wrk_svc with a stop request", tk.State, tk.ExternalID, tk.ServiceStopRequestedAt)
	}
	if tk := mustTask(t, st, "task_batch"); tk.State != model.TaskStateSkipped {
		t.Errorf("batch task = %s, want SKIPPED", tk.State)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	execs := executor.NewRegistry(logger)
	execs.Register(executor.NewWorkerExecutor(st, logger))
	sched := scheduler.NewLoop(st, execs, scheduler.DefaultConfig(), logger)
	tick := func(when string) {
		t.Helper()
		for i := 0; i < 3; i++ {
			if err := sched.Tick(ctx); err != nil {
				t.Fatalf("%s: tick %d: %v", when, i+1, err)
			}
			checkSubmission(when)
		}
	}
	tick("ticks while stopping")
	if code, env := completeTask(t, srv, "wrk_svc", "task_svc", `{"state":"SKIPPED","exit_code":-1}`); code != http.StatusOK {
		t.Fatalf("complete = %d %+v, want 200", code, env.Error)
	}
	tick("ticks after the worker confirmed")

	srv.store = st
	if w, _ := mgDo(t, srv, "DELETE", "/api/v1/submissions/sub_del", "", adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("delete after the stop = %d %s, want 200", w.Code, w.Body.String())
	}
}

// F4: a keyless service-mode worker row is adopted only by a key whose group
// list names its group; a key with an empty group list is refused (403) and
// the row stays unbound.
func TestLegacyWorker_ServiceModeAdoption(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		want         int
		wantKeyID    string
	}{
		{"key with an empty group list refused", "secretA", http.StatusForbidden, ""},
		{"key naming the group adopts", "secretG1", http.StatusOK, "keyG1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := keyedServer(t)
			now := time.Now().UTC()
			if err := st.CreateWorker(context.Background(), &model.Worker{
				ID: "wrk_svc_legacy", Name: "legacy", Group: "g1", State: model.WorkerStateOnline,
				Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now, ServiceMode: true, HostID: "host-1",
				Labels: map[string]string{}, Datasets: map[string]string{},
			}); err != nil {
				t.Fatal(err)
			}
			w, _ := keyedDo(t, srv, tc.secret, "PUT", "/api/v1/workers/wrk_svc_legacy/heartbeat", `{}`)
			if w.Code != tc.want {
				t.Fatalf("heartbeat = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if got, err := st.GetWorker(context.Background(), "wrk_svc_legacy"); err != nil || got == nil || got.KeyID != tc.wantKeyID {
				t.Errorf("worker = %+v, %v; want key_id %q", got, err, tc.wantKeyID)
			}
		})
	}
}
