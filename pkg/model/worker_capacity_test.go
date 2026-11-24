package model

import "testing"

func TestWorkerEffectiveMax(t *testing.T) {
	ip := func(v int) *int { return &v }
	i64p := func(v int64) *int64 { return &v }

	tests := []struct {
		name    string
		w       Worker
		wantCPU *int
		wantMem *int64
	}{
		{name: "nothing reported", w: Worker{}},
		{name: "worker max only", w: Worker{MaxCPUs: ip(8), MaxMemMB: i64p(1000)}, wantCPU: ip(8), wantMem: i64p(1000)},
		{
			name:    "host smaller than worker max",
			w:       Worker{HostID: "h", MaxCPUs: ip(64), HostCPUs: ip(16), MaxMemMB: i64p(4000), HostMemMB: i64p(2000)},
			wantCPU: ip(16),
			wantMem: i64p(2000),
		},
		{
			name:    "worker max smaller than host",
			w:       Worker{HostID: "h", MaxCPUs: ip(4), HostCPUs: ip(16), MaxMemMB: i64p(1000), HostMemMB: i64p(2000)},
			wantCPU: ip(4),
			wantMem: i64p(1000),
		},
		{
			name:    "host capacity only",
			w:       Worker{HostID: "h", HostCPUs: ip(16), HostMemMB: i64p(2000)},
			wantCPU: ip(16),
			wantMem: i64p(2000),
		},
		{
			name:    "host capacity ignored without a host id",
			w:       Worker{MaxCPUs: ip(64), HostCPUs: ip(16), HostMemMB: i64p(2000)},
			wantCPU: ip(64),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCPU := tt.w.EffectiveMaxCPUs()
			if (gotCPU == nil) != (tt.wantCPU == nil) || (gotCPU != nil && *gotCPU != *tt.wantCPU) {
				t.Errorf("EffectiveMaxCPUs() = %v, want %v", deref(gotCPU), deref(tt.wantCPU))
			}
			gotMem := tt.w.EffectiveMaxMemMB()
			if (gotMem == nil) != (tt.wantMem == nil) || (gotMem != nil && *gotMem != *tt.wantMem) {
				t.Errorf("EffectiveMaxMemMB() = %v, want %v", deref(gotMem), deref(tt.wantMem))
			}
		})
	}
}

// deref renders a nil pointer as "nil" for test messages.
func deref[T int | int64](p *T) any {
	if p == nil {
		return "nil"
	}
	return *p
}
