package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

var (
	// ErrBlessingActive is returned by CreateToolBlessing when an active
	// blessing for the same tool hash already exists.
	ErrBlessingActive = errors.New("an active blessing for this tool hash already exists")
	// ErrBlessingNotFound is returned when a blessing id does not exist.
	ErrBlessingNotFound = errors.New("tool blessing not found")
	// ErrBlessingRevoked is returned when mutating an already revoked blessing.
	ErrBlessingRevoked = errors.New("tool blessing is revoked")
)

const toolBlessingColumns = `id, tool_hash, tool_name, env_names, worker_groups, image_ref, image_sha256s,
	blessed_by, blessed_at, note, summary, revoked_at, revoked_by, revoke_reason`

func scanToolBlessing(row interface {
	Scan(dest ...any) error
}) (*model.ToolBlessing, error) {
	var b model.ToolBlessing
	var envJSON, groupsJSON, shaJSON, blessedAt, summaryJSON string
	var revokedAt sql.NullString
	if err := row.Scan(&b.ID, &b.ToolHash, &b.ToolName, &envJSON, &groupsJSON, &b.ImageRef, &shaJSON,
		&b.BlessedBy, &blessedAt, &b.Note, &summaryJSON, &revokedAt, &b.RevokedBy, &b.RevokeReason); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(envJSON, &b.EnvNames, "env_names"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(groupsJSON, &b.WorkerGroups, "worker_groups"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(shaJSON, &b.ImageSHA256s, "image_sha256s"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(summaryJSON, &b.Summary, "summary"); err != nil {
		return nil, err
	}
	var err error
	if b.BlessedAt, err = parseTimeOrZero(blessedAt); err != nil {
		return nil, err
	}
	if b.RevokedAt, err = scanNullableTime(revokedAt); err != nil {
		return nil, err
	}
	// Never hand out nil slices: JSON clients expect [].
	if b.EnvNames == nil {
		b.EnvNames = []string{}
	}
	if b.WorkerGroups == nil {
		b.WorkerGroups = []string{}
	}
	if b.ImageSHA256s == nil {
		b.ImageSHA256s = []string{}
	}
	return &b, nil
}

func jsonList(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CreateToolBlessing inserts a new blessing. It returns ErrBlessingActive when
// an active blessing for the same tool hash exists. At least one pinned image
// digest is required.
func (s *SQLiteStore) CreateToolBlessing(ctx context.Context, b *model.ToolBlessing) error {
	s.logger.Debug("sql", "op", "insert", "table", "blessed_tools", "id", b.ID)
	if b.ID == "" || b.ToolHash == "" {
		return fmt.Errorf("tool blessing needs id and tool_hash")
	}
	if len(b.ImageSHA256s) == 0 {
		return fmt.Errorf("tool blessing needs at least one pinned image sha256")
	}
	envJSON, err := jsonList(b.EnvNames)
	if err != nil {
		return fmt.Errorf("marshal env_names: %w", err)
	}
	groupsJSON, err := jsonList(b.WorkerGroups)
	if err != nil {
		return fmt.Errorf("marshal worker_groups: %w", err)
	}
	shaJSON, err := jsonList(b.ImageSHA256s)
	if err != nil {
		return fmt.Errorf("marshal image_sha256s: %w", err)
	}
	summaryJSON, err := json.Marshal(b.Summary)
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO blessed_tools (`+toolBlessingColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ToolHash, b.ToolName, envJSON, groupsJSON, b.ImageRef, shaJSON,
		b.BlessedBy, b.BlessedAt.UTC().Format(time.RFC3339Nano), b.Note, string(summaryJSON),
		nullableTime(b.RevokedAt), b.RevokedBy, b.RevokeReason)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") && strings.Contains(err.Error(), "tool_hash") {
			return ErrBlessingActive
		}
		return err
	}
	return nil
}

// GetToolBlessing returns the blessing with the given id, or nil if absent.
func (s *SQLiteStore) GetToolBlessing(ctx context.Context, id string) (*model.ToolBlessing, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+toolBlessingColumns+` FROM blessed_tools WHERE id = ?`, id)
	b, err := scanToolBlessing(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// GetActiveToolBlessingByHash returns the active (non-revoked) blessing for a
// tool hash, or nil if there is none.
func (s *SQLiteStore) GetActiveToolBlessingByHash(ctx context.Context, toolHash string) (*model.ToolBlessing, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+toolBlessingColumns+` FROM blessed_tools WHERE tool_hash = ? AND revoked_at IS NULL`, toolHash)
	b, err := scanToolBlessing(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// ListToolBlessings lists blessings newest first. Revoked rows are included
// only when includeRevoked is set.
func (s *SQLiteStore) ListToolBlessings(ctx context.Context, includeRevoked bool) ([]*model.ToolBlessing, error) {
	q := `SELECT ` + toolBlessingColumns + ` FROM blessed_tools`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	q += ` ORDER BY blessed_at DESC, id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.ToolBlessing{}
	for rows.Next() {
		b, err := scanToolBlessing(rows)
		if err != nil {
			s.logger.Error("skipping corrupt blessed_tools row", "error", err)
			continue
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// RevokeToolBlessing marks a blessing revoked. The row is kept. It returns
// ErrBlessingNotFound or ErrBlessingRevoked as appropriate.
func (s *SQLiteStore) RevokeToolBlessing(ctx context.Context, id, revokedBy, reason string) error {
	s.logger.Debug("sql", "op", "update", "table", "blessed_tools", "id", id, "action", "revoke")
	res, err := s.db.ExecContext(ctx,
		`UPDATE blessed_tools SET revoked_at = ?, revoked_by = ?, revoke_reason = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), revokedBy, reason, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		b, err := s.GetToolBlessing(ctx, id)
		if err != nil {
			return err
		}
		if b == nil {
			return ErrBlessingNotFound
		}
		return ErrBlessingRevoked
	}
	return nil
}

// AddToolBlessingImageSHA256 appends an allowed image digest to an active
// blessing (idempotent: a digest already present is a no-op).
func (s *SQLiteStore) AddToolBlessingImageSHA256(ctx context.Context, id, sha string) error {
	s.logger.Debug("sql", "op", "update", "table", "blessed_tools", "id", id, "action", "add-image")
	sha = model.NormalizeSHA256(sha)
	if !model.IsValidSHA256(sha) {
		return fmt.Errorf("invalid image sha256 %q", sha)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var shaJSON string
	var revokedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT image_sha256s, revoked_at FROM blessed_tools WHERE id = ?`, id).Scan(&shaJSON, &revokedAt)
	if err == sql.ErrNoRows {
		return ErrBlessingNotFound
	}
	if err != nil {
		return err
	}
	if revokedAt.Valid {
		return ErrBlessingRevoked
	}
	var list []string
	if err := unmarshalJSON(shaJSON, &list, "image_sha256s"); err != nil {
		return err
	}
	for _, d := range list {
		if model.NormalizeSHA256(d) == sha {
			return tx.Commit()
		}
	}
	list = append(list, sha)
	out, err := jsonList(list)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blessed_tools SET image_sha256s = ? WHERE id = ?`, out, id); err != nil {
		return err
	}
	return tx.Commit()
}
