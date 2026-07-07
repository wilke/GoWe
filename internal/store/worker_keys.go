package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/me/gowe/pkg/model"
)

// nullableTime formats t for a nullable TEXT column: NULL when t is nil.
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

// scanNullableTime parses a nullable RFC3339Nano timestamp column.
func scanNullableTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return nil, fmt.Errorf("corrupt timestamp %q: %w", ns.String, err)
	}
	return &t, nil
}

// CreateWorkerKey inserts a new worker key. Only k.KeyHash (not the raw secret)
// is persisted.
func (s *SQLiteStore) CreateWorkerKey(ctx context.Context, k *model.WorkerKey) error {
	s.logger.Debug("sql", "op", "insert", "table", "worker_keys", "id", k.ID)

	groupsJSON, err := json.Marshal(k.Groups)
	if err != nil {
		return fmt.Errorf("marshal groups: %w", err)
	}

	disabled := 0
	if k.Disabled {
		disabled = 1
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO worker_keys (id, label, key_hash, key_prefix, groups, description, disabled, created_by, created_at, expires_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Label, k.KeyHash, k.KeyPrefix, string(groupsJSON), k.Description,
		disabled, k.CreatedBy, k.CreatedAt.Format(time.RFC3339Nano),
		nullableTime(k.ExpiresAt), nullableTime(k.LastUsedAt),
	)
	return err
}

// scanWorkerKey scans a single worker_keys row into a model.WorkerKey.
func scanWorkerKey(row interface {
	Scan(dest ...any) error
}) (*model.WorkerKey, error) {
	var k model.WorkerKey
	var groupsJSON, createdAt string
	var disabled int
	var expiresAt, lastUsedAt sql.NullString

	if err := row.Scan(&k.ID, &k.Label, &k.KeyHash, &k.KeyPrefix, &groupsJSON,
		&k.Description, &disabled, &k.CreatedBy, &createdAt, &expiresAt, &lastUsedAt); err != nil {
		return nil, err
	}

	k.Disabled = disabled != 0
	if err := unmarshalJSON(groupsJSON, &k.Groups, "groups"); err != nil {
		return nil, err
	}
	var err error
	if k.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if k.ExpiresAt, err = scanNullableTime(expiresAt); err != nil {
		return nil, err
	}
	if k.LastUsedAt, err = scanNullableTime(lastUsedAt); err != nil {
		return nil, err
	}
	return &k, nil
}

const workerKeyColumns = `id, label, key_hash, key_prefix, groups, description, disabled, created_by, created_at, expires_at, last_used_at`

// GetWorkerKeyByID returns the worker key with the given id, or nil if not found.
func (s *SQLiteStore) GetWorkerKeyByID(ctx context.Context, id string) (*model.WorkerKey, error) {
	s.logger.Debug("sql", "op", "select", "table", "worker_keys", "id", id)

	row := s.db.QueryRowContext(ctx,
		`SELECT `+workerKeyColumns+` FROM worker_keys WHERE id = ?`, id)
	k, err := scanWorkerKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// GetWorkerKeyByHash returns the worker key with the given SHA-256 hash, or nil
// if not found. This is the lookup used during worker authentication.
func (s *SQLiteStore) GetWorkerKeyByHash(ctx context.Context, hash string) (*model.WorkerKey, error) {
	s.logger.Debug("sql", "op", "select", "table", "worker_keys", "by", "hash")

	row := s.db.QueryRowContext(ctx,
		`SELECT `+workerKeyColumns+` FROM worker_keys WHERE key_hash = ?`, hash)
	k, err := scanWorkerKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// ListWorkerKeys returns all worker keys ordered by creation time (newest first).
func (s *SQLiteStore) ListWorkerKeys(ctx context.Context) ([]*model.WorkerKey, error) {
	s.logger.Debug("sql", "op", "list", "table", "worker_keys")

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+workerKeyColumns+` FROM worker_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*model.WorkerKey
	for rows.Next() {
		k, err := scanWorkerKey(rows)
		if err != nil {
			s.logger.Error("skipping corrupt worker_key row", "error", err)
			continue
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// UpdateWorkerKey updates the mutable fields of a worker key (label, groups,
// description, disabled, expiry). The key hash is immutable.
func (s *SQLiteStore) UpdateWorkerKey(ctx context.Context, k *model.WorkerKey) error {
	s.logger.Debug("sql", "op", "update", "table", "worker_keys", "id", k.ID)

	groupsJSON, err := json.Marshal(k.Groups)
	if err != nil {
		return fmt.Errorf("marshal groups: %w", err)
	}

	disabled := 0
	if k.Disabled {
		disabled = 1
	}

	// Note: last_used_at is deliberately NOT written here. It is owned solely by
	// TouchWorkerKey (updated on each successful auth); writing it from a PATCH's
	// round-tripped struct could clobber a concurrent Touch with a stale value.
	result, err := s.db.ExecContext(ctx,
		`UPDATE worker_keys SET label=?, groups=?, description=?, disabled=?, expires_at=? WHERE id=?`,
		k.Label, string(groupsJSON), k.Description, disabled,
		nullableTime(k.ExpiresAt), k.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker key %s not found", k.ID)
	}
	return nil
}

// DeleteWorkerKey permanently removes (revokes) a worker key.
func (s *SQLiteStore) DeleteWorkerKey(ctx context.Context, id string) error {
	s.logger.Debug("sql", "op", "delete", "table", "worker_keys", "id", id)

	result, err := s.db.ExecContext(ctx, `DELETE FROM worker_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("worker key %s not found", id)
	}
	return nil
}

// CountWorkerKeys returns the number of configured worker keys. Used to decide
// whether DB-backed worker-key enforcement is active.
func (s *SQLiteStore) CountWorkerKeys(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_keys`).Scan(&n)
	return n, err
}

// TouchWorkerKey records that a key was used at the given time. Best-effort: a
// missing key is not treated as an error.
func (s *SQLiteStore) TouchWorkerKey(ctx context.Context, id string, when time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE worker_keys SET last_used_at=? WHERE id=?`,
		when.Format(time.RFC3339Nano), id)
	return err
}
