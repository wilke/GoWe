package scheduler

import "context"

// Scheduler evaluates task readiness, dispatches tasks to executors,
// and manages the submission lifecycle.
type Scheduler interface {
	// Start begins the scheduling loop. Blocks until ctx is cancelled.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the scheduler.
	Stop() error

	// Tick runs a single scheduling iteration. Used for testing.
	Tick(ctx context.Context) error

	// State reports the scheduler's lifecycle state: "not_started", "running",
	// or "stopped". Surfaced by the /api/v1/health endpoint. Safe for
	// concurrent use.
	State() string
}
