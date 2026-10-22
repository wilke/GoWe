package cwlrunner

import (
	"context"
	"log/slog"

	"golang.org/x/sync/semaphore"
)

// INVARIANT: both Semaphore (below) and CoresSemaphore are acquired ONLY by
// leaf tool executions (a single CommandLineTool run, including one scatter
// iteration of a tool). Structural steps -- sub-workflow execution and
// scatter coordination -- must NEVER hold a slot while waiting on their
// children to complete; they only wait on goroutines/results, they never
// call Acquire themselves. This is what makes ONE global semaphore shared
// across arbitrary nesting depth (top-level steps, nested sub-workflows,
// scatter-over-subworkflow iterations) deadlock-free: a structural step
// blocked on children never holds a slot that a descendant leaf execution
// needs in order to make progress.

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

// CoresSemaphore provides a weighted semaphore for bounded machine-core
// consumption across all concurrent tool executions (the --cores budget,
// #50/#183). It is the weighted analogue of Semaphore and is subject to the
// same leaf-only acquisition invariant documented above. Built on
// golang.org/x/sync/semaphore.Weighted, which grants FIFO-fair.
type CoresSemaphore struct {
	sem    *semaphore.Weighted
	budget int64
	logger *slog.Logger
}

// NewCoresSemaphore creates a weighted semaphore with the given core budget.
// If budget <= 0, returns nil (disabled -- no core-count limiting; this is
// the default so existing behavior is unchanged unless --cores is passed).
func NewCoresSemaphore(budget int, logger *slog.Logger) *CoresSemaphore {
	if budget <= 0 {
		return nil
	}
	return &CoresSemaphore{
		sem:    semaphore.NewWeighted(int64(budget)),
		budget: int64(budget),
		logger: logger,
	}
}

// Acquire blocks until `weight` cores are available or ctx is cancelled.
// weight is clamped to the configured budget (with a logged warning) when it
// would otherwise exceed the entire budget -- this prevents a single tool
// whose ResourceRequirement.coresMin is larger than --cores from
// self-deadlocking (it would otherwise never be able to acquire enough
// weight to run at all). toolID is used only for the warning log line.
//
// Returns the weight actually acquired (needed for the matching Release) and
// whether acquisition succeeded. If the semaphore is nil (disabled), it
// returns (0, true) immediately -- callers must treat a 0 weight as "nothing
// to release".
func (c *CoresSemaphore) Acquire(ctx context.Context, weight int, toolID string) (int64, bool) {
	if c == nil {
		return 0, true
	}
	w := int64(weight)
	if w <= 0 {
		w = 1
	}
	if w > c.budget {
		if c.logger != nil {
			c.logger.Warn("tool cores requirement exceeds --cores budget; clamping to avoid self-deadlock",
				"tool", toolID, "requested_cores", w, "cores_budget", c.budget)
		}
		w = c.budget
	}
	if err := c.sem.Acquire(ctx, w); err != nil {
		return 0, false
	}
	return w, true
}

// Release releases `weight` cores previously returned by Acquire. A weight
// of 0 (as returned when the semaphore is nil, or when Acquire failed) is a
// no-op, so callers can unconditionally call Release with whatever Acquire
// returned.
func (c *CoresSemaphore) Release(weight int64) {
	if c == nil || weight <= 0 {
		return
	}
	c.sem.Release(weight)
}

// Capacity returns the configured core budget, or 0 if nil (disabled).
func (c *CoresSemaphore) Capacity() int {
	if c == nil {
		return 0
	}
	return int(c.budget)
}
