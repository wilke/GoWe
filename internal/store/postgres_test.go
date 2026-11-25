package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/me/gowe/pkg/model"
)

// replacement is U+FFFD, what bindArgs puts in place of invalid UTF-8.
var replacement = string(utf8.RuneError)

// --- Unit tests: no database ---

func TestPostgresDialect_Rebind(t *testing.T) {
	d := postgresDialect{}
	var many, manyWant []string
	for i := 1; i <= 11; i++ {
		many = append(many, "?")
		manyWant = append(manyWant, fmt.Sprintf("$%d", i))
	}
	tests := []struct {
		name, in, want string
	}{
		{"no placeholders", `SELECT id FROM tasks WHERE state = 'QUEUED'`, `SELECT id FROM tasks WHERE state = 'QUEUED'`},
		{"empty", ``, ``},
		{"several", `UPDATE tasks SET state = ?, priority = ? WHERE id = ?`, `UPDATE tasks SET state = $1, priority = $2 WHERE id = $3`},
		{"two digits", `VALUES (` + strings.Join(many, ", ") + `)`, `VALUES (` + strings.Join(manyWant, ", ") + `)`},
		{"? inside a literal", `SELECT '?' FROM t WHERE a = ?`, `SELECT '?' FROM t WHERE a = $1`},
		{"escaped quote", `SELECT 'it''s ?' FROM t WHERE a = ?`, `SELECT 'it''s ?' FROM t WHERE a = $1`},
		{"literal of one escaped quote", `WHERE a = '''' AND b = ?`, `WHERE a = '''' AND b = $1`},
		{"empty literal", `WHERE a = '' AND b = ?`, `WHERE a = '' AND b = $1`},
		{"? right after a literal", `SELECT 'a'||? FROM t WHERE b = 'x'?`, `SELECT 'a'||$1 FROM t WHERE b = 'x'$2`},
		{"? right before a literal", `WHERE a = ?'x'`, `WHERE a = $1'x'`},
		{"double-quoted identifier", `SELECT "a?b" FROM t WHERE c = ?`, `SELECT "a?b" FROM t WHERE c = $1`},
		{"quote inside double quotes", `SELECT "it's?" FROM t WHERE c = ?`, `SELECT "it's?" FROM t WHERE c = $1`},
		{"unterminated literal", `SELECT ? FROM t WHERE a = 'abc ?`, `SELECT $1 FROM t WHERE a = 'abc ?`},
		{"store fragment", d.jsonTextParam("labels") + ` = ?`,
			`(CASE WHEN labels IS JSON OBJECT THEN labels::json ->> CAST($1 AS TEXT) END) = $2`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.rebind(tc.in); got != tc.want {
				t.Errorf("rebind(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPostgresDialect_DDL(t *testing.T) {
	d := postgresDialect{}
	tests := []struct {
		name, in, want string
	}{
		{"column type", `retry_count   INTEGER NOT NULL DEFAULT 0,`, `retry_count   BIGINT NOT NULL DEFAULT 0,`},
		{"at the end", `ALTER TABLE tasks ADD COLUMN charged_gpus INTEGER`, `ALTER TABLE tasks ADD COLUMN charged_gpus BIGINT`},
		{"at the start", `INTEGER`, `BIGINT`},
		{"several", `(a INTEGER,b INTEGER)`, `(a BIGINT,b BIGINT)`},
		{"not in a longer word", `INTEGERS MYINTEGER INTEGER_X X_INTEGER INTEGER2`, `INTEGERS MYINTEGER INTEGER_X X_INTEGER INTEGER2`},
		{"words around a match", `INTEGERS INTEGER MYINTEGER`, `INTEGERS BIGINT MYINTEGER`},
		{"lower case is left alone", `a integer`, `a integer`},
		{"no INTEGER", `CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`, `CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.ddl(tc.in); got != tc.want {
				t.Errorf("ddl(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}

	// Every INTEGER of the real schema becomes BIGINT.
	var stmts []string
	stmts = append(stmts, schema...)
	for _, a := range alterStatements {
		stmts = append(stmts, a.alterSQL, a.indexSQL)
	}
	for _, s := range stmts {
		got := d.ddl(s)
		if strings.Contains(got, "INTEGER") || strings.Count(got, "BIGINT") != strings.Count(s, "INTEGER") {
			t.Errorf("ddl(%q) = %q", s, got)
		}
	}
}

// TestPostgresDialect_Fragments pins the SQL text of the JSON helpers and
// the catalog-free methods.
func TestPostgresDialect_Fragments(t *testing.T) {
	d := postgresDialect{}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"name", d.name(), "postgres"},
		{"jsonValid", d.jsonValid("runtime_hints"), `(runtime_hints IS JSON)`},
		{"jsonText", d.jsonText("runtime_hints", "worker_group"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN runtime_hints::json #>> '{worker_group}' END)`},
		{"jsonText nested", d.jsonText("runtime_hints", "stager_overrides.http_credential"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN runtime_hints::json #>> '{stager_overrides,http_credential}' END)`},
		{"jsonNumber", d.jsonNumber("runtime_hints", "cores"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN CASE WHEN json_typeof(runtime_hints::json #> '{cores}') = 'number' THEN (runtime_hints::json #>> '{cores}')::numeric END END)`},
		{"jsonIsTrue", d.jsonIsTrue("runtime_hints", "inject_bvbrc_token"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN CASE json_typeof(runtime_hints::json #> '{inject_bvbrc_token}')` +
				` WHEN 'boolean' THEN (runtime_hints::json #>> '{inject_bvbrc_token}') = 'true'` +
				` WHEN 'number' THEN (runtime_hints::json #>> '{inject_bvbrc_token}')::numeric = 1 ELSE FALSE END END)`},
		{"jsonHasKey", d.jsonHasKey("runtime_hints", "stager_overrides.http_credential"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN runtime_hints::json #> '{stager_overrides,http_credential}' IS NOT NULL END)`},
		{"jsonRemove two paths", d.jsonRemove("runtime_hints", "stager_overrides.http_credential", "secrets"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN ((runtime_hints::jsonb #- '{stager_overrides,http_credential}') #- '{secrets}')::text ELSE runtime_hints END)`},
		{"jsonRemove one path", d.jsonRemove("runtime_hints", "inject_bvbrc_token"),
			`(CASE WHEN runtime_hints IS JSON OBJECT THEN (runtime_hints::jsonb #- '{inject_bvbrc_token}')::text ELSE runtime_hints END)`},
		{"jsonTextParam", d.jsonTextParam("labels"),
			`(CASE WHEN labels IS JSON OBJECT THEN labels::json ->> CAST(? AS TEXT) END)`},
		{"jsonAnyValueEquals", d.jsonAnyValueEquals("labels"),
			`EXISTS (SELECT 1 FROM json_each_text(CASE WHEN labels IS JSON OBJECT THEN labels::json ELSE '{}'::json END) AS kv WHERE kv.value = ?)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got  %s\nwant %s", tc.got, tc.want)
			}
		})
	}
}

func TestPostgresDialect_JSONPathRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"", ".", "a..b", ".a", "a.", "A", "a-b", "a'b", "a b", "a,b", "a}b", "a\"b"} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("pgJSONPath(%q) did not panic", path)
				}
			}()
			_ = pgJSONPath(path)
		})
	}
	if got := pgJSONPath("stager_overrides.http_credential"); got != `'{stager_overrides,http_credential}'` {
		t.Errorf("pgJSONPath = %s", got)
	}
}

func TestPostgresDialect_BindArgs(t *testing.T) {
	d := postgresDialect{}
	nul := "x\x00y"
	valid := "fine"
	yes, no := true, false
	tests := []struct {
		name string
		in   []any
		want []any
	}{
		{"NUL removed", []any{"a\x00b\x00"}, []any{"ab"}},
		{"invalid UTF-8 replaced", []any{"a\xffb"}, []any{"a" + replacement + "b"}},
		{"truncated sequence replaced", []any{"caf\xc3"}, []any{"caf" + replacement}},
		{"invalid UTF-8 and NUL", []any{"a\x00\xff\xfeb"}, []any{"a" + replacement + "b"}},
		{"*string sanitised to a string", []any{&nul}, []any{"xy"}},
		{"JSON NUL escape kept in plain text", []any{`split("\u0000")`}, []any{`split("\u0000")`}},
		{"bool to int64", []any{true, false}, []any{int64(1), int64(0)}},
		{"*bool to int64", []any{&yes, &no, (*bool)(nil)}, []any{int64(1), int64(0), (*bool)(nil)}},
		{"others kept", []any{"ok", 7, &valid, []byte("b\x00\xff"), nil, true},
			[]any{"ok", 7, &valid, []byte("b\x00\xff"), nil, int64(1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := fmt.Sprintf("%#v", tc.in)
			got := d.bindArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("bindArgs = %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if fmt.Sprintf("%#v", got[i]) != fmt.Sprintf("%#v", tc.want[i]) {
					t.Errorf("arg %d = %#v, want %#v", i, got[i], tc.want[i])
				}
			}
			if after := fmt.Sprintf("%#v", tc.in); after != before {
				t.Errorf("input modified: %s, was %s", after, before)
			}
		})
	}
	if nul != "x\x00y" {
		t.Errorf("*string target modified: %q", nul)
	}

	t.Run("untouched slice returned as is", func(t *testing.T) {
		in := []any{"plain", "ünïcödé", 1, int64(2), nil, []byte("x\x00"), (*string)(nil), &valid, 1.5}
		got := d.bindArgs(in)
		if len(got) != len(in) || &got[0] != &in[0] {
			t.Errorf("bindArgs copied a slice it did not change")
		}
		if got := d.bindArgs(nil); got != nil {
			t.Errorf("bindArgs(nil) = %#v", got)
		}
	})
	t.Run("sqlite returns the same slice", func(t *testing.T) {
		in := []any{"a\x00b", true}
		got := sqliteDialect{}.bindArgs(in)
		if len(got) != len(in) || &got[0] != &in[0] || got[0] != "a\x00b" || got[1] != true {
			t.Errorf("sqliteDialect.bindArgs changed its input: %#v", got)
		}
	})
}

func TestPostgresSecretsAreRedacted(t *testing.T) {
	const url = "postgres://gowe:s3cr3t-pw@db.example:notaport/gowe"
	_, err := NewPostgresStore(context.Background(), PostgresConfig{URL: url, Password: "0ther-pw"}, testLogger())
	if err == nil {
		t.Fatal("NewPostgresStore accepted an invalid port")
	}
	for _, secret := range []string{"s3cr3t-pw", "0ther-pw", url} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q contains %q", err, secret)
		}
	}

	// A malformed URL makes the parser quote a fragment of it, which can be
	// part of the password.
	for _, bad := range []string{"postgres://user:pa/ss@host/db", "postgres://user:x%zzy@host/db"} {
		_, err := NewPostgresStore(context.Background(), PostgresConfig{URL: bad}, testLogger())
		if err == nil {
			t.Fatalf("NewPostgresStore accepted %q", bad)
		}
		for _, frag := range []string{"pa", "ss", "zz"} {
			if strings.Contains(strings.TrimPrefix(err.Error(), "parse postgres connection string: invalid connection string"), frag) {
				t.Errorf("error %q for %q contains %q", err, bad, frag)
			}
		}
	}
	if _, err := NewPostgresStore(context.Background(), PostgresConfig{URL: "  "}, testLogger()); err == nil {
		t.Error("NewPostgresStore accepted an empty connection string")
	}

	secrets := postgresSecrets(PostgresConfig{URL: "postgres://u:p%40ss@h/db", Password: "other"})
	msg := redactSecrets(`parse "postgres://u:p%40ss@h/db": p@ss other`, secrets)
	for _, secret := range []string{"p@ss", "p%40ss", "other"} {
		if strings.Contains(msg, secret) {
			t.Errorf("redacted message %q contains %q", msg, secret)
		}
	}
}

func TestDialect_JSONDoc(t *testing.T) {
	const doc = `{"a":"x\u0000y","b":1}`
	if got := (sqliteDialect{}).jsonDoc(doc); got != doc {
		t.Errorf("sqlite jsonDoc = %q, want it unchanged", got)
	}
	if got, want := (postgresDialect{}).jsonDoc(doc), `{"a":"x\ufffdy","b":1}`; got != want {
		t.Errorf("postgres jsonDoc = %q, want %q", got, want)
	}
	if got := (postgresDialect{}).jsonDoc(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("postgres jsonDoc changed a clean document: %q", got)
	}
}

// --- Tests on a real PostgreSQL (GOWE_TEST_POSTGRES_URL) ---

// requirePostgres returns a test store on PostgreSQL, or skips the test when
// GOWE_TEST_POSTGRES_URL is not set.
func requirePostgres(t *testing.T) *SQLStore {
	t.Helper()
	if os.Getenv(postgresTestURLEnv) == "" {
		t.Skipf("set %s to run against PostgreSQL", postgresTestURLEnv)
	}
	st := testStore(t)
	if !isPostgres(st) {
		t.Fatalf("testStore returned a %s store with %s set", st.dialect.name(), postgresTestURLEnv)
	}
	return st
}

func TestPostgres_ServerChecks(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()
	if err := checkPostgresServer(ctx, st.db.db); err != nil {
		t.Fatalf("server checks on the test database: %v", err)
	}

	url := os.Getenv(postgresTestURLEnv)
	t.Run("missing schema", func(t *testing.T) {
		bad, err := NewPostgresStore(ctx, PostgresConfig{URL: url, Schema: "gowe_t_no_such_schema"}, testLogger())
		if err == nil {
			bad.Close()
			t.Fatal("NewPostgresStore accepted a schema that does not exist")
		}
		if !strings.Contains(err.Error(), "schema") {
			t.Errorf("error %q does not name the schema problem", err)
		}
	})
	t.Run("wrong password is not reported", func(t *testing.T) {
		const pw = "gowe-wrong-pw-5f2a"
		bad, err := NewPostgresStore(ctx, PostgresConfig{URL: url, Password: pw}, testLogger())
		if err == nil {
			bad.Close()
			t.Skip("the server accepts any password (trust authentication)")
		}
		if strings.Contains(err.Error(), pw) || strings.Contains(err.Error(), url) {
			t.Errorf("error %q holds the password or the URL", err)
		}
	})
}

// TestPostgres_JSONHelpers evaluates every JSON helper in real SQL against a
// valid object and against values that are not a JSON object: each must
// yield NULL/false there, never an error.
func TestPostgres_JSONHelpers(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()
	d := st.dialect

	docs := map[string]string{
		"obj": `{"s":"x","n":5,"f":1.5,"t":true,"fl":false,"one":1,"onef":1.0,"st":"true","s1":"1",` +
			`"z":null,"nest":{"k":"v","m":"w"},"arr":[1,2]}`,
		"empty": ``,
		"null":  `null`,
		"list":  `[]`,
		"bad":   `not json`,
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TABLE json_probe (id TEXT PRIMARY KEY, doc TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for id, doc := range docs {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO json_probe (id, doc) VALUES (?, ?)`, id, doc); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// texts evaluates frag (as text) on every row; NULL rows are absent.
	texts := func(t *testing.T, frag string, args ...any) map[string]string {
		t.Helper()
		rows, err := st.db.QueryContext(ctx, `SELECT id, CAST(`+frag+` AS TEXT) FROM json_probe`, args...)
		if err != nil {
			t.Fatalf("query %s: %v", frag, err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var id string
			var v sql.NullString
			if err := rows.Scan(&id, &v); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if v.Valid {
				got[id] = v.String
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("query %s: %v", frag, err)
		}
		return got
	}
	// truths evaluates condition frag on every row; rows where it is true
	// are present (NULL and false are both absent).
	truths := func(t *testing.T, frag string, args ...any) map[string]bool {
		t.Helper()
		rows, err := st.db.QueryContext(ctx, `SELECT id, COALESCE(`+frag+`, FALSE) FROM json_probe`, args...)
		if err != nil {
			t.Fatalf("query %s: %v", frag, err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var id string
			var v bool
			if err := rows.Scan(&id, &v); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if v {
				got[id] = true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("query %s: %v", frag, err)
		}
		return got
	}
	obj := func(v string) map[string]string { return map[string]string{"obj": v} }
	objTrue := map[string]bool{"obj": true}
	none := map[string]string{}
	noneTrue := map[string]bool{}

	textTests := []struct {
		name string
		frag string
		args []any
		want map[string]string
	}{
		{"jsonText string", d.jsonText("doc", "s"), nil, obj("x")},
		{"jsonText number", d.jsonText("doc", "n"), nil, obj("5")},
		{"jsonText nested", d.jsonText("doc", "nest.k"), nil, obj("v")},
		{"jsonText JSON null", d.jsonText("doc", "z"), nil, none},
		{"jsonText missing", d.jsonText("doc", "missing"), nil, none},
		{"jsonText through a scalar", d.jsonText("doc", "s.k"), nil, none},
		{"jsonNumber integer", d.jsonNumber("doc", "n"), nil, obj("5")},
		{"jsonNumber fraction", d.jsonNumber("doc", "f"), nil, obj("1.5")},
		{"jsonNumber string", d.jsonNumber("doc", "s1"), nil, none},
		{"jsonNumber true", d.jsonNumber("doc", "t"), nil, none},
		{"jsonNumber object", d.jsonNumber("doc", "nest"), nil, none},
		{"jsonNumber missing", d.jsonNumber("doc", "missing"), nil, none},
		{"jsonTextParam present", d.jsonTextParam("doc"), []any{"s"}, obj("x")},
		{"jsonTextParam number", d.jsonTextParam("doc"), []any{"n"}, obj("5")},
		{"jsonTextParam missing", d.jsonTextParam("doc"), []any{"missing"}, none},
	}
	for _, tc := range textTests {
		t.Run(tc.name, func(t *testing.T) {
			got := texts(t, tc.frag, tc.args...)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("%s\n got %v\nwant %v", tc.frag, got, tc.want)
			}
		})
	}

	condTests := []struct {
		name string
		frag string
		args []any
		want map[string]bool
	}{
		{"jsonValid", d.jsonValid("doc"), nil, map[string]bool{"obj": true, "null": true, "list": true}},
		{"jsonIsTrue true", d.jsonIsTrue("doc", "t"), nil, objTrue},
		{"jsonIsTrue number 1", d.jsonIsTrue("doc", "one"), nil, objTrue},
		{"jsonIsTrue number 1.0", d.jsonIsTrue("doc", "onef"), nil, objTrue},
		{"jsonIsTrue false", d.jsonIsTrue("doc", "fl"), nil, noneTrue},
		{"jsonIsTrue string true", d.jsonIsTrue("doc", "st"), nil, noneTrue},
		{"jsonIsTrue string 1", d.jsonIsTrue("doc", "s1"), nil, noneTrue},
		{"jsonIsTrue number 5", d.jsonIsTrue("doc", "n"), nil, noneTrue},
		{"jsonIsTrue JSON null", d.jsonIsTrue("doc", "z"), nil, noneTrue},
		{"jsonIsTrue missing", d.jsonIsTrue("doc", "missing"), nil, noneTrue},
		{"jsonHasKey present", d.jsonHasKey("doc", "s"), nil, objTrue},
		{"jsonHasKey JSON null", d.jsonHasKey("doc", "z"), nil, objTrue},
		{"jsonHasKey nested", d.jsonHasKey("doc", "nest.k"), nil, objTrue},
		{"jsonHasKey nested missing", d.jsonHasKey("doc", "nest.x"), nil, noneTrue},
		{"jsonHasKey through a scalar", d.jsonHasKey("doc", "s.k"), nil, noneTrue},
		{"jsonHasKey missing", d.jsonHasKey("doc", "missing"), nil, noneTrue},
		{"jsonAnyValueEquals match", d.jsonAnyValueEquals("doc"), []any{"x"}, objTrue},
		{"jsonAnyValueEquals nested value is not top level", d.jsonAnyValueEquals("doc"), []any{"v"}, noneTrue},
		{"jsonAnyValueEquals no match", d.jsonAnyValueEquals("doc"), []any{"nope"}, noneTrue},
	}
	for _, tc := range condTests {
		t.Run(tc.name, func(t *testing.T) {
			got := truths(t, tc.frag, tc.args...)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("%s\n got %v\nwant %v", tc.frag, got, tc.want)
			}
		})
	}

	t.Run("jsonRemove", func(t *testing.T) {
		if _, err := st.db.ExecContext(ctx,
			`UPDATE json_probe SET doc = `+d.jsonRemove("doc", "s", "nest.k")); err != nil {
			t.Fatalf("update: %v", err)
		}
		rows, err := st.db.QueryContext(ctx, `SELECT id, doc FROM json_probe`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, doc string
			if err := rows.Scan(&id, &doc); err != nil {
				t.Fatal(err)
			}
			if id != "obj" {
				if doc != docs[id] {
					t.Errorf("%s: %q changed to %q", id, docs[id], doc)
				}
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(doc), &m); err != nil {
				t.Fatalf("obj after remove is not JSON: %q: %v", doc, err)
			}
			if _, ok := m["s"]; ok {
				t.Errorf("key s survived: %s", doc)
			}
			nest, _ := m["nest"].(map[string]any)
			if _, ok := nest["k"]; ok || nest["m"] != "w" {
				t.Errorf("nest = %v, want only m", m["nest"])
			}
			if m["n"] != float64(5) || m["z"] != nil || len(m) != 11 { // 12 keys, s removed
				t.Errorf("other keys changed: %s", doc)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestPostgres_TaskTextIsSanitised: PostgreSQL TEXT cannot hold NUL bytes or
// invalid UTF-8; task output is stored with the NULs removed and invalid
// bytes replaced instead of failing the write.
func TestPostgres_TaskTextIsSanitised(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()

	task := sampleTask("sub_pg-1")
	task.Stdout = "out\x00put \xff\xfe end"
	task.Stderr = "e\x00rr"
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("get task = (%v, %v)", got, err)
	}
	if want := "output " + replacement + " end"; got.Stdout != want {
		t.Errorf("stdout = %q, want %q", got.Stdout, want)
	}
	if got.Stderr != "err" {
		t.Errorf("stderr = %q, want %q", got.Stderr, "err")
	}

	got.Stdout = "\x00again\xc3"
	if err := st.UpdateTask(ctx, got); err != nil {
		t.Fatalf("update task: %v", err)
	}
	again, err := st.GetTask(ctx, task.ID)
	if err != nil || again == nil {
		t.Fatalf("get task = (%v, %v)", again, err)
	}
	if want := "again" + replacement; again.Stdout != want {
		t.Errorf("stdout after update = %q, want %q", again.Stdout, want)
	}
}

// TestPostgres_IntegerColumnsAre64Bit: INTEGER columns are created BIGINT, so
// values beyond 32 bits are stored.
func TestPostgres_IntegerColumnsAre64Bit(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()
	big := int64(1) << 40
	task := sampleTask("sub_pg-1")
	task.StageInMs = &big
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil || got.StageInMs == nil || *got.StageInMs != big {
		t.Fatalf("get task = (%+v, %v), want stage_in_ms %d", got, err, big)
	}
}

func TestPostgres_ConstraintViolations(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()
	insert := func(id, key, value string) error {
		_, err := st.db.ExecContext(ctx,
			`INSERT INTO label_vocabulary (id, key, value, description, color, created_at) VALUES (?, ?, ?, '', '', ?)`,
			id, key, value, formatTime(time.Now()))
		return err
	}
	if err := insert("lv_1", "project", "a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tests := []struct {
		name           string
		err            error
		wantUnique     bool
		wantPK         bool
		wantConstraint string
	}{
		{"duplicate primary key", insert("lv_1", "project", "b"), true, true, "label_vocabulary_pkey"},
		{"duplicate unique constraint", insert("lv_2", "project", "a"), true, false, "label_vocabulary_key_value_key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pe *pgconn.PgError
			if !errors.As(tc.err, &pe) {
				t.Fatalf("err = %v, want a *pgconn.PgError", tc.err)
			}
			if pe.ConstraintName != tc.wantConstraint {
				t.Errorf("constraint = %q, want %q", pe.ConstraintName, tc.wantConstraint)
			}
			if got := st.dialect.isUniqueViolation(tc.err); got != tc.wantUnique {
				t.Errorf("isUniqueViolation = %v, want %v", got, tc.wantUnique)
			}
			if got := st.dialect.isPrimaryKeyViolation(tc.err); got != tc.wantPK {
				t.Errorf("isPrimaryKeyViolation = %v, want %v", got, tc.wantPK)
			}
		})
	}

	// The partial unique index on active blessings is a unique violation
	// that is not a primary key.
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_pg1", "sha256:pg")); err != nil {
		t.Fatalf("first blessing: %v", err)
	}
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_pg2", "sha256:pg")); !errors.Is(err, ErrBlessingActive) {
		t.Errorf("second active blessing: err = %v, want ErrBlessingActive", err)
	}
}

// TestPostgres_CheckoutToleratesNonObjectHints: runtime_hints that are valid
// JSON but not an object ('null', '[]') or not JSON at all never make the
// candidate query fail; such rows have no hints.
func TestPostgres_CheckoutToleratesNonObjectHints(t *testing.T) {
	st := requirePostgres(t)
	ctx := context.Background()
	mustCreateWorker(t, st, chargeWorker("wrk_pg", nil))
	for i, raw := range []string{`null`, `[]`, `[1,{"cores":1}]`, `"str"`, `7`, `{`, ``} {
		id := fmt.Sprintf("odd_%d", i)
		mustCreateTask(t, st, capTask(id, 0, 0, time.Hour))
		if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints = ? WHERE id = ?`, raw, id); err != nil {
			t.Fatalf("set hints %q: %v", raw, err)
		}
	}
	for i := 0; i < 7; i++ {
		if _, err := st.CheckoutTask(ctx, "wrk_pg", "", model.RuntimeNone); err != nil {
			t.Fatalf("checkout #%d: %v", i+1, err)
		}
	}
}
