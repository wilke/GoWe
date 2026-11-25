package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// fakeGPUs is a fake inventory of n GPUs with UUIDs GPU-0 ... GPU-(n-1).
func fakeGPUs(n int) []model.GPUInfo {
	gpus := make([]model.GPUInfo, n)
	for i := range gpus {
		gpus[i] = model.GPUInfo{Index: i, UUID: fmt.Sprintf("GPU-%d", i), Name: "Fake GPU", MemTotalMB: 1000}
	}
	return gpus
}

func TestGPUDeviceID(t *testing.T) {
	if got := gpuDeviceID(model.GPUInfo{Index: 3, UUID: "GPU-abc"}); got != "GPU-abc" {
		t.Errorf("with uuid = %q, want GPU-abc", got)
	}
	if got := gpuDeviceID(model.GPUInfo{Index: 3}); got != "3" {
		t.Errorf("without uuid = %q, want 3", got)
	}
}

func TestGPULedger_DistinctDevicesInInventoryOrder(t *testing.T) {
	l := newGPULedger(fakeGPUs(3))
	ctx := context.Background()
	a, err := l.acquire(ctx, 1, nil)
	if err != nil || len(a) != 1 || a[0] != "GPU-0" {
		t.Fatalf("first acquire = (%v, %v), want [GPU-0]", a, err)
	}
	b, err := l.acquire(ctx, 2, nil)
	if err != nil || len(b) != 2 || b[0] != "GPU-1" || b[1] != "GPU-2" {
		t.Fatalf("second acquire = (%v, %v), want [GPU-1 GPU-2]", b, err)
	}
	// Nothing free: a bounded acquire times out.
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if got, err := l.acquire(tctx, 1, nil); !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("acquire with nothing free = (%v, %v), want DeadlineExceeded", got, err)
	}
	// Release frees exactly those devices.
	l.release(a)
	c, err := l.acquire(ctx, 1, nil)
	if err != nil || len(c) != 1 || c[0] != "GPU-0" {
		t.Fatalf("acquire after release = (%v, %v), want [GPU-0]", c, err)
	}
}

func TestGPULedger_Bounds(t *testing.T) {
	l := newGPULedger(fakeGPUs(2))
	if got, err := l.acquire(context.Background(), 0, nil); got != nil || err != nil {
		t.Errorf("acquire(0) = (%v, %v), want (nil, nil)", got, err)
	}
	called := false
	if _, err := l.acquire(context.Background(), 3, func() { called = true }); err == nil {
		t.Error("acquire(3) of 2 devices: want an error at once")
	}
	if called {
		t.Error("acquire above the device count must not wait")
	}
	// Releasing unknown or free devices is harmless.
	l.release([]string{"GPU-9", "GPU-0"})
	l.release(nil)
	if got, err := l.acquire(context.Background(), 2, nil); err != nil || len(got) != 2 {
		t.Errorf("acquire(2) = (%v, %v), want both devices", got, err)
	}
}

func TestGPULedger_BlockingAcquireUnblocksOnRelease(t *testing.T) {
	l := newGPULedger(fakeGPUs(2))
	held, _ := l.acquire(context.Background(), 1, nil)

	waiting := make(chan struct{})
	done := make(chan []string, 1)
	var waits int
	go func() {
		got, err := l.acquire(context.Background(), 2, func() {
			waits++
			close(waiting)
		})
		if err != nil {
			t.Errorf("blocked acquire: %v", err)
		}
		done <- got
	}()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("acquire(2) with one device held did not wait")
	}
	select {
	case got := <-done:
		t.Fatalf("acquire(2) returned %v while a device was held", got)
	case <-time.After(20 * time.Millisecond):
	}
	l.release(held)
	select {
	case got := <-done:
		if len(got) != 2 {
			t.Errorf("acquire(2) = %v, want both devices", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire(2) did not return after the release")
	}
	if waits != 1 {
		t.Errorf("onWait called %d times, want once", waits)
	}
}

func TestGPULedger_ContextCancel(t *testing.T) {
	l := newGPULedger(fakeGPUs(1))
	_, _ = l.acquire(context.Background(), 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.acquire(ctx, 1, func() { cancel() })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not return after cancellation")
	}
}

// Many tasks acquiring and releasing concurrently never share a device.
func TestGPULedger_NeverTheSameDeviceToTwoHolders(t *testing.T) {
	const devices, workers, rounds = 3, 8, 50
	l := newGPULedger(fakeGPUs(devices))
	var mu sync.Mutex
	owner := map[string]int{}
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				n := 1 + (g+r)%2
				got, err := l.acquire(context.Background(), n, nil)
				if err != nil || len(got) != n {
					t.Errorf("acquire(%d) = (%v, %v)", n, got, err)
					return
				}
				mu.Lock()
				for _, d := range got {
					if o, taken := owner[d]; taken {
						t.Errorf("device %s handed to %d while held by %d", d, g, o)
					}
					owner[d] = g
				}
				mu.Unlock()
				time.Sleep(time.Microsecond)
				mu.Lock()
				for _, d := range got {
					delete(owner, d)
				}
				mu.Unlock()
				l.release(got)
			}
		}(g)
	}
	wg.Wait()
}
