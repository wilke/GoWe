package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/groupacl"
)

// The start-up log of the worker-group policy (#261): always one INFO line,
// and one WARN when restrictions meet a flag that accepts unverified
// identities.
func TestLogGroupPolicy(t *testing.T) {
	load := func(t *testing.T, content string) *groupacl.Policy {
		t.Helper()
		path := filepath.Join(t.TempDir(), "server.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := groupacl.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	const restricted = `{"worker_groups": {"default_deny": true, "groups": {"gpu": {"users": ["alice"]}}}}`
	const noPolicyMsg = "no worker group policy: every user may use every worker group"
	const restrictedMsg = "worker group access restricted"
	const warnMsg = "worker group restrictions rely on verified usernames"

	tests := []struct {
		name      string
		policy    *groupacl.Policy
		skip      bool
		mgrast    bool
		wantInfo  string
		wantWarn  []string // flags named in the WARN; nil = no WARN
		wantInLog []string
	}{
		{"no config", nil, false, false, noPolicyMsg, nil, nil},
		{"no config, unverified flags", nil, true, true, noPolicyMsg, nil, nil},
		{"empty block", load(t, `{"worker_groups": {}}`), false, false, noPolicyMsg, nil, nil},
		{"restricted", load(t, restricted), false, false, restrictedMsg, nil, []string{"groups=[gpu]", "default_deny=true"}},
		{"restricted, skip verify", load(t, restricted), true, false, restrictedMsg, []string{"--insecure-skip-token-verify"}, nil},
		{"restricted, unverified MG-RAST", load(t, restricted), false, true, restrictedMsg, []string{"--allow-unverified-mgrast"}, nil},
		{"restricted, both", load(t, restricted), true, true, restrictedMsg, []string{"--insecure-skip-token-verify", "--allow-unverified-mgrast"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logGroupPolicy(slog.New(slog.NewTextHandler(&buf, nil)), tt.policy, tt.skip, tt.mgrast)
			out := buf.String()

			if n := strings.Count(out, "level=INFO"); n != 1 || !strings.Contains(out, tt.wantInfo) {
				t.Errorf("want exactly one INFO line with %q, got:\n%s", tt.wantInfo, out)
			}
			for _, s := range tt.wantInLog {
				if !strings.Contains(out, s) {
					t.Errorf("log lacks %q:\n%s", s, out)
				}
			}
			if strings.Contains(out, "alice") {
				t.Errorf("the log must not name users:\n%s", out)
			}
			warns := strings.Count(out, "level=WARN")
			if tt.wantWarn == nil {
				if warns != 0 {
					t.Errorf("want no WARN, got:\n%s", out)
				}
				return
			}
			if warns != 1 || !strings.Contains(out, warnMsg) {
				t.Fatalf("want exactly one WARN %q, got:\n%s", warnMsg, out)
			}
			for _, flag := range tt.wantWarn {
				if !strings.Contains(out, flag) {
					t.Errorf("WARN does not name %s:\n%s", flag, out)
				}
			}
		})
	}
}
