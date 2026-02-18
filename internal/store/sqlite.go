package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and returns a Store.
// Use ":memory:" for an in-memory database (useful in tests).
func NewSQLiteStore(dbPath string, logger *slog.Logger) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma wal: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma fk: %w", err)
	}

	return &SQLiteStore{
		db:     db,
		logger: logger.With("component", "store"),
	}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Migrate creates all required tables and indexes.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	s.logger.Debug("sql", "op", "migrate")
	return migrate(ctx, s.db)
}

// --- Workflow CRUD ---

func (s *SQLiteStore) CreateWorkflow(ctx context.Context, wf *model.Workflow) error {
	s.logger.Debug("sql", "op", "insert", "table", "workflows", "id", wf.ID)

	inputsJSON, err := json.Marshal(wf.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}
	outputsJSON, err := json.Marshal(wf.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	stepsJSON, err := json.Marshal(wf.Steps)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflows (id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON),
		wf.CreatedAt.Format(time.RFC3339Nano), wf.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) GetWorkflow(ctx context.Context, id string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select", "table", "workflows", "id", id)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON string
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, created_at, updated_at
		 FROM workflows WHERE id = ?`, id,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(inputsJSON), &wf.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal inputs: %w", err)
	}
	if err := json.Unmarshal([]byte(outputsJSON), &wf.Outputs); err != nil {
		return nil, fmt.Errorf("unmarshal outputs: %w", err)
	}
	if err := json.Unmarshal([]byte(stepsJSON), &wf.Steps); err != nil {
		return nil, fmt.Errorf("unmarshal steps: %w", err)
	}
	wf.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	wf.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	return &wf, nil
}

func (s *SQLiteStore) GetWorkflowByHash(ctx context.Context, hash string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select_by_hash", "table", "workflows", "hash", hash)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON string
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, created_at, updated_at
		 FROM workflows WHERE content_hash = ?`, hash,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(inputsJSON), &wf.Inputs); err != nil {
		return nil, fmt.Errorf("unmarshal inputs: %w", err)
	}
	if err := json.Unmarshal([]byte(outputsJSON), &wf.Outputs); err != nil {
		return nil, fmt.Errorf("unmarshal outputs: %w", err)
	}
	if err := json.Unmarshal([]byte(stepsJSON), &wf.Steps); err != nil {
		return nil, fmt.Errorf("unmarshal steps: %w", err)
	}
	wf.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	wf.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	return &wf, nil
}

func (s *SQLiteStore) ListWorkflows(ctx context.Context, opts model.ListOptions) ([]*model.Workflow, int, error) {
	s.logger.Debug("sql", "op", "list", "table", "workflows", "limit", opts.Limit, "offset", opts.Offset)
	opts.Clamp()

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, created_at, updated_at
		 FROM workflows ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var workflows []*model.Workflow
	for rows.Next() {
		var wf model.Workflow
		var inputsJSON, outputsJSON, stepsJSON string
		var createdAt, updatedAt string

		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
			&inputsJSON, &outputsJSON, &stepsJSON, &createdAt, &updatedAt); err != nil {
			return nil, 0, err
		}
		json.Unmarshal([]byte(inputsJSON), &wf.Inputs)
		json.Unmarshal([]byte(outputsJSON), &wf.Outputs)
		json.Unmarshal([]byte(stepsJSON), &wf.Steps)
		wf.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		wf.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

		workflows = append(workflows, &wf)
	}
	return workflows, total, rows.Err()
}

func (s *SQLiteStore) UpdateWorkflow(ctx context.Context, wf *model.Workflow) error {
	s.logger.Debug("sql", "op", "update", "table", "workflows", "id", wf.ID)

	inputsJSON, err := json.Marshal(wf.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}
	outputsJSON, err := json.Marshal(wf.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	stepsJSON, err := json.Marshal(wf.Steps)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET name=?, description=?, class=?, cwl_version=?, content_hash=?, raw_cwl=?,
		 inputs=?, outputs=?, steps=?, updated_at=? WHERE id=?`,
		wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON),
		wf.UpdatedAt.Format(time.RFC3339Nano), wf.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("workflow %s not found", wf.ID)
	}
	return nil
}

func (s *SQLiteStore) DeleteWorkflow(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "workflows", "id", id)

	result, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("workflow %s not found", id)
	}
	return nil
}

// --- Submission CRUD ---

func (s *SQLiteStore) CreateSubmission(ctx context.Context, sub *model.Submission) error {
	s.logger.Debug("sql", "op", "insert", "table", "submissions", "id", sub.ID)

	inputsJSON, err := json.Marshal(sub.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}
	outputsJSON, err := json.Marshal(sub.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	labelsJSON, err := json.Marshal(sub.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	var completedAt *string
	if sub.CompletedAt != nil {
		s := sub.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO submissions (id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.WorkflowID, sub.WorkflowName, string(sub.State),
		string(inputsJSON), string(outputsJSON), string(labelsJSON),
		sub.SubmittedBy, sub.CreatedAt.Format(time.RFC3339Nano), completedAt,
	)
	return err
}

func (s *SQLiteStore) GetSubmission(ctx context.Context, id string) (*model.Submission, error) {
	s.logger.Debug("sql", "op", "select", "table", "submissions", "id", id)

	var sub model.Submission
	var inputsJSON, outputsJSON, labelsJSON string
	var state, createdAt string
	var completedAt *string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at
		 FROM submissions WHERE id = ?`, id,
	).Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
		&inputsJSON, &outputsJSON, &labelsJSON,
		&sub.SubmittedBy, &createdAt, &completedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sub.State = model.SubmissionState(state)
	json.Unmarshal([]byte(inputsJSON), &sub.Inputs)
	json.Unmarshal([]byte(outputsJSON), &sub.Outputs)
	json.Unmarshal([]byte(labelsJSON), &sub.Labels)
	sub.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if completedAt != nil {
		t, _ := time.Parse(time.RFC3339Nano, *completedAt)
		sub.CompletedAt = &t
	}

	// Load associated tasks.
	tasks, err := s.ListTasksBySubmission(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}
	for _, t := range tasks {
		sub.Tasks = append(sub.Tasks, *t)
	}

	return &sub, nil
}

func (s *SQLiteStore) ListSubmissions(ctx context.Context, opts model.ListOptions) ([]*model.Submission, int, error) {
	s.logger.Debug("sql", "op", "list", "table", "submissions", "limit", opts.Limit, "offset", opts.Offset)
	opts.Clamp()

	// Build WHERE clause dynamically based on filters.
	var whereClauses []string
	var countArgs []any

	if opts.State != "" {
		whereClauses = append(whereClauses, "state = ?")
		countArgs = append(countArgs, opts.State)
	}
	if opts.DateStart != "" {
		whereClauses = append(whereClauses, "created_at >= ?")
		countArgs = append(countArgs, opts.DateStart+"T00:00:00Z")
	}
	if opts.DateEnd != "" {
		whereClauses = append(whereClauses, "created_at <= ?")
		countArgs = append(countArgs, opts.DateEnd+"T23:59:59Z")
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count query.
	var total int
	countQuery := `SELECT COUNT(*) FROM submissions` + whereSQL
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// List query with pagination.
	listQuery := `SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at
		FROM submissions` + whereSQL + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	listArgs := append(countArgs, opts.Limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var subs []*model.Submission
	for rows.Next() {
		var sub model.Submission
		var inputsJSON, outputsJSON, labelsJSON string
		var state, createdAt string
		var completedAt *string

		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
			&inputsJSON, &outputsJSON, &labelsJSON,
			&sub.SubmittedBy, &createdAt, &completedAt); err != nil {
			return nil, 0, err
		}

		sub.State = model.SubmissionState(state)
		json.Unmarshal([]byte(inputsJSON), &sub.Inputs)
		json.Unmarshal([]byte(outputsJSON), &sub.Outputs)
		json.Unmarshal([]byte(labelsJSON), &sub.Labels)
		sub.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if completedAt != nil {
			t, _ := time.Parse(time.RFC3339Nano, *completedAt)
			sub.CompletedAt = &t
		}

		subs = append(subs, &sub)
	}
	return subs, total, rows.Err()
}

func (s *SQLiteStore) UpdateSubmission(ctx context.Context, sub *model.Submission) error {
	s.logger.Debug("sql", "op", "update", "table", "submissions", "id", sub.ID)

	outputsJSON, err := json.Marshal(sub.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	labelsJSON, err := json.Marshal(sub.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	var completedAt *string
	if sub.CompletedAt != nil {
		s := sub.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET state=?, outputs=?, labels=?, completed_at=? WHERE id=?`,
		string(sub.State), string(outputsJSON), string(labelsJSON), completedAt, sub.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("submission %s not found", sub.ID)
	}
	return nil
}

// --- Task operations ---

func (s *SQLiteStore) CreateTask(ctx context.Context, task *model.Task) error {
	s.logger.Debug("sql", "op", "insert", "table", "tasks", "id", task.ID)

	inputsJSON, err := json.Marshal(task.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}
	outputsJSON, err := json.Marshal(task.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	dependsOnJSON, err := json.Marshal(task.DependsOn)
	if err != nil {
		return fmt.Errorf("marshal depends_on: %w", err)
	}

	var startedAt, completedAt *string
	if task.StartedAt != nil {
		s := task.StartedAt.Format(time.RFC3339Nano)
		startedAt = &s
	}
	if task.CompletedAt != nil {
		s := task.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tasks (id, submission_id, step_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.SubmissionID, task.StepID, string(task.State),
		string(task.ExecutorType), task.ExternalID, task.BVBRCAppID,
		string(inputsJSON), string(outputsJSON), string(dependsOnJSON),
		task.RetryCount, task.MaxRetries,
		task.Stdout, task.Stderr, task.ExitCode,
		task.CreatedAt.Format(time.RFC3339Nano), startedAt, completedAt,
	)
	return err
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*model.Task, error) {
	s.logger.Debug("sql", "op", "select", "table", "tasks", "id", id)
	return s.scanTask(s.db.QueryRowContext(ctx,
		`SELECT id, submission_id, step_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at
		 FROM tasks WHERE id = ?`, id))
}

func (s *SQLiteStore) ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list", "table", "tasks", "submission_id", submissionID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at
		 FROM tasks WHERE submission_id = ? ORDER BY created_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

func (s *SQLiteStore) UpdateTask(ctx context.Context, task *model.Task) error {
	s.logger.Debug("sql", "op", "update", "table", "tasks", "id", task.ID)

	outputsJSON, err := json.Marshal(task.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	var startedAt, completedAt *string
	if task.StartedAt != nil {
		v := task.StartedAt.Format(time.RFC3339Nano)
		startedAt = &v
	}
	if task.CompletedAt != nil {
		v := task.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &v
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state=?, executor_type=?, external_id=?,
		 outputs=?, retry_count=?, stdout=?, stderr=?, exit_code=?,
		 started_at=?, completed_at=? WHERE id=?`,
		string(task.State), string(task.ExecutorType), task.ExternalID,
		string(outputsJSON), task.RetryCount,
		task.Stdout, task.Stderr, task.ExitCode,
		startedAt, completedAt, task.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not found", task.ID)
	}
	return nil
}

func (s *SQLiteStore) GetTasksByState(ctx context.Context, state model.TaskState) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list_by_state", "table", "tasks", "state", state)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at
		 FROM tasks WHERE state = ? ORDER BY created_at`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

// --- scan helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func (s *SQLiteStore) scanTask(row scanner) (*model.Task, error) {
	var task model.Task
	var inputsJSON, outputsJSON, dependsOnJSON string
	var state, executorType, createdAt string
	var startedAt, completedAt *string

	err := row.Scan(
		&task.ID, &task.SubmissionID, &task.StepID, &state,
		&executorType, &task.ExternalID, &task.BVBRCAppID,
		&inputsJSON, &outputsJSON, &dependsOnJSON,
		&task.RetryCount, &task.MaxRetries,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&createdAt, &startedAt, &completedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	task.State = model.TaskState(state)
	task.ExecutorType = model.ExecutorType(executorType)
	json.Unmarshal([]byte(inputsJSON), &task.Inputs)
	json.Unmarshal([]byte(outputsJSON), &task.Outputs)
	json.Unmarshal([]byte(dependsOnJSON), &task.DependsOn)
	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if startedAt != nil {
		t, _ := time.Parse(time.RFC3339Nano, *startedAt)
		task.StartedAt = &t
	}
	if completedAt != nil {
		t, _ := time.Parse(time.RFC3339Nano, *completedAt)
		task.CompletedAt = &t
	}

	return &task, nil
}

func (s *SQLiteStore) scanTasks(rows *sql.Rows) ([]*model.Task, error) {
	var tasks []*model.Task
	for rows.Next() {
		var task model.Task
		var inputsJSON, outputsJSON, dependsOnJSON string
		var state, executorType, createdAt string
		var startedAt, completedAt *string

		if err := rows.Scan(
			&task.ID, &task.SubmissionID, &task.StepID, &state,
			&executorType, &task.ExternalID, &task.BVBRCAppID,
			&inputsJSON, &outputsJSON, &dependsOnJSON,
			&task.RetryCount, &task.MaxRetries,
			&task.Stdout, &task.Stderr, &task.ExitCode,
			&createdAt, &startedAt, &completedAt,
		); err != nil {
			return nil, err
		}

		task.State = model.TaskState(state)
		task.ExecutorType = model.ExecutorType(executorType)
		json.Unmarshal([]byte(inputsJSON), &task.Inputs)
		json.Unmarshal([]byte(outputsJSON), &task.Outputs)
		json.Unmarshal([]byte(dependsOnJSON), &task.DependsOn)
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if startedAt != nil {
			t, _ := time.Parse(time.RFC3339Nano, *startedAt)
			task.StartedAt = &t
		}
		if completedAt != nil {
			t, _ := time.Parse(time.RFC3339Nano, *completedAt)
			task.CompletedAt = &t
		}

		tasks = append(tasks, &task)
	}
	return tasks, rows.Err()
}

// --- Session operations ---

func (s *SQLiteStore) CreateSession(ctx context.Context, sess *model.Session) error {
	s.logger.Debug("sql", "op", "insert", "table", "sessions", "id", sess.ID)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, username, role, token, token_exp, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.Username, sess.Role,
		sess.Token, sess.TokenExp.Unix(),
		sess.CreatedAt.Unix(), sess.ExpiresAt.Unix(),
	)
	return err
}

func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*model.Session, error) {
	s.logger.Debug("sql", "op", "select", "table", "sessions", "id", id)

	var sess model.Session
	var tokenExp, createdAt, expiresAt int64

	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, username, role, token, token_exp, created_at, expires_at
		 FROM sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.UserID, &sess.Username, &sess.Role,
		&sess.Token, &tokenExp, &createdAt, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sess.TokenExp = time.Unix(tokenExp, 0)
	sess.CreatedAt = time.Unix(createdAt, 0)
	sess.ExpiresAt = time.Unix(expiresAt, 0)

	return &sess, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "sessions", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	s.logger.Debug("sql", "op", "delete_expired", "table", "sessions")

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLiteStore) DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error) {
	s.logger.Debug("sql", "op", "delete_by_user", "table", "sessions", "user_id", userID)

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// --- Worker operations ---

func (s *SQLiteStore) CreateWorker(ctx context.Context, w *model.Worker) error {
	s.logger.Debug("sql", "op", "insert", "table", "workers", "id", w.ID)

	labelsJSON, err := json.Marshal(w.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workers (id, name, hostname, state, runtime, labels, last_seen, current_task, registered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Hostname, string(w.State), string(w.Runtime),
		string(labelsJSON), w.LastSeen.Format(time.RFC3339Nano),
		w.CurrentTask, w.RegisteredAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) GetWorker(ctx context.Context, id string) (*model.Worker, error) {
	s.logger.Debug("sql", "op", "select", "table", "workers", "id", id)

	var w model.Worker
	var state, runtime, labelsJSON, lastSeen, registeredAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, hostname, state, runtime, labels, last_seen, current_task, registered_at
		 FROM workers WHERE id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.Hostname, &state, &runtime,
		&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	w.State = model.WorkerState(state)
	w.Runtime = model.ContainerRuntime(runtime)
	json.Unmarshal([]byte(labelsJSON), &w.Labels)
	w.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
	w.RegisteredAt, _ = time.Parse(time.RFC3339Nano, registeredAt)

	return &w, nil
}

func (s *SQLiteStore) UpdateWorker(ctx context.Context, w *model.Worker) error {
	s.logger.Debug("sql", "op", "update", "table", "workers", "id", w.ID)

	labelsJSON, err := json.Marshal(w.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workers SET name=?, hostname=?, state=?, runtime=?, labels=?,
		 last_seen=?, current_task=? WHERE id=?`,
		w.Name, w.Hostname, string(w.State), string(w.Runtime),
		string(labelsJSON), w.LastSeen.Format(time.RFC3339Nano),
		w.CurrentTask, w.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker %s not found", w.ID)
	}
	return nil
}

func (s *SQLiteStore) DeleteWorker(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "workers", "id", id)

	result, err := s.db.ExecContext(ctx, `DELETE FROM workers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker %s not found", id)
	}
	return nil
}

func (s *SQLiteStore) ListWorkers(ctx context.Context) ([]*model.Worker, error) {
	s.logger.Debug("sql", "op", "list", "table", "workers")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hostname, state, runtime, labels, last_seen, current_task, registered_at
		 FROM workers ORDER BY registered_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []*model.Worker
	for rows.Next() {
		var w model.Worker
		var state, runtime, labelsJSON, lastSeen, registeredAt string

		if err := rows.Scan(&w.ID, &w.Name, &w.Hostname, &state, &runtime,
			&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt); err != nil {
			return nil, err
		}

		w.State = model.WorkerState(state)
		w.Runtime = model.ContainerRuntime(runtime)
		json.Unmarshal([]byte(labelsJSON), &w.Labels)
		w.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		w.RegisteredAt, _ = time.Parse(time.RFC3339Nano, registeredAt)

		workers = append(workers, &w)
	}
	return workers, rows.Err()
}

// CheckoutTask atomically finds the oldest QUEUED worker task and transitions
// it to RUNNING, assigning it to the given worker. Returns nil if no task is
// available. Runtime capability matching: if runtime is "none", only tasks
// without _docker_image are eligible; otherwise all QUEUED worker tasks match.
func (s *SQLiteStore) CheckoutTask(ctx context.Context, workerID string, runtime model.ContainerRuntime) (*model.Task, error) {
	s.logger.Debug("sql", "op", "checkout_task", "worker_id", workerID, "runtime", runtime)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Find oldest QUEUED task assigned to the worker executor.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at
		 FROM tasks WHERE state = 'QUEUED' AND executor_type = 'worker'
		 ORDER BY created_at LIMIT 10`)
	if err != nil {
		return nil, err
	}

	var candidates []*model.Task
	for rows.Next() {
		var task model.Task
		var inputsJSON, outputsJSON, dependsOnJSON string
		var stateStr, executorType, createdAt string
		var startedAt, completedAt *string

		if err := rows.Scan(
			&task.ID, &task.SubmissionID, &task.StepID, &stateStr,
			&executorType, &task.ExternalID, &task.BVBRCAppID,
			&inputsJSON, &outputsJSON, &dependsOnJSON,
			&task.RetryCount, &task.MaxRetries,
			&task.Stdout, &task.Stderr, &task.ExitCode,
			&createdAt, &startedAt, &completedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}

		task.State = model.TaskState(stateStr)
		task.ExecutorType = model.ExecutorType(executorType)
		json.Unmarshal([]byte(inputsJSON), &task.Inputs)
		json.Unmarshal([]byte(outputsJSON), &task.Outputs)
		json.Unmarshal([]byte(dependsOnJSON), &task.DependsOn)
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if startedAt != nil {
			t, _ := time.Parse(time.RFC3339Nano, *startedAt)
			task.StartedAt = &t
		}
		if completedAt != nil {
			t, _ := time.Parse(time.RFC3339Nano, *completedAt)
			task.CompletedAt = &t
		}

		candidates = append(candidates, &task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Filter by runtime capability.
	var selected *model.Task
	for _, task := range candidates {
		hasImage := false
		if img, ok := task.Inputs["_docker_image"].(string); ok && img != "" {
			hasImage = true
		}
		if runtime == model.RuntimeNone && hasImage {
			continue // Worker can't run container tasks
		}
		selected = task
		break
	}

	if selected == nil {
		return nil, nil
	}

	// Transition to RUNNING and assign to worker.
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		`UPDATE tasks SET state = 'RUNNING', external_id = ?, started_at = ? WHERE id = ? AND state = 'QUEUED'`,
		workerID, nowStr, selected.ID)
	if err != nil {
		return nil, fmt.Errorf("update task state: %w", err)
	}

	// Update worker's current_task.
	_, err = tx.ExecContext(ctx,
		`UPDATE workers SET current_task = ?, last_seen = ? WHERE id = ?`,
		selected.ID, nowStr, workerID)
	if err != nil {
		return nil, fmt.Errorf("update worker current_task: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	selected.State = model.TaskStateRunning
	selected.ExternalID = workerID
	selected.StartedAt = &now

	return selected, nil
}
