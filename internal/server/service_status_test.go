package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// A service-mode worker reports a status for each running service in its
// heartbeat ("services"); the server validates and stores it and shows it on
// the task, with a state that never looks live for a task that is not.

// newStatusServer is testServerWithStore with the server's log (DEBUG and
// up) captured, and the store optionally wrapped.
func newStatusServer(t *testing.T, wrap func(store.Store) store.Store) (*Server, store.Store, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", quiet)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	var s store.Store = st
	if wrap != nil {
		s = wrap(st)
	}
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := New(config.DefaultServerConfig(), s, nil, logger,
		WithAnonymousConfig(&AnonymousConfig{
			Enabled:          true,
			AllowedExecutors: []model.ExecutorType{model.ExecutorTypeLocal, model.ExecutorTypeContainer, model.ExecutorTypeWorker, model.ExecutorTypeBVBRC},
		}))
	return srv, st, &logs
}

// statusOf returns the service status of task id as the store reads it.
func statusOf(t *testing.T, st store.Store, id string) *model.ServiceStatus {
	t.Helper()
	return mustTask(t, st, id).ServiceStatus
}

// heartbeatCode sends body as a heartbeat of workerID and returns the status
// code and envelope, whatever the code.
func heartbeatCode(t *testing.T, srv *Server, workerID, body string) (int, envelope) {
	t.Helper()
	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", body)
	return w.Code, env
}

// A service-mode worker's statuses are stored on its RUNNING services and
// shown by the task endpoint; updated_at and ready_at come from the server.
func TestHeartbeatServices_StoredFromServiceModeWorker(t *testing.T) {
	srv, st := testServerWithStore()
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	seedOwnedTask(t, st, "task_svc", "wrk_svc", true, model.TaskStateRunning)

	before := time.Now().UTC()
	heartbeat(t, srv, "wrk_svc", `{"running_tasks":["task_svc"],"services":[
		{"task_id":"task_svc","state":"ready","host":"gpu01.example.org","port":8000,"scheme":"http",
		 "gpus":[{"index":0,"uuid":"GPU-0"}],"pid":42,"detail":"ok","updated_at":"2000-01-01T00:00:00Z"}]}`)

	got := statusOf(t, st, "task_svc")
	if got == nil || got.State != model.ServiceStateReady || got.Host != "gpu01.example.org" || got.Port != 8000 ||
		len(got.GPUs) != 1 || got.GPUs[0].UUID != "GPU-0" || got.PID != 42 || got.Detail != "ok" {
		t.Fatalf("stored status = %+v, want the report", got)
	}
	if got.UpdatedAt.Before(before) {
		t.Errorf("updated_at = %v, want the server's time (>= %v), not the worker's", got.UpdatedAt, before)
	}
	if got.ReadyAt == nil || got.ReadyAt.Before(before) {
		t.Errorf("ready_at = %v, want the server's time of the first ready report", got.ReadyAt)
	}

	// The task endpoint shows it.
	env := doGet(t, srv, "/api/v1/submissions/sub_review/tasks/task_svc")
	var tk model.Task
	if err := json.Unmarshal(env.Data, &tk); err != nil {
		t.Fatal(err)
	}
	if tk.ServiceStatus == nil || tk.ServiceStatus.State != model.ServiceStateReady || tk.ServiceStatus.Port != 8000 {
		t.Errorf("GET task service_status = %+v, want ready on port 8000", tk.ServiceStatus)
	}
}

// A worker that is not in service mode gets its services field ignored
// entirely (one DEBUG), even an oversized one: no 400, nothing stored.
func TestHeartbeatServices_IgnoredFromBatchWorker(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	workerID := registerTestWorker(t, srv)
	seedOwnedTask(t, st, "task_svc", workerID, true, model.TaskStateRunning)

	heartbeat(t, srv, workerID, `{"running_tasks":["task_svc"],"services":[{"task_id":"task_svc","state":"ready"}]}`)
	if got := statusOf(t, st, "task_svc"); got != nil {
		t.Fatalf("status = %+v, want none from a batch worker", got)
	}
	if n := strings.Count(logs.String(), "ignoring services from a worker not in service mode"); n != 1 {
		t.Errorf("DEBUG lines = %d, want 1:\n%s", n, logs.String())
	}

	if code, env := heartbeatCode(t, srv, workerID, servicesBody(model.MaxHeartbeatServices+1)); code != http.StatusOK {
		t.Errorf("oversized list from a batch worker = %d %+v, want 200 (field ignored)", code, env.Error)
	}
}

// servicesBody is a heartbeat body with n valid service entries for tasks
// t0..t(n-1).
func servicesBody(n int) string {
	entries := make([]map[string]any, n)
	for i := range entries {
		entries[i] = map[string]any{"task_id": fmt.Sprintf("t%d", i), "state": "starting"}
	}
	b, _ := json.Marshal(map[string]any{"services": entries})
	return string(b)
}

// More than 64 entries from a service-mode worker: the whole list is ignored
// with one WARN (worker id, count), and everything else in the heartbeat
// runs: 200, last_seen and the usage sample stored, a stop-requested service
// in cancel_tasks; nothing stored. 64 entries are accepted.
func TestHeartbeatServices_TooMany(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	old := time.Now().UTC().Add(-time.Minute)
	seedGPUServiceWorker(t, st, "wrk_svc", old)
	seedOwnedTask(t, st, "t0", "wrk_svc", true, model.TaskStateRunning)
	_, subID := createTestSubmission(t, srv)
	seedServiceIn(t, st, subID, "task_stop", "wrk_svc")
	if w, _ := doPut(t, srv, "/api/v1/submissions/"+subID+"/cancel", ""); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
	}
	if tk := mustTask(t, st, "task_stop"); tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt == nil {
		t.Fatalf("task_stop = %s, stop %v; want RUNNING with a stop request (setup)", tk.State, tk.ServiceStopRequestedAt)
	}

	entries := make([]map[string]any, model.MaxHeartbeatServices+1)
	for i := range entries {
		entries[i] = map[string]any{"task_id": fmt.Sprintf("t%d", i), "state": "starting"}
	}
	entries[1] = map[string]any{"task_id": "task_stop", "state": "stopping"}
	host := map[string]any{"load1": 1.5, "load5": 1, "mem_total_mb": 4096, "mem_available_mb": 1024, "sampled_at": "2026-11-27T12:00:00Z"}
	body, err := json.Marshal(map[string]any{"running_tasks": []string{"t0", "task_stop"}, "host": host, "services": entries})
	if err != nil {
		t.Fatal(err)
	}
	code, env := heartbeatCode(t, srv, "wrk_svc", string(body))
	if code != http.StatusOK {
		t.Fatalf("heartbeat = %d %+v, want 200 (the list is ignored)", code, env.Error)
	}
	var resp model.HeartbeatResponse
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatal(err)
	}
	cancel := strings.Join(resp.CancelTasks, ",")
	if !strings.Contains(","+cancel+",", ",task_stop,") || strings.Contains(","+cancel+",", ",t0,") {
		t.Errorf("cancel_tasks = %v, want task_stop (and not t0)", resp.CancelTasks)
	}
	for _, id := range []string{"t0", "task_stop"} {
		if got := statusOf(t, st, id); got != nil {
			t.Errorf("%s status = %+v, want none stored", id, got)
		}
	}
	wk, err := st.GetWorker(context.Background(), "wrk_svc")
	if err != nil || wk == nil || !wk.LastSeen.After(old) {
		t.Fatalf("worker = %+v, %v; want last_seen updated", wk, err)
	}
	if wk.Usage == nil || wk.Usage.Host == nil || wk.Usage.Host.Load1 != 1.5 || wk.Usage.Host.MemAvailableMB != 1024 {
		t.Errorf("usage = %+v, want the sample of the same heartbeat", wk.Usage)
	}
	out := logs.String()
	if n := strings.Count(out, "heartbeat: ignoring services: too many entries"); n != 1 {
		t.Errorf("too-many WARN lines = %d, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `level=WARN msg="heartbeat: ignoring services: too many entries" component=server worker_id=wrk_svc count=65`) {
		t.Errorf("no WARN naming the worker and the count:\n%s", out)
	}

	if code, env := heartbeatCode(t, srv, "wrk_svc", servicesBody(model.MaxHeartbeatServices)); code != http.StatusOK {
		t.Fatalf("64 entries = %d %+v, want 200", code, env.Error)
	}
	if got := statusOf(t, st, "t0"); got == nil || got.State != model.ServiceStateStarting {
		t.Errorf("status = %+v, want starting", got)
	}
}

// An invalid entry is skipped with one WARN naming the task and the reason;
// the valid ones are stored and the heartbeat succeeds. The last of
// duplicate entries wins.
func TestHeartbeatServices_InvalidEntrySkipped(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	for _, id := range []string{"task_a", "task_b", "task_c", "task_d"} {
		seedOwnedTask(t, st, id, "wrk_svc", true, model.TaskStateRunning)
	}

	resp := heartbeat(t, srv, "wrk_svc", `{"running_tasks":["task_a","task_b","task_c","task_d"],"services":[
		{"task_id":"task_a","state":"starting","detail":"line1\nline2"},
		{"task_id":"task_b","state":"bogus"},
		{"task_id":"task_c","state":"ready","port":"not a number"},
		{"state":"ready"},
		{"task_id":"task_d","state":"starting"},
		{"task_id":"task_d","state":"unhealthy","detail":"503"}]}`)
	if resp.WorkerID != "wrk_svc" {
		t.Errorf("response = %+v", resp)
	}

	if got := statusOf(t, st, "task_a"); got == nil || got.State != model.ServiceStateStarting || got.Detail != "line1 line2" {
		t.Errorf("task_a status = %+v, want starting with the detail normalised", got)
	}
	for _, id := range []string{"task_b", "task_c"} {
		if got := statusOf(t, st, id); got != nil {
			t.Errorf("%s status = %+v, want none (invalid entry skipped)", id, got)
		}
	}
	if got := statusOf(t, st, "task_d"); got == nil || got.State != model.ServiceStateUnhealthy || got.Detail != "503" {
		t.Errorf("task_d status = %+v, want the last entry (unhealthy)", got)
	}

	out := logs.String()
	for _, want := range []string{
		`task_id=task_b error="state must be starting, ready, unhealthy or stopping, got \"bogus\""`,
		`msg="heartbeat: skipping a service status that cannot be decoded" component=server worker_id=wrk_svc task_id=task_c`,
		`msg="heartbeat: skipping a service status without task_id"`,
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("log does not hold exactly one %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "level=WARN"); n != 3 {
		t.Errorf("WARN lines = %d, want 3 (one per skipped entry):\n%s", n, out)
	}
}

// A report for another worker's service changes nothing.
func TestHeartbeatServices_AnotherWorkersTaskIgnored(t *testing.T) {
	srv, st := testServerWithStore()
	seedGPUServiceWorker(t, st, "wrk_a", time.Now().UTC())
	seedGPUServiceWorker(t, st, "wrk_b", time.Now().UTC())
	seedOwnedTask(t, st, "task_b", "wrk_b", true, model.TaskStateRunning)

	heartbeat(t, srv, "wrk_a", `{"services":[{"task_id":"task_b","state":"ready","host":"evil.example","port":1}]}`)
	if got := statusOf(t, st, "task_b"); got != nil {
		t.Fatalf("status = %+v, want none (not wrk_a's task)", got)
	}
	heartbeat(t, srv, "wrk_b", `{"services":[{"task_id":"task_b","state":"starting"}]}`)
	if got := statusOf(t, st, "task_b"); got == nil || got.State != model.ServiceStateStarting {
		t.Errorf("status = %+v, want its own worker's report", got)
	}
}

// twoStrikesNowStore runs the reconcile with no grace window, twice, so a
// service the heartbeat omits is ended by the heartbeat's own reconcile.
type twoStrikesNowStore struct{ store.Store }

func (s twoStrikesNowStore) ReconcileWorkerTasks(ctx context.Context, workerID string, running []string, _ time.Duration) ([]string, error) {
	if _, err := s.Store.ReconcileWorkerTasks(ctx, workerID, running, 0); err != nil {
		return nil, err
	}
	time.Sleep(2 * time.Millisecond) // the mark must be strictly older than the second pass's cutoff
	return s.Store.ReconcileWorkerTasks(ctx, workerID, running, 0)
}

// The status is stored after the reconcile: a service the same heartbeat's
// reconcile ended is not written.
func TestHeartbeatServices_NotWrittenForTaskReconcileEnded(t *testing.T) {
	srv, st, _ := newStatusServer(t, func(s store.Store) store.Store { return twoStrikesNowStore{s} })
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	seedOwnedTask(t, st, "task_svc", "wrk_svc", true, model.TaskStateRunning)

	heartbeat(t, srv, "wrk_svc", `{"running_tasks":[],"services":[{"task_id":"task_svc","state":"ready","port":8000}]}`)
	tk := mustTask(t, st, "task_svc")
	if tk.State != model.TaskStateFailed {
		t.Fatalf("task = %s, want FAILED by the reconcile (test setup)", tk.State)
	}
	if tk.ServiceStatus != nil {
		t.Errorf("status = %+v, want none: the reconcile ended the task before the status was stored", tk.ServiceStatus)
	}
}

// The shown state of a stored status follows the task: stopping after a
// cancel (stop requested), lost when the worker went silent, stopped once the
// task ended; on the task, list and submission endpoints alike.
func TestServiceStatus_ShownStateOnAPI(t *testing.T) {
	readers := func(t *testing.T, srv *Server, subID string) map[string]string {
		t.Helper()
		out := map[string]string{}
		var tk model.Task
		if err := json.Unmarshal(doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/task_svc").Data, &tk); err != nil {
			t.Fatal(err)
		}
		if tk.ServiceStatus != nil {
			out["task"] = tk.ServiceStatus.State
		}
		var list []model.Task
		if err := json.Unmarshal(doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks").Data, &list); err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 && list[0].ServiceStatus != nil {
			out["list"] = list[0].ServiceStatus.State
		}
		var sub model.Submission
		if err := json.Unmarshal(doGet(t, srv, "/api/v1/submissions/"+subID).Data, &sub); err != nil {
			t.Fatal(err)
		}
		if len(sub.Tasks) == 1 && sub.Tasks[0].ServiceStatus != nil {
			out["submission"] = sub.Tasks[0].ServiceStatus.State
		}
		return out
	}
	setup := func(t *testing.T) (*Server, store.Store, string) {
		t.Helper()
		srv, st := testServerWithStore()
		seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
		_, subID := createTestSubmission(t, srv)
		seedServiceIn(t, st, subID, "task_svc", "wrk_svc")
		heartbeat(t, srv, "wrk_svc", `{"running_tasks":["task_svc"],"services":[{"task_id":"task_svc","state":"ready","port":8000}]}`)
		return srv, st, subID
	}
	check := func(t *testing.T, srv *Server, st store.Store, subID, want string) {
		t.Helper()
		got := readers(t, srv, subID)
		for _, r := range []string{"task", "list", "submission"} {
			if got[r] != want {
				t.Errorf("%s endpoint shows %q, want %q", r, got[r], want)
			}
		}
	}

	t.Run("running", func(t *testing.T) {
		srv, st, subID := setup(t)
		check(t, srv, st, subID, model.ServiceStateReady)
	})
	t.Run("stop requested", func(t *testing.T) {
		srv, st, subID := setup(t)
		if w, _ := doPut(t, srv, "/api/v1/submissions/"+subID+"/cancel", ""); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceStopRequestedAt == nil {
			t.Fatalf("task = %s, stop %v; want RUNNING with a stop request (setup)", tk.State, tk.ServiceStopRequestedAt)
		}
		check(t, srv, st, subID, model.ServiceStateStopping)
	})
	t.Run("worker lost", func(t *testing.T) {
		srv, st, subID := setup(t)
		time.Sleep(2 * time.Millisecond)
		if _, _, err := st.ReapStaleWorkers(context.Background(), time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceLostAt == nil {
			t.Fatalf("task = %s, lost %v; want RUNNING and lost (setup)", tk.State, tk.ServiceLostAt)
		}
		check(t, srv, st, subID, model.ServiceStateLost)
	})
	t.Run("ended", func(t *testing.T) {
		srv, st, subID := setup(t)
		if code, env := completeTask(t, srv, "wrk_svc", "task_svc", `{"state":"FAILED","exit_code":1}`); code != http.StatusOK {
			t.Fatalf("complete = %d %+v", code, env.Error)
		}
		check(t, srv, st, subID, model.ServiceStateStopped)
		// The last status is kept (the endpoint is still shown).
		if got := statusOf(t, st, "task_svc"); got == nil || got.Port != 8000 {
			t.Errorf("status = %+v, want the last one kept", got)
		}
	})
}

// A heartbeat without services leaves a stored status untouched and is
// answered exactly as before (no new field in the answer).
func TestHeartbeatServices_AbsentFieldUnchanged(t *testing.T) {
	srv, st := testServerWithStore()
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	seedOwnedTask(t, st, "task_svc", "wrk_svc", true, model.TaskStateRunning)
	heartbeat(t, srv, "wrk_svc", `{"running_tasks":["task_svc"],"services":[{"task_id":"task_svc","state":"ready","port":8000}]}`)
	stored := statusOf(t, st, "task_svc")
	if stored == nil {
		t.Fatal("no status stored (setup)")
	}

	compact := func(raw json.RawMessage) string {
		var b bytes.Buffer
		if err := json.Compact(&b, raw); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	for _, body := range []string{"", "{}", `{"running_tasks":["task_svc"]}`, `{"running_tasks":["task_svc"],"services":null}`} {
		code, env := heartbeatCode(t, srv, "wrk_svc", body)
		if code != http.StatusOK {
			t.Fatalf("heartbeat %q = %d %+v", body, code, env.Error)
		}
		if got, want := compact(env.Data), `{"worker_id":"wrk_svc","state":"online"}`; got != want {
			t.Errorf("heartbeat %q answer = %s, want %s", body, got, want)
		}
		got := statusOf(t, st, "task_svc")
		if got == nil || got.State != stored.State || got.Port != stored.Port || !got.UpdatedAt.Equal(stored.UpdatedAt) ||
			got.ReadyAt == nil || !got.ReadyAt.Equal(*stored.ReadyAt) {
			t.Errorf("after heartbeat %q status = %+v, want unchanged %+v", body, got, stored)
		}
	}

	// A batch worker's answer is unchanged too.
	workerID := registerTestWorker(t, srv)
	code, env := heartbeatCode(t, srv, workerID, `{"running_tasks":[]}`)
	if code != http.StatusOK {
		t.Fatalf("batch heartbeat = %d %+v", code, env.Error)
	}
	if got, want := compact(env.Data), `{"worker_id":"`+workerID+`","state":"online"}`; got != want {
		t.Errorf("batch heartbeat answer = %s, want %s", got, want)
	}
}

// A malformed services value (not a list) from a service-mode worker is
// ignored with a WARN: the heartbeat succeeds.
func TestHeartbeatServices_NotAList(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	resp := heartbeat(t, srv, "wrk_svc", `{"services":{"task_id":"x","state":"ready"}}`)
	if resp.WorkerID != "wrk_svc" {
		t.Errorf("response = %+v", resp)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="heartbeat: ignoring services: not a list"`) {
		t.Errorf("no WARN:\n%s", logs.String())
	}
}

// An entry whose task_id is longer than 128 bytes or holds a byte outside
// 0x21..0x7e is skipped with a WARN (the id logged cut to 128 bytes); the
// other entries of the heartbeat are stored, a 128-byte id included.
func TestHeartbeatServices_InvalidTaskIDSkipped(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	id128 := strings.Repeat("a", 128)
	seedOwnedTask(t, st, "task_ok", "wrk_svc", true, model.TaskStateRunning)
	seedOwnedTask(t, st, id128, "wrk_svc", true, model.TaskStateRunning)
	id129 := strings.Repeat("b", 129)
	withNUL := "task" + string(rune(0)) + "ok"

	body, err := json.Marshal(map[string]any{"services": []map[string]any{
		{"task_id": withNUL, "state": "ready"},
		{"task_id": id129, "state": "ready"},
		{"task_id": "task ok", "state": "ready"},
		{"task_id": "t" + string(rune(0xe9)), "state": "ready"},
		{"task_id": "task_ok", "state": "starting"},
		{"task_id": id128, "state": "unhealthy"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat(t, srv, "wrk_svc", string(body))

	if got := statusOf(t, st, "task_ok"); got == nil || got.State != model.ServiceStateStarting {
		t.Errorf("task_ok status = %+v, want starting", got)
	}
	if got := statusOf(t, st, id128); got == nil || got.State != model.ServiceStateUnhealthy {
		t.Errorf("128-byte id status = %+v, want unhealthy (128 bytes is allowed)", got)
	}
	out := logs.String()
	if n := strings.Count(out, `level=WARN msg="heartbeat: skipping a service status with an invalid task_id"`); n != 4 {
		t.Errorf("invalid task_id WARN lines = %d, want 4:\n%s", n, out)
	}
	if strings.Contains(out, id129) {
		t.Error("the over-long task id is logged in full")
	}
	if !strings.Contains(out, "task_id="+strings.Repeat("b", 128)) {
		t.Errorf("the over-long task id is not logged cut to 128 bytes:\n%s", out)
	}
	// The skipped ids never reach the store (no "ignored" DEBUG line).
	if strings.Contains(out, "service statuses ignored") {
		t.Errorf("a skipped entry reached the store:\n%s", out)
	}
}

// The WARN for an entry that cannot be decoded logs at most 256 bytes of the
// error text, which may quote the reported value; the other entries are
// stored.
func TestHeartbeatServices_UndecodableErrorCut(t *testing.T) {
	srv, st, logs := newStatusServer(t, nil)
	seedGPUServiceWorker(t, st, "wrk_svc", time.Now().UTC())
	seedOwnedTask(t, st, "task_bad", "wrk_svc", true, model.TaskStateRunning)
	seedOwnedTask(t, st, "task_ok", "wrk_svc", true, model.TaskStateRunning)

	huge := strings.Repeat("x", 10<<10)
	body, err := json.Marshal(map[string]any{"services": []map[string]any{
		{"task_id": "task_bad", "state": "ready", "ready_at": huge},
		{"task_id": "task_ok", "state": "starting"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat(t, srv, "wrk_svc", string(body))

	if got := statusOf(t, st, "task_bad"); got != nil {
		t.Errorf("task_bad status = %+v, want none", got)
	}
	if got := statusOf(t, st, "task_ok"); got == nil || got.State != model.ServiceStateStarting {
		t.Errorf("task_ok status = %+v, want starting", got)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "skipping a service status that cannot be decoded") {
			if line != "" {
				t.Fatalf("more than one decode WARN:\n%s", logs.String())
			}
			line = l
		}
	}
	if line == "" || !strings.Contains(line, "level=WARN") || !strings.Contains(line, "task_id=task_bad") {
		t.Fatalf("no decode WARN naming task_bad:\n%.2000s", logs.String())
	}
	if !strings.Contains(line, strings.Repeat("x", 100)) {
		t.Errorf("the WARN does not carry the (cut) error text: %.600s", line)
	}
	if strings.Contains(line, strings.Repeat("x", 257)) || len(line) > 1024 {
		t.Errorf("the WARN carries more than 256 bytes of the error (%d bytes): %.600s", len(line), line)
	}
}
