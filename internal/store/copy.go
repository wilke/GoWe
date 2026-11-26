package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrCopyCommitUnconfirmed is returned (wrapped) when the final COMMIT on the
// destination returned an error. The rows may or may not have been
// committed, for example when the connection dropped at that moment.
var ErrCopyCommitUnconfirmed = errors.New("the commit on the destination was not confirmed")

// copyBeforeVerifyHook, when set by a test, runs in the destination
// transaction after all rows were inserted and before they are verified.
var copyBeforeVerifyHook func(ctx context.Context, tx *sqlTx) error

// CopyReport says what CopyDatabase did.
type CopyReport struct {
	Tables []CopyTableReport // in copy order
}

// CopyTableReport is what CopyDatabase did to one table.
type CopyTableReport struct {
	Table           string
	Rows            int64
	TimestampsFixed int64 // TEXT timestamps rewritten to the fixed-width UTC layout
	TextSanitised   int64 // values changed because PostgreSQL cannot store them as written
}

// TotalRows is the number of rows copied over all tables.
func (r *CopyReport) TotalRows() int64 {
	var n int64
	for _, t := range r.Tables {
		n += t.Rows
	}
	return n
}

// copyTable is a table CopyDatabase copies.
type copyTable struct {
	name string
	pk   []string // primary key columns: the copy and verification order
	// secretKey: the primary key is a credential (the session id is the
	// session cookie), so errors identify a row by its position, never by
	// its key.
	secretKey bool
}

// copyTables lists every table of the schema (migrations.go) in copy order:
// users before linked_providers, which references it. A test fails when
// Migrate creates a table missing here.
var copyTables = []copyTable{
	{name: "users", pk: []string{"id"}},
	{name: "linked_providers", pk: []string{"user_id", "provider"}},
	{name: "workflows", pk: []string{"id"}},
	{name: "submissions", pk: []string{"id"}},
	{name: "step_instances", pk: []string{"id"}},
	{name: "tasks", pk: []string{"id"}},
	{name: "workers", pk: []string{"id"}},
	{name: "worker_keys", pk: []string{"id"}},
	{name: "blessed_tools", pk: []string{"id"}},
	{name: "worker_group_policies", pk: []string{"group_name"}},
	{name: "server_settings", pk: []string{"key"}},
	{name: "sessions", pk: []string{"id"}, secretKey: true},
	{name: "label_vocabulary", pk: []string{"id"}},
}

// copyJSONDocColumns are the columns holding a JSON document that SQL parses
// (see dialect.jsonDoc), as "table.column".
var copyJSONDocColumns = map[string]bool{
	"tasks.runtime_hints": true,
	"workflows.labels":    true,
}

// copyProgressEvery is how many rows CopyDatabase copies between two calls
// of its progress function.
const copyProgressEvery = 1000

// migrateSourceHint is how an operator brings an older SQLite database to
// the current schema.
const migrateSourceHint = "the SQLite database must be at the current schema " +
	"(run the copy tool with --migrate-source on a COPY of the database)"

// copyColumn is a column of a table being copied and how its values are
// transformed on the way.
type copyColumn struct {
	name      string
	timestamp bool // TEXT timestamp: rewritten to the fixed-width layout
	jsonDoc   bool // JSON document SQL parses: passes through dialect.jsonDoc
}

// copyPlan is one table ready to copy: its columns in the destination's
// order and the SQL built from them.
type copyPlan struct {
	table     copyTable
	cols      []copyColumn
	pkIdx     []int // positions of the primary key columns in cols
	selectSQL string
	insertSQL string
}

// CopyDatabase copies every row of every table from src into dst, which must have the current
// schema (Migrate was run) and no rows. It is all-or-nothing: on any error or verification
// mismatch nothing is committed to dst.
//
// src is read twice: once to copy, once to verify, so it must not change
// meanwhile (it is meant to be an offline copy). Each value is transformed on
// the way: TEXT timestamps are rewritten to the store's fixed-width UTC
// layout, the JSON documents SQL parses pass through dst's jsonDoc, and text
// is made storable by dst (PostgreSQL: no NUL bytes, valid UTF-8). The
// verification compares every column of every row of dst with the
// transformed value of src.
//
// Errors name tables, columns, primary keys and counts, never other values.
// progress, when non-nil, is called every 1000 rows and at the end of each
// table with the number of rows of the table copied so far.
func CopyDatabase(ctx context.Context, src, dst *SQLStore, progress func(table string, rows int64)) (*CopyReport, error) {
	if src == nil || dst == nil {
		return nil, errors.New("copy database: source and destination are required")
	}
	if src == dst {
		return nil, errors.New("copy database: source and destination are the same store")
	}
	for _, t := range copyTables {
		if !isSQLIdentifier(t.name) {
			return nil, fmt.Errorf("copy database: invalid table name %q", t.name)
		}
		for _, c := range t.pk {
			if !isSQLIdentifier(c) {
				return nil, fmt.Errorf("copy database: invalid key column %q of table %s", c, t.name)
			}
		}
	}

	// Every table of the source must be one the copy knows, or its rows
	// would be left behind silently.
	srcTables, err := src.dialect.tableNames(ctx, src.db)
	if err != nil {
		return nil, fmt.Errorf("copy database: source: %w", err)
	}
	known := map[string]bool{}
	for _, t := range copyTables {
		known[t.name] = true
	}
	for _, name := range srcTables {
		if !known[name] {
			return nil, fmt.Errorf("copy database: the source database has a table %q that the copy does not know; "+
				"it is not part of the GoWe schema and its rows would be lost", name)
		}
	}

	// From here on every statement on dst runs in the transaction: the
	// store has one connection, a statement on dst.db would wait for it
	// forever.
	tx, err := dst.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("copy database: begin transaction on the destination: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	plans := make([]*copyPlan, 0, len(copyTables))
	for _, t := range copyTables {
		p, err := planCopy(ctx, src, dst, tx, t)
		if err != nil {
			return nil, fmt.Errorf("copy database: %w", err)
		}
		plans = append(plans, p)
	}

	// Keep everything else out of the tables until the commit: a server
	// started on the destination by mistake would otherwise add rows next to
	// the copy after its tables were verified.
	if dst.dialect.name() == "postgres" {
		names := make([]string, 0, len(plans))
		for _, p := range plans {
			names = append(names, p.table.name)
		}
		if _, err := tx.ExecContext(ctx, `LOCK TABLE `+strings.Join(names, ", ")+` IN ACCESS EXCLUSIVE MODE`); err != nil {
			return nil, fmt.Errorf("copy database: lock the destination tables: %w", safeDriverError(err))
		}
	}

	// The destination must be empty before anything is written.
	for _, p := range plans {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+p.table.name).Scan(&n); err != nil {
			return nil, fmt.Errorf("copy database: count rows of %s in the destination: %w", p.table.name, err)
		}
		if n != 0 {
			return nil, fmt.Errorf("copy database: target database is not empty (table %s has %d rows)", p.table.name, n)
		}
	}

	report := &CopyReport{}
	for _, p := range plans {
		tr, err := copyRows(ctx, src, dst.dialect, tx, p, progress)
		if err != nil {
			return nil, fmt.Errorf("copy database: %w", err)
		}
		report.Tables = append(report.Tables, *tr)
	}

	if copyBeforeVerifyHook != nil {
		if err := copyBeforeVerifyHook(ctx, tx); err != nil {
			return nil, err
		}
	}

	for i, p := range plans {
		if err := verifyRows(ctx, src, dst.dialect, tx, p, report.Tables[i].Rows); err != nil {
			return nil, fmt.Errorf("copy database: verify: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("copy database: %w: %v", ErrCopyCommitUnconfirmed, safeDriverError(err))
	}
	return report, nil
}

// planCopy reads the columns of t from both databases, checks that they are
// the same set, and builds the table's SELECT and INSERT.
func planCopy(ctx context.Context, src, dst *SQLStore, tx *sqlTx, t copyTable) (*copyPlan, error) {
	srcCols, err := src.dialect.columnNames(ctx, src.db, t.name)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dstCols, err := dst.dialect.columnNames(ctx, tx, t.name)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	if len(dstCols) == 0 {
		return nil, fmt.Errorf("table %s does not exist in the destination database (run Migrate on it first)", t.name)
	}
	if len(srcCols) == 0 {
		return nil, fmt.Errorf("table %s does not exist in the source database: %s", t.name, migrateSourceHint)
	}
	for _, c := range append(append([]string(nil), srcCols...), dstCols...) {
		if !isSQLIdentifier(c) {
			return nil, fmt.Errorf("table %s has a column named %q, which is not a plain lower-case identifier", t.name, c)
		}
	}
	if missing, extra := diffColumns(srcCols, dstCols); len(missing) > 0 || len(extra) > 0 {
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, "missing in the source: "+strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			parts = append(parts, "only in the source: "+strings.Join(extra, ", "))
		}
		return nil, fmt.Errorf("table %s has different columns in the two databases (%s): %s",
			t.name, strings.Join(parts, "; "), migrateSourceHint)
	}

	p := &copyPlan{table: t}
	names := make([]string, len(dstCols))
	for i, c := range dstCols {
		names[i] = c
		p.cols = append(p.cols, copyColumn{
			name:      c,
			timestamp: strings.HasSuffix(c, "_at") || c == "last_seen",
			jsonDoc:   copyJSONDocColumns[t.name+"."+c],
		})
	}
	for _, k := range t.pk {
		idx := -1
		for i, c := range dstCols {
			if c == k {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("table %s has no key column %s", t.name, k)
		}
		p.pkIdx = append(p.pkIdx, idx)
	}

	colList := strings.Join(names, ", ")
	p.selectSQL = `SELECT ` + colList + ` FROM ` + t.name + ` ORDER BY ` + strings.Join(t.pk, ", ")
	p.insertSQL = `INSERT INTO ` + t.name + ` (` + colList + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`
	return p, nil
}

// diffColumns returns the columns of dst that src lacks and the columns of
// src that dst lacks, each sorted.
func diffColumns(src, dst []string) (missing, extra []string) {
	inSrc := map[string]bool{}
	for _, c := range src {
		inSrc[c] = true
	}
	inDst := map[string]bool{}
	for _, c := range dst {
		inDst[c] = true
		if !inSrc[c] {
			missing = append(missing, c)
		}
	}
	for _, c := range src {
		if !inDst[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// newRowBuffer returns n values and pointers to them for rows.Scan.
func newRowBuffer(n int) (vals []any, ptrs []any) {
	vals = make([]any, n)
	ptrs = make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, ptrs
}

// copyRows streams the rows of p's table from src into the transaction tx,
// transformed for the destination dialect d.
func copyRows(ctx context.Context, src *SQLStore, d dialect, tx *sqlTx, p *copyPlan, progress func(string, int64)) (*CopyTableReport, error) {
	tr := &CopyTableReport{Table: p.table.name}
	rows, err := src.db.QueryContext(ctx, p.selectSQL)
	if err != nil {
		return nil, fmt.Errorf("read %s from the source: %w", p.table.name, err)
	}
	defer rows.Close()

	vals, ptrs := newRowBuffer(len(p.cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("read %s from the source, row %d: %w", p.table.name, tr.Rows+1, err)
		}
		for i, c := range p.cols {
			v, tsFixed, sanitised := transformCopyValue(d, c, vals[i])
			vals[i] = v
			if tsFixed {
				tr.TimestampsFixed++
			}
			if sanitised {
				tr.TextSanitised++
			}
		}
		if _, err := tx.ExecContext(ctx, p.insertSQL, vals...); err != nil {
			return nil, fmt.Errorf("insert into %s, %s: %w", p.table.name, rowRef(p, vals, tr.Rows+1), safeDriverError(err))
		}
		tr.Rows++
		if progress != nil && tr.Rows%copyProgressEvery == 0 {
			progress(p.table.name, tr.Rows)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s from the source: %w", p.table.name, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("read %s from the source: %w", p.table.name, err)
	}
	if progress != nil && (tr.Rows == 0 || tr.Rows%copyProgressEvery != 0) {
		progress(p.table.name, tr.Rows)
	}
	return tr, nil
}

// transformCopyValue turns a value read from the source into the value
// written to the destination: []byte becomes string; a string in a
// timestamp column that parses is rewritten with formatTime (tsFixed when
// that changed it); a JSON document SQL parses passes through d.jsonDoc and
// every string through d.sanitizeText (sanitised when either changed it).
// Other values are returned as they are.
func transformCopyValue(d dialect, c copyColumn, v any) (out any, tsFixed, sanitised bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		return v, false, false
	}
	if c.timestamp && s != "" {
		if t, err := parseTimeOrZero(s); err == nil {
			if f := formatTime(t); f != s {
				s, tsFixed = f, true
			}
		}
	}
	if c.jsonDoc {
		if j := d.jsonDoc(s); j != s {
			s, sanitised = j, true
		}
	}
	if clean, changed := d.sanitizeText(s); changed {
		s, sanitised = clean, true
	}
	return s, tsFixed, sanitised
}

// verifyRows reads p's table again from src and from the transaction tx,
// both ordered by primary key, and compares them row by row and column by
// column, the source value transformed as copyRows did. want is the number
// of rows copyRows inserted.
func verifyRows(ctx context.Context, src *SQLStore, d dialect, tx *sqlTx, p *copyPlan, want int64) error {
	srcRows, err := src.db.QueryContext(ctx, p.selectSQL)
	if err != nil {
		return fmt.Errorf("read %s from the source: %w", p.table.name, err)
	}
	defer srcRows.Close()
	dstRows, err := tx.QueryContext(ctx, p.selectSQL)
	if err != nil {
		return fmt.Errorf("read %s from the destination: %w", p.table.name, safeDriverError(err))
	}
	defer dstRows.Close()

	srcVals, srcPtrs := newRowBuffer(len(p.cols))
	dstVals, dstPtrs := newRowBuffer(len(p.cols))
	names := make([]string, len(p.cols))
	for i, c := range p.cols {
		names[i] = c.name
	}
	var n int64
	for {
		srcMore := srcRows.Next()
		dstMore := dstRows.Next()
		if !srcMore || !dstMore {
			if err := srcRows.Err(); err != nil {
				return fmt.Errorf("read %s from the source: %w", p.table.name, err)
			}
			if err := dstRows.Err(); err != nil {
				return fmt.Errorf("read %s from the destination: %w", p.table.name, safeDriverError(err))
			}
			if srcMore != dstMore {
				return fmt.Errorf("table %s: the destination has a different number of rows than the source (they differ after %d rows)", p.table.name, n)
			}
			break
		}
		n++
		if err := srcRows.Scan(srcPtrs...); err != nil {
			return fmt.Errorf("read %s from the source, row %d: %w", p.table.name, n, err)
		}
		if err := dstRows.Scan(dstPtrs...); err != nil {
			return fmt.Errorf("read %s from the destination, row %d: %w", p.table.name, n, safeDriverError(err))
		}
		for i, c := range p.cols {
			srcVals[i], _, _ = transformCopyValue(d, c, srcVals[i])
		}
		if col, ok := compareRows(names, srcVals, dstVals); !ok {
			return fmt.Errorf("table %s, %s: column %s differs between source and destination", p.table.name, rowRef(p, srcVals, n), col)
		}
	}
	if n != want {
		return fmt.Errorf("table %s: %d rows copied but %d read back", p.table.name, want, n)
	}
	return nil
}

// compareRows compares two rows of the same columns. It returns the name of
// the first column whose values differ and false, or "" and true when the
// rows are equal. Integers compare as int64, everything else as strings;
// NULL equals only NULL. It never returns a value.
func compareRows(cols []string, want, got []any) (string, bool) {
	for i, c := range cols {
		var w, g any
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if !sameValue(w, g) {
			return c, false
		}
	}
	if len(want) != len(cols) || len(got) != len(cols) {
		return "(number of columns)", false
	}
	return "", true
}

// cmpKind classifies a value for compareRows.
type cmpKind int

const (
	cmpNull cmpKind = iota
	cmpInt
	cmpText
)

// normaliseValue maps a value as a database/sql driver returns it to its
// kind and comparable form: integers (and integral floats) to int64, text to
// string, NULL to cmpNull. Anything else compares by its printed form.
func normaliseValue(v any) (cmpKind, int64, string) {
	switch x := v.(type) {
	case nil:
		return cmpNull, 0, ""
	case int64:
		return cmpInt, x, ""
	case int32:
		return cmpInt, int64(x), ""
	case int:
		return cmpInt, int64(x), ""
	case bool:
		if x {
			return cmpInt, 1, ""
		}
		return cmpInt, 0, ""
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x < math.MaxInt64 {
			return cmpInt, int64(x), ""
		}
		return cmpText, 0, strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return cmpText, 0, x
	case []byte:
		return cmpText, 0, string(x)
	default:
		return cmpText, 0, fmt.Sprintf("%T:%v", v, v)
	}
}

// sameValue reports whether a and b are the same value for compareRows.
func sameValue(a, b any) bool {
	ka, ia, sa := normaliseValue(a)
	kb, ib, sb := normaliseValue(b)
	if ka != kb {
		return false
	}
	switch ka {
	case cmpNull:
		return true
	case cmpInt:
		return ia == ib
	default:
		return sa == sb
	}
}

// rowRef identifies a row in an error: by its primary key, or by its
// position n (1-based, in primary key order) when the key is a credential or
// not plain text.
func rowRef(p *copyPlan, vals []any, n int64) string {
	if p.table.secretKey {
		return fmt.Sprintf("row %d in key order (the key of %s is a credential and is not shown)", n, p.table.name)
	}
	parts := make([]string, 0, len(p.pkIdx))
	for j, idx := range p.pkIdx {
		var v any
		if idx < len(vals) {
			v = vals[idx]
		}
		var key string
		switch x := v.(type) {
		case string:
			key = x
		case []byte:
			key = string(x)
		case int64:
			key = strconv.FormatInt(x, 10)
		default:
			return fmt.Sprintf("row %d in key order (key %s is not text)", n, p.table.pk[j])
		}
		parts = append(parts, p.table.pk[j]+"="+strconv.QuoteToASCII(key))
	}
	return "key " + strings.Join(parts, ", ")
}

// safeDriverError returns an error for err that holds no row value. A
// PostgreSQL error message can quote the value it rejected ("invalid input
// syntax for type bigint: ..."), and the driver's encoding errors print the
// Go value, so only the SQLSTATE and the names the server reports (table,
// column, constraint, type) are kept. Context cancellation is returned as
// it is.
func safeDriverError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, sql.ErrTxDone) || errors.Is(err, sql.ErrConnDone) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := "PostgreSQL error SQLSTATE " + pe.Code
		var names []string
		if pe.TableName != "" {
			names = append(names, "table "+pe.TableName)
		}
		if pe.ColumnName != "" {
			names = append(names, "column "+pe.ColumnName)
		}
		if pe.ConstraintName != "" {
			names = append(names, "constraint "+pe.ConstraintName)
		}
		if pe.DataTypeName != "" {
			names = append(names, "type "+pe.DataTypeName)
		}
		if len(names) > 0 {
			msg += " (" + strings.Join(names, ", ") + ")"
		}
		return errors.New(msg + "; the message is not shown because it can contain row values")
	}
	return fmt.Errorf("%T (the message is not shown because it can contain row values)", err)
}
