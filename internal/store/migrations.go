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

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_content_hash ON workflows(content_hash) WHERE content_hash != ''`,

	`CREATE INDEX IF NOT EXISTS idx_submissions_workflow_id ON submissions(workflow_id)`,
	`CREATE INDEX IF NOT EXISTS idx_submissions_state ON submissions(state)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_submission_id ON tasks(submission_id)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`,
}

// alterMigrations are ALTER TABLE statements for upgrading existing databases.
// Errors containing "duplicate column" are ignored (column already exists).
var alterMigrations = []string{
	`ALTER TABLE workflows ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''`,
}

// migrate executes all schema DDL statements and alter migrations.
func migrate(ctx context.Context, db *sql.DB) error {
	for _, stmt := range schema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, stmt := range alterMigrations {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return err
		}
	}
	return nil
}
