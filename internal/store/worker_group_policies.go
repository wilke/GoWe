package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
)

var (
	// ErrWorkerGroupPolicyNotFound is returned by AddWorkerGroupUser and
	// RemoveWorkerGroupUser when the group has no rule.
	ErrWorkerGroupPolicyNotFound = errors.New("worker group policy not found")
	// ErrWorkerGroupUsersFull is returned (wrapped) by AddWorkerGroupUser
	// when the users list already holds groupacl.MaxUsers entries.
	ErrWorkerGroupUsersFull = errors.New("worker group users list is full")
)

const workerGroupPolicyColumns = `group_name, users, managers, allow_admins, updated_by, created_at, updated_at`

// scanWorkerGroupPolicy scans a single worker_group_policies row.
func scanWorkerGroupPolicy(row interface {
	Scan(dest ...any) error
}) (*model.WorkerGroupPolicy, error) {
	var p model.WorkerGroupPolicy
	var usersJSON, managersJSON, createdAt, updatedAt string
	var admins int
	if err := row.Scan(&p.Group, &usersJSON, &managersJSON, &admins, &p.UpdatedBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.Admins = admins != 0
	if err := unmarshalJSON(usersJSON, &p.Users, "users"); err != nil {
		return nil, err
	}
	if p.Users == nil {
		p.Users = []string{}
	}
	if err := unmarshalJSON(managersJSON, &p.Managers, "managers"); err != nil {
		return nil, err
	}
	if p.Managers == nil {
		p.Managers = []string{}
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
// kept. Managers are replaced too when p.Managers is not nil; nil keeps the
// stored managers of an existing rule (a new rule then has none). A zero
// UpdatedAt is now, a zero CreatedAt is UpdatedAt. The caller validates the
// group name, users and managers (internal/groupacl).
func (s *SQLStore) UpsertWorkerGroupPolicy(ctx context.Context, p *model.WorkerGroupPolicy) error {
	s.logger.Debug("sql", "op", "upsert", "table", "worker_group_policies", "group", p.Group)
	if p.Group == "" {
		return fmt.Errorf("worker group policy needs a group name")
	}
	usersJSON, err := jsonList(p.Users)
	if err != nil {
		return fmt.Errorf("marshal users: %w", err)
	}
	managersJSON, err := jsonList(p.Managers)
	if err != nil {
		return fmt.Errorf("marshal managers: %w", err)
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
	setManagers := ", managers = excluded.managers"
	if p.Managers == nil {
		setManagers = ""
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO worker_group_policies (`+workerGroupPolicyColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(group_name) DO UPDATE SET users = excluded.users`+setManagers+`, allow_admins = excluded.allow_admins,
		   updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		p.Group, usersJSON, managersJSON, admins, p.UpdatedBy, formatTime(created), formatTime(updated))
	return err
}

// AddWorkerGroupUser adds username to the users of group's rule, in one
// transaction, and reports whether the list changed (false: already listed).
// It never creates a rule: ErrWorkerGroupPolicyNotFound when group has none.
// A list already holding groupacl.MaxUsers entries is an error
// (ErrWorkerGroupUsersFull). Only users,
// updated_by and updated_at are written. The caller validates username.
func (s *SQLStore) AddWorkerGroupUser(ctx context.Context, group, username, by string) (bool, error) {
	return s.changeWorkerGroupUsers(ctx, group, by, func(users []string) ([]string, bool, error) {
		if slices.Contains(users, username) {
			return users, false, nil
		}
		if len(users) >= groupacl.MaxUsers {
			return nil, false, fmt.Errorf("%w: group %q has %d users; at most %d are allowed", ErrWorkerGroupUsersFull, group, len(users), groupacl.MaxUsers)
		}
		return append(users, username), true, nil
	})
}

// RemoveWorkerGroupUser removes username from the users of group's rule, in
// one transaction, and reports whether the list changed (false: not listed).
// It never creates a rule: ErrWorkerGroupPolicyNotFound when group has none.
// Only users, updated_by and updated_at are written.
func (s *SQLStore) RemoveWorkerGroupUser(ctx context.Context, group, username, by string) (bool, error) {
	return s.changeWorkerGroupUsers(ctx, group, by, func(users []string) ([]string, bool, error) {
		i := slices.Index(users, username)
		if i < 0 {
			return users, false, nil
		}
		return slices.Delete(users, i, i+1), true, nil
	})
}

// changeWorkerGroupUsers reads the users of group's rule, applies change and,
// when it reports a change, writes users, updated_by and updated_at, all in
// one transaction.
func (s *SQLStore) changeWorkerGroupUsers(ctx context.Context, group, by string,
	change func(users []string) ([]string, bool, error)) (bool, error) {
	s.logger.Debug("sql", "op", "update", "table", "worker_group_policies", "group", group, "col", "users")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var usersJSON string
	err = tx.QueryRowContext(ctx, `SELECT users FROM worker_group_policies WHERE group_name = ?`, group).Scan(&usersJSON)
	if err == sql.ErrNoRows {
		return false, ErrWorkerGroupPolicyNotFound
	}
	if err != nil {
		return false, err
	}
	var users []string
	if err := unmarshalJSON(usersJSON, &users, "users"); err != nil {
		return false, err
	}
	users, changed, err := change(users)
	if err != nil || !changed {
		return false, err
	}
	newJSON, err := jsonList(users)
	if err != nil {
		return false, fmt.Errorf("marshal users: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE worker_group_policies SET users = ?, updated_by = ?, updated_at = ? WHERE group_name = ?`,
		newJSON, by, formatTime(time.Now().UTC()), group); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
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

	_, err := s.db.ExecContext(ctx, upsertServerSettingSQL, key, value, updatedBy, formatTime(time.Now().UTC()))
	return err
}

// upsertServerSettingSQL creates or replaces one server_settings row; the
// arguments are key, value, updated_by, updated_at.
const upsertServerSettingSQL = `INSERT INTO server_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
	 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`
