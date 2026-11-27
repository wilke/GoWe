package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Stopping a service keeps its GPUs until the worker confirms: a cancel keeps
// a RUNNING service RUNNING (charged) with a stop request, the heartbeat
// two-strike rule ends such a service SKIPPED, a submission with a running
// service is not deleted, and a service whose worker cannot confirm can be
// force-released.

// inSubmission moves task tk to submission subID.
func inSubmission(tk *model.Task, subID string) *model.Task {
	tk.SubmissionID = subID
	return tk
}

// ensureLiveSubmission stores a RUNNING submission id unless one exists.
func ensureLiveSubmission(t *testing.T, st *SQLStore, id string) {
	t.Helper()
	ctx := context.Background()
	if sub, err := st.GetSubmission(ctx, id); err != nil {
		t.Fatal(err)
	} else if sub != nil {
		return
	}
	sub := sampleSubmission("wf_test-1")
	sub.ID, sub.State = id, model.SubmissionStateRunning
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission %s: %v", id, err)
	}
}

// A cancel keeps a RUNNING service RUNNING and charged with a stop request
// (set once); a QUEUED service and every ordinary task of the submission are
// SKIPPED exactly as before; a second cancel changes nothing.
func TestCancelNonTerminalTasks_RunningServiceKept(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
	mustCreateWorker(t, st, svcWorker("wrk_b", false, nil))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	seedRunningOn(t, st, "plain", "wrk_b", false, 2, 0)
	mustCreateTask(t, st, svcQueued("svc_q", true, 0, 1, time.Hour))
	mustCreateTask(t, st, svcQueued("plain_q", false, 0, 0, time.Hour))
	// A GPU service of another submission, to probe the GPU charge.
	mustCreateTask(t, st, inSubmission(svcQueued("probe", true, 0, 1, time.Minute), "sub_2"))

	cancelAt := time.Now().UTC().Truncate(time.Millisecond)
	n, err := st.CancelNonTerminalTasks(ctx, "sub_1", cancelAt)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("SKIPPED = %d, want 3 (svc_q, plain, plain_q)", n)
	}

	svc := checkServiceRunning(t, st, "svc", "wrk_s", 1)
	if svc.ServiceStopRequestedAt == nil || !svc.ServiceStopRequestedAt.Equal(cancelAt) {
		t.Errorf("service_stop_requested_at = %v, want %v", svc.ServiceStopRequestedAt, cancelAt)
	}
	if svc.CompletedAt != nil {
		t.Errorf("running service completed_at = %v, want nil", svc.CompletedAt)
	}
	for _, id := range []string{"svc_q", "plain", "plain_q"} {
		tk := mustGetTask(t, st, id)
		if tk.State != model.TaskStateSkipped || tk.CompletedAt == nil || !tk.CompletedAt.Equal(cancelAt) {
			t.Errorf("%s = %s (completed %v), want SKIPPED at %v", id, tk.State, tk.CompletedAt, cancelAt)
		}
		if tk.ServiceStopRequestedAt != nil {
			t.Errorf("%s service_stop_requested_at = %v, want nil", id, tk.ServiceStopRequestedAt)
		}
	}
	// The ordinary running task keeps its owner and charge columns, as before.
	if plain := mustGetTask(t, st, "plain"); plain.ExternalID != "wrk_b" || plain.ChargedCores == nil {
		t.Errorf("plain = on %q, cores %s; want its row otherwise untouched", plain.ExternalID, ptrStr(plain.ChargedCores))
	}
	if probe := mustGetTask(t, st, "probe"); probe.State != model.TaskStateQueued {
		t.Errorf("task of another submission = %s, want QUEUED", probe.State)
	}

	// Still charged: the only GPU of wrk_s is held by the running service.
	if got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone); err != nil || got != nil {
		t.Fatalf("checkout = (%v, %v), want (nil, nil): the stopping service still holds the GPU", got, err)
	}

	// Cancelling twice changes nothing.
	if n, err := st.CancelNonTerminalTasks(ctx, "sub_1", cancelAt.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("second cancel = (%d, %v), want (0, nil)", n, err)
	}
	again := checkServiceRunning(t, st, "svc", "wrk_s", 1)
	if again.ServiceStopRequestedAt == nil || !again.ServiceStopRequestedAt.Equal(cancelAt) {
		t.Errorf("service_stop_requested_at after a second cancel = %v, want the first %v", again.ServiceStopRequestedAt, cancelAt)
	}
}

// A service with a stop request is returned by CancelledTasksForWorker on
// every call while it is RUNNING, even when its submission is not (or no
// longer readable as) CANCELLED; a running service without one is not.
func TestCancelledTasksForWorker_StopRequested(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
	seedRunningOn(t, st, "svc_stop", "wrk_s", true, 0, 0)
	seedRunningOn(t, st, "svc_run", "wrk_s", true, 0, 0)
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET service_stop_requested_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), "svc_stop"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := st.CancelledTasksForWorker(ctx, "wrk_s", []string{"svc_stop", "svc_run"})
		if err != nil || len(got) != 1 || got[0] != "svc_stop" {
			t.Fatalf("call %d: cancel = %v, %v; want [svc_stop]", i+1, got, err)
		}
	}
}

// The heartbeat two-strike rule ends a RUNNING service with a stop request
// SKIPPED (it stopped as asked), not FAILED.
func TestReconcileWorkerTasks_StoppedServiceSkipped(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) {
		oneGPU(w)
		w.CurrentTask = "svc"
	}))
	seedRunningOn(t, st, "svc", "wrk_s", true, 4, 1)
	if _, err := st.CancelNonTerminalTasks(ctx, "sub_1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ReconcileWorkerTasks(ctx, "wrk_s", []string{}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if marked := checkServiceRunning(t, st, "svc", "wrk_s", 1); marked.ServiceMissingAt == nil {
		t.Fatal("first omission: service_missing_at not set")
	}
	setServiceMissingAt(t, st, "svc", time.Now().UTC().Add(-2*time.Minute))
	if _, err := st.ReconcileWorkerTasks(ctx, "wrk_s", []string{}, time.Minute); err != nil {
		t.Fatal(err)
	}

	svc := mustGetTask(t, st, "svc")
	if svc.State != model.TaskStateSkipped || svc.Stderr != ServiceStoppedMissingStderr || svc.CompletedAt == nil {
		t.Fatalf("service = %s stderr %q completed %v, want SKIPPED %q", svc.State, svc.Stderr, svc.CompletedAt, ServiceStoppedMissingStderr)
	}
	if w, err := st.GetWorker(ctx, "wrk_s"); err != nil || w == nil || w.CurrentTask != "" {
		t.Errorf("worker = %+v, %v; want current_task moved off the stopped service", w, err)
	}
	// Its GPU is free again.
	mustCreateTask(t, st, inSubmission(svcQueued("next", true, 0, 1, time.Minute), "sub_2"))
	if got, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone); err != nil || got == nil || got.ID != "next" {
		t.Fatalf("checkout = (%v, %v), want next", got, err)
	}
}

// seedServiceSubmission stores a workflow and submission sub_test-1 and
// returns the submission id.
func seedServiceSubmission(t *testing.T, st *SQLStore) string {
	t.Helper()
	ctx := context.Background()
	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	sub := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatal(err)
	}
	return sub.ID
}

// A submission with a RUNNING service, itself or in a descendant child
// submission, is not deleted (ErrSubmissionHasRunningService, nothing
// removed); once the service has ended it is.
func TestDeleteSubmission_RunningServiceRefused(t *testing.T) {
	t.Run("own task", func(t *testing.T) {
		st := testStore(t)
		ctx := context.Background()
		subID := seedServiceSubmission(t, st)
		mustCreateWorker(t, st, svcWorker("wrk_s", true, nil))
		tk := svcQueued("svc", true, 0, 0, time.Hour)
		tk.SubmissionID, tk.State, tk.ExternalID = subID, model.TaskStateRunning, "wrk_s"
		mustCreateTask(t, st, tk)
		plain := svcQueued("plain", false, 0, 0, time.Hour)
		plain.SubmissionID, plain.State = subID, model.TaskStateSuccess
		mustCreateTask(t, st, plain)

		if running, err := st.HasRunningService(ctx, subID); err != nil || !running {
			t.Fatalf("HasRunningService = (%v, %v), want true", running, err)
		}
		err := st.DeleteSubmission(ctx, subID)
		if !errors.Is(err, ErrSubmissionHasRunningService) {
			t.Fatalf("DeleteSubmission = %v, want ErrSubmissionHasRunningService", err)
		}
		if sub, err := st.GetSubmission(ctx, subID); err != nil || sub == nil {
			t.Errorf("submission = (%v, %v), want kept", sub, err)
		}
		if tasks, err := st.ListTasksBySubmission(ctx, subID); err != nil || len(tasks) != 2 {
			t.Errorf("tasks = %d, %v; want both kept", len(tasks), err)
		}

		// The service ended: the submission is deleted.
		svc := mustGetTask(t, st, "svc")
		now := time.Now().UTC()
		svc.State, svc.CompletedAt = model.TaskStateSkipped, &now
		if err := st.UpdateTask(ctx, svc); err != nil {
			t.Fatal(err)
		}
		if running, err := st.HasRunningService(ctx, subID); err != nil || running {
			t.Fatalf("HasRunningService after the end = (%v, %v), want false", running, err)
		}
		if err := st.DeleteSubmission(ctx, subID); err != nil {
			t.Fatalf("DeleteSubmission after the end = %v", err)
		}
		if sub, _ := st.GetSubmission(ctx, subID); sub != nil {
			t.Error("submission still there")
		}
	})

	t.Run("descendant child submission", func(t *testing.T) {
		st := testStore(t)
		ctx := context.Background()
		parentID := seedServiceSubmission(t, st)
		proxy := svcQueued("proxy", false, 0, 0, time.Hour)
		proxy.SubmissionID, proxy.State, proxy.ExecutorType = parentID, model.TaskStateRunning, model.ExecutorTypeSubworkflow
		mustCreateTask(t, st, proxy)
		child := sampleSubmission(sampleWorkflow().ID)
		child.ID, child.ParentTaskID = "sub_child", "proxy"
		if err := st.CreateSubmission(ctx, child); err != nil {
			t.Fatal(err)
		}
		// A hand-made row: a child never gets a service task.
		tk := svcQueued("svc_child", true, 0, 0, time.Hour)
		tk.SubmissionID, tk.State, tk.ExternalID = "sub_child", model.TaskStateRunning, "wrk_s"
		mustCreateTask(t, st, tk)

		if running, err := st.HasRunningService(ctx, parentID); err != nil || !running {
			t.Fatalf("HasRunningService(parent) = (%v, %v), want true", running, err)
		}
		if err := st.DeleteSubmission(ctx, parentID); !errors.Is(err, ErrSubmissionHasRunningService) {
			t.Fatalf("DeleteSubmission(parent) = %v, want ErrSubmissionHasRunningService", err)
		}
		if proxyRow := mustGetTask(t, st, "proxy"); proxyRow.State != model.TaskStateRunning {
			t.Errorf("proxy = %s, want kept", proxyRow.State)
		}
	})
}

// seedForceRelease stores, in submission sub_1, a RUNNING service "svc" of
// workerID charged one GPU and carrying a secret, with stderr already set.
func seedForceRelease(t *testing.T, st *SQLStore, workerID string) {
	t.Helper()
	tk := svcQueued("svc", true, 0, 1, 2*time.Hour)
	tk.State, tk.ExternalID = model.TaskStateRunning, workerID
	started := time.Now().UTC().Add(-time.Hour)
	tk.StartedAt = &started
	tk.ChargedCores, tk.ChargedRamMB, tk.ChargedGPUs = intP(0), int64P(0), intP(1)
	tk.Stderr = "serving"
	tk.RuntimeHints.Secrets = map[string]string{"HF_TOKEN": "hf_secret"}
	mustCreateTask(t, st, tk)
}

// lostWorker makes w a GPU service worker silent for an hour (the reaper
// then marks its services lost).
func lostWorker(w *model.Worker) {
	oneGPU(w)
	w.LastSeen = time.Now().UTC().Add(-time.Hour)
	w.CurrentTask = "svc"
}

// checkReleased fails unless task svc ended in want with the force-release
// line after its stderr, its secrets scrubbed and no retries left.
func checkReleased(t *testing.T, st *SQLStore, got *model.Task, want model.TaskState) {
	t.Helper()
	stored := mustGetTask(t, st, "svc")
	for _, tk := range []*model.Task{got, stored} {
		if tk.State != want || tk.CompletedAt == nil || tk.ExitCode != nil {
			t.Errorf("task = %s (completed %v, exit %v), want %s", tk.State, tk.CompletedAt, tk.ExitCode, want)
		}
		if wantErr := "serving\n" + ForceReleasedStderr("mgr"); tk.Stderr != wantErr {
			t.Errorf("stderr = %q, want %q", tk.Stderr, wantErr)
		}
		if tk.RuntimeHints == nil || tk.RuntimeHints.Secrets != nil || tk.RuntimeHints.Service == nil {
			t.Errorf("hints = %+v, want secrets scrubbed, service kept", tk.RuntimeHints)
		}
		if tk.RetryCount != tk.MaxRetries {
			t.Errorf("retry %d/%d, want none left", tk.RetryCount, tk.MaxRetries)
		}
	}
}

// Force release: refused (typed reason, nothing written) unless the task of
// the submission is a RUNNING service whose worker cannot confirm a stop;
// otherwise FAILED (SKIPPED with a stop request), uncharged.
func TestForceReleaseService(t *testing.T) {
	t.Run("lost worker", func(t *testing.T) {
		for _, stop := range []bool{false, true} {
			want := model.TaskStateFailed
			name := "no stop request"
			if stop {
				want, name = model.TaskStateSkipped, "stop requested"
			}
			t.Run(name, func(t *testing.T) {
				st := testStore(t)
				ctx := context.Background()
				mustCreateWorker(t, st, svcWorker("wrk_s", true, lostWorker))
				seedForceRelease(t, st, "wrk_s")
				if stop {
					if _, err := st.CancelNonTerminalTasks(ctx, "sub_1", time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := st.ReapStaleWorkers(ctx, time.Minute); err != nil {
					t.Fatal(err)
				}

				got, err := st.ForceReleaseService(ctx, "sub_1", "svc", "mgr")
				if err != nil {
					t.Fatalf("ForceReleaseService = %v", err)
				}
				checkReleased(t, st, got, want)
				if w, err := st.GetWorker(ctx, "wrk_s"); err != nil || w == nil || w.CurrentTask != "" {
					t.Errorf("worker = %+v, %v; want kept, current_task moved off the service", w, err)
				}
				// The GPU is no longer charged: the worker may take a GPU service.
				mustCreateTask(t, st, inSubmission(svcQueued("next", true, 0, 1, time.Minute), "sub_2"))
				if next, err := st.CheckoutTask(ctx, "wrk_s", svcGroup, model.RuntimeNone); err != nil || next == nil || next.ID != "next" {
					t.Fatalf("checkout = (%v, %v), want next", next, err)
				}
			})
		}
	})

	t.Run("worker row missing", func(t *testing.T) {
		st := testStore(t)
		ctx := context.Background()
		seedForceRelease(t, st, "wrk_gone")
		if charges, err := st.SumChargedByWorker(ctx); err != nil {
			t.Fatal(err)
		} else if _, ok := charges["wrk_gone"]; !ok {
			t.Fatalf("charges before = %v, want wrk_gone counted", charges)
		}
		got, err := st.ForceReleaseService(ctx, "sub_1", "svc", "mgr")
		if err != nil {
			t.Fatalf("ForceReleaseService = %v", err)
		}
		checkReleased(t, st, got, model.TaskStateFailed)
		if charges, err := st.SumChargedByWorker(ctx); err != nil || len(charges) != 0 {
			t.Errorf("charges after = %v, %v; want none", charges, err)
		}
	})

	refusals := []struct {
		name    string
		seed    func(t *testing.T, st *SQLStore)
		subID   string
		taskID  string
		wantErr error
		reason  string
	}{
		{"worker online", func(t *testing.T, st *SQLStore) {
			mustCreateWorker(t, st, svcWorker("wrk_s", true, oneGPU))
			seedForceRelease(t, st, "wrk_s")
		}, "sub_1", "svc", ErrServiceNotReleasable, ReleaseWorkerOnline},
		{"worker offline, not yet lost", func(t *testing.T, st *SQLStore) {
			mustCreateWorker(t, st, svcWorker("wrk_s", true, func(w *model.Worker) { w.State = model.WorkerStateOffline }))
			seedForceRelease(t, st, "wrk_s")
		}, "sub_1", "svc", ErrServiceNotReleasable, ReleaseWorkerOnline},
		{"not a service", func(t *testing.T, st *SQLStore) {
			seedRunningOn(t, st, "svc", "wrk_gone", false, 1, 0)
		}, "sub_1", "svc", ErrServiceNotReleasable, ReleaseNotService},
		{"not RUNNING", func(t *testing.T, st *SQLStore) {
			mustCreateTask(t, st, svcQueued("svc", true, 0, 1, time.Hour))
		}, "sub_1", "svc", ErrServiceNotReleasable, ReleaseNotRunning},
		{"another submission", func(t *testing.T, st *SQLStore) {
			seedForceRelease(t, st, "wrk_gone")
		}, "sub_other", "svc", ErrTaskNotFound, ""},
		{"unknown task", func(t *testing.T, st *SQLStore) {}, "sub_1", "svc", ErrTaskNotFound, ""},
	}
	for _, tt := range refusals {
		t.Run("refused: "+tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			tt.seed(t, st)
			var before *model.Task
			if tk, _ := st.GetTask(ctx, tt.taskID); tk != nil {
				before = tk
			}
			got, err := st.ForceReleaseService(ctx, tt.subID, tt.taskID, "mgr")
			if got != nil || !errors.Is(err, tt.wantErr) {
				t.Fatalf("ForceReleaseService = (%v, %v), want %v", got, err, tt.wantErr)
			}
			if tt.reason != "" {
				var re *ServiceReleaseError
				if !errors.As(err, &re) || re.Reason != tt.reason || err.Error() != tt.reason {
					t.Errorf("error = %#v, want reason %q", err, tt.reason)
				}
			}
			if before != nil {
				after := mustGetTask(t, st, tt.taskID)
				if after.State != before.State || strings.Contains(after.Stderr, "force-released") {
					t.Errorf("task = %s stderr %q, want unchanged %s", after.State, after.Stderr, before.State)
				}
			}
		})
	}
}
