package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/me/gowe/pkg/model"
)

func hostIntP(v int) *int       { return &v }
func hostInt64P(v int64) *int64 { return &v }

func TestRefreshHostGauges(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fresh, older, stale := now.Add(-10*time.Second), now.Add(-time.Minute), now.Add(-5*time.Minute)
	gpu0 := model.GPUInfo{Index: 0, UUID: "GPU-0", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}
	gpu1 := model.GPUInfo{Index: 1, UUID: "GPU-1", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}

	workers := []*model.Worker{
		// host-a: two online workers sharing GPU-0, plus an offline one.
		{
			ID:        "a1",
			State:     model.WorkerStateOnline,
			HostID:    "host-a",
			HostCPUs:  hostIntP(192),
			HostMemMB: hostInt64P(1547000),
			MaxCPUs:   hostIntP(64),
			MaxMemMB:  hostInt64P(500000),
			GPUs:      []model.GPUInfo{gpu0, gpu1},
			Usage: &model.WorkerUsage{
				Host: &model.HostUsage{Load1: 3.5, MemTotalMB: 1547000, MemAvailableMB: 400000},
				GPUs: []model.GPUUsage{{UUID: "GPU-0", MemUsedMB: hostInt64P(1000)}},
			},
			UsageAt: &fresh,
		},
		{
			ID:        "a2",
			State:     model.WorkerStateOnline,
			HostID:    "host-a",
			HostMemMB: hostInt64P(1600000),
			MaxCPUs:   hostIntP(32),
			MaxMemMB:  hostInt64P(200000),
			GPUs:      []model.GPUInfo{gpu0},
			Usage: &model.WorkerUsage{
				Host: &model.HostUsage{Load1: 1, MemTotalMB: 1547000, MemAvailableMB: 1},
				GPUs: []model.GPUUsage{{UUID: "GPU-0", MemUsedMB: hostInt64P(999)}},
			},
			UsageAt: &older,
		},
		{
			// Offline: counts for the physical totals only, never for the
			// claimed sums or usage, even with the freshest usage.
			ID:       "a3",
			State:    model.WorkerStateOffline,
			HostID:   "host-a",
			HostCPUs: hostIntP(192),
			MaxCPUs:  hostIntP(16),
			MaxMemMB: hostInt64P(1000),
			Usage:    &model.WorkerUsage{Host: &model.HostUsage{Load1: 99, MemTotalMB: 1, MemAvailableMB: 1}},
			UsageAt:  &now,
		},
		// host-b: one online worker whose usage is stale.
		{
			ID:        "b1",
			State:     model.WorkerStateOnline,
			HostID:    "host-b",
			HostCPUs:  hostIntP(8),
			HostMemMB: hostInt64P(16000),
			MaxCPUs:   hostIntP(8),
			MaxMemMB:  hostInt64P(16000),
			Usage:     &model.WorkerUsage{Host: &model.HostUsage{Load1: 2, MemTotalMB: 16000, MemAvailableMB: 8000}},
			UsageAt:   &stale,
		},
		// No host id: skipped entirely.
		{ID: "x1", State: model.WorkerStateOnline, HostCPUs: hostIntP(4), MaxCPUs: hostIntP(4)},
	}

	r := NewRegistry(Config{})
	r.refreshHostGauges(workers, now)

	// Series counts first: WithLabelValues below would create series.
	counts := []struct {
		name string
		vec  *prometheus.GaugeVec
		want int
	}{
		{"cpus: both hosts, none for the empty host id", r.hostCPUs, 2},
		{"claimed cpus", r.hostClaimedCPUs, 2},
		{"load1: stale host-b omitted", r.hostLoad1, 1},
		{"mem available: stale host-b omitted", r.hostMemAvailBytes, 1},
		{"gpu total: shared GPU-0 counted once", r.hostGPUMemTotal, 2},
		{"gpu used: GPU-1 has no usage", r.hostGPUMemUsed, 1},
	}
	for _, c := range counts {
		t.Run("count "+c.name, func(t *testing.T) {
			if got := testutil.CollectAndCount(c.vec); got != c.want {
				t.Errorf("series = %d, want %d", got, c.want)
			}
		})
	}

	const mib = 1 << 20
	values := []struct {
		name   string
		vec    *prometheus.GaugeVec
		labels []string
		want   float64
	}{
		{"cpus host-a", r.hostCPUs, []string{"host-a"}, 192},
		{"cpus host-b", r.hostCPUs, []string{"host-b"}, 8},
		{"mem is the max over workers", r.hostMemBytes, []string{"host-a"}, 1600000 * mib},
		{"claimed cpus sum online only", r.hostClaimedCPUs, []string{"host-a"}, 96},
		{"claimed mem sum online only", r.hostClaimedMemBytes, []string{"host-a"}, 700000 * mib},
		{"claimed cpus host-b", r.hostClaimedCPUs, []string{"host-b"}, 8},
		{"load1 from the freshest online usage", r.hostLoad1, []string{"host-a"}, 3.5},
		{"mem available from the freshest online usage", r.hostMemAvailBytes, []string{"host-a"}, 400000 * mib},
		{"gpu 0 total", r.hostGPUMemTotal, []string{"host-a", "0"}, 143771 * mib},
		{"gpu 1 total", r.hostGPUMemTotal, []string{"host-a", "1"}, 143771 * mib},
		{"gpu 0 used from the freshest report", r.hostGPUMemUsed, []string{"host-a", "0"}, 1000 * mib},
	}
	for _, c := range values {
		t.Run(c.name, func(t *testing.T) {
			if got := testutil.ToFloat64(c.vec.WithLabelValues(c.labels...)); got != c.want {
				t.Errorf("value = %v, want %v", got, c.want)
			}
		})
	}

	// A refresh without host-a drops its series (stale label sets reset).
	r.refreshHostGauges(workers[3:4], now)
	if got := testutil.CollectAndCount(r.hostCPUs); got != 1 {
		t.Errorf("cpus series after host-a left = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(r.hostGPUMemTotal); got != 0 {
		t.Errorf("gpu total series after host-a left = %d, want 0", got)
	}
}

func TestRefreshGauges_IncludesHostGauges(t *testing.T) {
	r := NewRegistry(Config{})
	at := time.Now().UTC()
	w := &model.Worker{
		State:    model.WorkerStateOnline,
		HostID:   "h",
		HostCPUs: hostIntP(4),
		Usage:    &model.WorkerUsage{Host: &model.HostUsage{Load1: 0.5, MemTotalMB: 1024, MemAvailableMB: 512}},
		UsageAt:  &at,
	}
	r.RefreshGauges(nil, nil, []*model.Worker{w}, nil)
	if got := testutil.ToFloat64(r.hostCPUs.WithLabelValues("h")); got != 4 {
		t.Errorf("gowe_host_cpus{host=h} = %v, want 4", got)
	}
	if got := testutil.ToFloat64(r.hostLoad1.WithLabelValues("h")); got != 0.5 {
		t.Errorf("gowe_host_load1{host=h} = %v, want 0.5", got)
	}
}
