package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/me/gowe/pkg/model"
)

const workerGroupPolicyColumns = `group_name, users, allow_admins, updated_by, created_at, updated_at`

// scanWorkerGroupPolicy scans a single worker_group_policies row.
func scanWorkerGroupPolicy(row interface {
	Scan(dest ...any) error
}) (*model.WorkerGroupPolicy, error) {
	var p model.WorkerGroupPolicy
	var usersJSON, createdAt, updatedAt string
	var admins int
	if err := row.Scan(&p.Group, &usersJSON, &admins, &p.UpdatedBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.Admins = admins != 0
	if err := unmarshalJSON(usersJSON, &p.Users, "users"); err != nil {
		return nil, err
	}
	if p.Users == nil {
		p.Users = []string{}
	}
	var err error
	if p.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = parseTimeOrZero(updatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListWorkerGroupPolicies returns every worker-group rule, sorted by group
// name (byte order, the same on every backend). Unlike the other list
// methods it does not skip a row it cannot read: a dropped rule would open
// its group, so the error is returned.
func (s *SQLStore) ListWorkerGroupPolicies(ctx context.Context) ([]*model.WorkerGroupPolicy, error) {
	s.logger.Debug("sql", "op", "list", "table", "worker_group_policies")

	rows, err := s.db.QueryContext(ctx, `SELECT `+workerGroupPolicyColumns+` FROM worker_group_policies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*model.WorkerGroupPolicy{}
	for rows.Next() {
		p, err := scanWorkerGroupPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("read worker_group_policies row: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out, nil
}

// UpsertWorkerGroupPolicy creates the rule of p.Group or replaces its users,
// admins flag, updated_by and updated_at; created_at of an existing rule is
// kept. A zero UpdatedAt is now, a zero CreatedAt is UpdatedAt. The caller
// validates the group name and users (internal/groupacl).
func (s *SQLStore) UpsertWorkerGroupPolicy(ctx context.Context, p *model.WorkerGroupPolicy) error {
	s.logger.Debug("sql", "op", "upsert", "table", "worker_group_policies", "group", p.Group)
	if p.Group == "" {
		return fmt.Errorf("worker group policy needs a group name")
	}
	usersJSON, err := jsonList(p.Users)
	if err != nil {
		return fmt.Errorf("marshal users: %w", err)
	}
	admins := 0
	if p.Admins {
		admins = 1
	}
	updated := p.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = updated
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO worker_group_policies (`+workerGroupPolicyColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(group_name) DO UPDATE SET users = excluded.users, allow_admins = excluded.allow_admins,
		   updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		p.Group, usersJSON, admins, p.UpdatedBy, formatTime(created), formatTime(updated))
	return err
}

// DeleteWorkerGroupPolicy removes the rule of group and reports whether one
// existed.
func (s *SQLStore) DeleteWorkerGroupPolicy(ctx context.Context, group string) (bool, error) {
	s.logger.Debug("sql", "op", "delete", "table", "worker_group_policies", "group", group)

	res, err := s.db.ExecContext(ctx, `DELETE FROM worker_group_policies WHERE group_name = ?`, group)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetServerSetting returns the value stored under key; found is false (no
// error) when the key has no row.
func (s *SQLStore) GetServerSetting(ctx context.Context, key string) (string, bool, error) {
	s.logger.Debug("sql", "op", "select", "table", "server_settings", "key", key)

	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM server_settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetServerSetting creates or replaces the value of key, stamping
// updated_by and updated_at (now).
func (s *SQLStore) SetServerSetting(ctx context.Context, key, value, updatedBy string) error {
	s.logger.Debug("sql", "op", "upsert", "table", "server_settings", "key", key)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		key, value, updatedBy, formatTime(time.Now().UTC()))
	return err
}
