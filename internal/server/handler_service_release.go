package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// ForceReleaseRefused is the 403 message of every refused force release by
// who the caller is: an anonymous caller, a service identity, a user who is
// neither a server admin nor a manager of any worker group, and a manager of
// other groups than the task's. It never names a group, so a refused caller
// learns nothing about the task.
const ForceReleaseRefused = "only a server admin or a manager of the service's worker group may force-release it"

// The worker_notified values of a force release: when the worker is told to
// stop the service (the heartbeat's cancel_tasks lists a released service
// the worker still reports).
const (
	ForceReleaseNotifiedNextHeartbeat = "on its next heartbeat"
	ForceReleaseNotifiedIfBack        = "on its next heartbeat, if it comes back"
	ForceReleaseNotifiedNever         = "never: its worker record no longer exists"
)

// handleForceReleaseService ends a RUNNING service task whose worker cannot
// confirm that it stopped (the worker is lost, service_lost_at, or its row is
// gone) or has not confirmed a stop request older than
// model.ServiceStopReleaseAfter, so its charged cores, memory and GPUs no
// longer count. The process may still run on the worker's host; the caller
// takes that risk.
//
// Who: a server admin or a manager of the task's worker group. Anyone else
// (an anonymous caller, a service identity, a user who manages no group) is
// refused with 403 ForceReleaseRefused before anything is looked up, and a
// manager of other groups gets the same 403 for an existing task; the
// submission owner gets no right from ownership. Unknown submission or task,
// or a task of another submission: 404, as the neighbouring task routes;
// except that a server admin may release a task whose submission row is gone
// (a task with that id whose submission_id is the {id} of the URL), the
// worker then being told to stop it on its next heartbeat. Every other
// refusal (not a service, not RUNNING, worker online) is a 409 from
// store.ForceReleaseService, which checks and writes in one transaction.
//
// The answer holds only id, state, external_id, completed_at and
// worker_notified (when the worker is told to stop it).
// POST /api/v1/submissions/{id}/tasks/{tid}/force-release
func (s *Server) handleForceReleaseService(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	subID := chi.URLParam(r, "id")
	tid := chi.URLParam(r, "tid")

	refuse := func() {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{Code: model.ErrForbidden, Message: ForceReleaseRefused})
	}
	uc := UserFromContext(r.Context())
	if uc == nil || uc.User == nil || uc.User.IsAnonymous() || isServiceCaller(uc) {
		refuse()
		return
	}
	actor := requestActor(r)
	// Before any lookup, so a caller without any such right cannot probe
	// which submissions and tasks exist.
	if !actor.IsAdmin && len(s.groupPolicy.ManagedGroups(actor.Username)) == 0 {
		refuse()
		return
	}

	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	task, err := s.store.GetTask(r.Context(), tid)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if sub == nil {
		// A task left without its submission row (created as the submission
		// was deleted): only a server admin may release it.
		if !actor.IsAdmin || task == nil || task.SubmissionID != subID {
			respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", subID))
			return
		}
	}
	if task == nil || task.SubmissionID != subID {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		return
	}

	// The group never changes after the task is created, so it can be read
	// before the store's transaction.
	group := ""
	if task.RuntimeHints != nil {
		group = task.RuntimeHints.WorkerGroup
	}
	if !s.mayManageGroup(actor, group) {
		refuse()
		return
	}

	released, err := s.store.ForceReleaseService(r.Context(), subID, tid, actor.Username)
	if err != nil {
		var refused *store.ServiceReleaseError
		switch {
		case errors.Is(err, store.ErrTaskNotFound):
			respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("task", tid))
		case errors.As(err, &refused):
			respondError(w, reqID, http.StatusConflict, &model.APIError{Code: model.ErrConflict, Message: refused.Reason})
		default:
			respondError(w, reqID, http.StatusInternalServerError,
				&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		}
		return
	}

	// Recorded like a worker's /complete: once, behind the store's CAS.
	s.metrics.ObserveTaskTerminal(released, s.workflowNameFor(r.Context(), subID), "force_release")
	if released.State == model.TaskStateSkipped {
		s.metrics.AddTasksSkipped(1)
	}

	hostID := ""
	notified := ForceReleaseNotifiedNever
	if wk, err := s.store.GetWorker(r.Context(), released.ExternalID); err == nil && wk != nil {
		hostID = wk.HostID
		notified = ForceReleaseNotifiedNextHeartbeat
		if released.ServiceLostAt != nil {
			notified = ForceReleaseNotifiedIfBack
		}
	}
	s.logger.Warn("service task force-released: its worker did not confirm that it stopped; its process and GPUs may still be in use",
		"actor", actor.Username, "task_id", released.ID, "submission_id", subID,
		"worker_id", released.ExternalID, "host_id", hostID, "state", released.State,
		"submission_exists", sub != nil, "worker_notified", notified)

	respondOK(w, reqID, map[string]any{
		"id":              released.ID,
		"state":           released.State,
		"external_id":     released.ExternalID,
		"completed_at":    released.CompletedAt,
		"worker_notified": notified,
	})
}
