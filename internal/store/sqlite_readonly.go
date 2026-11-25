package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"time"
)

// OpenSQLiteReadOnly opens the existing SQLite database file at path strictly
// read-only, through a file: URI with mode=ro: SQLite then neither creates the
// file nor writes to it. No PRAGMA is set and Migrate must not be called (it
// would fail): the store is only for reading, for example as the source of
// CopyDatabase.
func OpenSQLiteReadOnly(path string, logger *slog.Logger) (*SQLStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite path %s: %w", path, err)
	}
	// url.URL escapes characters that mean something in a URI (?, #, %), so
	// any file name reaches SQLite unchanged.
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s read-only: %w", abs, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	// sql.Open does not connect; a missing or unreadable file shows here.
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %s read-only: %w", abs, err)
	}

	d := sqliteDialect{}
	return &SQLStore{
		db:      &sqlDB{db: db, d: d},
		dialect: d,
		logger:  logger.With("component", "store"),
	}, nil
}
