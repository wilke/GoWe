package model

import (
	"cmp"
	"slices"
	"time"
)

// HostUsageMaxAge is how old (by server receive time) a worker's usage may be
// before it no longer counts towards its host's usage (#323).
const HostUsageMaxAge = 2 * time.Minute

// LegacyHostIDPrefix prefixes the synthetic host id of workers that did not
// report a host id: they are grouped by hostname under
// LegacyHostIDPrefix+hostname. A reported host id can never collide with it,
// since ValidateHostID does not allow ':'.
const LegacyHostIDPrefix = "hostname:"

// Host is the read-only per-host view of the workers sharing a host id
// (#323). Capacity, claimed totals, usage and GPUs come from the host's
// online workers only; Workers lists all of them, including offline ones.
type Host struct {
	HostID string `json:"host_id"`
	// Legacy marks a host grouped by hostname because its workers did not
	// report a host id.
	Legacy bool `json:"legacy,omitempty"`
	// Hostnames are the sorted, distinct hostnames of the online workers.
	Hostnames []string `json:"hostnames,omitempty"`
	// CPUs and MemMB are the physical totals: the max reported by the online
	// workers, 0 (omitted) when unknown.
	CPUs  int   `json:"cpus,omitempty"`
	MemMB int64 `json:"mem_mb,omitempty"`
	// ClaimedCPUs and ClaimedMemMB sum max_cpus / max_mem_mb over the online
	// workers.
	ClaimedCPUs  int   `json:"claimed_cpus"`
	ClaimedMemMB int64 `json:"claimed_mem_mb"`
	// Usage is the most recent host usage among the online workers, nil when
	// none is at most HostUsageMaxAge old. UsageAt is its server receive time.
	Usage   *HostUsage `json:"usage,omitempty"`
	UsageAt *time.Time `json:"usage_at,omitempty"`
	// GPUs is the inventory of the online workers, one entry per distinct uuid,
	// sorted by index then uuid.
	GPUs          []HostGPU    `json:"gpus,omitempty"`
	Workers       []HostWorker `json:"workers"`
	WorkersOnline int          `json:"workers_online"`
	WorkersTotal  int          `json:"workers_total"`
	RunningTasks  int          `json:"running_tasks"`
}

// HostGPU is one GPU of a host with its freshest usage. MemUsedMB and UtilPct
// are nil when unknown or stale. UsageAt is the server receive time of the
// worker usage that supplied MemUsedMB (UtilPct when there is no MemUsedMB),
// nil when the GPU has no fresh sample.
type HostGPU struct {
	Index      int        `json:"index"`
	UUID       string     `json:"uuid"`
	Name       string     `json:"name"`
	MemTotalMB int64      `json:"mem_total_mb"`
	MemUsedMB  *int64     `json:"mem_used_mb,omitempty"`
	UtilPct    *float64   `json:"util_pct,omitempty"`
	UsageAt    *time.Time `json:"usage_at,omitempty"`
}

// HostWorker is the slim projection of a worker listed under its host.
type HostWorker struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Group        string           `json:"group"`
	State        WorkerState      `json:"state"`
	Runtime      ContainerRuntime `json:"runtime"`
	Slots        *int             `json:"slots,omitempty"`
	MaxCPUs      *int             `json:"max_cpus,omitempty"`
	MaxMemMB     *int64           `json:"max_mem_mb,omitempty"`
	GPUEnabled   bool             `json:"gpu_enabled,omitempty"`
	GPUDevice    string           `json:"gpu_device,omitempty"`
	Version      string           `json:"version,omitempty"`
	LastSeen     time.Time        `json:"last_seen"`
	RunningTasks int              `json:"running_tasks"`
}

// AggregateHosts groups workers by host id into the per-host view, sorted by
// host id. Workers without a host id are grouped by hostname under
// LegacyHostIDPrefix+hostname and flagged Legacy. A host is listed when it has
// at least one worker, but only online workers count towards its capacity,
// claimed totals, usage and GPUs; their usage counts only when it is at most
// HostUsageMaxAge old at now, the most recent sample winning. GPUs are
// deduplicated by uuid and sorted by (index, uuid); two uuids sharing an index
// are both listed. runningByWorker maps a worker id to its running task
// count and may be nil. The result is never nil.
func AggregateHosts(workers []*Worker, runningByWorker map[string]int, now time.Time) []Host {
	type group struct {
		legacy  bool
		workers []*Worker
	}
	groups := make(map[string]*group)
	for _, w := range workers {
		if w == nil {
			continue
		}
		id, legacy := w.HostID, false
		if id == "" {
			id, legacy = LegacyHostIDPrefix+w.Hostname, true
		}
		g := groups[id]
		if g == nil {
			g = &group{legacy: legacy}
			groups[id] = g
		}
		g.workers = append(g.workers, w)
	}

	hosts := make([]Host, 0, len(groups))
	for id, g := range groups {
		hosts = append(hosts, aggregateHost(id, g.legacy, g.workers, runningByWorker, now))
	}
	slices.SortFunc(hosts, func(a, b Host) int { return cmp.Compare(a.HostID, b.HostID) })
	return hosts
}

// gpuSample is the freshest known usage of one GPU uuid.
type gpuSample struct {
	memUsedMB *int64
	memAt     time.Time
	utilPct   *float64
	utilAt    time.Time
}

// aggregateHost builds one host from its workers (all non-nil, owned by the
// caller and sorted here in place).
func aggregateHost(id string, legacy bool, workers []*Worker, runningByWorker map[string]int, now time.Time) Host {
	// Sorting first makes the result independent of the input order, also
	// for ties (equal usage times, the same GPU uuid on two workers).
	slices.SortFunc(workers, func(a, b *Worker) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})

	h := Host{
		HostID:       id,
		Legacy:       legacy,
		Workers:      make([]HostWorker, 0, len(workers)),
		WorkersTotal: len(workers),
	}
	hostnames := make(map[string]struct{})
	gpus := make(map[string]GPUInfo)
	samples := make(map[string]*gpuSample)

	for _, w := range workers {
		running := runningByWorker[w.ID]
		h.RunningTasks += running
		h.Workers = append(h.Workers, HostWorker{
			ID:           w.ID,
			Name:         w.Name,
			Group:        w.Group,
			State:        w.State,
			Runtime:      w.Runtime,
			Slots:        w.Slots,
			MaxCPUs:      w.MaxCPUs,
			MaxMemMB:     w.MaxMemMB,
			GPUEnabled:   w.GPUEnabled,
			GPUDevice:    w.GPUDevice,
			Version:      w.Version,
			LastSeen:     w.LastSeen,
			RunningTasks: running,
		})

		if w.State != WorkerStateOnline {
			continue
		}
		h.WorkersOnline++
		if w.Hostname != "" {
			hostnames[w.Hostname] = struct{}{}
		}
		if w.HostCPUs != nil {
			h.CPUs = max(h.CPUs, *w.HostCPUs)
		}
		if w.HostMemMB != nil {
			h.MemMB = max(h.MemMB, *w.HostMemMB)
		}
		if w.MaxCPUs != nil {
			h.ClaimedCPUs += *w.MaxCPUs
		}
		if w.MaxMemMB != nil {
			h.ClaimedMemMB += *w.MaxMemMB
		}
		for _, g := range w.GPUs {
			if _, ok := gpus[g.UUID]; !ok {
				gpus[g.UUID] = g
			}
		}

		if w.Usage == nil || w.UsageAt == nil || now.Sub(*w.UsageAt) > HostUsageMaxAge {
			continue
		}
		at := *w.UsageAt
		if w.Usage.Host != nil && (h.Usage == nil || at.After(*h.UsageAt)) {
			u := *w.Usage.Host
			h.Usage, h.UsageAt = &u, &at
		}
		for _, u := range w.Usage.GPUs {
			s := samples[u.UUID]
			if s == nil {
				s = &gpuSample{}
				samples[u.UUID] = s
			}
			if u.MemUsedMB != nil && (s.memUsedMB == nil || at.After(s.memAt)) {
				v := *u.MemUsedMB
				s.memUsedMB, s.memAt = &v, at
			}
			if u.UtilPct != nil && (s.utilPct == nil || at.After(s.utilAt)) {
				v := *u.UtilPct
				s.utilPct, s.utilAt = &v, at
			}
		}
	}

	for name := range hostnames {
		h.Hostnames = append(h.Hostnames, name)
	}
	slices.Sort(h.Hostnames)

	inventory := make([]GPUInfo, 0, len(gpus))
	for _, g := range gpus {
		inventory = append(inventory, g)
	}
	slices.SortFunc(inventory, func(a, b GPUInfo) int {
		return cmp.Or(cmp.Compare(a.Index, b.Index), cmp.Compare(a.UUID, b.UUID))
	})
	for _, g := range inventory {
		hg := HostGPU{Index: g.Index, UUID: g.UUID, Name: g.Name, MemTotalMB: g.MemTotalMB}
		if s := samples[g.UUID]; s != nil {
			hg.MemUsedMB, hg.UtilPct = s.memUsedMB, s.utilPct
			switch {
			case s.memUsedMB != nil:
				at := s.memAt
				hg.UsageAt = &at
			case s.utilPct != nil:
				at := s.utilAt
				hg.UsageAt = &at
			}
		}
		h.GPUs = append(h.GPUs, hg)
	}
	return h
}
