package worker

import (
	"context"
	"sort"
	"sync"
)

// activeTaskSet tracks the tasks a worker is currently executing along with the
// cancel function for each task's execution context. It is the durable link
// between a task ID and the running process, used to (a) report running tasks on
// each heartbeat and (b) cancel a specific task when the server signals it.
type activeTaskSet struct {
	mu        sync.Mutex
	m         map[string]context.CancelFunc
	cancelled map[string]struct{} // tasks explicitly cancelled via cancel()
}

func newActiveTaskSet() *activeTaskSet {
	return &activeTaskSet{
		m:         make(map[string]context.CancelFunc),
		cancelled: make(map[string]struct{}),
	}
}

// add registers a running task and its cancel function.
func (a *activeTaskSet) add(taskID string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[taskID] = cancel
	delete(a.cancelled, taskID)
}

// remove deregisters a task (called when execution finishes).
func (a *activeTaskSet) remove(taskID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, taskID)
	delete(a.cancelled, taskID)
}

// ids returns the IDs of all currently-running tasks, sorted for stable output.
func (a *activeTaskSet) ids() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.m) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.m))
	for id := range a.m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// cancel invokes the cancel function for the given task if it is registered and
// records that the cancellation was explicit (vs. a worker-shutdown context
// cancellation). Returns true if the task was found and cancelled.
func (a *activeTaskSet) cancel(taskID string) bool {
	a.mu.Lock()
	cancel, ok := a.m[taskID]
	if ok {
		a.cancelled[taskID] = struct{}{}
	}
	a.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// wasCancelled reports whether the task was explicitly cancelled via cancel().
// This distinguishes a server-driven cancellation (report SKIPPED) from a
// worker-shutdown context cancellation (leave the task for the server to requeue).
func (a *activeTaskSet) wasCancelled(taskID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.cancelled[taskID]
	return ok
}
