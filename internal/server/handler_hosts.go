package server

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/me/gowe/pkg/model"
)

// hostsData is the shape returned under the response envelope's "data" key.
type hostsData struct {
	Hosts []model.Host `json:"hosts"`
}

// legacyHostRedactedPrefix replaces model.LegacyHostIDPrefix in the host id
// of a legacy host shown to a non-admin caller.
const legacyHostRedactedPrefix = "legacy:"

// handleListHosts returns the read-only per-host view (#323): workers grouped
// by host id with capacity, claimed totals, usage and GPUs, computed by
// model.AggregateHosts exactly like the gowe_host_* gauges. Like
// handleFleetRoster it sits behind apiAuthMiddleware (any signed-in user, no
// worker key), and like /fleet it shows no hostnames to non-admin callers
// (see redactHostnames).
//
// GET /api/v1/hosts
func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	running, err := s.store.CountRunningTasksByWorker(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	hosts := model.AggregateHosts(workers, running, time.Now())
	if uc := UserFromContext(r.Context()); uc == nil || uc.User == nil || !uc.User.IsAdmin() {
		hosts = redactHostnames(hosts)
	}
	respondOK(w, reqID, hostsData{Hosts: hosts})
}

// redactHostnames applies the /fleet policy (no hostnames for a user-facing
// status view) to the per-host view: it drops every host's hostnames and
// replaces the id of a legacy host, which embeds its hostname, with
// "legacy:" plus the smallest id of its workers. Hosts are re-sorted by id.
func redactHostnames(hosts []model.Host) []model.Host {
	for i := range hosts {
		h := &hosts[i]
		h.Hostnames = nil
		if !h.Legacy {
			continue
		}
		minID := ""
		for _, wk := range h.Workers {
			if minID == "" || wk.ID < minID {
				minID = wk.ID
			}
		}
		h.HostID = legacyHostRedactedPrefix + minID
	}
	slices.SortFunc(hosts, func(a, b model.Host) int { return cmp.Compare(a.HostID, b.HostID) })
	return hosts
}
