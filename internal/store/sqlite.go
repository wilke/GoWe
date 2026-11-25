package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/internal/tokencrypt"
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

// storeTimeLayout is how every TEXT timestamp column is written: UTC with
// exactly nine fractional digits. Timestamps are compared and ordered as
// strings in SQL, which is only chronological when all values have the same
// width and zone; time.RFC3339Nano drops trailing zeros and keeps the zone.
const storeTimeLayout = "2006-01-02T15:04:05.000000000Z"

// formatTime renders t for a TEXT timestamp column (see storeTimeLayout).
// Rows written before this layout was introduced are still RFC3339Nano and
// parse the same way; next to new rows they can only sort wrongly within the
// same second.
func formatTime(t time.Time) string {
	return t.UTC().Format(storeTimeLayout)
}

// formatTimePtr formats a *time.Time as a *string for a nullable TEXT column
// (see formatTime), returning nil (SQL NULL) when t is nil.
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

// scanTimePtr parses a nullable RFC3339Nano TEXT column into a *time.Time,
// returning nil when raw is nil (SQL NULL).
func scanTimePtr(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	t, err := parseTimeOrZero(*raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
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

// SQLStore implements Store over database/sql; the dialect decides the SQL
// flavour.
type SQLStore struct {
	db      *sqlDB
	dialect dialect
	logger  *slog.Logger

	// cipher, when non-nil, encrypts persisted provider tokens at rest
	// (submissions.user_token and any bearer credential embedded in a task's
	// runtime hints). Configured via ConfigureTokenEncryption; see tokens.go.
	cipher *tokencrypt.Cipher
	// refusePlaintextTokens, when true and cipher is nil, makes the store fail
	// closed rather than write a provider token in the clear.
	refusePlaintextTokens bool
	// plaintextWarnOnce guards a single startup warning when tokens are being
	// persisted without encryption.
	plaintextWarnOnce sync.Once

	// checkoutHoldAfter enables CheckoutTask's hold rule when > 0. Set via
	// SetCheckoutHoldAfter before the store is used.
	checkoutHoldAfter time.Duration
	// holdMu guards holdLast: per worker id, the holder task id found by that
	// worker's previous checkout, so the start of a hold is logged once.
	holdMu   sync.Mutex
	holdLast map[string]string
}

// SQLiteStore is the old name of SQLStore, kept for callers.
type SQLiteStore = SQLStore

// SetCheckoutHoldAfter configures CheckoutTask's hold rule: once a queued task
// has waited d (since it was last dispatched, else created) for capacity on a
// worker that runs several tasks, that worker stops taking later tasks that
// need the same capacity. d <= 0 disables the rule (the default). Call before
// the store is used.
func (s *SQLStore) SetCheckoutHoldAfter(d time.Duration) {
	s.checkoutHoldAfter = d
}

// noteHolder records the holder task id found by workerID's checkout ("" =
// none; the entry is deleted) and reports whether a holder differs from the
// one recorded at the worker's previous checkout. The map is reset if it
// reaches checkoutHoldLogMax entries (workers removed while holding).
func (s *SQLStore) noteHolder(workerID, holderID string) bool {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	if holderID == "" {
		delete(s.holdLast, workerID)
		return false
	}
	if s.holdLast[workerID] == holderID {
		return false
	}
	if _, known := s.holdLast[workerID]; s.holdLast == nil || (!known && len(s.holdLast) >= checkoutHoldLogMax) {
		s.holdLast = map[string]string{}
	}
	s.holdLast[workerID] = holderID
	return true
}

// heldFor returns the task workerID's previous checkout held for ("" = none).
func (s *SQLStore) heldFor(workerID string) string {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	return s.holdLast[workerID]
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and returns a Store.
// Use ":memory:" for an in-memory database (useful in tests).
func NewSQLiteStore(dbPath string, logger *slog.Logger) (*SQLStore, error) {
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

	d := sqliteDialect{}
	return &SQLStore{
		db:      &sqlDB{db: db, d: d},
		dialect: d,
		logger:  logger.With("component", "store"),
	}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error {
	return s.db.Close()
}

// Migrate creates all required tables and indexes.
func (s *SQLStore) Migrate(ctx context.Context) error {
	s.logger.Debug("sql", "op", "migrate")
	return migrate(ctx, s.db)
}

// --- Workflow CRUD ---

func (s *SQLStore) CreateWorkflow(ctx context.Context, wf *model.Workflow) error {
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
	secretInputsCol, err := marshalSecretInputs(wf.SecretInputs)
	if err != nil {
		return err
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflows (id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_by, created_at, updated_at, secret_inputs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON), s.dialect.jsonDoc(string(labelsJSON)),
		wf.CreatedBy,
		formatTime(wf.CreatedAt), formatTime(wf.UpdatedAt),
		secretInputsCol,
	)
	return err
}

// marshalSecretInputs JSON-encodes a workflow's SecretInputs for storage,
// returning nil (SQL NULL) for an empty slice so a workflow with no
// cwltool:Secrets declaration leaves the column untouched.
func marshalSecretInputs(ids []string) (*string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal secret_inputs: %w", err)
	}
	s := string(b)
	return &s, nil
}

// unmarshalSecretInputs reverses marshalSecretInputs for a value read from
// the database. A NULL/empty column decodes to a nil slice, no error.
func unmarshalSecretInputs(col *string) ([]string, error) {
	if col == nil || *col == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(*col), &ids); err != nil {
		return nil, fmt.Errorf("unmarshal secret_inputs: %w", err)
	}
	return ids, nil
}

func (s *SQLStore) GetWorkflow(ctx context.Context, id string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select", "table", "workflows", "id", id)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string
	var secretInputsCol *string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_by, created_at, updated_at, secret_inputs
		 FROM workflows WHERE id = ?`, id,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &wf.CreatedBy, &createdAt, &updatedAt, &secretInputsCol)

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
	if wf.SecretInputs, err = unmarshalSecretInputs(secretInputsCol); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLStore) GetWorkflowByHash(ctx context.Context, hash string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select_by_hash", "table", "workflows", "hash", hash)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string
	var secretInputsCol *string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_by, created_at, updated_at, secret_inputs
		 FROM workflows WHERE content_hash = ? ORDER BY created_at DESC LIMIT 1`, hash,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &wf.CreatedBy, &createdAt, &updatedAt, &secretInputsCol)

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
	if wf.SecretInputs, err = unmarshalSecretInputs(secretInputsCol); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLStore) GetWorkflowByName(ctx context.Context, name string) (*model.Workflow, error) {
	s.logger.Debug("sql", "op", "select_by_name", "table", "workflows", "name", name)

	var wf model.Workflow
	var inputsJSON, outputsJSON, stepsJSON, labelsJSON string
	var createdAt, updatedAt string
	var secretInputsCol *string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_by, created_at, updated_at, secret_inputs
		 FROM workflows WHERE name = ? ORDER BY created_at DESC LIMIT 1`, name,
	).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
		&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &wf.CreatedBy, &createdAt, &updatedAt, &secretInputsCol)

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
	if wf.SecretInputs, err = unmarshalSecretInputs(secretInputsCol); err != nil {
		return nil, err
	}

	return &wf, nil
}

func (s *SQLStore) ListWorkflows(ctx context.Context, opts model.ListOptions) ([]*model.Workflow, int, error) {
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
		where = append(where, `(LOWER(name) LIKE LOWER(?) OR LOWER(id) LIKE LOWER(?) OR LOWER(description) LIKE LOWER(?))`)
		pat := "%" + opts.Search + "%"
		args = append(args, pat, pat, pat)
	}
	for _, lbl := range opts.Labels {
		if k, v, ok := strings.Cut(lbl, ":"); ok {
			where = append(where, s.dialect.jsonTextParam("labels")+` = ?`)
			args = append(args, k, v)
		} else {
			where = append(where, s.dialect.jsonAnyValueEquals("labels"))
			args = append(args, lbl)
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
		`SELECT id, name, description, class, cwl_version, content_hash, raw_cwl, inputs, outputs, steps, labels, created_by, created_at, updated_at, secret_inputs
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
		var secretInputsCol *string

		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Class, &wf.CWLVersion, &wf.ContentHash, &wf.RawCWL,
			&inputsJSON, &outputsJSON, &stepsJSON, &labelsJSON, &wf.CreatedBy, &createdAt, &updatedAt, &secretInputsCol); err != nil {
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
		if wf.SecretInputs, err = unmarshalSecretInputs(secretInputsCol); err != nil {
			slog.Error("skipping corrupt workflow row", "id", wf.ID, "error", err)
			continue
		}

		workflows = append(workflows, &wf)
	}
	return workflows, total, rows.Err()
}

func (s *SQLStore) UpdateWorkflow(ctx context.Context, wf *model.Workflow) error {
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
	secretInputsCol, err := marshalSecretInputs(wf.SecretInputs)
	if err != nil {
		return err
	}

	// Default class to "Workflow" if not set.
	class := wf.Class
	if class == "" {
		class = "Workflow"
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET name=?, description=?, class=?, cwl_version=?, content_hash=?, raw_cwl=?,
		 inputs=?, outputs=?, steps=?, labels=?, updated_at=?, secret_inputs=? WHERE id=?`,
		wf.Name, wf.Description, class, wf.CWLVersion, wf.ContentHash, wf.RawCWL,
		string(inputsJSON), string(outputsJSON), string(stepsJSON), s.dialect.jsonDoc(string(labelsJSON)),
		formatTime(wf.UpdatedAt), secretInputsCol, wf.ID,
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

func (s *SQLStore) DeleteWorkflow(ctx context.Context, id string) error {
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

func (s *SQLStore) CreateSubmission(ctx context.Context, sub *model.Submission) error {
	s.logger.Debug("sql", "op", "insert", "table", "submissions", "id", sub.ID)
	return s.insertSubmission(ctx, s.db, sub)
}

// insertSubmission marshals the submission (including token encryption) and
// runs the shared INSERT on the given execer. It is the single insert path
// behind CreateSubmission and CreateSubmissionWithSteps so both persist the
// same field set.
func (s *SQLStore) insertSubmission(ctx context.Context, ex execer, sub *model.Submission) error {
	inputsJSON, err := json.Marshal(sub.Inputs)
	if err != nil {
		return fmt.Errorf("marshal inputs: %w", err)
	}

	// submitted_inputs is an immutable snapshot captured ONCE, here, at
	// creation time. It defaults to a verbatim copy of Inputs unless the
	// caller has already set SubmittedInputs explicitly (e.g. a test
	// constructing a submission directly). No other write path may touch
	// this column — see UpdateSubmissionInputs, which rewrites Inputs alone.
	submittedInputs := sub.SubmittedInputs
	if submittedInputs == nil {
		submittedInputs = sub.Inputs
	}
	submittedInputsJSON, err := json.Marshal(submittedInputs)
	if err != nil {
		return fmt.Errorf("marshal submitted_inputs: %w", err)
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
		s := formatTime(*sub.CompletedAt)
		completedAt = &s
	}

	// Store token expiry as Unix timestamp (0 if not set).
	tokenExpiry := int64(0)
	if !sub.TokenExpiry.IsZero() {
		tokenExpiry = sub.TokenExpiry.Unix()
	}

	// Encrypt the submitter's provider token before it touches disk. The
	// in-memory sub.UserToken stays plaintext for the delegation path.
	storedToken, err := s.encryptToken(sub.UserToken, submissionTokenAAD(sub.ID))
	if err != nil {
		return err
	}

	// Secrets are write-once here, like submitted_inputs above: no other
	// write path touches these four columns (secrets_purged_at is later
	// updated only by the dedicated PurgeSubmissionSecrets). Empty Secrets
	// encrypts to "" (stored as SQL NULL) and secret_names/secrets_purged_at
	// follow suit so a submission with no secrets leaves all four columns
	// NULL/empty.
	storedSecrets, err := s.encryptSecretsMap(sub.Secrets, submissionSecretsAAD(sub.ID))
	if err != nil {
		return err
	}
	var secretsCol, secretNamesCol *string
	if storedSecrets != "" {
		secretsCol = &storedSecrets
	}
	if len(sub.SecretNames) > 0 {
		b, err := json.Marshal(sub.SecretNames)
		if err != nil {
			return fmt.Errorf("marshal secret_names: %w", err)
		}
		s := string(b)
		secretNamesCol = &s
	}

	_, err = ex.ExecContext(ctx,
		`INSERT INTO submissions (id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, output_destination, output_state,
		 prestage_started_at, prestage_completed_at, poststage_started_at, poststage_completed_at, submitted_inputs,
		 secrets, secret_names, secrets_retention, secrets_purged_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.WorkflowID, sub.WorkflowName, string(sub.State),
		string(inputsJSON), string(outputsJSON), string(labelsJSON),
		sub.SubmittedBy, formatTime(sub.CreatedAt), completedAt,
		storedToken, tokenExpiry, sub.AuthProvider, sub.ParentTaskID,
		sub.OutputDestination, sub.OutputState,
		formatTimePtr(sub.PrestageStartedAt), formatTimePtr(sub.PrestageCompletedAt),
		formatTimePtr(sub.PoststageStartedAt), formatTimePtr(sub.PoststageCompletedAt),
		string(submittedInputsJSON),
		secretsCol, secretNamesCol, sub.SecretsRetention, formatTimePtr(sub.SecretsPurgedAt),
	)
	return err
}

// CreateSubmissionWithSteps creates a submission and all its step instances in
// ONE transaction (all-or-nothing): a crash or a bad step insert mid-batch can
// never leave a submission row without its step instances — the zero-step
// window that would let the scheduler's finalize phase complete an empty child
// submission. Token encryption follows the CreateSubmission path via the
// shared insertSubmission helper.
func (s *SQLStore) CreateSubmissionWithSteps(ctx context.Context, sub *model.Submission, steps []*model.StepInstance) error {
	s.logger.Debug("sql", "op", "insert_with_steps", "table", "submissions", "id", sub.ID, "steps", len(steps))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := s.insertSubmission(ctx, tx, sub); err != nil {
		return fmt.Errorf("insert submission %s: %w", sub.ID, err)
	}
	for _, si := range steps {
		if err := s.insertStepInstance(ctx, tx, si); err != nil {
			return fmt.Errorf("insert step instance %s: %w", si.ID, err)
		}
	}

	return tx.Commit()
}

func (s *SQLStore) GetSubmission(ctx context.Context, id string) (*model.Submission, error) {
	s.logger.Debug("sql", "op", "select", "table", "submissions", "id", id)

	var sub model.Submission
	var inputsJSON, outputsJSON, labelsJSON, errorJSON string
	var state, createdAt string
	var completedAt *string
	var tokenExpiry int64
	var prestageStartedAt, prestageCompletedAt, poststageStartedAt, poststageCompletedAt *string
	var submittedInputsJSON *string
	var secretsCol, secretNamesJSON, secretsPurgedAt *string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, error, output_destination, output_state,
		 prestage_started_at, prestage_completed_at, poststage_started_at, poststage_completed_at, submitted_inputs,
		 secrets, secret_names, secrets_retention, secrets_purged_at
		 FROM submissions WHERE id = ?`, id,
	).Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
		&inputsJSON, &outputsJSON, &labelsJSON,
		&sub.SubmittedBy, &createdAt, &completedAt,
		&sub.UserToken, &tokenExpiry, &sub.AuthProvider, &sub.ParentTaskID, &errorJSON,
		&sub.OutputDestination, &sub.OutputState,
		&prestageStartedAt, &prestageCompletedAt, &poststageStartedAt, &poststageCompletedAt,
		&submittedInputsJSON,
		&secretsCol, &secretNamesJSON, &sub.SecretsRetention, &secretsPurgedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sub.State = model.SubmissionState(state)
	if sub.UserToken, err = s.decryptToken(sub.UserToken, submissionTokenAAD(sub.ID)); err != nil {
		return nil, fmt.Errorf("decrypt user token: %w", err)
	}
	var storedSecrets string
	if secretsCol != nil {
		storedSecrets = *secretsCol
	}
	if sub.Secrets, err = s.decryptSecretsMap(storedSecrets, submissionSecretsAAD(sub.ID)); err != nil {
		return nil, fmt.Errorf("decrypt submission secrets: %w", err)
	}
	if secretNamesJSON != nil {
		if err := unmarshalJSON(*secretNamesJSON, &sub.SecretNames, "secret_names"); err != nil {
			return nil, err
		}
	}
	if sub.SecretsPurgedAt, err = scanTimePtr(secretsPurgedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(inputsJSON, &sub.Inputs, "inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &sub.Outputs, "outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &sub.Labels, "labels"); err != nil {
		return nil, err
	}
	// submitted_inputs is nullable: NULL means no snapshot (pre-#239 row),
	// scanned as nil and left as a nil map, no error.
	if submittedInputsJSON != nil {
		if err := unmarshalJSON(*submittedInputsJSON, &sub.SubmittedInputs, "submitted_inputs"); err != nil {
			return nil, err
		}
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
	if sub.PrestageStartedAt, err = scanTimePtr(prestageStartedAt); err != nil {
		return nil, err
	}
	if sub.PrestageCompletedAt, err = scanTimePtr(prestageCompletedAt); err != nil {
		return nil, err
	}
	if sub.PoststageStartedAt, err = scanTimePtr(poststageStartedAt); err != nil {
		return nil, err
	}
	if sub.PoststageCompletedAt, err = scanTimePtr(poststageCompletedAt); err != nil {
		return nil, err
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

func (s *SQLStore) ListSubmissions(ctx context.Context, opts model.ListOptions) ([]*model.Submission, int, error) {
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
	if opts.WorkflowName != "" {
		// Match submissions against every workflow ID ever registered under
		// this name, not just the newest one — re-registering a workflow
		// under the same name creates a new ID/row, and earlier submissions
		// still reference the older IDs.
		whereClauses = append(whereClauses, "workflow_id IN (SELECT id FROM workflows WHERE name = ?)")
		countArgs = append(countArgs, opts.WorkflowName)
	}
	if opts.DateStart != "" {
		whereClauses = append(whereClauses, "created_at >= ?")
		// No fraction and no zone suffix: this prefix sorts before every
		// timestamp of that day, whatever its fractional digits.
		countArgs = append(countArgs, opts.DateStart+"T00:00:00")
	}
	if opts.DateEnd != "" {
		whereClauses = append(whereClauses, "created_at <= ?")
		countArgs = append(countArgs, opts.DateEnd+"T23:59:59Z")
	}
	if opts.Search != "" {
		whereClauses = append(whereClauses, `(LOWER(workflow_name) LIKE LOWER(?) OR LOWER(id) LIKE LOWER(?))`)
		pat := "%" + opts.Search + "%"
		countArgs = append(countArgs, pat, pat)
	}
	if opts.SubmittedBy != "" {
		whereClauses = append(whereClauses, "submitted_by = ?")
		countArgs = append(countArgs, opts.SubmittedBy)
	}
	if opts.ExcludeChildren {
		whereClauses = append(whereClauses, "(parent_task_id = '' OR parent_task_id IS NULL)")
	}
	if states := opts.OutputStates(); len(states) > 0 {
		placeholders := make([]string, len(states))
		for i, st := range states {
			placeholders[i] = "?"
			countArgs = append(countArgs, st)
		}
		whereClauses = append(whereClauses, "output_state IN ("+strings.Join(placeholders, ",")+")")
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
	listQuery := `SELECT ` + submissionListColumns + `
		FROM submissions` + whereSQL + ` ORDER BY ` + orderSQL + ` LIMIT ? OFFSET ?`
	listArgs := append(countArgs, opts.Limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	subs, err := s.scanSubmissionRows(rows)
	return subs, total, err
}

// submissionListColumns is the column set scanned by scanSubmissionRows. New
// columns are appended at the end so the position of every existing column
// stays stable.
// secret_names/secrets_retention/secrets_purged_at are metadata only (#260):
// never the encrypted secrets column itself, so list rows can report
// SecretsState() without a per-row decrypt.
const submissionListColumns = `id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, output_destination, output_state, prestage_started_at, prestage_completed_at, poststage_started_at, poststage_completed_at, secret_names, secrets_retention, secrets_purged_at`

// scanSubmissionRows scans submissionListColumns rows, skipping (with an error
// log) rows that fail token decryption or JSON/timestamp parsing so one corrupt
// row cannot hide the rest of the result set.
func (s *SQLStore) scanSubmissionRows(rows *sql.Rows) ([]*model.Submission, error) {
	var subs []*model.Submission
	for rows.Next() {
		var sub model.Submission
		var inputsJSON, outputsJSON, labelsJSON string
		var state, createdAt string
		var completedAt *string
		var tokenExpiry int64
		var prestageStartedAt, prestageCompletedAt, poststageStartedAt, poststageCompletedAt *string
		var secretNamesJSON, secretsPurgedAt *string

		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
			&inputsJSON, &outputsJSON, &labelsJSON,
			&sub.SubmittedBy, &createdAt, &completedAt,
			&sub.UserToken, &tokenExpiry, &sub.AuthProvider, &sub.ParentTaskID,
			&sub.OutputDestination, &sub.OutputState,
			&prestageStartedAt, &prestageCompletedAt, &poststageStartedAt, &poststageCompletedAt,
			&secretNamesJSON, &sub.SecretsRetention, &secretsPurgedAt); err != nil {
			return nil, err
		}

		sub.State = model.SubmissionState(state)
		var err error
		if sub.UserToken, err = s.decryptToken(sub.UserToken, submissionTokenAAD(sub.ID)); err != nil {
			slog.Error("skipping submission row with undecryptable token", "id", sub.ID, "error", err)
			continue
		}
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
		if sub.PrestageStartedAt, err = scanTimePtr(prestageStartedAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if sub.PrestageCompletedAt, err = scanTimePtr(prestageCompletedAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if sub.PoststageStartedAt, err = scanTimePtr(poststageStartedAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if sub.PoststageCompletedAt, err = scanTimePtr(poststageCompletedAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}
		if secretNamesJSON != nil {
			if err := unmarshalJSON(*secretNamesJSON, &sub.SecretNames, "secret_names"); err != nil {
				slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
				continue
			}
		}
		if sub.SecretsPurgedAt, err = scanTimePtr(secretsPurgedAt); err != nil {
			slog.Error("skipping corrupt submission row", "id", sub.ID, "error", err)
			continue
		}

		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

// ListSubmissionsAwaitingOutputStaging returns COMPLETED submissions that have
// an output destination and no recorded delivery outcome — the exact set the
// scheduler's post-stage phase must process. Filtering in SQL keeps the
// per-tick cost bounded by pending deliveries instead of every COMPLETED
// submission in the database.
func (s *SQLStore) ListSubmissionsAwaitingOutputStaging(ctx context.Context) ([]*model.Submission, error) {
	s.logger.Debug("sql", "op", "list_awaiting_output_staging", "table", "submissions")

	query := `SELECT ` + submissionListColumns + `
		FROM submissions
		WHERE state = 'COMPLETED' AND output_destination != '' AND (output_state = '' OR output_state IS NULL)
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubmissionRows(rows)
}

// ListSubmissionsAwaitingPrestage returns RUNNING submissions whose server-side
// workspace pre-staging was started but never completed
// (prestage_started_at IS NOT NULL AND prestage_completed_at IS NULL). This is
// the recovery half of the scheduler's pre-stage loop selection (#268/#269):
// PENDING submissions are already covered by the loop's existing PENDING scan,
// but a submission that raced onto RUNNING before pre-staging finished (or one
// left stuck by the pre-fix deadlock) must keep being retried until pre-stage
// either completes or exhausts its failure threshold — otherwise it is never
// looked at again and dispatchReady defers it forever. No pagination cap here:
// unlike the PENDING scan, this set is self-limiting — every RUNNING row
// either completes pre-staging within a few ticks or fails outright once
// prestageFailThreshold is reached (becoming terminal and leaving this set).
func (s *SQLStore) ListSubmissionsAwaitingPrestage(ctx context.Context) ([]*model.Submission, error) {
	s.logger.Debug("sql", "op", "list_awaiting_prestage", "table", "submissions")

	query := `SELECT ` + submissionListColumns + `
		FROM submissions
		WHERE state = 'RUNNING' AND prestage_started_at IS NOT NULL AND prestage_completed_at IS NULL
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubmissionRows(rows)
}

func (s *SQLStore) CountSubmissionsByState(ctx context.Context, since time.Time, submittedBy string) (map[string]int, error) {
	s.logger.Debug("sql", "op", "count_by_state", "table", "submissions", "since", since)

	query := `SELECT state, COUNT(*) FROM submissions`
	var where []string
	var args []any
	if !since.IsZero() {
		where = append(where, `created_at >= ?`)
		args = append(args, formatTime(since))
	}
	if submittedBy != "" {
		where = append(where, `submitted_by = ?`)
		args = append(args, submittedBy)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
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

// GetSubmissionMeta is a lean SELECT of workflow_name and labels only — no
// inputs/outputs/tasks/timestamps, no token decryption — for label lookups
// (e.g. Prometheus workflow labeling) that only need to name a submission's
// workflow.
func (s *SQLStore) GetSubmissionMeta(ctx context.Context, id string) (*model.SubmissionMeta, error) {
	s.logger.Debug("sql", "op", "select_meta", "table", "submissions", "id", id)

	var meta model.SubmissionMeta
	var labelsJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT workflow_name, labels FROM submissions WHERE id = ?`, id,
	).Scan(&meta.WorkflowName, &labelsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &meta.Labels, "labels"); err != nil {
		return nil, err
	}
	return &meta, nil
}

// execSubmissionUpdate marshals the mutable submission columns and runs the
// shared UPDATE with the given WHERE clause, returning the number of rows
// affected. It is the single write path behind UpdateSubmission and
// FinalizeSubmission so both persist the same field set.
func (s *SQLStore) execSubmissionUpdate(ctx context.Context, sub *model.Submission, where string, whereArgs ...any) (int64, error) {
	outputsJSON, err := json.Marshal(sub.Outputs)
	if err != nil {
		return 0, fmt.Errorf("marshal outputs: %w", err)
	}
	labelsJSON, err := json.Marshal(sub.Labels)
	if err != nil {
		return 0, fmt.Errorf("marshal labels: %w", err)
	}

	errorJSON := ""
	if sub.Error != nil {
		if b, err := json.Marshal(sub.Error); err == nil {
			errorJSON = string(b)
		}
	}

	var completedAt *string
	if sub.CompletedAt != nil {
		v := formatTime(*sub.CompletedAt)
		completedAt = &v
	}

	args := append([]any{
		string(sub.State), string(outputsJSON), string(labelsJSON), errorJSON,
		completedAt, sub.OutputDestination, sub.OutputState,
		formatTimePtr(sub.PrestageStartedAt), formatTimePtr(sub.PrestageCompletedAt),
		formatTimePtr(sub.PoststageStartedAt), formatTimePtr(sub.PoststageCompletedAt),
	}, whereArgs...)
	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET state=?, outputs=?, labels=?, error=?, completed_at=?, output_destination=?, output_state=?,
		 prestage_started_at=?, prestage_completed_at=?, poststage_started_at=?, poststage_completed_at=?`+where,
		args...,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

func (s *SQLStore) UpdateSubmission(ctx context.Context, sub *model.Submission) error {
	s.logger.Debug("sql", "op", "update", "table", "submissions", "id", sub.ID)

	n, err := s.execSubmissionUpdate(ctx, sub, ` WHERE id=?`, sub.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("submission %s not found", sub.ID)
	}
	return nil
}

// FinalizeSubmission writes the same field set as UpdateSubmission, but only
// while the submission is still active (compare-and-set): a row already in a
// terminal state (CANCELLED, COMPLETED, FAILED) is left untouched. Returns
// applied=false (no error) when the guard rejected the write, so callers can
// re-read and observe the winning state.
func (s *SQLStore) FinalizeSubmission(ctx context.Context, sub *model.Submission) (bool, error) {
	s.logger.Debug("sql", "op", "finalize", "table", "submissions", "id", sub.ID)

	n, err := s.execSubmissionUpdate(ctx, sub, ` WHERE id=? AND state NOT IN (?, ?, ?)`,
		sub.ID,
		string(model.SubmissionStateCancelled), string(model.SubmissionStateCompleted), string(model.SubmissionStateFailed))
	if err != nil {
		return false, fmt.Errorf("finalize submission %s: %w", sub.ID, err)
	}
	return n > 0, nil
}

// UpdateSubmissionIfState writes the submission only while the row still
// carries the expected (state, output_state) pair — see store.Store.
func (s *SQLStore) UpdateSubmissionIfState(ctx context.Context, sub *model.Submission, expectState model.SubmissionState, expectOutputState string) (bool, error) {
	s.logger.Debug("sql", "op", "update_if_state", "table", "submissions", "id", sub.ID,
		"expect_state", expectState, "expect_output_state", expectOutputState)

	n, err := s.execSubmissionUpdate(ctx, sub, ` WHERE id=? AND state=? AND output_state=?`,
		sub.ID, string(expectState), expectOutputState)
	if err != nil {
		return false, fmt.Errorf("update submission %s if state: %w", sub.ID, err)
	}
	return n > 0, nil
}

// ActivateSubmission moves a submission from PENDING to RUNNING
// (compare-and-set). Returns applied=false (no error) when the submission is
// no longer PENDING — e.g. it was cancelled mid-tick — so the activation
// cannot resurrect a terminal submission.
func (s *SQLStore) ActivateSubmission(ctx context.Context, id string) (bool, error) {
	s.logger.Debug("sql", "op", "activate", "table", "submissions", "id", id)

	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET state=? WHERE id=? AND state=?`,
		string(model.SubmissionStateRunning), id, string(model.SubmissionStatePending))
	if err != nil {
		return false, fmt.Errorf("activate submission %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// PurgeSubmissionSecrets clears a submission's secret values (secrets=NULL)
// while keeping secret_names for auditability, and stamps secrets_purged_at.
// Used by the scheduler's retention sweep once a submission's
// SecretsRetention policy says its secret values are due for removal; see
// ListSubmissionsWithSecretsForRetention.
func (s *SQLStore) PurgeSubmissionSecrets(ctx context.Context, id string, at time.Time) error {
	s.logger.Debug("sql", "op", "purge_secrets", "table", "submissions", "id", id)

	result, err := s.db.ExecContext(ctx,
		`UPDATE submissions SET secrets=NULL, secrets_purged_at=? WHERE id=?`,
		formatTime(at), id)
	if err != nil {
		return fmt.Errorf("purge submission secrets %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("submission %s not found", id)
	}
	return nil
}

// ListSubmissionsWithSecretsForRetention returns terminal-state submissions
// that still carry secret values (submissions.secrets IS NOT NULL/non-empty),
// with just enough loaded (state, created_at, completed_at,
// secrets_retention, secret_names) for the scheduler's retention sweep to
// decide whether to purge and to log a purge by count. Secret values are
// never decrypted or returned here — every result's Secrets field is nil —
// so this query is cheap to run every tick.
func (s *SQLStore) ListSubmissionsWithSecretsForRetention(ctx context.Context) ([]*model.Submission, error) {
	s.logger.Debug("sql", "op", "list_secrets_for_retention", "table", "submissions")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, created_at, completed_at, secrets_retention, secret_names
		 FROM submissions
		 WHERE secrets IS NOT NULL AND secrets != '' AND state IN (?, ?, ?)`,
		string(model.SubmissionStateCompleted), string(model.SubmissionStateFailed), string(model.SubmissionStateCancelled))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*model.Submission
	for rows.Next() {
		var sub model.Submission
		var state, createdAt string
		var completedAt, secretNamesJSON *string
		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state, &createdAt, &completedAt, &sub.SecretsRetention, &secretNamesJSON); err != nil {
			return nil, err
		}
		sub.State = model.SubmissionState(state)
		if sub.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			return nil, err
		}
		if sub.CompletedAt, err = scanTimePtr(completedAt); err != nil {
			return nil, err
		}
		if secretNamesJSON != nil {
			if err := unmarshalJSON(*secretNamesJSON, &sub.SecretNames, "secret_names"); err != nil {
				return nil, err
			}
		}
		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

func (s *SQLStore) DeleteSubmission(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "submissions", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Delete child tasks first.
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE submission_id = ?`, id); err != nil {
		return fmt.Errorf("delete tasks: %w", err)
	}

	// Delete child step instances.
	if _, err := tx.ExecContext(ctx, `DELETE FROM step_instances WHERE submission_id = ?`, id); err != nil {
		return fmt.Errorf("delete step_instances: %w", err)
	}

	// Delete the submission itself.
	result, err := tx.ExecContext(ctx, `DELETE FROM submissions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete submission: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("submission %s not found", id)
	}

	return tx.Commit()
}

func (s *SQLStore) UpdateSubmissionInputs(ctx context.Context, id string, inputs map[string]any) error {
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

// GetChildSubmissions returns every submission whose parent_task_id matches
// the given proxy task. The scan is deliberately STRICT (unlike the lenient
// scanSubmissionRows): a corrupt row must surface as an error, never be
// silently skipped — the scheduler treats "no children" as a crash window and
// repairs it by creating a new child, so converting "child exists but is
// unreadable" into "no child" would create a duplicate child submission.
func (s *SQLStore) GetChildSubmissions(ctx context.Context, parentTaskID string) ([]*model.Submission, error) {
	s.logger.Debug("sql", "op", "list_children", "table", "submissions", "parent_task_id", parentTaskID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, parent_task_id, error,
		 prestage_started_at, prestage_completed_at, poststage_started_at, poststage_completed_at
		 FROM submissions WHERE parent_task_id = ? ORDER BY created_at`, parentTaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*model.Submission
	for rows.Next() {
		var sub model.Submission
		var inputsJSON, outputsJSON, labelsJSON, errorJSON string
		var state, createdAt string
		var completedAt *string
		var prestageStartedAt, prestageCompletedAt, poststageStartedAt, poststageCompletedAt *string

		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.WorkflowName, &state,
			&inputsJSON, &outputsJSON, &labelsJSON,
			&sub.SubmittedBy, &createdAt, &completedAt, &sub.ParentTaskID, &errorJSON,
			&prestageStartedAt, &prestageCompletedAt, &poststageStartedAt, &poststageCompletedAt); err != nil {
			return nil, err
		}

		sub.State = model.SubmissionState(state)
		if errorJSON != "" {
			var subErr model.SubmissionError
			if err := unmarshalJSON(errorJSON, &subErr, "error"); err != nil {
				return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
			}
			sub.Error = &subErr
		}
		if err := unmarshalJSON(inputsJSON, &sub.Inputs, "inputs"); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if err := unmarshalJSON(outputsJSON, &sub.Outputs, "outputs"); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if err := unmarshalJSON(labelsJSON, &sub.Labels, "labels"); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if sub.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if completedAt != nil {
			t, err := parseTimeOrZero(*completedAt)
			if err != nil {
				return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
			}
			sub.CompletedAt = &t
		}
		if sub.PrestageStartedAt, err = scanTimePtr(prestageStartedAt); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if sub.PrestageCompletedAt, err = scanTimePtr(prestageCompletedAt); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if sub.PoststageStartedAt, err = scanTimePtr(poststageStartedAt); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}
		if sub.PoststageCompletedAt, err = scanTimePtr(poststageCompletedAt); err != nil {
			return nil, fmt.Errorf("corrupt child submission row %s: %w", sub.ID, err)
		}

		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

// --- StepInstance operations ---

func (s *SQLStore) CreateStepInstance(ctx context.Context, si *model.StepInstance) error {
	s.logger.Debug("sql", "op", "insert", "table", "step_instances", "id", si.ID)
	return s.insertStepInstance(ctx, s.db, si)
}

// insertStepInstance marshals the step instance and runs the shared INSERT on
// the given execer. It is the single insert path behind CreateStepInstance,
// BatchCreateStepInstances, and CreateSubmissionWithSteps so all persist the
// same field set.
func (s *SQLStore) insertStepInstance(ctx context.Context, ex execer, si *model.StepInstance) error {
	outputsJSON, err := json.Marshal(si.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	var completedAt *string
	if si.CompletedAt != nil {
		v := formatTime(*si.CompletedAt)
		completedAt = &v
	}

	scatterDimsJSON, err := json.Marshal(si.ScatterDims)
	if err != nil {
		return fmt.Errorf("marshal scatter_dims: %w", err)
	}

	var errText *string
	if si.Error != "" {
		errText = &si.Error
	}

	_, err = ex.ExecContext(ctx,
		`INSERT INTO step_instances (id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, error, created_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		si.ID, si.SubmissionID, si.StepID, string(si.State),
		si.ScatterCount, si.ScatterMethod, string(scatterDimsJSON), string(outputsJSON), errText,
		formatTime(si.CreatedAt), completedAt,
	)
	return err
}

func (s *SQLStore) BatchCreateStepInstances(ctx context.Context, steps []*model.StepInstance) error {
	if len(steps) == 0 {
		return nil
	}

	s.logger.Debug("sql", "op", "batch_insert", "table", "step_instances", "count", len(steps))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for _, si := range steps {
		if err := s.insertStepInstance(ctx, tx, si); err != nil {
			return fmt.Errorf("insert step instance %s: %w", si.ID, err)
		}
	}

	return tx.Commit()
}

func (s *SQLStore) GetStepInstance(ctx context.Context, id string) (*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "select", "table", "step_instances", "id", id)

	var si model.StepInstance
	var state, outputsJSON, createdAt string
	var completedAt *string
	var errText *string
	var scatterDimsJSON string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, error, created_at, completed_at
		 FROM step_instances WHERE id = ?`, id,
	).Scan(&si.ID, &si.SubmissionID, &si.StepID, &state,
		&si.ScatterCount, &si.ScatterMethod, &scatterDimsJSON, &outputsJSON, &errText, &createdAt, &completedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	si.State = model.StepInstanceState(state)
	if errText != nil {
		si.Error = *errText
	}
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

func (s *SQLStore) UpdateStepInstance(ctx context.Context, si *model.StepInstance) error {
	s.logger.Debug("sql", "op", "update", "table", "step_instances", "id", si.ID)
	return s.execStepInstanceUpdate(ctx, s.db, si)
}

// execStepInstanceUpdate marshals the mutable step-instance columns and runs
// the shared UPDATE on the given execer. It is the single write path behind
// UpdateStepInstance and CreateTasksAndDispatchStep so both persist the same
// field set.
func (s *SQLStore) execStepInstanceUpdate(ctx context.Context, ex execer, si *model.StepInstance) error {
	outputsJSON, err := json.Marshal(si.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	var completedAt *string
	if si.CompletedAt != nil {
		v := formatTime(*si.CompletedAt)
		completedAt = &v
	}

	scatterDimsJSON, err := json.Marshal(si.ScatterDims)
	if err != nil {
		return fmt.Errorf("marshal scatter_dims: %w", err)
	}

	var errText *string
	if si.Error != "" {
		errText = &si.Error
	}

	result, err := ex.ExecContext(ctx,
		`UPDATE step_instances SET state=?, scatter_count=?, scatter_method=?, scatter_dims=?, outputs=?, error=?, completed_at=? WHERE id=?`,
		string(si.State), si.ScatterCount, si.ScatterMethod, string(scatterDimsJSON), string(outputsJSON), errText, completedAt, si.ID,
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

func (s *SQLStore) ListStepsBySubmission(ctx context.Context, submissionID string) ([]*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "list", "table", "step_instances", "submission_id", submissionID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, error, created_at, completed_at
		 FROM step_instances WHERE submission_id = ? ORDER BY created_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanStepInstances(rows)
}

func (s *SQLStore) ListStepsByState(ctx context.Context, state model.StepInstanceState) ([]*model.StepInstance, error) {
	s.logger.Debug("sql", "op", "list_by_state", "table", "step_instances", "state", state)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, submission_id, step_id, state, scatter_count, scatter_method, scatter_dims, outputs, error, created_at, completed_at
		 FROM step_instances WHERE state = ? ORDER BY created_at`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanStepInstances(rows)
}

func (s *SQLStore) CancelNonTerminalSteps(ctx context.Context, submissionID string, completedAt time.Time) (int, error) {
	s.logger.Debug("sql", "op", "cancel_non_terminal_steps", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE step_instances
		 SET state = ?, completed_at = ?
		 WHERE submission_id = ? AND state NOT IN (?, ?, ?)`,
		string(model.StepStateSkipped), formatTime(completedAt),
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

func (s *SQLStore) scanStepInstances(rows *sql.Rows) ([]*model.StepInstance, error) {
	var items []*model.StepInstance
	for rows.Next() {
		var si model.StepInstance
		var state, outputsJSON, createdAt string
		var completedAt *string
		var errText *string
		var scatterDimsJSON string

		if err := rows.Scan(&si.ID, &si.SubmissionID, &si.StepID, &state,
			&si.ScatterCount, &si.ScatterMethod, &scatterDimsJSON, &outputsJSON, &errText, &createdAt, &completedAt); err != nil {
			return nil, err
		}

		si.State = model.StepInstanceState(state)
		if errText != nil {
			si.Error = *errText
		}
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

// execer abstracts *sqlDB and *sqlTx so shared write helpers can run
// standalone or inside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *SQLStore) CreateTask(ctx context.Context, task *model.Task) error {
	s.logger.Debug("sql", "op", "insert", "table", "tasks", "id", task.ID)
	return s.insertTask(ctx, s.db, task)
}

// taskColumns is the column set returned by every unscoped `SELECT * FROM
// tasks` query and the column order for `INSERT INTO tasks`. Scanned
// positionally by scanTask, scanTasks, and scanCheckoutRow (CheckoutTask) —
// all three must stay in this exact order. New columns are appended at the
// end so the position of every existing column stays stable.
const taskColumns = `id, submission_id, step_id, step_instance_id, state, executor_type, external_id,
	 bvbrc_app_id, inputs, outputs, depends_on, priority, retry_count, max_retries,
	 stdout, stderr, exit_code, created_at, started_at, completed_at,
	 tool, job, runtime_hints, scatter_index, dispatched_at, stage_in_ms, stage_out_ms,
	 charged_cores, charged_ram_mb, charged_gpus`

// checkInjectBlessed refuses to persist a non-terminal task that asks for the
// user's token to be injected into the tool environment without naming the
// blessing that authorised it (model.ErrUnblessedInject). Terminal tasks are
// exempt so a failed/cancelled row can always be written.
func checkInjectBlessed(task *model.Task) error {
	if h := task.RuntimeHints; h != nil && h.InjectBVBRCToken && h.TokenBlessingID == "" && !task.State.IsTerminal() {
		return fmt.Errorf("task %s: %w", task.ID, model.ErrUnblessedInject)
	}
	return nil
}

// insertTask marshals the task (including runtime-hint encryption) and runs
// the shared INSERT on the given execer. It is the single insert path behind
// CreateTask and CreateTasksAndDispatchStep so both persist the same field
// set.
func (s *SQLStore) insertTask(ctx context.Context, ex execer, task *model.Task) error {
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
	if err := checkInjectBlessed(task); err != nil {
		return err
	}
	runtimeHintsJSON, err := s.marshalRuntimeHints(task.RuntimeHints, task.ID)
	if err != nil {
		return err
	}

	var startedAt, completedAt, dispatchedAt *string
	if task.StartedAt != nil {
		s := formatTime(*task.StartedAt)
		startedAt = &s
	}
	if task.CompletedAt != nil {
		s := formatTime(*task.CompletedAt)
		completedAt = &s
	}
	if task.DispatchedAt != nil {
		s := formatTime(*task.DispatchedAt)
		dispatchedAt = &s
	}

	_, err = ex.ExecContext(ctx,
		`INSERT INTO tasks (`+taskColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.SubmissionID, task.StepID, task.StepInstanceID, string(task.State),
		string(task.ExecutorType), task.ExternalID, task.BVBRCAppID,
		string(inputsJSON), string(outputsJSON), string(dependsOnJSON),
		task.Priority, task.RetryCount, task.MaxRetries,
		task.Stdout, task.Stderr, task.ExitCode,
		formatTime(task.CreatedAt), startedAt, completedAt,
		string(toolJSON), string(jobJSON), string(runtimeHintsJSON),
		task.ScatterIndex, dispatchedAt, task.StageInMs, task.StageOutMs,
		nullableInt(task.ChargedCores), nullableInt(task.ChargedRamMB), nullableInt(task.ChargedGPUs),
	)
	return err
}

// CreateTasksAndDispatchStep creates every task of a dispatched step and
// persists the step instance's new state in ONE transaction: either all task
// rows and the step update land, or none do. A crash (or a bad task) mid-batch
// can therefore never leave a DISPATCHED step with a partial task set — the
// step stays READY and the dispatch is cleanly retried next tick.
func (s *SQLStore) CreateTasksAndDispatchStep(ctx context.Context, tasks []*model.Task, si *model.StepInstance) error {
	s.logger.Debug("sql", "op", "create_tasks_dispatch_step", "table", "tasks", "count", len(tasks), "step_instance_id", si.ID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for _, task := range tasks {
		if err := s.insertTask(ctx, tx, task); err != nil {
			return fmt.Errorf("insert task %s: %w", task.ID, err)
		}
	}

	if err := s.execStepInstanceUpdate(ctx, tx, si); err != nil {
		return fmt.Errorf("update step_instance %s: %w", si.ID, err)
	}

	return tx.Commit()
}

func (s *SQLStore) GetTask(ctx context.Context, id string) (*model.Task, error) {
	s.logger.Debug("sql", "op", "select", "table", "tasks", "id", id)
	return s.scanTask(s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+`
		 FROM tasks WHERE id = ?`, id))
}

func (s *SQLStore) ListTasksBySubmission(ctx context.Context, submissionID string) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list", "table", "tasks", "submission_id", submissionID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+`
		 FROM tasks WHERE submission_id = ? ORDER BY created_at`, submissionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

func (s *SQLStore) ListTasksBySubmissionPaged(ctx context.Context, submissionID string, opts model.ListOptions) ([]*model.Task, int, error) {
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
		`SELECT `+taskColumns+`
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

func (s *SQLStore) ListTasksByStepInstance(ctx context.Context, stepInstanceID string) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list", "table", "tasks", "step_instance_id", stepInstanceID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+`
		 FROM tasks WHERE step_instance_id = ? ORDER BY scatter_index, created_at`, stepInstanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

// execTaskUpdate marshals the mutable task columns (encrypting any embedded
// HTTP bearer token via marshalRuntimeHints) and runs the shared UPDATE with
// the given WHERE clause, returning the number of rows affected. It is the
// single write path behind UpdateTask and TerminalizeTask so both persist the
// same field set.
func (s *SQLStore) execTaskUpdate(ctx context.Context, task *model.Task, where string, whereArgs ...any) (int64, error) {
	// Sanitize NaN/Inf values before marshaling (Go's encoding/json rejects them).
	sanitizeFloats(task.Outputs)
	sanitizeFloats(task.Tool)
	sanitizeFloats(task.Job)

	outputsJSON, err := json.Marshal(task.Outputs)
	if err != nil {
		return 0, fmt.Errorf("marshal outputs: %w", err)
	}
	toolJSON, err := json.Marshal(task.Tool)
	if err != nil {
		return 0, fmt.Errorf("marshal tool: %w", err)
	}
	jobJSON, err := json.Marshal(task.Job)
	if err != nil {
		return 0, fmt.Errorf("marshal job: %w", err)
	}
	if err := checkInjectBlessed(task); err != nil {
		return 0, err
	}
	runtimeHintsJSON, err := s.marshalRuntimeHints(task.RuntimeHints, task.ID)
	if err != nil {
		return 0, err
	}

	var startedAt, completedAt, dispatchedAt *string
	if task.StartedAt != nil {
		v := formatTime(*task.StartedAt)
		startedAt = &v
	}
	if task.CompletedAt != nil {
		v := formatTime(*task.CompletedAt)
		completedAt = &v
	}
	if task.DispatchedAt != nil {
		v := formatTime(*task.DispatchedAt)
		dispatchedAt = &v
	}

	args := append([]any{
		string(task.State), string(task.ExecutorType), task.ExternalID,
		string(outputsJSON), task.Priority, task.RetryCount, task.MaxRetries,
		task.Stdout, task.Stderr, task.ExitCode,
		startedAt, completedAt,
		string(toolJSON), string(jobJSON), string(runtimeHintsJSON),
		task.StepInstanceID, task.ScatterIndex,
		dispatchedAt, task.StageInMs, task.StageOutMs,
	}, whereArgs...)
	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state=?, executor_type=?, external_id=?,
		 outputs=?, priority=?, retry_count=?, max_retries=?, stdout=?, stderr=?, exit_code=?,
		 started_at=?, completed_at=?, tool=?, job=?, runtime_hints=?,
		 step_instance_id=?, scatter_index=?,
		 dispatched_at=?, stage_in_ms=?, stage_out_ms=?`+where,
		args...,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

func (s *SQLStore) UpdateTask(ctx context.Context, task *model.Task) error {
	s.logger.Debug("sql", "op", "update", "table", "tasks", "id", task.ID)

	n, err := s.execTaskUpdate(ctx, task, ` WHERE id=?`, task.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task %s not found", task.ID)
	}
	return nil
}

// TerminalizeTask writes the same field set as UpdateTask, but only while the
// task is not already terminal (SUCCESS, FAILED, SKIPPED) — compare-and-set,
// so a concurrent terminal write (e.g. a cancel that SKIPPED the task) is
// never overwritten. Returns applied=false (no error) when the guard rejected
// the write, so callers can re-read and observe the winning state.
func (s *SQLStore) TerminalizeTask(ctx context.Context, task *model.Task) (bool, error) {
	s.logger.Debug("sql", "op", "terminalize", "table", "tasks", "id", task.ID)

	n, err := s.execTaskUpdate(ctx, task, ` WHERE id=? AND state NOT IN (?, ?, ?)`,
		task.ID,
		string(model.TaskStateSuccess), string(model.TaskStateFailed), string(model.TaskStateSkipped))
	if err != nil {
		return false, fmt.Errorf("terminalize task %s: %w", task.ID, err)
	}
	return n > 0, nil
}

// TerminalizeTaskFrom writes the same field set as UpdateTask, but only while
// the task is still in the exact `from` state — a tighter compare-and-set
// than TerminalizeTask's "not already terminal" guard. Use it when the
// caller's snapshot is only valid for one specific state: e.g. a task
// observed as QUEUED that a worker may have since checked out (moving it to
// RUNNING with a new external_id). TerminalizeTask's NOT-IN guard would still
// let that write through and clobber the checkout; this method's exact-state
// guard does not. Returns applied=false (no error) when the task has already
// moved on, so the caller can leave it alone.
func (s *SQLStore) TerminalizeTaskFrom(ctx context.Context, task *model.Task, from model.TaskState) (bool, error) {
	s.logger.Debug("sql", "op", "terminalize_from", "table", "tasks", "id", task.ID, "from", from)

	n, err := s.execTaskUpdate(ctx, task, ` WHERE id=? AND state=?`, task.ID, string(from))
	if err != nil {
		return false, fmt.Errorf("terminalize task %s from %s: %w", task.ID, from, err)
	}
	return n > 0, nil
}

// CASTaskState moves a task from one exact state to another (compare-and-set).
// Returns applied=false (no error) when the task is no longer in the `from`
// state — e.g. a concurrent cancel SKIPPED it — so a stale snapshot can never
// resurrect a terminal task (retry marking FAILED→RETRYING, retry claiming
// RETRYING→SCHEDULED). Only the state column is written; any bookkeeping
// happens in the caller after a successful claim.
func (s *SQLStore) CASTaskState(ctx context.Context, id string, from, to model.TaskState) (bool, error) {
	s.logger.Debug("sql", "op", "cas_state", "table", "tasks", "id", id, "from", from, "to", to)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state=? WHERE id=? AND state=?`,
		string(to), id, string(from))
	if err != nil {
		return false, fmt.Errorf("cas task %s state %s->%s: %w", id, from, to, err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// MarkTaskRunning transitions a task QUEUED→RUNNING (compare-and-set),
// stamping started_at only when it is not already set (COALESCE keeps a
// pre-existing stamp, e.g. one written by CheckoutTask). It deliberately
// writes no other column — never external_id — so a stale poll snapshot can
// never clobber a concurrent checkout's worker assignment (the F-J zombie
// class). Returns applied=false (no error) when the task is no longer QUEUED.
func (s *SQLStore) MarkTaskRunning(ctx context.Context, id string) (bool, error) {
	s.logger.Debug("sql", "op", "mark_running", "table", "tasks", "id", id)

	now := formatTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET state = ?, started_at = COALESCE(started_at, ?) WHERE id = ? AND state = ?`,
		string(model.TaskStateRunning), now, id, string(model.TaskStateQueued))
	if err != nil {
		return false, fmt.Errorf("mark task %s running: %w", id, err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// UpdateTaskPriority sets only the priority column. A full-row
// read-modify-write here would be in the same clobber class as F-J: it could
// overwrite a concurrent checkout's external_id/started_at.
func (s *SQLStore) UpdateTaskPriority(ctx context.Context, id string, priority int) error {
	s.logger.Debug("sql", "op", "update_priority", "table", "tasks", "id", id, "priority", priority)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET priority = ? WHERE id = ?`, priority, id)
	if err != nil {
		return fmt.Errorf("update task %s priority: %w", id, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("task %s not found", id)
	}
	return nil
}

// ScrubTaskSecretsForSubmission clears RuntimeHints.Secrets and any
// StagerOverrides.HTTPCredential from every task belonging to submissionID,
// persisting the change with a narrow runtime_hints-only write (see
// UpdateTaskPriority: a full-row write here could clobber a concurrent
// checkout/report). Tasks with nothing to scrub are left untouched. Returns
// the number of task rows actually rewritten.
func (s *SQLStore) ScrubTaskSecretsForSubmission(ctx context.Context, submissionID string) (int, error) {
	s.logger.Debug("sql", "op", "scrub_secrets", "table", "tasks", "submission_id", submissionID)

	tasks, err := s.ListTasksBySubmission(ctx, submissionID)
	if err != nil {
		return 0, fmt.Errorf("list tasks for submission %s: %w", submissionID, err)
	}

	scrubbed := 0
	for _, t := range tasks {
		if t.RuntimeHints == nil {
			continue
		}
		hasSecret := len(t.RuntimeHints.Secrets) > 0 ||
			(t.RuntimeHints.StagerOverrides != nil && t.RuntimeHints.StagerOverrides.HTTPCredential != nil)
		if !hasSecret {
			continue
		}
		t.RuntimeHints.ScrubSecrets()
		runtimeHintsJSON, err := s.marshalRuntimeHints(t.RuntimeHints, t.ID)
		if err != nil {
			return scrubbed, fmt.Errorf("marshal scrubbed runtime_hints for task %s: %w", t.ID, err)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET runtime_hints = ? WHERE id = ?`, runtimeHintsJSON, t.ID,
		); err != nil {
			return scrubbed, fmt.Errorf("scrub secrets for task %s: %w", t.ID, err)
		}
		scrubbed++
	}
	return scrubbed, nil
}

func (s *SQLStore) GetTasksByState(ctx context.Context, state model.TaskState) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "list_by_state", "table", "tasks", "state", state)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+`
		 FROM tasks WHERE state = ? ORDER BY created_at`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

// CountTasksByState returns the current global count of tasks per state (a
// single GROUP BY query), used to refresh the gowe_tasks{state} gauge once
// per scheduler tick.
func (s *SQLStore) CountTasksByState(ctx context.Context) (map[string]int, error) {
	s.logger.Debug("sql", "op", "count_by_state", "table", "tasks")

	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks GROUP BY state`)
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

// CountTasksQueuedByGroup returns the current count of QUEUED tasks per
// worker group (a single GROUP BY query on the runtime_hints worker_group
// JSON field, ” normalized to "default" in SQL so the map's keys already
// match the gowe_queue_depth{group} label convention), used to refresh that
// gauge once per scheduler tick. json_extract reads the plaintext
// worker_group field directly — only the nested HTTP bearer credential
// inside runtime_hints is ever encrypted (see tokens.go), so this needs no
// decryption.
func (s *SQLStore) CountTasksQueuedByGroup(ctx context.Context) (map[string]int, error) {
	s.logger.Debug("sql", "op", "count_queued_by_group", "table", "tasks")

	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(NULLIF(`+s.dialect.jsonText("runtime_hints", "worker_group")+`, ''), 'default') AS grp, COUNT(*)
		 FROM tasks WHERE state = ?
		 GROUP BY grp`, string(model.TaskStateQueued))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var group string
		var count int
		if err := rows.Scan(&group, &count); err != nil {
			return nil, err
		}
		counts[group] = count
	}
	return counts, rows.Err()
}

// CountRunningTasksByWorker returns the number of RUNNING worker-executor
// tasks per owning worker id (CheckoutTask stores the worker id in
// external_id; rows without an owner are excluded), used by the read-only
// per-host view (#323). The WHERE clause is served by idx_tasks_state_executor.
func (s *SQLStore) CountRunningTasksByWorker(ctx context.Context) (map[string]int, error) {
	s.logger.Debug("sql", "op", "count_running_by_worker", "table", "tasks")

	rows, err := s.db.QueryContext(ctx,
		`SELECT external_id, COUNT(*) FROM tasks
		 WHERE state = ? AND executor_type = ? AND external_id != ''
		 GROUP BY external_id`,
		string(model.TaskStateRunning), string(model.ExecutorTypeWorker))
	if err != nil {
		return nil, fmt.Errorf("count running tasks by worker: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var workerID string
		var count int
		if err := rows.Scan(&workerID, &count); err != nil {
			return nil, fmt.Errorf("count running tasks by worker: scan: %w", err)
		}
		counts[workerID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count running tasks by worker: %w", err)
	}
	return counts, nil
}

// SumChargedByWorker returns the resources charged at checkout to the
// RUNNING worker-executor tasks of each owning worker id (a NULL charge, from
// a task checked out by an older server, counts as 0). Used by the per-host
// view and the host gauges; filtering by worker state is the caller's job.
func (s *SQLStore) SumChargedByWorker(ctx context.Context) (map[string]model.WorkerCharge, error) {
	s.logger.Debug("sql", "op", "sum_charged_by_worker", "table", "tasks")

	rows, err := s.db.QueryContext(ctx,
		`SELECT external_id, CAST(COALESCE(SUM(charged_cores), 0) AS BIGINT), CAST(COALESCE(SUM(charged_ram_mb), 0) AS BIGINT) FROM tasks
		 WHERE state = ? AND executor_type = ? AND external_id != ''
		 GROUP BY external_id`,
		string(model.TaskStateRunning), string(model.ExecutorTypeWorker))
	if err != nil {
		return nil, fmt.Errorf("sum charged by worker: %w", err)
	}
	defer rows.Close()

	charges := make(map[string]model.WorkerCharge)
	for rows.Next() {
		var workerID string
		var c model.WorkerCharge
		if err := rows.Scan(&workerID, &c.Cores, &c.RamMB); err != nil {
			return nil, fmt.Errorf("sum charged by worker: scan: %w", err)
		}
		charges[workerID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sum charged by worker: %w", err)
	}
	return charges, nil
}

func (s *SQLStore) GetActiveTasks(ctx context.Context) ([]*model.Task, error) {
	s.logger.Debug("sql", "op", "get_active_tasks")

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+`
		 FROM tasks WHERE state IN (?, ?, ?, ?)
		 ORDER BY priority DESC, created_at`,
		string(model.TaskStatePending), string(model.TaskStateScheduled),
		string(model.TaskStateQueued), string(model.TaskStateRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanTasks(rows)
}

// CancelNonTerminalTasks SKIPs every non-terminal task of a submission EXCEPT
// sub-workflow proxy tasks (executor_type='subworkflow'): proxies are cancelled
// by the scheduler's per-tick reconciliation (pollSubworkflowTask), which is
// the only path that also cancels the proxy's child submission — skipping a
// proxy here would remove it from the RUNNING scan and orphan its child.
func (s *SQLStore) CancelNonTerminalTasks(ctx context.Context, submissionID string, completedAt time.Time) (int, error) {
	s.logger.Debug("sql", "op", "cancel_non_terminal_tasks", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks
		 SET state = ?, completed_at = ?
		 WHERE submission_id = ? AND state NOT IN (?, ?, ?) AND executor_type != ?`,
		string(model.TaskStateSkipped), formatTime(completedAt),
		submissionID,
		string(model.TaskStateSuccess), string(model.TaskStateFailed), string(model.TaskStateSkipped),
		string(model.ExecutorTypeSubworkflow))
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal tasks: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cancel non-terminal tasks rows affected: %w", err)
	}
	return int(n), nil
}

func (s *SQLStore) GetTaskSummaries(ctx context.Context, submissionIDs []string) (map[string]model.TaskSummary, error) {
	if len(submissionIDs) == 0 {
		return nil, nil
	}
	s.logger.Debug("sql", "op", "get_task_summaries", "count", len(submissionIDs))

	placeholders := make([]string, len(submissionIDs))
	args := make([]any, len(submissionIDs))
	for i, id := range submissionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT submission_id, state, COUNT(*) FROM tasks
		WHERE submission_id IN (` + strings.Join(placeholders, ",") + `)
		GROUP BY submission_id, state`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get task summaries: %w", err)
	}
	defer rows.Close()

	result := make(map[string]model.TaskSummary)
	for rows.Next() {
		var subID, state string
		var count int
		if err := rows.Scan(&subID, &state, &count); err != nil {
			return nil, fmt.Errorf("scan task summary: %w", err)
		}
		ts := result[subID]
		ts.Total += count
		switch model.TaskState(state) {
		case model.TaskStatePending:
			ts.Pending = count
		case model.TaskStateScheduled:
			ts.Scheduled = count
		case model.TaskStateQueued:
			ts.Queued = count
		case model.TaskStateRunning:
			ts.Running = count
		case model.TaskStateSuccess:
			ts.Success = count
		case model.TaskStateFailed:
			ts.Failed = count
		case model.TaskStateSkipped:
			ts.Skipped = count
		}
		result[subID] = ts
	}
	return result, rows.Err()
}

// ResetFailedTasks also clears started_at (M1): without this, a retried
// bvbrc task's MarkTaskRunning (COALESCE(started_at, ?)) would preserve the
// PRIOR attempt's stamp instead of stamping the new one, because a non-nil
// started_at survives from the failed attempt otherwise.
func (s *SQLStore) ResetFailedTasks(ctx context.Context, submissionID string) (int, error) {
	s.logger.Debug("sql", "op", "reset_failed_tasks", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks
		 SET state = ?, retry_count = 0, started_at = NULL, completed_at = NULL, stderr = '', exit_code = NULL,
		     stage_in_ms = NULL, stage_out_ms = NULL
		 WHERE submission_id = ? AND state = ?`,
		string(model.TaskStatePending), submissionID, string(model.TaskStateFailed))
	if err != nil {
		return 0, fmt.Errorf("reset failed tasks: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *SQLStore) ResetFailedSteps(ctx context.Context, submissionID string) (int, error) {
	s.logger.Debug("sql", "op", "reset_failed_steps", "submission_id", submissionID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE step_instances
		 SET state = ?, completed_at = NULL, error = NULL
		 WHERE submission_id = ? AND state = ?`,
		string(model.StepStateWaiting), submissionID, string(model.StepStateFailed))
	if err != nil {
		return 0, fmt.Errorf("reset failed steps: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// --- scan helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func (s *SQLStore) scanTask(row scanner) (*model.Task, error) {
	var task model.Task
	var inputsJSON, outputsJSON, dependsOnJSON string
	var toolJSON, jobJSON, runtimeHintsJSON string
	var state, executorType, createdAt string
	var startedAt, completedAt, dispatchedAt *string

	err := row.Scan(
		&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &state,
		&executorType, &task.ExternalID, &task.BVBRCAppID,
		&inputsJSON, &outputsJSON, &dependsOnJSON,
		&task.Priority, &task.RetryCount, &task.MaxRetries,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&createdAt, &startedAt, &completedAt,
		&toolJSON, &jobJSON, &runtimeHintsJSON,
		&task.ScatterIndex, &dispatchedAt, &task.StageInMs, &task.StageOutMs,
		&task.ChargedCores, &task.ChargedRamMB, &task.ChargedGPUs,
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
	if err := s.revealRuntimeHints(task.RuntimeHints, task.ID); err != nil {
		return nil, fmt.Errorf("decrypt runtime_hints token: %w", err)
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
	if dispatchedAt != nil {
		t, err := parseTimeOrZero(*dispatchedAt)
		if err != nil {
			return nil, err
		}
		task.DispatchedAt = &t
	}

	return &task, nil
}

func (s *SQLStore) scanTasks(rows *sql.Rows) ([]*model.Task, error) {
	var tasks []*model.Task
	for rows.Next() {
		var task model.Task
		var inputsJSON, outputsJSON, dependsOnJSON string
		var toolJSON, jobJSON, runtimeHintsJSON string
		var state, executorType, createdAt string
		var startedAt, completedAt, dispatchedAt *string

		if err := rows.Scan(
			&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &state,
			&executorType, &task.ExternalID, &task.BVBRCAppID,
			&inputsJSON, &outputsJSON, &dependsOnJSON,
			&task.Priority, &task.RetryCount, &task.MaxRetries,
			&task.Stdout, &task.Stderr, &task.ExitCode,
			&createdAt, &startedAt, &completedAt,
			&toolJSON, &jobJSON, &runtimeHintsJSON,
			&task.ScatterIndex, &dispatchedAt, &task.StageInMs, &task.StageOutMs,
			&task.ChargedCores, &task.ChargedRamMB, &task.ChargedGPUs,
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
		if err := s.revealRuntimeHints(task.RuntimeHints, task.ID); err != nil {
			slog.Error("skipping task row with undecryptable token", "id", task.ID, "error", err)
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
		if dispatchedAt != nil {
			t, err := parseTimeOrZero(*dispatchedAt)
			if err != nil {
				slog.Error("skipping corrupt task row", "id", task.ID, "error", err)
				continue
			}
			task.DispatchedAt = &t
		}

		tasks = append(tasks, &task)
	}
	return tasks, rows.Err()
}

// --- Session operations ---

func (s *SQLStore) CreateSession(ctx context.Context, sess *model.Session) error {
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

func (s *SQLStore) GetSession(ctx context.Context, id string) (*model.Session, error) {
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

func (s *SQLStore) DeleteSession(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "sessions", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *SQLStore) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	s.logger.Debug("sql", "op", "delete_expired", "table", "sessions")

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLStore) DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error) {
	s.logger.Debug("sql", "op", "delete_by_user", "table", "sessions", "user_id", userID)

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// --- Worker operations ---

func (s *SQLStore) CreateWorker(ctx context.Context, w *model.Worker) error {
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

	gpusJSON, err := nullableGPUsJSON(w.GPUs)
	if err != nil {
		return err
	}

	usageJSON, err := nullableUsageJSON(w.Usage)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workers (`+workerColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Hostname, group, string(w.State), string(w.Runtime),
		string(labelsJSON), formatTime(w.LastSeen),
		w.CurrentTask, formatTime(w.RegisteredAt),
		gpuEnabled, w.GPUDevice, string(datasetsJSON), w.Version, w.KeyID,
		nullableInt(w.MaxCPUs), nullableInt(w.MaxMemMB),
		w.HostID, nullableInt(w.HostCPUs), nullableInt(w.HostMemMB), nullableInt(w.Slots),
		nullableBool(w.CgroupCPU), nullableBool(w.CgroupMemory), gpusJSON,
		usageJSON, formatTimePtr(w.UsageAt),
		nullableInt(w.DefaultTaskCores), nullableInt(w.DefaultTaskRamMB),
	)
	return err
}

// workerColumns is the single column list for workers reads/inserts; its order
// must match scanWorker and the CreateWorker argument list.
const workerColumns = "id, name, hostname, worker_group, state, runtime, labels, last_seen, current_task, registered_at, gpu_enabled, gpu_device, datasets, version, key_id, max_cpus, max_mem_mb, host_id, host_cpus, host_mem_mb, slots, cgroup_cpu, cgroup_memory, gpus, host_usage, host_usage_at, default_task_cores, default_task_ram_mb"

type rowScanner interface {
	Scan(dest ...any) error
}

// corruptWorkerRowError marks a row that scanned but whose JSON/time columns
// do not decode; list callers skip such rows, GetWorker returns the error.
type corruptWorkerRowError struct {
	id  string
	err error
}

func (e *corruptWorkerRowError) Error() string { return e.err.Error() }
func (e *corruptWorkerRowError) Unwrap() error { return e.err }

// scanWorker scans one workerColumns row. A raw sql error (including
// sql.ErrNoRows) is returned as-is; a decode failure is a *corruptWorkerRowError.
func scanWorker(sc rowScanner) (*model.Worker, error) {
	var w model.Worker
	var state, runtime, labelsJSON, datasetsJSON, lastSeen, registeredAt string
	var gpuEnabled int
	var maxCPUs, maxMem sql.NullInt64
	var hostCPUs, hostMem, slots, cgroupCPU, cgroupMem sql.NullInt64
	var defaultCores, defaultMem sql.NullInt64
	var gpusJSON, usageJSON, usageAt *string
	if err := sc.Scan(&w.ID, &w.Name, &w.Hostname, &w.Group, &state, &runtime,
		&labelsJSON, &lastSeen, &w.CurrentTask, &registeredAt, &gpuEnabled, &w.GPUDevice,
		&datasetsJSON, &w.Version, &w.KeyID, &maxCPUs, &maxMem,
		&w.HostID, &hostCPUs, &hostMem, &slots, &cgroupCPU, &cgroupMem, &gpusJSON,
		&usageJSON, &usageAt, &defaultCores, &defaultMem); err != nil {
		return nil, err
	}
	w.State = model.WorkerState(state)
	w.Runtime = model.ContainerRuntime(runtime)
	w.GPUEnabled = gpuEnabled != 0
	if maxCPUs.Valid {
		v := int(maxCPUs.Int64)
		w.MaxCPUs = &v
	}
	if maxMem.Valid {
		v := maxMem.Int64
		w.MaxMemMB = &v
	}
	if defaultCores.Valid {
		v := int(defaultCores.Int64)
		w.DefaultTaskCores = &v
	}
	if defaultMem.Valid {
		v := defaultMem.Int64
		w.DefaultTaskRamMB = &v
	}
	if hostCPUs.Valid {
		v := int(hostCPUs.Int64)
		w.HostCPUs = &v
	}
	if hostMem.Valid {
		v := hostMem.Int64
		w.HostMemMB = &v
	}
	if slots.Valid {
		v := int(slots.Int64)
		w.Slots = &v
	}
	if cgroupCPU.Valid {
		v := cgroupCPU.Int64 != 0
		w.CgroupCPU = &v
	}
	if cgroupMem.Valid {
		v := cgroupMem.Int64 != 0
		w.CgroupMemory = &v
	}
	var err error
	if err = unmarshalJSON(labelsJSON, &w.Labels, "labels"); err != nil {
		return nil, &corruptWorkerRowError{w.ID, err}
	}
	if err = unmarshalJSON(datasetsJSON, &w.Datasets, "datasets"); err != nil {
		return nil, &corruptWorkerRowError{w.ID, err}
	}
	// NULL = not reported (nil); "[]" = reported, empty (#323).
	if gpusJSON != nil {
		if err = unmarshalJSON(*gpusJSON, &w.GPUs, "gpus"); err != nil {
			return nil, &corruptWorkerRowError{w.ID, err}
		}
	}
	// NULL = usage never reported (#323). Usage is telemetry: an undecodable
	// value is dropped rather than making the row (and so every heartbeat of
	// the worker) fail.
	if usageJSON != nil {
		var u model.WorkerUsage
		if uerr := unmarshalJSON(*usageJSON, &u, "host_usage"); uerr != nil {
			slog.Warn("ignoring corrupt worker usage", "id", w.ID, "error", uerr)
		} else if at, terr := scanTimePtr(usageAt); terr != nil {
			slog.Warn("ignoring corrupt worker usage time", "id", w.ID, "error", terr)
		} else {
			w.Usage, w.UsageAt = &u, at
		}
	}
	if w.LastSeen, err = parseTimeOrZero(lastSeen); err != nil {
		return nil, &corruptWorkerRowError{w.ID, err}
	}
	if w.RegisteredAt, err = parseTimeOrZero(registeredAt); err != nil {
		return nil, &corruptWorkerRowError{w.ID, err}
	}
	return &w, nil
}

// nullableInt maps a nil capacity pointer to SQL NULL.
func nullableInt[T int | int64](p *T) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}

// nullableBool maps a nil flag to SQL NULL and true/false to 1/0.
func nullableBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return int64(1)
	}
	return int64(0)
}

// nullableGPUsJSON maps nil GPUs (not reported) to SQL NULL and any reported
// list, including an empty one, to its JSON encoding.
func nullableGPUsJSON(gpus []model.GPUInfo) (*string, error) {
	if gpus == nil {
		return nil, nil
	}
	b, err := json.Marshal(gpus)
	if err != nil {
		return nil, fmt.Errorf("marshal gpus: %w", err)
	}
	s := string(b)
	return &s, nil
}

// nullableUsageJSON maps nil usage (never reported) to SQL NULL and any
// reported usage to its JSON encoding.
func nullableUsageJSON(u *model.WorkerUsage) (*string, error) {
	if u == nil {
		return nil, nil
	}
	b, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("marshal host usage: %w", err)
	}
	s := string(b)
	return &s, nil
}

func (s *SQLStore) GetWorker(ctx context.Context, id string) (*model.Worker, error) {
	s.logger.Debug("sql", "op", "select", "table", "workers", "id", id)

	w, err := scanWorker(s.db.QueryRowContext(ctx,
		`SELECT `+workerColumns+` FROM workers WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *SQLStore) UpdateWorker(ctx context.Context, w *model.Worker) error {
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

	gpusJSON, err := nullableGPUsJSON(w.GPUs)
	if err != nil {
		return err
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE workers SET name=?, hostname=?, worker_group=?, state=?, runtime=?, labels=?,
		 last_seen=?, current_task=?, gpu_enabled=?, gpu_device=?, datasets=?, max_cpus=?, max_mem_mb=?,
		 host_id=?, host_cpus=?, host_mem_mb=?, slots=?, cgroup_cpu=?, cgroup_memory=?, gpus=?,
		 default_task_cores=?, default_task_ram_mb=? WHERE id=?`,
		w.Name, w.Hostname, w.Group, string(w.State), string(w.Runtime),
		string(labelsJSON), formatTime(w.LastSeen),
		w.CurrentTask, gpuEnabled, w.GPUDevice, string(datasetsJSON),
		nullableInt(w.MaxCPUs), nullableInt(w.MaxMemMB),
		w.HostID, nullableInt(w.HostCPUs), nullableInt(w.HostMemMB), nullableInt(w.Slots),
		nullableBool(w.CgroupCPU), nullableBool(w.CgroupMemory), gpusJSON,
		nullableInt(w.DefaultTaskCores), nullableInt(w.DefaultTaskRamMB), w.ID,
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

// TouchWorker records a heartbeat in a single UPDATE: state and last_seen,
// plus host_usage and host_usage_at (= lastSeen) only when usage is non-nil,
// so a heartbeat without usage keeps the stored one. Unlike UpdateWorker it
// never writes current_task, so it cannot clobber a concurrent CheckoutTask.
func (s *SQLStore) TouchWorker(ctx context.Context, id string, state model.WorkerState, lastSeen time.Time, usage *model.WorkerUsage) error {
	s.logger.Debug("sql", "op", "touch", "table", "workers", "id", id, "usage", usage != nil)

	ts := formatTime(lastSeen)
	var result sql.Result
	var err error
	if usage == nil {
		result, err = s.db.ExecContext(ctx,
			`UPDATE workers SET state=?, last_seen=? WHERE id=?`, string(state), ts, id)
	} else {
		usageJSON, merr := nullableUsageJSON(usage)
		if merr != nil {
			return merr
		}
		result, err = s.db.ExecContext(ctx,
			`UPDATE workers SET state=?, last_seen=?, host_usage=?, host_usage_at=? WHERE id=?`,
			string(state), ts, usageJSON, ts, id)
	}
	if err != nil {
		return fmt.Errorf("touch worker: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker %s not found", id)
	}
	return nil
}

// DeleteWorker removes the worker row and, in the same transaction, requeues
// the worker's RUNNING worker-executor tasks (same reset as RequeueWorkerTasks)
// so they are not orphaned against a deleted worker. Returns an error and
// changes nothing if the worker does not exist.
func (s *SQLStore) DeleteWorker(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "workers", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `DELETE FROM workers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete worker: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker %s not found", id)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL,
		     charged_cores = NULL, charged_ram_mb = NULL, charged_gpus = NULL
		 WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`, id)
	if err != nil {
		return fmt.Errorf("requeue tasks: %w", err)
	}
	requeued, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("requeue tasks: rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if requeued > 0 {
		s.logger.Info("requeued tasks of deleted worker", "worker_id", id, "count", requeued)
	}
	return nil
}

// AdoptWorkerKey binds a legacy worker row (key_id = ”) to keyID. It is a
// compare-and-set: it reports true only when this call performed the binding,
// so two concurrent adopters cannot both win.
func (s *SQLStore) AdoptWorkerKey(ctx context.Context, id, keyID string) (bool, error) {
	s.logger.Debug("sql", "op", "update", "table", "workers", "id", id, "col", "key_id")
	res, err := s.db.ExecContext(ctx,
		`UPDATE workers SET key_id = ? WHERE id = ? AND key_id = ''`, keyID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListWorkersByKey returns the workers bound to the given worker key id.
func (s *SQLStore) ListWorkersByKey(ctx context.Context, keyID string) ([]*model.Worker, error) {
	return s.listWorkers(ctx, "WHERE key_id = ?", keyID)
}

func (s *SQLStore) ListWorkers(ctx context.Context) ([]*model.Worker, error) {
	return s.listWorkers(ctx, "")
}

func (s *SQLStore) listWorkers(ctx context.Context, where string, args ...any) ([]*model.Worker, error) {
	s.logger.Debug("sql", "op", "list", "table", "workers")

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+workerColumns+` FROM workers `+where+` ORDER BY registered_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []*model.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			var corrupt *corruptWorkerRowError
			if errors.As(err, &corrupt) {
				slog.Error("skipping corrupt worker row", "id", corrupt.id, "error", corrupt.err)
				continue
			}
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

func (s *SQLStore) ListWorkerGroups(ctx context.Context) ([]string, error) {
	s.logger.Debug("sql", "op", "list_groups", "table", "workers")

	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT worker_group FROM workers ORDER BY worker_group`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// CheckoutTask atomically finds a QUEUED worker task and transitions it to
// RUNNING, assigning it to the given worker. Returns nil if no task is available.
// Selection considers runtime capability, worker group, and dataset affinity:
//   - Runtime: if runtime is "none", only tasks without docker_image are eligible.
//   - Group: if workerGroup is non-empty, only tasks with matching or empty WorkerGroup.
//   - Prestage datasets: task is skipped if worker lacks a required prestage dataset.
//   - Cache datasets: among eligible tasks, prefer the one with the highest cache score
//     (number of matching cache datasets). Ties are broken by creation order (oldest first).
//   - Capacity: per dimension (cores, RAM) the request must fit the effective
//     capacity (min of the worker's max and its host's capacity), and the
//     task's charge (request, else the worker's default share, else 0) must
//     fit what is free on the worker and on its host (host_id + key_id)
//     after the charges of RUNNING tasks. A task that charges 0 always fits.
//     The charge is recorded on the task (charged_cores, charged_ram_mb).
//   - GPUs (worker level only): a task needing k = GPUCount GPUs is offered
//     to a GPU worker whose capacity (model.Worker.GPUCapacity) is known
//     when k fits it and, for k > 0, fits what the worker's RUNNING tasks
//     leave free; to a GPU worker of unknown capacity only when k <= 1, with
//     no free gate; never to a CPU-only worker when k > 0. charged_gpus = k.
//   - Hold (only with SetCheckoutHoldAfter > 0): when the first task in queue
//     order that this worker could take but for the free capacity on its own
//     worker gate has waited longer than the hold-after duration, and would
//     fit the host gate if this worker's own tasks finished, tasks behind it
//     that charge a dimension it is short in are not handed out (see
//     findHolder). When the task a worker held for becomes eligible, no task
//     behind it in queue order is considered at that checkout.
func (s *SQLStore) CheckoutTask(ctx context.Context, workerID string, workerGroup string, runtime model.ContainerRuntime) (*model.Task, error) {
	s.logger.Debug("sql", "op", "checkout_task", "worker_id", workerID, "group", workerGroup, "runtime", runtime)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Look up the worker's datasets, GPU and capacity BEFORE the candidate
	// query: the capacity bounds are part of that query so a run of unfit
	// tasks at the head of the queue cannot fill the LIMIT window and starve
	// fitting tasks behind it (#324).
	var workerDatasetsJSON string
	var workerDatasets map[string]string
	var workerGPUEnabled bool
	var workerMaxCPUs, workerMaxMem sql.NullInt64
	var workerHostID, workerKeyID string
	var workerHostCPUs, workerHostMem, workerDefaultCores, workerDefaultMem sql.NullInt64
	var workerGPUsJSON sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT datasets, gpu_enabled, max_cpus, max_mem_mb, host_id, key_id,
		        host_cpus, host_mem_mb, default_task_cores, default_task_ram_mb, gpus
		 FROM workers WHERE id = ?`, workerID).
		Scan(&workerDatasetsJSON, &workerGPUEnabled, &workerMaxCPUs, &workerMaxMem, &workerHostID, &workerKeyID,
			&workerHostCPUs, &workerHostMem, &workerDefaultCores, &workerDefaultMem, &workerGPUsJSON); err != nil {
		if err != sql.ErrNoRows {
			s.logger.Warn("sql", "op", "checkout_task_load_worker", "worker_id", workerID, "error", err)
		}
		workerDatasets = map[string]string{}
	} else if err := json.Unmarshal([]byte(workerDatasetsJSON), &workerDatasets); err != nil {
		s.logger.Warn("sql", "op", "checkout_task_parse_worker_datasets", "worker_id", workerID, "error", err)
		workerDatasets = map[string]string{}
	}
	// GPU capacity: model.Worker.GPUCapacity on the stored columns. An
	// undecodable inventory is an unknown capacity.
	var workerGPUs []model.GPUInfo
	if workerGPUsJSON.Valid && workerGPUsJSON.String != "" {
		if err := json.Unmarshal([]byte(workerGPUsJSON.String), &workerGPUs); err != nil {
			s.logger.Warn("sql", "op", "checkout_task_parse_worker_gpus", "worker_id", workerID, "error", err)
			workerGPUs = nil
		}
	}
	workerGPUCap := (&model.Worker{GPUEnabled: workerGPUEnabled, GPUs: workerGPUs}).GPUCapacity()

	// Charges of the RUNNING tasks of this worker and of the workers sharing
	// its host and key (the host gate). Offline workers are left out: their
	// tasks are about to be requeued by the reaper. The worker's own tasks
	// count whatever its state. A NULL charge (checked out by an older server) counts as 0.
	// GPUs are counted on this worker only (no host term).
	var workerChargedCores, workerChargedMem, hostChargedCores, hostChargedMem, workerChargedGPUs int64
	if err := tx.QueryRowContext(ctx,
		`SELECT
		   CAST(COALESCE(SUM(CASE WHEN t.external_id = ? THEN t.charged_cores END), 0) AS BIGINT),
		   CAST(COALESCE(SUM(CASE WHEN t.external_id = ? THEN t.charged_ram_mb END), 0) AS BIGINT),
		   CAST(COALESCE(SUM(CASE WHEN (w.state != 'offline' OR w.id = ?) AND w.host_id = ? AND w.key_id = ? THEN t.charged_cores END), 0) AS BIGINT),
		   CAST(COALESCE(SUM(CASE WHEN (w.state != 'offline' OR w.id = ?) AND w.host_id = ? AND w.key_id = ? THEN t.charged_ram_mb END), 0) AS BIGINT),
		   CAST(COALESCE(SUM(CASE WHEN t.external_id = ? THEN t.charged_gpus END), 0) AS BIGINT)
		 FROM tasks t JOIN workers w ON w.id = t.external_id
		 WHERE t.state = 'RUNNING' AND t.executor_type = 'worker'`,
		workerID, workerID,
		workerID, workerHostID, workerKeyID,
		workerID, workerHostID, workerKeyID,
		workerID).
		Scan(&workerChargedCores, &workerChargedMem, &hostChargedCores, &hostChargedMem, &workerChargedGPUs); err != nil {
		return nil, fmt.Errorf("sum charged resources: %w", err)
	}

	// Per dimension: capCores/capMem bound what a single task may request,
	// freeCores/freeMem what may still be charged. nil disables the check
	// (legacy worker, or no such row); the SQL binds below are NULL then.
	capCores, freeCores := checkoutBounds(workerHostID,
		nullInt64Ptr(workerMaxCPUs), nullInt64Ptr(workerHostCPUs), workerChargedCores, hostChargedCores)
	capMem, freeMem := checkoutBounds(workerHostID,
		nullInt64Ptr(workerMaxMem), nullInt64Ptr(workerHostMem), workerChargedMem, hostChargedMem)
	// Hold rule bounds (see findHolder), per dimension: ownFree is what is
	// free on this worker's own gate (nil without a worker max); drain is
	// what the host gate would leave if this worker's own running tasks
	// finished (nil when the host term does not apply, as in checkoutBounds).
	_, ownFreeCores := checkoutBounds("", nullInt64Ptr(workerMaxCPUs), nil, workerChargedCores, 0)
	_, ownFreeMem := checkoutBounds("", nullInt64Ptr(workerMaxMem), nil, workerChargedMem, 0)
	_, drainCores := checkoutBounds(workerHostID, nil, nullInt64Ptr(workerHostCPUs), 0, hostChargedCores-workerChargedCores)
	_, drainMem := checkoutBounds(workerHostID, nil, nullInt64Ptr(workerHostMem), 0, hostChargedMem-workerChargedMem)
	// GPUs, worker level only (no host term, no default share): a CPU-only
	// worker takes no task that needs a GPU (total 0); a GPU worker with an
	// unknown capacity takes tasks needing at most one GPU, with no free gate
	// and no hold; a known capacity bounds the request and, minus the GPUs
	// charged to this worker's RUNNING tasks, the charge (also its own gate
	// for the hold rule).
	var capGPUs, freeGPUs *int64
	switch {
	case !workerGPUEnabled:
		v := int64(0)
		capGPUs = &v
	case workerGPUCap == nil:
		v := int64(1)
		capGPUs = &v
	default:
		c, f := int64(*workerGPUCap), int64(*workerGPUCap)-workerChargedGPUs
		capGPUs, freeGPUs = &c, &f
	}
	ownFreeGPUs := freeGPUs
	// A non-positive default share is treated as unset.
	defaultCores, defaultMem := positiveInt64Ptr(workerDefaultCores), positiveInt64Ptr(workerDefaultMem)
	// A task whose last dispatch (else creation) is before holdCutoff has
	// waited longer than the hold-after duration (hold rule only).
	holdCutoff := time.Now().UTC().Add(-s.checkoutHoldAfter)

	// Find the oldest QUEUED worker tasks this worker can take. The capacity,
	// group and GPU predicates are part of the query so a run of tasks this
	// worker can never take cannot fill the LIMIT window and hide fitting
	// tasks behind it; the Go loop below re-checks them (defence in depth).
	// A runtime hint read in SQL, NULL when runtime_hints is not valid JSON:
	// numHint as a number, textHint as text, trueHint as a condition that is
	// true when the hint is JSON true.
	d := s.dialect
	validHints := func(expr string) string {
		return `CASE WHEN ` + d.jsonValid("runtime_hints") + ` THEN ` + expr + ` END`
	}
	numHint := func(path string) string { return validHints(d.jsonNumber("runtime_hints", path)) }
	textHint := func(path string) string { return validHints(d.jsonText("runtime_hints", path)) }
	trueHint := func(path string) string { return validHints(d.jsonIsTrue("runtime_hints", path)) }
	// charge is taskCharge in SQL for one dimension; it binds the worker's
	// default share (one ?).
	charge := func(path string) string {
		return `(CASE WHEN COALESCE(` + numHint(path) + `, 0) > 0 THEN ` + numHint(path) + ` ELSE COALESCE(CAST(? AS BIGINT), 0) END)`
	}
	// gpuRequest is model.RuntimeHints.GPUCount in SQL (no binds). It is both
	// the request and the charge of the GPU dimension.
	gpuRequest := `(CASE WHEN COALESCE(` + numHint("gpus") + `, 0) > 0 THEN ` + numHint("gpus") +
		` WHEN COALESCE(` + trueHint("requires_gpu") + `, FALSE) THEN 1 ELSE 0 END)`
	// Per dimension: request and charge are SQL expressions; every
	// occurrence of charge binds chargeArgs.
	dims := []struct {
		request, charge string
		chargeArgs      []any
		total, free     *int64
		ownFree         *int64
	}{
		{`COALESCE(` + numHint("cores") + `, 0)`, charge("cores"), []any{nullableInt(defaultCores)}, capCores, freeCores, ownFreeCores},
		{`COALESCE(` + numHint("ram_mb") + `, 0)`, charge("ram_mb"), []any{nullableInt(defaultMem)}, capMem, freeMem, ownFreeMem},
		{gpuRequest, gpuRequest, nil, capGPUs, freeGPUs, ownFreeGPUs},
	}
	// candidateQuery builds a query for the QUEUED worker tasks this worker
	// can take, in queue order: the aged predicate when aged (first: cheap
	// and usually false), the token-blessing, effective capacity, group and
	// GPU predicates, the free-capacity gate when withFree, then extra
	// (binding extraArgs).
	candidateQuery := func(withFree, aged bool, extra string, extraArgs []any, limit int) (string, []any) {
		query := `SELECT ` + taskColumns + `
		 FROM tasks WHERE state = 'QUEUED' AND executor_type = 'worker'`
		var args []any
		if aged {
			query += `
		   AND COALESCE(dispatched_at, created_at) < ?`
			args = append(args, formatTime(holdCutoff))
		}
		query += `
		   AND NOT COALESCE(CASE WHEN ` + d.jsonValid("runtime_hints") + `
		         THEN (` + d.jsonIsTrue("runtime_hints", "inject_bvbrc_token") + `
		               AND COALESCE(` + d.jsonText("runtime_hints", "token_blessing_id") + `,'') = '') END, FALSE)`
		for _, dim := range dims {
			// Total: the request must fit the effective capacity.
			query += `
		   AND (CAST(? AS BIGINT) IS NULL OR ` + dim.request + ` <= CAST(? AS BIGINT))`
			args = append(args, nullableInt(dim.total), nullableInt(dim.total))
			if withFree {
				// Free: a task that charges nothing always passes, even when
				// free is negative (host over-committed).
				query += `
		   AND (CAST(? AS BIGINT) IS NULL OR ` + dim.charge + ` = 0 OR ` + dim.charge + ` <= CAST(? AS BIGINT))`
				args = append(args, nullableInt(dim.free))
				args = append(args, dim.chargeArgs...)
				args = append(args, dim.chargeArgs...)
				args = append(args, nullableInt(dim.free))
			}
		}
		// Worker group, same semantics as the Go check below.
		group := `COALESCE(` + textHint("worker_group") + `, '')`
		if workerGroup != "default" && workerGroup != "" {
			query += `
		   AND ` + group + ` = ?`
			args = append(args, workerGroup)
		} else {
			query += `
		   AND ` + group + ` IN ('', 'default')`
		}
		// GPU: a CPU-only worker skips tasks that require a GPU.
		if !workerGPUEnabled {
			query += `
		   AND NOT COALESCE(` + trueHint("requires_gpu") + `, FALSE)`
		}
		query += extra + fmt.Sprintf(`
		 ORDER BY priority DESC, created_at LIMIT %d`, limit)
		return query, append(args, extraArgs...)
	}

	// Filter by runtime capability, worker group, and dataset affinity.
	canRunContainers := model.HasContainerRuntime(runtime)

	// checkEligible applies every per-task check of this checkout except the
	// free-capacity gate (shared by the candidate and the hold passes): the
	// effective capacity (defence in depth: the queries apply the same bound),
	// container runtime, GPU, worker group and prestage datasets. It returns
	// the task's cache score (number of matching cache datasets).
	checkEligible := func(task *model.Task) (int, bool) {
		reqCores, reqMem := taskRequest(task)
		if capCores != nil && reqCores > *capCores {
			s.logger.Debug("checkout skip: task exceeds worker cores", "task_id", task.ID, "worker_id", workerID,
				"want", reqCores, "max", *capCores)
			return 0, false
		}
		if capMem != nil && reqMem > *capMem {
			s.logger.Debug("checkout skip: task exceeds worker memory", "task_id", task.ID, "worker_id", workerID,
				"want_mb", reqMem, "max_mb", *capMem)
			return 0, false
		}

		// Check container runtime capability.
		taskNeedsContainer := false
		if task.RuntimeHints != nil && task.RuntimeHints.DockerImage != "" {
			taskNeedsContainer = true
		}
		if img, ok := task.Inputs["_docker_image"].(string); ok && img != "" {
			taskNeedsContainer = true
		}
		if taskNeedsContainer && !canRunContainers {
			return 0, false
		}

		// Check GPU requirement: CPU-only workers skip GPU tasks, and the
		// GPU count must fit the worker's GPU capacity (capGPUs).
		reqGPUs := int64(task.RuntimeHints.GPUCount())
		if reqGPUs > 0 && !workerGPUEnabled {
			return 0, false
		}
		if capGPUs != nil && reqGPUs > *capGPUs {
			s.logger.Debug("checkout skip: task exceeds worker GPUs", "task_id", task.ID, "worker_id", workerID,
				"want", reqGPUs, "max", *capGPUs)
			return 0, false
		}

		// Check worker group matching.
		taskGroup := ""
		if task.RuntimeHints != nil {
			taskGroup = task.RuntimeHints.WorkerGroup
		}
		if workerGroup != "default" && workerGroup != "" {
			if taskGroup != workerGroup {
				return 0, false
			}
		} else {
			if taskGroup != "" && taskGroup != "default" {
				return 0, false
			}
		}

		// Check dataset affinity.
		cacheScore := 0
		if task.RuntimeHints != nil && len(task.RuntimeHints.RequiredDatasets) > 0 {
			missingPrestage := false
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
				return 0, false
			}
		}
		return cacheScore, true
	}

	// chargeOf is what this worker would charge the task, per dimension (the
	// GPU charge is the GPU count: no default share).
	chargeOf := func(task *model.Task) (cores, ramMB, gpus int64) {
		reqCores, reqMem := taskRequest(task)
		return taskCharge(reqCores, defaultCores), taskCharge(reqMem, defaultMem), int64(task.RuntimeHints.GPUCount())
	}

	// An eligible task, scored by affinity.
	type scored struct {
		task       *model.Task
		cacheScore int
		gpuMatch   bool // true if task wants GPU and worker has GPU
	}

	// loadEligible runs the candidate query (free-capacity gate on, plus
	// extra binding extraArgs) and keeps the rows that pass checkEligible and
	// the free-capacity gate (defence in depth: the query applies the same
	// bounds). It returns them in queue order, and whether any of them
	// charges a non-zero amount.
	loadEligible := func(extra string, extraArgs []any) ([]scored, bool, error) {
		query, args := candidateQuery(true, false, extra, extraArgs, 10)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, err
		}

		var candidates []*model.Task
		for rows.Next() {
			task, err := s.scanCheckoutRow(rows)
			if err != nil {
				rows.Close()
				return nil, false, err
			}
			if task != nil {
				candidates = append(candidates, task)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, err
		}

		var eligible []scored
		anyCharged := false // some eligible task charges a non-zero amount
		for _, task := range candidates {
			cacheScore, ok := checkEligible(task)
			if !ok {
				continue
			}
			// Check free capacity (defence in depth: the candidate query already
			// applies the same bounds).
			chargeCores, chargeMem, chargeGPUs := chargeOf(task)
			if freeCores != nil && chargeCores != 0 && chargeCores > *freeCores {
				s.logger.Debug("checkout skip: task exceeds free cores", "task_id", task.ID, "worker_id", workerID,
					"charge", chargeCores, "free", *freeCores)
				continue
			}
			if freeMem != nil && chargeMem != 0 && chargeMem > *freeMem {
				s.logger.Debug("checkout skip: task exceeds free memory", "task_id", task.ID, "worker_id", workerID,
					"charge_mb", chargeMem, "free_mb", *freeMem)
				continue
			}
			if freeGPUs != nil && chargeGPUs != 0 && chargeGPUs > *freeGPUs {
				s.logger.Debug("checkout skip: task exceeds free GPUs", "task_id", task.ID, "worker_id", workerID,
					"charge", chargeGPUs, "free", *freeGPUs)
				continue
			}
			anyCharged = anyCharged || chargeCores != 0 || chargeMem != 0 || chargeGPUs != 0

			eligible = append(eligible, scored{
				task:       task,
				cacheScore: cacheScore,
				gpuMatch:   chargeGPUs > 0 && workerGPUEnabled,
			})
		}
		return eligible, anyCharged, nil
	}

	eligible, anyCharged, err := loadEligible("", nil)
	if err != nil {
		return nil, err
	}

	// findHolder returns the holder of the hold rule and the dimensions it is
	// short in: the first task in queue order that passes every check of this
	// worker except the free-capacity gate, that has waited longer than the
	// hold-after duration since it was last dispatched (else created), that
	// is short on this worker's own gate in some dimension (charge > 0 and
	// above ownFree; never in a dimension without a worker max), and for
	// which holding can help: in each short dimension its charge would fit
	// the host gate if this worker's own running tasks finished (drain). The
	// host gate alone never makes a task short: it is shared with workers
	// that may never see the holder, so holding for it could idle this
	// worker without helping. GPUs have no host term: a task short on GPUs
	// (only with a known GPU capacity) always fits once this worker's own
	// tasks finish. Only the first checkoutHoldScan rows matching
	// in SQL are examined; if none of them qualifies there is no holder. Rows
	// that cannot be scanned, decoded or decrypted are never holders.
	findHolder := func() (*model.Task, bool, bool, bool, error) {
		var short []string
		var extraArgs []any
		for _, dim := range dims {
			if dim.ownFree == nil {
				continue
			}
			short = append(short, `(`+dim.charge+` > 0 AND `+dim.charge+` > ?)`)
			extraArgs = append(extraArgs, dim.chargeArgs...)
			extraArgs = append(extraArgs, dim.chargeArgs...)
			extraArgs = append(extraArgs, *dim.ownFree)
		}
		if len(short) == 0 {
			return nil, false, false, false, nil // no worker max: no task is short
		}
		extra := `
		   AND (` + strings.Join(short, ` OR `) + `)`
		query, args := candidateQuery(false, true, extra, extraArgs, checkoutHoldScan)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, false, false, fmt.Errorf("find checkout holder: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			task, err := s.scanCheckoutRow(rows)
			if err != nil {
				s.logger.Warn("sql", "op", "checkout_hold_scan", "worker_id", workerID, "error", err)
				continue
			}
			if task == nil {
				continue
			}
			if _, ok := checkEligible(task); !ok {
				continue
			}
			c, m, g := chargeOf(task)
			shortCores := ownFreeCores != nil && c > 0 && c > *ownFreeCores
			shortMem := ownFreeMem != nil && m > 0 && m > *ownFreeMem
			shortGPUs := ownFreeGPUs != nil && g > 0 && g > *ownFreeGPUs
			if !shortCores && !shortMem && !shortGPUs {
				continue
			}
			if (shortCores && drainCores != nil && c > *drainCores) || (shortMem && drainMem != nil && m > *drainMem) {
				continue
			}
			return task, shortCores, shortMem, shortGPUs, nil
		}
		if err := rows.Err(); err != nil {
			return nil, false, false, false, fmt.Errorf("find checkout holder: %w", err)
		}
		return nil, false, false, false, nil
	}

	// Hold rule: leave out the tasks behind the holder in queue order that
	// charge a dimension it is short in, so they cannot keep taking the
	// capacity it waits for. Tasks that charge nothing there are never held.
	// No holder search (no query) when this worker has no running charges
	// (nothing can be short on its own gate) or nothing eligible charges
	// capacity (nothing to hold; the recorded holder is then kept so a hold
	// over an otherwise empty queue is not logged again).
	var holder *model.Task
	var shortCores, shortMem, shortGPUs bool
	var protectedID string // the task this worker's previous checkout held for
	if s.checkoutHoldAfter > 0 {
		protectedID = s.heldFor(workerID)
		switch {
		case workerChargedCores == 0 && workerChargedMem == 0 && workerChargedGPUs == 0:
			s.noteHolder(workerID, "")
		case anyCharged:
			if holder, shortCores, shortMem, shortGPUs, err = findHolder(); err != nil {
				return nil, err
			}
			if holder == nil {
				s.noteHolder(workerID, "")
			}
		}
	}
	if holder != nil {
		// Re-run the candidate query without the held tasks, so they cannot
		// fill the LIMIT window and hide the tasks behind them (the bind order
		// follows the predicate: priority, priority, created_at, then one
		// default share per cores / RAM charge expression; the GPU charge
		// binds nothing).
		var short, charged []string
		extraArgs := []any{holder.Priority, holder.Priority, formatTime(holder.CreatedAt)}
		if shortCores {
			short = append(short, "cores")
			charged = append(charged, charge("cores")+` != 0`)
			extraArgs = append(extraArgs, nullableInt(defaultCores))
		}
		if shortMem {
			short = append(short, "ram")
			charged = append(charged, charge("ram_mb")+` != 0`)
			extraArgs = append(extraArgs, nullableInt(defaultMem))
		}
		if shortGPUs {
			short = append(short, "gpus")
			charged = append(charged, gpuRequest+` != 0`)
		}
		extra := `
		   AND NOT ((priority < ? OR (priority = ? AND created_at >= ?)) AND (` + strings.Join(charged, ` OR `) + `))`
		if eligible, _, err = loadEligible(extra, extraArgs); err != nil {
			return nil, err
		}
		// Defence in depth: the query already left the held tasks out.
		kept := eligible[:0]
		for _, e := range eligible {
			behind := e.task.Priority < holder.Priority ||
				(e.task.Priority == holder.Priority && !e.task.CreatedAt.Before(holder.CreatedAt))
			c, m, g := chargeOf(e.task)
			if behind && ((shortCores && c != 0) || (shortMem && m != 0) || (shortGPUs && g != 0)) {
				continue
			}
			kept = append(kept, e)
		}
		eligible = kept

		s.logger.Debug("checkout hold", "worker_id", workerID, "holder_task_id", holder.ID, "short", strings.Join(short, ","))
		if s.noteHolder(workerID, holder.ID) {
			c, m, g := chargeOf(holder)
			attrs := []any{"worker_id", workerID, "holder_task_id", holder.ID, "short", strings.Join(short, ",")}
			if shortCores {
				attrs = append(attrs, "charge_cores", c, "free_cores", *ownFreeCores)
			}
			if shortMem {
				attrs = append(attrs, "charge_ram_mb", m, "free_ram_mb", *ownFreeMem)
			}
			if shortGPUs {
				attrs = append(attrs, "charge_gpus", g, "free_gpus", *ownFreeGPUs)
			}
			s.logger.Info("checkout hold: worker stops taking later tasks that need the capacity a waiting task is short of", attrs...)
		}
	}

	// Protected task: when the task this worker held for at its previous
	// checkout now fits, nothing behind it in queue order is considered, so
	// the capacity freed for it does not go to a better-scored task behind
	// it. Tasks ahead of it keep their place.
	if protectedID != "" {
		for i, e := range eligible {
			if e.task.ID == protectedID {
				eligible = eligible[:i+1]
				break
			}
		}
	}

	var selected *model.Task
	// Select best task: GPU workers prefer GPU tasks, then best cache score,
	// then first.
	{
		bestScore := -1
		for _, e := range eligible {
			score := e.cacheScore
			if workerGPUEnabled && e.gpuMatch {
				score += 100 // Strongly prefer GPU tasks on GPU workers
			}
			if score > bestScore {
				bestScore = score
				selected = e.task
			}
		}
		if selected == nil && len(eligible) > 0 {
			selected = eligible[0].task
		}
	}

	if selected == nil {
		return nil, nil
	}

	// Transition to RUNNING, assign to worker and record the charge (always
	// written, 0 included, so every task checked out here is accounted).
	reqCores, reqMem := taskRequest(selected)
	chargedCores := int(taskCharge(reqCores, defaultCores))
	chargedMem := taskCharge(reqMem, defaultMem)
	chargedGPUs := selected.RuntimeHints.GPUCount()
	now := time.Now().UTC()
	nowStr := formatTime(now)
	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET state = 'RUNNING', external_id = ?, started_at = ?, charged_cores = ?, charged_ram_mb = ?, charged_gpus = ?
		 WHERE id = ? AND state = 'QUEUED'`,
		workerID, nowStr, chargedCores, chargedMem, chargedGPUs, selected.ID)
	if err != nil {
		return nil, fmt.Errorf("update task state: %w", err)
	}
	// The task was QUEUED when this transaction selected it. With a single
	// connection nothing can change it in between; with a connection pool
	// another checkout could. Hand out nothing then: the worker polls again.
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("update task state: %w", err)
	} else if n == 0 {
		return nil, nil
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
	selected.ChargedCores = &chargedCores
	selected.ChargedRamMB = &chargedMem
	selected.ChargedGPUs = &chargedGPUs

	return selected, nil
}

// checkoutHoldScan bounds the hold rule's search (CheckoutTask, findHolder):
// at most this many short, aged tasks are examined per checkout.
const checkoutHoldScan = 50

// checkoutHoldLogMax bounds noteHolder's map (one entry per holding worker).
const checkoutHoldLogMax = 1024

// scanCheckoutRow scans and decodes one row (taskColumns order) of a
// CheckoutTask query. A corrupt or undecryptable row is logged and skipped:
// it returns (nil, nil). An error means the row could not be scanned.
func (s *SQLStore) scanCheckoutRow(rows *sql.Rows) (*model.Task, error) {
	var task model.Task
	var inputsJSON, outputsJSON, dependsOnJSON string
	var toolJSON, jobJSON, runtimeHintsJSON string
	var stateStr, executorType, createdAt string
	var startedAt, completedAt, dispatchedAt *string

	if err := rows.Scan(
		&task.ID, &task.SubmissionID, &task.StepID, &task.StepInstanceID, &stateStr,
		&executorType, &task.ExternalID, &task.BVBRCAppID,
		&inputsJSON, &outputsJSON, &dependsOnJSON,
		&task.Priority, &task.RetryCount, &task.MaxRetries,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&createdAt, &startedAt, &completedAt,
		&toolJSON, &jobJSON, &runtimeHintsJSON,
		&task.ScatterIndex, &dispatchedAt, &task.StageInMs, &task.StageOutMs,
		&task.ChargedCores, &task.ChargedRamMB, &task.ChargedGPUs,
	); err != nil {
		return nil, err
	}

	task.State = model.TaskState(stateStr)
	task.ExecutorType = model.ExecutorType(executorType)
	if err := unmarshalJSON(inputsJSON, &task.Inputs, "inputs"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := unmarshalJSON(outputsJSON, &task.Outputs, "outputs"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := unmarshalJSON(dependsOnJSON, &task.DependsOn, "depends_on"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := unmarshalJSON(toolJSON, &task.Tool, "tool"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := unmarshalJSON(jobJSON, &task.Job, "job"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := unmarshalJSON(runtimeHintsJSON, &task.RuntimeHints, "runtime_hints"); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if err := s.revealRuntimeHints(task.RuntimeHints, task.ID); err != nil {
		slog.Error("skipping task row with undecryptable token in checkout", "id", task.ID, "error", err)
		return nil, nil
	}
	if t, err := parseTimeOrZero(createdAt); err != nil {
		slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
		return nil, nil
	} else {
		task.CreatedAt = t
	}
	if startedAt != nil {
		t, err := parseTimeOrZero(*startedAt)
		if err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			return nil, nil
		}
		task.StartedAt = &t
	}
	if completedAt != nil {
		t, err := parseTimeOrZero(*completedAt)
		if err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			return nil, nil
		}
		task.CompletedAt = &t
	}
	if dispatchedAt != nil {
		t, err := parseTimeOrZero(*dispatchedAt)
		if err != nil {
			slog.Error("skipping corrupt task row in checkout", "id", task.ID, "error", err)
			return nil, nil
		}
		task.DispatchedAt = &t
	}
	return &task, nil
}

// taskRequest returns the cores and RAM (MiB) a task requests, 0 when it
// requests none.
func taskRequest(t *model.Task) (cores, ramMB int64) {
	if t.RuntimeHints == nil {
		return 0, 0
	}
	return int64(t.RuntimeHints.Cores), t.RuntimeHints.RamMB
}

// taskCharge is what checkout charges for one dimension of a task: its
// request when positive, else the worker's default share when set, else 0.
// CheckoutTask's candidate query computes the same value in SQL.
func taskCharge(request int64, defaultShare *int64) int64 {
	if request > 0 {
		return request
	}
	if defaultShare != nil {
		return *defaultShare
	}
	return 0
}

// checkoutBounds computes one resource dimension's checkout limits. total
// bounds what a single task may request: the smaller of the worker's max and
// the host's capacity. free is the smaller of the worker's max minus the
// worker's charged and the host's capacity minus the host's charged; it may
// be negative. The host terms count only when hostID is non-empty and
// hostCap is set. nil means no limit.
func checkoutBounds(hostID string, workerMax, hostCap *int64, workerCharged, hostCharged int64) (total, free *int64) {
	if hostID == "" {
		hostCap = nil
	}
	var workerFree, hostFree *int64
	if workerMax != nil {
		v := *workerMax - workerCharged
		workerFree = &v
	}
	if hostCap != nil {
		v := *hostCap - hostCharged
		hostFree = &v
	}
	return minInt64Ptr(workerMax, hostCap), minInt64Ptr(workerFree, hostFree)
}

// minInt64Ptr returns the smaller of the non-nil values, nil when both are nil.
func minInt64Ptr(a, b *int64) *int64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *b < *a:
		return b
	default:
		return a
	}
}

// nullInt64Ptr maps a NULL column to nil.
func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// positiveInt64Ptr maps a NULL or non-positive column to nil.
func positiveInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid || n.Int64 <= 0 {
		return nil
	}
	v := n.Int64
	return &v
}

// MarkStaleWorkersOffline transitions workers to offline if their last_seen
// is older than the given timeout. Returns the workers that were transitioned.
func (s *SQLStore) MarkStaleWorkersOffline(ctx context.Context, timeout time.Duration) ([]*model.Worker, error) {
	cutoff := formatTime(time.Now().UTC().Add(-timeout))
	s.logger.Debug("sql", "op", "mark_stale_offline", "cutoff", cutoff)

	// Find stale workers first.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+workerColumns+` FROM workers WHERE state = 'online' AND last_seen < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []*model.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			var corrupt *corruptWorkerRowError
			if errors.As(err, &corrupt) {
				slog.Error("skipping corrupt worker row", "id", corrupt.id, "error", corrupt.err)
				continue
			}
			return nil, err
		}
		stale = append(stale, w)
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

// ReapStaleWorkers marks online workers whose last_seen is older than timeout
// offline and requeues their RUNNING worker-executor tasks (same reset as
// RequeueWorkerTasks), in one transaction: a failure leaves the workers online
// so the next pass retries, instead of offline with their tasks RUNNING
// forever. The same pass requeues RUNNING worker tasks of workers that are
// already offline and stale (stranded before this was atomic) and clears
// current_task on every offline stale worker, none of which has a RUNNING
// task left. Stale (last_seen older than timeout) keeps a worker that is
// still offline but has just checked out a task (checkout bumps last_seen)
// out of the stranded requeue. Returns the workers marked offline with their
// requeued counts, and the number of stranded tasks requeued.
func (s *SQLStore) ReapStaleWorkers(ctx context.Context, timeout time.Duration) ([]ReapedWorker, int, error) {
	cutoff := formatTime(time.Now().UTC().Add(-timeout))
	s.logger.Debug("sql", "op", "reap_stale_workers", "cutoff", cutoff)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT `+workerColumns+` FROM workers WHERE state = 'online' AND last_seen < ?`, cutoff)
	if err != nil {
		return nil, 0, fmt.Errorf("select stale workers: %w", err)
	}
	var stale []*model.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			var corrupt *corruptWorkerRowError
			if errors.As(err, &corrupt) {
				// Still marked offline below; its tasks are requeued by the
				// stranded requeue.
				slog.Error("skipping corrupt worker row", "id", corrupt.id, "error", corrupt.err)
				continue
			}
			rows.Close()
			return nil, 0, err
		}
		stale = append(stale, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE workers SET state = 'offline' WHERE state = 'online' AND last_seen < ?`, cutoff); err != nil {
		return nil, 0, fmt.Errorf("mark workers offline: %w", err)
	}

	var reaped []ReapedWorker
	for _, w := range stale {
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL,
			     charged_cores = NULL, charged_ram_mb = NULL, charged_gpus = NULL
			 WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`, w.ID)
		if err != nil {
			return nil, 0, fmt.Errorf("requeue tasks of %s: %w", w.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, 0, fmt.Errorf("requeue tasks of %s: rows affected: %w", w.ID, err)
		}
		w.State = model.WorkerStateOffline
		w.CurrentTask = ""
		reaped = append(reaped, ReapedWorker{Worker: w, Requeued: int(n)})
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL,
		     charged_cores = NULL, charged_ram_mb = NULL, charged_gpus = NULL
		 WHERE state = 'RUNNING' AND executor_type = 'worker'
		   AND external_id IN (SELECT id FROM workers WHERE state = 'offline' AND last_seen < ?)`, cutoff)
	if err != nil {
		return nil, 0, fmt.Errorf("requeue stranded tasks: %w", err)
	}
	stranded, err := res.RowsAffected()
	if err != nil {
		return nil, 0, fmt.Errorf("requeue stranded tasks: rows affected: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE workers SET current_task = ''
		 WHERE state = 'offline' AND last_seen < ? AND current_task != ''`, cutoff); err != nil {
		return nil, 0, fmt.Errorf("clear worker current_task: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit: %w", err)
	}
	return reaped, int(stranded), nil
}

// RequeueWorkerTasks resets RUNNING tasks assigned to the given worker back to
// QUEUED so they can be picked up by another worker, and clears the worker's
// current_task, in one transaction. Returns the count of requeued tasks.
func (s *SQLStore) RequeueWorkerTasks(ctx context.Context, workerID string) (int, error) {
	s.logger.Debug("sql", "op", "requeue_worker_tasks", "worker_id", workerID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL,
		     charged_cores = NULL, charged_ram_mb = NULL, charged_gpus = NULL
		 WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`, workerID)
	if err != nil {
		return 0, fmt.Errorf("requeue tasks: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("requeue tasks: rows affected: %w", err)
	}

	// Clear the worker's current_task: it has no RUNNING task left.
	if _, err := tx.ExecContext(ctx,
		`UPDATE workers SET current_task = '' WHERE id = ?`, workerID); err != nil {
		return 0, fmt.Errorf("clear worker current_task: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return int(n), nil
}

// releaseWorkerTaskSQL moves a worker's current_task off a task: if it is
// that task, or a task the worker is no longer running (left behind when a
// terminal report was refused), it becomes the worker's most recently started
// other RUNNING worker task, or empty if none (the UI shows a worker with an
// empty current_task as idle). Args: workerID, taskID, workerID, taskID,
// workerID.
const releaseWorkerTaskSQL = `UPDATE workers SET current_task = COALESCE(
	    (SELECT id FROM tasks
	     WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ? AND id != ?
	     ORDER BY started_at DESC NULLS LAST LIMIT 1), '')
	 WHERE id = ? AND (current_task = ? OR current_task NOT IN
	    (SELECT id FROM tasks
	     WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?))`

// ReleaseWorkerTask moves the worker's current_task off taskID (see
// releaseWorkerTaskSQL) in one UPDATE that writes no other column, so it
// cannot clobber a concurrent checkout, complete or heartbeat on the same
// worker the way a full-row UpdateWorker can. An unknown worker, or a
// current_task that is another RUNNING task of the worker, is a no-op.
func (s *SQLStore) ReleaseWorkerTask(ctx context.Context, workerID, taskID string) error {
	s.logger.Debug("sql", "op", "release_worker_task", "worker_id", workerID, "task_id", taskID)

	if _, err := s.db.ExecContext(ctx, releaseWorkerTaskSQL, workerID, taskID, workerID, taskID, workerID); err != nil {
		return fmt.Errorf("release worker task: %w", err)
	}
	return nil
}

// ReconcileWorkerTasks requeues tasks the database attributes to workerID
// (state=RUNNING, executor_type=worker) that the worker no longer reports as
// running. running is the set of task IDs the worker claims to be executing,
// reported via heartbeat. Tasks whose started_at is younger than minAge are left
// alone to avoid racing the checkout→first-heartbeat window (a freshly checked-out
// task the worker has not yet had a chance to report). Returns the IDs requeued.
//
// This complements ReapStaleWorkers (which only handles
// dead workers): it catches the alive-but-forgetful case where a worker keeps
// heartbeating after a server restart but has dropped its in-flight task.
func (s *SQLStore) ReconcileWorkerTasks(ctx context.Context, workerID string, running []string, minAge time.Duration) ([]string, error) {
	s.logger.Debug("sql", "op", "reconcile_worker_tasks", "worker_id", workerID, "running", len(running))

	runningSet := make(map[string]struct{}, len(running))
	for _, id := range running {
		runningSet[id] = struct{}{}
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, started_at FROM tasks
		 WHERE state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`, workerID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: query running tasks: %w", err)
	}

	cutoff := time.Now().UTC().Add(-minAge)
	var orphans []string
	for rows.Next() {
		var id string
		var startedAt *string
		if err := rows.Scan(&id, &startedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reconcile: scan: %w", err)
		}
		// Worker still claims this task — legitimately in-flight.
		if _, ok := runningSet[id]; ok {
			continue
		}
		// Within the grace window — too fresh to judge as orphaned.
		if startedAt != nil {
			if t, terr := parseTimeOrZero(*startedAt); terr == nil && t.After(cutoff) {
				continue
			}
		}
		orphans = append(orphans, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reconcile: rows: %w", err)
	}

	for _, id := range orphans {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET state = 'QUEUED', external_id = '', started_at = NULL,
			     charged_cores = NULL, charged_ram_mb = NULL, charged_gpus = NULL
			 WHERE id = ? AND state = 'RUNNING' AND executor_type = 'worker' AND external_id = ?`,
			id, workerID); err != nil {
			return nil, fmt.Errorf("reconcile: requeue %s: %w", id, err)
		}
		// Move current_task off this orphaned task only if it pointed at it,
		// onto another of the worker's RUNNING tasks if any remain.
		if _, err := s.db.ExecContext(ctx, releaseWorkerTaskSQL,
			workerID, id, workerID, id, workerID); err != nil {
			return nil, fmt.Errorf("reconcile: clear current_task %s: %w", id, err)
		}
	}

	return orphans, nil
}

// CancelledTasksForWorker returns the subset of the given task IDs whose owning
// submission is CANCELLED — the tasks a worker is running that it should kill.
// The submission state is the durable cancellation signal: the scheduler marks a
// cancelled submission's tasks terminal (SKIPPED), but that does not stop the
// worker's process, which is what this drives. Returns nil for an empty input.
func (s *SQLStore) CancelledTasksForWorker(ctx context.Context, taskIDs []string) ([]string, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(taskIDs))
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT t.id FROM tasks t
		 JOIN submissions s ON t.submission_id = s.id
		 WHERE t.id IN (` + strings.Join(placeholders, ",") + `)
		   AND s.state = 'CANCELLED'`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cancelled tasks: query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cancelled tasks: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- User operations ---

func (s *SQLStore) GetUser(ctx context.Context, username string) (*model.User, error) {
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

func (s *SQLStore) GetOrCreateUser(ctx context.Context, username string, provider model.AuthProvider) (*model.User, error) {
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

	// Upsert: concurrent first requests for the same username race this
	// insert (SELECT above saw no row for both). ON CONFLICT lets the loser
	// land as a last_login touch instead of a UNIQUE-constraint error.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, provider, role, created_at, last_login)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET last_login = excluded.last_login`,
		user.ID, user.Username, string(user.Provider), string(user.Role),
		user.CreatedAt.Unix(), user.LastLoginAt.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	// Re-read so the caller gets the winning row (ID/role of the row that
	// actually exists, whichever request created it).
	existing, err := s.GetUser(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("read user after upsert: %w", err)
	}
	if existing == nil {
		return nil, fmt.Errorf("user %s missing after upsert", username)
	}

	s.logger.Info("user created", "id", existing.ID, "username", username, "provider", provider)
	return existing, nil
}

func (s *SQLStore) UpdateUser(ctx context.Context, user *model.User) error {
	s.logger.Debug("sql", "op", "update", "table", "users", "id", user.ID)

	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET role=?, last_login=? WHERE id=?`,
		string(user.Role), user.LastLoginAt.Unix(), user.ID,
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

func (s *SQLStore) ListUsers(ctx context.Context) ([]*model.User, error) {
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

func (s *SQLStore) LinkProvider(ctx context.Context, userID string, provider model.AuthProvider, username string) error {
	s.logger.Debug("sql", "op", "insert", "table", "linked_providers", "user_id", userID, "provider", provider)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO linked_providers (user_id, provider, username)
		 VALUES (?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET username = excluded.username`,
		userID, string(provider), username,
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

func (s *SQLStore) CreateLabelVocabulary(ctx context.Context, lv *model.LabelVocabulary) error {
	s.logger.Debug("sql", "op", "insert", "table", "label_vocabulary", "id", lv.ID)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO label_vocabulary (id, key, value, description, color, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		lv.ID, lv.Key, lv.Value, lv.Description, lv.Color,
		formatTime(lv.CreatedAt),
	)
	if s.dialect.isUniqueViolation(err) {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	return err
}

func (s *SQLStore) ListLabelVocabulary(ctx context.Context) ([]*model.LabelVocabulary, error) {
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

func (s *SQLStore) DeleteLabelVocabulary(ctx context.Context, id string) error {
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
// The primary key is always the last sort key: these lists are paged with
// LIMIT/OFFSET, and without a total order rows with equal sort values could
// be repeated or skipped across pages (PostgreSQL returns ties in any order).
func validatedOrderBy(sortBy, sortDir string, allowed map[string]string, defaultSort string) string {
	col, ok := allowed[sortBy]
	if !ok {
		return defaultSort + ", id"
	}
	dir := "DESC"
	if strings.EqualFold(sortDir, "asc") {
		dir = "ASC"
	}
	return col + " " + dir + ", id"
}
