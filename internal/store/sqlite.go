package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"

	_ "modernc.org/sqlite"
)

// unmarshalJSON unmarshals a JSON string into dest. field is used in the error message.
func unmarshalJSON(data string, dest any, field string) error {
	if data == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(data), dest); err != nil {
		return fmt.Errorf("corrupt %s JSON: %w", field, err)
	}
	return nil
}

// parseTimeOrZero parses an RFC3339Nano time string, returning zero time for empty strings.
func parseTimeOrZero(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("corrupt timestamp %q: %w", value, err)
	}
	return t, nil
}

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

	// Configure connection pool for SQLite.
	// SQLite handles one writer at a time, so limit connections to avoid "database is locked" errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

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
	labelsJSON, err := json.Marshal(wf.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflows (id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON), string(labelsJSON),
		wf.CreatedAt.Format(time.RFC3339Nano), wf.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) GetWorkflow(ctx context.Context, id string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select", "table", "workflows", "id", id)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_at, updated_at
		 FROM workflows WHERE id = ?`, id,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := unmarshalJSON(inputsJSON, &wf.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &wf.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(stepsJSON, &wf.Steps, "steps"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &wf.Labels, "labels"); err != nil {
		return nil, err
	}
	if wf.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if wf.UpdatedAt, err = parseTimeOrZero(updatedAt); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLiteStore) GetWorkflowByHash(ctx context.Context, hash string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select_by_hash", "table", "workflows", "hash", hash)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_at, updated_at
		 FROM workflows WHERE content_hash = ?`, hash,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := unmarshalJSON(inputsJSON, &wf.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &wf.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(stepsJSON, &wf.Steps, "steps"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &wf.Labels, "labels"); err != nil {
		return nil, err
	}
	if wf.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if wf.UpdatedAt, err = parseTimeOrZero(updatedAt); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLiteStore) GetWorkflowByName(ctx context.Context, name string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select_by_name", "table", "workflows", "name", name)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_at, updated_at
		 FROM workflows WHERE name = ? ORDER BY created_at DESC LIMIT 1`, name,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := unmarshalJSON(inputsJSON, &wf.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &wf.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(stepsJSON, &wf.Steps, "steps"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &wf.Labels, "labels"); err != nil {
		return nil, err
	}
	if wf.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if wf.UpdatedAt, err = parseTimeOrZero(updatedAt); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLiteStore) ListWorkflows(ctx context.Context, opts model.ListOptions) ([]*model.Workflow, int, error) {
	s.logger.Debug("sql", "op", "list", "table", "workflows", "limit", opts.Limit, "offset", opts.Offset)
	opts.Clamp()

	// Build WHERE clause for optional filters.
	var where []string
	var args []any

	if opts.Class != "" {
		if opts.Class == "Tool" {
			where = append(where, `class IN ('CommandLineTool', 'ExpressionTool')`)
		} else {
			where = append(where, `class = ?`)
			args = append(args, opts.Class)
		}
	}
	if opts.Search != "" {
		where = append(where, `(name LIKE ? OR id LIKE ? OR description LIKE ?)`)
		pat := "%" + opts.Search + "%"
		args = append(args, pat, pat, pat)
	}
	for _, lbl := range opts.Labels {
		if k, v, ok := strings.Cut(lbl, ":"); ok {
			where = append(where, `json_extract(labels, '$.'||?) = ?`)
			args = append(args, k, v)
		} else {
			where = append(where, `labels LIKE ?`)
			args = append(args, `%"`+lbl+`"%`)
		}
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderSQL := validatedOrderBy(opts.SortBy, opts.SortDir, map[string]string{
		"name":       "name",
		"class":      "class",
		"created_at": "created_at",
	}, "created_at DESC")

	queryArgs := append(args, opts.Limit, opts.Offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_at, updated_at
		 FROM workflows`+whereSQL+` ORDER BY `+orderSQL+` LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var workflows []*model.Workflow
	for rows.Next() {
		var wf model.Workflow
		var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
		var createdAt, updatedAt string

		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
			&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &createdAt, &updatedAt); err != nil {
			return nil, 0, err
		}
		if err := unmarshalJSON(inputsJSON, &wf.Inputs, "inputs"); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(outputsJSON, &wf.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(stepsJSON, &wf.Steps, "steps"); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(labelsJSON, &wf.Labels, "labels"); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}
		if wf.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}
		if wf.UpdatedAt, err = parseTimeOrZero(updatedAt); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}

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
	labelsJSON, err := json.Marshal(wf.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET name=?, description=?, class=?, cwl_version=?, content_hash=?, raw_cwl=?,
		 inputs=?, outputs=?, steps=?, labels=?, updated_at=? WHERE id=?`,
		wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON), string(labelsJSON),
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

	// Store token expiry as Unix timestamp (0 if not set).
	tokenExpiry := int64(0)
	if !sub.TokenExpiry.IsZero() {
		tokenExpiry = sub.TokenExpiry.Unix()
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO submissions (id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, output_destination, output_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.WorkflowID, sub.WorkflowName, string(sub.State),
		string(inputsJSON), string(outputsJSON), string(labelsJSON),
		sub.SubmittedBy, sub.CreatedAt.Format(time.RFC3339Nano), completedAt,
		sub.UserToken, tokenExpiry, sub.AuthProvider, sub.ParentTaskID,
		sub.OutputDestination, sub.OutputState,
	)
	return err
}

func (s *SQLiteStore) GetSubmission(ctx context.Context, id string) (*model.Submission, error) {
	s.logger.Debug("sql", "op", "select", "table", "submissions", "id", id)

	var sub model.Submission
	var inputsJSON, outputsJSON, labelsJSON, errorJSON string
	var state, createdAt string
	var completedAt *string
	var tokenExpiry int64

	err := s.db.QueryRowContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, error, output_destination, output_state
		 FROM submissions WHERE id = ?`, id,
	).Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
		&inputsJSON, &outputsJSON, &labelsJSON,
		&sub.SubmittedBy, &createdAt, &completedAt,
		&sub.UserToken, &tokenExpiry, &sub.AuthProvider, &sub.ParentTaskID, &errorJSON,
		&sub.OutputDestination, &sub.OutputState)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sub.State = model.SubmissionState(state)
	if err := unmarshalJSON(inputsJSON, &sub.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &sub.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &sub.Labels, "labels"); err != nil {
		return nil, err
	}
	if errorJSON != "" {
		var subErr model.SubmissionError
		if err := unmarshalJSON(errorJSON, &subErr, "error"); err != nil {
			return nil, err
		}
		sub.Error = &subErr
	}
	if sub.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if completedAt != nil {
		t, err := parseTimeOrZero(*completedAt)
		if err != nil {
			return nil, err
		}
		sub.CompletedAt = &t
	}
	if tokenExpiry > 0 {
		sub.TokenExpiry = time.Unix(tokenExpiry, 0)
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
	if opts.WorkflowID != "" {
		whereClauses = append(whereClauses, "workflow_id = ?")
		countArgs = append(countArgs, opts.WorkflowID)
	}
	if opts.DateStart != "" {
		whereClauses = append(whereClauses, "created_at >= ?")
		countArgs = append(countArgs, opts.DateStart+"T00:00:00Z")
	}
	if opts.DateEnd != "" {
		whereClauses = append(whereClauses, "created_at <= ?")
		countArgs = append(countArgs, opts.DateEnd+"T23:59:59Z")
	}
	if opts.Search != "" {
		whereClauses = append(whereClauses, `(workflow_name LIKE ? OR id LIKE ?)`)
		pat := "%" + opts.Search + "%"
		countArgs = append(countArgs, pat, pat)
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

	orderSQL := validatedOrderBy(opts.SortBy, opts.SortDir, map[string]string{
		"workflow_name": "workflow_name",
		"state":         "state",
		"created_at":    "created_at",
	}, "created_at DESC")

	// List query with pagination.
	listQuery := `SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, output_destination, output_state
		FROM submissions` + whereSQL + ` ORDER BY ` + orderSQL + ` LIMIT ? OFFSET ?`
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
		var tokenExpiry int64

		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
			&inputsJSON, &outputsJSON, &labelsJSON,
			&sub.SubmittedBy, &createdAt, &completedAt,
			&sub.UserToken, &tokenExpiry, &sub.AuthProvider,
			&sub.OutputDestination, &sub.OutputState); err != nil {
			return nil, 0, err
		}

		sub.State = model.SubmissionState(state)
		if err := unmarshalJSON(inputsJSON, &sub.Inputs, "inputs"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(outputsJSON, &sub.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(labelsJSON, &sub.Labels, "labels"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if sub.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
				continue
			}
			sub.CompletedAt = &t
		}
		if tokenExpiry > 0 {
			sub.TokenExpiry = time.Unix(tokenExpiry, 0)
		}

		subs = append(subs, &sub)
	}
	return subs, total, rows.Err()
}

func (s *SQLiteStore) CountSubmissionsByState(ctx context.Context, since time.Time) (map[string]int, error) {
	s.logger.Debug("sql", "op", "count_by_state", "table", "submissions", "since", since)

	query := `SELECT state, COUNT(*) FROM submissions`
	var args []any
	if !since.IsZero() {
		query += ` WHERE created_at >= ?`
		args = append(args, since.Format(time.RFC3339Nano))
	}
	query += ` GROUP BY state`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[state] = count
	}
	return counts, rows.Err()
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

	errorJSON := ""
	if sub.Error != nil {
		if b, err := json.Marshal(sub.Error); err == nil {
			errorJSON = string(b)
		}
	}

	var completedAt *string
	if sub.CompletedAt != nil {
		s := sub.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &s
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET state=?, outputs=?, labels=?, error=?, completed_at=?, output_destination=?, output_state=? WHERE id=?`,
		string(sub.State), string(outputsJSON), string(labelsJSON), errorJSON, completedAt, sub.OutputDestination, sub.OutputState, sub.ID,
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

func (s *SQLiteStore) UpdateSubmissionInputs(ctx context.Context, id string, inputs map[string]any) error {
	s.logger.Debug("sql", "op", "update_inputs", "table", "submissions", "id", id)

	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET inputs=? WHERE id=?`,
		string(inputsJSON), id,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("submission %s not found", id)
	}
	return nil
}

func (s *SQLiteStore) GetChildSubmissions(ctx context.Context, parentTaskID string) ([]*model.Submission, error) {
	s.logger.Debug("sql", "op", "list_children", "table", "submissions", "parent_task_id", parentTaskID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, parent_task_id
		 FROM submissions WHERE parent_task_id = ? ORDER BY created_at`, parentTaskID)
	if err != nil {
		return nil, err
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
			&sub.SubmittedBy, &createdAt, &completedAt, &sub.ParentTaskID); err != nil {
			return nil, err
		}

		sub.State = model.SubmissionState(state)
		if err := unmarshalJSON(inputsJSON, &sub.Inputs, "inputs"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(outputsJSON, &sub.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(labelsJSON, &sub.Labels, "labels"); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if sub.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
				continue
			}
			sub.CompletedAt = &t
		}

		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

// --- StepInstance operations ---

func (s *SQLiteStore) CreateStepInstance(ctx context.Context, si *model.StepInstance) error {
	s.logger.Debug("sql", "op", "insert", "table", "step_instances", "id", si.ID)

	outputsJSON, err := json.Marshal(si.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	var completedAt *string
	if si.CompletedAt != nil {
		v := si.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &v
	}

	scatterDimsJSON, err := json.Marshal(si.ScatterDims)
	if err != nil {
		return fmt.Errorf("marshal scatter_dims: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO step_instances (id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, created_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		si.ID, si.SubmissionID, si.StepID, string(si.State),
		si.ScatterCount, si.ScatterMethod, string(scatterDimsJSON), string(outputsJSON),
		si.CreatedAt.Format(time.RFC3339Nano), completedAt,
	)
	return err
}

func (s *SQLiteStore) BatchCreateStepInstances(ctx context.Context, steps []*model.StepInstance) error {
	if len(steps) == 0 {
		return nil
	}

	s.logger.Debug("sql", "op", "batch_insert", "table", "step_instances", "count", len(steps))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO step_instances (id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, created_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, si := range steps {
		outputsJSON, err := json.Marshal(si.Outputs)
		if err != nil {
			return fmt.Errorf("marshal outputs: %w", err)
		}

		var completedAt *string
		if si.CompletedAt != nil {
			v := si.CompletedAt.Format(time.RFC3339Nano)
			completedAt = &v
		}

		scatterDimsJSON, err := json.Marshal(si.ScatterDims)
		if err != nil {
			return fmt.Errorf("marshal scatter_dims: %w", err)
		}

		_, err = stmt.ExecContext(ctx,
			si.ID, si.SubmissionID, si.StepID, string(si.State),
			si.ScatterCount, si.ScatterMethod, string(scatterDimsJSON), string(outputsJSON),
			si.CreatedAt.Format(time.RFC3339Nano), completedAt,
		)
		if err != nil {
			return fmt.Errorf("insert step instance %s: %w", si.ID, err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetStepInstance(ctx context.Context, id string) (*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "select", "table", "step_instances", "id", id)

	var si model.StepInstance
	var state, outputsJSON, createdAt string
	var completedAt *string
	var scatterDimsJSON string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, created_at, completed_at
		 FROM step_instances WHERE id = ?`, id,
	).Scan(&si.ID, &si.SubmissionID, &si.StepID, &state,
		&si.ScatterCount, &si.ScatterMethod, &scatterDimsJSON, &outputsJSON, &createdAt, &completedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	si.State = model.StepInstanceState(state)
	if err := unmarshalJSON(outputsJSON, &si.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(scatterDimsJSON, &si.ScatterDims, "scatter_dims"); err != nil {
		return nil, err
	}
	if si.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if completedAt != nil {
		t, err := parseTimeOrZero(*completedAt)
		if err != nil {
			return nil, err
		}
		si.CompletedAt = &t
	}

	return &si, nil
}

func (s *SQLiteStore) UpdateStepInstance(ctx context.Context, si *model.StepInstance) error {
	s.logger.Debug("sql", "op", "update", "table", "step_instances", "id", si.ID)

	outputsJSON, err := json.Marshal(si.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	var completedAt *string
	if si.CompletedAt != nil {
		v := si.CompletedAt.Format(time.RFC3339Nano)
		completedAt = &v
	}

	scatterDimsJSON, err := json.Marshal(si.ScatterDims)
	if err != nil {
		return fmt.Errorf("marshal scatter_dims: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE step_instances SET state=?, scatter_count=?, scatter_method=?, scatter_dims=?, outputs=?, completed_at=? WHERE id=?`,
		string(si.State), si.ScatterCount, si.ScatterMethod, string(scatterDimsJSON), string(outputsJSON), completedAt, si.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("step_instance %s not found", si.ID)
	}
	return nil
}

func (s *SQLiteStore) ListStepsBySubmission(ctx context.Context, submissionID string) ([]*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "list", "table", "step_instances", "submission_id", submissionID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, created_at, completed_at
		 FROM step_instances WHERE submission_id = ? ORDER BY created_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanStepInstances(rows)
}

func (s *SQLiteStore) ListStepsByState(ctx context.Context, state model.StepInstanceState) ([]*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "list_by_state", "table", "step_instances", "state", state)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, created_at, completed_at
		 FROM step_instances WHERE state = ? ORDER BY created_at`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanStepInstances(rows)
}

func (s *SQLiteStore) CancelNonTerminalSteps(ctx context.Context, submissionID string, completedAt time.Time) (int, error) {
	s.logger.Debug("sql", "op", "cancel_non_terminal_steps", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE step_instances
		 SET state = ?, completed_at = ?
		 WHERE submission_id = ? AND state NOT IN (?, ?, ?)`,
		string(model.StepStateSkipped), completedAt.Format(time.RFC3339Nano),
		submissionID,
		string(model.StepStateCompleted), string(model.StepStateFailed), string(model.StepStateSkipped))
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal steps: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal steps rows affected: %w", err)
	}
	return int(n), nil
}

func (s *SQLiteStore) scanStepInstances(rows *sql.Rows) ([]*model.StepInstance, error) {
	var items []*model.StepInstance
	for rows.Next() {
		var si model.StepInstance
		var state, outputsJSON, createdAt string
		var completedAt *string
		var scatterDimsJSON string

		if err := rows.Scan(&si.ID, &si.SubmissionID, &si.StepID, &state,
			&si.ScatterCount, &si.ScatterMethod, &scatterDimsJSON, &outputsJSON, &createdAt, &completedAt); err != nil {
			return nil, err
		}

		si.State = model.StepInstanceState(state)
		if err := unmarshalJSON(outputsJSON, &si.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt step_instance row", "id", si.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(scatterDimsJSON, &si.ScatterDims, "scatter_dims"); err != nil {
			slog.Error("skipping corrupt step_instance row", "id", si.ID, "error", err)
			continue
		}
		if t, err := parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt step_instance row", "id", si.ID, "error", err)
			continue
		} else {
			si.CreatedAt = t
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				slog.Error("skipping corrupt step_instance row", "id", si.ID, "error", err)
				continue
			}
			si.CompletedAt = &t
		}

		items = append(items, &si)
	}
	return items, rows.Err()
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
	toolJSON, err := json.Marshal(task.Tool)
	if err != nil {
		return fmt.Errorf("marshal tool: %w", err)
	}
	jobJSON, err := json.Marshal(task.Job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	runtimeHintsJSON, err := json.Marshal(task.RuntimeHints)
	if err != nil {
		return fmt.Errorf("marshal runtime_hints: %w", err)
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
		`INSERT INTO tasks (id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.SubmissionID, task.StepID, task.StepInstanceID, string(task.State),
		string(task.ExecutorType), task.ExternalID, task.BVBRCAppID,
		string(inputsJSON), string(outputsJSON), string(dependsOnJSON),
		task.RetryCount, task.MaxRetries,
		task.Stdout, task.Stderr, task.ExitCode,
		task.CreatedAt.Format(time.RFC3339Nano), startedAt, completedAt,
		string(toolJSON), string(jobJSON), string(runtimeHintsJSON),
		task.ScatterIndex,
	)
	return err
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*model.Task, error) {
	s.logger.Debug("sql", "op", "select", "table", "tasks", "id", id)
	return s.scanTask(s.db.QueryRowContext(ctx,
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks WHERE id = ?`, id))
}

func (s *SQLiteStore) ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list", "table", "tasks", "submission_id", submissionID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks WHERE submission_id = ? ORDER BY created_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

func (s *SQLiteStore) ListTasksBySubmissionPaged(ctx context.Context, submissionID string, opts model.ListOptions) ([]*model.Task, int, error) {
	s.logger.Debug("sql", "op", "list_paged", "table", "tasks", "submission_id", submissionID, "limit", opts.Limit, "offset", opts.Offset)
	opts.Clamp()

	var where []string
	var args []any

	where = append(where, "submission_id = ?")
	args = append(args, submissionID)

	if opts.State != "" {
		where = append(where, "state = ?")
		args = append(args, opts.State)
	}

	whereSQL := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderSQL := validatedOrderBy(opts.SortBy, opts.SortDir, map[string]string{
		"created_at": "created_at",
		"state":      "state",
		"step_id":    "step_id",
	}, "created_at ASC")

	queryArgs := append(args, opts.Limit, opts.Offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks`+whereSQL+` ORDER BY `+orderSQL+` LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	tasks, err := s.scanTasks(rows)
	if err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

func (s *SQLiteStore) ListTasksByStepInstance(ctx context.Context, stepInstanceID string) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list", "table", "tasks", "step_instance_id", stepInstanceID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks WHERE step_instance_id = ? ORDER BY scatter_index, created_at`, stepInstanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

func (s *SQLiteStore) UpdateTask(ctx context.Context, task *model.Task) error {
	s.logger.Debug("sql", "op", "update", "table", "tasks", "id", task.ID)

	// Sanitize NaN/Inf values before marshaling (Go's encoding/json rejects them).
	sanitizeFloats(task.Outputs)
	sanitizeFloats(task.Tool)
	sanitizeFloats(task.Job)

	outputsJSON, err := json.Marshal(task.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}
	toolJSON, err := json.Marshal(task.Tool)
	if err != nil {
		return fmt.Errorf("marshal tool: %w", err)
	}
	jobJSON, err := json.Marshal(task.Job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	runtimeHintsJSON, err := json.Marshal(task.RuntimeHints)
	if err != nil {
		return fmt.Errorf("marshal runtime_hints: %w", err)
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
		 outputs=?, retry_count=?, max_retries=?, stdout=?, stderr=?, exit_code=?,
		 started_at=?, completed_at=?, tool=?, job=?, runtime_hints=?,
		 step_instance_id=?, scatter_index=? WHERE id=?`,
		string(task.State), string(task.ExecutorType), task.ExternalID,
		string(outputsJSON), task.RetryCount, task.MaxRetries,
		task.Stdout, task.Stderr, task.ExitCode,
		startedAt, completedAt,
		string(toolJSON), string(jobJSON), string(runtimeHintsJSON),
		task.StepInstanceID, task.ScatterIndex,
		task.ID,
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
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks WHERE state = ? ORDER BY created_at`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

func (s *SQLiteStore) CancelNonTerminalTasks(ctx context.Context, submissionID string, completedAt time.Time) (int, error) {
	s.logger.Debug("sql", "op", "cancel_non_terminal_tasks", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks
		 SET state = ?, completed_at = ?
		 WHERE submission_id = ? AND state NOT IN (?, ?, ?)`,
		string(model.TaskStateSkipped), completedAt.Format(time.RFC3339Nano),
		submissionID,
		string(model.TaskStateSuccess), string(model.TaskStateFailed), string(model.TaskStateSkipped))
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal tasks: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal tasks rows affected: %w", err)
	}
	return int(n), nil
}

// --- scan helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func (s *SQLiteStore) scanTask(row scanner) (*model.Task, error) {
	var task model.Task
	var inputsJSON, outputsJSON, dependsOnJSON string
	var toolJSON, jobJSON, runtimeHintsJSON string
	var state, executorType, createdAt string
	var startedAt, completedAt *string

	err := row.Scan(
		&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &state,
		&executorType, &task.ExternalID, &task.BVBRCAppID,
		&inputsJSON, &outputsJSON, &dependsOnJSON,
		&task.RetryCount, &task.MaxRetries,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&createdAt, &startedAt, &completedAt,
		&toolJSON, &jobJSON, &runtimeHintsJSON,
		&task.ScatterIndex,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	task.State = model.TaskState(state)
	task.ExecutorType = model.ExecutorType(executorType)
	if err := unmarshalJSON(inputsJSON, &task.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &task.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(dependsOnJSON, &task.DependsOn, "depends_on"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(toolJSON, &task.Tool, "tool"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(jobJSON, &task.Job, "job"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(runtimeHintsJSON, &task.RuntimeHints, "runtime_hints"); err != nil {
		return nil, err
	}
	if task.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if startedAt != nil {
		t, err := parseTimeOrZero(*startedAt)
		if err != nil {
			return nil, err
		}
		task.StartedAt = &t
	}
	if completedAt != nil {
		t, err := parseTimeOrZero(*completedAt)
		if err != nil {
			return nil, err
		}
		task.CompletedAt = &t
	}

	return &task, nil
}

func (s *SQLiteStore) scanTasks(rows *sql.Rows) ([]*model.Task, error) {
	var tasks []*model.Task
	for rows.Next() {
		var task model.Task
		var inputsJSON, outputsJSON, dependsOnJSON string
		var toolJSON, jobJSON, runtimeHintsJSON string
		var state, executorType, createdAt string
		var startedAt, completedAt *string

		if err := rows.Scan(
			&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &state,
			&executorType, &task.ExternalID, &task.BVBRCAppID,
			&inputsJSON, &outputsJSON, &dependsOnJSON,
			&task.RetryCount, &task.MaxRetries,
			&task.Stdout, &task.Stderr, &task.ExitCode,
			&createdAt, &startedAt, &completedAt,
			&toolJSON, &jobJSON, &runtimeHintsJSON,
			&task.ScatterIndex,
		); err != nil {
			return nil, err
		}

		task.State = model.TaskState(state)
		task.ExecutorType = model.ExecutorType(executorType)
		if err := unmarshalJSON(inputsJSON, &task.Inputs, "inputs"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(outputsJSON, &task.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(dependsOnJSON, &task.DependsOn, "depends_on"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(toolJSON, &task.Tool, "tool"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(jobJSON, &task.Job, "job"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(runtimeHintsJSON, &task.RuntimeHints, "runtime_hints"); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		}
		if t, err := parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
			continue
		} else {
			task.CreatedAt = t
		}
		if startedAt != nil {
			t, err := parseTimeOrZero(*startedAt)
			if err != nil {
				slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
				continue
			}
			task.StartedAt = &t
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
				continue
			}
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

	datasetsJSON, err := json.Marshal(w.Datasets)
	if err != nil {
		return fmt.Errorf("marshal datasets: %w", err)
	}

	// Default group to "default" if not set.
	group := w.Group
	if group == "" {
		group = "default"
	}

	gpuEnabled := 0
	if w.GPUEnabled {
		gpuEnabled = 1
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workers (id, name, hostname, worker_group, state, runtime, labels, last_seen, current_task, registered_at, gpu_enabled, gpu_device, datasets, version)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Hostname, group, string(w.State), string(w.Runtime),
		string(labelsJSON), w.LastSeen.Format(time.RFC3339Nano),
		w.CurrentTask, w.RegisteredAt.Format(time.RFC3339Nano),
		gpuEnabled, w.GPUDevice, string(datasetsJSON), w.Version,
	)
	return err
}

func (s *SQLiteStore) GetWorker(ctx context.Context, id string) (*model.Worker, error) {
	s.logger.Debug("sql", "op", "select", "table", "workers", "id", id)

	var w model.Worker
	var state, runtime, labelsJSON, datasetsJSON, lastSeen, registeredAt string
	var gpuEnabled int

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, hostname, worker_group, state, runtime, labels, last_seen, current_task, registered_at, gpu_enabled, gpu_device, datasets, version
		 FROM workers WHERE id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.Hostname, &w.Group, &state, &runtime,
		&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt, &gpuEnabled, &w.GPUDevice, &datasetsJSON, &w.Version)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	w.State = model.WorkerState(state)
	w.Runtime = model.ContainerRuntime(runtime)
	w.GPUEnabled = gpuEnabled != 0
	if err := unmarshalJSON(labelsJSON, &w.Labels, "labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(datasetsJSON, &w.Datasets, "datasets"); err != nil {
		return nil, err
	}
	if w.LastSeen, err = parseTimeOrZero(lastSeen); err != nil {
		return nil, err
	}
	if w.RegisteredAt, err = parseTimeOrZero(registeredAt); err != nil {
		return nil, err
	}

	return &w, nil
}

func (s *SQLiteStore) UpdateWorker(ctx context.Context, w *model.Worker) error {
	s.logger.Debug("sql", "op", "update", "table", "workers", "id", w.ID)

	labelsJSON, err := json.Marshal(w.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	datasetsJSON, err := json.Marshal(w.Datasets)
	if err != nil {
		return fmt.Errorf("marshal datasets: %w", err)
	}

	gpuEnabled := 0
	if w.GPUEnabled {
		gpuEnabled = 1
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workers SET name=?, hostname=?, worker_group=?, state=?, runtime=?, labels=?,
		 last_seen=?, current_task=?, gpu_enabled=?, gpu_device=?, datasets=? WHERE id=?`,
		w.Name, w.Hostname, w.Group, string(w.State), string(w.Runtime),
		string(labelsJSON), w.LastSeen.Format(time.RFC3339Nano),
		w.CurrentTask, gpuEnabled, w.GPUDevice, string(datasetsJSON), w.ID,
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
		`SELECT id, name, hostname, worker_group, state, runtime, labels, last_seen, current_task, registered_at, gpu_enabled, gpu_device, datasets, version
		 FROM workers ORDER BY registered_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []*model.Worker
	for rows.Next() {
		var w model.Worker
		var state, runtime, labelsJSON, datasetsJSON, lastSeen, registeredAt string
		var gpuEnabled int

		if err := rows.Scan(&w.ID, &w.Name, &w.Hostname, &w.Group, &state, &runtime,
			&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt, &gpuEnabled, &w.GPUDevice, &datasetsJSON, &w.Version); err != nil {
			return nil, err
		}

		w.State = model.WorkerState(state)
		w.Runtime = model.ContainerRuntime(runtime)
		w.GPUEnabled = gpuEnabled != 0
		if err := unmarshalJSON(labelsJSON, &w.Labels, "labels"); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(datasetsJSON, &w.Datasets, "datasets"); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		}
		if t, err := parseTimeOrZero(lastSeen); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		} else {
			w.LastSeen = t
		}
		if t, err := parseTimeOrZero(registeredAt); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		} else {
			w.RegisteredAt = t
		}

		workers = append(workers, &w)
	}
	return workers, rows.Err()
}

// CheckoutTask atomically finds a QUEUED worker task and transitions it to
// RUNNING, assigning it to the given worker. Returns nil if no task is available.
// Selection considers runtime capability, worker group, and dataset affinity:
//   - Runtime: if runtime is "none", only tasks without docker_image are eligible.
//   - Group: if workerGroup is non-empty, only tasks with matching or empty WorkerGroup.
//   - Prestage datasets: task is skipped if worker lacks a required prestage dataset.
//   - Cache datasets: among eligible tasks, prefer the one with the highest cache score
//     (number of matching cache datasets). Ties are broken by creation order (oldest first).
func (s *SQLiteStore) CheckoutTask(ctx context.Context, workerID string, workerGroup string, runtime model.ContainerRuntime) (*model.Task, error) {
	s.logger.Debug("sql", "op", "checkout_task", "worker_id", workerID, "group", workerGroup, "runtime", runtime)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Find oldest QUEUED task assigned to the worker executor.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
		 bvbrc_app_id, inputs, outputs, depends_on, retry_count, max_retries,
		 stdout, stderr, exit_code, created_at, started_at, completed_at,
		 tool, job, runtime_hints, scatter_index
		 FROM tasks WHERE state = 'QUEUED' AND executor_type = 'worker'
		 ORDER BY created_at LIMIT 10`)
	if err != nil {
		return nil, err
	}

	var candidates []*model.Task
	for rows.Next() {
		var task model.Task
		var inputsJSON, outputsJSON, dependsOnJSON string
		var toolJSON, jobJSON, runtimeHintsJSON string
		var stateStr, executorType, createdAt string
		var startedAt, completedAt *string

		if err := rows.Scan(
			&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &stateStr,
			&executorType, &task.ExternalID, &task.BVBRCAppID,
			&inputsJSON, &outputsJSON, &dependsOnJSON,
			&task.RetryCount, &task.MaxRetries,
			&task.Stdout, &task.Stderr, &task.ExitCode,
			&createdAt, &startedAt, &completedAt,
			&toolJSON, &jobJSON, &runtimeHintsJSON,
			&task.ScatterIndex,
		); err != nil {
			rows.Close()
			return nil, err
		}

		task.State = model.TaskState(stateStr)
		task.ExecutorType = model.ExecutorType(executorType)
		if err := unmarshalJSON(inputsJSON, &task.Inputs, "inputs"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(outputsJSON, &task.Outputs, "outputs"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(dependsOnJSON, &task.DependsOn, "depends_on"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(toolJSON, &task.Tool, "tool"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(jobJSON, &task.Job, "job"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(runtimeHintsJSON, &task.RuntimeHints, "runtime_hints"); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		}
		if t, err := parseTimeOrZero(createdAt); err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			continue
		} else {
			task.CreatedAt = t
		}
		if startedAt != nil {
			t, err := parseTimeOrZero(*startedAt)
			if err != nil {
				slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
				continue
			}
			task.StartedAt = &t
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
				continue
			}
			task.CompletedAt = &t
		}

		candidates = append(candidates, &task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Look up the worker's datasets for affinity matching.
	var workerDatasetsJSON string
	var workerDatasets map[string]string
	if err := tx.QueryRowContext(ctx, `SELECT datasets FROM workers WHERE id = ?`, workerID).Scan(&workerDatasetsJSON); err != nil {
		if err != sql.ErrNoRows {
			s.logger.Warn("sql", "op", "checkout_task_load_worker_datasets", "worker_id", workerID, "error", err)
		}
		workerDatasets = map[string]string{}
	} else if err := json.Unmarshal([]byte(workerDatasetsJSON), &workerDatasets); err != nil {
		s.logger.Warn("sql", "op", "checkout_task_parse_worker_datasets", "worker_id", workerID, "error", err)
		workerDatasets = map[string]string{}
	}

	// Filter by runtime capability, worker group, and dataset affinity.
	canRunContainers := model.HasContainerRuntime(runtime)

	var selected *model.Task
	bestCacheScore := -1
	for _, task := range candidates {
		// Check container runtime capability.
		// If a task has a DockerImage (from DockerRequirement), the worker must
		// have a container runtime (docker or apptainer) to execute it.
		// Workers with only runtime=none cannot run containerized tasks.
		taskNeedsContainer := false
		if task.RuntimeHints != nil && task.RuntimeHints.DockerImage != "" {
			taskNeedsContainer = true
		}
		if img, ok := task.Inputs["_docker_image"].(string); ok && img != "" {
			taskNeedsContainer = true
		}
		if taskNeedsContainer && !canRunContainers {
			continue
		}

		// Check worker group matching.
		// A non-default worker only picks up tasks that target its group.
		// A default worker picks up tasks with no group or group "default".
		taskGroup := ""
		if task.RuntimeHints != nil {
			taskGroup = task.RuntimeHints.WorkerGroup
		}
		if workerGroup != "default" && workerGroup != "" {
			// Non-default worker: only pick up tasks targeting this group.
			if taskGroup != workerGroup {
				continue
			}
		} else {
			// Default worker: skip tasks targeting a specific non-default group.
			if taskGroup != "" && taskGroup != "default" {
				continue
			}
		}

		// Check dataset affinity.
		if task.RuntimeHints != nil && len(task.RuntimeHints.RequiredDatasets) > 0 {
			missingPrestage := false
			cacheScore := 0
			for _, req := range task.RuntimeHints.RequiredDatasets {
				if req.Mode == "prestage" {
					if _, ok := workerDatasets[req.ID]; !ok {
						missingPrestage = true
						break
					}
				} else if req.Mode == "cache" {
					if _, ok := workerDatasets[req.ID]; ok {
						cacheScore++
					}
				}
			}
			if missingPrestage {
				continue // Worker missing required prestage dataset
			}
			// For cache-mode datasets, prefer workers that have more matching datasets.
			if cacheScore > bestCacheScore {
				bestCacheScore = cacheScore
				selected = task
				continue
			}
		}

		if selected == nil {
			selected = task
		}
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

// MarkStaleWorkersOffline transitions workers to offline if their last_seen
// is older than the given timeout. Returns the workers that were transitioned.
func (s *SQLiteStore) MarkStaleWorkersOffline(ctx context.Context, timeout time.Duration) ([]*model.Worker, error) {
	cutoff := time.Now().UTC().Add(-timeout).Format(time.RFC3339Nano)
	s.logger.Debug("sql", "op", "mark_stale_offline", "cutoff", cutoff)

	// Find stale workers first.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hostname, worker_group, state, runtime, labels, last_seen, current_task, registered_at, gpu_enabled, gpu_device, datasets, version
		 FROM workers WHERE state = 'online' AND last_seen < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []*model.Worker
	for rows.Next() {
		var w model.Worker
		var state, runtime, labelsJSON, datasetsJSON, lastSeen, registeredAt string
		var gpuEnabled int
		if err := rows.Scan(&w.ID, &w.Name, &w.Hostname, &w.Group, &state, &runtime,
			&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt, &gpuEnabled, &w.GPUDevice, &datasetsJSON, &w.Version); err != nil {
			return nil, err
		}
		w.State = model.WorkerState(state)
		w.Runtime = model.ContainerRuntime(runtime)
		w.GPUEnabled = gpuEnabled != 0
		if err := unmarshalJSON(labelsJSON, &w.Labels, "labels"); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		}
		if err := unmarshalJSON(datasetsJSON, &w.Datasets, "datasets"); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		}
		if t, err := parseTimeOrZero(lastSeen); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		} else {
			w.LastSeen = t
		}
		if t, err := parseTimeOrZero(registeredAt); err != nil {
			slog.Error("skipping corrupt worker row", "id", w.ID, "error", err)
			continue
		} else {
			w.RegisteredAt = t
		}
		stale = append(stale, &w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(stale) == 0 {
		return nil, nil
	}

	// Transition them to offline.
	_, err = s.db.ExecContext(ctx,
		`UPDATE workers SET state = 'offline' WHERE state = 'online' AND last_seen < ?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("mark workers offline: %w", err)
	}

	for _, w := range stale {
		w.State = model.WorkerStateOffline
	}
	return stale, nil
}

// RequeueWorkerTasks resets RUNNING tasks assigned to the given worker back to
// QUEUED so they can be picked up by another worker. Returns the count of
// requeued tasks.
func (s *SQLiteStore) RequeueWorkerTasks(ctx context.Context, workerID string) (int, error) {
	s.logger.Debug("sql", "op", "requeue_worker_tasks", "worker_id", workerID)

	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL
		 WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`, workerID)
	if err != nil {
		return 0, fmt.Errorf("requeue tasks: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("requeue tasks: rows affected: %w", err)
	}

	// Clear the worker's current_task.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE workers SET current_task = '' WHERE id = ?`, workerID); err != nil {
		return int(n), fmt.Errorf("clear worker current_task: %w", err)
	}

	return int(n), nil
}

// --- User operations ---

func (s *SQLiteStore) GetUser(ctx context.Context, username string) (*model.User, error) {
	s.logger.Debug("sql", "op", "select", "table", "users", "username", username)

	var user model.User
	var createdAt, lastLogin int64

	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, provider, role, created_at, last_login
		 FROM users WHERE username = ?`, username,
	).Scan(&user.ID, &user.Username, &user.Provider, &user.Role, &createdAt, &lastLogin)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	user.CreatedAt = time.Unix(createdAt, 0)
	user.LastLoginAt = time.Unix(lastLogin, 0)

	// Load linked providers.
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, username FROM linked_providers WHERE user_id = ?`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load linked providers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lp model.LinkedProvider
		if err := rows.Scan(&lp.Provider, &lp.Username); err != nil {
			return nil, err
		}
		user.LinkedProviders = append(user.LinkedProviders, lp)
	}

	return &user, rows.Err()
}

func (s *SQLiteStore) GetOrCreateUser(ctx context.Context, username string, provider model.AuthProvider) (*model.User, error) {
	s.logger.Debug("sql", "op", "get_or_create", "table", "users", "username", username)

	// Try to get existing user.
	user, err := s.GetUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if user != nil {
		// Update last login time.
		user.LastLoginAt = time.Now().UTC()
		if err := s.UpdateUser(ctx, user); err != nil {
			s.logger.Warn("update last_login failed", "username", username, "error", err)
		}
		return user, nil
	}

	// Create new user.
	now := time.Now().UTC()
	user = &model.User{
		ID:          "user_" + fmt.Sprintf("%d", now.UnixNano()),
		Username:    username,
		Provider:    provider,
		Role:        model.RoleUser,
		CreatedAt:   now,
		LastLoginAt: now,
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, provider, role, created_at, last_login)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		user.ID, user.Username, user.Provider, user.Role,
		user.CreatedAt.Unix(), user.LastLoginAt.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	s.logger.Info("user created", "id", user.ID, "username", username, "provider", provider)
	return user, nil
}

func (s *SQLiteStore) UpdateUser(ctx context.Context, user *model.User) error {
	s.logger.Debug("sql", "op", "update", "table", "users", "id", user.ID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET role=?, last_login=? WHERE id=?`,
		user.Role, user.LastLoginAt.Unix(), user.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %s not found", user.ID)
	}
	return nil
}

func (s *SQLiteStore) ListUsers(ctx context.Context) ([]*model.User, error) {
	s.logger.Debug("sql", "op", "list", "table", "users")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, provider, role, created_at, last_login
		 FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*model.User
	for rows.Next() {
		var user model.User
		var createdAt, lastLogin int64

		if err := rows.Scan(&user.ID, &user.Username, &user.Provider, &user.Role,
			&createdAt, &lastLogin); err != nil {
			return nil, err
		}

		user.CreatedAt = time.Unix(createdAt, 0)
		user.LastLoginAt = time.Unix(lastLogin, 0)
		users = append(users, &user)
	}
	return users, rows.Err()
}

func (s *SQLiteStore) LinkProvider(ctx context.Context, userID string, provider model.AuthProvider, username string) error {
	s.logger.Debug("sql", "op", "insert", "table", "linked_providers", "user_id", userID, "provider", provider)

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO linked_providers (user_id, provider, username)
		 VALUES (?, ?, ?)`,
		userID, provider, username,
	)
	return err
}

// sanitizeFloats recursively replaces NaN and Inf float64 values with nil
// so that encoding/json.Marshal doesn't fail.
func sanitizeFloats(v any) {
	switch val := v.(type) {
	case map[string]any:
		for k, v := range val {
			if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
				val[k] = nil
			} else {
				sanitizeFloats(v)
			}
		}
	case []any:
		for i, v := range val {
			if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
				val[i] = nil
			} else {
				sanitizeFloats(v)
			}
		}
	}
}

// --- Label Vocabulary CRUD ---

func (s *SQLiteStore) CreateLabelVocabulary(ctx context.Context, lv *model.LabelVocabulary) error {
	s.logger.Debug("sql", "op", "insert", "table", "label_vocabulary", "id", lv.ID)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO label_vocabulary (id, key, value, description, color, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		lv.ID, lv.Key, lv.Value, lv.Description, lv.Color,
		lv.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) ListLabelVocabulary(ctx context.Context) ([]*model.LabelVocabulary, error) {
	s.logger.Debug("sql", "op", "list", "table", "label_vocabulary")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, value, description, color, created_at FROM label_vocabulary ORDER BY key, value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.LabelVocabulary
	for rows.Next() {
		var lv model.LabelVocabulary
		var createdAt string
		if err := rows.Scan(&lv.ID, &lv.Key, &lv.Value, &lv.Description, &lv.Color, &createdAt); err != nil {
			return nil, err
		}
		if lv.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			return nil, err
		}
		result = append(result, &lv)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) DeleteLabelVocabulary(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "label_vocabulary", "id", id)

	result, err := s.db.ExecContext(ctx, `DELETE FROM label_vocabulary WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("label vocabulary entry %s not found", id)
	}
	return nil
}

// validatedOrderBy returns a safe ORDER BY clause using a whitelist of allowed columns.
func validatedOrderBy(sortBy, sortDir string, allowed map[string]string, defaultSort string) string {
	col, ok := allowed[sortBy]
	if !ok {
		return defaultSort
	}
	dir := "DESC"
	if strings.EqualFold(sortDir, "asc") {
		dir = "ASC"
	}
	return col + " " + dir
}
