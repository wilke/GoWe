package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestFormatTime(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 5, 0, time.UTC)
	plus5h := time.FixedZone("plus5", 5*3600)
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"whole second", base, "2026-11-25T10:00:05.000000000Z"},
		{"trailing zeros kept", base.Add(500 * time.Millisecond), "2026-11-25T10:00:05.500000000Z"},
		{"nanoseconds", base.Add(123456789), "2026-11-25T10:00:05.123456789Z"},
		{"other zone becomes UTC", base.In(plus5h), "2026-11-25T10:00:05.000000000Z"},
		{"zero time", time.Time{}, "0001-01-01T00:00:00.000000000Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTime(tc.in)
			if got != tc.want {
				t.Fatalf("formatTime = %q, want %q", got, tc.want)
			}
			back, err := parseTimeOrZero(got)
			if err != nil {
				t.Fatalf("parse %q: %v", got, err)
			}
			if !back.Equal(tc.in) {
				t.Errorf("round trip = %v, want %v", back, tc.in)
			}
		})
	}
}

// String order must be time order: SQL compares and sorts these columns as text.
func TestFormatTime_StringOrderIsTimeOrder(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 5, 0, time.UTC)
	plus5h := time.FixedZone("plus5", 5*3600)
	// Ascending instants. With time.RFC3339Nano the first pair sorts the wrong
	// way ("...05Z" > "...05.5Z") and so does the zone pair.
	times := []time.Time{
		base,
		base.Add(500 * time.Millisecond),
		base.Add(500*time.Millisecond + time.Nanosecond),
		base.Add(time.Second).In(plus5h),
		base.Add(2 * time.Second),
	}
	for i := 1; i < len(times); i++ {
		a, b := formatTime(times[i-1]), formatTime(times[i])
		if !(a < b) {
			t.Errorf("formatTime(%v) = %q is not below formatTime(%v) = %q", times[i-1], a, times[i], b)
		}
	}
}

// Old rows (RFC3339Nano, UTC) sit next to new ones without a migration. An old
// string must never sort below the fixed-width form of the same instant (so a
// holder task read back and re-formatted still matches "created_at >= ?"), and
// old and new values must order correctly across a second boundary.
func TestFormatTime_MixedWithOldLayout(t *testing.T) {
	for _, old := range []string{
		"2026-11-25T10:00:05Z",
		"2026-11-25T10:00:05.5Z",
		"2026-11-25T10:00:05.123Z",
		"2026-11-25T10:00:05.123456789Z",
	} {
		at, err := parseTimeOrZero(old)
		if err != nil {
			t.Fatalf("parse %q: %v", old, err)
		}
		if same := formatTime(at); old < same {
			t.Errorf("old %q sorts below its fixed-width form %q", old, same)
		}
		sec := at.Truncate(time.Second)
		if before := formatTime(sec.Add(-time.Nanosecond)); !(before < old) {
			t.Errorf("new %q (previous second) is not below old %q", before, old)
		}
		if after := formatTime(sec.Add(time.Second)); !(old < after) {
			t.Errorf("old %q is not below new %q (next second)", old, after)
		}
	}
}

// Rows written before the fixed-width layout are plain RFC3339Nano and must
// still be readable.
func TestParseTimeOrZero_OldLayout(t *testing.T) {
	want := time.Date(2026, 11, 25, 10, 0, 5, 500000000, time.UTC)
	for _, s := range []string{
		"2026-11-25T10:00:05.5Z",
		"2026-11-25T05:00:05.5-05:00",
		"2026-11-25T10:00:05.500000000Z",
	} {
		got, err := parseTimeOrZero(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if !got.Equal(want) {
			t.Errorf("parse %q = %v, want %v", s, got, want)
		}
	}
}

func TestListSubmissions_DateBoundsIncludeWholeDay(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	day := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	created := map[string]time.Time{
		"sub_prev-day-end":  day.Add(-time.Nanosecond),
		"sub_midnight":      day,
		"sub_first-second":  day.Add(500 * time.Millisecond),
		"sub_last-second":   day.Add(24*time.Hour - 500*time.Millisecond),
		"sub_next-midnight": day.Add(24 * time.Hour),
	}
	for id, at := range created {
		sub := sampleSubmission(wf.ID)
		sub.ID = id
		sub.CreatedAt = at
		if err := st.CreateSubmission(ctx, sub); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	opts := model.DefaultListOptions()
	opts.DateStart = "2026-11-25"
	opts.DateEnd = "2026-11-25"
	subs, total, err := st.ListSubmissions(ctx, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, s := range subs {
		got[s.ID] = true
	}
	want := []string{"sub_midnight", "sub_first-second", "sub_last-second"}
	if total != len(want) || len(got) != len(want) {
		t.Errorf("total = %d, listed = %v, want exactly %v", total, got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("%s is missing from the day's list", id)
		}
	}
}
