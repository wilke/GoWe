package metrics

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/me/gowe/pkg/model"
)

func newTestTask(state model.TaskState, dispatched, started, completed *time.Time) *model.Task {
	return &model.Task{
		ID:           "task1",
		StepID:       "step1",
		State:        state,
		ExecutorType: model.ExecutorTypeWorker,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "grp"},
		DispatchedAt: dispatched,
		StartedAt:    started,
		CompletedAt:  completed,
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestObserveTaskTerminal_Success_RecordsQueueAndRunSamples(t *testing.T) {
	r := NewRegistry(Config{})
	base := time.Now().UTC()
	dispatched := timePtr(base)
	started := timePtr(base.Add(2 * time.Second))
	completed := timePtr(base.Add(10 * time.Second))
	task := newTestTask(model.TaskStateSuccess, dispatched, started, completed)
	ms := int64(500)
	task.StageInMs = &ms
	task.StageOutMs = &ms

	r.ObserveTaskTerminal(task, "wf1", "")

	if n := testutil.CollectAndCount(r.taskQueueSeconds); n != 1 {
		t.Errorf("taskQueueSeconds samples = %d, want 1", n)
	}
	if n := testutil.CollectAndCount(r.taskRunSeconds); n != 1 {
		t.Errorf("taskRunSeconds samples = %d, want 1", n)
	}
	if n := testutil.CollectAndCount(r.taskStageSeconds); n != 2 {
		t.Errorf("taskStageSeconds samples = %d, want 2 (in+out)", n)
	}
	if n := testutil.CollectAndCount(r.taskFailures); n != 0 {
		t.Errorf("taskFailures samples = %d, want 0 for SUCCESS", n)
	}
}

func TestObserveTaskTerminal_FailedNoStartedAt_SkipsDurationsCountsFailure(t *testing.T) {
	r := NewRegistry(Config{})
	completed := timePtr(time.Now().UTC())
	task := newTestTask(model.TaskStateFailed, nil, nil, completed)

	r.ObserveTaskTerminal(task, "wf1", "submit")

	if n := testutil.CollectAndCount(r.taskQueueSeconds); n != 0 {
		t.Errorf("taskQueueSeconds samples = %d, want 0 (started_at nil)", n)
	}
	if n := testutil.CollectAndCount(r.taskRunSeconds); n != 0 {
		t.Errorf("taskRunSeconds samples = %d, want 0 (started_at nil)", n)
	}
	if got := testutil.ToFloat64(r.taskFailures.WithLabelValues("submit", "wf1", "step1", "worker", "grp")); got != 1 {
		t.Errorf("taskFailures{reason=submit} = %v, want 1", got)
	}
}

func TestObserveTaskTerminal_NonTerminalState_NoOp(t *testing.T) {
	r := NewRegistry(Config{})
	task := newTestTask(model.TaskStateRunning, nil, nil, nil)
	r.ObserveTaskTerminal(task, "wf1", "")
	if n := testutil.CollectAndCount(r.taskQueueSeconds); n != 0 {
		t.Errorf("taskQueueSeconds samples = %d, want 0 for non-terminal state", n)
	}
	if n := testutil.CollectAndCount(r.taskFailures); n != 0 {
		t.Errorf("taskFailures samples = %d, want 0 for non-terminal state", n)
	}
}

func TestNilRegistry_AllMethodsNoOp(t *testing.T) {
	var r *Registry
	task := newTestTask(model.TaskStateFailed, nil, nil, nil)
	sub := &model.Submission{State: model.SubmissionStateCompleted, CreatedAt: time.Now(), CompletedAt: timePtr(time.Now())}
	now := time.Now()

	// None of these must panic on a nil receiver.
	r.ObserveTaskTerminal(task, "wf", "reason")
	r.IncTaskRetry(task, "wf")
	r.AddTasksSkipped(5)
	r.ObserveSubmissionWall(sub)
	r.ObserveStaging("prestage", &now, &now)
	r.ObserveTickPhase("1", time.Second)
	r.RefreshGauges(nil, nil, nil, nil)
	if g := r.Gatherer(); g != nil {
		t.Errorf("Gatherer() on nil Registry = %v, want nil", g)
	}
}

func TestLabelCap_Overflow(t *testing.T) {
	r := NewRegistry(Config{LabelCap: 2})
	base := time.Now().UTC()
	dispatched := timePtr(base)
	started := timePtr(base.Add(time.Second))
	completed := timePtr(base.Add(2 * time.Second))

	for _, wf := range []string{"wf-a", "wf-b", "wf-c", "wf-d"} {
		task := newTestTask(model.TaskStateSuccess, dispatched, started, completed)
		r.ObserveTaskTerminal(task, wf, "")
	}

	// Only 2 distinct workflow values are allowed through; the 3rd and 4th
	// collapse into "_other" — so exactly 3 distinct series exist (wf-a,
	// wf-b, _other), not 4.
	if n := testutil.CollectAndCount(r.taskRunSeconds); n != 3 {
		t.Errorf("distinct taskRunSeconds series = %d, want 3 (2 capped + _other)", n)
	}
	// The two later, over-cap workflows (wf-c, wf-d) both collapsed onto the
	// same "_other" series, so it alone accumulated 2 samples.
	overflow := &dto.Metric{}
	if err := r.taskRunSeconds.WithLabelValues(overflowLabel, "step1", "worker", "grp").(prometheus.Histogram).Write(overflow); err != nil {
		t.Fatalf("write overflow series: %v", err)
	}
	if got := overflow.GetHistogram().GetSampleCount(); got != 2 {
		t.Errorf("_other series sample count = %d, want 2 (wf-c + wf-d)", got)
	}
}

func TestLabelCap_Overflow_ExecutorAndWorkerGroup(t *testing.T) {
	r := NewRegistry(Config{})
	base := time.Now().UTC()
	dispatched := timePtr(base)
	started := timePtr(base.Add(time.Second))
	completed := timePtr(base.Add(2 * time.Second))

	const n = 250
	for i := 0; i < n; i++ {
		task := &model.Task{
			ID:           "task1",
			StepID:       "step1",
			State:        model.TaskStateSuccess,
			ExecutorType: model.ExecutorType(fmt.Sprintf("executor-%d", i)),
			RuntimeHints: &model.RuntimeHints{WorkerGroup: fmt.Sprintf("group-%d", i)},
			DispatchedAt: dispatched,
			StartedAt:    started,
			CompletedAt:  completed,
		}
		r.ObserveTaskTerminal(task, "wf1", "")
	}

	// DefaultLabelCap (200) distinct executor values and 200 distinct
	// worker_group values are let through 1:1 (indices 0-199); the
	// remaining 50 (indices 200-249) collapse BOTH labels into "_other",
	// landing on a single shared overflow series. So exactly 201 distinct
	// series exist: 200 real + 1 overflow.
	if got := testutil.CollectAndCount(r.taskRunSeconds); got != DefaultLabelCap+1 {
		t.Errorf("distinct taskRunSeconds series = %d, want %d (%d capped + _other)", got, DefaultLabelCap+1, DefaultLabelCap)
	}

	overflow := &dto.Metric{}
	if err := r.taskRunSeconds.WithLabelValues("wf1", "step1", overflowLabel, overflowLabel).(prometheus.Histogram).Write(overflow); err != nil {
		t.Fatalf("write overflow series: %v", err)
	}
	if got := overflow.GetHistogram().GetSampleCount(); got != uint64(n-DefaultLabelCap) {
		t.Errorf("_other series sample count = %d, want %d (indices %d..%d)", got, n-DefaultLabelCap, DefaultLabelCap, n-1)
	}

	// A distinct value observed before the cap was reached (index 0) must
	// still pass through as itself, on both labels.
	real := &dto.Metric{}
	if err := r.taskRunSeconds.WithLabelValues("wf1", "step1", "executor-0", "group-0").(prometheus.Histogram).Write(real); err != nil {
		t.Fatalf("write real series: %v", err)
	}
	if got := real.GetHistogram().GetSampleCount(); got != 1 {
		t.Errorf("executor-0/group-0 series sample count = %d, want 1", got)
	}
}

func TestLabelCap_DisabledWorkflowLabel(t *testing.T) {
	r := NewRegistry(Config{DisableWorkflowLabel: true})
	base := time.Now().UTC()
	dispatched := timePtr(base)
	started := timePtr(base.Add(time.Second))
	completed := timePtr(base.Add(2 * time.Second))

	for _, wf := range []string{"wf-a", "wf-b"} {
		task := newTestTask(model.TaskStateSuccess, dispatched, started, completed)
		r.ObserveTaskTerminal(task, wf, "")
	}

	if n := testutil.CollectAndCount(r.taskRunSeconds); n != 1 {
		t.Errorf("distinct taskRunSeconds series = %d, want 1 (all collapse to _all)", n)
	}
}

func TestAddTasksSkipped(t *testing.T) {
	r := NewRegistry(Config{})
	r.AddTasksSkipped(3)
	r.AddTasksSkipped(2)
	if got := testutil.ToFloat64(r.tasksSkipped); got != 5 {
		t.Errorf("tasksSkipped = %v, want 5", got)
	}
	// A non-positive n is a no-op.
	r.AddTasksSkipped(0)
	r.AddTasksSkipped(-1)
	if got := testutil.ToFloat64(r.tasksSkipped); got != 5 {
		t.Errorf("tasksSkipped after no-op adds = %v, want 5", got)
	}
}

func TestObserveSubmissionWall(t *testing.T) {
	r := NewRegistry(Config{})
	created := time.Now().UTC()
	completed := timePtr(created.Add(90 * time.Second))
	sub := &model.Submission{State: model.SubmissionStateCancelled, CreatedAt: created, CompletedAt: completed}
	r.ObserveSubmissionWall(sub)
	if n := testutil.CollectAndCount(r.submissionWallSeconds); n != 1 {
		t.Errorf("submissionWallSeconds samples = %d, want 1", n)
	}
	m := &dto.Metric{}
	if err := r.submissionWallSeconds.WithLabelValues("cancelled").(prometheus.Histogram).Write(m); err != nil {
		t.Fatalf("write outcome=cancelled series: %v", err)
	}
	if got := m.GetHistogram().GetSampleCount(); got != 1 {
		t.Errorf("submissionWallSeconds{outcome=cancelled} sample count = %d, want 1", got)
	}
}

func TestObserveStaging_SkipsWhenStartedNil(t *testing.T) {
	r := NewRegistry(Config{})
	completed := time.Now()
	r.ObserveStaging("poststage", nil, &completed)
	if n := testutil.CollectAndCount(r.stagingSeconds); n != 0 {
		t.Errorf("stagingSeconds samples = %d, want 0 (started nil)", n)
	}
	started := completed.Add(-time.Minute)
	r.ObserveStaging("poststage", &started, &completed)
	if n := testutil.CollectAndCount(r.stagingSeconds); n != 1 {
		t.Errorf("stagingSeconds samples = %d, want 1", n)
	}
}

func TestRefreshGauges_ZeroFillsEnumStates(t *testing.T) {
	r := NewRegistry(Config{})
	r.RefreshGauges(map[string]int{"RUNNING": 3}, map[string]int{"COMPLETED": 1}, nil, nil)

	if got := testutil.ToFloat64(r.tasksGauge.WithLabelValues("RUNNING")); got != 3 {
		t.Errorf("tasks{state=RUNNING} = %v, want 3", got)
	}
	if got := testutil.ToFloat64(r.tasksGauge.WithLabelValues("PENDING")); got != 0 {
		t.Errorf("tasks{state=PENDING} = %v, want 0 (zero-filled)", got)
	}
	if got := testutil.ToFloat64(r.submissionsGauge.WithLabelValues("FAILED")); got != 0 {
		t.Errorf("submissions{state=FAILED} = %v, want 0 (zero-filled)", got)
	}
}

func TestRefreshGauges_WorkersAndQueueDepth(t *testing.T) {
	r := NewRegistry(Config{})
	workers := []*model.Worker{
		{State: model.WorkerStateOnline, Group: "default"},
		{State: model.WorkerStateOnline, Group: "default"},
		{State: model.WorkerStateOffline, Group: "esmfold"},
		{State: model.WorkerStateOnline, Group: ""},
	}
	// A real CountTasksQueuedByGroup already normalizes '' to 'default' in
	// its GROUP BY expression, so the map RefreshGauges receives never has
	// both keys at once; queueDepthGauge's own '' → 'default' mapping is
	// defense in depth, exercised separately below.
	queueDepth := map[string]int{"default": 4, "esmfold": 2}
	r.RefreshGauges(nil, nil, workers, queueDepth)

	if got := testutil.ToFloat64(r.workersGauge.WithLabelValues("online", "default")); got != 3 {
		t.Errorf("workers{state=online,group=default} = %v, want 3 (2 explicit + 1 empty-group)", got)
	}
	if got := testutil.ToFloat64(r.workersGauge.WithLabelValues("offline", "esmfold")); got != 1 {
		t.Errorf("workers{state=offline,group=esmfold} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.queueDepthGauge.WithLabelValues("default")); got != 4 {
		t.Errorf("queue_depth{group=default} = %v, want 4", got)
	}
	if got := testutil.ToFloat64(r.queueDepthGauge.WithLabelValues("esmfold")); got != 2 {
		t.Errorf("queue_depth{group=esmfold} = %v, want 2", got)
	}

	// The defensive '' → 'default' mapping still applies on its own.
	r.RefreshGauges(nil, nil, nil, map[string]int{"": 7})
	if got := testutil.ToFloat64(r.queueDepthGauge.WithLabelValues("default")); got != 7 {
		t.Errorf("queue_depth{group=default} after empty-key refresh = %v, want 7", got)
	}
}

func TestGatherer_ExposesRegisteredMetrics(t *testing.T) {
	r := NewRegistry(Config{})

	// A Prometheus *Vec reports no series (and so is absent from Gather())
	// until at least one label combination has been touched — populate one
	// sample of every metric so this test actually exercises registration,
	// not just NewRegistry's MustRegister not having panicked.
	base := time.Now().UTC()
	task := newTestTask(model.TaskStateFailed, timePtr(base), timePtr(base.Add(time.Second)), timePtr(base.Add(2*time.Second)))
	ms := int64(100)
	task.StageInMs, task.StageOutMs = &ms, &ms
	r.ObserveTaskTerminal(task, "wf1", "poll")
	r.IncTaskRetry(task, "wf1")
	r.AddTasksSkipped(1)
	r.ObserveSubmissionWall(&model.Submission{State: model.SubmissionStateCompleted, CreatedAt: base, CompletedAt: timePtr(base.Add(time.Minute))})
	r.ObserveStaging("prestage", timePtr(base), timePtr(base.Add(time.Second)))
	r.ObserveTickPhase("total", time.Millisecond)
	r.RefreshGauges(map[string]int{"RUNNING": 1}, map[string]int{"RUNNING": 1}, []*model.Worker{{State: model.WorkerStateOnline, Group: "default"}}, map[string]int{"default": 1})

	mfs, err := r.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("gather returned no metric families")
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for _, want := range []string{
		"gowe_task_queue_seconds", "gowe_task_run_seconds", "gowe_task_stage_seconds",
		"gowe_submission_wall_seconds", "gowe_staging_seconds", "gowe_scheduler_tick_seconds",
		"gowe_task_retries_total", "gowe_task_failures_total", "gowe_tasks_skipped_total",
		"gowe_tasks", "gowe_submissions", "gowe_workers", "gowe_queue_depth",
	} {
		if !names[want] {
			t.Errorf("metric %s not registered", want)
		}
	}
}
