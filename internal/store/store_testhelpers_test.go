package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// postgresTestURLEnv names the environment variable that runs the store tests
// on PostgreSQL: a connection URL to a database the tests may create schemas
// in (e.g. postgres://postgres:postgres@localhost:5432/postgres).
const postgresTestURLEnv = "GOWE_TEST_POSTGRES_URL"

// testLogger is the quiet logger of the test stores.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testStore returns a migrated store that is closed when the test ends: on
// PostgreSQL in a schema of its own when GOWE_TEST_POSTGRES_URL is set,
// otherwise an in-memory SQLite database. It is the one store factory of the
// package's tests.
func testStore(t *testing.T) *SQLStore {
	t.Helper()
	if url := os.Getenv(postgresTestURLEnv); url != "" {
		return testPostgresStore(t, url)
	}
	// A CI job that means to test PostgreSQL must not pass on SQLite because
	// the URL variable was dropped or misspelt.
	if os.Getenv("GOWE_TEST_REQUIRE_POSTGRES") != "" {
		t.Fatalf("GOWE_TEST_REQUIRE_POSTGRES is set but %s is not", postgresTestURLEnv)
	}
	st, err := NewSQLiteStore(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testPostgresStore creates a fresh schema gowe_t_<16 hex> in the database
// at url, returns a migrated store working in it, and drops the schema when
// the test ends. The schema is created and dropped over a separate
// connection that is only open for that, so a test holds one connection.
func testPostgresStore(t *testing.T, url string) *SQLStore {
	t.Helper()
	ctx := context.Background()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random schema name: %v", err)
	}
	schema := "gowe_t_" + hex.EncodeToString(b[:])

	// adminExec runs one statement in the database's default schema.
	adminExec := func(stmt string) error {
		admin, err := NewPostgresStore(ctx, PostgresConfig{URL: url}, testLogger())
		if err != nil {
			return err
		}
		defer admin.Close()
		_, err = admin.db.ExecContext(ctx, stmt)
		return err
	}
	if err := adminExec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := adminExec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	st, err := NewPostgresStore(ctx, PostgresConfig{URL: url, Schema: schema}, testLogger())
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	// Registered after the DROP cleanup, so it runs before it.
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// isPostgres reports whether st runs on PostgreSQL.
func isPostgres(st *SQLStore) bool { return st.dialect.name() == "postgres" }

// skipOnPostgres skips the test when st runs on PostgreSQL; reason says why
// the test only applies to SQLite.
func skipOnPostgres(t *testing.T, st *SQLStore, reason string) {
	t.Helper()
	if isPostgres(st) {
		t.Skipf("not on PostgreSQL: %s", reason)
	}
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
