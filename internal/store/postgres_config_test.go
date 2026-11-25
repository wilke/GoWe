package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestPostgresConnConfig_Password checks which password the driver
// configuration ends up with, with and without PasswordOnlyFromConfig. No
// database is needed: the configuration is built without connecting.
func TestPostgresConnConfig_Password(t *testing.T) {
	const (
		urlWithPW = "postgres://gowe:url-pw@127.0.0.1:5433/gowe"
		urlNoPW   = "postgres://gowe@127.0.0.1:5433/gowe"
	)
	tests := []struct {
		name       string
		cfg        PostgresConfig
		pgpassword string // PGPASSWORD
		passfile   bool   // PGPASSFILE names a file matching every connection
		want       string
	}{
		{name: "default: URL password kept", cfg: PostgresConfig{URL: urlWithPW}, want: "url-pw"},
		{name: "default: config password overrides URL", cfg: PostgresConfig{URL: urlWithPW, Password: "cfg-pw"}, want: "cfg-pw"},
		{name: "default: PGPASSWORD used", cfg: PostgresConfig{URL: urlNoPW}, pgpassword: "env-pw", want: "env-pw"},
		{name: "default: passfile used", cfg: PostgresConfig{URL: urlNoPW}, passfile: true, want: "file-pw"},

		{name: "only config: URL password discarded", cfg: PostgresConfig{URL: urlWithPW, PasswordOnlyFromConfig: true}, want: ""},
		{name: "only config: config password over URL", cfg: PostgresConfig{URL: urlWithPW, Password: "cfg-pw", PasswordOnlyFromConfig: true}, want: "cfg-pw"},
		{name: "only config: PGPASSWORD discarded", cfg: PostgresConfig{URL: urlNoPW, PasswordOnlyFromConfig: true}, pgpassword: "env-pw", want: ""},
		{name: "only config: passfile discarded", cfg: PostgresConfig{URL: urlNoPW, PasswordOnlyFromConfig: true}, passfile: true, want: ""},
		{name: "only config: config password over passfile", cfg: PostgresConfig{URL: urlNoPW, Password: "cfg-pw", PasswordOnlyFromConfig: true}, passfile: true, want: "cfg-pw"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			isolatePGEnv(t)
			t.Setenv("PGPASSWORD", tc.pgpassword)
			passfile := filepath.Join(dir, "no-such-pgpass")
			if tc.passfile {
				passfile = filepath.Join(dir, "pgpass")
				if err := os.WriteFile(passfile, []byte("*:*:*:*:file-pw\n"), 0o600); err != nil {
					t.Fatalf("write passfile: %v", err)
				}
			}
			t.Setenv("PGPASSFILE", passfile)

			connCfg, secrets, err := postgresConnConfig(tc.cfg)
			if err != nil {
				t.Fatalf("postgresConnConfig: %v", err)
			}
			if connCfg.Password != tc.want {
				t.Errorf("password = %q, want %q", connCfg.Password, tc.want)
			}
			// A password the driver found stays a secret to redact, even when
			// it is discarded.
			if tc.cfg.URL == urlWithPW && !slices.Contains(secrets, "url-pw") {
				t.Errorf("secrets do not list the URL password")
			}
		})
	}
}

// isolatePGEnv keeps the developer's environment out of a test that parses a
// connection string: the driver reads these variables, ~/.pgpass and the
// client certificates under the home directory on its own.
func isolatePGEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
		"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT",
		"PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
		"PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir())
}

func TestPostgresConnConfig_SchemaAndTimeout(t *testing.T) {
	isolatePGEnv(t)
	connCfg, _, err := postgresConnConfig(PostgresConfig{URL: "postgres://gowe@127.0.0.1:5433/gowe", Schema: "gowe_s", PasswordOnlyFromConfig: true})
	if err != nil {
		t.Fatalf("postgresConnConfig: %v", err)
	}
	if got := connCfg.RuntimeParams["search_path"]; got != "gowe_s" {
		t.Errorf("search_path = %q, want gowe_s", got)
	}
	if connCfg.ConnectTimeout != defaultPostgresConnectTimeout {
		t.Errorf("connect timeout = %v, want %v", connCfg.ConnectTimeout, defaultPostgresConnectTimeout)
	}
	if connCfg.Host != "127.0.0.1" || connCfg.Port != 5433 || connCfg.Database != "gowe" || connCfg.User != "gowe" {
		t.Errorf("host/port/database/user = %s/%d/%s/%s", connCfg.Host, connCfg.Port, connCfg.Database, connCfg.User)
	}

	if _, _, err := postgresConnConfig(PostgresConfig{URL: " ", PasswordOnlyFromConfig: true}); err == nil {
		t.Error("postgresConnConfig accepted an empty connection string")
	}
}
