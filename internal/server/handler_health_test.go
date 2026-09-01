package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/store"
)

// newHealthTestServer builds a Server wired to a real scheduler.Loop (not
// started), so tests can drive the scheduler's actual lifecycle and observe
// it through the /api/v1/health handler exactly as production wiring does
// (cmd/server/main.go: scheduler.NewLoop -> server.New).
func newHealthTestServer(t *testing.T) (*Server, *scheduler.Loop) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))

	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := executor.NewRegistry(logger)
	reg.Register(executor.NewLocalExecutor(t.TempDir(), logger))

	sched := scheduler.NewLoop(st, reg, scheduler.DefaultConfig(), logger)
	srv := New(config.DefaultServerConfig(), st, sched, logger)
	return srv, sched
}

func healthSchedulerField(t *testing.T, srv *Server) string {
	t.Helper()
	env := doGet(t, srv, "/api/v1/health")
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal health data: %v", err)
	}
	got, ok := data["scheduler"].(string)
	if !ok {
		t.Fatalf("health response missing string 'scheduler' field, data=%v", data)
	}
	return got
}

// waitForSchedulerState polls the health endpoint until the reported
// scheduler state matches want or the timeout elapses.
func waitForSchedulerState(t *testing.T, srv *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = healthSchedulerField(t, srv)
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("health scheduler field = %q after timeout, want %q", got, want)
}

// TestHandleHealth_SchedulerState verifies /api/v1/health reports the real
// scheduler lifecycle state instead of a hardcoded value (issue #193).
func TestHandleHealth_SchedulerState(t *testing.T) {
	t.Run("no scheduler attached", func(t *testing.T) {
		srv := testServer() // sched == nil, as in some test/dry-run configurations
		if got := healthSchedulerField(t, srv); got != scheduler.StateNotStarted {
			t.Errorf("scheduler field = %q, want %q", got, scheduler.StateNotStarted)
		}
	})

	t.Run("not started", func(t *testing.T) {
		srv, _ := newHealthTestServer(t)
		if got := healthSchedulerField(t, srv); got != scheduler.StateNotStarted {
			t.Errorf("scheduler field = %q, want %q", got, scheduler.StateNotStarted)
		}
	})

	t.Run("running", func(t *testing.T) {
		srv, sched := newHealthTestServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		go func() { _ = sched.Start(ctx) }()
		waitForSchedulerState(t, srv, scheduler.StateRunning)
	})

	t.Run("stopped", func(t *testing.T) {
		srv, sched := newHealthTestServer(t)
		ctx := context.Background()

		go func() { _ = sched.Start(ctx) }()
		waitForSchedulerState(t, srv, scheduler.StateRunning)

		if err := sched.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if got := healthSchedulerField(t, srv); got != scheduler.StateStopped {
			t.Errorf("scheduler field = %q, want %q", got, scheduler.StateStopped)
		}
	})
}
