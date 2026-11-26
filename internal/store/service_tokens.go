package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/me/gowe/pkg/model"
)

// ErrServiceTokenLimit is returned (wrapped) by CreateServiceToken when the
// name already has model.MaxServiceTokensPerName tokens or the server
// model.MaxServiceTokens.
var ErrServiceTokenLimit = errors.New("service token limit reached")

// CreateServiceToken inserts a new service token after checking the limits,
// in one transaction. Only t.TokenHash (never the raw token) is persisted.
func (s *SQLStore) CreateServiceToken(ctx context.Context, t *model.ServiceToken) error {
	s.logger.Debug("sql", "op", "insert", "table", "service_tokens", "id", t.ID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var perName, total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_tokens WHERE name = ?`, t.Name).Scan(&perName); err != nil {
		return fmt.Errorf("count service tokens of %q: %w", t.Name, err)
	}
	if perName >= model.MaxServiceTokensPerName {
		return fmt.Errorf("%w: service %q has %d tokens; at most %d are allowed per name",
			ErrServiceTokenLimit, t.Name, perName, model.MaxServiceTokensPerName)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_tokens`).Scan(&total); err != nil {
		return fmt.Errorf("count service tokens: %w", err)
	}
	if total >= model.MaxServiceTokens {
		return fmt.Errorf("%w: the server has %d service tokens; at most %d are allowed",
			ErrServiceTokenLimit, total, model.MaxServiceTokens)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_tokens (id, name, token_hash, token_prefix, description, created_by, created_at, expires_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenHash, t.TokenPrefix, t.Description, t.CreatedBy,
		formatTime(t.CreatedAt), nullableTime(t.ExpiresAt), nullableTime(t.LastUsedAt),
	); err != nil {
		if s.dialect.isUniqueViolation(err) {
			// The driver's message is not kept: it may name the hash.
			return fmt.Errorf("%w: a service token with this id or hash exists", ErrDuplicate)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// serviceTokenListColumns are the columns ListServiceTokens reads: every
// column but token_hash.
const serviceTokenListColumns = `id, name, token_prefix, description, created_by, created_at, expires_at, last_used_at`

// scanServiceToken scans a row of serviceTokenListColumns, preceded by
// token_hash when withHash is set.
func scanServiceToken(row interface {
	Scan(dest ...any) error
}, withHash bool) (*model.ServiceToken, error) {
	var t model.ServiceToken
	var createdAt string
	var expiresAt, lastUsedAt sql.NullString
	dest := []any{&t.ID, &t.Name, &t.TokenPrefix, &t.Description, &t.CreatedBy, &createdAt, &expiresAt, &lastUsedAt}
	if withHash {
		dest = append([]any{&t.TokenHash}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	var err error
	if t.CreatedAt, err = parseTimeOrZero(createdAt); err != nil {
		return nil, err
	}
	t.ExpiresAt, t.ExpiryInvalid = scanServiceTokenExpiry(expiresAt)
	if t.LastUsedAt, err = scanNullableTime(lastUsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// scanServiceTokenExpiry reads service_tokens.expires_at. Only NULL means no
// expiry. An empty or unparsable value is not an error (the row stays listed
// and revocable) but marks the token invalid: it counts as expired, and
// ExpiresAt points to the zero time so no reader can take it for "never".
// (scanNullableTime, which the other tables use, reads "" as NULL.)
func scanServiceTokenExpiry(ns sql.NullString) (expiresAt *time.Time, invalid bool) {
	if !ns.Valid {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil || ns.String == "" {
		zero := time.Time{}
		return &zero, true
	}
	return &t, false
}

// ListServiceTokens returns every service token, newest first. The hash is
// not read: TokenHash is always empty.
func (s *SQLStore) ListServiceTokens(ctx context.Context) ([]*model.ServiceToken, error) {
	s.logger.Debug("sql", "op", "list", "table", "service_tokens")

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+serviceTokenListColumns+` FROM service_tokens ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := []*model.ServiceToken{}
	for rows.Next() {
		t, err := scanServiceToken(rows, false)
		if err != nil {
			s.logger.Error("skipping corrupt service_tokens row", "error", err)
			continue
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// GetServiceTokenByHash returns the service token whose token_hash is hash,
// or nil if there is none. This is the lookup of the authentication path.
func (s *SQLStore) GetServiceTokenByHash(ctx context.Context, hash string) (*model.ServiceToken, error) {
	s.logger.Debug("sql", "op", "select", "table", "service_tokens", "by", "hash")

	row := s.db.QueryRowContext(ctx,
		`SELECT token_hash, `+serviceTokenListColumns+` FROM service_tokens WHERE token_hash = ?`, hash)
	t, err := scanServiceToken(row, true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteServiceToken removes (revokes) the service token id in one
// transaction and returns its name (for the audit log); found is false, with
// no error, when there is no such token.
func (s *SQLStore) DeleteServiceToken(ctx context.Context, id string) (name string, found bool, err error) {
	s.logger.Debug("sql", "op", "delete", "table", "service_tokens", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `SELECT name FROM service_tokens WHERE id = ?`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM service_tokens WHERE id = ?`, id)
	if err != nil {
		return "", false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	return name, n > 0, nil
}

// TouchServiceToken records that a token was used at the given time. A
// missing token is not an error.
func (s *SQLStore) TouchServiceToken(ctx context.Context, id string, when time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE service_tokens SET last_used_at = ? WHERE id = ?`,
		formatTime(when), id)
	return err
}
