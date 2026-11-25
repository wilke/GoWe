package store

import (
	"errors"

	sqlite "modernc.org/sqlite"
)

// ErrDuplicate is returned (wrapped) when an insert violates a unique
// constraint. Callers test it with errors.Is instead of matching the text of
// a database-specific error.
var ErrDuplicate = errors.New("duplicate entry")

// SQLite extended result codes (https://www.sqlite.org/rescode.html).
const (
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
)

// isUniqueViolation reports whether err is a unique-constraint violation,
// on a primary key or on a unique index.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqliteConstraintUnique || se.Code() == sqliteConstraintPrimaryKey
}

// isPrimaryKeyViolation reports whether err is a duplicate primary key.
func isPrimaryKeyViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqliteConstraintPrimaryKey
}
