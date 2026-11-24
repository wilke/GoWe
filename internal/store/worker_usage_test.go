package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// testUsage returns a fully populated usage sample (#323).
func testUsage(load1 float64) *model.WorkerUsage {
	mem := int64(512)
	util := 42.5
	psi := 1.25
	return &model.WorkerUsage{
		Host: &model.HostUsage{
			Load1:           load1,
			Load5:           0.5,
			MemTotalMB:      4096,
			MemAvailableMB:  1024,
			PSICPUSomeAvg10: &psi,
			SampledAt:       time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
		},
		GPUs: []model.GPUUsage{{UUID: "GPU-a", MemUsedMB: &mem, UtilPct: &util}},
	}
}

// usageJSONOf renders usage as JSON so comparisons are readable and do not
// depend on pointer identity.
func usageJSONOf(t *testing.T, u *model.WorkerUsage) string {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTouchWorker(t *testing.T) {
	t0 := time.Now().UTC().Truncate(time.Millisecond)
	t1 := t0.Add(5 * time.Second)
	tests := []struct {
		name        string
		usage       *model.WorkerUsage
		wantUsage   *model.WorkerUsage
		wantUsageAt time.Time
	}{
		{"with usage replaces it", testUsage(2), testUsage(2), t1},
		{"without usage keeps it", nil, testUsage(1), t0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			w := sampleWorker()
			w.State = model.WorkerStateOffline
			w.CurrentTask = "task_1"
			if err := st.CreateWorker(ctx, w); err != nil {
				t.Fatal(err)
			}
			// An earlier heartbeat stored usage.
			if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOffline, t0, testUsage(1)); err != nil {
				t.Fatal(err)
			}
			if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOnline, t1, tt.usage); err != nil {
				t.Fatal(err)
			}

			got, err := st.GetWorker(ctx, w.ID)
			if err != nil || got == nil {
				t.Fatalf("GetWorker: %v %v", got, err)
			}
			if got.State != model.WorkerStateOnline || !got.LastSeen.Equal(t1) {
				t.Errorf("state/last_seen = %s/%v, want online/%v", got.State, got.LastSeen, t1)
			}
			if g, w := usageJSONOf(t, got.Usage), usageJSONOf(t, tt.wantUsage); g != w {
				t.Errorf("usage = %s, want %s", g, w)
			}
			if got.UsageAt == nil || !got.UsageAt.Equal(tt.wantUsageAt) {
				t.Errorf("usage_at = %v, want %v", got.UsageAt, tt.wantUsageAt)
			}
			// Columns TouchWorker does not own are untouched.
			if got.CurrentTask != "task_1" || got.Name != w.Name || got.Labels["env"] != "test" {
				t.Errorf("other columns changed: current_task=%q name=%q labels=%v", got.CurrentTask, got.Name, got.Labels)
			}
		})
	}
}

func TestTouchWorker_NeverReportedStaysNull(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOnline, time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	var usageNull, usageAtNull bool
	if err := st.db.QueryRowContext(ctx,
		`SELECT host_usage IS NULL, host_usage_at IS NULL FROM workers WHERE id = ?`, w.ID).
		Scan(&usageNull, &usageAtNull); err != nil {
		t.Fatal(err)
	}
	if !usageNull || !usageAtNull {
		t.Errorf("NULL columns: host_usage=%v host_usage_at=%v, want both NULL", usageNull, usageAtNull)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	if got.Usage != nil || got.UsageAt != nil {
		t.Errorf("usage = %v at %v, want nil", got.Usage, got.UsageAt)
	}
}

func TestTouchWorker_NotFound(t *testing.T) {
	st := testStore(t)
	if err := st.TouchWorker(context.Background(), "wrk_missing", model.WorkerStateOnline, time.Now().UTC(), nil); err == nil {
		t.Error("TouchWorker on a missing worker: want error")
	}
}

// TestTouchWorker_KeepsCheckedOutTask: a heartbeat working from a worker
// snapshot read before a concurrent checkout must not clear current_task.
func TestTouchWorker_KeepsCheckedOutTask(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(ctx, capTask("t1", 0, 0, time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.GetWorker(ctx, w.ID)
	if err != nil || snapshot == nil {
		t.Fatalf("GetWorker: %v %v", snapshot, err)
	}
	task, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil || task == nil {
		t.Fatalf("CheckoutTask = %v, %v; want t1", task, err)
	}
	if err := st.TouchWorker(ctx, snapshot.ID, snapshot.State, time.Now().UTC(), testUsage(1)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	if got.CurrentTask != "t1" {
		t.Errorf("current_task = %q, want t1", got.CurrentTask)
	}
}

func TestUpdateWorker_LeavesUsageIntact(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := st.TouchWorker(ctx, w.ID, model.WorkerStateOnline, at, testUsage(1)); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	got.Name = "renamed"
	got.Usage, got.UsageAt = nil, nil
	if err := st.UpdateWorker(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetWorker(ctx, w.ID)
	if err != nil || again == nil {
		t.Fatalf("GetWorker after update: %v %v", again, err)
	}
	if again.Name != "renamed" {
		t.Errorf("name = %q, want renamed", again.Name)
	}
	if g, w := usageJSONOf(t, again.Usage), usageJSONOf(t, testUsage(1)); g != w {
		t.Errorf("usage after UpdateWorker = %s, want %s", g, w)
	}
	if again.UsageAt == nil || !again.UsageAt.Equal(at) {
		t.Errorf("usage_at after UpdateWorker = %v, want %v", again.UsageAt, at)
	}
}

func TestCreateWorker_UsageRoundTrip(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Millisecond)
	tests := []struct {
		name  string
		usage *model.WorkerUsage
		at    *time.Time
	}{
		{"reported", testUsage(3), &at},
		{"not reported", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			w := sampleWorker()
			w.Usage, w.UsageAt = tt.usage, tt.at
			if err := st.CreateWorker(ctx, w); err != nil {
				t.Fatal(err)
			}
			all, err := st.ListWorkers(ctx)
			if err != nil || len(all) != 1 {
				t.Fatalf("ListWorkers = %d, %v", len(all), err)
			}
			if g, w := usageJSONOf(t, all[0].Usage), usageJSONOf(t, tt.usage); g != w {
				t.Errorf("usage = %s, want %s", g, w)
			}
			if (all[0].UsageAt == nil) != (tt.at == nil) || (tt.at != nil && !all[0].UsageAt.Equal(*tt.at)) {
				t.Errorf("usage_at = %v, want %v", all[0].UsageAt, tt.at)
			}
		})
	}
}

func TestMigrate_WorkerUsageColumnsIdempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
	for _, col := range []string{"host_usage", "host_usage_at"} {
		var n int
		if err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('workers') WHERE name = ?`, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s count = %d, err %v", col, n, err)
		}
	}
}
