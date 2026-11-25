package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
)

// dialect is everything SQLStore needs to know about the SQL flavour of the
// database behind it. The store writes portable SQL with ? placeholders and
// asks the dialect for the parts that differ between databases: placeholder
// syntax, schema column types, catalog lookups, JSON access and constraint
// error codes.
//
// The JSON helpers return SQL fragments for a TEXT column holding a JSON
// object. col is a column name and path a dot-separated key path such as
// "worker_group" or "stager_overrides.http_credential". Both are pasted into
// the SQL text, so they must be literals from the code, never user input.
type dialect interface {
	// name identifies the dialect ("sqlite", "postgres").
	name() string

	// rebind turns a query written with ? placeholders into the
	// database's placeholder syntax.
	rebind(query string) string
	// bindArgs adapts query arguments to what the database accepts for the
	// store's column types. It returns args itself when nothing changes and
	// never modifies args in place.
	bindArgs(args []any) []any

	// jsonDoc prepares a JSON document for a column that SQL parses with the
	// json helpers above (tasks.runtime_hints, workflows.labels). SQLite
	// stores it as is. PostgreSQL cannot read a document containing the
	// escape for a NUL character, so that escape is replaced there.
	jsonDoc(doc string) string
	// ddl adapts a schema statement (CREATE TABLE / ALTER TABLE / CREATE
	// INDEX ...) written for SQLite to the database's column types.
	ddl(stmt string) string

	// columnExists reports whether table has a column named column
	// (case-insensitive).
	columnExists(ctx context.Context, q queryer, table, column string) (bool, error)
	// indexExists reports whether an index named name exists.
	indexExists(ctx context.Context, q queryer, name string) (bool, error)

	// jsonValid is a condition that is true when col holds valid JSON.
	jsonValid(col string) string
	// jsonText is the value at path as text (NULL when absent).
	jsonText(col, path string) string
	// jsonNumber is the value at path as a number (NULL when absent).
	jsonNumber(col, path string) string
	// jsonIsTrue is a condition that is true when the value at path is the
	// JSON boolean true (NULL when absent).
	jsonIsTrue(col, path string) string
	// jsonHasKey is a condition that is true when path is present in col,
	// whatever its value (JSON null included).
	jsonHasKey(col, path string) string
	// jsonRemove is col with the given paths removed.
	jsonRemove(col string, paths ...string) string
	// jsonTextParam is the value as text at the top-level key bound to one
	// ? parameter.
	jsonTextParam(col string) string
	// jsonAnyValueEquals is a condition, binding one ? parameter, that is
	// true when some top-level value of col equals that parameter.
	jsonAnyValueEquals(col string) string

	// isUniqueViolation reports whether err is a unique-constraint
	// violation, on a primary key or on a unique index.
	isUniqueViolation(err error) bool
	// isPrimaryKeyViolation reports whether err is a duplicate primary key.
	isPrimaryKeyViolation(err error) bool
}

// queryer is what the dialect's catalog lookups need: a *sqlDB or a *sqlTx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// sqliteDialect is the dialect of SQLite (modernc.org/sqlite). The schema and
// the store's SQL are written for SQLite, so rebind and ddl are the identity.
type sqliteDialect struct{}

var _ dialect = sqliteDialect{}

// SQLite extended result codes (https://www.sqlite.org/rescode.html).
const (
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
)

func (sqliteDialect) name() string { return "sqlite" }

// rebind returns query unchanged: SQLite takes ? placeholders.
func (sqliteDialect) rebind(query string) string { return query }

// bindArgs returns args unchanged: SQLite stores every value as given.
func (sqliteDialect) bindArgs(args []any) []any { return args }

func (sqliteDialect) jsonDoc(doc string) string { return doc }

// ddl returns stmt unchanged: the schema is written for SQLite.
func (sqliteDialect) ddl(stmt string) string { return stmt }

// columnExists reads PRAGMA table_info(table). Its errors are returned as
// they are, so migrate's errors read as before.
func (sqliteDialect) columnExists(ctx context.Context, q queryer, table, column string) (bool, error) {
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// indexExists looks the index up in sqlite_master.
func (sqliteDialect) indexExists(ctx context.Context, q queryer, name string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("look up index %s: %w", name, err)
	}
	return n > 0, nil
}

// sqliteJSONPath renders a dot-separated key path as a quoted SQLite JSON
// path literal: "a.b" -> '$.a.b'.
func sqliteJSONPath(path string) string { return `'$.` + path + `'` }

func (sqliteDialect) jsonValid(col string) string { return `json_valid(` + col + `)` }

func (sqliteDialect) jsonText(col, path string) string {
	return `json_extract(` + col + `,` + sqliteJSONPath(path) + `)`
}

// jsonNumber is the same as jsonText: json_extract returns a JSON number as
// an SQL number.
func (d sqliteDialect) jsonNumber(col, path string) string { return d.jsonText(col, path) }

// jsonIsTrue compares with 1: json_extract returns JSON true as integer 1.
func (d sqliteDialect) jsonIsTrue(col, path string) string { return d.jsonText(col, path) + `=1` }

func (sqliteDialect) jsonHasKey(col, path string) string {
	return `json_type(` + col + `, ` + sqliteJSONPath(path) + `) IS NOT NULL`
}

func (sqliteDialect) jsonRemove(col string, paths ...string) string {
	args := make([]string, 0, 1+len(paths))
	args = append(args, col)
	for _, p := range paths {
		args = append(args, sqliteJSONPath(p))
	}
	return `json_remove(` + strings.Join(args, `, `) + `)`
}

func (sqliteDialect) jsonTextParam(col string) string { return `json_extract(` + col + `, '$.'||?)` }

func (sqliteDialect) jsonAnyValueEquals(col string) string {
	return `EXISTS (SELECT 1 FROM json_each(` + col + `) WHERE json_each.value = ?)`
}

func (sqliteDialect) isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqliteConstraintUnique || se.Code() == sqliteConstraintPrimaryKey
}

func (sqliteDialect) isPrimaryKeyViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqliteConstraintPrimaryKey
}
