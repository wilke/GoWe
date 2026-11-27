package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gowe cancel prints the services left stopping, only when there are any.
func TestCancel_ServicesStopping(t *testing.T) {
	const line = "Services stopping: 1 (their GPUs stay charged until the worker confirms)"
	for _, c := range []struct {
		name, data string
		stopping   bool
	}{
		{"a service stopping", `{"id":"sub_1","state":"CANCELLED","tasks_cancelled":2,"services_stopping":1,"tasks_already_completed":1}`, true},
		{"none stopping", `{"id":"sub_1","state":"CANCELLED","tasks_cancelled":2,"services_stopping":0,"tasks_already_completed":1}`, false},
		{"older server", `{"id":"sub_1","state":"CANCELLED","tasks_cancelled":2,"tasks_already_completed":1}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "PUT" || r.URL.Path != "/api/v1/submissions/sub_1/cancel" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"NOT_FOUND","message":"no route"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":`+c.data+`}`)
			}))
			defer srv.Close()

			// gowe cancel prints to os.Stdout.
			_, out, err := runCLIWithStdio(t, "--server", srv.URL, "cancel", "sub_1")
			if err != nil || !strings.Contains(out, "Tasks cancelled: 2") {
				t.Fatalf("cancel: %v\n%s", err, out)
			}
			if got := strings.Contains(out, line); got != c.stopping {
				t.Errorf("output has %q = %v, want %v:\n%s", line, got, c.stopping, out)
			}
		})
	}
}
