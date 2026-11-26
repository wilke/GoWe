package store

import (
	"context"
	"fmt"
	"time"

	"github.com/me/gowe/pkg/model"
)

// archiveTaskAttemptsSQL copies the current values of the task rows matched
// by the caller's condition (%s, written against the alias t) into
// task_attempts, one new attempt per task. Args: reason, archived_at, then
// the condition's arguments. The parameters in the select list are cast so
// PostgreSQL knows their type. Rows that carry nothing worth keeping (never
// started, no output, no exit code) are skipped.
const archiveTaskAttemptsSQL = `INSERT INTO task_attempts (task_id, attempt, submission_id, reason, state, exit_code,
	    stdout, stderr, executor_type, external_id, worker_name, retry_count, started_at, completed_at, archived_at)
	 SELECT t.id,
	        COALESCE((SELECT MAX(a.attempt) FROM task_attempts a WHERE a.task_id = t.id), 0) + 1,
	        t.submission_id, CAST(? AS TEXT), t.state, t.exit_code,
	        t.stdout, t.stderr, t.executor_type, t.external_id,
	        COALESCE((SELECT w.name FROM workers w WHERE w.id = t.external_id), ''),
	        t.retry_count, t.started_at, t.completed_at, CAST(? AS TEXT)
	 FROM tasks t
	 WHERE (%s)
	   AND (t.started_at IS NOT NULL OR t.stdout != '' OR t.stderr != '' OR t.exit_code IS NOT NULL)`

// trimTaskAttemptsSQL deletes the attempts of the task rows matched by the
// caller's condition (%s, on the alias t) that are older than the newest
// model.MaxTaskAttempts of their task. Args: the condition's arguments, then
// the number to keep.
const trimTaskAttemptsSQL = `DELETE FROM task_attempts
	 WHERE task_id IN (SELECT t.id FROM tasks t WHERE (%s))
	   AND attempt <= (SELECT MAX(a.attempt) FROM task_attempts a WHERE a.task_id = task_attempts.task_id) - CAST(? AS BIGINT)`

// archiveTaskAttempts archives, on ex (the database or a transaction), the
// task rows matching cond (SQL on the alias t, with its arguments args) as
// their next attempt with the given reason, then deletes those tasks'
// attempts beyond the newest model.MaxTaskAttempts. It must run before the
// caller blanks the rows (so cond still matches the same rows for both
// statements), in the caller's transaction where it has one. It returns the
// number of attempts written.
func (s *SQLStore) archiveTaskAttempts(ctx context.Context, ex execer, reason, cond string, args ...any) (int64, error) {
	all := append([]any{reason, formatTime(time.Now().UTC())}, args...)
	res, err := ex.ExecContext(ctx, fmt.Sprintf(archiveTaskAttemptsSQL, cond), all...)
	if err != nil {
		return 0, fmt.Errorf("archive task attempts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("archive task attempts: rows affected: %w", err)
	}
	trimArgs := append(append([]any{}, args...), model.MaxTaskAttempts)
	if _, err := ex.ExecContext(ctx, fmt.Sprintf(trimTaskAttemptsSQL, cond), trimArgs...); err != nil {
		return 0, fmt.Errorf("trim task attempts: %w", err)
	}
	return n, nil
}

// ArchiveAndResetTask keeps the task's current record as its next attempt
// (reason, see model.AttemptReason*) and writes task (the caller's blanked
// copy, the same field set as UpdateTask) in one transaction, both only while
// the row is still in state from: the archive runs under the same condition
// as TerminalizeTaskFrom's compare-and-set update, and an update that matches
// no row rolls the archive back. applied=false (nothing archived, nothing
// written, no error) when the row is no longer in from. A row that holds
// nothing worth keeping (never started, no output, no exit code) is written
// without an attempt.
func (s *SQLStore) ArchiveAndResetTask(ctx context.Context, task *model.Task, from model.TaskState, reason string) (bool, error) {
	s.logger.Debug("sql", "op", "archive_and_reset", "table", "tasks", "id", task.ID, "from", from, "reason", reason)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("archive and reset task %s: begin tx: %w", task.ID, err)
	}
	defer tx.Rollback()

	if _, err := s.archiveTaskAttempts(ctx, tx, reason, `t.id = ? AND t.state = ?`, task.ID, string(from)); err != nil {
		return false, fmt.Errorf("archive and reset task %s: %w", task.ID, err)
	}
	n, err := s.execTaskUpdate(ctx, tx, task, ` WHERE id=? AND state=?`, task.ID, string(from))
	if err != nil {
		return false, fmt.Errorf("archive and reset task %s from %s: %w", task.ID, from, err)
	}
	if n == 0 {
		// The row left from: the deferred Rollback discards the archive.
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("archive and reset task %s: commit: %w", task.ID, err)
	}
	return true, nil
}

// ListTaskAttempts returns the archived earlier attempts of task taskID,
// ordered by attempt ascending (empty, not nil, when there are none).
func (s *SQLStore) ListTaskAttempts(ctx context.Context, taskID string) ([]model.TaskAttempt, error) {
	s.logger.Debug("sql", "op", "list", "table", "task_attempts", "task_id", taskID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, attempt, submission_id, reason, state, exit_code, stdout, stderr,
		        executor_type, external_id, worker_name, retry_count, started_at, completed_at, archived_at
		 FROM task_attempts WHERE task_id = ? ORDER BY attempt`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task attempts: %w", err)
	}
	defer rows.Close()

	out := []model.TaskAttempt{}
	for rows.Next() {
		var a model.TaskAttempt
		var startedAt, completedAt *string
		var archivedAt string
		if err := rows.Scan(&a.TaskID, &a.Attempt, &a.SubmissionID, &a.Reason, &a.State, &a.ExitCode,
			&a.Stdout, &a.Stderr, &a.ExecutorType, &a.ExternalID, &a.WorkerName, &a.RetryCount,
			&startedAt, &completedAt, &archivedAt); err != nil {
			return nil, fmt.Errorf("scan task attempt: %w", err)
		}
		if a.StartedAt, err = scanTimePtr(startedAt); err != nil {
			return nil, err
		}
		if a.CompletedAt, err = scanTimePtr(completedAt); err != nil {
			return nil, err
		}
		if a.ArchivedAt, err = parseTimeOrZero(archivedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task attempts: %w", err)
	}
	return out, nil
}
