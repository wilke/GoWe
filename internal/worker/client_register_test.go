package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/me/gowe/pkg/model"
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

func TestClientRegister_SendsHostInventory(t *testing.T) {
	yes, no := true, false
	allAbsent := map[string]any{"host_id": nil, "host_cpus": nil, "host_mem_mb": nil, "slots": nil,
		"cgroup_cpu": nil, "cgroup_memory": nil, "gpus": nil}
	tests := []struct {
		name string
		opts RegisterOptions
		want map[string]any // expected JSON value per key; nil = key absent
	}{
		{"all reported", RegisterOptions{
			HostID:       "0123456789abcdef",
			HostCPUs:     192,
			HostMemMB:    1547000,
			Slots:        1,
			CgroupCPU:    &yes,
			CgroupMemory: &no,
			GPUs:         []model.GPUInfo{{Index: 0, UUID: "GPU-a", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}},
		}, map[string]any{
			"host_id":       "0123456789abcdef",
			"host_cpus":     float64(192),
			"host_mem_mb":   float64(1547000),
			"slots":         float64(1),
			"cgroup_cpu":    true,
			"cgroup_memory": false,
			"gpus":          []any{map[string]any{"index": float64(0), "uuid": "GPU-a", "name": "NVIDIA H200 NVL", "mem_total_mb": float64(143771)}},
		}},
		{"zero values omitted", RegisterOptions{}, allAbsent},
		{"empty gpu list sent", RegisterOptions{GPUs: []model.GPUInfo{}}, map[string]any{"gpus": []any{}}},
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
			for key, want := range tt.want {
				got, present := body[key]
				if want == nil {
					if present {
						t.Errorf("%s = %v, want absent", key, got)
					}
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}
