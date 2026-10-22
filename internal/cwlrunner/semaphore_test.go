package cwlrunner

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/pkg/cwl"
)

func TestSemaphore_LimitsConcurrency(t *testing.T) {
	sem := NewSemaphore(3)

	var maxConcurrent int32
	var current int32
	var wg sync.WaitGroup

	// Run 10 concurrent goroutines, each should acquire the semaphore
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx := context.Background()
			if !sem.Acquire(ctx) {
				t.Error("Acquire failed unexpectedly")
				return
			}

			// Track current concurrency
			c := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if c <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
					break
				}
			}

			// Simulate work
			time.Sleep(10 * time.Millisecond)

			atomic.AddInt32(&current, -1)
			sem.Release()
		}()
	}

	wg.Wait()

	if maxConcurrent > 3 {
		t.Errorf("Max concurrent %d exceeded semaphore limit 3", maxConcurrent)
	}
	if maxConcurrent < 3 {
		t.Logf("Warning: max concurrent was only %d (expected 3)", maxConcurrent)
	}
}

func TestSemaphore_Nil(t *testing.T) {
	var sem *Semaphore // nil semaphore

	// Should not block or panic
	ctx := context.Background()
	if !sem.Acquire(ctx) {
		t.Error("nil semaphore Acquire should return true")
	}
	sem.Release() // should not panic

	if sem.Capacity() != 0 {
		t.Errorf("nil semaphore capacity should be 0, got %d", sem.Capacity())
	}
}

func TestSemaphore_ContextCancellation(t *testing.T) {
	sem := NewSemaphore(1)

	// Acquire the only slot
	ctx := context.Background()
	sem.Acquire(ctx)

	// Try to acquire with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	if sem.Acquire(ctx) {
		t.Error("Acquire should return false when context is cancelled")
		sem.Release()
	}

	// Release the original slot
	sem.Release()
}

func TestNewSemaphore_ZeroOrNegative(t *testing.T) {
	if NewSemaphore(0) != nil {
		t.Error("NewSemaphore(0) should return nil")
	}
	if NewSemaphore(-1) != nil {
		t.Error("NewSemaphore(-1) should return nil")
	}
}

func TestNewCoresSemaphore_ZeroOrNegative(t *testing.T) {
	if NewCoresSemaphore(0, nil) != nil {
		t.Error("NewCoresSemaphore(0, nil) should return nil")
	}
	if NewCoresSemaphore(-1, nil) != nil {
		t.Error("NewCoresSemaphore(-1, nil) should return nil")
	}
}

func TestCoresSemaphore_Nil(t *testing.T) {
	var c *CoresSemaphore // nil semaphore (i.e. --cores disabled)

	ctx := context.Background()
	weight, ok := c.Acquire(ctx, 64, "big-tool")
	if !ok {
		t.Error("nil CoresSemaphore Acquire should return ok=true")
	}
	if weight != 0 {
		t.Errorf("nil CoresSemaphore Acquire should return weight 0, got %d", weight)
	}
	c.Release(weight) // should not panic

	if c.Capacity() != 0 {
		t.Errorf("nil CoresSemaphore capacity should be 0, got %d", c.Capacity())
	}
}

// TestCoresSemaphore_CapEnforcement proves the weighted semaphore caps total
// acquired weight at the configured budget: with budget=4 and each of 12
// goroutines acquiring weight=1, at most 4 should ever be admitted at once.
func TestCoresSemaphore_CapEnforcement(t *testing.T) {
	c := NewCoresSemaphore(4, nil)

	var maxConcurrent, current int32
	var wg sync.WaitGroup

	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx := context.Background()
			weight, ok := c.Acquire(ctx, 1, "tool")
			if !ok {
				t.Error("Acquire failed unexpectedly")
				return
			}
			defer c.Release(weight)

			cur := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if cur <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&current, -1)
		}()
	}

	wg.Wait()

	if maxConcurrent > 4 {
		t.Errorf("max concurrent weight %d exceeded cores budget 4", maxConcurrent)
	}
	if maxConcurrent < 4 {
		t.Logf("warning: max concurrent was only %d (expected 4)", maxConcurrent)
	}
}

// TestCoresSemaphore_CapEnforcement_WeightedWaves proves admission is
// floor(budget/weight): with budget=4 and weight=2 per acquisition, at most
// 2 acquisitions should ever be concurrently admitted.
func TestCoresSemaphore_CapEnforcement_WeightedWaves(t *testing.T) {
	c := NewCoresSemaphore(4, nil)

	var maxConcurrent, current int32
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx := context.Background()
			weight, ok := c.Acquire(ctx, 2, "tool")
			if !ok {
				t.Error("Acquire failed unexpectedly")
				return
			}
			defer c.Release(weight)

			cur := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if cur <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&current, -1)
		}()
	}

	wg.Wait()

	if maxConcurrent > 2 {
		t.Errorf("max concurrent acquisitions %d exceeded floor(budget/weight)=2", maxConcurrent)
	}
}

// TestCoresSemaphore_ClampWithWarning proves a requirement larger than the
// budget is clamped (so it can still run instead of self-deadlocking) and
// that a warning is logged identifying the clamp.
func TestCoresSemaphore_ClampWithWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := NewCoresSemaphore(2, logger)

	ctx := context.Background()
	weight, ok := c.Acquire(ctx, 5, "jackhmmer")
	if !ok {
		t.Fatal("Acquire should succeed after clamping")
	}
	if weight != 2 {
		t.Errorf("expected clamped weight 2 (the budget), got %d", weight)
	}
	c.Release(weight)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "clamp") {
		t.Errorf("expected a warning log mentioning clamping, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "jackhmmer") {
		t.Errorf("expected the warning to identify the tool, got: %s", logOutput)
	}
}

// TestCoresSemaphore_ContextCancellation mirrors TestSemaphore_ContextCancellation.
func TestCoresSemaphore_ContextCancellation(t *testing.T) {
	c := NewCoresSemaphore(1, nil)

	ctx := context.Background()
	if _, ok := c.Acquire(ctx, 1, "tool"); !ok {
		t.Fatal("initial Acquire should succeed")
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	weight, ok := c.Acquire(cancelledCtx, 1, "tool")
	if ok {
		t.Error("Acquire should return false when context is cancelled")
		c.Release(weight)
	}
	if weight != 0 {
		t.Errorf("failed Acquire should return weight 0, got %d", weight)
	}
}

// TestAcquireReleaseExecutionSlot_Composition exercises acquireExecutionSlot/
// releaseExecutionSlot together (count-then-cores acquire order), table-driven
// over count-only, cores-only, and both-bounded configurations.
func TestAcquireReleaseExecutionSlot_Composition(t *testing.T) {
	tool := &cwl.CommandLineTool{ID: "test-tool"}

	tests := []struct {
		name          string
		maxWorkers    int
		coresBudget   int
		n             int // number of concurrent acquirers
		wantMaxConcur int32
	}{
		{name: "count only", maxWorkers: 1, coresBudget: 0, n: 6, wantMaxConcur: 1},
		{name: "cores only", maxWorkers: 0, coresBudget: 1, n: 6, wantMaxConcur: 1},
		{name: "both bounded, count tighter", maxWorkers: 1, coresBudget: 4, n: 6, wantMaxConcur: 1},
		{name: "both bounded, cores tighter", maxWorkers: 4, coresBudget: 1, n: 6, wantMaxConcur: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ParallelConfig{Enabled: true, MaxWorkers: tc.maxWorkers, CoresBudget: tc.coresBudget}
			if tc.maxWorkers > 0 {
				cfg.Semaphore = NewSemaphore(tc.maxWorkers)
			}
			if tc.coresBudget > 0 {
				cfg.Cores = NewCoresSemaphore(tc.coresBudget, nil)
			}

			var maxConcurrent, current int32
			var wg sync.WaitGroup

			for i := 0; i < tc.n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					ctx := context.Background()
					coresWeight, ok := acquireExecutionSlot(ctx, cfg, tool, nil, nil)
					if !ok {
						t.Error("acquireExecutionSlot failed unexpectedly")
						return
					}
					defer releaseExecutionSlot(cfg, coresWeight)

					cur := atomic.AddInt32(&current, 1)
					for {
						old := atomic.LoadInt32(&maxConcurrent)
						if cur <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, cur) {
							break
						}
					}
					time.Sleep(10 * time.Millisecond)
					atomic.AddInt32(&current, -1)
				}()
			}

			wg.Wait()

			if maxConcurrent > tc.wantMaxConcur {
				t.Errorf("max concurrent %d exceeded expected cap %d", maxConcurrent, tc.wantMaxConcur)
			}
		})
	}
}

// TestAcquireExecutionSlot_CountAcquiredBeforeCores proves the fixed
// acquisition order (count semaphore, then cores semaphore): when the count
// semaphore is already exhausted, acquireExecutionSlot must block on it
// without ever touching (decrementing) the cores budget. We verify this by
// holding the sole count slot externally, launching a blocked
// acquireExecutionSlot call, and confirming the cores budget is still fully
// available (via a non-blocking TryAcquire) while that call is stuck.
func TestAcquireExecutionSlot_CountAcquiredBeforeCores(t *testing.T) {
	tool := &cwl.CommandLineTool{ID: "test-tool"}
	cfg := ParallelConfig{
		Enabled:     true,
		MaxWorkers:  1,
		Semaphore:   NewSemaphore(1),
		CoresBudget: 4,
		Cores:       NewCoresSemaphore(4, nil),
	}

	// Hold the only count slot ourselves so any acquireExecutionSlot call
	// must block on the count semaphore.
	holderCtx := context.Background()
	if !cfg.Semaphore.Acquire(holderCtx) {
		t.Fatal("failed to acquire the count semaphore for the test holder")
	}

	done := make(chan struct{})
	blockedCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	go func() {
		defer close(done)
		_, ok := acquireExecutionSlot(blockedCtx, cfg, tool, nil, nil)
		if ok {
			t.Error("acquireExecutionSlot should not succeed while the count semaphore is held")
		}
	}()

	// Give the goroutine a moment to reach (and block on) the count Acquire.
	time.Sleep(30 * time.Millisecond)

	// The cores budget should be untouched: a full-budget TryAcquire must
	// still succeed, proving acquireExecutionSlot never got past the count
	// semaphore to attempt the cores acquisition.
	if !cfg.Cores.sem.TryAcquire(4) {
		t.Error("cores budget was partially consumed while blocked on the count semaphore -- acquisition order violated")
	} else {
		cfg.Cores.sem.Release(4)
	}

	<-done
	cfg.Semaphore.Release()
}
