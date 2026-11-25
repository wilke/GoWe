package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresConfig selects a PostgreSQL database.
type PostgresConfig struct {
	URL      string // postgres:// or postgresql:// URL, or a key=value DSN
	Password string // overrides any password in URL; empty = none (e.g. Unix socket, trust auth)
	Schema   string // optional: schema to work in (sets search_path); "" = the role's default
}

// minPostgresVersionNum is the oldest server_version_num the store accepts:
// the JSON helpers use the IS JSON predicate of PostgreSQL 16.
const minPostgresVersionNum = 160000

// defaultPostgresConnectTimeout applies when the connection string sets no
// connect_timeout.
const defaultPostgresConnectTimeout = 10 * time.Second

// NewPostgresStore opens the PostgreSQL database cfg selects and checks that
// the server can hold the store (version, encoding, collation). Like the
// SQLite store it uses a single connection. Call Migrate before use.
//
// Neither the password nor the URL is ever logged or put into an error; only
// host, port and database are.
func NewPostgresStore(ctx context.Context, cfg PostgresConfig, logger *slog.Logger) (*SQLStore, error) {
	secrets := postgresSecrets(cfg)

	// An empty string would make the driver build the whole configuration
	// from PG* environment variables and connect wherever they point.
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("postgres connection string is empty")
	}
	connCfg, err := pgx.ParseConfig(cfg.URL)
	if err != nil {
		// The parse error quotes fragments of the connection string, which
		// can be part of a password: report nothing of it.
		return nil, errors.New("parse postgres connection string: invalid connection string")
	}
	if connCfg.ConnectTimeout == 0 {
		// The store has one connection: without a timeout an unreachable
		// server would block every store call until TCP gives up.
		connCfg.ConnectTimeout = defaultPostgresConnectTimeout
	}
	if connCfg.Password != "" {
		secrets = append(secrets, connCfg.Password)
	}
	if cfg.Password != "" {
		connCfg.Password = cfg.Password
	}
	if cfg.Schema != "" {
		if connCfg.RuntimeParams == nil {
			connCfg.RuntimeParams = map[string]string{}
		}
		connCfg.RuntimeParams["search_path"] = cfg.Schema
	}
	where := fmt.Sprintf("host=%s port=%d database=%s", connCfg.Host, connCfg.Port, connCfg.Database)

	db := stdlib.OpenDB(*connCfg)
	// One connection, as for SQLite: the store's transactions and its
	// CheckoutTask rely on statements never running concurrently.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	if err := checkPostgresServer(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres %s: %w", where, safeError(err, secrets))
	}

	// The schema is logged because it is where the tables live: with no
	// explicit Schema it follows the role's search_path and can change.
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres %s: read current schema: %w", where, safeError(err, secrets))
	}

	logger = logger.With("component", "store")
	logger.Info("opened postgres store", "host", connCfg.Host, "port", connCfg.Port, "database", connCfg.Database, "schema", schema)

	d := postgresDialect{}
	return &SQLStore{
		db:      &sqlDB{db: db, d: d},
		dialect: d,
		logger:  logger,
	}, nil
}

// checkPostgresServer connects and checks what the store relies on: version
// 16 or newer, UTF8 encoding, byte-order (C) collation, and a schema to
// create the tables in.
func checkPostgresServer(ctx context.Context, db *sql.DB) error {
	var verNum string
	if err := db.QueryRowContext(ctx, `SHOW server_version_num`).Scan(&verNum); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(verNum))
	if err != nil {
		return fmt.Errorf("read server_version_num %q: %w", verNum, err)
	}
	if n < minPostgresVersionNum {
		return fmt.Errorf("PostgreSQL 16 or newer is required, the server is %d (server_version_num): the JSON predicates use IS JSON", n)
	}

	var encoding string
	if err := db.QueryRowContext(ctx, `SHOW server_encoding`).Scan(&encoding); err != nil {
		return fmt.Errorf("read server_encoding: %w", err)
	}
	if encoding != "UTF8" {
		return fmt.Errorf("the database encoding must be UTF8, not %s", encoding)
	}

	var byteOrder bool
	if err := db.QueryRowContext(ctx, `SELECT 'B' < 'a'`).Scan(&byteOrder); err != nil {
		return fmt.Errorf("check collation: %w", err)
	}
	if !byteOrder {
		return errors.New("the database must use the C collation (timestamps and names are TEXT, compared and sorted as text, " +
			"which is only correct in byte order): create it with --locale=C or " +
			"`CREATE DATABASE ... TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'`")
	}

	var schema sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("read current schema: %w", err)
	}
	if !schema.Valid {
		return errors.New("no schema of the search_path exists: create the schema first")
	}
	return nil
}

// postgresSecrets lists the strings that must never appear in a message: the
// configured password, the URL, and the password embedded in the URL.
func postgresSecrets(cfg PostgresConfig) []string {
	var secrets []string
	if cfg.Password != "" {
		secrets = append(secrets, cfg.Password)
	}
	if cfg.URL != "" {
		secrets = append(secrets, cfg.URL)
	}
	if u, err := url.Parse(cfg.URL); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			secrets = append(secrets, pw)
			// The URL may carry it percent-encoded.
			if esc := url.QueryEscape(pw); esc != pw {
				secrets = append(secrets, esc)
			}
		}
	}
	return secrets
}

// redactSecrets replaces every occurrence of each secret in msg, longest
// first so that a secret contained in another (the password in the URL) does
// not break the longer match.
func redactSecrets(msg string, secrets []string) string {
	sorted := append([]string(nil), secrets...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, s := range sorted {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}

// safeError returns err itself when its message holds no secret, else an
// error with the redacted message (the chain is dropped: it holds the
// secret too).
func safeError(err error, secrets []string) error {
	msg := err.Error()
	if red := redactSecrets(msg, secrets); red != msg {
		return errors.New(red)
	}
	return err
}
