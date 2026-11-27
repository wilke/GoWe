package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	return fmt.Sprintf("worker still holds %d running service(s); cancel their submissions first", e.Count)
}

// Is reports whether target is ErrWorkerHasServices.
func (e *WorkerServicesError) Is(target error) bool { return target == ErrWorkerHasServices }

// ErrServiceNotWorker refuses storing a service task (RuntimeHints.Service)
// whose executor is not the worker executor (insertTask).
var ErrServiceNotWorker = errors.New("a service task runs only on the worker executor")

// ErrServiceNotReset refuses blanking a service task for another attempt
// (ArchiveAndResetTask): a service is never re-run.
var ErrServiceNotReset = errors.New("a service task is not reset for another attempt")

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

// failMissingService marks the RUNNING service task id of workerID FAILED
// with ServiceMissingStderr, no exit code and no retries left, through a
// compare-and-set full-row write that applies only while the task is still
// a RUNNING service of workerID (state, is_service and external_id in the
// WHERE clause: a task requeued, re-assigned or finished since the read is
// never overwritten). A task that is no longer a RUNNING service of
// workerID is left alone. It logs one WARN when the write applied.
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
	now := time.Now().UTC()
	task.State = model.TaskStateFailed
	task.Stderr = ServiceMissingStderr
	task.ExitCode = nil
	task.CompletedAt = &now
	task.MaxRetries = task.RetryCount
	task.RuntimeHints.ScrubSecrets()
	n, err := s.execTaskUpdate(ctx, s.db, task,
		` WHERE id=? AND state=? AND is_service = 1 AND external_id=?`,
		task.ID, string(model.TaskStateRunning), workerID)
	if err != nil {
		return fmt.Errorf("fail missing service %s: %w", id, err)
	}
	if n > 0 {
		s.logger.Warn("service task failed: no longer running on its worker (not in the worker's heartbeat)",
			"task_id", id, "worker_id", workerID, "submission_id", task.SubmissionID)
	}
	return nil
}
