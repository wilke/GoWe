package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// forceReleaseServer fakes the force-release route and records requests;
// task task_live is refused (409).
func forceReleaseServer(t *testing.T) (*httptest.Server, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.RequestURI(), nil})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/submissions/sub_1/tasks/task_live/force-release":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"CONFLICT","message":"the service's worker is online; cancel the submission, then wait 15 minutes or until the worker is marked lost"}}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/force-release"):
			_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":{"id":"task_svc","state":"FAILED","external_id":"wrk_lost","completed_at":"2026-01-01T00:00:00Z","worker_notified":"on its next heartbeat, if it comes back"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"NOT_FOUND","message":"no route"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), reqs...)
	}
}

func TestAdminForceReleaseService(t *testing.T) {
	srv, reqs := forceReleaseServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "force-release-service", "sub_1", "task_svc")
	if err != nil || !strings.Contains(out, "Force-released service task task_svc (now FAILED; worker wrk_lost)") ||
		!strings.Contains(out, "The worker is told to stop it: on its next heartbeat, if it comes back.") {
		t.Fatalf("force-release-service: %v\n%s", err, out)
	}
	if last := reqs()[len(reqs())-1]; last.Method != "POST" || last.Path != "/api/v1/submissions/sub_1/tasks/task_svc/force-release" {
		t.Errorf("request = %+v", last)
	}

	// The server's refusal reaches the user.
	_, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "force-release-service", "sub_1", "task_live")
	if err == nil || !strings.Contains(err.Error(), "then wait 15 minutes or until the worker is marked lost") {
		t.Errorf("refused release: %v, want the server's 409 message", err)
	}

	// Invalid arguments make no request.
	n := len(reqs())
	for _, args := range [][]string{{"sub_1"}, {"sub_1", ""}, {"", "task_svc"}, {"a", "b", "c"}} {
		if _, err := runBlessCLI(t, "", append([]string{"--server", srv.URL, "admin", "force-release-service"}, args...)...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(reqs()) != n {
		t.Errorf("refused arguments made requests: %+v", reqs()[n:])
	}
}
