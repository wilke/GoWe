package cwlrunner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
