package server

import "testing"

func TestFleetDegradedLabels(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"nil", nil, nil},
		{"unrelated labels dropped", map[string]string{"rack": "7"}, nil},
		{"degraded kept, others dropped",
			map[string]string{"inherit_host_env": "true", "apptainer_containall": "false", "rack": "7"},
			map[string]string{"inherit_host_env": "true", "apptainer_containall": "false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fleetDegradedLabels(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
