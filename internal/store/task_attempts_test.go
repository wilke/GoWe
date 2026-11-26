package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// failedWorkerTask creates a FAILED worker task of sub that ran on workerID
// and left output and an exit code.
func failedWorkerTask(t *testing.T, st *SQLStore, sub *model.Submission, id, workerID string) *model.Task {
	t.Helper()
	tk := sampleTask(sub.ID)
	tk.ID = id
	tk.State = model.TaskStateFailed
	tk.ExecutorType = model.ExecutorTypeWorker
	tk.ExternalID = workerID
	tk.RetryCount = 1
	tk.Stdout = "out of " + id
	tk.Stderr = "error of " + id
	tk.ExitCode = intP(3)
	started := time.Now().UTC().Add(-time.Hour)
	completed := started.Add(10 * time.Minute)
	tk.StartedAt, tk.CompletedAt = &started, &completed
	mustCreateTask(t, st, tk)
	return tk
}

func mustListAttempts(t *testing.T, st *SQLStore, taskID string) []model.TaskAttempt {
	t.Helper()
	got, err := st.ListTaskAttempts(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListTaskAttempts(%s): %v", taskID, err)
	}
	return got
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// archiveTask archives task id's current values as its next attempt through
// the helper every archiving path uses, outside a transaction.
func archiveTask(t *testing.T, st *SQLStore, id, reason string) {
	t.Helper()
	if _, err := st.archiveTaskAttempts(context.Background(), st.db, reason, `t.id = ?`, id); err != nil {
		t.Fatalf("archive task %s: %v", id, err)
	}
}

func TestArchiveTaskAttempts_CopiesFieldsAndNumbers(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	mustCreateWorker(t, st, chargeWorker("wrk_att", func(w *model.Worker) { w.Name = "node-att" }))
	tk := failedWorkerTask(t, st, sub, "task_att", "wrk_att")

	before := time.Now().UTC().Add(-time.Second)
	for i := 1; i <= 3; i++ {
		if i > 1 {
			// The next attempt fails with other output.
			if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET stderr = ?, exit_code = ? WHERE id = ?`,
				"error "+strconv.Itoa(i), i, tk.ID); err != nil {
				t.Fatalf("update task: %v", err)
			}
		}
		archiveTask(t, st, tk.ID, model.AttemptReasonRetry)
	}

	got := mustListAttempts(t, st, tk.ID)
	if len(got) != 3 {
		t.Fatalf("attempts = %d, want 3", len(got))
	}
	for i, a := range got {
		if a.Attempt != i+1 {
			t.Errorf("attempt[%d].Attempt = %d, want %d (ascending)", i, a.Attempt, i+1)
		}
	}
	a := got[0]
	if a.TaskID != tk.ID || a.SubmissionID != sub.ID || a.Reason != model.AttemptReasonRetry ||
		a.State != string(model.TaskStateFailed) || a.Stdout != tk.Stdout || a.Stderr != tk.Stderr ||
		a.ExitCode == nil || *a.ExitCode != 3 || a.ExecutorType != string(model.ExecutorTypeWorker) ||
		a.ExternalID != "wrk_att" || a.WorkerName != "node-att" || a.RetryCount != 1 {
		t.Errorf("attempt 1 = %+v, want the task's values with worker node-att", a)
	}
	if !sameTimePtr(a.StartedAt, tk.StartedAt) || !sameTimePtr(a.CompletedAt, tk.CompletedAt) {
		t.Errorf("attempt 1 times = %v/%v, want %v/%v", a.StartedAt, a.CompletedAt, tk.StartedAt, tk.CompletedAt)
	}
	if a.ArchivedAt.Before(before) {
		t.Errorf("archived_at = %v, want about now", a.ArchivedAt)
	}
	if got[2].Stderr != "error 3" || got[2].ExitCode == nil || *got[2].ExitCode != 3 {
		t.Errorf("attempt 3 = %q exit %v, want \"error 3\" exit 3", got[2].Stderr, ptrStr(got[2].ExitCode))
	}
	if got[1].Stderr != "error 2" || got[1].ExitCode == nil || *got[1].ExitCode != 2 {
		t.Errorf("attempt 2 = %q exit %v, want \"error 2\" exit 2", got[1].Stderr, ptrStr(got[1].ExitCode))
	}
}

func TestArchiveTaskAttempts_NothingToKeep(t *testing.T) {
	st, sub := reconcileFixture(t)

	tk := sampleTask(sub.ID) // PENDING, never started, no output
	mustCreateTask(t, st, tk)
	archiveTask(t, st, tk.ID, model.AttemptReasonReset)
	if got := mustListAttempts(t, st, tk.ID); got == nil || len(got) != 0 {
		t.Errorf("attempts = %#v, want an empty list", got)
	}
	archiveTask(t, st, "task_unknown", model.AttemptReasonReset) // not an error
}

// The history keeps the newest model.MaxTaskAttempts attempts of a task:
// 25 archives leave attempts 6..25, and another task's rows stay.
func TestArchiveTaskAttempts_KeepsNewest(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	tk := failedWorkerTask(t, st, sub, "task_many", "wrk_none")
	other := failedWorkerTask(t, st, sub, "task_other", "wrk_none")

	// The other task holds more rows than the limit (written directly, so
	// no trim ran for it): archiving task_many must not touch them.
	const otherRows = model.MaxTaskAttempts + 10
	for i := 1; i <= otherRows; i++ {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO task_attempts (task_id, attempt, submission_id, reason, state, archived_at) VALUES (?, ?, ?, ?, ?, ?)`,
			other.ID, i, sub.ID, model.AttemptReasonRetry, string(model.TaskStateFailed), formatTime(time.Now().UTC())); err != nil {
			t.Fatalf("insert attempt %d of %s: %v", i, other.ID, err)
		}
	}

	for i := 1; i <= 25; i++ {
		archiveTask(t, st, tk.ID, model.AttemptReasonRetry)
	}

	got := mustListAttempts(t, st, tk.ID)
	if len(got) != model.MaxTaskAttempts {
		t.Fatalf("attempts = %d, want %d", len(got), model.MaxTaskAttempts)
	}
	for i, a := range got {
		if want := 6 + i; a.Attempt != want {
			t.Errorf("attempt[%d] = %d, want %d (6..25)", i, a.Attempt, want)
		}
	}
	if n := len(mustListAttempts(t, st, other.ID)); n != otherRows {
		t.Errorf("attempts of the other task = %d, want %d (untouched)", n, otherRows)
	}
}

// ArchiveAndResetTask archives the row and writes the blanked copy together
// while the row is in from; once the row has left from, it archives nothing
// and writes nothing.
func TestArchiveAndResetTask(t *testing.T) {
	ctx := context.Background()

	t.Run("applied", func(t *testing.T) {
		st, sub := reconcileFixture(t)
		tk := failedWorkerTask(t, st, sub, "task_ar", "wrk_none")

		blank := *tk
		blank.State = model.TaskStateQueued
		blank.Stdout, blank.Stderr, blank.ExitCode = "", "", nil
		blank.StartedAt, blank.CompletedAt = nil, nil
		applied, err := st.ArchiveAndResetTask(ctx, &blank, model.TaskStateFailed, model.AttemptReasonReset)
		if err != nil || !applied {
			t.Fatalf("ArchiveAndResetTask = (%v, %v), want (true, nil)", applied, err)
		}
		got := mustListAttempts(t, st, tk.ID)
		if len(got) != 1 || got[0].Reason != model.AttemptReasonReset || got[0].State != string(model.TaskStateFailed) ||
			got[0].Stderr != tk.Stderr || got[0].ExitCode == nil || *got[0].ExitCode != 3 {
			t.Errorf("attempts = %+v, want one reset attempt with the failed stderr and exit 3", got)
		}
		live, err := st.GetTask(ctx, tk.ID)
		if err != nil || live == nil {
			t.Fatalf("GetTask = (%v, %v)", live, err)
		}
		if live.State != model.TaskStateQueued || live.Stderr != "" || live.Stdout != "" || live.ExitCode != nil ||
			live.StartedAt != nil || live.CompletedAt != nil {
			t.Errorf("task = %s stderr %q stdout %q exit %v started %v completed %v, want QUEUED and blank",
				live.State, live.Stderr, live.Stdout, ptrStr(live.ExitCode), live.StartedAt, live.CompletedAt)
		}
	})

	t.Run("row left from", func(t *testing.T) {
		st, sub := reconcileFixture(t)
		tk := failedWorkerTask(t, st, sub, "task_ar_lost", "wrk_none")
		// A concurrent writer (markRetries) moved the task on.
		if ok, err := st.CASTaskState(ctx, tk.ID, model.TaskStateFailed, model.TaskStateRetrying); err != nil || !ok {
			t.Fatalf("CASTaskState = (%v, %v)", ok, err)
		}

		blank := *tk
		blank.State = model.TaskStateQueued
		blank.Stdout, blank.Stderr, blank.ExitCode = "", "", nil
		applied, err := st.ArchiveAndResetTask(ctx, &blank, model.TaskStateFailed, model.AttemptReasonReset)
		if err != nil || applied {
			t.Fatalf("ArchiveAndResetTask = (%v, %v), want (false, nil)", applied, err)
		}
		if got := mustListAttempts(t, st, tk.ID); len(got) != 0 {
			t.Errorf("attempts = %+v, want none", got)
		}
		live, err := st.GetTask(ctx, tk.ID)
		if err != nil || live == nil {
			t.Fatalf("GetTask = (%v, %v)", live, err)
		}
		if live.State != model.TaskStateRetrying || live.Stderr != tk.Stderr || live.ExitCode == nil {
			t.Errorf("task = %s stderr %q exit %v, want RETRYING with its output (nothing written)",
				live.State, live.Stderr, ptrStr(live.ExitCode))
		}
	})
}

func TestResetFailedTasks_ArchivesAndResets(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	failed := failedWorkerTask(t, st, sub, "task_failed", "wrk_none")

	neverRan := sampleTask(sub.ID)
	neverRan.ID = "task_never_ran"
	neverRan.State = model.TaskStateFailed
	mustCreateTask(t, st, neverRan)

	ok := failedWorkerTask(t, st, sub, "task_ok", "wrk_none")
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET state = 'SUCCESS' WHERE id = ?`, ok.ID); err != nil {
		t.Fatalf("mark success: %v", err)
	}

	n, err := st.ResetFailedTasks(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ResetFailedTasks: %v", err)
	}
	if n != 2 {
		t.Errorf("reset = %d, want 2", n)
	}

	got := mustListAttempts(t, st, failed.ID)
	if len(got) != 1 || got[0].Reason != model.AttemptReasonReset || got[0].State != string(model.TaskStateFailed) ||
		got[0].Stderr != failed.Stderr || got[0].ExitCode == nil || *got[0].ExitCode != 3 {
		t.Errorf("attempts of the failed task = %+v, want one reset attempt with its stderr and exit 3", got)
	}
	if got := mustListAttempts(t, st, neverRan.ID); len(got) != 0 {
		t.Errorf("attempts of the never-run task = %+v, want none", got)
	}
	if got := mustListAttempts(t, st, ok.ID); len(got) != 0 {
		t.Errorf("attempts of the successful task = %+v, want none", got)
	}

	live, err := st.GetTask(ctx, failed.ID)
	if err != nil || live == nil {
		t.Fatalf("GetTask = (%v, %v)", live, err)
	}
	if live.State != model.TaskStatePending || live.Stderr != "" || live.ExitCode != nil ||
		live.StartedAt != nil || live.CompletedAt != nil || live.RetryCount != 0 {
		t.Errorf("reset task = %s stderr %q exit %v started %v completed %v retries %d, want PENDING and blank",
			live.State, live.Stderr, ptrStr(live.ExitCode), live.StartedAt, live.CompletedAt, live.RetryCount)
	}
}

// TestRequeuePaths_ArchiveAttempt: every requeue of a RUNNING worker task
// archives it with the worker's id and name and its reason, and still
// requeues it.
func TestRequeuePaths_ArchiveAttempt(t *testing.T) {
	old := time.Now().UTC().Add(-5 * time.Minute)
	tests := []struct {
		name    string
		worker  func(w *model.Worker)
		requeue func(ctx context.Context, st *SQLStore) error // error also when it requeued the wrong count
		reason  string
	}{
		{
			name: "DeleteWorker",
			requeue: func(ctx context.Context, st *SQLStore) error {
				return st.DeleteWorker(ctx, "wrk_q")
			},
			reason: model.AttemptReasonWorkerRemoved,
		},
		{
			name:   "DeleteOfflineWorker",
			worker: func(w *model.Worker) { w.State, w.LastSeen = model.WorkerStateOffline, old },
			requeue: func(ctx context.Context, st *SQLStore) error {
				deleted, err := st.DeleteOfflineWorker(ctx, "wrk_q", "", time.Minute)
				if err == nil && !deleted {
					return errors.New("DeleteOfflineWorker deleted nothing")
				}
				return err
			},
			reason: model.AttemptReasonWorkerRemoved,
		},
		{
			name: "RequeueWorkerTasks",
			requeue: func(ctx context.Context, st *SQLStore) error {
				n, err := st.RequeueWorkerTasks(ctx, "wrk_q")
				if err == nil && n != 1 {
					return fmt.Errorf("requeued %d, want 1", n)
				}
				return err
			},
			reason: model.AttemptReasonRequeued,
		},
		{
			name:   "ReapStaleWorkers stale worker",
			worker: func(w *model.Worker) { w.LastSeen = old },
			requeue: func(ctx context.Context, st *SQLStore) error {
				reaped, _, err := st.ReapStaleWorkers(ctx, time.Minute)
				if err == nil && (len(reaped) != 1 || reaped[0].Requeued != 1) {
					return fmt.Errorf("reaped = %+v, want wrk_q with 1 task", reaped)
				}
				return err
			},
			reason: model.AttemptReasonWorkerOffline,
		},
		{
			name:   "ReapStaleWorkers stranded task",
			worker: func(w *model.Worker) { w.State, w.LastSeen = model.WorkerStateOffline, old },
			requeue: func(ctx context.Context, st *SQLStore) error {
				_, stranded, err := st.ReapStaleWorkers(ctx, time.Minute)
				if err == nil && stranded != 1 {
					return fmt.Errorf("stranded = %d, want 1", stranded)
				}
				return err
			},
			reason: model.AttemptReasonWorkerOffline,
		},
		{
			name: "ReconcileWorkerTasks",
			requeue: func(ctx context.Context, st *SQLStore) error {
				ids, err := st.ReconcileWorkerTasks(ctx, "wrk_q", nil, time.Minute)
				if err == nil && len(ids) != 1 {
					return fmt.Errorf("reconciled = %v, want [task_q]", ids)
				}
				return err
			},
			reason: model.AttemptReasonRequeued,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st, sub := reconcileFixture(t)
			mustCreateWorker(t, st, chargeWorker("wrk_q", func(w *model.Worker) {
				w.Name = "node-q"
				if tt.worker != nil {
					tt.worker(w)
				}
			}))
			runningTask(t, st, sub, "task_q", "wrk_q", 10*time.Minute)
			if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET stderr = 'partial' WHERE id = 'task_q'`); err != nil {
				t.Fatalf("set stderr: %v", err)
			}

			if err := tt.requeue(ctx, st); err != nil {
				t.Fatalf("requeue: %v", err)
			}

			live, err := st.GetTask(ctx, "task_q")
			if err != nil || live == nil {
				t.Fatalf("GetTask = (%v, %v)", live, err)
			}
			if live.State != model.TaskStateQueued || live.ExternalID != "" || live.StartedAt != nil {
				t.Errorf("task = %s/%q started %v, want QUEUED/\"\" nil", live.State, live.ExternalID, live.StartedAt)
			}

			got := mustListAttempts(t, st, "task_q")
			if len(got) != 1 {
				t.Fatalf("attempts = %+v, want 1", got)
			}
			a := got[0]
			if a.Attempt != 1 || a.Reason != tt.reason || a.State != string(model.TaskStateRunning) ||
				a.ExternalID != "wrk_q" || a.WorkerName != "node-q" || a.StartedAt == nil || a.Stderr != "partial" {
				t.Errorf("attempt = %+v, want #1 %s RUNNING on wrk_q/node-q with started_at and stderr", a, tt.reason)
			}
		})
	}
}

// A DeleteOfflineWorker that deletes nothing (the worker is online) archives
// nothing either: the archive is rolled back with the transaction.
func TestDeleteOfflineWorker_NotDeletedArchivesNothing(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	mustCreateWorker(t, st, chargeWorker("wrk_on", nil))
	runningTask(t, st, sub, "task_on", "wrk_on", 10*time.Minute)

	deleted, err := st.DeleteOfflineWorker(ctx, "wrk_on", "", time.Minute)
	if err != nil || deleted {
		t.Fatalf("DeleteOfflineWorker = (%v, %v), want (false, nil)", deleted, err)
	}
	if got := mustListAttempts(t, st, "task_on"); len(got) != 0 {
		t.Errorf("attempts = %+v, want none", got)
	}
	live, err := st.GetTask(ctx, "task_on")
	if err != nil || live == nil || live.State != model.TaskStateRunning {
		t.Errorf("task = (%v, %v), want RUNNING (untouched)", live, err)
	}
}

func TestDeleteSubmission_DeletesTaskAttempts(t *testing.T) {
	ctx := context.Background()
	st, sub := reconcileFixture(t)
	tk := failedWorkerTask(t, st, sub, "task_del", "wrk_none")
	archiveTask(t, st, tk.ID, model.AttemptReasonRetry)
	if got := mustListAttempts(t, st, tk.ID); len(got) != 1 {
		t.Fatalf("attempts before delete = %d, want 1", len(got))
	}

	if err := st.DeleteSubmission(ctx, sub.ID); err != nil {
		t.Fatalf("DeleteSubmission: %v", err)
	}
	if n := countRows(t, st, "task_attempts"); n != 0 {
		t.Errorf("task_attempts rows after delete = %d, want 0", n)
	}
}
