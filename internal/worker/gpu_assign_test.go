package worker

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// gpuRecordingRuntime is a blockingRuntime that records the GPU configuration
// each task reached the runtime layer with.
type gpuRecordingRuntime struct {
	*blockingRuntime
	mu   sync.Mutex
	gpus map[string]GPUConfig
}

func newGPURecordingRuntime() *gpuRecordingRuntime {
	return &gpuRecordingRuntime{blockingRuntime: newBlockingRuntime(), gpus: map[string]GPUConfig{}}
}

func (r *gpuRecordingRuntime) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	r.mu.Lock()
	r.gpus[filepath.Base(spec.WorkDir)] = spec.GPU
	r.mu.Unlock()
	return r.blockingRuntime.Run(ctx, spec)
}

func (r *gpuRecordingRuntime) gpuOf(id string) (GPUConfig, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gpus[id]
	return g, ok
}

// gpuLoopTask is a charged legacy task needing gpus GPUs, with the GPU charge
// chargedGPUs (nil = a server that does not count GPUs).
func gpuLoopTask(id string, gpus int, chargedGPUs *int) *model.Task {
	task := loopTask(id, true)
	task.RuntimeHints = &model.RuntimeHints{RequiresGPU: gpus > 0, GPUs: gpus}
	task.ChargedGPUs = chargedGPUs
	return task
}

// newGPULoopWorker is a --gpu loop test worker with a fake inventory of
// devices GPUs; above one slot it assigns devices per task. The inventory is
// given to the ledger only (gpuInventory stays nil), so the usage sampler
// started by Run never runs nvidia-smi.
func newGPULoopWorker(t *testing.T, url string, rt Runtime, concurrency, devices int, logs io.Writer) *Worker {
	t.Helper()
	w := newLoopTestWorker(t, url, rt, concurrency, logs)
	w.gpu = GPUWorkerConfig{Enabled: true}
	if concurrency > 1 {
		w.gpuLedger = newGPULedger(fakeGPUs(devices))
	}
	return w
}

// shortGPUWait sets gpuAcquireWait for one test. Call it before startWorker
// so the restore runs after the worker stopped.
func shortGPUWait(t *testing.T, d time.Duration) {
	t.Helper()
	orig := gpuAcquireWait
	gpuAcquireWait = d
	t.Cleanup(func() { gpuAcquireWait = orig })
}

func TestTaskLoop_GPUWorkerGivesEachTaskItsOwnDevices(t *testing.T) {
	fs := newFakeServer(t, regRespCharging,
		gpuLoopTask("g1", 1, loopPtr(1)), gpuLoopTask("g2", 1, loopPtr(1)), gpuLoopTask("cpu", 0, loopPtr(0)))
	rt := newGPURecordingRuntime()
	w := newGPULoopWorker(t, fs.srv.URL, rt, 3, 2, nil)
	run := startWorker(t, w)

	waitFor(t, "3 tasks running", func() bool { return rt.runningCount() == 3 })
	g1, _ := rt.gpuOf("g1")
	g2, _ := rt.gpuOf("g2")
	cpu, _ := rt.gpuOf("cpu")
	for name, g := range map[string]GPUConfig{"g1": g1, "g2": g2} {
		if len(g.Devices) != 1 || !g.HasAccess() || g.AccessDenied {
			t.Errorf("%s GPU config = %+v, want exactly one assigned device", name, g)
		}
	}
	if len(g1.Devices) == 1 && len(g2.Devices) == 1 && g1.Devices[0] == g2.Devices[0] {
		t.Errorf("g1 and g2 share device %s", g1.Devices[0])
	}
	if !cpu.AccessDenied || len(cpu.Devices) != 0 || cpu.HasAccess() {
		t.Errorf("cpu GPU config = %+v, want GPU access denied", cpu)
	}

	for _, id := range []string{"g1", "g2", "cpu"} {
		rt.release(id)
		waitFor(t, id+" reported", func() bool { _, ok := fs.result(id); return ok })
		if res, _ := fs.result(id); res.State != model.TaskStateSuccess {
			t.Errorf("%s reported %q, want SUCCESS", id, res.State)
		}
	}
	waitFor(t, "task goroutines done", func() bool { return len(w.active.ids()) == 0 })
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Every device was released.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := w.gpuLedger.acquire(ctx, 2, nil); err != nil || len(got) != 2 {
		t.Errorf("acquire(2) after all tasks ended = (%v, %v), want both devices free", got, err)
	}
}

func TestTaskLoop_GPUWaitTimeoutFailsTask(t *testing.T) {
	shortGPUWait(t, 50*time.Millisecond)
	// The server charged g2 although g1 still holds the only device (its
	// charge was released early): g2 waits, then fails.
	fs := newFakeServer(t, regRespCharging, gpuLoopTask("g1", 1, loopPtr(1)), gpuLoopTask("g2", 1, loopPtr(1)))
	rt := newGPURecordingRuntime()
	logs := &syncBuffer{}
	w := newGPULoopWorker(t, fs.srv.URL, rt, 2, 1, logs)
	run := startWorker(t, w)

	waitFor(t, "g1 running", func() bool { return rt.isRunning("g1") })
	waitFor(t, "g2 reported", func() bool { _, ok := fs.result("g2"); return ok })
	res, _ := fs.result("g2")
	if res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "free GPU devices after waiting") {
		t.Errorf("g2 result = %s %q, want FAILED with the wait error", res.State, res.Stderr)
	}
	if _, ran := rt.gpuOf("g2"); ran {
		t.Error("g2 reached the runtime without devices")
	}
	if n := strings.Count(logs.String(), "waiting for GPU devices"); n != 1 {
		t.Errorf("wait WARN logged %d times, want once:\n%s", n, logs.String())
	}
	rt.release("g1")
	waitFor(t, "g1 reported", func() bool { _, ok := fs.result("g1"); return ok })
	_ = run.stop()
}

func TestTaskLoop_GPUWaitCancelledByServerIsSkipped(t *testing.T) {
	shortGPUWait(t, 10*time.Second)
	fs := newFakeServer(t, regRespCharging, gpuLoopTask("g1", 1, loopPtr(1)), gpuLoopTask("g2", 1, loopPtr(1)))
	rt := newGPURecordingRuntime()
	logs := &syncBuffer{}
	w := newGPULoopWorker(t, fs.srv.URL, rt, 2, 1, logs)
	run := startWorker(t, w)

	waitFor(t, "g1 running", func() bool { return rt.isRunning("g1") })
	waitFor(t, "g2 waiting", func() bool { return strings.Contains(logs.String(), "waiting for GPU devices") })
	if !w.active.cancel("g2") {
		t.Fatal("g2 not active")
	}
	waitFor(t, "g2 reported", func() bool { _, ok := fs.result("g2"); return ok })
	if res, _ := fs.result("g2"); res.State != model.TaskStateSkipped {
		t.Errorf("g2 reported %s, want SKIPPED", res.State)
	}
	rt.release("g1")
	waitFor(t, "g1 reported", func() bool { _, ok := fs.result("g1"); return ok })
	_ = run.stop()
}

func TestTaskLoop_GPUCountAboveDevicesFails(t *testing.T) {
	shortGPUWait(t, 10*time.Second)
	fs := newFakeServer(t, regRespCharging, gpuLoopTask("big", 2, loopPtr(2)))
	rt := newGPURecordingRuntime()
	w := newGPULoopWorker(t, fs.srv.URL, rt, 2, 1, nil)
	run := startWorker(t, w)

	waitFor(t, "big reported", func() bool { _, ok := fs.result("big"); return ok })
	res, _ := fs.result("big")
	if res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "needs 2 GPUs but this worker has 1") {
		t.Errorf("big result = %s %q, want FAILED naming the counts", res.State, res.Stderr)
	}
	_ = run.stop()
}

// A task without a GPU charge (server that does not count GPUs) drops the
// worker to one task at a time, and tasks keep getting devices from the
// ledger, never every GPU.
func TestTaskLoop_GPUNilChargeDropsToOneTaskAndKeepsLedger(t *testing.T) {
	fs := newFakeServer(t, regRespCharging, gpuLoopTask("g1", 1, nil), gpuLoopTask("cpu", 0, nil))
	rt := newGPURecordingRuntime()
	logs := &syncBuffer{}
	w := newGPULoopWorker(t, fs.srv.URL, rt, 2, 2, logs)
	run := startWorker(t, w)

	waitFor(t, "g1 running", func() bool { return rt.isRunning("g1") })
	if l := w.slotLimit(); l != 1 {
		t.Errorf("slotLimit = %d after a checkout without a GPU charge, want 1", l)
	}
	if !strings.Contains(logs.String(), "carries no GPU charge") {
		t.Errorf("missing WARN:\n%s", logs.String())
	}
	g1, _ := rt.gpuOf("g1")
	if len(g1.Devices) != 1 || g1.DeviceID != "" {
		t.Errorf("g1 GPU config = %+v, want one assigned device (from the requested count)", g1)
	}
	settle()
	if n := fs.checkouts(); n != 1 {
		t.Errorf("checkouts = %d while g1 runs at one slot, want 1", n)
	}
	rt.release("g1")
	waitFor(t, "cpu running", func() bool { return rt.isRunning("cpu") })
	if cpu, _ := rt.gpuOf("cpu"); !cpu.AccessDenied {
		t.Errorf("cpu GPU config = %+v, want GPU access denied (ledger kept)", cpu)
	}
	rt.release("cpu")
	waitFor(t, "cpu reported", func() bool { _, ok := fs.result("cpu"); return ok })
	_ = run.stop()
}

// At one slot every task gets the worker's static GPU config, as before.
func TestTaskLoop_GPUConcurrencyOneKeepsLegacyConfig(t *testing.T) {
	fs := newFakeServer(t, regRespCharging, gpuLoopTask("cpu", 0, loopPtr(0)), gpuLoopTask("g2", 2, loopPtr(2)))
	rt := newGPURecordingRuntime()
	w := newGPULoopWorker(t, fs.srv.URL, rt, 1, 0, nil)
	w.gpu.DeviceID = "0,1"
	run := startWorker(t, w)

	want := GPUConfig{Enabled: true, DeviceID: "0,1"}
	for _, id := range []string{"cpu", "g2"} {
		waitFor(t, id+" running", func() bool { return rt.isRunning(id) })
		g, _ := rt.gpuOf(id)
		if !g.Enabled || g.DeviceID != want.DeviceID || len(g.Devices) != 0 || g.AccessDenied {
			t.Errorf("%s GPU config = %+v, want %+v", id, g, want)
		}
		rt.release(id)
		waitFor(t, id+" reported", func() bool { _, ok := fs.result(id); return ok })
	}
	_ = run.stop()
}

func TestValidateGPUConcurrency(t *testing.T) {
	tests := []struct {
		name    string
		slots   int
		gpu     bool
		inv     []model.GPUInfo
		wantErr bool
	}{
		{"several slots, GPU, no inventory", 2, true, nil, true},
		{"several slots, GPU, empty inventory", 2, true, []model.GPUInfo{}, true},
		{"several slots, GPU, inventory", 2, true, fakeGPUs(2), false},
		{"one slot, GPU, no inventory", 1, true, nil, false},
		{"several slots, no GPU", 4, false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGPUConcurrency(tt.slots, tt.gpu, tt.inv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "per-task GPU assignment needs the GPU inventory (nvidia-smi)") {
				t.Errorf("err = %v, want the inventory message", err)
			}
		})
	}
}

func TestTaskGPUCount(t *testing.T) {
	tests := []struct {
		name string
		task *model.Task
		want int
	}{
		{"charge wins over the request", &model.Task{ChargedGPUs: loopPtr(1), RuntimeHints: &model.RuntimeHints{RequiresGPU: true, GPUs: 2}}, 1},
		{"zero charge", &model.Task{ChargedGPUs: loopPtr(0), RuntimeHints: &model.RuntimeHints{RequiresGPU: true}}, 0},
		{"no charge: requested count", &model.Task{RuntimeHints: &model.RuntimeHints{RequiresGPU: true, GPUs: 3}}, 3},
		{"no charge: boolean only", &model.Task{RuntimeHints: &model.RuntimeHints{RequiresGPU: true}}, 1},
		{"no charge, no hints", &model.Task{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskGPUCount(tt.task); got != tt.want {
				t.Errorf("taskGPUCount = %d, want %d", got, tt.want)
			}
		})
	}
}
