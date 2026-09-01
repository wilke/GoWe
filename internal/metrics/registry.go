// Package metrics defines GoWe's Prometheus instrumentation: a Registry that
// owns a private prometheus.Registry (never the global default one) and every
// gowe_* metric, plus the observation helpers instrumentation call sites use.
//
// A nil *Registry is always valid — every method on it no-ops — so callers
// throughout the server and scheduler never need a nil guard before calling
// an Observe/Inc/Add method; only the code that WIRES a Registry (cmd/server)
// needs to know whether metrics are enabled.
package metrics

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/me/gowe/pkg/model"
)

// DefaultLabelCap is the default per-label distinct-value cap applied to the
// user-authored "workflow" and "step" label values (M4): once this many
// distinct values have been observed for a label, further distinct values
// collapse into overflowLabel so a shared server cannot be driven into
// unbounded cardinality by workflow/step names it does not control.
const DefaultLabelCap = 200

// overflowLabel is the value substituted for the (cap+1)th and later
// distinct workflow/step label values.
const overflowLabel = "_other"

// allWorkflowsLabel is the workflow label value used for every observation
// when the workflow label is disabled (--metrics-workflow-label=false).
const allWorkflowsLabel = "_all"

// defaultWorkerGroup is substituted for an empty worker_group label, mirroring
// the scheduler/store convention that ” means the default group.
const defaultWorkerGroup = "default"

// Config configures a Registry.
type Config struct {
	// LabelCap bounds the number of distinct values tracked per
	// user-authored label (workflow, step) before further values collapse
	// into "_other". Zero selects DefaultLabelCap.
	LabelCap int

	// DisableWorkflowLabel, when true, replaces every workflow label value
	// with "_all" instead of the real (unbounded, user-authored) workflow
	// name — the --metrics-workflow-label=false escape hatch. The zero
	// value (false) keeps the workflow label enabled, matching the flag's
	// default-on behavior.
	DisableWorkflowLabel bool
}

// durationBuckets spans task/submission/staging durations from ~1 second to
// a day: workflow tasks routinely run from seconds to hours.
var durationBuckets = []float64{
	1, 2, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400,
}

// Registry owns a private Prometheus registry and every gowe_* metric. A nil
// *Registry no-ops every method (see package doc).
type Registry struct {
	reg *prometheus.Registry

	taskQueueSeconds      *prometheus.HistogramVec
	taskRunSeconds        *prometheus.HistogramVec
	taskStageSeconds      *prometheus.HistogramVec
	submissionWallSeconds *prometheus.HistogramVec
	stagingSeconds        *prometheus.HistogramVec
	tickSeconds           *prometheus.HistogramVec

	taskRetries  *prometheus.CounterVec
	taskFailures *prometheus.CounterVec
	tasksSkipped prometheus.Counter

	tasksGauge       *prometheus.GaugeVec
	submissionsGauge *prometheus.GaugeVec
	workersGauge     *prometheus.GaugeVec
	queueDepthGauge  *prometheus.GaugeVec

	workflowLabelEnabled bool
	labelCap             int

	mu              sync.Mutex
	workflowSeen    map[string]struct{}
	stepSeen        map[string]struct{}
	executorSeen    map[string]struct{}
	workerGroupSeen map[string]struct{}
}

// NewRegistry builds a Registry with a private prometheus.Registry (isolated
// from the process-global DefaultRegisterer, so multiple Registries — e.g.
// one per test — never collide) and registers every gowe_* metric on it.
func NewRegistry(cfg Config) *Registry {
	labelCap := cfg.LabelCap
	if labelCap <= 0 {
		labelCap = DefaultLabelCap
	}

	taskLabels := []string{"workflow", "step", "executor", "worker_group"}

	r := &Registry{
		reg: prometheus.NewRegistry(),

		taskQueueSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_task_queue_seconds",
			Help:    "Time a task spent queued: dispatched_at to started_at.",
			Buckets: durationBuckets,
		}, taskLabels),
		taskRunSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_task_run_seconds",
			Help:    "Time a task spent running: started_at to completed_at.",
			Buckets: durationBuckets,
		}, taskLabels),
		taskStageSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_task_stage_seconds",
			Help:    "Worker-measured input/output staging duration for a task.",
			Buckets: durationBuckets,
		}, append([]string{"dir"}, taskLabels...)),
		submissionWallSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_submission_wall_seconds",
			Help:    "Submission wall-clock duration: created_at to completed_at.",
			Buckets: durationBuckets,
		}, []string{"outcome"}),
		stagingSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_staging_seconds",
			Help:    "Submission-level workspace staging duration by phase.",
			Buckets: durationBuckets,
		}, []string{"phase"}),
		tickSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gowe_scheduler_tick_seconds",
			Help:    "Scheduler tick duration by phase, plus phase=\"total\" for the whole tick.",
			Buckets: prometheus.DefBuckets,
		}, []string{"phase"}),

		taskRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gowe_task_retries_total",
			Help: "Total number of FAILED→RETRYING task transitions.",
		}, taskLabels),
		taskFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gowe_task_failures_total",
			Help: "Total number of tasks that reached the FAILED state, by reason.",
		}, append([]string{"reason"}, taskLabels...)),
		tasksSkipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gowe_tasks_skipped_total",
			Help: "Total number of tasks SKIPPED by a cancellation fan-out (bulk-counted, never per-row).",
		}),

		tasksGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gowe_tasks",
			Help: "Current number of tasks by state.",
		}, []string{"state"}),
		submissionsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gowe_submissions",
			Help: "Current number of submissions by state.",
		}, []string{"state"}),
		workersGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gowe_workers",
			Help: "Current number of registered workers by state and group.",
		}, []string{"state", "group"}),
		queueDepthGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gowe_queue_depth",
			Help: "Current number of QUEUED worker tasks by worker group.",
		}, []string{"group"}),

		workflowLabelEnabled: !cfg.DisableWorkflowLabel,
		labelCap:             labelCap,
		workflowSeen:         make(map[string]struct{}),
		stepSeen:             make(map[string]struct{}),
		executorSeen:         make(map[string]struct{}),
		workerGroupSeen:      make(map[string]struct{}),
	}

	r.reg.MustRegister(
		r.taskQueueSeconds, r.taskRunSeconds, r.taskStageSeconds,
		r.submissionWallSeconds, r.stagingSeconds, r.tickSeconds,
		r.taskRetries, r.taskFailures, r.tasksSkipped,
		r.tasksGauge, r.submissionsGauge, r.workersGauge, r.queueDepthGauge,
	)

	return r
}

// Gatherer exposes the private Prometheus registry for the /metrics HTTP
// handler. Returns nil for a nil Registry (callers wiring the listener must
// check for a nil Registry before starting it; this is not on the
// nil-safe-instrumentation path).
func (r *Registry) Gatherer() prometheus.Gatherer {
	if r == nil {
		return nil
	}
	return r.reg
}

// boundedLabel maps v to itself while there is room under the cap (or v was
// already seen), and to overflowLabel once the cap is exceeded. Safe for
// concurrent use across the scheduler goroutine and server handler
// goroutines.
func (r *Registry) boundedLabel(seen map[string]struct{}, v string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := seen[v]; ok {
		return v
	}
	if len(seen) >= r.labelCap {
		return overflowLabel
	}
	seen[v] = struct{}{}
	return v
}

// taskLabelValues resolves the four base task labels: workflow (escape
// hatch + cap), step (cap), executor (cap), and worker_group (empty →
// "default", then cap). executor and worker_group are user-authored via
// gowe:Execution hints (parser.go accepts any string for both), so they get
// their own seen-maps/caps just like workflow and step — otherwise a
// submitter could drive unbounded cardinality through either label.
func (r *Registry) taskLabelValues(workflow, step, executor, workerGroup string) (wf, st, ex, grp string) {
	if r.workflowLabelEnabled {
		wf = r.boundedLabel(r.workflowSeen, workflow)
	} else {
		wf = allWorkflowsLabel
	}
	st = r.boundedLabel(r.stepSeen, step)
	ex = r.boundedLabel(r.executorSeen, executor)
	grp = workerGroup
	if grp == "" {
		grp = defaultWorkerGroup
	}
	grp = r.boundedLabel(r.workerGroupSeen, grp)
	return wf, st, ex, grp
}

// workerGroupOf extracts a task's worker_group runtime hint, defaulting to
// "" (mapped to "default" by taskLabelValues) when absent.
func workerGroupOf(task *model.Task) string {
	if task.RuntimeHints == nil {
		return ""
	}
	return task.RuntimeHints.WorkerGroup
}

// ObserveTaskTerminal records a task's terminal outcome. It is the single
// instrumentation point for gowe_task_queue_seconds, gowe_task_run_seconds,
// gowe_task_stage_seconds, and gowe_task_failures_total, called at every
// site that lands a task in a terminal state after winning the store's CAS
// guard (handleWorkerTaskComplete, persistSubmitOutcome, pollInFlight's
// terminal branch, pollSubworkflowTask's advancement/failure paths,
// detectStuckTasks' fail path).
//
// Per M3 scoping, the DURATION histograms observe SUCCESS/FAILED only, and
// only when task.StartedAt is non-nil (the PR1/PR2 trust rule: a nil
// started_at means the timestamp was never real, e.g. a submit-time failure
// on an async executor). The FAILURES counter is unconditional on
// timestamps — every task that reaches FAILED counts, with reason
// classifying the observation site (e.g. "submit", "poll", "worker",
// "subworkflow", "stuck"); reason is ignored for a SUCCESS outcome.
func (r *Registry) ObserveTaskTerminal(task *model.Task, workflow, reason string) {
	if r == nil || task == nil {
		return
	}
	if task.State != model.TaskStateSuccess && task.State != model.TaskStateFailed {
		return
	}

	wf, step, executor, group := r.taskLabelValues(workflow, task.StepID, string(task.ExecutorType), workerGroupOf(task))

	if task.StartedAt != nil {
		if task.DispatchedAt != nil {
			r.taskQueueSeconds.WithLabelValues(wf, step, executor, group).
				Observe(task.StartedAt.Sub(*task.DispatchedAt).Seconds())
		}
		if task.CompletedAt != nil {
			r.taskRunSeconds.WithLabelValues(wf, step, executor, group).
				Observe(task.CompletedAt.Sub(*task.StartedAt).Seconds())
		}
		if task.StageInMs != nil {
			r.taskStageSeconds.WithLabelValues("in", wf, step, executor, group).
				Observe(float64(*task.StageInMs) / 1000)
		}
		if task.StageOutMs != nil {
			r.taskStageSeconds.WithLabelValues("out", wf, step, executor, group).
				Observe(float64(*task.StageOutMs) / 1000)
		}
	}

	if task.State == model.TaskStateFailed {
		if reason == "" {
			reason = "unknown"
		}
		r.taskFailures.WithLabelValues(reason, wf, step, executor, group).Inc()
	}
}

// IncTaskRetry increments gowe_task_retries_total for a task that just moved
// FAILED→RETRYING.
func (r *Registry) IncTaskRetry(task *model.Task, workflow string) {
	if r == nil || task == nil {
		return
	}
	wf, step, executor, group := r.taskLabelValues(workflow, task.StepID, string(task.ExecutorType), workerGroupOf(task))
	r.taskRetries.WithLabelValues(wf, step, executor, group).Inc()
}

// AddTasksSkipped increments gowe_tasks_skipped_total by n. Callers pass the
// RETURN COUNT of a bulk CancelNonTerminalTasks call (API cancel handler
// fan-out, scheduler cascade) — the metric is deliberately unlabeled because
// a bulk cancel spans many steps/workflows in one call, and counting SKIPs
// per-row is explicitly out of scope (M3). The one per-row exception is a
// sub-workflow proxy task retired to SKIPPED outside CancelNonTerminalTasks
// (which excludes proxies): callers there pass n=1 per retired proxy, gated
// on the store CAS write actually applying.
func (r *Registry) AddTasksSkipped(n int) {
	if r == nil || n <= 0 {
		return
	}
	r.tasksSkipped.Add(float64(n))
}

// ObserveSubmissionWall records gowe_submission_wall_seconds{outcome},
// outcome taken from sub.State (lowercased). Called at every site that
// terminalizes a submission's own row: the scheduler's FinalizeSubmission
// wrapper (normal completion AND the scheduler's cancel cascade) and the
// cancel sequence's own top-level write (outcome="cancelled" — F-M:
// cancelled submissions never pass through the scheduler's finalize path).
func (r *Registry) ObserveSubmissionWall(sub *model.Submission) {
	if r == nil || sub == nil || sub.CompletedAt == nil {
		return
	}
	outcome := strings.ToLower(string(sub.State))
	r.submissionWallSeconds.WithLabelValues(outcome).
		Observe(sub.CompletedAt.Sub(sub.CreatedAt).Seconds())
}

// ObserveStaging records gowe_staging_seconds{phase} for a submission-level
// workspace staging phase ("prestage" or "poststage"). Both started and
// completed must be non-nil (a poststage failure path can stamp
// PoststageCompletedAt with PrestageStartedAt/PoststageStartedAt nil — the
// same started-nil trust rule as tasks applies here).
func (r *Registry) ObserveStaging(phase string, started, completed *time.Time) {
	if r == nil || started == nil || completed == nil {
		return
	}
	r.stagingSeconds.WithLabelValues(phase).Observe(completed.Sub(*started).Seconds())
}

// ObserveTickPhase records gowe_scheduler_tick_seconds{phase}. Phase is one
// of the Loop.Tick phase numbers ("1", "1.5", "2", "2.5", "3", "3.5", "4",
// "5", "5.5", "6") or "total" for the whole tick.
func (r *Registry) ObserveTickPhase(phase string, d time.Duration) {
	if r == nil {
		return
	}
	r.tickSeconds.WithLabelValues(phase).Observe(d.Seconds())
}

// allTaskStates and allSubmissionStates are the fixed enums zero-filled on
// every gauge refresh so a state with zero current rows still reports 0
// instead of disappearing from the metric (distinguishing "0 running" from
// "no data").
var allTaskStates = []model.TaskState{
	model.TaskStatePending, model.TaskStateScheduled, model.TaskStateQueued,
	model.TaskStateRunning, model.TaskStateSuccess, model.TaskStateFailed,
	model.TaskStateRetrying, model.TaskStateSkipped,
}

var allSubmissionStates = []model.SubmissionState{
	model.SubmissionStatePending, model.SubmissionStateRunning,
	model.SubmissionStateCompleted, model.SubmissionStateFailed,
	model.SubmissionStateCancelled,
}

// RefreshGauges sets gowe_tasks, gowe_submissions, gowe_workers, and
// gowe_queue_depth from a fresh per-tick snapshot. taskCounts and subCounts
// key by the raw state string (as returned by CountTasksByState /
// CountSubmissionsByState); queueDepth keys by worker group (” meaning
// default, mapped here like every other worker_group label).
func (r *Registry) RefreshGauges(taskCounts, subCounts map[string]int, workers []*model.Worker, queueDepth map[string]int) {
	if r == nil {
		return
	}
	for _, st := range allTaskStates {
		r.tasksGauge.WithLabelValues(string(st)).Set(float64(taskCounts[string(st)]))
	}
	for _, st := range allSubmissionStates {
		r.submissionsGauge.WithLabelValues(string(st)).Set(float64(subCounts[string(st)]))
	}

	r.workersGauge.Reset()
	for _, w := range workers {
		group := w.Group
		if group == "" {
			group = defaultWorkerGroup
		}
		r.workersGauge.WithLabelValues(string(w.State), group).Inc()
	}

	r.queueDepthGauge.Reset()
	for group, n := range queueDepth {
		if group == "" {
			group = defaultWorkerGroup
		}
		r.queueDepthGauge.WithLabelValues(group).Set(float64(n))
	}
}
