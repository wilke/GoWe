package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestListSteps_KeepCreationOrderAfterUpdates: the step instances of one
// submission are created with timestamps one nanosecond apart (as the
// submission handlers do) and must come back in creation order after some of
// them were updated. On PostgreSQL an UPDATE writes a new row version
// elsewhere, so rows with equal sort keys would come back reordered.
func TestListSteps_KeepCreationOrderAfterUpdates(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	const n = 12
	const subID = "sub_order-1"
	now := time.Now().UTC()
	steps := make([]*model.StepInstance, 0, n)
	for i := 0; i < n; i++ {
		steps = append(steps, &model.StepInstance{
			// IDs and step IDs sort opposite to creation order, so neither
			// can produce the expected order by accident.
			ID:           fmt.Sprintf("si_order_%02d", n-1-i),
			SubmissionID: subID,
			StepID:       fmt.Sprintf("step_%02d", n-1-i),
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now.Add(time.Duration(i) * time.Nanosecond),
		})
	}
	if err := st.BatchCreateStepInstances(ctx, steps); err != nil {
		t.Fatalf("BatchCreateStepInstances: %v", err)
	}
	// A step of another submission, created at the same instant, must not
	// show up.
	other := &model.StepInstance{
		ID: "si_order_other", SubmissionID: "sub_order-2", StepID: "x",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := st.CreateStepInstance(ctx, other); err != nil {
		t.Fatalf("CreateStepInstance: %v", err)
	}

	// Update the first steps (and some of them twice), the way the
	// scheduler advances them.
	big := strings.Repeat("x", 4096)
	for round, state := range []model.StepInstanceState{model.StepStateDispatched, model.StepStateRunning} {
		for i := 0; i < n; i += round + 1 {
			if i%3 == 2 {
				continue // leave some steps WAITING
			}
			si := steps[i]
			si.State = state
			si.Outputs = map[string]any{"round": round, "pad": big}
			if err := st.UpdateStepInstance(ctx, si); err != nil {
				t.Fatalf("UpdateStepInstance %s: %v", si.ID, err)
			}
		}
	}

	wantIDs := func(filter func(*model.StepInstance) bool) []string {
		var ids []string
		for _, si := range steps {
			if filter(si) {
				ids = append(ids, si.ID)
			}
		}
		return ids
	}
	gotIDs := func(list []*model.StepInstance) []string {
		ids := make([]string, 0, len(list))
		for _, si := range list {
			ids = append(ids, si.ID)
		}
		return ids
	}

	tests := []struct {
		name string
		list func() ([]*model.StepInstance, error)
		want []string
	}{
		{
			name: "ListStepsBySubmission",
			list: func() ([]*model.StepInstance, error) { return st.ListStepsBySubmission(ctx, subID) },
			want: wantIDs(func(*model.StepInstance) bool { return true }),
		},
		{
			name: "ListStepsByState RUNNING",
			list: func() ([]*model.StepInstance, error) { return st.ListStepsByState(ctx, model.StepStateRunning) },
			want: wantIDs(func(si *model.StepInstance) bool { return si.State == model.StepStateRunning }),
		},
		{
			name: "ListStepsByState DISPATCHED",
			list: func() ([]*model.StepInstance, error) { return st.ListStepsByState(ctx, model.StepStateDispatched) },
			want: wantIDs(func(si *model.StepInstance) bool { return si.State == model.StepStateDispatched }),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.want) == 0 {
				t.Fatal("test setup: no step expected")
			}
			list, err := tc.list()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			got := gotIDs(list)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("order\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}
