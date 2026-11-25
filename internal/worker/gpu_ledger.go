package worker

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/me/gowe/pkg/model"
)

// gpuLedger hands out the GPU devices of a worker that runs several tasks at
// once, so that each device belongs to at most one task at a time. The server
// already keeps the GPUs charged to a worker's running tasks within its
// inventory; the ledger picks which devices a task gets, and makes a task
// wait when the server released a charge before the process holding the
// device ended.
type gpuLedger struct {
	mu      sync.Mutex
	devices []string      // device identifiers, in inventory order
	held    []bool        // held[i]: devices[i] is assigned to a task
	changed chan struct{} // closed (and replaced) on every release
}

// newGPULedger returns a ledger over the devices of inventory (see
// gpuDeviceID), all free.
func newGPULedger(inventory []model.GPUInfo) *gpuLedger {
	ids := make([]string, len(inventory))
	for i, g := range inventory {
		ids[i] = gpuDeviceID(g)
	}
	return &gpuLedger{devices: ids, held: make([]bool, len(ids)), changed: make(chan struct{})}
}

// gpuDeviceID is the identifier passed to tools for a GPU: its UUID when the
// inventory has one (stable whatever the device numbering), else its
// nvidia-smi index.
func gpuDeviceID(g model.GPUInfo) string {
	if g.UUID != "" {
		return g.UUID
	}
	return strconv.Itoa(g.Index)
}

// size is the number of devices of the ledger.
func (l *gpuLedger) size() int {
	return len(l.devices)
}

// acquire returns n distinct free devices (the first free ones in inventory
// order) and marks them held, blocking until n are free or ctx is done
// (ctx.Err() then). onWait, when not nil, is called once, before the first
// wait. n <= 0 returns nil; n above the number of devices is an error,
// returned at once.
func (l *gpuLedger) acquire(ctx context.Context, n int, onWait func()) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > len(l.devices) {
		return nil, fmt.Errorf("%d GPUs requested, the worker has %d", n, len(l.devices))
	}
	waited := false
	for {
		l.mu.Lock()
		got := l.takeLocked(n)
		changed := l.changed
		l.mu.Unlock()
		if got != nil {
			return got, nil
		}
		if !waited && onWait != nil {
			onWait()
		}
		waited = true
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// takeLocked marks and returns the first n free devices, or returns nil
// (marking nothing) when fewer than n are free. l.mu must be held.
func (l *gpuLedger) takeLocked(n int) []string {
	var idx []int
	for i, h := range l.held {
		if !h {
			idx = append(idx, i)
			if len(idx) == n {
				break
			}
		}
	}
	if len(idx) < n {
		return nil
	}
	out := make([]string, n)
	for j, i := range idx {
		l.held[i] = true
		out[j] = l.devices[i]
	}
	return out
}

// release frees devices (from acquire) and wakes the waiting acquirers.
// Unknown or already free devices are ignored.
func (l *gpuLedger) release(devices []string) {
	if len(devices) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, d := range devices {
		for i, id := range l.devices {
			if id == d {
				l.held[i] = false
			}
		}
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// gpuHold records the devices a task goroutine holds; the goroutine's
// outermost defer releases them (releaseTaskGPUs), before the task's slot.
type gpuHold struct {
	devices []string
}
