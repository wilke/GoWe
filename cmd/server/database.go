package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/me/gowe/internal/store"
)

// Database backends the server can run on.
const (
	backendSQLite   = "sqlite"
	backendPostgres = "postgres"
)

// pgRefusedEnv lists the environment variables refused when PostgreSQL is
// selected: the driver reads them on its own, and they would silently supply
// a password or change where and how it connects.
var pgRefusedEnv = []string{
	"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
	"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT",
	"PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
	"PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS",
}

// passwordHint names the one place a database password may come from.
// maxConfigFileSize bounds how much of the --config file is read.
const maxConfigFileSize = 1 << 20

const passwordHint = "put the password in database.password in the --config file (regular file, mode 0600)"

// databaseFileConfig is the "database" block of the --config file.
type databaseFileConfig struct {
	URL      string `json:"url"`      // postgres:// or postgresql:// URL, without a password
	Password string `json:"password"` // PostgreSQL password; requires the file to be mode 0600 or stricter
	Schema   string `json:"schema"`   // optional PostgreSQL schema (store.PostgresConfig.Schema)
}

// dbSelection is the database the server opens.
type dbSelection struct {
	Backend string // backendSQLite or backendPostgres

	// SQLitePath is the --db value for SQLite, unchanged: a file path,
	// ":memory:", or "" for the default ~/.gowe/gowe.db (see resolveSQLitePath).
	SQLitePath string

	// Postgres is the store configuration for PostgreSQL.
	Postgres store.PostgresConfig

	// UnusedPassword is set when SQLite is selected but the config file
	// holds a database.password: not an error (the file may be shared by
	// several servers), the caller logs a warning.
	UnusedPassword bool

	// UnusedURL: SQLite was selected by --db although the config file
	// gives a database.url. The caller warns: the URL is not used.
	UnusedURL bool
}

// isPostgresURL reports whether s selects PostgreSQL.
// looksLikeURL reports whether s is more likely a mistyped database URL than
// a file path.
func looksLikeURL(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(t, "postgres:") || strings.HasPrefix(t, "postgresql:")
}

func isPostgresURL(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

// selectDatabase decides which database the server opens from the --db value
// and the "database" block of the --config file (configPath, "" = none). A
// non-empty --db wins over database.url. getenv is injected for testing.
//
// No error it returns contains the URL or the password.
func selectDatabase(dbFlag, configPath string, getenv func(string) string) (*dbSelection, error) {
	fileCfg, err := readDatabaseConfig(configPath)
	if err != nil {
		return nil, err
	}

	if fileCfg.URL != "" {
		if !isPostgresURL(fileCfg.URL) {
			return nil, fmt.Errorf("database.url in %s must start with postgres:// or postgresql:// (SQLite is configured with --db)", configPath)
		}
		if err := checkURLHasNoPassword(fileCfg.URL, "database.url in "+configPath); err != nil {
			return nil, err
		}
	}

	// A value that looks like a URL but is not an exact postgres:// or
	// postgresql:// prefix would be taken for a SQLite file name, and the
	// open error would then print it, password included.
	if !isPostgresURL(dbFlag) && looksLikeURL(dbFlag) {
		return nil, errors.New("--db looks like a URL but does not start with postgres:// or postgresql:// (lower case, no leading space); a SQLite database is given as a file path")
	}

	var pgURL, source string
	switch {
	case dbFlag != "":
		if isPostgresURL(dbFlag) {
			pgURL, source = dbFlag, "--db"
		}
	case fileCfg.URL != "":
		pgURL, source = fileCfg.URL, "database.url in "+configPath
	}

	if pgURL == "" {
		return &dbSelection{
			Backend:        backendSQLite,
			SQLitePath:     dbFlag,
			UnusedPassword: fileCfg.Password != "",
			UnusedURL:      fileCfg.URL != "",
		}, nil
	}

	if err := checkURLHasNoPassword(pgURL, source); err != nil {
		return nil, err
	}
	if err := checkPostgresEnv(getenv); err != nil {
		return nil, err
	}
	return &dbSelection{
		Backend: backendPostgres,
		Postgres: store.PostgresConfig{
			URL:                    pgURL,
			Password:               fileCfg.Password,
			Schema:                 fileCfg.Schema,
			PasswordOnlyFromConfig: true,
		},
	}, nil
}

// readDatabaseConfig reads the "database" block of the --config file at path
// strictly: an unreadable file, invalid JSON, or an unknown or ill-typed field
// in the block is an error. Other top-level keys are left to their own
// readers. No path, or no block, yields the zero value. When the block holds
// a non-empty password, the file must be a regular file with no permission
// bits for group or others.
//
// JSON errors are reported by position and field name only: the decoder's
// own messages can quote characters or numbers from the file, which may be
// part of the password.
func readDatabaseConfig(path string) (databaseFileConfig, error) {
	var dc databaseFileConfig
	if path == "" {
		return dc, nil
	}

	// Stat the opened file, not the path, so the mode checked is the mode of
	// the bytes read.
	// O_NONBLOCK so a FIFO at this path cannot block start-up; the mode is
	// then read from the open descriptor, the file the bytes come from.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return dc, fmt.Errorf("config file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return dc, fmt.Errorf("config file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return dc, fmt.Errorf("config file %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return dc, fmt.Errorf("read config file %s: %w", path, err)
	}
	if len(data) > maxConfigFileSize {
		return dc, fmt.Errorf("config file %s is larger than %d bytes", path, maxConfigFileSize)
	}

	var top struct {
		Database json.RawMessage `json:"database"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return dc, jsonError(path, "", err)
	}
	if len(top.Database) == 0 || string(top.Database) == "null" {
		return dc, nil
	}

	dec := json.NewDecoder(bytes.NewReader(top.Database))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dc); err != nil {
		return databaseFileConfig{}, jsonError(path, "database", err)
	}

	if dc.Password != "" {
		if err := checkSecretFileMode(path, fi); err != nil {
			return databaseFileConfig{}, err
		}
	}
	return dc, nil
}

// jsonError turns a decoding error of the config file into a message that
// holds no value from the file. block names the object being decoded ("" for
// the top level).
func jsonError(path, block string, err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Errorf("config file %s is not valid JSON (error at byte %d)", path, syn.Offset)
	case errors.As(err, &typ):
		field := block
		if typ.Field != "" {
			if field != "" {
				field += "."
			}
			field += typ.Field
		}
		if field == "" {
			return fmt.Errorf("config file %s: the top level must be a JSON object", path)
		}
		if typ.Field == "" {
			return fmt.Errorf("config file %s: %s must be a JSON object", path, field)
		}
		return fmt.Errorf("config file %s: %s must be a %s", path, field, typ.Type)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return fmt.Errorf("config file %s is not valid JSON (unexpected end of input)", path)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// The message quotes the field name, never a value.
		name := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return fmt.Errorf("config file %s: unknown field %s in %s (allowed: url, password, schema)", path, name, block)
	default:
		return fmt.Errorf("config file %s: invalid %s", path, orTopLevel(block))
	}
}

func orTopLevel(block string) string {
	if block == "" {
		return "JSON"
	}
	return block + " block"
}

// checkSecretFileMode refuses a config file holding a password unless it is a
// regular file with no permission bits for group or others (mode 0600 or
// stricter). Same checks as the worker key file (cmd/worker/workerkey.go).
func checkSecretFileMode(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("config file %s holds database.password but is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("config file %s holds database.password and has mode %04o; it must be 0600 or stricter (chmod 600 %s)", path, perm, path)
	}
	return nil
}

// checkURLHasNoPassword refuses a PostgreSQL URL that carries a password,
// in the user info or as a password= query parameter. source names where the
// URL came from. Neither the URL nor the password is put in the error.
func checkURLHasNoPassword(rawURL, source string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse quotes the URL in its error: report nothing of it.
		return fmt.Errorf("the PostgreSQL URL from %s is not a valid URL", source)
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			return fmt.Errorf("the PostgreSQL URL from %s contains a password: remove it from the URL and %s", source, passwordHint)
		}
	}
	if u.Query().Has("password") {
		return fmt.Errorf("the PostgreSQL URL from %s has a password= parameter: remove it from the URL and %s", source, passwordHint)
	}
	// These make the driver read the connection or the password from
	// another file, which would bypass the rules above.
	for _, param := range []string{"passfile", "service", "servicefile", "sslpassword"} {
		if u.Query().Has(param) {
			return fmt.Errorf("the PostgreSQL URL from %s has a %s= parameter, which is not supported: give host, port, database and user in the URL and %s", source, param, passwordHint)
		}
	}
	return nil
}

// checkPostgresEnv refuses the PG* variables the driver would read on its
// own. Only the names are reported, never a value.
func checkPostgresEnv(getenv func(string) string) error {
	var set []string
	for _, name := range pgRefusedEnv {
		if getenv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("environment variable(s) %s must not be set when the server uses PostgreSQL: "+
		"the driver reads them on its own and they would silently supply a password or change where and how the server connects, "+
		"and the environment is readable by other processes of the same user; unset them, "+
		"give the whole connection (host, port, database, user, sslmode) in --db or database.url, and %s", strings.Join(set, ", "), passwordHint)
}

// resolveSQLitePath returns the SQLite database path: dbPath itself, or for
// "" ~/.gowe/gowe.db (creating ~/.gowe).
func resolveSQLitePath(dbPath string) (string, error) {
	if dbPath != "" {
		return dbPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".gowe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	return filepath.Join(dir, "gowe.db"), nil
}

// openStore opens the selected database. For SQLite, sqlitePath is the
// resolved path (see resolveSQLitePath).
func openStore(ctx context.Context, sel *dbSelection, sqlitePath string, logger *slog.Logger) (*store.SQLStore, error) {
	if sel.Backend == backendPostgres {
		return store.NewPostgresStore(ctx, sel.Postgres, logger)
	}
	return store.NewSQLiteStore(sqlitePath, logger)
}
