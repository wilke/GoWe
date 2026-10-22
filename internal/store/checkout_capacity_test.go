package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func capTask(id string, cores int, ramMB int64, age time.Duration) *model.Task {
	tk := tokenTask(id, "")
	tk.RuntimeHints = &model.RuntimeHints{Cores: cores, RamMB: ramMB}
	tk.CreatedAt = time.Now().UTC().Add(-age).Truncate(time.Millisecond)
	return tk
}

func capWorker(maxCPUs *int, maxMem *int64) *model.Worker {
	w := sampleWorker()
	w.MaxCPUs, w.MaxMemMB = maxCPUs, maxMem
	return w
}

func intP(v int) *int       { return &v }
func int64P(v int64) *int64 { return &v }

func TestCheckoutTask_WorkerFit(t *testing.T) {
	tests := []struct {
		name    string
		maxCPUs *int
		maxMem  *int64
		cores   int
		ram     int64
		want    bool
	}{
		{"fits", intP(16), int64P(32000), 8, 16000, true},
		{"equal cap fits", intP(16), int64P(32000), 16, 32000, true},
		{"cores exceed", intP(16), int64P(32000), 17, 0, false},
		{"ram exceed", intP(16), int64P(32000), 1, 32001, false},
		{"legacy NULL unchecked", nil, nil, 4096, 1 << 40, true},
		{"only cpu cap, ram unchecked", intP(4), nil, 2, 1 << 40, true},
		{"only mem cap, cores unchecked", nil, int64P(1000), 512, 500, true},
		{"zero hints unchecked", intP(1), int64P(1), 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			if err := st.CreateWorker(ctx, capWorker(tt.maxCPUs, tt.maxMem)); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateTask(ctx, capTask("t1", tt.cores, tt.ram, time.Minute)); err != nil {
				t.Fatal(err)
			}
			got, err := st.CheckoutTask(ctx, "wrk_test-1", "", model.RuntimeNone)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.want {
				t.Errorf("checked out = %v, want %v", got != nil, tt.want)
			}
		})
	}
}

func TestCheckoutTask_UnfitHeadDoesNotStarve(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateWorker(ctx, capWorker(intP(16), int64P(64000))); err != nil {
		t.Fatal(err)
	}
	// 12 older tasks that do not fit fill the LIMIT 10 window...
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("big_%02d", i)
		if err := st.CreateTask(ctx, capTask(id, 64, 0, time.Hour+time.Duration(12-i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// ...one newer task that does.
	if err := st.CreateTask(ctx, capTask("small", 4, 0, time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := st.CheckoutTask(ctx, "wrk_test-1", "", model.RuntimeNone)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "small" {
		t.Fatalf("checked out %+v, want the fitting task behind the unfit head", got)
	}
	// The unfit tasks stay queued.
	if again, err := st.CheckoutTask(ctx, "wrk_test-1", "", model.RuntimeNone); err != nil || again != nil {
		t.Errorf("second checkout = (%v, %v), want (nil, nil)", again, err)
	}
}

func TestWorkerCapacity_RoundTrip(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	w := capWorker(intP(128), int64P(512000))
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	legacy := sampleWorker()
	legacy.ID = "wrk_legacy"
	if err := st.CreateWorker(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorker(ctx, w.ID)
	if err != nil || got == nil {
		t.Fatalf("GetWorker: %v %v", got, err)
	}
	if got.MaxCPUs == nil || *got.MaxCPUs != 128 || got.MaxMemMB == nil || *got.MaxMemMB != 512000 {
		t.Errorf("caps = %v/%v, want 128/512000", got.MaxCPUs, got.MaxMemMB)
	}
	gl, _ := st.GetWorker(ctx, legacy.ID)
	if gl.MaxCPUs != nil || gl.MaxMemMB != nil {
		t.Errorf("legacy caps should be nil, got %v/%v", gl.MaxCPUs, gl.MaxMemMB)
	}

	all, err := st.ListWorkers(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListWorkers = %d, %v", len(all), err)
	}
	for _, x := range all {
		if x.ID == w.ID && (x.MaxCPUs == nil || *x.MaxCPUs != 128) {
			t.Errorf("listed caps lost: %v", x.MaxCPUs)
		}
	}

	// UpdateWorker persists changes, including clearing to NULL.
	got.MaxCPUs = intP(64)
	got.MaxMemMB = nil
	if err := st.UpdateWorker(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetWorker(ctx, w.ID)
	if again.MaxCPUs == nil || *again.MaxCPUs != 64 || again.MaxMemMB != nil {
		t.Errorf("after update caps = %v/%v", again.MaxCPUs, again.MaxMemMB)
	}

	// MarkStaleWorkersOffline returns caps too.
	stale, err := st.MarkStaleWorkersOffline(ctx, -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range stale {
		if x.ID == w.ID {
			found = true
			if x.MaxCPUs == nil || *x.MaxCPUs != 64 {
				t.Errorf("stale caps = %v", x.MaxCPUs)
			}
		}
	}
	if !found {
		t.Error("worker not returned by MarkStaleWorkersOffline")
	}
}

func TestMigrate_WorkerCapacityColumnsIdempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+2, err)
		}
	}
	for _, col := range []string{"max_cpus", "max_mem_mb"} {
		var n int
		if err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('workers') WHERE name = ?`, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s count = %d, err %v", col, n, err)
		}
	}
}
