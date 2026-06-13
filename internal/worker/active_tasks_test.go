package worker

import (
	"context"
	"testing"
)

func TestActiveTaskSet_AddIDsRemove(t *testing.T) {
	a := newActiveTaskSet()
	if ids := a.ids(); ids != nil {
		t.Fatalf("empty set ids = %v, want nil", ids)
	}

	a.add("task_b", func() {})
	a.add("task_a", func() {})
	ids := a.ids()
	if len(ids) != 2 || ids[0] != "task_a" || ids[1] != "task_b" {
		t.Fatalf("ids = %v, want sorted [task_a task_b]", ids)
	}

	a.remove("task_a")
	if ids := a.ids(); len(ids) != 1 || ids[0] != "task_b" {
		t.Fatalf("after remove ids = %v, want [task_b]", ids)
	}
}

func TestActiveTaskSet_CancelInvokesContext(t *testing.T) {
	a := newActiveTaskSet()
	ctx, cancel := context.WithCancel(context.Background())
	a.add("task_x", cancel)

	if ok := a.cancel("task_missing"); ok {
		t.Error("cancel of unknown task returned true")
	}
	if ctx.Err() != nil {
		t.Error("context cancelled unexpectedly")
	}

	if ok := a.cancel("task_x"); !ok {
		t.Fatal("cancel of known task returned false")
	}
	if ctx.Err() != context.Canceled {
		t.Errorf("context err = %v, want context.Canceled", ctx.Err())
	}
	if !a.wasCancelled("task_x") {
		t.Error("wasCancelled(task_x) = false, want true after explicit cancel")
	}
}

func TestActiveTaskSet_WasCancelledDistinguishesShutdown(t *testing.T) {
	a := newActiveTaskSet()
	a.add("task_y", func() {})

	// A task that finishes normally (never explicitly cancelled) reports false —
	// this is how a worker-shutdown context cancellation is distinguished from a
	// server-driven cancel.
	if a.wasCancelled("task_y") {
		t.Error("wasCancelled(task_y) = true, want false (not explicitly cancelled)")
	}

	// re-adding a task clears any stale cancelled marker.
	a.cancel("task_y")
	a.remove("task_y")
	a.add("task_y", func() {})
	if a.wasCancelled("task_y") {
		t.Error("wasCancelled after re-add = true, want false (marker cleared)")
	}
}
