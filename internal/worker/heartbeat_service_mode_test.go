package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// heartbeatRecorder is a stub server that keeps the last heartbeat body.
type heartbeatRecorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body []byte
}

func newHeartbeatRecorder(t *testing.T) *heartbeatRecorder {
	t.Helper()
	hr := &heartbeatRecorder{}
	hr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hr.mu.Lock()
		hr.body = raw
		hr.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"worker_id":"wrk_1","state":"online"}}`))
	}))
	t.Cleanup(hr.srv.Close)
	return hr
}

func (hr *heartbeatRecorder) last() string {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	return string(hr.body)
}

// The batch heartbeat body is byte for byte what it was before service mode:
// the literals below are the encoding of model.HeartbeatRequest as sent by
// earlier workers.
func TestHeartbeat_BatchBodyUnchanged(t *testing.T) {
	sampled := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	usage := &model.WorkerUsage{Host: &model.HostUsage{Load1: 1.5, MemTotalMB: 4096, MemAvailableMB: 1024, SampledAt: sampled}}
	tests := []struct {
		name    string
		running []string // tasks registered in w.active
		usage   *model.WorkerUsage
		want    string
	}{
		{"nothing running", nil, nil, `{}`},
		{"one task", []string{"task_1"}, nil, `{"running_tasks":["task_1"]}`},
		{"two tasks, sorted", []string{"task_2", "task_1"}, nil, `{"running_tasks":["task_1","task_2"]}`},
		{"with a usage sample", []string{"task_1"}, usage,
			`{"running_tasks":["task_1"],"host":{"load1":1.5,"load5":0,"mem_total_mb":4096,"mem_available_mb":1024,"sampled_at":"2026-11-27T10:00:00Z"}}`},
		{"usage, nothing running", nil, usage,
			`{"host":{"load1":1.5,"load5":0,"mem_total_mb":4096,"mem_available_mb":1024,"sampled_at":"2026-11-27T10:00:00Z"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := newHeartbeatRecorder(t)
			w := &Worker{client: NewClient(hr.srv.URL, nil), active: newActiveTaskSet(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			for _, id := range tt.running {
				w.active.add(id, func() {})
			}
			if _, err := w.sendHeartbeat(context.Background(), tt.usage); err != nil {
				t.Fatalf("sendHeartbeat: %v", err)
			}
			if got := hr.last(); got != tt.want {
				t.Errorf("body = %s\nwant   %s", got, tt.want)
			}
		})
	}
}

func TestHeartbeat_ServiceModeBody(t *testing.T) {
	hr := newHeartbeatRecorder(t)
	w := &Worker{
		client:        NewClient(hr.srv.URL, nil),
		active:        newActiveTaskSet(),
		serviceMode:   true,
		advertiseHost: "gpu01.example.org",
		services:      newServiceRegistry(20000, 20009),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.services.canBind = func(int) bool { return true }

	// Nothing running: both lists are sent, empty.
	if _, err := w.sendHeartbeat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got, want := hr.last(), `{"running_tasks":[],"services":[]}`; got != want {
		t.Errorf("empty service heartbeat = %s, want %s", got, want)
	}

	// One service in preparation (no entry yet), one ready.
	w.active.add("task_prep", func() {})
	w.active.add("task_svc", func() {})
	readyAt := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	port, err := w.services.reserve("task_svc", &serviceEntry{
		scheme:      "http",
		stopGrace:   30 * time.Second,
		gpus:        []model.ServiceGPU{{UUID: "GPU-a", Index: 2}},
		imageSHA256: "ab",
	})
	if err != nil || port != 20000 {
		t.Fatalf("reserve = %d, %v", port, err)
	}
	w.services.update("task_svc", func(e *serviceEntry) {
		e.pid = 4242
		e.state = model.ServiceStateReady
		e.readyAt = &readyAt
	})
	if _, err := w.sendHeartbeat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		RunningTasks []string              `json:"running_tasks"`
		Services     []model.ServiceReport `json:"services"`
	}
	if err := json.Unmarshal([]byte(hr.last()), &body); err != nil {
		t.Fatalf("body %s: %v", hr.last(), err)
	}
	if len(body.RunningTasks) != 2 || body.RunningTasks[0] != "task_prep" || body.RunningTasks[1] != "task_svc" {
		t.Errorf("running_tasks = %v", body.RunningTasks)
	}
	if len(body.Services) != 1 {
		t.Fatalf("services = %+v, want one", body.Services)
	}
	s := body.Services[0]
	if s.TaskID != "task_svc" || s.State != model.ServiceStateReady || s.Host != "gpu01.example.org" || s.Port != 20000 ||
		s.Scheme != "http" || s.PID != 4242 || s.ImageSHA256 != "ab" || s.ReadyAt == nil || !s.ReadyAt.Equal(readyAt) ||
		len(s.GPUs) != 1 || s.GPUs[0].UUID != "GPU-a" || s.GPUs[0].Index != 2 {
		t.Errorf("service entry = %+v", s)
	}
}

func TestServiceRegistry_Ports(t *testing.T) {
	r := newServiceRegistry(30000, 30003)
	busy := map[int]bool{30000: true} // bound by something else
	r.canBind = func(p int) bool { return !busy[p] }

	a, err := r.reserve("a", &serviceEntry{})
	if err != nil || a != 30001 {
		t.Fatalf("a = %d, %v; want 30001 (30000 cannot be bound)", a, err)
	}
	b, err := r.reserve("b", &serviceEntry{})
	if err != nil || b != 30002 {
		t.Fatalf("b = %d, %v; want 30002 (30001 is a's)", b, err)
	}
	r.remove("a")
	c, err := r.reserve("c", &serviceEntry{})
	if err != nil || c != 30001 {
		t.Fatalf("c = %d, %v; want 30001 (released by a)", c, err)
	}
	if _, err := r.reserve("d", &serviceEntry{}); err != nil {
		t.Fatalf("d: %v", err)
	}
	if _, err := r.reserve("e", &serviceEntry{}); err == nil {
		t.Error("reserve beyond the range succeeded")
	}
}

func TestHealthTracker(t *testing.T) {
	tr := newHealthTracker(2)
	now := time.Now()
	steps := []struct {
		ok      bool
		state   string
		changed bool
	}{
		{false, model.ServiceStateStarting, false},
		{true, model.ServiceStateReady, true},
		{false, model.ServiceStateReady, false},
		{false, model.ServiceStateUnhealthy, true},
		{false, model.ServiceStateUnhealthy, false},
		{true, model.ServiceStateReady, true},
		{false, model.ServiceStateReady, false},
	}
	for i, s := range steps {
		state, changed := tr.observe(s.ok, now.Add(time.Duration(i)*time.Second))
		if state != s.state || changed != s.changed {
			t.Errorf("step %d: %s/%v, want %s/%v", i, state, changed, s.state, s.changed)
		}
	}
	if tr.readyAt == nil || !tr.readyAt.Equal(now.Add(time.Second)) {
		t.Errorf("readyAt = %v, want the first ready check", tr.readyAt)
	}
}

func TestServiceDetail(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	if got := serviceDetail(string(long)); len(got) != model.MaxServiceDetailLen {
		t.Errorf("len = %d, want %d", len(got), model.MaxServiceDetailLen)
	}
	if got := serviceDetail("a\nb\tc"); got != "a b c" {
		t.Errorf("control characters: %q", got)
	}
	st := model.ServiceStatus{State: model.ServiceStateStarting, Detail: serviceDetail(string(long[:510]) + "é€")}
	if err := st.Validate(time.Now()); err != nil {
		t.Errorf("cut detail refused by the server's check: %v", err)
	}
}
