package model

import "testing"

func TestRuntimeHintsGPUCount(t *testing.T) {
	tests := []struct {
		name string
		h    *RuntimeHints
		want int
	}{
		{"nil hints", nil, 0},
		{"neither", &RuntimeHints{}, 0},
		{"boolean only", &RuntimeHints{RequiresGPU: true}, 1},
		{"count", &RuntimeHints{RequiresGPU: true, GPUs: 3}, 3},
		{"count without boolean", &RuntimeHints{GPUs: 2}, 2},
		{"non-positive count falls back to boolean", &RuntimeHints{RequiresGPU: true, GPUs: -1}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.GPUCount(); got != tt.want {
				t.Errorf("GPUCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWorkerGPUCapacity(t *testing.T) {
	two := []GPUInfo{{Index: 0, UUID: "GPU-a"}, {Index: 1, UUID: "GPU-b"}}
	tests := []struct {
		name string
		w    Worker
		want *int
	}{
		{"not GPU-enabled", Worker{GPUs: two}, nil},
		{"GPU worker, inventory not reported", Worker{GPUEnabled: true}, nil},
		{"GPU worker, empty inventory", Worker{GPUEnabled: true, GPUs: []GPUInfo{}}, nil},
		{"GPU worker with inventory", Worker{GPUEnabled: true, GPUs: two}, func() *int { v := 2; return &v }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.w.GPUCapacity()
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("GPUCapacity() = %v, want %v", deref(got), deref(tt.want))
			}
		})
	}
}
