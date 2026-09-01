package scheduler

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/pkg/model"
)

// gatherFamily returns the metric family with the given name, or nil.
func gatherFamily(t *testing.T, reg *metrics.Registry, name string) *dto.MetricFamily {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

// TestTick_ObservesPhaseHistogram runs a single empty Tick and verifies
// gowe_scheduler_tick_seconds carries a "total" sample plus every phase that
// actually ran under testSetup's DefaultConfig: 1, 2, 2.5, 3, 3.5 (threshold
// 30 > 0), 4, 5, 6. Phases 1.5/5.5 are absent — testSetup never configures a
// workspace stager, so those phases are skipped entirely (not just recorded
// as zero-duration).
func TestTick_ObservesPhaseHistogram(t *testing.T) {
	sched, _ := testSetup(t)
	reg := metrics.NewRegistry(metrics.Config{})
	sched.SetMetrics(reg)

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	mf := gatherFamily(t, reg, "gowe_scheduler_tick_seconds")
	if mf == nil {
		t.Fatal("gowe_scheduler_tick_seconds metric family not found")
	}
	seenPhases := map[string]bool{}
	for _, m := range mf.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "phase" {
				seenPhases[l.GetValue()] = true
			}
		}
	}
	for _, want := range []string{"total", "1", "2", "2.5", "3", "3.5", "4", "5", "6"} {
		if !seenPhases[want] {
			t.Errorf("phase %q missing from gowe_scheduler_tick_seconds; saw %v", want, seenPhases)
		}
	}
	for _, absent := range []string{"1.5", "5.5"} {
		if seenPhases[absent] {
			t.Errorf("phase %q present but no workspace stager is configured; want absent", absent)
		}
	}
}

// TestTick_RefreshesStateGauges seeds tasks in known states directly in the
// store, runs one Tick, and verifies gowe_tasks{state} reflects the seeded
// counts (including zero-fill for states with no rows).
func TestTick_RefreshesStateGauges(t *testing.T) {
	sched, st := testSetup(t)
	reg := metrics.NewRegistry(metrics.Config{})
	sched.SetMetrics(reg)

	ctx := context.Background()
	wfID, subID := createPipeline(t, st, []model.Step{}, map[string]any{}, 3)
	_ = wfID

	seed := []model.TaskState{
		model.TaskStateQueued, model.TaskStateQueued, model.TaskStateRunning,
	}
	for i, state := range seed {
		task := &model.Task{
			ID:           "gauge_task_" + string(rune('a'+i)),
			SubmissionID: subID,
			StepID:       "step1",
			State:        state,
			ExecutorType: model.ExecutorTypeLocal,
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			ScatterIndex: -1,
		}
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	mf := gatherFamily(t, reg, "gowe_tasks")
	if mf == nil {
		t.Fatal("gowe_tasks metric family not found")
	}
	got := map[string]float64{}
	for _, m := range mf.GetMetric() {
		var state string
		for _, l := range m.GetLabel() {
			if l.GetName() == "state" {
				state = l.GetValue()
			}
		}
		got[state] = m.GetGauge().GetValue()
	}
	// 2 QUEUED seeded above; the pipeline created by createPipeline may also
	// have advanced some step to PENDING/RUNNING tasks depending on the
	// scheduler's own dispatch this tick, so only assert the floor the seed
	// data guarantees, plus the zero-fill contract for a state we know is
	// empty (SKIPPED — nothing in this test skips anything).
	if got["QUEUED"] < 2 {
		t.Errorf("tasks{state=QUEUED} = %v, want >= 2", got["QUEUED"])
	}
	if v, ok := got["SKIPPED"]; !ok || v != 0 {
		t.Errorf("tasks{state=SKIPPED} = %v (present=%v), want 0 (zero-filled)", v, ok)
	}
}

// TestTick_RefreshesWorkerGauge_IncludesOfflineWorkers guards against
// reusing workerCapabilities().Workers for the gauge refresh: that cache is
// deliberately online-only (it answers "can a task be scheduled right
// now"), so a naive refresh would silently drop offline/draining workers
// from gowe_workers{state,group} instead of reporting them in their own
// state bucket.
func TestTick_RefreshesWorkerGauge_IncludesOfflineWorkers(t *testing.T) {
	sched, st := testSetup(t)
	reg := metrics.NewRegistry(metrics.Config{})
	sched.SetMetrics(reg)

	ctx := context.Background()
	now := time.Now().UTC()
	workers := []*model.Worker{
		{ID: "w_online", Name: "w1", Hostname: "h1", State: model.WorkerStateOnline, Group: "default", RegisteredAt: now, LastSeen: now},
		{ID: "w_offline", Name: "w2", Hostname: "h2", State: model.WorkerStateOffline, Group: "esmfold", RegisteredAt: now, LastSeen: now},
	}
	for _, w := range workers {
		if err := st.CreateWorker(ctx, w); err != nil {
			t.Fatalf("create worker %s: %v", w.ID, err)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	mf := gatherFamily(t, reg, "gowe_workers")
	if mf == nil {
		t.Fatal("gowe_workers metric family not found")
	}
	found := map[string]bool{}
	for _, m := range mf.GetMetric() {
		var state, group string
		for _, l := range m.GetLabel() {
			switch l.GetName() {
			case "state":
				state = l.GetValue()
			case "group":
				group = l.GetValue()
			}
		}
		if m.GetGauge().GetValue() > 0 {
			found[state+"/"+group] = true
		}
	}
	if !found["online/default"] {
		t.Errorf("workers{state=online,group=default} not reported; series = %v", found)
	}
	if !found["offline/esmfold"] {
		t.Errorf("workers{state=offline,group=esmfold} not reported (regression: gauge must not be online-only); series = %v", found)
	}
}
