package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// A service task (tasks.is_service = 1, a task created with
// RuntimeHints.Service) is never put back in the queue: no requeue path
// matches it, a worker that holds a RUNNING one is not deleted, and a live
// worker whose heartbeats (with running_tasks) omit it twice, the second
// time after the grace window, gets it marked FAILED (ReconcileWorkerTasks).

// ErrWorkerHasServices is matched (errors.Is) by the *WorkerServicesError
// a worker deletion returns when the worker still holds RUNNING service
// tasks.
var ErrWorkerHasServices = errors.New("worker still holds running services")

// WorkerServicesError refuses deleting worker WorkerID: it still holds Count
// RUNNING service tasks. Its message is meant for the caller (HTTP 409).
type WorkerServicesError struct {
	WorkerID string
	Count    int
}

func (e *WorkerServicesError) Error() string {
	return fmt.Sprintf("worker still holds %d running service(s); cancel their submissions, or force-release them if the worker is gone", e.Count)
}

// Is reports whether target is ErrWorkerHasServices.
func (e *WorkerServicesError) Is(target error) bool { return target == ErrWorkerHasServices }

// ErrServiceNotWorker refuses storing a service task (RuntimeHints.Service)
// whose executor is not the worker executor (insertTask).
var ErrServiceNotWorker = errors.New("a service task runs only on the worker executor")

// ErrServiceNotReset refuses blanking a service task for another attempt
// (ArchiveAndResetTask): a service is never re-run.
var ErrServiceNotReset = errors.New("a service task is not reset for another attempt")

// ErrServiceSubmissionGone refuses storing a service task (insertTask)
// whose submission row does not exist or is in a terminal state: a delete or
// cancel committed after the scheduler read the submission, and nothing
// could ever stop or release such a task.
var ErrServiceSubmissionGone = errors.New("the service task's submission no longer exists or has ended")

// checkServiceSubmissionLive returns ErrServiceSubmissionGone (wrapped) when
// submission submissionID does not exist or is terminal, read on q (the
// transaction of the insert that follows).
func checkServiceSubmissionLive(ctx context.Context, q queryer, submissionID string) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM submissions WHERE id = ?`, submissionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("submission %s: %w", submissionID, ErrServiceSubmissionGone)
	}
	if err != nil {
		return fmt.Errorf("read submission %s: %w", submissionID, err)
	}
	if model.SubmissionState(state).IsTerminal() {
		return fmt.Errorf("submission %s is %s: %w", submissionID, state, ErrServiceSubmissionGone)
	}
	return nil
}

// overlappingServiceGPUs is CheckoutTask's GPU host term for worker workerID
// on host hostID with GPU inventory inventory (non-empty): the sum of the
// charged_gpus of RUNNING service tasks held by OTHER worker rows of the same
// host_id, online or offline, whose stored GPU inventory shares at least one
// GPU UUID with inventory. It runs on tx, CheckoutTask's transaction. A row
// whose inventory cannot be decoded shares nothing.
func (s *SQLStore) overlappingServiceGPUs(ctx context.Context, tx *sqlTx, workerID, hostID string, inventory []model.GPUInfo) (int64, error) {
	mine := make(map[string]bool, len(inventory))
	for _, g := range inventory {
		if g.UUID != "" {
			mine[g.UUID] = true
		}
	}
	if len(mine) == 0 {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT w.id, w.gpus, t.charged_gpus FROM tasks t JOIN workers w ON w.id = t.external_id
		 WHERE t.state = 'RUNNING' AND t.executor_type = 'worker' AND t.is_service = 1
		   AND t.external_id != ? AND w.host_id = ? AND t.charged_gpus > 0`, workerID, hostID)
	if err != nil {
		return 0, fmt.Errorf("sum overlapping service GPUs: %w", err)
	}
	defer rows.Close()
	var sum int64
	for rows.Next() {
		var otherID string
		var gpusJSON sql.NullString
		var charged int64
		if err := rows.Scan(&otherID, &gpusJSON, &charged); err != nil {
			return 0, fmt.Errorf("sum overlapping service GPUs: scan: %w", err)
		}
		if !gpusJSON.Valid || gpusJSON.String == "" {
			continue
		}
		var theirs []model.GPUInfo
		if err := json.Unmarshal([]byte(gpusJSON.String), &theirs); err != nil {
			s.logger.Warn("sql", "op", "checkout_task_parse_other_worker_gpus", "worker_id", otherID, "error", err)
			continue
		}
		for _, g := range theirs {
			if mine[g.UUID] {
				sum += charged
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sum overlapping service GPUs: %w", err)
	}
	return sum, nil
}

// ServiceMissingStderr is the stderr of a RUNNING service task marked FAILED
// because its live worker's heartbeat no longer lists it.
const ServiceMissingStderr = "service is no longer running on its worker (not in the worker's heartbeat)"

// ServiceStoppedMissingStderr is the stderr of a RUNNING service task with a
// stop request marked SKIPPED because its live worker's heartbeat no longer
// lists it: it stopped as asked.
const ServiceStoppedMissingStderr = "service stopped after a cancel (no longer in the worker's heartbeat)"

// failMissingService ends the RUNNING service task id of workerID with no
// exit code and no retries left: FAILED with ServiceMissingStderr, or, when
// a stop was requested (service_stop_requested_at), SKIPPED with
// ServiceStoppedMissingStderr. The write is a compare-and-set full-row write
// that applies only while the task is still a RUNNING service of workerID
// (state, is_service and external_id in the WHERE clause: a task requeued,
// re-assigned or finished since the read is never overwritten); the FAILED
// write also requires that no stop was requested since the read (the next
// heartbeat then ends it SKIPPED). A task that is no longer a RUNNING service
// of workerID is left alone. It logs once when the write applied.
func (s *SQLStore) failMissingService(ctx context.Context, workerID, id string) error {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	// The owner is checked by the write itself (external_id in its WHERE
	// clause), which is what makes it safe against a concurrent re-assignment.
	if task == nil || !task.IsService || task.State != model.TaskStateRunning {
		return nil
	}
	stopped := task.ServiceStopRequestedAt != nil
	now := time.Now().UTC()
	task.State = model.TaskStateFailed
	task.Stderr = ServiceMissingStderr
	where := ` WHERE id=? AND state=? AND is_service = 1 AND external_id=? AND service_stop_requested_at IS NULL`
	if stopped {
		task.State = model.TaskStateSkipped
		task.Stderr = ServiceStoppedMissingStderr
		where = ` WHERE id=? AND state=? AND is_service = 1 AND external_id=?`
	}
	task.ExitCode = nil
	task.CompletedAt = &now
	task.MaxRetries = task.RetryCount
	task.RuntimeHints.ScrubSecrets()
	n, err := s.execTaskUpdate(ctx, s.db, task, where,
		task.ID, string(model.TaskStateRunning), workerID)
	if err != nil {
		return fmt.Errorf("fail missing service %s: %w", id, err)
	}
	switch {
	case n > 0 && stopped:
		s.logger.Info("service task stopped after a cancel: no longer running on its worker (not in the worker's heartbeat)",
			"task_id", id, "worker_id", workerID, "submission_id", task.SubmissionID)
	case n > 0:
		s.logger.Warn("service task failed: no longer running on its worker (not in the worker's heartbeat)",
			"task_id", id, "worker_id", workerID, "submission_id", task.SubmissionID)
	}
	return nil
}

// ErrSubmissionHasRunningService refuses deleting a submission (or one with
// a descendant child submission) that has a RUNNING service task: its worker
// would never be told to stop it and its charges would vanish. Its message is
// meant for the caller (HTTP 409).
var ErrSubmissionHasRunningService = errors.New("submission has a running service; cancel it and wait until it has stopped, or force-release it")

// DeleteRefusedServiceStopping is the 409 message of a submission delete
// that DeleteSubmission refused (ErrSubmissionHasRunningService) after the
// delete handler had already cancelled its steps and tasks (a service was
// checked out after the handler's own check): the handler then completes the
// cancel, so the submission is CANCELLED and its service is asked to stop.
const DeleteRefusedServiceStopping = "submission has a running service, so it was not deleted: it was cancelled and its service is stopping; delete it once the service has stopped"

// DeleteRefusedMessage is the 409 message of such a refused delete, built
// from the state of the submission as re-read after the handler's cancel
// sequence: DeleteRefusedServiceStopping when it is CANCELLED; otherwise (the
// scheduler, say, ended it first, so the cancel did not apply) a message
// naming its real state.
func DeleteRefusedMessage(state model.SubmissionState) string {
	if state == model.SubmissionStateCancelled {
		return DeleteRefusedServiceStopping
	}
	return fmt.Sprintf("submission has a running service that is being stopped; the submission is %s; delete it again once the service has stopped", state)
}

// CountStoppingServices implements Store.
func (s *SQLStore) CountStoppingServices(ctx context.Context, submissionID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks
		 WHERE submission_id = ? AND state = 'RUNNING' AND is_service = 1 AND executor_type = 'worker'
		   AND service_stop_requested_at IS NOT NULL`, submissionID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count stopping services of %s: %w", submissionID, err)
	}
	return n, nil
}

// HasRunningService implements Store.
func (s *SQLStore) HasRunningService(ctx context.Context, submissionID string) (bool, error) {
	return hasRunningService(ctx, s.db, submissionID)
}

// hasRunningService reports, on q (the database or a transaction), whether a
// task of submission id or of any descendant child submission (through the
// parent_task_id of a child pointing at a task of its parent) is a RUNNING
// service. Each row set is read and closed before the next query, so it runs
// on a transaction's single connection too.
func hasRunningService(ctx context.Context, q queryer, id string) (bool, error) {
	visited := map[string]bool{}
	pending := []string{id}
	for len(pending) > 0 {
		subID := pending[0]
		pending = pending[1:]
		if visited[subID] {
			continue
		}
		visited[subID] = true

		var n int
		if err := q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tasks WHERE submission_id = ? AND state = 'RUNNING' AND is_service = 1`, subID).
			Scan(&n); err != nil {
			return false, fmt.Errorf("count running services of %s: %w", subID, err)
		}
		if n > 0 {
			return true, nil
		}

		rows, err := q.QueryContext(ctx,
			`SELECT id FROM submissions
			 WHERE parent_task_id IN (SELECT id FROM tasks WHERE submission_id = ?) AND parent_task_id != ''`, subID)
		if err != nil {
			return false, fmt.Errorf("list child submissions of %s: %w", subID, err)
		}
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				rows.Close()
				return false, fmt.Errorf("list child submissions of %s: scan: %w", subID, err)
			}
			pending = append(pending, child)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("list child submissions of %s: %w", subID, err)
		}
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("list child submissions of %s: %w", subID, err)
		}
	}
	return false, nil
}

// marshalServiceStatus is the tasks.service_status value of st: its JSON
// document, or nil (NULL) for no status.
func marshalServiceStatus(st *model.ServiceStatus) (*string, error) {
	if st == nil {
		return nil, nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("marshal service_status: %w", err)
	}
	doc := string(b)
	return &doc, nil
}

// scanServiceStatus decodes the tasks.service_status value raw into
// task.ServiceStatus and applies the shown state
// (model.Task.ApplyShownServiceState), so every reader of a task (API, SSE,
// web UI, worker checkout) sees the same rewritten state; the stored document
// is not changed. task.State, ServiceLostAt and ServiceStopRequestedAt must
// already be set. A document that cannot be decoded is logged and dropped: it
// never hides the task. A JSON null, or a document without a state (such as
// {}), is no status: the server never stores one, and a status without a
// state shows nothing.
func (s *SQLStore) scanServiceStatus(task *model.Task, raw *string) {
	if raw == nil || *raw == "" {
		return
	}
	var st model.ServiceStatus
	if err := json.Unmarshal([]byte(*raw), &st); err != nil {
		s.logger.Warn("ignoring an undecodable service_status", "task_id", task.ID, "error", err)
		return
	}
	if st.State == "" {
		return
	}
	task.ServiceStatus = &st
	task.ApplyShownServiceState()
}

// serviceStatusMatch is the condition a task must meet for its worker's
// report to be stored: a RUNNING worker service checked out by that worker.
const serviceStatusMatch = ` WHERE id = ? AND state = 'RUNNING' AND is_service = 1 AND executor_type = 'worker' AND external_id = ?`

// UpdateServiceStatuses implements Store. In one transaction, each report
// is stored as the tasks.service_status of its task, with UpdatedAt = now,
// only while the task is a RUNNING worker service of workerID; any other
// report (unknown id, another worker's task, not RUNNING, not a service)
// changes nothing and its task id is returned in ignored. ReadyAt is sticky:
// a stored ready_at is kept whatever the report says. Without one, a report
// in state ready sets it: to the report's ready_at, or to now when it carries
// none or one earlier than the task's started_at; a report in any other state
// sets none (its ready_at is dropped). The caller validates the reports
// (model.ServiceStatus.Validate) and removes duplicate task ids.
func (s *SQLStore) UpdateServiceStatuses(ctx context.Context, workerID string, reports []model.ServiceReport, now time.Time) ([]string, error) {
	if len(reports) == 0 {
		return nil, nil
	}
	s.logger.Debug("sql", "op", "update_service_statuses", "worker_id", workerID, "count", len(reports))
	now = now.UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("update service statuses: begin tx: %w", err)
	}
	defer tx.Rollback()

	var ignored []string
	for _, r := range reports {
		var raw, startedRaw *string
		err := tx.QueryRowContext(ctx, `SELECT service_status, started_at FROM tasks`+serviceStatusMatch, r.TaskID, workerID).
			Scan(&raw, &startedRaw)
		if errors.Is(err, sql.ErrNoRows) {
			ignored = append(ignored, r.TaskID)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("update service statuses: read %s: %w", r.TaskID, err)
		}

		st := r.ServiceStatus
		st.UpdatedAt = now
		var stored model.ServiceStatus
		if raw != nil && *raw != "" {
			if err := json.Unmarshal([]byte(*raw), &stored); err != nil {
				s.logger.Warn("replacing an undecodable service_status", "task_id", r.TaskID, "error", err)
				stored = model.ServiceStatus{}
			}
		}
		reported := st.ReadyAt
		st.ReadyAt = nil
		switch {
		case stored.ReadyAt != nil:
			readyAt := stored.ReadyAt.UTC()
			st.ReadyAt = &readyAt
		case st.State == model.ServiceStateReady:
			readyAt := now
			if reported != nil {
				readyAt = reported.UTC()
				started, err := scanTimePtr(startedRaw)
				if err != nil {
					s.logger.Warn("update service statuses: ignoring an unreadable started_at", "task_id", r.TaskID, "error", err)
				}
				if started != nil && readyAt.Before(*started) {
					readyAt = now
				}
			}
			st.ReadyAt = &readyAt
		}
		doc, err := marshalServiceStatus(&st)
		if err != nil {
			return nil, fmt.Errorf("update service statuses: %s: %w", r.TaskID, err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET service_status = ?`+serviceStatusMatch, doc, r.TaskID, workerID)
		if err != nil {
			return nil, fmt.Errorf("update service statuses: write %s: %w", r.TaskID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			ignored = append(ignored, r.TaskID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("update service statuses: commit: %w", err)
	}
	return ignored, nil
}

// ErrTaskNotFound is returned (wrapped) by ForceReleaseService when the task
// does not exist or belongs to another submission.
var ErrTaskNotFound = errors.New("task not found")

// ErrServiceNotReleasable is matched (errors.Is) by the *ServiceReleaseError
// ForceReleaseService returns when the task may not be force-released.
var ErrServiceNotReleasable = errors.New("service task cannot be force-released")

// ServiceReleaseError refuses a force release of task TaskID; Reason says
// why and is meant for the caller (HTTP 409).
type ServiceReleaseError struct {
	TaskID string
	Reason string
}

func (e *ServiceReleaseError) Error() string { return e.Reason }

// Is reports whether target is ErrServiceNotReleasable.
func (e *ServiceReleaseError) Is(target error) bool { return target == ErrServiceNotReleasable }

// The reasons of a refused force release. ReleaseWorkerOnline names
// model.ServiceStopReleaseAfter (15 minutes).
const (
	ReleaseNotService   = "the task is not a service task"
	ReleaseNotRunning   = "the service task is not RUNNING"
	ReleaseWorkerOnline = "the service's worker is online; cancel the submission, then wait 15 minutes or until the worker is marked lost"
	ReleaseStateChanged = "the service task or its worker changed concurrently; read it again"
)

// ForceReleasedStderr is the stderr line of a service task force-released by
// actor.
func ForceReleasedStderr(actor string) string {
	return fmt.Sprintf("force-released by %s: the worker did not confirm that the service stopped; its process and GPUs may still be in use", actor)
}

// ForceReleaseNotifyWorker is appended ("; " + it) to ForceReleasedStderr
// when the service was released while its worker row exists and is not
// lost (a stop request older than model.ServiceStopReleaseAfter, or the
// submission row is gone): the worker may be online.
const ForceReleaseNotifyWorker = "the worker will be told to stop it on its next heartbeat"

// ForceReleaseService implements Store. The caller checks who may do it; this
// method checks, in the same transaction as the write, that the task belongs
// to submissionID, is a RUNNING service and that its worker cannot confirm a
// stop, or has not for too long: service_lost_at is set, the worker row
// (external_id) does not exist, service_stop_requested_at is older than
// model.ServiceStopReleaseAfter, or the submission row submissionID does not
// exist (the caller allows that only to a server admin). The write is a
// compare-and-set from RUNNING with the same external_id that repeats those
// conditions; the task ends FAILED (SKIPPED when service_stop_requested_at
// is set) with no exit code and no retries left, ForceReleasedStderr(actor)
// appended to stderr (with ForceReleaseNotifyWorker when the worker row
// exists and is not lost) and its secrets scrubbed. Its charges then no
// longer count. The worker's current_task is moved off the task when the row
// exists.
func (s *SQLStore) ForceReleaseService(ctx context.Context, submissionID, taskID, actor string) (*model.Task, error) {
	s.logger.Debug("sql", "op", "force_release_service", "submission_id", submissionID, "task_id", taskID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("force release: begin tx: %w", err)
	}
	defer tx.Rollback()

	task, err := s.scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
	if err != nil {
		return nil, fmt.Errorf("force release: read task %s: %w", taskID, err)
	}
	if task == nil || task.SubmissionID != submissionID {
		return nil, fmt.Errorf("force release %s: %w", taskID, ErrTaskNotFound)
	}
	refuse := func(reason string) (*model.Task, error) {
		return nil, &ServiceReleaseError{TaskID: taskID, Reason: reason}
	}
	if !task.IsService {
		return refuse(ReleaseNotService)
	}
	if task.State != model.TaskStateRunning {
		return refuse(ReleaseNotRunning)
	}
	now := time.Now().UTC()
	stopCutoff := now.Add(-model.ServiceStopReleaseAfter)
	var workers, subs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workers WHERE id = ?`, task.ExternalID).
		Scan(&workers); err != nil {
		return nil, fmt.Errorf("force release: read worker %s: %w", task.ExternalID, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE id = ?`, submissionID).
		Scan(&subs); err != nil {
		return nil, fmt.Errorf("force release: read submission %s: %w", submissionID, err)
	}
	lost, rowGone := task.ServiceLostAt != nil, workers == 0
	stopOld := task.ServiceStopRequestedAt != nil && task.ServiceStopRequestedAt.Before(stopCutoff)
	if !lost && !rowGone && !stopOld && subs > 0 {
		return refuse(ReleaseWorkerOnline)
	}

	workerID := task.ExternalID
	task.State = model.TaskStateFailed
	if task.ServiceStopRequestedAt != nil {
		task.State = model.TaskStateSkipped
	}
	if task.Stderr != "" && !strings.HasSuffix(task.Stderr, "\n") {
		task.Stderr += "\n"
	}
	task.Stderr += ForceReleasedStderr(actor)
	if !lost && !rowGone {
		task.Stderr += "; " + ForceReleaseNotifyWorker
	}
	task.ExitCode = nil
	task.CompletedAt = &now
	task.MaxRetries = task.RetryCount
	task.RuntimeHints.ScrubSecrets()
	// The release conditions are part of the write too: a worker that
	// heartbeats in between (TouchWorker clears service_lost_at) wins unless
	// another condition still holds.
	n, err := s.execTaskUpdate(ctx, tx, task,
		` WHERE id=? AND state=? AND is_service = 1 AND external_id=?
		   AND (service_lost_at IS NOT NULL
		        OR NOT EXISTS (SELECT 1 FROM workers WHERE id = ?)
		        OR (service_stop_requested_at IS NOT NULL AND service_stop_requested_at < ?)
		        OR NOT EXISTS (SELECT 1 FROM submissions WHERE id = ?))`,
		task.ID, string(model.TaskStateRunning), workerID, workerID, formatTime(stopCutoff), submissionID)
	if err != nil {
		return nil, fmt.Errorf("force release %s: %w", taskID, err)
	}
	if n == 0 {
		return refuse(ReleaseStateChanged)
	}
	if _, err := tx.ExecContext(ctx, releaseWorkerTaskSQL,
		workerID, task.ID, workerID, task.ID, workerID); err != nil {
		return nil, fmt.Errorf("force release: clear current_task of %s: %w", workerID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("force release: commit: %w", err)
	}
	return task, nil
}
