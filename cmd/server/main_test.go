package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/server"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty is unset", in: "", want: ""},
		{name: "trims trailing slash", in: "/a/b/", want: "/a/b"},
		{name: "already normalized", in: "/a/b", want: "/a/b"},
		{name: "single segment", in: "/gowe", want: "/gowe"},
		{name: "no leading slash is an error", in: "a/b", wantErr: true},
		{name: "root alone is an error", in: "/", wantErr: true},
		{name: "root with extra slashes is an error", in: "//", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBasePath(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeBasePath(%q) = %q, nil; want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeBasePath(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeBasePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// testMainStore opens an in-memory, migrated SQLite store for main package
// tests (same pattern as internal/server's testAuthStore).
func testMainStore(t *testing.T) store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestWorkerAPIIsKeyless covers the seam behind the keyless-worker-API startup
// warning (#251): no static keys and no DB-backed keys => keyless (warn);
// either source configured => not keyless (no warn).
func TestWorkerAPIIsKeyless(t *testing.T) {
	ctx := context.Background()

	t.Run("no keys at all", func(t *testing.T) {
		st := testMainStore(t)
		if !workerAPIIsKeyless(ctx, st, &server.WorkerKeyConfig{}) {
			t.Error("workerAPIIsKeyless = false, want true (no static or DB keys)")
		}
	})

	t.Run("static key configured", func(t *testing.T) {
		st := testMainStore(t)
		cfg := &server.WorkerKeyConfig{Keys: map[string]server.WorkerKeyEntry{
			"secret-abc": {ID: "node-a", Groups: []string{"default"}},
		}}
		if workerAPIIsKeyless(ctx, st, cfg) {
			t.Error("workerAPIIsKeyless = true, want false (static key configured)")
		}
	})

	t.Run("DB-backed key minted", func(t *testing.T) {
		st := testMainStore(t)
		_, hash, prefix, err := model.GenerateWorkerKey()
		if err != nil {
			t.Fatalf("generate worker key: %v", err)
		}
		key := &model.WorkerKey{
			ID: "wk_db1", Label: "db-node", KeyHash: hash, KeyPrefix: prefix,
			Groups: []string{"default"}, CreatedAt: time.Now().UTC(),
		}
		if err := st.CreateWorkerKey(ctx, key); err != nil {
			t.Fatalf("CreateWorkerKey: %v", err)
		}
		if workerAPIIsKeyless(ctx, st, &server.WorkerKeyConfig{}) {
			t.Error("workerAPIIsKeyless = true, want false (DB-backed key minted)")
		}
	})

	t.Run("nil store, no static keys", func(t *testing.T) {
		if !workerAPIIsKeyless(ctx, nil, &server.WorkerKeyConfig{}) {
			t.Error("workerAPIIsKeyless = false, want true (nil store, no static keys)")
		}
	})
}
