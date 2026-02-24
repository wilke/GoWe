package cwlrunner

import "context"

// Semaphore provides a counting semaphore for bounded concurrency.
// It limits the total number of concurrent operations across the entire workflow,
// including both DAG steps and scatter iterations.
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore creates a semaphore with the given capacity.
// If n <= 0, returns nil (unlimited concurrency).
func NewSemaphore(n int) *Semaphore {
	if n <= 0 {
		return nil
	}
	return &Semaphore{ch: make(chan struct{}, n)}
}

// Acquire blocks until a slot is available or context is cancelled.
// Returns true if acquired, false if context was cancelled.
// If semaphore is nil, returns true immediately (unlimited).
func (s *Semaphore) Acquire(ctx context.Context) bool {
	if s == nil {
		return true
	}
	select {
	case s.ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release releases a slot.
// If semaphore is nil, this is a no-op.
func (s *Semaphore) Release() {
	if s == nil {
		return
	}
	<-s.ch
}

// Capacity returns the semaphore capacity, or 0 if nil (unlimited).
func (s *Semaphore) Capacity() int {
	if s == nil {
		return 0
	}
	return cap(s.ch)
}
