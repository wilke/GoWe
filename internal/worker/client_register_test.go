package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRegister_SendsCapacity(t *testing.T) {
	tests := []struct {
		name     string
		opts     RegisterOptions
		wantCPUs any
		wantMem  any
	}{
		{"both reported", RegisterOptions{MaxCPUs: 16, MaxMemMB: 32000}, float64(16), float64(32000)},
		{"zero values omitted", RegisterOptions{}, nil, nil},
		{"only cpus", RegisterOptions{MaxCPUs: 4}, float64(4), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"ok","data":{"id":"wrk_1","name":"n"}}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, nil)
			if _, err := c.Register(context.Background(), "n", "h", "default", "none", tt.opts); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if got := body["max_cpus"]; got != tt.wantCPUs {
				t.Errorf("max_cpus = %v, want %v", got, tt.wantCPUs)
			}
			if got := body["max_mem_mb"]; got != tt.wantMem {
				t.Errorf("max_mem_mb = %v, want %v", got, tt.wantMem)
			}
		})
	}
}
