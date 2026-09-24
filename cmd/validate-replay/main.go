// Command validate-replay is a read-only report tool: it replays submitted
// inputs, stored in a COPY of a GoWe SQLite database, through
// internal/validate.SubmissionInputs to estimate how many submissions would
// fail once input type validation runs in "enforce" mode. It never writes
// to the database and refuses outright to run against the known production
// database path.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// productionDBPath is the one file this tool must never be pointed at,
// per the operating rule: only ever run validate-replay against a COPY.
const productionDBPath = "/scout/wf/gowe/gowe.db"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate-replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to a COPY of the GoWe SQLite database (required)")
	limit := fs.Int("limit", 0, "maximum number of submissions to process, 0 = all")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of a text summary")
	since := fs.String("since", "", "only consider submissions created at or after this RFC3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *dbPath == "" {
		fmt.Fprintln(stderr, "validate-replay: --db is required")
		return 2
	}

	var sinceTime *time.Time
	if *since != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			fmt.Fprintf(stderr, "validate-replay: --since: %v\n", err)
			return 2
		}
		sinceTime = &t
	}

	absPath, err := refuseIfProduction(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "validate-replay: %v\n", err)
		return 2
	}

	db, err := openReadOnly(absPath)
	if err != nil {
		fmt.Fprintf(stderr, "validate-replay: %v\n", err)
		return 1
	}
	defer db.Close()

	report, err := Run(context.Background(), db, *dbPath, RunOptions{Limit: *limit, Since: sinceTime})
	if err != nil {
		fmt.Fprintf(stderr, "validate-replay: %v\n", err)
		return 1
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "validate-replay: encode json: %v\n", err)
			return 1
		}
		return 0
	}

	writeText(stdout, report)
	return 0
}

// refuseIfProduction resolves dbPath to an absolute path and returns an
// error if it names the production database, by literal absolute-path
// match or, when the paths can be resolved on disk, by symlink target
// (e.g. a symlink that points at production). It performs no writes.
func refuseIfProduction(dbPath string) (string, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve --db path: %w", err)
	}

	if abs == productionDBPath {
		return "", fmt.Errorf("refusing to run against the production database %s — run this tool only against a COPY", productionDBPath)
	}

	resolved := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = r
	}
	prodResolved := productionDBPath
	if r, err := filepath.EvalSymlinks(productionDBPath); err == nil {
		prodResolved = r
	}
	if resolved == prodResolved {
		return "", fmt.Errorf("refusing to run against the production database %s (resolved via symlink) — run this tool only against a COPY", productionDBPath)
	}

	return abs, nil
}

// openReadOnly opens dbPath strictly read-only via the modernc.org/sqlite
// "sqlite" driver, using SQLite's file: URI mode=ro so no write, including
// WAL/journal creation, ever touches the file.
func openReadOnly(absPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro", absPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", absPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s read-only: %w", absPath, err)
	}
	return db, nil
}
