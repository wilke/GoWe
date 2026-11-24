package model

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func hostTestPtr[T any](v T) *T { return &v }

// hostsJSON renders hosts as JSON for readable, pointer-free comparison.
func hostsJSON(t *testing.T, hosts []Host) string {
	t.Helper()
	b, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAggregateHosts(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fresh, older, stale := now.Add(-10*time.Second), now.Add(-time.Minute), now.Add(-5*time.Minute)
	gpu0 := GPUInfo{Index: 0, UUID: "GPU-0", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}
	gpu1 := GPUInfo{Index: 1, UUID: "GPU-1", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}

	tests := []struct {
		name    string
		workers []*Worker
		running map[string]int
		want    []Host
	}{
		{name: "nil input", workers: nil, want: []Host{}},
		{name: "empty input", workers: []*Worker{}, want: []Host{}},
		{
			name: "two hosts: offline worker, shared GPU, freshest usage, running tasks",
			workers: []*Worker{
				{
					ID:        "a1",
					Name:      "w-a",
					Hostname:  "node-a",
					Group:     "gpu",
					State:     WorkerStateOnline,
					Runtime:   RuntimeApptainer,
					Version:   "v1",
					LastSeen:  fresh,
					Slots:     hostTestPtr(1),
					HostID:    "host-a",
					HostCPUs:  hostTestPtr(192),
					HostMemMB: hostTestPtr[int64](1547000),
					MaxCPUs:   hostTestPtr(64),
					MaxMemMB:  hostTestPtr[int64](500000),
					GPUs:      []GPUInfo{gpu0, gpu1},
					Usage: &WorkerUsage{
						Host: &HostUsage{Load1: 3.5, MemTotalMB: 1547000, MemAvailableMB: 400000},
						GPUs: []GPUUsage{{UUID: "GPU-0", MemUsedMB: hostTestPtr[int64](1000), UtilPct: hostTestPtr(50.0)}},
					},
					UsageAt:    &fresh,
					GPUEnabled: true,
					GPUDevice:  "0,1",
				},
				{
					// Same hostname as a1 (listed once) and same name (ordered by id).
					ID:        "a2",
					Name:      "w-a",
					Hostname:  "node-a",
					State:     WorkerStateOnline,
					HostID:    "host-a",
					HostMemMB: hostTestPtr[int64](1600000),
					MaxCPUs:   hostTestPtr(32),
					MaxMemMB:  hostTestPtr[int64](200000),
					GPUs:      []GPUInfo{gpu0},
					Usage: &WorkerUsage{
						Host: &HostUsage{Load1: 1, MemTotalMB: 1547000, MemAvailableMB: 1},
						GPUs: []GPUUsage{{UUID: "GPU-0", MemUsedMB: hostTestPtr[int64](999), UtilPct: hostTestPtr(99.0)}},
					},
					UsageAt: &older,
				},
				{
					// Offline: listed, but never counts for capacity, claimed,
					// hostnames, GPUs or usage, even with the freshest usage.
					ID:       "a3",
					Name:     "w-0",
					Hostname: "node-a-old",
					State:    WorkerStateOffline,
					HostID:   "host-a",
					HostCPUs: hostTestPtr(256),
					MaxCPUs:  hostTestPtr(16),
					MaxMemMB: hostTestPtr[int64](1000),
					GPUs:     []GPUInfo{{Index: 2, UUID: "GPU-2", MemTotalMB: 1}},
					Usage:    &WorkerUsage{Host: &HostUsage{Load1: 99}},
					UsageAt:  &now,
				},
				{
					// Stale usage: omitted.
					ID:       "b1",
					Name:     "w-b",
					Hostname: "node-b",
					State:    WorkerStateOnline,
					HostID:   "host-b",
					HostCPUs: hostTestPtr(8),
					MaxCPUs:  hostTestPtr(8),
					Usage:    &WorkerUsage{Host: &HostUsage{Load1: 2, MemTotalMB: 16000, MemAvailableMB: 8000}},
					UsageAt:  &stale,
				},
			},
			running: map[string]int{"a1": 2, "a3": 1, "b1": 1, "wrk_unknown": 5},
			want: []Host{
				{
					HostID:       "host-a",
					Hostnames:    []string{"node-a"},
					CPUs:         192,
					MemMB:        1600000,
					ClaimedCPUs:  96,
					ClaimedMemMB: 700000,
					Usage:        &HostUsage{Load1: 3.5, MemTotalMB: 1547000, MemAvailableMB: 400000},
					UsageAt:      &fresh,
					GPUs: []HostGPU{
						{Index: 0, UUID: "GPU-0", Name: "NVIDIA H200 NVL", MemTotalMB: 143771, MemUsedMB: hostTestPtr[int64](1000), UtilPct: hostTestPtr(50.0), UsageAt: &fresh},
						{Index: 1, UUID: "GPU-1", Name: "NVIDIA H200 NVL", MemTotalMB: 143771},
					},
					Workers: []HostWorker{
						{ID: "a3", Name: "w-0", State: WorkerStateOffline, MaxCPUs: hostTestPtr(16), MaxMemMB: hostTestPtr[int64](1000), RunningTasks: 1},
						{ID: "a1", Name: "w-a", Group: "gpu", State: WorkerStateOnline, Runtime: RuntimeApptainer, Slots: hostTestPtr(1), MaxCPUs: hostTestPtr(64), MaxMemMB: hostTestPtr[int64](500000), GPUEnabled: true, GPUDevice: "0,1", Version: "v1", LastSeen: fresh, RunningTasks: 2},
						{ID: "a2", Name: "w-a", State: WorkerStateOnline, MaxCPUs: hostTestPtr(32), MaxMemMB: hostTestPtr[int64](200000)},
					},
					WorkersOnline: 2,
					WorkersTotal:  3,
					RunningTasks:  3,
				},
				{
					HostID:      "host-b",
					Hostnames:   []string{"node-b"},
					CPUs:        8,
					ClaimedCPUs: 8,
					Workers: []HostWorker{
						{ID: "b1", Name: "w-b", State: WorkerStateOnline, MaxCPUs: hostTestPtr(8), RunningTasks: 1},
					},
					WorkersOnline: 1,
					WorkersTotal:  1,
					RunningTasks:  1,
				},
			},
		},
		{
			name: "all-offline host listed with zero claimed and no usage or GPUs",
			workers: []*Worker{{
				ID:        "g1",
				Name:      "gone",
				Hostname:  "node-gone",
				State:     WorkerStateOffline,
				HostID:    "host-gone",
				HostCPUs:  hostTestPtr(64),
				HostMemMB: hostTestPtr[int64](256000),
				MaxCPUs:   hostTestPtr(32),
				MaxMemMB:  hostTestPtr[int64](100000),
				GPUs:      []GPUInfo{gpu0},
				Usage: &WorkerUsage{
					Host: &HostUsage{Load1: 1, MemTotalMB: 256000, MemAvailableMB: 1},
					GPUs: []GPUUsage{{UUID: "GPU-0", MemUsedMB: hostTestPtr[int64](5)}},
				},
				UsageAt: &now,
			}},
			running: map[string]int{"g1": 1},
			want: []Host{{
				HostID: "host-gone",
				Workers: []HostWorker{
					{ID: "g1", Name: "gone", State: WorkerStateOffline, MaxCPUs: hostTestPtr(32), MaxMemMB: hostTestPtr[int64](100000), RunningTasks: 1},
				},
				WorkersTotal: 1,
				RunningTasks: 1,
			}},
		},
		{
			name: "stale usage omitted for the host and its GPUs",
			workers: []*Worker{{
				ID:     "s1",
				Name:   "s",
				State:  WorkerStateOnline,
				HostID: "host-s",
				GPUs:   []GPUInfo{gpu0},
				Usage: &WorkerUsage{
					Host: &HostUsage{Load1: 4, MemTotalMB: 1024, MemAvailableMB: 512},
					GPUs: []GPUUsage{{UUID: "GPU-0", MemUsedMB: hostTestPtr[int64](7), UtilPct: hostTestPtr(1.0)}},
				},
				UsageAt: &stale,
			}},
			want: []Host{{
				HostID:        "host-s",
				GPUs:          []HostGPU{{Index: 0, UUID: "GPU-0", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}},
				Workers:       []HostWorker{{ID: "s1", Name: "s", State: WorkerStateOnline}},
				WorkersOnline: 1,
				WorkersTotal:  1,
			}},
		},
		{
			name: "two uuids sharing an index are both listed in (index, uuid) order",
			workers: []*Worker{{
				ID:     "w1",
				Name:   "w",
				State:  WorkerStateOnline,
				HostID: "host-a",
				GPUs: []GPUInfo{
					{Index: 0, UUID: "GPU-b", MemTotalMB: 200},
					{Index: 0, UUID: "GPU-a", MemTotalMB: 100},
					{Index: 1, UUID: "GPU-c", MemTotalMB: 300},
				},
				Usage: &WorkerUsage{GPUs: []GPUUsage{
					{UUID: "GPU-b", MemUsedMB: hostTestPtr[int64](20)},
					{UUID: "GPU-a", MemUsedMB: hostTestPtr[int64](10)},
				}},
				UsageAt: &now,
			}},
			want: []Host{{
				HostID: "host-a",
				GPUs: []HostGPU{
					{Index: 0, UUID: "GPU-a", MemTotalMB: 100, MemUsedMB: hostTestPtr[int64](10), UsageAt: &now},
					{Index: 0, UUID: "GPU-b", MemTotalMB: 200, MemUsedMB: hostTestPtr[int64](20), UsageAt: &now},
					{Index: 1, UUID: "GPU-c", MemTotalMB: 300},
				},
				Workers:       []HostWorker{{ID: "w1", Name: "w", State: WorkerStateOnline}},
				WorkersOnline: 1,
				WorkersTotal:  1,
			}},
		},
		{
			name: "workers without a host id grouped by hostname and flagged legacy",
			workers: []*Worker{
				{ID: "l1", Name: "old-a", Hostname: "old-1", State: WorkerStateOnline, MaxCPUs: hostTestPtr(4)},
				{ID: "l2", Name: "old-b", Hostname: "old-1", State: WorkerStateOffline, MaxCPUs: hostTestPtr(4)},
				{ID: "l3", Name: "old-c", Hostname: "old-2", State: WorkerStateOnline},
				{ID: "z1", Name: "new", Hostname: "old-1", State: WorkerStateOnline, HostID: "host-z"},
			},
			running: map[string]int{"l1": 1},
			want: []Host{
				{
					HostID:        "host-z",
					Hostnames:     []string{"old-1"},
					Workers:       []HostWorker{{ID: "z1", Name: "new", State: WorkerStateOnline}},
					WorkersOnline: 1,
					WorkersTotal:  1,
				},
				{
					HostID:      "hostname:old-1",
					Legacy:      true,
					Hostnames:   []string{"old-1"},
					ClaimedCPUs: 4,
					Workers: []HostWorker{
						{ID: "l1", Name: "old-a", State: WorkerStateOnline, MaxCPUs: hostTestPtr(4), RunningTasks: 1},
						{ID: "l2", Name: "old-b", State: WorkerStateOffline, MaxCPUs: hostTestPtr(4)},
					},
					WorkersOnline: 1,
					WorkersTotal:  2,
					RunningTasks:  1,
				},
				{
					HostID:        "hostname:old-2",
					Legacy:        true,
					Hostnames:     []string{"old-2"},
					Workers:       []HostWorker{{ID: "l3", Name: "old-c", State: WorkerStateOnline}},
					WorkersOnline: 1,
					WorkersTotal:  1,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := hostsJSON(t, tt.want)
			got := AggregateHosts(tt.workers, tt.running, now)
			if got == nil {
				t.Fatal("AggregateHosts returned nil, want a non-nil slice")
			}
			if g := hostsJSON(t, got); g != want {
				t.Errorf("hosts =\n%s\nwant\n%s", g, want)
			}

			// The result must not depend on the input order.
			reversed := slices.Clone(tt.workers)
			slices.Reverse(reversed)
			if g := hostsJSON(t, AggregateHosts(reversed, tt.running, now)); g != want {
				t.Errorf("hosts for reversed input =\n%s\nwant\n%s", g, want)
			}
		})
	}
}
