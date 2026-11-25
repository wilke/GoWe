package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noEnv is an environment with no variable set.
func noEnv(string) string { return "" }

// TestRun_FlagValidation covers the refusals that need no database: each
// returns exit code 2 before anything is opened.
func TestRun_FlagValidation(t *testing.T) {
	const testURLPW = "Qz4mR8-url"
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(dbFile, []byte("not opened by these tests"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveDir := t.TempDir()
	liveFile := filepath.Join(liveDir, "gowe.db")
	if err := os.WriteFile(liveFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveFile+"-wal", []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyWALDir := t.TempDir()
	emptyWALFile := filepath.Join(emptyWALDir, "gowe.db")
	if err := os.WriteFile(emptyWALFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyWALFile+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A symbolic link to the live file: the -wal sits next to the real file.
	linkToLive := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(liveFile, linkToLive); err != nil {
		t.Fatal(err)
	}
	const pgURL = "postgres://gowe@127.0.0.1:5433/gowe"

	tests := []struct {
		name    string
		args    []string
		want    []string // substrings of stderr
		notWant []string
	}{
		{name: "missing --from", args: []string{"--to", pgURL},
			want: []string{"--from is required"}},
		{name: "--from does not exist", args: []string{"--from", filepath.Join(dir, "missing.db"), "--to", pgURL},
			want: []string{"--from", "no such file"}},
		{name: "--from is a directory", args: []string{"--from", dir, "--to", pgURL},
			want: []string{"not a regular file"}},
		{name: "target defaults to SQLite", args: []string{"--from", dbFile},
			want: []string{"the target must be PostgreSQL"}},
		{name: "target is a SQLite path", args: []string{"--from", dbFile, "--to", filepath.Join(dir, "target.db")},
			want: []string{"the target must be PostgreSQL"}},
		{name: "password in --to", args: []string{"--from", dbFile, "--to", "postgres://gowe:" + testURLPW + "@127.0.0.1/gowe"},
			want: []string{"--to", "contains a password"}, notWant: []string{testURLPW, "--db"}},
		{name: "--migrate-source next to a non-empty -wal", args: []string{"--from", liveFile, "--to", pgURL, "--migrate-source"},
			want: []string{"looks like a live database", "-wal is not empty", ".backup"}},
		{name: "--migrate-source through a symlink to a live database", args: []string{"--from", linkToLive, "--to", pgURL, "--migrate-source"},
			want: []string{"looks like a live database"}},
		{name: "--migrate-source with an empty -wal passes that check", args: []string{"--from", emptyWALFile, "--migrate-source"},
			want: []string{"the target must be PostgreSQL"}, notWant: []string{"live database"}},
		{name: "positional argument", args: []string{"--from", dbFile, "--to", pgURL, "extra"},
			want: []string{`unexpected argument "extra"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, &stdout, &stderr, noEnv)
			if code != 2 {
				t.Errorf("exit code = %d, want 2; stderr:\n%s", code, stderr.String())
			}
			for _, w := range tc.want {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("stderr does not contain %q:\n%s", w, stderr.String())
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(stderr.String(), w) {
					t.Errorf("stderr contains %q:\n%s", w, stderr.String())
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}

	for _, f := range []string{dbFile, liveFile, emptyWALFile} {
		if b, err := os.ReadFile(f); err != nil || (string(b) != "x" && string(b) != "not opened by these tests") {
			t.Errorf("%s changed: %q %v", f, b, err)
		}
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr, noEnv); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "gowe-db-copy --from") {
		t.Errorf("usage not printed:\n%s", stderr.String())
	}
}
