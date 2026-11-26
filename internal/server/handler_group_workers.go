package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
)

// The workers of a group for its managers and server admins
// (GET/DELETE /api/v1/worker-groups/{group}/workers), and the admin removal
// of any worker (DELETE /api/v1/admin/workers/{id}).
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
const groupWorkerOtherKey = "other"

// groupWorker is a worker as listed to the managers of its group: a
// projection of model.Worker, not the full record.
type groupWorker struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Hostname     string                 `json:"hostname"`
	PeerAddr     string                 `json:"peer_addr"`
	State        model.WorkerState      `json:"state"`
	LastSeen     time.Time              `json:"last_seen"`
	RegisteredAt time.Time              `json:"registered_at"`
	Version      string                 `json:"version"`
	Runtime      model.ContainerRuntime `json:"runtime"`
	GPUEnabled   bool                   `json:"gpu_enabled"`
	CurrentTask  string                 `json:"current_task"`
	// KeyID is the id of the registering key when its groups are exactly
	// [group], groupWorkerOtherKey for any other key, "" when the worker is
	// bound to no key (worker auth was off when it registered).
	KeyID string `json:"key_id"`
}

// workerGroupName is the group of wk, with "" folded to the default group.
func workerGroupName(wk *model.Worker) string {
	if wk.Group == "" {
		return groupacl.DefaultGroup
	}
	return wk.Group
}

// handleListWorkerGroupWorkers lists the workers of a group (whatever key
// they registered with), offline ones included, sorted by name.
// GET /api/v1/worker-groups/{group}/workers
func (s *Server) handleListWorkerGroupWorkers(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, _, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	if !s.requireGroupRule(r.Context(), w, r, reqID, group) {
		return
	}
	keys, err := s.groupKeys(r.Context(), group)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	own := make(map[string]bool, len(keys))
	for _, k := range keys {
		own[k.ID] = true
	}
	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	out := []groupWorker{}
	for _, wk := range workers {
		if workerGroupName(wk) != group {
			continue
		}
		keyID := wk.KeyID
		if keyID != "" && !own[keyID] {
			keyID = groupWorkerOtherKey
		}
		// The caller is a server admin or a manager of group, so the peer
		// address is shown.
		out = append(out, groupWorker{
			ID:           wk.ID,
			Name:         wk.Name,
			Hostname:     wk.Hostname,
			PeerAddr:     wk.PeerAddr,
			State:        wk.State,
			LastSeen:     wk.LastSeen,
			RegisteredAt: wk.RegisteredAt,
			Version:      wk.Version,
			Runtime:      wk.Runtime,
			GPUEnabled:   wk.GPUEnabled,
			CurrentTask:  wk.CurrentTask,
			KeyID:        keyID,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	respondOK(w, reqID, map[string]any{
		"total":   len(out),
		"workers": out,
	})
}

// handleRemoveWorkerGroupWorker removes an offline, silent worker of a group
// (see removeWorker), under the same lock-reload-recheck as the other
// manager writes.
// DELETE /api/v1/worker-groups/{group}/workers/{id}
func (s *Server) handleRemoveWorkerGroupWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	group, user, ok := s.startGroupRoute(w, r, reqID)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	ctx, done, ok := s.beginGroupWrite(w, r, reqID, user, group)
	if !ok {
		return
	}
	defer done()
	s.removeWorker(ctx, w, r, reqID, user.Username, id, group)
}

// handleAdminRemoveWorker removes an offline, silent worker of any group
// (see removeWorker).
// DELETE /api/v1/admin/workers/{id}
func (s *Server) handleAdminRemoveWorker(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	ctx, cancel := writeContext(r)
	defer cancel()
	s.removeWorker(ctx, w, r, reqID, adminName(r), id, "")
}

// removeWorker deletes worker id with DeleteOfflineWorker (silent for
// workerRemovalSilence; in group unless group is "") and answers 200, 404
// when there is no such worker (in group), or 409 when the worker exists
// but is not removable (yet).
func (s *Server) removeWorker(ctx context.Context, w http.ResponseWriter, r *http.Request, reqID, actor, id, group string) {
	notFound := func() {
		kind := "worker"
		if group != "" {
			kind = "worker of group " + group
		}
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError(kind, id))
	}
	inScope := func(wk *model.Worker) bool {
		return wk != nil && (group == "" || workerGroupName(wk) == group)
	}

	wk, err := s.store.GetWorker(ctx, id)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if !inScope(wk) {
		notFound()
		return
	}
	deleted, err := s.store.DeleteOfflineWorker(ctx, id, group, workerRemovalSilence)
	if err != nil {
		s.internalErrForCaller(w, r, reqID, err)
		return
	}
	if !deleted {
		// The delete decided; a fresh read only explains why.
		if cur, err := s.store.GetWorker(ctx, id); err == nil {
			if !inScope(cur) {
				notFound()
				return
			}
			wk = cur
		}
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: workerNotRemovableMessage(wk, workerRemovalSilence, time.Now()),
		})
		return
	}
	s.logger.Info("worker removed", "actor", actor, "worker_id", id, "group", workerGroupName(wk))
	respondOK(w, reqID, map[string]any{
		"id":      id,
		"removed": true,
	})
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
