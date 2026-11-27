package scheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/cancelseq"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// goneBeforeInsertStore runs gone on the submission of every task right
// before delegating CreateTask: a delete or cancel that commits between the
// tick's read of the submission and the insert.
type goneBeforeInsertStore struct {
	store.Store
	gone  func(ctx context.Context, st store.Store, subID string) error
	calls int
}

func (s *goneBeforeInsertStore) CreateTask(ctx context.Context, task *model.Task) error {
	s.calls++
	if err := s.gone(ctx, s.Store, task.SubmissionID); err != nil {
		return err
	}
	return s.Store.CreateTask(ctx, task)
}

// F1b: a service step whose submission is deleted, or cancelled, between
// the tick's read and the insert gets no task row; the tick ends without an
// error and later ticks do not try again.
func TestDispatchStep_ServiceSubmissionGoneBeforeInsert(t *testing.T) {
	for _, c := range []struct {
		name string
		gone func(ctx context.Context, st store.Store, subID string) error
	}{
		{"deleted", func(ctx context.Context, st store.Store, subID string) error {
			return st.DeleteSubmission(ctx, subID)
		}},
		{"cancelled", func(ctx context.Context, st store.Store, subID string) error {
			sub, err := st.GetSubmission(ctx, subID)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			sub.State, sub.CompletedAt = model.SubmissionStateCancelled, &now
			_, err = cancelseq.Run(ctx, st, nil, slog.Default(), sub, now)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			sched, st := newServiceSched(t)
			subID := registerGraphWorkflowInputsAs(t, st, serviceStepCWL, map[string]any{"port": 8000}, nil, "alice")
			gone := &goneBeforeInsertStore{Store: st, gone: c.gone}
			sched.store = gone

			for tick := 1; tick <= 3; tick++ {
				if err := sched.Tick(ctx); err != nil {
					t.Fatalf("tick %d: %v", tick, err)
				}
			}
			if tasks, err := st.ListTasksBySubmission(ctx, subID); err != nil || len(tasks) != 0 {
				t.Errorf("tasks = %+v, %v; want no task row", tasks, err)
			}
			if gone.calls != 1 {
				t.Errorf("CreateTask calls = %d, want 1 (nothing tried again)", gone.calls)
			}
		})
	}
}

// A CANCELLED submission whose service is still RUNNING (a cancel keeps it
// RUNNING with a stop request until its worker reports) goes through ticks
// without an error, a new task, a state change or a re-dispatch; once the
// worker has reported the service stopped, the submission stays CANCELLED.
func TestCancelledSubmission_RunningServiceTicks(t *testing.T) {
	ctx := context.Background()
	sched, st := newServiceSched(t)
	subID, first := dispatchServiceStep(t, sched, st)
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{ID: "wrk_svc", Name: "svc", Group: "llm", State: model.WorkerStateOnline,
		Runtime: model.RuntimeApptainer, Labels: map[string]string{}, LastSeen: now, RegisteredAt: now, ServiceMode: true}); err != nil {
		t.Fatal(err)
	}
	running, err := st.CheckoutTask(ctx, "wrk_svc", "llm", model.RuntimeApptainer)
	if err != nil || running == nil || running.ID != first.ID {
		t.Fatalf("CheckoutTask = (%v, %v), want %s", running, err, first.ID)
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil || sub == nil {
		t.Fatalf("GetSubmission = (%v, %v)", sub, err)
	}
	sub.State, sub.CompletedAt = model.SubmissionStateCancelled, &now
	if _, err := cancelseq.Run(ctx, st, nil, sched.logger, sub, now); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, when string, wantTask model.TaskState) {
		t.Helper()
		tasks := hintsByStep(t, sched, subID)["s"]
		if len(tasks) != 1 || tasks[0].ID != first.ID {
			t.Fatalf("%s: tasks = %+v, want only %s", when, tasks, first.ID)
		}
		if tk := tasks[0]; tk.State != wantTask || tk.ExternalID != "wrk_svc" || tk.ServiceStopRequestedAt == nil {
			t.Fatalf("%s: task = %s on %q (stop %v), want %s on wrk_svc with a stop request",
				when, tk.State, tk.ExternalID, tk.ServiceStopRequestedAt, wantTask)
		}
		if si := getStepInstancesByStep(t, st, subID)["s"]; si.State != model.StepStateSkipped {
			t.Fatalf("%s: step = %s %q, want SKIPPED", when, si.State, si.Error)
		}
		if got, err := st.GetSubmission(ctx, subID); err != nil || got == nil || got.State != model.SubmissionStateCancelled {
			t.Fatalf("%s: submission = %+v, %v; want CANCELLED", when, got, err)
		}
	}

	check(t, "after cancel", model.TaskStateRunning)
	for tick := 1; tick <= 5; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		check(t, "tick", model.TaskStateRunning)
	}

	// The worker reports the service stopped (what /complete stores).
	running.State, running.CompletedAt = model.TaskStateSkipped, &now
	if applied, err := st.TerminalizeTask(ctx, running); err != nil || !applied {
		t.Fatalf("TerminalizeTask = (%v, %v)", applied, err)
	}
	for tick := 1; tick <= 3; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d after the report: %v", tick, err)
		}
		check(t, "tick after the report", model.TaskStateSkipped)
	}
}
