package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKeyFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "worker.key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveWorkerKey(t *testing.T) {
	tests := []struct {
		name       string
		argv       bool
		file       func(t *testing.T) string
		env        string
		want       string
		wantErr    string // substring
		wantNoRead bool
	}{
		{
			name:    "argv key refused and names both alternatives",
			argv:    true,
			wantErr: "--worker-key-file",
		},
		{
			name:    "argv key refused even when file is also given",
			argv:    true,
			file:    func(t *testing.T) string { return writeKeyFile(t, "k\n", 0o600) },
			wantErr: "--worker-key-file",
		},
		{
			name: "file 0600 accepted and trimmed",
			file: func(t *testing.T) string { return writeKeyFile(t, "  filekey \n", 0o600) },
			want: "filekey",
		},
		{
			name: "file 0400 accepted",
			file: func(t *testing.T) string { return writeKeyFile(t, "filekey", 0o400) },
			want: "filekey",
		},
		{
			name:    "group-readable file refused",
			file:    func(t *testing.T) string { return writeKeyFile(t, "filekey", 0o640) },
			wantErr: "0600 or stricter",
		},
		{
			name:    "world-readable file refused",
			file:    func(t *testing.T) string { return writeKeyFile(t, "filekey", 0o644) },
			wantErr: "0600 or stricter",
		},
		{
			name:    "directory refused",
			file:    func(t *testing.T) string { return t.TempDir() },
			wantErr: "not a regular file",
		},
		{
			name:    "missing file refused",
			file:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			wantErr: "worker key file",
		},
		{
			name:    "empty file refused",
			file:    func(t *testing.T) string { return writeKeyFile(t, " \n", 0o600) },
			wantErr: "empty",
		},
		{
			name:    "exported env key refused without a key file",
			env:     "envkey",
			wantErr: "must not be set",
		},
		{
			name:    "exported env key refused even with a key file",
			file:    func(t *testing.T) string { return writeKeyFile(t, "filekey", 0o600) },
			env:     "envkey",
			wantErr: "must not be set",
		},
		{
			name:    "argv key flag present but empty is still refused",
			argv:    true,
			wantErr: "--worker-key-file",
		},
		{
			name: "nothing configured yields empty key",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(workerKeyEnv, tt.env)
			var file string
			if tt.file != nil {
				file = tt.file(t)
			}
			got, err := resolveWorkerKey(tt.argv, file, os.Getenv)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckKeyFileNotBound(t *testing.T) {
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	dataDir := filepath.Join(root, "data")
	for _, d := range []string{keyDir, dataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(keyDir, "worker.key")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(keyDir, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		file    string
		bound   []string
		wantErr bool
	}{
		{"unrelated paths ok", key, []string{dataDir, "", filepath.Join(root, "keys2")}, false},
		{"parent dir bound", key, []string{root}, true},
		{"exact dir bound", key, []string{keyDir}, true},
		{"file itself bound", key, []string{key}, true},
		{"bound through symlink", key, []string{link}, true},
		{"key given through symlink", filepath.Join(link, "worker.key"), []string{keyDir}, true},
		{"sibling with same prefix is fine", key, []string{filepath.Join(root, "key")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkKeyFileNotBound(tt.file, tt.bound)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestArgvKeyErrorNamesAlternatives(t *testing.T) {
	msg := errWorkerKeyOnArgv.Error()
	for _, want := range []string{"--worker-key-file", "/proc"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}
