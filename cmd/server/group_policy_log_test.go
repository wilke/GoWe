package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
)

// The start-up log of the worker-group policy (#261): always one INFO line,
// and one WARN when restrictions meet a flag that accepts unverified
// identities.
func TestLogGroupPolicy(t *testing.T) {
	// load stores the rules (and default_deny when set) in a fresh database
	// and loads them as main does.
	load := func(t *testing.T, defaultDeny bool, rules ...*model.WorkerGroupPolicy) *groupacl.Policy {
		t.Helper()
		ctx := context.Background()
		st := testMainStore(t)
		for _, r := range rules {
			if err := st.UpsertWorkerGroupPolicy(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		if defaultDeny {
			if err := st.SetServerSetting(ctx, groupacl.DefaultDenyKey, "true", "root"); err != nil {
				t.Fatal(err)
			}
		}
		p := groupacl.New()
		if err := p.Reload(ctx, st); err != nil {
			t.Fatal(err)
		}
		return p
	}
	restricted := func(t *testing.T) *groupacl.Policy {
		return load(t, true, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}})
	}
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
		{"no policy handle", nil, false, false, noPolicyMsg, nil, nil},
		{"no policy handle, unverified flags", nil, true, true, noPolicyMsg, nil, nil},
		{"nothing stored", load(t, false), false, false, noPolicyMsg, nil, nil},
		{"nothing stored, unverified flags", load(t, false), true, true, noPolicyMsg, nil, nil},
		{"restricted", restricted(t), false, false, restrictedMsg, nil, []string{"groups=[gpu]", "default_deny=true"}},
		{"restricted, skip verify", restricted(t), true, false, restrictedMsg, []string{"--insecure-skip-token-verify"}, nil},
		{"restricted, unverified MG-RAST", restricted(t), false, true, restrictedMsg, []string{"--allow-unverified-mgrast"}, nil},
		{"restricted, both", restricted(t), true, true, restrictedMsg, []string{"--insecure-skip-token-verify", "--allow-unverified-mgrast"}, nil},
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
			if !strings.Contains(out, "managers are identified by username") {
				t.Errorf("WARN does not say that group managers are identified by username:\n%s", out)
			}
		})
	}
}
