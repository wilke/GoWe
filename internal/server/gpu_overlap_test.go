package server

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func overlapWorker(id, host string, state model.WorkerState, uuids ...string) *model.Worker {
	w := &model.Worker{
		ID: id, Name: id + "-name", State: state, HostID: host, GPUEnabled: true,
		Runtime: model.RuntimeNone, LastSeen: time.Now().UTC(), RegisteredAt: time.Now().UTC(),
		Labels: map[string]string{}, Datasets: map[string]string{},
	}
	for i, u := range uuids {
		w.GPUs = append(w.GPUs, model.GPUInfo{Index: i, UUID: u, Name: "Fake GPU"})
	}
	return w
}

func TestFindGPUOverlaps(t *testing.T) {
	online, offline := model.WorkerStateOnline, model.WorkerStateOffline
	tests := []struct {
		name   string
		w      *model.Worker
		others []*model.Worker
		want   map[string]string // other worker id -> uuids in common
	}{
		{"overlap on the same host", overlapWorker("new", "h1", online, "GPU-a", "GPU-b"),
			[]*model.Worker{overlapWorker("old", "h1", online, "GPU-b", "GPU-c")},
			map[string]string{"old": "GPU-b"}},
		{"disjoint GPUs", overlapWorker("new", "h1", online, "GPU-a"),
			[]*model.Worker{overlapWorker("old", "h1", online, "GPU-b")}, nil},
		{"other host", overlapWorker("new", "h1", online, "GPU-a"),
			[]*model.Worker{overlapWorker("old", "h2", online, "GPU-a")}, nil},
		{"other worker offline", overlapWorker("new", "h1", online, "GPU-a"),
			[]*model.Worker{overlapWorker("old", "h1", offline, "GPU-a")}, nil},
		{"itself ignored", overlapWorker("new", "h1", online, "GPU-a"),
			[]*model.Worker{overlapWorker("new", "h1", online, "GPU-a")}, nil},
		{"no host id", overlapWorker("new", "", online, "GPU-a"),
			[]*model.Worker{overlapWorker("old", "", online, "GPU-a")}, nil},
		{"registering worker not GPU-enabled", func() *model.Worker {
			w := overlapWorker("new", "h1", online, "GPU-a")
			w.GPUEnabled = false
			return w
		}(), []*model.Worker{overlapWorker("old", "h1", online, "GPU-a")}, nil},
		{"two others, all GPUs", overlapWorker("new", "h1", online, "GPU-a", "GPU-b"),
			[]*model.Worker{
				overlapWorker("o1", "h1", online, "GPU-a"),
				overlapWorker("o2", "h1", online, "GPU-b", "GPU-a"),
			},
			map[string]string{"o1": "GPU-a", "o2": "GPU-a,GPU-b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findGPUOverlaps(tt.w, tt.others)
			if len(got) != len(tt.want) {
				t.Fatalf("overlaps = %+v, want %v", got, tt.want)
			}
			for _, o := range got {
				if want, ok := tt.want[o.worker.ID]; !ok || strings.Join(o.uuids, ",") != want {
					t.Errorf("overlap with %s = %v, want %q", o.worker.ID, o.uuids, want)
				}
			}
		})
	}
}

// warnGPUOverlap logs one WARN naming both workers and the shared UUIDs.
func TestWarnGPUOverlap_Logs(t *testing.T) {
	logs := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	srv := New(config.DefaultServerConfig(), st, nil, logger)

	old := overlapWorker("wrk_old", "h1", model.WorkerStateOnline, "GPU-a", "GPU-b")
	if err := st.CreateWorker(ctx, old); err != nil {
		t.Fatal(err)
	}
	reg := overlapWorker("wrk_new", "h1", model.WorkerStateOnline, "GPU-b")
	if err := st.CreateWorker(ctx, reg); err != nil {
		t.Fatal(err)
	}
	srv.warnGPUOverlap(ctx, reg)

	out := logs.String()
	for _, want := range []string{"level=WARN", "wrk_new", "wrk_old", "GPU-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
	if strings.Contains(out, "GPU-a") {
		t.Errorf("log %q names GPU-a, which is not shared", out)
	}
}
