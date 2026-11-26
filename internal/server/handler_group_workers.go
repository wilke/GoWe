package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/ui"
	"github.com/me/gowe/pkg/model"
)

// The workers of a group for its managers and server admins
// (GET/DELETE /api/v1/worker-groups/{group}/workers, through the operations
// ListGroupWorkers and RemoveGroupWorker shared with the web UI), and the
// admin removal of any worker (DELETE /api/v1/admin/workers/{id}).
//
// A worker that stops heartbeating is marked offline by the reaper and its
// row stays listed until it is removed here. Only a worker that is offline
// and has sent no heartbeat for workerRemovalSilence (3 x the reaper's
// timeout) is removed: "offline" alone does not mean stopped (a heartbeat
// brings it back), and removing a worker requeues its RUNNING tasks, so
// removing a live one could run a task twice. The check is part of the
// store's delete (DeleteOfflineWorker), not a read before it. A worker
// process still running when its row is removed is answered 404 from then on
// and does not register again by itself.

// groupWorkerOtherKey is the key_id shown for a worker whose key is not one
// of the group's own keys (groups exactly [group]).
const groupWorkerOtherKey = ui.GroupWorkerOtherKey

// groupWorker is a worker as listed to the managers of its group: a
// projection of model.Worker, not the full record (defined in the ui
// package, which renders it too).
type groupWorker = ui.GroupWorker

// workerGroupName is the group of wk, with "" folded to the default group.
func workerGroupName(wk *model.Worker) string {
	if wk.Group == "" {
		return groupacl.DefaultGroup
	}
	return wk.Group
}

// workerRemovable reports whether wk passes the removal rule of
// DeleteOfflineWorker at now: offline and last seen more than silentFor ago.
// Only a hint for listings; the removal decides inside its delete.
func workerRemovable(wk *model.Worker, silentFor time.Duration, now time.Time) bool {
	return wk.State == model.WorkerStateOffline && wk.LastSeen.Before(now.Add(-silentFor))
}

// handleListWorkerGroupWorkers lists the workers of a group (whatever key
// they registered with), offline ones included, sorted by name.
// GET /api/v1/worker-groups/{group}/workers
func (s *Server) handleListWorkerGroupWorkers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	out, err := s.ListGroupWorkers(r.Context(), requestActor(r), chi.URLParam(r, "group"))
	if err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{
		"total":   len(out),
		"workers": out,
	})
}

// handleRemoveWorkerGroupWorker removes an offline, silent worker of a group
// (see RemoveGroupWorker).
// DELETE /api/v1/worker-groups/{group}/workers/{id}
func (s *Server) handleRemoveWorkerGroupWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.RemoveGroupWorker(r.Context(), requestActor(r), chi.URLParam(r, "group"), id); err != nil {
		respondGroupError(w, reqID, err)
		return
	}
	respondWorkerRemoved(w, reqID, id)
}

// handleAdminRemoveWorker removes an offline, silent worker of any group
// (see removeWorker).
// DELETE /api/v1/admin/workers/{id}
func (s *Server) handleAdminRemoveWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	ctx, cancel := writeContext(r)
	defer cancel()
	if e := s.removeWorker(ctx, requestActor(r), id, ""); e != nil {
		respondGroupError(w, reqID, e)
		return
	}
	respondWorkerRemoved(w, reqID, id)
}

// respondWorkerRemoved answers 200 for a removed worker.
func respondWorkerRemoved(w http.ResponseWriter, reqID, id string) {
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"removed": true,
	})
}

// removeWorker deletes worker id with DeleteOfflineWorker (silent for
// workerRemovalSilence; in group unless group is ""). It returns not found
// when there is no such worker (in group), or a conflict when the worker
// exists but is not removable (yet).
func (s *Server) removeWorker(ctx context.Context, actor ui.GroupActor, id, group string) *ui.GroupError {
	notFound := func() *ui.GroupError {
		kind := "worker"
		if group != "" {
			kind = "worker of group " + group
		}
		return groupErr(ui.GroupErrNotFound, model.NewNotFoundError(kind, id).Message)
	}
	inScope := func(wk *model.Worker) bool {
		return wk != nil && (group == "" || workerGroupName(wk) == group)
	}

	wk, err := s.store.GetWorker(ctx, id)
	if err != nil {
		return s.internalErrFor(actor, group, err)
	}
	if !inScope(wk) {
		return notFound()
	}
	deleted, err := s.store.DeleteOfflineWorker(ctx, id, group, workerRemovalSilence)
	if err != nil {
		return s.internalErrFor(actor, group, err)
	}
	if !deleted {
		// The delete decided; a fresh read only explains why.
		if cur, err := s.store.GetWorker(ctx, id); err == nil {
			if !inScope(cur) {
				return notFound()
			}
			wk = cur
		}
		return groupErr(ui.GroupErrConflict, workerNotRemovableMessage(wk, workerRemovalSilence, time.Now()))
	}
	s.logger.Info("worker removed", "actor", actor.Username, "worker_id", id, "group", workerGroupName(wk))
	return nil
}

// workerNotRemovableMessage tells why wk cannot be removed and what to do;
// silentFor is the removal wait (workerRemovalSilence).
func workerNotRemovableMessage(wk *model.Worker, silentFor time.Duration, now time.Time) string {
	if wk.State != model.WorkerStateOffline {
		return fmt.Sprintf("worker %s is %s: stop it on its host (or revoke its key) and wait until it is offline; it can be removed once it has sent no heartbeat for %s (3 x the worker timeout)",
			wk.ID, wk.State, silentFor)
	}
	return fmt.Sprintf("worker %s is offline but was last seen %s ago: it can be removed once it has sent no heartbeat for %s (3 x the worker timeout)",
		wk.ID, now.Sub(wk.LastSeen).Truncate(time.Second), silentFor)
}
