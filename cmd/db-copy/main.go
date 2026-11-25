// Command gowe-db-copy copies a GoWe SQLite database into an empty
// PostgreSQL database, once, offline, so that a deployment can move to
// PostgreSQL with its history. It reads a COPY of the SQLite file (read-only
// unless --migrate-source is given), writes everything in one PostgreSQL
// transaction, verifies every row before committing, and commits nothing on
// any error. See docs/tools/db-copy.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	"github.com/me/gowe/internal/dbconfig"
	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/store"
)

const usageText = `Usage: gowe-db-copy --from <sqlite file> [--to postgres://...] [--config server.json] [--migrate-source]

Copies every row of a GoWe SQLite database into an empty PostgreSQL database,
in one transaction, verified before commit. Run it on a COPY of the SQLite
file while no server uses it (see docs/tools/db-copy.md).

The PostgreSQL target is selected as gowe-server selects its database: --to
plays the role of --db, and --config supplies database.url, database.password
(the only place a password may come from; the file must be mode 0600) and
database.schema.

Flags:
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// errReported is a usage error the flag package has already printed.
var errReported = errors.New("invalid flags")

// options are the parsed and checked command line.
type options struct {
	from          string // absolute path of the SQLite file
	migrateSource bool
	target        *dbconfig.Selection
	debug         bool
}

// run is the whole program; it returns the exit code: 0 success, 1 failure,
// 2 usage error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	opts, err := parseArgs(args, stderr, getenv)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, errReported) {
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "gowe-db-copy: %v\n", err)
		return 2
	}

	level := slog.LevelWarn
	if opts.debug {
		level = slog.LevelDebug
	}
	logger := logging.NewLoggerWithWriter(level, "text", stderr)

	if err := copyDatabase(ctx, opts, stdout, stderr, logger); err != nil {
		fmt.Fprintf(stderr, "gowe-db-copy: %v\n", err)
		if errors.Is(err, store.ErrCopyCommitUnconfirmed) {
			fmt.Fprintln(stderr, "gowe-db-copy: the commit was not confirmed, so the PostgreSQL database may or may not hold the rows: check it before retrying (the tool refuses a database that is not empty)")
		} else {
			fmt.Fprintln(stderr, "gowe-db-copy: no rows were written to the PostgreSQL database")
		}
		return 1
	}
	return 0
}

// parseArgs parses the flags and checks everything that needs no database:
// the source file, the -wal guard of --migrate-source, and that the target
// is PostgreSQL.
func parseArgs(args []string, stderr io.Writer, getenv func(string) string) (*options, error) {
	fs := flag.NewFlagSet("gowe-db-copy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageText)
		fs.PrintDefaults()
	}
	from := fs.String("from", "", "the SQLite database file to copy (required; a COPY, not the live file)")
	to := fs.String("to", "", "the PostgreSQL target: a postgres:// or postgresql:// URL without a password (default: database.url of --config)")
	configPath := fs.String("config", "", "server config file (JSON) with the database block url/password/schema; with a password it must be mode 0600")
	migrateSource := fs.Bool("migrate-source", false, "bring the SQLite file to the current schema first (WRITES to the file: only for a copy)")
	debug := fs.Bool("debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, errReported // the flag package printed it with the usage
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q (all options are flags)", fs.Arg(0))
	}

	if *from == "" {
		return nil, errors.New("--from is required: the SQLite database file to copy")
	}
	abs, err := filepath.Abs(*from)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	// Resolve symbolic links: SQLite keeps the -wal file next to the real
	// file, so the live-database check below must look there.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("--from %s is not a regular file", abs)
	}
	if *migrateSource {
		if err := refuseLiveDatabase(abs); err != nil {
			return nil, err
		}
	}

	sel, err := dbconfig.SelectFor("--to", *to, *configPath, getenv)
	if err != nil {
		return nil, fmt.Errorf("target database: %w", err)
	}
	if sel.Backend != dbconfig.BackendPostgres {
		return nil, errors.New("the target must be PostgreSQL: give --to postgres://user@host:port/database " +
			"or database.url in the --config file (a SQLite target is not supported)")
	}

	return &options{from: abs, migrateSource: *migrateSource, target: sel, debug: *debug}, nil
}

// refuseLiveDatabase refuses a SQLite file with a non-empty write-ahead log
// next to it: a server may be using it, and its last writes may still be in
// the log. --migrate-source writes to the file, so it must be a copy.
func refuseLiveDatabase(path string) error {
	fi, err := os.Stat(path + "-wal")
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("--migrate-source: check %s-wal: %w", path, err)
	case fi.Size() == 0:
		return nil
	}
	return fmt.Errorf("--migrate-source modifies the file, and %s looks like a live database (%s-wal is not empty): "+
		"stop the server and make a copy first, for example\n"+
		"  sqlite3 'file:%s?mode=ro' \".backup /path/copy.db\"\n"+
		"then run gowe-db-copy on the copy", path, path, path)
}

// sqliteFileURI is the file: URI of the SQLite file at abs with the given
// mode (ro, rw), with every special character escaped.
func sqliteFileURI(abs, mode string) string {
	return (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=" + mode}).String()
}

// copyDatabase opens both databases, copies and prints the report.
func copyDatabase(ctx context.Context, opts *options, stdout, stderr io.Writer, logger *slog.Logger) error {
	var src *store.SQLStore
	var err error
	if opts.migrateSource {
		// mode=rw: open the existing file, never create one.
		src, err = store.NewSQLiteStore(sqliteFileURI(opts.from, "rw"), logger)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		defer src.Close()
		fmt.Fprintf(stderr, "migrating the schema of %s (this modifies the file)\n", opts.from)
		if err := src.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate source: %w", err)
		}
	} else {
		if fi, err := os.Stat(opts.from + "-wal"); err == nil && fi.Size() > 0 {
			fmt.Fprintf(stderr, "warning: %s-wal is not empty: if a server is still using %s, stop it and copy the file first\n", opts.from, opts.from)
		}
		src, err = store.OpenSQLiteReadOnly(opts.from, logger)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		defer src.Close()
	}

	// The store logs host, port, database and schema; never the URL.
	dst, err := dbconfig.Open(ctx, opts.target, "", logger)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer dst.Close()
	if err := dst.Migrate(ctx); err != nil {
		return fmt.Errorf("create the schema in the target: %w", err)
	}

	fmt.Fprintf(stderr, "copying %s into PostgreSQL (one transaction, verified before commit)\n", opts.from)
	report, err := store.CopyDatabase(ctx, src, dst, func(table string, rows int64) {
		fmt.Fprintf(stderr, "  %s: %d rows\n", table, rows)
	})
	if err != nil {
		return err
	}

	writeReport(stdout, report)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Reminder: start gowe-server on PostgreSQL with the same token key (--token-key-file or GOWE_TOKEN_KEY)")
	fmt.Fprintln(stdout, "as before: encrypted provider tokens and secrets were copied as they are and only that key decrypts them.")
	fmt.Fprintln(stdout, "Keep the SQLite file: it is the way back.")
	return nil
}

// writeReport prints the per-table table and the summary line.
func writeReport(w io.Writer, report *store.CopyReport) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "table\trows\ttimestamps fixed\ttext sanitised")
	for _, t := range report.Tables {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", t.Table, t.Rows, t.TimestampsFixed, t.TextSanitised)
	}
	tw.Flush()
	fmt.Fprintf(w, "copy verified: %d rows in %d tables\n", report.TotalRows(), len(report.Tables))
}
