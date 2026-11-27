package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// Registration responses: a server that charges at checkout echoes the
// default share; an older one does not.
const (
	regRespCharging = `{"id":"wrk_1","name":"w","default_task_cores":1,"default_task_ram_mb":100}`
	regRespOld      = `{"id":"wrk_1","name":"w"}`
)

func loopPtr[T any](v T) *T { return &v }

// loopTask is a legacy task; charged sets the resource charge a charging
// server attaches at checkout.
func loopTask(id string, charged bool) *model.Task {
	task := &model.Task{ID: id, Inputs: map[string]any{"_base_command": []any{"true"}}}
	if charged {
		task.ChargedCores = loopPtr(1)
		task.ChargedRamMB = loopPtr[int64](100)
	}
	return task
}

// fakeServer is a stub GoWe server for the worker loop: registration,
// heartbeat, checkout from a queue, completion and deregistration, with an
// ordered event log.
type fakeServer struct {
	srv *httptest.Server

	mu           sync.Mutex
	regResp      string
	regBody      map[string]any
	queue        []*model.Task
	events       []string // "checkout:<id>", "checkout:none", "complete:<id>:<state>", "deregister"
	results      map[string]TaskResult
	onDeregister func()
	lastHB       []byte   // body of the last heartbeat
	cancelTasks  []string // returned as cancel_tasks in every heartbeat answer

	// onHeartbeat, when set, sees every heartbeat body; beforeComplete, when
	// set, runs before a completion is recorded and answered (it may block).
	// Set them before the worker starts.
	onHeartbeat    func(body []byte)
	beforeComplete func(taskID string)
}

func newFakeServer(t *testing.T, regResp string, tasks ...*model.Task) *fakeServer {
	t.Helper()
	fs := &fakeServer{regResp: regResp, queue: tasks, results: map[string]TaskResult{}}
	fs.srv = httptest.NewServer(http.HandlerFunc(fs.handle))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/api/v1/workers":
		fs.mu.Lock()
		_ = json.Unmarshal(body, &fs.regBody)
		resp := fs.regResp
		fs.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"status":"ok","data":%s}`, resp)
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/heartbeat"):
		fs.mu.Lock()
		fs.lastHB = body
		cancel := append([]string(nil), fs.cancelTasks...)
		hook := fs.onHeartbeat
		fs.mu.Unlock()
		if hook != nil {
			hook(body)
		}
		if len(cancel) > 0 {
			data, _ := json.Marshal(model.HeartbeatResponse{CancelTasks: cancel})
			fmt.Fprintf(w, `{"status":"ok","data":%s}`, data)
			return
		}
		fmt.Fprint(w, `{"status":"ok","data":{}}`)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/work"):
		fs.mu.Lock()
		if len(fs.queue) == 0 {
			fs.events = append(fs.events, "checkout:none")
			fs.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		task := fs.queue[0]
		fs.queue = fs.queue[1:]
		fs.events = append(fs.events, "checkout:"+task.ID)
		fs.mu.Unlock()
		data, _ := json.Marshal(task)
		fmt.Fprintf(w, `{"status":"ok","data":%s}`, data)
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/complete"):
		var res TaskResult
		_ = json.Unmarshal(body, &res)
		parts := strings.Split(path, "/") // /api/v1/workers/{w}/tasks/{t}/complete
		id := parts[len(parts)-2]
		fs.mu.Lock()
		gate := fs.beforeComplete
		fs.mu.Unlock()
		if gate != nil {
			gate(id)
		}
		fs.mu.Lock()
		fs.results[id] = res
		fs.events = append(fs.events, "complete:"+id+":"+string(res.State))
		fs.mu.Unlock()
		fmt.Fprint(w, `{"status":"ok","data":{}}`)
	case r.Method == http.MethodDelete:
		fs.mu.Lock()
		fs.events = append(fs.events, "deregister")
		hook := fs.onDeregister
		fs.mu.Unlock()
		if hook != nil {
			hook()
		}
		fmt.Fprint(w, `{"status":"ok","data":{}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (fs *fakeServer) eventLog() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.events...)
}

// checkouts counts the tasks handed out so far.
func (fs *fakeServer) checkouts() int {
	n := 0
	for _, e := range fs.eventLog() {
		if strings.HasPrefix(e, "checkout:") && e != "checkout:none" {
			n++
		}
	}
	return n
}

func (fs *fakeServer) result(id string) (TaskResult, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	res, ok := fs.results[id]
	return res, ok
}

func (fs *fakeServer) registration() map[string]any {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.regBody
}

func indexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// blockingRuntime runs each task until it is released or its context is
// cancelled, tracking how many run at once. The task id is the base name of
// the task dir.
type blockingRuntime struct {
	ignoreCtx bool          // keep running after cancellation, until released
	exitDelay time.Duration // delay between cancellation and return (tool grace period)

	mu      sync.Mutex
	gates   map[string]chan struct{}
	running map[string]bool
	maxSeen int
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{gates: map[string]chan struct{}{}, running: map[string]bool{}}
}

func (r *blockingRuntime) gate(id string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gates[id]
	if !ok {
		g = make(chan struct{})
		r.gates[id] = g
	}
	return g
}

func (r *blockingRuntime) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	id := filepath.Base(spec.WorkDir)
	gate := r.gate(id)
	r.mu.Lock()
	r.running[id] = true
	r.maxSeen = max(r.maxSeen, len(r.running))
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
	}()
	if r.ignoreCtx {
		<-gate
		// Like toolexec: a run whose context was cancelled is an error even
		// when the tool itself exited cleanly.
		if ctx.Err() != nil {
			return RunResult{ExitCode: -1}, ctx.Err()
		}
		return RunResult{}, nil
	}
	select {
	case <-gate:
		return RunResult{}, nil
	case <-ctx.Done():
		time.Sleep(r.exitDelay)
		return RunResult{ExitCode: -1}, ctx.Err()
	}
}

func (r *blockingRuntime) release(id string) { close(r.gate(id)) }

func (r *blockingRuntime) isRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[id]
}

func (r *blockingRuntime) runningCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running)
}

func (r *blockingRuntime) maxRunning() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxSeen
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newLoopTestWorker(t *testing.T, url string, rt Runtime, concurrency int, logs io.Writer) *Worker {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	return &Worker{
		client:      NewClient(url, nil),
		runtime:     rt,
		workDir:     t.TempDir(),
		poll:        5 * time.Millisecond,
		active:      newActiveTaskSet(),
		concurrency: concurrency,
		logger:      slog.New(slog.NewTextHandler(logs, nil)),
	}
}

// loopRun is a worker Run in the background.
type loopRun struct {
	t      *testing.T
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

func startWorker(t *testing.T, w *Worker) *loopRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &loopRun{t: t, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- w.Run(ctx, Config{Name: "w", Group: "default", Runtime: "none"}) }()
	t.Cleanup(func() { r.stop() })
	return r
}

// stop cancels the run and waits for Run to return.
func (r *loopRun) stop() error {
	r.once.Do(func() {
		r.cancel()
		select {
		case r.err = <-r.done:
		case <-time.After(10 * time.Second):
			r.t.Error("worker Run did not return after cancellation")
		}
	})
	return r.err
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle lets the loop run several poll intervals.
func settle() { time.Sleep(50 * time.Millisecond) }

func TestTaskLoop_RunsUpToConcurrencyTasks(t *testing.T) {
	var tasks []*model.Task
	for i := 1; i <= 5; i++ {
		tasks = append(tasks, loopTask(fmt.Sprintf("task_%d", i), true))
	}
	fs := newFakeServer(t, regRespCharging, tasks...)
	rt := newBlockingRuntime()
	w := newLoopTestWorker(t, fs.srv.URL, rt, 3, nil)
	run := startWorker(t, w)

	waitFor(t, "3 tasks running", func() bool { return rt.runningCount() == 3 })
	settle()
	if n := rt.runningCount(); n != 3 {
		t.Fatalf("running = %d, want 3", n)
	}
	if n := fs.checkouts(); n != 3 {
		t.Fatalf("checked out %d tasks with 3 slots busy, want 3", n)
	}
	if ids := w.active.ids(); len(ids) != 3 {
		t.Errorf("active ids = %v, want the 3 running tasks", ids)
	}

	// Each released task frees a slot that the loop refills.
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("task_%d", i)
		waitFor(t, id+" running", func() bool { return rt.isRunning(id) })
		rt.release(id)
		waitFor(t, id+" reported", func() bool { _, ok := fs.result(id); return ok })
		if res, _ := fs.result(id); res.State != model.TaskStateSuccess {
			t.Errorf("%s reported %q, want SUCCESS", id, res.State)
		}
	}
	if m := rt.maxRunning(); m != 3 {
		t.Errorf("max tasks running at once = %d, want 3", m)
	}
	if err := run.stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestTaskLoop_ConcurrencyOneChecksOutAfterReport(t *testing.T) {
	fs := newFakeServer(t, regRespOld, loopTask("task_1", false), loopTask("task_2", false))
	rt := newBlockingRuntime()
	w := newLoopTestWorker(t, fs.srv.URL, rt, 1, nil)
	run := startWorker(t, w)

	waitFor(t, "task_1 running", func() bool { return rt.isRunning("task_1") })
	settle()
	ev := fs.eventLog()
	if i := indexOf(ev, "checkout:task_1"); i != len(ev)-1 {
		t.Fatalf("checkout while task_1 runs: events %v", ev)
	}

	rt.release("task_1")
	waitFor(t, "task_2 running", func() bool { return rt.isRunning("task_2") })
	ev = fs.eventLog()
	done, next := indexOf(ev, "complete:task_1:SUCCESS"), indexOf(ev, "checkout:task_2")
	if done < 0 || next < done {
		t.Fatalf("task_2 checked out before task_1 reported: %v", ev)
	}
	if next != done+1 {
		t.Errorf("checkout requests between task_1's checkout and report: %v", ev)
	}
	rt.release("task_2")
	waitFor(t, "task_2 reported", func() bool { _, ok := fs.result("task_2"); return ok })
	if m := rt.maxRunning(); m != 1 {
		t.Errorf("max tasks running at once = %d, want 1", m)
	}
	_ = run.stop()
}

func TestRun_RegistrationBody(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		hostCPUs  int
		hostMemMB int64
		resp      string
		want      map[string]any // nil value = key absent
	}{
		{"concurrency 1 sends no share",
			Config{Resources: ResourceWorkerConfig{MaxCPUs: 8, MaxMemMB: 32000}}, 8, 32000, regRespOld,
			map[string]any{"slots": float64(1), "default_task_cores": nil, "default_task_ram_mb": nil}},
		{"concurrency 1 ignores overrides",
			Config{Concurrency: 1, DefaultTaskCores: 2, DefaultTaskRamMB: 1000, Resources: ResourceWorkerConfig{MaxCPUs: 8, MaxMemMB: 32000}}, 8, 32000, regRespOld,
			map[string]any{"slots": float64(1), "default_task_cores": nil, "default_task_ram_mb": nil}},
		{"concurrency 4 share bounded by host",
			Config{Concurrency: 4, Resources: ResourceWorkerConfig{MaxCPUs: 64, MaxMemMB: 256000}}, 16, 64000, regRespCharging,
			map[string]any{"slots": float64(4), "default_task_cores": float64(4), "default_task_ram_mb": float64(16000)}},
		{"concurrency 4 share bounded by max",
			Config{Concurrency: 4, Resources: ResourceWorkerConfig{MaxCPUs: 8, MaxMemMB: 10000}}, 64, 256000, regRespCharging,
			map[string]any{"slots": float64(4), "default_task_cores": float64(2), "default_task_ram_mb": float64(2500)}},
		{"concurrency 4 with overrides",
			Config{Concurrency: 4, DefaultTaskCores: 3, DefaultTaskRamMB: 8000, Resources: ResourceWorkerConfig{MaxCPUs: 16, MaxMemMB: 64000}}, 16, 64000, regRespCharging,
			map[string]any{"slots": float64(4), "default_task_cores": float64(3), "default_task_ram_mb": float64(8000)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots, cores, ram, err := resolveConcurrency(tt.cfg, tt.hostCPUs, tt.hostMemMB)
			if err != nil {
				t.Fatalf("resolveConcurrency: %v", err)
			}
			fs := newFakeServer(t, tt.resp)
			w := newLoopTestWorker(t, fs.srv.URL, newBlockingRuntime(), slots, nil)
			w.shareCores, w.shareRamMB = cores, ram
			w.resources = tt.cfg.Resources
			w.hostCPUs, w.hostMemMB = tt.hostCPUs, tt.hostMemMB
			run := startWorker(t, w)
			waitFor(t, "registration", func() bool { return fs.registration() != nil })
			_ = run.stop()

			body := fs.registration()
			for key, want := range tt.want {
				got, present := body[key]
				if want == nil {
					if present {
						t.Errorf("%s = %v, want absent", key, got)
					}
					continue
				}
				if got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestResolveConcurrency(t *testing.T) {
	res := func(cpus int, mem int64) ResourceWorkerConfig {
		return ResourceWorkerConfig{MaxCPUs: cpus, MaxMemMB: mem}
	}
	tests := []struct {
		name      string
		cfg       Config
		hostCPUs  int
		hostMemMB int64
		slots     int
		cores     int
		ram       int64
		wantErr   string
	}{
		{name: "unset is one slot", cfg: Config{Resources: res(8, 8000)}, hostCPUs: 8, hostMemMB: 8000, slots: 1},
		{name: "negative refused", cfg: Config{Concurrency: -1, Resources: res(8, 8000)}, wantErr: "at least 1"},
		{name: "above CPUs refused", cfg: Config{Concurrency: 9, Resources: res(8, 8000)}, hostCPUs: 8, wantErr: "exceeds the worker's 8 CPUs"},
		{name: "above host CPUs refused", cfg: Config{Concurrency: 5, Resources: res(32, 8000)}, hostCPUs: 4, wantErr: "exceeds the worker's 4 CPUs"},
		// The GPU inventory is checked in New (validateGPUConcurrency).
		{name: "gpu allowed above 1", cfg: Config{Concurrency: 2, GPU: GPUWorkerConfig{Enabled: true}, Resources: res(8, 8000)}, slots: 2, cores: 4, ram: 4000},
		{name: "gpu allowed at 1", cfg: Config{Concurrency: 1, GPU: GPUWorkerConfig{Enabled: true}, Resources: res(8, 8000)}, slots: 1},
		{name: "cores override above capacity", cfg: Config{Concurrency: 2, DefaultTaskCores: 9, Resources: res(8, 8000)}, wantErr: "--default-task-cores 9"},
		{name: "ram override above capacity", cfg: Config{Concurrency: 2, DefaultTaskRamMB: 9000, Resources: res(8, 8000)}, wantErr: "--default-task-ram 9000"},
		{name: "ram override above host capacity", cfg: Config{Concurrency: 2, DefaultTaskRamMB: 5000, Resources: res(8, 8000)}, hostMemMB: 4000, wantErr: "--default-task-ram 5000"},
		{name: "negative override refused", cfg: Config{Concurrency: 2, DefaultTaskCores: -1, Resources: res(8, 8000)}, wantErr: "negative"},
		{name: "integer division", cfg: Config{Concurrency: 2, Resources: res(5, 8001)}, slots: 2, cores: 2, ram: 4000},
		{name: "at least one core", cfg: Config{Concurrency: 3, Resources: res(3, 900)}, slots: 3, cores: 1, ram: 300},
		{name: "unknown memory gives no ram share", cfg: Config{Concurrency: 2, Resources: res(8, 0)}, slots: 2, cores: 4, ram: 0},
		{name: "overrides replace the share", cfg: Config{Concurrency: 2, DefaultTaskCores: 8, DefaultTaskRamMB: 8000, Resources: res(8, 8000)}, slots: 2, cores: 8, ram: 8000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots, cores, ram, err := resolveConcurrency(tt.cfg, tt.hostCPUs, tt.hostMemMB)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if slots != tt.slots || cores != tt.cores || ram != tt.ram {
				t.Errorf("got slots=%d cores=%d ram=%d, want %d %d %d", slots, cores, ram, tt.slots, tt.cores, tt.ram)
			}
		})
	}
}

func TestValidateConcurrency(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{{0, true}, {-3, true}, {1, false}, {4, false}} {
		if err := ValidateConcurrency(tc.n); (err != nil) != tc.wantErr {
			t.Errorf("ValidateConcurrency(%d) = %v, wantErr %v", tc.n, err, tc.wantErr)
		}
	}
}

func TestTaskShare(t *testing.T) {
	charged := loopTask("t", true)
	w := &Worker{concurrency: 3}
	if c, r := w.taskShare(charged); c != 1 || r != 100 {
		t.Errorf("charged task share = %d/%d, want 1/100", c, r)
	}
	if c, r := w.taskShare(loopTask("t", false)); c != 0 || r != 0 {
		t.Errorf("uncharged task share = %d/%d, want 0/0", c, r)
	}
	zero := &model.Task{ID: "t", ChargedCores: loopPtr(0), ChargedRamMB: loopPtr[int64](0)}
	if c, r := w.taskShare(zero); c != 0 || r != 0 {
		t.Errorf("zero charge share = %d/%d, want 0/0 (whole budget)", c, r)
	}
	if c, r := (&Worker{concurrency: 1}).taskShare(charged); c != 0 || r != 0 {
		t.Errorf("concurrency 1 share = %d/%d, want 0/0", c, r)
	}
	single := &Worker{concurrency: 3}
	single.singleTask.Store(true)
	if c, r := single.taskShare(charged); c != 0 || r != 0 {
		t.Errorf("after fallback share = %d/%d, want 0/0", c, r)
	}
}

func TestRun_OldServerRunsOneTaskAtATime(t *testing.T) {
	fs := newFakeServer(t, regRespOld, loopTask("task_1", true), loopTask("task_2", true), loopTask("task_3", true))
	rt := newBlockingRuntime()
	logs := &syncBuffer{}
	w := newLoopTestWorker(t, fs.srv.URL, rt, 3, logs)
	w.shareCores, w.shareRamMB = 1, 100
	run := startWorker(t, w)

	waitFor(t, "task_1 running", func() bool { return rt.isRunning("task_1") })
	settle()
	if n := fs.checkouts(); n != 1 {
		t.Fatalf("checkouts = %d against an old server, want 1", n)
	}
	if got := fs.registration()["slots"]; got != float64(3) {
		t.Errorf("registered slots = %v, want 3", got)
	}
	if l := w.slotLimit(); l != 1 {
		t.Errorf("slotLimit = %d, want 1", l)
	}
	if c, r := w.taskShare(loopTask("x", true)); c != 0 || r != 0 {
		t.Errorf("share applied against an old server: %d/%d", c, r)
	}
	if !strings.Contains(logs.String(), "server does not account for concurrent tasks") {
		t.Errorf("missing WARN: %s", logs.String())
	}
	for _, id := range []string{"task_1", "task_2", "task_3"} {
		waitFor(t, id+" running", func() bool { return rt.isRunning(id) })
		rt.release(id)
		waitFor(t, id+" reported", func() bool { _, ok := fs.result(id); return ok })
	}
	if m := rt.maxRunning(); m != 1 {
		t.Errorf("max tasks running at once = %d, want 1", m)
	}
	_ = run.stop()
}

func TestTaskLoop_UnchargedTaskDropsToOneSlot(t *testing.T) {
	fs := newFakeServer(t, regRespCharging,
		loopTask("task_1", true), loopTask("task_2", true), loopTask("task_3", false), loopTask("task_4", true))
	rt := newBlockingRuntime()
	logs := &syncBuffer{}
	w := newLoopTestWorker(t, fs.srv.URL, rt, 3, logs)
	run := startWorker(t, w)

	// The first three fill the slots; the third has no charge.
	waitFor(t, "3 tasks running", func() bool { return rt.runningCount() == 3 })
	if l := w.slotLimit(); l != 1 {
		t.Fatalf("slotLimit = %d after an uncharged checkout, want 1", l)
	}
	// Tasks already running continue; no new checkout while any still runs.
	for _, id := range []string{"task_1", "task_2"} {
		rt.release(id)
		waitFor(t, id+" reported", func() bool { _, ok := fs.result(id); return ok })
	}
	settle()
	if n := fs.checkouts(); n != 3 {
		t.Fatalf("checkouts = %d while task_3 still runs at one slot, want 3", n)
	}
	rt.release("task_3")
	waitFor(t, "task_4 running", func() bool { return rt.isRunning("task_4") })
	rt.release("task_4")
	waitFor(t, "task_4 reported", func() bool { _, ok := fs.result("task_4"); return ok })
	if n := strings.Count(logs.String(), "checked-out task carries no resource charge"); n != 1 {
		t.Errorf("guard WARN logged %d times, want once", n)
	}
	_ = run.stop()
}

func TestTaskLoop_ShutdownWaitsForTasksThenDeregisters(t *testing.T) {
	fs := newFakeServer(t, regRespCharging, loopTask("task_1", true), loopTask("task_2", true))
	rt := newBlockingRuntime()
	rt.exitDelay = 30 * time.Millisecond // tools take a moment to die
	w := newLoopTestWorker(t, fs.srv.URL, rt, 2, nil)
	var activeAtDereg, runningAtDereg atomic.Int32
	activeAtDereg.Store(-1)
	fs.mu.Lock()
	fs.onDeregister = func() {
		activeAtDereg.Store(int32(len(w.active.ids())))
		runningAtDereg.Store(int32(rt.runningCount()))
	}
	fs.mu.Unlock()
	run := startWorker(t, w)

	waitFor(t, "2 tasks running", func() bool { return rt.runningCount() == 2 })
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := fs.eventLog()
	if indexOf(ev, "deregister") < 0 {
		t.Fatalf("no deregister: %v", ev)
	}
	if a, r := activeAtDereg.Load(), runningAtDereg.Load(); a != 0 || r != 0 {
		t.Errorf("at deregister: %d active task goroutines, %d tools running; want 0/0", a, r)
	}
	for _, e := range ev {
		if strings.HasPrefix(e, "complete:") {
			t.Errorf("interrupted task reported: %s (want left non-terminal for requeue)", e)
		}
	}
}

// A server that requeued a task this worker is still running (missed
// heartbeats) can hand the same task back: the worker must not start a second
// run in the same task dir.
func TestTaskLoop_DoesNotRunAnActiveTaskTwice(t *testing.T) {
	fs := newFakeServer(t, regRespCharging, loopTask("task_1", true), loopTask("task_1", true))
	rt := newBlockingRuntime()
	logs := &syncBuffer{}
	w := newLoopTestWorker(t, fs.srv.URL, rt, 2, logs)
	run := startWorker(t, w)

	waitFor(t, "task_1 handed out twice", func() bool { return fs.checkouts() >= 2 })
	waitFor(t, "duplicate refused", func() bool {
		return strings.Contains(logs.String(), "already running")
	})
	if n := rt.maxRunning(); n != 1 {
		t.Errorf("max concurrent runs = %d, want 1", n)
	}
	rt.release("task_1")
	waitFor(t, "task_1 reported", func() bool { _, ok := fs.result("task_1"); return ok })
	waitFor(t, "task_1 goroutine done", func() bool { return len(w.active.ids()) == 0 })
	n := 0
	for _, e := range fs.eventLog() {
		if strings.HasPrefix(e, "complete:task_1") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("task_1 reported %d times, want 1", n)
	}
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestTaskLoop_ShutdownWaitIsBounded(t *testing.T) {
	orig := shutdownTaskWait
	shutdownTaskWait = 50 * time.Millisecond
	t.Cleanup(func() { shutdownTaskWait = orig })

	fs := newFakeServer(t, regRespOld, loopTask("task_1", false))
	rt := newBlockingRuntime()
	rt.ignoreCtx = true // a tool that does not die
	logs := &syncBuffer{}
	w := newLoopTestWorker(t, fs.srv.URL, rt, 1, logs)
	run := startWorker(t, w)

	waitFor(t, "task_1 running", func() bool { return rt.isRunning("task_1") })
	_ = run.stop()
	if indexOf(fs.eventLog(), "deregister") < 0 {
		t.Fatalf("no deregister after the bounded wait: %v", fs.eventLog())
	}
	out := logs.String()
	if !strings.Contains(out, "tasks still running after the shutdown wait") || !strings.Contains(out, "task_1") {
		t.Errorf("missing WARN naming task_1: %s", out)
	}

	// The straggler finishes after shutdown: still not reported.
	rt.release("task_1")
	waitFor(t, "task_1 goroutine done", func() bool { return len(w.active.ids()) == 0 })
	if _, ok := fs.result("task_1"); ok {
		t.Error("task finishing after shutdown was reported")
	}
}

// blockingStager blocks in StageIn or StageOut until its context is done.
type blockingStager struct {
	blockIn, blockOut bool
	entered           chan struct{}
	once              sync.Once
}

func (s *blockingStager) block(ctx context.Context) error {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingStager) StageIn(ctx context.Context, _, _ string, _ staging.StageOptions) error {
	if s.blockIn {
		return s.block(ctx)
	}
	return nil
}

func (s *blockingStager) StageOut(ctx context.Context, src, _ string, _ staging.StageOptions) (string, error) {
	if s.blockOut {
		return "", s.block(ctx)
	}
	return "file://" + src, nil
}

func (s *blockingStager) Supports(string) bool { return true }

func TestExecuteWithCWLTool_CancelDuringStaging(t *testing.T) {
	stageInTask := func() *model.Task {
		return &model.Task{ID: "task_si", Job: map[string]any{
			"f": map[string]any{"class": "File", "location": "http://example.invalid/in.txt"},
		}, Tool: map[string]any{
			"class": "CommandLineTool", "cwlVersion": "v1.2",
			"baseCommand": []any{"true"},
			"inputs":      map[string]any{"f": map[string]any{"type": "File"}},
			"outputs":     map[string]any{},
		}}
	}
	stageOutTask := func() *model.Task {
		return &model.Task{ID: "task_so", Job: map[string]any{}, Tool: map[string]any{
			"class": "CommandLineTool", "cwlVersion": "v1.2",
			"baseCommand": []any{"sh", "-c", "echo x > out.txt"},
			"inputs":      map[string]any{},
			"outputs": map[string]any{
				"o": map[string]any{"type": "File", "outputBinding": map[string]any{"glob": "out.txt"}},
			},
		}}
	}
	tests := []struct {
		name      string
		task      func() *model.Task
		stager    *blockingStager
		serverCxl bool // explicit server cancel instead of worker shutdown
		wantState model.TaskState
	}{
		{"stage-in, shutdown", stageInTask, &blockingStager{blockIn: true}, false, ""},
		{"stage-in, server cancel", stageInTask, &blockingStager{blockIn: true}, true, model.TaskStateSkipped},
		{"stage-out, shutdown", stageOutTask, &blockingStager{blockOut: true}, false, ""},
		{"stage-out, server cancel", stageOutTask, &blockingStager{blockOut: true}, true, model.TaskStateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var reports []TaskResult
			ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				var res TaskResult
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &res)
				mu.Lock()
				reports = append(reports, res)
				mu.Unlock()
				_, _ = rw.Write([]byte(`{"status":"ok"}`))
			}))
			defer ts.Close()

			w := newCWLTestWorker(t, "", io.Discard)
			w.containerRuntime = "none"
			tt.stager.entered = make(chan struct{})
			w.stager = tt.stager
			w.client = NewClient(ts.URL, nil)
			w.client.workerID = "wrk_test"
			task := tt.task()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- w.executeTask(ctx, task) }()
			select {
			case <-tt.stager.entered:
			case err := <-done:
				t.Fatalf("task finished before reaching the stager: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("stager never reached")
			}
			if tt.serverCxl {
				if !w.active.cancel(task.ID) {
					t.Fatal("task not active")
				}
			} else {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("task did not return after cancellation")
			}

			mu.Lock()
			defer mu.Unlock()
			if tt.wantState == "" {
				if len(reports) != 0 {
					t.Errorf("reports on shutdown = %+v, want none (left for requeue)", reports)
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v, want context.Canceled", err)
				}
				return
			}
			if len(reports) != 1 || reports[0].State != tt.wantState {
				t.Errorf("reports = %+v, want one %s", reports, tt.wantState)
			}
		})
	}
}

// File literals staged by two tasks with the same basename and different
// contents land in each task's own staging dir.
func TestStageRemoteInputs_FileLiteralsAreTaskPrivate(t *testing.T) {
	root := t.TempDir()
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	literal := func(contents string) map[string]any {
		// Inside a record: RematerializeRecursive does not reach it, so
		// stage-in materializes it.
		return map[string]any{"rec": map[string]any{
			"f": map[string]any{"class": "File", "basename": "conf.txt", "contents": contents},
		}}
	}
	inA, inB := literal("A"), literal("B")
	dirA, dirB := filepath.Join(root, "task_a"), filepath.Join(root, "task_b")
	if err := stageRemoteInputs(context.Background(), nil, inA, dirA, lg); err != nil {
		t.Fatal(err)
	}
	if err := stageRemoteInputs(context.Background(), nil, inB, dirB, lg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in   map[string]any
		dir  string
		want string
	}{{inA, dirA, "A"}, {inB, dirB, "B"}} {
		f := tc.in["rec"].(map[string]any)["f"].(map[string]any)
		p, _ := f["path"].(string)
		if !strings.HasPrefix(p, tc.dir+"_staging"+string(filepath.Separator)) {
			t.Errorf("literal path %q not under %s_staging", p, tc.dir)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != tc.want {
			t.Errorf("%s = %q (err %v), want %q", p, b, err, tc.want)
		}
	}
}

func TestCleanupTaskDir_KeepsDirWhenOutputsReferenceStaging(t *testing.T) {
	workDir := t.TempDir()
	taskDir := filepath.Join(workDir, "task_st")
	for _, d := range []string{taskDir, taskDir + "_staging"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w := &Worker{workDir: workDir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.cleanupTaskDir(&model.Task{ID: "task_st"}, map[string]any{
		"out": map[string]any{"class": "File", "location": "file://" + filepath.Join(taskDir+"_staging", "cwl-literals", "x")},
	})
	if _, err := os.Stat(taskDir + "_staging"); err != nil {
		t.Errorf("staging dir referenced by an output was deleted: %v", err)
	}
}
