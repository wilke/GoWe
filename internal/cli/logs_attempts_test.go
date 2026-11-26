package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestWriteTaskAttempts(t *testing.T) {
	started := time.Date(2026, 11, 20, 10, 0, 5, 0, time.UTC)
	exit := 137
	var b bytes.Buffer
	writeTaskAttempts(&b, "task_1", []model.TaskAttempt{
		{TaskID: "task_1", Attempt: 1, Reason: model.AttemptReasonRetry, ExitCode: &exit, ExecutorType: "worker",
			ExternalID: "wrk_1", WorkerName: "node-1", StartedAt: &started, Stderr: "Killed"},
		{TaskID: "task_1", Attempt: 2, Reason: model.AttemptReasonWorkerOffline, ExecutorType: "worker", ExternalID: "wrk_2"},
		// Never checked out: a queued worker task's external_id is its own id.
		{TaskID: "task_1", Attempt: 3, Reason: model.AttemptReasonRetry, ExecutorType: "worker", ExternalID: "task_1"},
		// Not a worker task: external_id is not a worker.
		{TaskID: "task_1", Attempt: 4, Reason: model.AttemptReasonReset, ExecutorType: "bvbrc", ExternalID: "job-123"},
	})
	out := b.String()
	for _, want := range []string{
		"--- task_1 attempt 1: reason retry, exit 137, worker node-1, 2026-11-20T10:00:05Z to - ---\n[stderr]\nKilled\n",
		"--- task_1 attempt 2: reason worker_offline, exit -, worker wrk_2, - to - ---\n",
		"--- task_1 attempt 3: reason retry, exit -, worker -, - to - ---\n",
		"--- task_1 attempt 4: reason reset, exit -, worker -, - to - ---\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}

	b.Reset()
	writeTaskAttempts(&b, "task_2", nil)
	if !strings.Contains(b.String(), "task_2: no earlier attempts") {
		t.Errorf("output for no attempts = %q", b.String())
	}
}

// logsServer fakes the task list and task logs routes of submission sub_1
// (two tasks) and answers the attempts route with attemptsStatus: 404 as a
// server without the route does (plain text), anything else as a JSON error.
// It records the request paths.
func logsServer(t *testing.T, attemptsStatus int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		write := func(data string) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":`+data+`}`)
		}
		p := r.URL.Path
		switch {
		case p == "/api/v1/submissions/sub_1/tasks/":
			write(`[{"id":"task_a","step_id":"a"},{"id":"task_b","step_id":"b"}]`)
		case strings.HasSuffix(p, "/logs"):
			write(`{"step_id":"x","stdout":"","stderr":"","exit_code":0}`)
		case strings.HasSuffix(p, "/attempts") && attemptsStatus == http.StatusNotFound:
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/attempts"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(attemptsStatus)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"INTERNAL","message":"database is locked"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// Against a server without the attempts route, --attempts prints one notice
// and the logs of every task still follow, with exit status 0.
func TestLogsAttempts_ServerWithoutRoute(t *testing.T) {
	t.Run("all tasks", func(t *testing.T) {
		srv, paths := logsServer(t, http.StatusNotFound)
		out, err := runBlessCLI(t, "", "--server", srv.URL, "logs", "sub_1", "--attempts")
		if err != nil {
			t.Fatalf("logs --attempts: %v\n%s", err, out)
		}
		if n := strings.Count(out, noAttemptHistory); n != 1 {
			t.Errorf("notice printed %d times, want once:\n%s", n, out)
		}
		got := strings.Join(paths(), "\n")
		if !strings.Contains(got, "/api/v1/submissions/sub_1/tasks/task_b/logs") {
			t.Errorf("the second task's logs were not requested:\n%s", got)
		}
	})
	t.Run("one task", func(t *testing.T) {
		srv, _ := logsServer(t, http.StatusNotFound)
		out, err := runBlessCLI(t, "", "--server", srv.URL, "logs", "sub_1", "--task", "task_a", "--attempts")
		if err != nil {
			t.Fatalf("logs --task --attempts: %v\n%s", err, out)
		}
		if n := strings.Count(out, noAttemptHistory); n != 1 {
			t.Errorf("notice printed %d times, want once:\n%s", n, out)
		}
	})
}

// Any other failure of the attempts route is still an error.
func TestLogsAttempts_OtherErrorReported(t *testing.T) {
	srv, _ := logsServer(t, http.StatusInternalServerError)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "logs", "sub_1", "--attempts")
	if err == nil || !strings.Contains(err.Error(), "get attempts") || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("err = %v, want the attempts error\n%s", err, out)
	}
	if strings.Contains(out, noAttemptHistory) {
		t.Errorf("a 500 must not be reported as a missing route:\n%s", out)
	}
}
