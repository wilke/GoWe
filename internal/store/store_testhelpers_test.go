package store

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// testStore returns a migrated in-memory store that is closed when the test
// ends. It is the one store factory of the package's tests.
func testStore(t *testing.T) *SQLStore {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// hasColumn reports whether table has column, asked through the store's
// dialect.
func hasColumn(t *testing.T, st *SQLStore, table, column string) bool {
	t.Helper()
	ok, err := st.dialect.columnExists(context.Background(), st.db, table, column)
	if err != nil {
		t.Fatalf("column %s.%s: %v", table, column, err)
	}
	return ok
}

// hasIndex reports whether an index called name exists, asked through the
// store's dialect.
func hasIndex(t *testing.T, st *SQLStore, name string) bool {
	t.Helper()
	ok, err := st.dialect.indexExists(context.Background(), st.db, name)
	if err != nil {
		t.Fatalf("index %s: %v", name, err)
	}
	return ok
}

// editRuntimeHints rewrites the stored runtime_hints JSON object of task
// taskID in Go, bypassing the store's encoding and guards: it reads the raw
// column, decodes it, applies edit and writes the result back. Numbers are
// kept as json.Number so they round-trip unchanged.
func editRuntimeHints(t *testing.T, st *SQLStore, taskID string, edit func(map[string]any)) {
	t.Helper()
	ctx := context.Background()
	var raw string
	if err := st.db.QueryRowContext(ctx, `SELECT runtime_hints FROM tasks WHERE id = ?`, taskID).Scan(&raw); err != nil {
		t.Fatalf("read runtime_hints of %s: %v", taskID, err)
	}
	hints := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&hints); err != nil {
		t.Fatalf("decode runtime_hints of %s: %v", taskID, err)
	}
	edit(hints)
	b, err := json.Marshal(hints)
	if err != nil {
		t.Fatalf("encode runtime_hints of %s: %v", taskID, err)
	}
	res, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints = ? WHERE id = ?`, string(b), taskID)
	if err != nil {
		t.Fatalf("write runtime_hints of %s: %v", taskID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("write runtime_hints of %s: %d rows, err %v", taskID, n, err)
	}
}
