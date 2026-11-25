package store

import (
	"context"
	"database/sql"
)

// sqlDB is the store's database handle: a *sql.DB whose query methods pass
// every query through the dialect's rebind, so the store's SQL can be written
// with ? placeholders for every database. The *sql.DB is a private field, not
// embedded, so a call that would skip rebind (Exec, Query, Prepare, Begin)
// does not compile.
type sqlDB struct {
	db *sql.DB
	d  dialect
}

// ExecContext rebinds query and runs it on the database.
func (db *sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.db.ExecContext(ctx, db.d.rebind(query), args...)
}

// QueryContext rebinds query and runs it on the database.
func (db *sqlDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.db.QueryContext(ctx, db.d.rebind(query), args...)
}

// QueryRowContext rebinds query and runs it on the database.
func (db *sqlDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.db.QueryRowContext(ctx, db.d.rebind(query), args...)
}

// BeginTx starts a transaction whose queries are rebound like the handle's.
func (db *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sqlTx, error) {
	tx, err := db.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlTx{tx: tx, d: db.d}, nil
}

// sqlTx is a transaction of a sqlDB: a *sql.Tx whose query methods pass every
// query through the dialect's rebind.
type sqlTx struct {
	tx *sql.Tx
	d  dialect
}

// ExecContext rebinds query and runs it in the transaction.
func (tx *sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(ctx, tx.d.rebind(query), args...)
}

// QueryContext rebinds query and runs it in the transaction.
func (tx *sqlTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.tx.QueryContext(ctx, tx.d.rebind(query), args...)
}

// QueryRowContext rebinds query and runs it in the transaction.
func (tx *sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(ctx, tx.d.rebind(query), args...)
}

// Close closes the database handle.
func (db *sqlDB) Close() error { return db.db.Close() }

// Commit commits the transaction.
func (tx *sqlTx) Commit() error { return tx.tx.Commit() }

// Rollback aborts the transaction.
func (tx *sqlTx) Rollback() error { return tx.tx.Rollback() }
