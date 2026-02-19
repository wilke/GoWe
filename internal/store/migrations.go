package store

import (
	"context"
	"database/sql"
	"strings"
)

// schema contains the DDL for all GoWe tables.
// Each statement uses IF NOT EXISTS for idempotency.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS workflows (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		description  TEXT NOT NULL DEFAULT '',
		cwl_version  TEXT NOT NULL,
		content_hash TEXT NOT NULL DEFAULT '',
		raw_cwl      TEXT NOT NULL,
		inputs       TEXT NOT NULL,
		outputs      TEXT NOT NULL,
		steps        TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS submissions (
		id            TEXT PRIMARY KEY,
		workflow_id   TEXT NOT NULL,
		workflow_name TEXT NOT NULL,
		state         TEXT NOT NULL DEFAULT 'PENDING',
		inputs        TEXT NOT NULL,
		outputs       TEXT NOT NULL DEFAULT '{}',
		labels        TEXT NOT NULL DEFAULT '{}',
		submitted_by  TEXT NOT NULL DEFAULT '',
		created_at    TEXT NOT NULL,
		completed_at  TEXT
	)`,

	`CREATE TABLE IF NOT EXISTS tasks (
		id            TEXT PRIMARY KEY,
		submission_id TEXT NOT NULL,
		step_id       TEXT NOT NULL,
		state         TEXT NOT NULL DEFAULT 'PENDING',
		executor_type TEXT NOT NULL DEFAULT 'local',
		external_id   TEXT NOT NULL DEFAULT '',
		bvbrc_app_id  TEXT NOT NULL DEFAULT '',
		inputs        TEXT NOT NULL DEFAULT '{}',
		outputs       TEXT NOT NULL DEFAULT '{}',
		depends_on    TEXT NOT NULL DEFAULT '[]',
		retry_count   INTEGER NOT NULL DEFAULT 0,
		max_retries   INTEGER NOT NULL DEFAULT 3,
		stdout        TEXT NOT NULL DEFAULT '',
		stderr        TEXT NOT NULL DEFAULT '',
		exit_code     INTEGER,
		created_at    TEXT NOT NULL,
		started_at    TEXT,
		completed_at  TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS idx_submissions_workflow_id ON submissions(workflow_id)`,
	`CREATE INDEX IF NOT EXISTS idx_submissions_state ON submissions(state)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_submission_id ON tasks(submission_id)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`,
	// Compound index for worker task checkout query (state + executor_type)
	`CREATE INDEX IF NOT EXISTS idx_tasks_state_executor ON tasks(state, executor_type)`,

	// Sessions table for UI authentication
	`CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		username   TEXT NOT NULL,
		role       TEXT NOT NULL DEFAULT 'user',
		token      TEXT NOT NULL,
		token_exp  INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,

	// Index for listing submissions by submitter
	`CREATE INDEX IF NOT EXISTS idx_submissions_submitted_by ON submissions(submitted_by)`,

	// Workers table for remote task execution
	`CREATE TABLE IF NOT EXISTS workers (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL,
		hostname      TEXT NOT NULL DEFAULT '',
		state         TEXT NOT NULL DEFAULT 'online',
		runtime       TEXT NOT NULL DEFAULT 'none',
		labels        TEXT NOT NULL DEFAULT '{}',
		last_seen     TEXT NOT NULL,
		current_task  TEXT NOT NULL DEFAULT '',
		registered_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_workers_state ON workers(state)`,
}

// alterStatements are column additions that need special handling since
// SQLite doesn't support IF NOT EXISTS for ALTER TABLE ADD COLUMN.
var alterStatements = []struct {
	table    string
	column   string
	alterSQL string
	indexSQL string // Optional index to create after column is added
}{
	{
		table:    "workflows",
		column:   "content_hash",
		alterSQL: "ALTER TABLE workflows ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''",
		indexSQL: "CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_content_hash ON workflows(content_hash) WHERE content_hash != ''",
	},
	{
		table:    "workflows",
		column:   "created_by",
		alterSQL: "ALTER TABLE workflows ADD COLUMN created_by TEXT NOT NULL DEFAULT ''",
		indexSQL: "CREATE INDEX IF NOT EXISTS idx_workflows_created_by ON workflows(created_by)",
	},
	{
		table:    "workflows",
		column:   "class",
		alterSQL: "ALTER TABLE workflows ADD COLUMN class TEXT NOT NULL DEFAULT 'Workflow'",
		indexSQL: "CREATE INDEX IF NOT EXISTS idx_workflows_class ON workflows(class)",
	},
	// Tool, Job, and RuntimeHints for CWL worker execution
	{
		table:    "tasks",
		column:   "tool",
		alterSQL: "ALTER TABLE tasks ADD COLUMN tool TEXT NOT NULL DEFAULT '{}'",
	},
	{
		table:    "tasks",
		column:   "job",
		alterSQL: "ALTER TABLE tasks ADD COLUMN job TEXT NOT NULL DEFAULT '{}'",
	},
	{
		table:    "tasks",
		column:   "runtime_hints",
		alterSQL: "ALTER TABLE tasks ADD COLUMN runtime_hints TEXT NOT NULL DEFAULT '{}'",
	},
}

// migrate executes all schema DDL statements, alter migrations, and post-migration indexes.
func migrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range schema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	// Execute ALTER TABLE statements idempotently.
	for _, alter := range alterStatements {
		if err := addColumnIfNotExists(ctx, db, alter.table, alter.column, alter.alterSQL); err != nil {
			return err
		}
		if alter.indexSQL != "" {
			if _, err := db.ExecContext(ctx, alter.indexSQL); err != nil {
				return err
			}
		}
	}

	return nil
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist.
func addColumnIfNotExists(ctx context.Context, db *sql.DB, table, column, alterSQL string) error {
	// Query table info to check if column exists.
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return nil // Column already exists
		}
	}

	// Column doesn't exist, add it.
	_, err = db.ExecContext(ctx, alterSQL)
	return err
}
