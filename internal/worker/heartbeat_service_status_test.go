package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// A batch-mode worker's heartbeat (Client.Heartbeat) never carries a
// services key, whatever it reports; only ServiceHeartbeat sends one.
func TestClientHeartbeat_NoServicesKey(t *testing.T) {
	host := &model.HostUsage{Load1: 1, MemTotalMB: 4096, SampledAt: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
	tests := []struct {
		name    string
		running []string
		usage   *model.WorkerUsage
	}{
		{"nothing running, no sample", nil, nil},
		{"empty running list", []string{}, nil},
		{"tasks running", []string{"task_1", "task_2"}, nil},
		{"tasks running with a sample", []string{"task_1"}, &model.WorkerUsage{Host: host}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok","data":{"worker_id":"wrk_1","state":"online"}}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, nil)
			if _, err := c.Heartbeat(context.Background(), tt.running, tt.usage); err != nil {
				t.Fatalf("Heartbeat: %v", err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("heartbeat body %q is not a JSON object: %v", raw, err)
			}
			if _, ok := body["services"]; ok {
				t.Errorf("heartbeat body has a services key: %s", raw)
			}
		})
	}
}
