package store

import (
	"context"
	"errors"
	"testing"
)

// TestSQLiteDialect_Fragments pins the SQL text sqliteDialect produces: the
// fragments the store sent to SQLite before the dialect seam existed
// (whitespace inside json_extract aside).
func TestSQLiteDialect_Fragments(t *testing.T) {
	d := sqliteDialect{}
	const query = `SELECT id FROM tasks WHERE state = ? AND executor_type = ? LIMIT 10`
	const stmt = `ALTER TABLE tasks ADD COLUMN charged_gpus INTEGER`
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"name", d.name(), "sqlite"},
		{"rebind is the identity", d.rebind(query), query},
		{"ddl is the identity", d.ddl(stmt), stmt},
		{"jsonValid", d.jsonValid("runtime_hints"), `json_valid(runtime_hints)`},
		{"jsonNumber", d.jsonNumber("runtime_hints", "cores"), `json_extract(runtime_hints,'$.cores')`},
		{"jsonNumber gpus", d.jsonNumber("runtime_hints", "gpus"), `json_extract(runtime_hints,'$.gpus')`},
		{"jsonText", d.jsonText("runtime_hints", "worker_group"), `json_extract(runtime_hints,'$.worker_group')`},
		{"jsonText nested", d.jsonText("runtime_hints", "stager_overrides.http_credential"), `json_extract(runtime_hints,'$.stager_overrides.http_credential')`},
		{"jsonIsTrue", d.jsonIsTrue("runtime_hints", "inject_bvbrc_token"), `json_extract(runtime_hints,'$.inject_bvbrc_token')=1`},
		{"jsonHasKey", d.jsonHasKey("runtime_hints", "stager_overrides.http_credential"), `json_type(runtime_hints, '$.stager_overrides.http_credential') IS NOT NULL`},
		{"jsonHasKey top level", d.jsonHasKey("runtime_hints", "secrets"), `json_type(runtime_hints, '$.secrets') IS NOT NULL`},
		{"jsonRemove two paths", d.jsonRemove("runtime_hints", "stager_overrides.http_credential", "secrets"), `json_remove(runtime_hints, '$.stager_overrides.http_credential', '$.secrets')`},
		{"jsonRemove one path", d.jsonRemove("runtime_hints", "inject_bvbrc_token"), `json_remove(runtime_hints, '$.inject_bvbrc_token')`},
		{"jsonTextParam", d.jsonTextParam("labels"), `json_extract(labels, '$.'||?)`},
		{"jsonAnyValueEquals", d.jsonAnyValueEquals("labels"), `EXISTS (SELECT 1 FROM json_each(labels) WHERE json_each.value = ?)`},
		// The CheckoutTask hint guard, as built from the helpers.
		{"checkout hint guard",
			`CASE WHEN ` + d.jsonValid("runtime_hints") + ` THEN ` + d.jsonNumber("runtime_hints", "ram_mb") + ` END`,
			`CASE WHEN json_valid(runtime_hints) THEN json_extract(runtime_hints,'$.ram_mb') END`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got  %s\nwant %s", tc.got, tc.want)
			}
		})
	}
}

// TestSQLiteDialect_ConstraintErrors checks the constraint classification on
// real SQLite errors.
func TestSQLiteDialect_ConstraintErrors(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	insert := func(id, key, value string) error {
		_, err := st.db.ExecContext(ctx,
			`INSERT INTO label_vocabulary (id, key, value, description, color, created_at) VALUES (?, ?, ?, '', '', ?)`,
			id, key, value, "2026-01-01T00:00:00.000000000Z")
		return err
	}
	if err := insert("lv_1", "project", "a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pkErr := insert("lv_1", "project", "b")
	uniqueErr := insert("lv_2", "project", "a")

	tests := []struct {
		name               string
		err                error
		wantUnique, wantPK bool
	}{
		{"nil", nil, false, false},
		{"not a database error", errors.New("boom"), false, false},
		{"duplicate primary key", pkErr, true, true},
		{"duplicate unique index", uniqueErr, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := st.dialect.isUniqueViolation(tc.err); got != tc.wantUnique {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", tc.err, got, tc.wantUnique)
			}
			if got := st.dialect.isPrimaryKeyViolation(tc.err); got != tc.wantPK {
				t.Errorf("isPrimaryKeyViolation(%v) = %v, want %v", tc.err, got, tc.wantPK)
			}
		})
	}
}

// TestSQLiteDialect_Catalog checks columnExists and indexExists on a migrated
// database.
func TestSQLiteDialect_Catalog(t *testing.T) {
	st := testStore(t)
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{"existing column", hasColumn(t, st, "tasks", "runtime_hints"), true},
		{"column name is case-insensitive", hasColumn(t, st, "tasks", "RUNTIME_HINTS"), true},
		{"missing column", hasColumn(t, st, "tasks", "no_such_column"), false},
		{"existing index", hasIndex(t, st, "idx_label_vocab_key"), true},
		{"missing index", hasIndex(t, st, "idx_no_such_index"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}
