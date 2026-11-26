package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// A worker task that fails and is retried keeps the failed attempt (reason
// retry, its stderr, exit code and worker) in task_attempts, and the live
// task row is blank again for the new attempt.
func TestResubmitRetrying_RecordsAttempt(t *testing.T) {
	ctx := context.Background()
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	sched.SetGroupPolicy(loadTestGroupPolicy(t, st, testPolicy{}))
	subID := registerGraphWorkflowAs(t, st, groupNoHintCWL, nil, "alice")
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks := hintsByStep(t, sched, subID)["s"]
	if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
		t.Fatalf("setup: tasks = %+v, want one QUEUED task", tasks)
	}
	task := tasks[0]
	if task.RetryCount >= task.MaxRetries {
		t.Fatalf("setup: task has no retries left (%d/%d)", task.RetryCount, task.MaxRetries)
	}

	// The attempt runs on a worker and fails, as the worker's report writes it.
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	exit := 2
	task.State = model.TaskStateFailed
	task.ExternalID = "wrk_attempt"
	task.Stderr = "boom: out of memory"
	task.Stdout = "partial"
	task.ExitCode = &exit
	task.StartedAt, task.CompletedAt = &started, &completed
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("fail task: %v", err)
	}

	// Tick 1: markRetries (FAILED→RETRYING). Tick 2: resubmitRetrying.
	for i := 0; i < 2; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	live, err := st.GetTask(ctx, task.ID)
	if err != nil || live == nil {
		t.Fatalf("GetTask = (%v, %v)", live, err)
	}
	if live.State != model.TaskStateQueued || live.RetryCount != 1 {
		t.Fatalf("task = %s retry %d (%q), want QUEUED after one retry", live.State, live.RetryCount, live.Stderr)
	}
	if live.Stderr != "" || live.Stdout != "" || live.ExitCode != nil || live.StartedAt != nil || live.CompletedAt != nil {
		t.Errorf("live task = stderr %q stdout %q exit %v started %v completed %v, want blank",
			live.Stderr, live.Stdout, live.ExitCode, live.StartedAt, live.CompletedAt)
	}

	attempts, err := st.ListTaskAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTaskAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %+v, want 1", attempts)
	}
	a := attempts[0]
	if a.Attempt != 1 || a.Reason != model.AttemptReasonRetry || a.Stderr != "boom: out of memory" ||
		a.Stdout != "partial" || a.ExitCode == nil || *a.ExitCode != 2 || a.ExternalID != "wrk_attempt" ||
		a.RetryCount != 0 || a.StartedAt == nil || !a.StartedAt.Equal(started) {
		t.Errorf("attempt = %+v, want #1 retry with the failed stderr, stdout, exit 2, worker and start", a)
	}
}

// retryingWorkerTask dispatches a worker task, lets its attempt fail on a
// worker (output, exit code, times) and marks it RETRYING as markRetries
// does, so resubmitRetrying picks it up next.
func retryingWorkerTask(t *testing.T) (*Loop, store.Store, *model.Task) {
	t.Helper()
	ctx := context.Background()
	sched, st := testSetup(t)
	queueOnlyWorkerExecutor(sched, st)
	sched.SetGroupPolicy(loadTestGroupPolicy(t, st, testPolicy{}))
	subID := registerGraphWorkflowAs(t, st, groupNoHintCWL, nil, "alice")
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks := hintsByStep(t, sched, subID)["s"]
	if len(tasks) != 1 || tasks[0].State != model.TaskStateQueued {
		t.Fatalf("setup: tasks = %+v, want one QUEUED task", tasks)
	}
	task := tasks[0]
	if task.RetryCount != 0 || task.MaxRetries < 1 {
		t.Fatalf("setup: task retries %d/%d, want 0 of at least 1", task.RetryCount, task.MaxRetries)
	}

	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	exit := 2
	task.State = model.TaskStateFailed
	task.ExternalID = "wrk_attempt"
	task.Stderr = "boom: out of memory"
	task.Stdout = "partial"
	task.ExitCode = &exit
	task.StartedAt, task.CompletedAt = &started, &completed
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("fail task: %v", err)
	}
	if ok, err := st.CASTaskState(ctx, task.ID, model.TaskStateFailed, model.TaskStateRetrying); err != nil || !ok {
		t.Fatalf("mark retrying = (%v, %v)", ok, err)
	}
	return sched, st, task
}

// runResubmitRetrying runs the retry phase once, as a tick does.
func runResubmitRetrying(t *testing.T, sched *Loop) {
	t.Helper()
	sched.cache = newTickCache()
	if err := sched.resubmitRetrying(context.Background(), map[string]bool{}); err != nil {
		t.Fatalf("resubmitRetrying: %v", err)
	}
}

func mustAttempts(t *testing.T, st store.Store, taskID string) []model.TaskAttempt {
	t.Helper()
	got, err := st.ListTaskAttempts(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListTaskAttempts: %v", err)
	}
	return got
}

func mustTask(t *testing.T, st store.Store, taskID string) *model.Task {
	t.Helper()
	live, err := st.GetTask(context.Background(), taskID)
	if err != nil || live == nil {
		t.Fatalf("GetTask(%s) = (%v, %v)", taskID, live, err)
	}
	return live
}

// crashAfterResetStore runs ArchiveAndResetTask, notes the retry_count the
// row holds right after it, and then fails, as if the scheduler stopped
// between the reset write and the submit.
type crashAfterResetStore struct {
	store.Store
	calls                int
	retryCountAfterReset int
}

func (s *crashAfterResetStore) ArchiveAndResetTask(ctx context.Context, task *model.Task, from model.TaskState, reason string) (bool, error) {
	s.calls++
	applied, err := s.Store.ArchiveAndResetTask(ctx, task, from, reason)
	if err != nil || !applied {
		return applied, err
	}
	live, err := s.Store.GetTask(ctx, task.ID)
	if err != nil || live == nil {
		return false, fmt.Errorf("re-read task %s: (%v, %v)", task.ID, live, err)
	}
	s.retryCountAfterReset = live.RetryCount
	return false, errors.New("simulated stop after the reset write")
}

// The reset write keeps the old retry count, so a stop between it and the
// submit costs the retry once, not twice: the next pass reclaims the blanked
// SCHEDULED row, archives nothing more, and counts the retry once.
func TestResubmitRetrying_StopAfterResetCountsRetryOnce(t *testing.T) {
	sched, st, task := retryingWorkerTask(t)

	crash := &crashAfterResetStore{Store: st}
	sched.store = crash
	runResubmitRetrying(t, sched)
	if crash.calls != 1 {
		t.Fatalf("ArchiveAndResetTask calls = %d, want 1", crash.calls)
	}
	if crash.retryCountAfterReset != 0 {
		t.Errorf("retry_count after the reset write = %d, want 0 (not persisted by that write)", crash.retryCountAfterReset)
	}
	live := mustTask(t, st, task.ID)
	if live.State != model.TaskStateScheduled || live.RetryCount != 0 || live.Stderr != "" || live.ExitCode != nil {
		t.Fatalf("task after the stop = %s retry %d stderr %q, want SCHEDULED, retry 0, blank",
			live.State, live.RetryCount, live.Stderr)
	}
	if got := mustAttempts(t, st, task.ID); len(got) != 1 {
		t.Fatalf("attempts after the stop = %+v, want 1", got)
	}

	// The restarted scheduler's next pass.
	sched.store = st
	runResubmitRetrying(t, sched)
	live = mustTask(t, st, task.ID)
	if live.State != model.TaskStateQueued || live.RetryCount != 1 {
		t.Errorf("task = %s retry %d, want QUEUED after exactly one counted retry", live.State, live.RetryCount)
	}
	got := mustAttempts(t, st, task.ID)
	if len(got) != 1 || got[0].Stderr != "boom: out of memory" || got[0].RetryCount != 0 {
		t.Errorf("attempts = %+v, want only the failed attempt (the blanked row archives nothing)", got)
	}
}

// cancelBeforeResetStore SKIPs the task, as a concurrent cancel does, right
// before ArchiveAndResetTask runs.
type cancelBeforeResetStore struct {
	store.Store
}

func (s *cancelBeforeResetStore) ArchiveAndResetTask(ctx context.Context, task *model.Task, from model.TaskState, reason string) (bool, error) {
	if ok, err := s.Store.CASTaskState(ctx, task.ID, from, model.TaskStateSkipped); err != nil || !ok {
		return false, fmt.Errorf("cancel task %s: (%v, %v)", task.ID, ok, err)
	}
	return s.Store.ArchiveAndResetTask(ctx, task, from, reason)
}

// A retry whose task was cancelled between the claim and the reset records
// no attempt and writes nothing.
func TestResubmitRetrying_CancelledBeforeResetRecordsNoAttempt(t *testing.T) {
	sched, st, task := retryingWorkerTask(t)

	sched.store = &cancelBeforeResetStore{Store: st}
	runResubmitRetrying(t, sched)

	live := mustTask(t, st, task.ID)
	if live.State != model.TaskStateSkipped || live.RetryCount != 0 || live.Stderr != "boom: out of memory" {
		t.Errorf("task = %s retry %d stderr %q, want SKIPPED and untouched", live.State, live.RetryCount, live.Stderr)
	}
	if got := mustAttempts(t, st, task.ID); len(got) != 0 {
		t.Errorf("attempts = %+v, want none", got)
	}
}
