package store

import "errors"

// ErrDuplicate is returned (wrapped) when an insert violates a unique
// constraint. Callers test it with errors.Is instead of matching the text of
// a database-specific error. The database-specific error codes are checked by
// the store's dialect (dialect.isUniqueViolation).
var ErrDuplicate = errors.New("duplicate entry")
