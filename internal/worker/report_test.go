package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// newReportTestWorker builds a minimal Worker wired to the given server URL,
// enough to exercise the report path.
func newReportTestWorker(url string) *Worker {
	c := NewClient(url, nil)
	c.workerID = "wrk_test"
	return &Worker{
		client: c,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// shrinkBackoff makes report retries near-instant for the duration of a test.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	orig := reportRetryBackoff
	reportRetryBackoff = time.Millisecond
	t.Cleanup(func() { reportRetryBackoff = orig })
}

func TestReportComplete_ConflictNotRetried(t *testing.T) {
	shrinkBackoff(t)
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"status":"error","error":{"code":"CONFLICT","message":"task is already terminal"}}`))
	}))
	defer ts.Close()

	w := newReportTestWorker(ts.URL)
	err := w.reportComplete(context.Background(), "task_1", TaskResult{State: model.TaskStateSuccess})

	if !errors.Is(err, errReportDeclined) {
		t.Fatalf("err = %v, want errReportDeclined (callers must skip cleanup so the task dir is kept)", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (a 409 must never be retried)", n)
	}
}

func TestReportComplete_ServerErrorRetriedThenGivesUp(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"500 internal server error", http.StatusInternalServerError},
		{"429 too many requests", http.StatusTooManyRequests},
		{"408 request timeout", http.StatusRequestTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkBackoff(t)
			var requests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tt.statusCode)
			}))
			defer ts.Close()

			w := newReportTestWorker(ts.URL)
			err := w.reportComplete(context.Background(), "task_1", TaskResult{State: model.TaskStateSuccess})

			if err == nil {
				t.Fatal("expected error after exhausting retries")
			}
			if errors.Is(err, errReportDeclined) {
				t.Fatalf("err = %v, must not be classified as declined", err)
			}
			if n := requests.Load(); n != 3 {
				t.Errorf("requests = %d, want 3 (transient errors retried up to 3 attempts)", n)
			}
		})
	}
}

func TestReportComplete_TransientErrorThenSuccess(t *testing.T) {
	shrinkBackoff(t)
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"ok","data":{"task_id":"task_1","state":"SUCCESS"}}`))
	}))
	defer ts.Close()

	w := newReportTestWorker(ts.URL)
	if err := w.reportComplete(context.Background(), "task_1", TaskResult{State: model.TaskStateSuccess}); err != nil {
		t.Fatalf("reportComplete: %v", err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry after a 503)", n)
	}
}

func TestReportComplete_OtherClientErrorNotRetried(t *testing.T) {
	shrinkBackoff(t)
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"status":"error","error":{"code":"NOT_FOUND","message":"task not found"}}`))
	}))
	defer ts.Close()

	w := newReportTestWorker(ts.URL)
	err := w.reportComplete(context.Background(), "task_1", TaskResult{State: model.TaskStateSuccess})

	if err == nil || errors.Is(err, errReportDeclined) {
		t.Fatalf("err = %v, want a plain error (404 is neither declined nor transient)", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (4xx other than 409 not retried)", n)
	}
}

// TestReportCancelled_ConflictIsExpected: after a cancel fan-out the server
// has usually already SKIPPED the task; its 409 to the worker's SKIPPED
// report is the routine outcome and must not surface as an error.
func TestReportCancelled_ConflictIsExpected(t *testing.T) {
	shrinkBackoff(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"status":"error","error":{"code":"CONFLICT","message":"task is already terminal (SKIPPED)"}}`))
	}))
	defer ts.Close()

	w := newReportTestWorker(ts.URL)
	if err := w.reportCancelled(context.Background(), &model.Task{ID: "task_1"}); err != nil {
		t.Fatalf("reportCancelled: %v, want nil (routine 409 after cancel fan-out)", err)
	}
}
