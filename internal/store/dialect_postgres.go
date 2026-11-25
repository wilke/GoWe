package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// postgresDialect is the dialect of PostgreSQL (16 or newer) through
// github.com/jackc/pgx/v5/stdlib. The database keeps the SQLite schema's
// logical types: JSON documents and timestamps are TEXT, booleans are
// integers 0/1, and INTEGER columns become BIGINT (see ddl).
//
// JSON columns are TEXT and may hold anything (invalid JSON, ”, 'null', a
// list). PostgreSQL does not guarantee the evaluation order of AND/OR, so
// every JSON helper guards its own cast with CASE WHEN col IS JSON OBJECT
// (CASE evaluates in order) and never relies on a sibling jsonValid
// condition: an invalid row yields NULL (false for the EXISTS of
// jsonAnyValueEquals), never an error.
type postgresDialect struct{}

var _ dialect = postgresDialect{}

// SQLSTATE codes (https://www.postgresql.org/docs/current/errcodes-appendix.html).
const pgUniqueViolation = "23505" // unique_violation

func (postgresDialect) name() string { return "postgres" }

// rebind replaces each ? with $1, $2, ... in order, in one pass. Single-quoted
// string literals ('...', with ” as an escaped quote) and double-quoted
// identifiers are copied unchanged: a doubled quote simply closes the section
// and opens the next one, which is copied the same way. An unterminated quote
// copies the rest of the query unchanged (the database reports the error).
// A query without ? is returned as it is.
func (postgresDialect) rebind(query string) string {
	if strings.IndexByte(query, '?') < 0 {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 32)
	n := 0
	start := 0 // query[start:i] is pending, unchanged
	for i := 0; i < len(query); {
		switch c := query[i]; c {
		case '\'', '"':
			end := strings.IndexByte(query[i+1:], c)
			if end < 0 {
				i = len(query)
				continue
			}
			i += end + 2
		case '?':
			b.WriteString(query[start:i])
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i++
			start = i
		default:
			i++
		}
	}
	b.WriteString(query[start:])
	return b.String()
}

// isIdentByte reports whether c can be part of an SQL word.
func isIdentByte(c byte) bool {
	return c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// ddl rewrites the column type INTEGER (the whole upper-case word, as written
// in migrations.go) to BIGINT: SQLite's INTEGER is 64-bit, PostgreSQL's is
// 32-bit and would overflow unix seconds and memory sizes. Every other part
// of the schema is valid PostgreSQL as written.
func (postgresDialect) ddl(stmt string) string {
	const from, to = "INTEGER", "BIGINT"
	if !strings.Contains(stmt, from) {
		return stmt
	}
	var b strings.Builder
	b.Grow(len(stmt))
	last := 0 // stmt[last:] is not yet written
	for i := 0; i < len(stmt); {
		j := strings.Index(stmt[i:], from)
		if j < 0 {
			break
		}
		j += i
		end := j + len(from)
		if (j == 0 || !isIdentByte(stmt[j-1])) && (end == len(stmt) || !isIdentByte(stmt[end])) {
			b.WriteString(stmt[last:j])
			b.WriteString(to)
			last = end
		}
		i = end
	}
	b.WriteString(stmt[last:])
	return b.String()
}

// columnExists looks the column up in information_schema.columns of the
// current schema, case-insensitively.
func (postgresDialect) columnExists(ctx context.Context, q queryer, table, column string) (bool, error) {
	var ok bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name::text = CAST(? AS TEXT)
		   AND lower(column_name::text) = lower(CAST(? AS TEXT)))`,
		table, column).Scan(&ok); err != nil {
		return false, fmt.Errorf("look up column %s.%s: %w", table, column, err)
	}
	return ok, nil
}

// indexExists looks the index up in pg_indexes of the current schema.
func (postgresDialect) indexExists(ctx context.Context, q queryer, name string) (bool, error) {
	var ok bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes
		 WHERE schemaname = current_schema() AND indexname::text = CAST(? AS TEXT))`,
		name).Scan(&ok); err != nil {
		return false, fmt.Errorf("look up index %s: %w", name, err)
	}
	return ok, nil
}

// tableNames lists the base tables of the current schema from
// information_schema.tables.
func (postgresDialect) tableNames(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT table_name::text FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		 ORDER BY table_name::text`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return names, nil
}

// columnNames lists the columns of table in the current schema from
// information_schema.columns, lower-cased, by ordinal_position.
func (postgresDialect) columnNames(ctx context.Context, q queryer, table string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT lower(column_name::text) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name::text = CAST(? AS TEXT)
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("list columns of %s: %w", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list columns of %s: %w", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list columns of %s: %w", table, err)
	}
	return names, nil
}

// pgJSONPath renders a dot-separated key path as a quoted PostgreSQL text[]
// path literal: "a.b" -> '{a,b}'. Paths are Go constants of the store; it
// panics on anything but non-empty keys made of [a-z0-9_], so a path that
// would need quoting can never reach the SQL text.
func pgJSONPath(path string) string {
	keys := strings.Split(path, ".")
	for _, k := range keys {
		if k == "" {
			panic(fmt.Sprintf("store: invalid JSON path %q", path))
		}
		for i := 0; i < len(k); i++ {
			if c := k[i]; !(c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z')) {
				panic(fmt.Sprintf("store: invalid JSON path %q", path))
			}
		}
	}
	return `'{` + strings.Join(keys, ",") + `}'`
}

// pgIfObject is CASE WHEN col IS JSON OBJECT THEN expr END: expr may cast col
// to json, it is only evaluated on rows that hold a JSON object.
func pgIfObject(col, expr string) string {
	return `(CASE WHEN ` + col + ` IS JSON OBJECT THEN ` + expr + ` END)`
}

func (postgresDialect) jsonValid(col string) string { return `(` + col + ` IS JSON)` }

func (postgresDialect) jsonText(col, path string) string {
	return pgIfObject(col, col+`::json #>> `+pgJSONPath(path))
}

// jsonNumber is NULL unless the value at path is a JSON number. The type
// test and the cast are nested CASEs so the cast never sees a non-number.
func (postgresDialect) jsonNumber(col, path string) string {
	p := pgJSONPath(path)
	return pgIfObject(col,
		`CASE WHEN json_typeof(`+col+`::json #> `+p+`) = 'number' THEN (`+col+`::json #>> `+p+`)::numeric END`)
}

// jsonIsTrue is true when the value at path is JSON true or a JSON number
// equal to 1, like SQLite's json_extract(...)=1 (which is also true for 1.0
// and false for the strings "true" and "1"); false for any other value or a
// missing key, NULL when col is not a JSON object.
func (postgresDialect) jsonIsTrue(col, path string) string {
	p := pgJSONPath(path)
	return pgIfObject(col,
		`CASE json_typeof(`+col+`::json #> `+p+`)`+
			` WHEN 'boolean' THEN (`+col+`::json #>> `+p+`) = 'true'`+
			` WHEN 'number' THEN (`+col+`::json #>> `+p+`)::numeric = 1`+
			` ELSE FALSE END`)
}

// jsonHasKey is true when path is present, also when its value is JSON null
// (#> yields the json value null, which IS NOT NULL in SQL).
func (postgresDialect) jsonHasKey(col, path string) string {
	return pgIfObject(col, col+`::json #> `+pgJSONPath(path)+` IS NOT NULL`)
}

// jsonRemove deletes the paths through jsonb (json has no #-); a column that
// is not a JSON object is returned unchanged. The result is jsonb's text
// form (keys reordered, normalised spacing): the same JSON value.
func (postgresDialect) jsonRemove(col string, paths ...string) string {
	expr := col + `::jsonb`
	for _, p := range paths {
		expr = `(` + expr + ` #- ` + pgJSONPath(p) + `)`
	}
	return `(CASE WHEN ` + col + ` IS JSON OBJECT THEN ` + expr + `::text ELSE ` + col + ` END)`
}

func (postgresDialect) jsonTextParam(col string) string {
	return pgIfObject(col, col+`::json ->> CAST(? AS TEXT)`)
}

func (postgresDialect) jsonAnyValueEquals(col string) string {
	return `EXISTS (SELECT 1 FROM json_each_text(CASE WHEN ` + col + ` IS JSON OBJECT THEN ` + col +
		`::json ELSE '{}'::json END) AS kv WHERE kv.value = ?)`
}

// bindArgs makes the arguments acceptable to PostgreSQL: TEXT rejects NUL
// bytes and invalid UTF-8, which SQLite stores (task stdout/stderr can hold
// both), so string and non-nil *string values lose their NULs and have
// invalid UTF-8 replaced with U+FFFD; []byte values are left alone. bool and
// non-nil *bool become int64 0/1 (the store's boolean columns are integers
// and pgx does not encode a bool into an integer column). The slice is copied
// only when a value changes.
func (postgresDialect) bindArgs(args []any) []any {
	var out []any
	for i, a := range args {
		var v any
		switch x := a.(type) {
		case string:
			s, changed := pgSanitizeText(x)
			if !changed {
				continue
			}
			v = s
		case *string:
			if x == nil {
				continue
			}
			s, changed := pgSanitizeText(*x)
			if !changed {
				continue
			}
			v = s
		case bool:
			v = boolInt64(x)
		case *bool:
			if x == nil {
				continue
			}
			v = boolInt64(*x)
		default:
			continue
		}
		if out == nil {
			out = make([]any, len(args))
			copy(out, args)
		}
		out[i] = v
	}
	if out == nil {
		return args
	}
	return out
}

// sanitizeText is pgSanitizeText, the rule bindArgs applies to strings.
func (postgresDialect) sanitizeText(s string) (string, bool) { return pgSanitizeText(s) }

func boolInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// pgNULEscape is the JSON escape for a NUL character. PostgreSQL cannot hold
// that character in text, so a JSON document containing the escape fails as
// soon as it is cast to jsonb or the value is extracted (SQLSTATE 22P05).
const pgNULEscape = `\u0000`

// jsonDoc replaces the JSON escape \u0000 by \ufffd. It is applied only to
// the documents SQL parses (see dialect.jsonDoc); other TEXT columns hold the
// six characters unchanged.
func (postgresDialect) jsonDoc(doc string) string {
	if !strings.Contains(doc, pgNULEscape) {
		return doc
	}
	return strings.ReplaceAll(doc, pgNULEscape, `\ufffd`)
}

// pgSanitizeText returns s with invalid UTF-8 replaced by U+FFFD and NUL
// bytes removed, and whether that changed anything. Valid text without NUL
// (the common case) is only scanned.
func pgSanitizeText(s string) (string, bool) {
	if strings.IndexByte(s, 0) < 0 && utf8.ValidString(s) {
		return s, false
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, string(utf8.RuneError)), "\x00", ""), true
}

func (postgresDialect) isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgUniqueViolation
}

// isPrimaryKeyViolation relies on PostgreSQL's default constraint name for a
// primary key, <table>_pkey (the schema declares no constraint names).
func (postgresDialect) isPrimaryKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgUniqueViolation && strings.HasSuffix(pe.ConstraintName, "_pkey")
}
