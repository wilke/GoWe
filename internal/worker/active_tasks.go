package worker

import (
	"context"
	"os"
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
	cancelled map[string]struct{}    // tasks explicitly cancelled via cancel()
	outcomes  map[string]string      // run outcome recorded at the final report (kept-dir markers)
	dirs      map[string]os.FileInfo // task dir created by the run (kept-dir markers)
}

func newActiveTaskSet() *activeTaskSet {
	return &activeTaskSet{
		m:         make(map[string]context.CancelFunc),
		cancelled: make(map[string]struct{}),
		outcomes:  make(map[string]string),
		dirs:      make(map[string]os.FileInfo),
	}
}

// add registers a running task and its cancel function.
func (a *activeTaskSet) add(taskID string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[taskID] = cancel
	delete(a.cancelled, taskID)
	delete(a.outcomes, taskID)
	delete(a.dirs, taskID)
}

// remove deregisters a task (called when execution finishes).
func (a *activeTaskSet) remove(taskID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, taskID)
	delete(a.cancelled, taskID)
	delete(a.outcomes, taskID)
	delete(a.dirs, taskID)
}

// setTaskDir records the os.FileInfo of the task dir the run of a registered
// task created (prepareTaskDir), so a kept-dir marker is only written for that
// very directory (ownsTaskDir). Ignored for a task that is not registered, and
// on a nil set.
func (a *activeTaskSet) setTaskDir(taskID string, fi os.FileInfo) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.m[taskID]; ok {
		a.dirs[taskID] = fi
	}
}

// taskDir returns the FileInfo recorded by setTaskDir, nil if none.
func (a *activeTaskSet) taskDir(taskID string) os.FileInfo {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dirs[taskID]
}

// setOutcome records how the run of a registered task ended (runOutcomeSuccess
// or a kept-dir marker reason). Ignored for a task that is not registered, and
// on a nil set (test workers without one).
func (a *activeTaskSet) setOutcome(taskID, outcome string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.m[taskID]; ok {
		a.outcomes[taskID] = outcome
	}
}

// outcome returns the outcome recorded by setOutcome, "" if none.
func (a *activeTaskSet) outcome(taskID string) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outcomes[taskID]
}

// has reports whether the task is currently registered as running (false on
// a nil set).
func (a *activeTaskSet) has(taskID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.m[taskID]
	return ok
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
