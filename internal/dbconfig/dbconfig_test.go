package dbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake secrets used by these tests. They appear in no test name, so no
// temporary path (which holds the test name) can contain them.
const (
	testFilePW = "Kx7pW2-file"
	testURLPW  = "Qz4mR8-url"
	testEnvPW  = "Vn3tY6-env"
)

// clearPGEnv unsets the refused PG* variables for the test, so the
// environment of the test run cannot change the outcome.
func clearPGEnv(t *testing.T) {
	t.Helper()
	for _, name := range pgRefusedEnv {
		t.Setenv(name, "")
	}
}

// writeConfig writes content to a config file with the given mode in a
// fresh temporary directory and returns its path.
func writeConfig(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// WriteFile's mode is filtered by the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	return path
}

func TestSelectDatabase(t *testing.T) {
	const (
		pgURL      = "postgres://gowe@127.0.0.1:5433/gowe"
		pgsqlURL   = "postgresql://gowe@127.0.0.1:5433/gowe"
		otherPGURL = "postgres://gowe@db.example:5432/other"
	)
	fullCfg := `{"admins": ["alice"], "database": {"url": "` + otherPGURL + `", "password": "` + testFilePW + `", "schema": "gowe"}}`

	tests := []struct {
		name       string
		db         string
		config     string // file content; "" = no --config
		mode       os.FileMode
		wantBack   string
		wantPath   string // SQLite
		wantURL    string // PostgreSQL
		wantPW     string // PostgreSQL
		wantSchema string // PostgreSQL
		wantUnused bool   // SQLite: database.password unused
	}{
		{name: "sqlite path", db: "/var/lib/gowe/gowe.db", wantBack: BackendSQLite, wantPath: "/var/lib/gowe/gowe.db"},
		{name: "sqlite memory", db: ":memory:", wantBack: BackendSQLite, wantPath: ":memory:"},
		{name: "sqlite default", db: "", wantBack: BackendSQLite, wantPath: ""},
		{name: "postgres URL in --db", db: pgURL, wantBack: BackendPostgres, wantURL: pgURL},
		{name: "postgresql URL in --db", db: pgsqlURL, wantBack: BackendPostgres, wantURL: pgsqlURL},
		{name: "URL from config", config: fullCfg, mode: 0o600,
			wantBack: BackendPostgres, wantURL: otherPGURL, wantPW: testFilePW, wantSchema: "gowe"},
		{name: "postgres --db wins over config url", db: pgURL, config: fullCfg, mode: 0o600,
			wantBack: BackendPostgres, wantURL: pgURL, wantPW: testFilePW, wantSchema: "gowe"},
		{name: "sqlite --db wins over config url", db: "/data/gowe.db", config: fullCfg, mode: 0o600,
			wantBack: BackendSQLite, wantPath: "/data/gowe.db", wantUnused: true},
		{name: "config 0400 with password accepted", config: fullCfg, mode: 0o400,
			wantBack: BackendPostgres, wantURL: otherPGURL, wantPW: testFilePW, wantSchema: "gowe"},
		{name: "password only, --db URL", db: pgsqlURL, config: `{"database": {"password": "` + testFilePW + `"}}`, mode: 0o600,
			wantBack: BackendPostgres, wantURL: pgsqlURL, wantPW: testFilePW},
		{name: "password with sqlite is unused", db: ":memory:", config: `{"database": {"password": "` + testFilePW + `"}}`, mode: 0o600,
			wantBack: BackendSQLite, wantPath: ":memory:", wantUnused: true},
		{name: "config without database block", config: `{"admins": ["alice"]}`, mode: 0o644,
			wantBack: BackendSQLite, wantPath: ""},
		{name: "config with database null", config: `{"database": null}`, mode: 0o644,
			wantBack: BackendSQLite, wantPath: ""},
		{name: "other top-level keys and ill-typed admins are not checked", config: `{"admins": "alice", "x": 1}`, mode: 0o644,
			wantBack: BackendSQLite, wantPath: ""},
		{name: "config 0644 without password", db: "", config: `{"database": {"url": "` + pgURL + `"}}`, mode: 0o644,
			wantBack: BackendPostgres, wantURL: pgURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearPGEnv(t)
			var configPath string
			if tc.config != "" {
				configPath = writeConfig(t, tc.config, tc.mode)
			}
			sel, err := Select(tc.db, configPath, os.Getenv)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if sel.Backend != tc.wantBack {
				t.Fatalf("backend = %s, want %s", sel.Backend, tc.wantBack)
			}
			if sel.UnusedPassword != tc.wantUnused {
				t.Errorf("UnusedPassword = %v, want %v", sel.UnusedPassword, tc.wantUnused)
			}
			if tc.wantBack == BackendSQLite {
				if sel.SQLitePath != tc.wantPath {
					t.Errorf("SQLite path = %q, want %q", sel.SQLitePath, tc.wantPath)
				}
				return
			}
			pg := sel.Postgres
			if pg.URL != tc.wantURL {
				t.Errorf("URL = %q, want %q", pg.URL, tc.wantURL)
			}
			if pg.Password != tc.wantPW {
				t.Errorf("password differs from the expected one")
			}
			if pg.Schema != tc.wantSchema {
				t.Errorf("schema = %q, want %q", pg.Schema, tc.wantSchema)
			}
			if !pg.PasswordOnlyFromConfig {
				t.Error("PasswordOnlyFromConfig = false, want true")
			}
		})
	}
}

func TestSelectDatabase_Refusals(t *testing.T) {
	const pgURL = "postgres://gowe@127.0.0.1:5433/gowe"
	urlUserPW := "postgres://gowe:" + testURLPW + "@127.0.0.1:5433/gowe"
	urlQueryPW := "postgres://gowe@127.0.0.1:5433/gowe?sslmode=disable&password=" + testURLPW
	pwBlock := `{"database": {"password": "` + testFilePW + `"}}`

	tests := []struct {
		name    string
		db      string
		config  string // file content; "" = no --config
		mode    os.FileMode
		noFile  bool              // --config names a file that does not exist
		env     map[string]string // set for the test
		want    []string          // substrings the error must contain
		secrets []string          // substrings the error must not contain
	}{
		{name: "user info password in --db", db: urlUserPW,
			want: []string{"--db", "contains a password", "database.password", "--config"}, secrets: []string{testURLPW, urlUserPW}},
		{name: "empty user info password in --db", db: "postgres://gowe:@127.0.0.1/gowe",
			want: []string{"contains a password"}},
		{name: "query password in --db", db: urlQueryPW,
			want: []string{"--db", "password= parameter", "database.password"}, secrets: []string{testURLPW, urlQueryPW}},
		{name: "passfile parameter in --db", db: "postgres://gowe@127.0.0.1/gowe?passfile=/tmp/pgpass",
			want: []string{"--db", "passfile= parameter"}},
		{name: "service parameter in --db", db: "postgres://gowe@127.0.0.1/gowe?service=prod",
			want: []string{"--db", "service= parameter"}},
		{name: "servicefile parameter in config url", config: `{"database": {"url": "postgres://gowe@127.0.0.1/gowe?servicefile=/tmp/s"}}`, mode: 0o600,
			want: []string{"database.url", "servicefile= parameter"}},
		{name: "user info password in config url", config: `{"database": {"url": "` + urlUserPW + `"}}`, mode: 0o600,
			want: []string{"database.url", "contains a password", "database.password"}, secrets: []string{testURLPW, urlUserPW}},
		{name: "query password in config url", config: `{"database": {"url": "` + urlQueryPW + `"}}`, mode: 0o600,
			want: []string{"database.url", "password= parameter"}, secrets: []string{testURLPW, urlQueryPW}},
		{name: "password in config url refused even when --db wins", db: ":memory:", config: `{"database": {"url": "` + urlUserPW + `"}}`, mode: 0o600,
			want: []string{"database.url", "contains a password"}, secrets: []string{testURLPW}},
		{name: "config url not postgres", config: `{"database": {"url": "/var/lib/gowe/gowe.db"}}`, mode: 0o600,
			want: []string{"database.url", "postgres://", "--db"}},
		{name: "config url sqlite scheme", config: `{"database": {"url": "sqlite:///gowe.db"}}`, mode: 0o600,
			want: []string{"database.url", "--db"}},
		{name: "invalid URL in --db", db: "postgres://gowe:" + testURLPW + "@127.0.0.1:5433/%zz",
			want: []string{"not a valid URL"}, secrets: []string{testURLPW}},

		{name: "PGPASSWORD", db: pgURL, env: map[string]string{"PGPASSWORD": testEnvPW},
			want: []string{"PGPASSWORD", "database.password"}, secrets: []string{testEnvPW}},
		{name: "PGPASSFILE", db: pgURL, env: map[string]string{"PGPASSFILE": "/home/u/" + testEnvPW},
			want: []string{"PGPASSFILE"}, secrets: []string{testEnvPW}},
		{name: "PGSERVICE", db: pgURL, env: map[string]string{"PGSERVICE": testEnvPW},
			want: []string{"PGSERVICE"}, secrets: []string{testEnvPW}},
		{name: "PGSERVICEFILE", db: pgURL, env: map[string]string{"PGSERVICEFILE": testEnvPW},
			want: []string{"PGSERVICEFILE"}, secrets: []string{testEnvPW}},
		{name: "PGOPTIONS", db: pgURL, env: map[string]string{"PGOPTIONS": "-c search_path=" + testEnvPW},
			want: []string{"PGOPTIONS"}, secrets: []string{testEnvPW}},
		{name: "PGHOST redirects the connection", db: pgURL, env: map[string]string{"PGHOST": "db.other.example"},
			want: []string{"PGHOST"}, secrets: []string{"db.other.example"}},
		{name: "PGSSLMODE", db: pgURL, env: map[string]string{"PGSSLMODE": "disable"},
			want: []string{"PGSSLMODE"}},
		{name: "PGSSLPASSWORD", db: pgURL, env: map[string]string{"PGSSLPASSWORD": testEnvPW},
			want: []string{"PGSSLPASSWORD"}, secrets: []string{testEnvPW}},
		{name: "sslpassword parameter in --db", db: "postgres://gowe@127.0.0.1/gowe?sslpassword=" + testURLPW,
			want: []string{"--db", "sslpassword= parameter"}, secrets: []string{testURLPW}},
		{name: "mistyped scheme is not taken for a file", db: "Postgres://gowe:" + testURLPW + "@127.0.0.1/gowe",
			want: []string{"--db", "postgres://"}, secrets: []string{testURLPW}},
		{name: "scheme without slashes is not taken for a file", db: " postgresql:gowe:" + testURLPW + "@127.0.0.1/gowe",
			want: []string{"--db", "postgres://"}, secrets: []string{testURLPW}},
		{name: "several variables named", db: pgURL, env: map[string]string{"PGPASSWORD": testEnvPW, "PGOPTIONS": testEnvPW},
			want: []string{"PGPASSWORD, PGOPTIONS"}, secrets: []string{testEnvPW}},
		{name: "env with URL from config", config: `{"database": {"url": "` + pgURL + `"}}`, mode: 0o600,
			env: map[string]string{"PGPASSWORD": testEnvPW}, want: []string{"PGPASSWORD"}, secrets: []string{testEnvPW}},

		{name: "config 0644 with password", db: pgURL, config: pwBlock, mode: 0o644,
			want: []string{"mode 0644", "chmod 600 "}, secrets: []string{testFilePW}},
		{name: "config 0640 with password", db: pgURL, config: pwBlock, mode: 0o640,
			want: []string{"mode 0640", "chmod 600 "}, secrets: []string{testFilePW}},
		{name: "config 0604 with password and sqlite", db: ":memory:", config: pwBlock, mode: 0o604,
			want: []string{"chmod 600 "}, secrets: []string{testFilePW}},

		{name: "unknown field in database", config: `{"database": {"url": "` + pgURL + `", "passwd": "` + testFilePW + `"}}`, mode: 0o600,
			want: []string{`unknown field "passwd"`, "database"}, secrets: []string{testFilePW}},
		{name: "ill-typed password", config: `{"database": {"password": 918273645}}`, mode: 0o600,
			want: []string{"database.password", "string"}, secrets: []string{"918273645"}},
		{name: "ill-typed url", config: `{"database": {"url": ["` + pgURL + `"]}}`, mode: 0o600,
			want: []string{"database.url"}},
		{name: "database not an object", config: `{"database": "` + pgURL + `"}`, mode: 0o600,
			want: []string{"database must be a JSON object"}, secrets: []string{pgURL}},
		{name: "malformed JSON", config: `{"admins": ["alice"`, mode: 0o600,
			want: []string{"not valid JSON"}},
		{name: "malformed JSON inside the password", config: `{"database": {"password": "` + testFilePW + `\q"}}`, mode: 0o600,
			want: []string{"not valid JSON"}, secrets: []string{testFilePW, "q"}},
		{name: "top level not an object", config: `["alice"]`, mode: 0o600,
			want: []string{"top level must be a JSON object"}},
		{name: "config file missing", noFile: true,
			want: []string{"config file"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearPGEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var configPath string
			switch {
			case tc.noFile:
				configPath = filepath.Join(t.TempDir(), "missing.json")
			case tc.config != "":
				configPath = writeConfig(t, tc.config, tc.mode)
			}
			sel, err := Select(tc.db, configPath, os.Getenv)
			if err == nil {
				t.Fatalf("Select = %+v, want an error", sel.Backend)
			}
			msg := err.Error()
			// The path may legitimately hold short substrings ("q"): check
			// secrets in the message without it.
			bare := msg
			if configPath != "" {
				bare = strings.ReplaceAll(bare, configPath, "<config>")
			}
			for _, s := range tc.secrets {
				if strings.Contains(bare, s) {
					t.Errorf("error leaks a secret or the URL (%d bytes)", len(s))
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error does not contain %q:\n%s", w, bare)
				}
			}
		})
	}
}

// TestSelectFor_FlagName: a program whose database flag has another name
// (gowe-db-copy's --to) gets errors that name its flag, never --db.
func TestSelectFor_FlagName(t *testing.T) {
	const pgURL = "postgres://gowe@127.0.0.1:5433/gowe"
	tests := []struct {
		name string
		db   string
		env  map[string]string
		want string
	}{
		{name: "password in URL", db: "postgres://gowe:" + testURLPW + "@127.0.0.1/gowe", want: "the PostgreSQL URL from --to contains a password"},
		{name: "mistyped scheme", db: "Postgres://gowe@127.0.0.1/gowe", want: "--to looks like a URL"},
		{name: "PG variable", db: pgURL, env: map[string]string{"PGPASSWORD": testEnvPW}, want: "in --to or database.url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearPGEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := SelectFor("--to", tc.db, "", os.Getenv)
			if err == nil {
				t.Fatal("SelectFor succeeded, want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error does not contain %q: %s", tc.want, msg)
			}
			if strings.Contains(msg, "--db") {
				t.Errorf("error names --db: %s", msg)
			}
			if strings.Contains(msg, testURLPW) || strings.Contains(msg, testEnvPW) {
				t.Error("error leaks a secret")
			}
		})
	}
}

// TestSelectDatabase_EnvIgnoredForSQLite: the PG* variables only matter when
// PostgreSQL is selected.
func TestSelectDatabase_EnvIgnoredForSQLite(t *testing.T) {
	for _, name := range pgRefusedEnv {
		t.Run(name, func(t *testing.T) {
			clearPGEnv(t)
			t.Setenv(name, testEnvPW)
			sel, err := Select(":memory:", "", os.Getenv)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if sel.Backend != BackendSQLite || sel.SQLitePath != ":memory:" {
				t.Errorf("selection = %s %q, want sqlite :memory:", sel.Backend, sel.SQLitePath)
			}
		})
	}
}

func TestResolveSQLitePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want func(home string) string
	}{
		{name: "path kept", in: "/data/gowe.db", want: func(string) string { return "/data/gowe.db" }},
		{name: "memory kept", in: ":memory:", want: func(string) string { return ":memory:" }},
		{name: "empty is the default", in: "", want: func(home string) string { return filepath.Join(home, ".gowe", "gowe.db") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			got, err := ResolveSQLitePath(tc.in)
			if err != nil {
				t.Fatalf("ResolveSQLitePath: %v", err)
			}
			if want := tc.want(home); got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
			_, statErr := os.Stat(filepath.Join(home, ".gowe"))
			if created := statErr == nil; created != (tc.in == "") {
				t.Errorf("~/.gowe created = %v, want %v", created, tc.in == "")
			}
		})
	}
}

// TestSelectDatabase_UnusedURL: --db with a SQLite path wins over
// database.url, and the selection says so, so the caller can warn.
func TestSelectDatabase_UnusedURL(t *testing.T) {
	clearPGEnv(t)
	cfg := writeConfig(t, `{"database": {"url": "postgres://gowe@127.0.0.1:5433/gowe"}}`, 0o600)
	sel, err := Select("/var/lib/gowe/gowe.db", cfg, os.Getenv)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.Backend != BackendSQLite || !sel.UnusedURL || sel.UnusedPassword {
		t.Errorf("selection = %+v, want SQLite with UnusedURL only", sel)
	}
}

// TestSelectDatabase_ConfigNotRegularFile: a directory (or FIFO, device) at
// --config is refused instead of being read.
func TestSelectDatabase_ConfigNotRegularFile(t *testing.T) {
	clearPGEnv(t)
	_, err := Select(":memory:", t.TempDir(), os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file error", err)
	}
}

// TestSelectDatabase_SQLiteFileURI: a file: URI is a SQLite name, not a
// mistyped PostgreSQL URL.
func TestSelectDatabase_SQLiteFileURI(t *testing.T) {
	clearPGEnv(t)
	sel, err := Select("file:///var/lib/gowe/gowe.db?mode=rwc", "", os.Getenv)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.Backend != BackendSQLite {
		t.Errorf("backend = %s, want sqlite", sel.Backend)
	}
}
