package worker

import "testing"

func TestDegradedLabels(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want map[string]string
	}{
		{"fully isolated", Config{}, nil},
		{"inherit host env", Config{InheritHostEnv: true}, map[string]string{"inherit_host_env": "true"}},
		{"no containall", Config{ApptainerNoContainall: true}, map[string]string{"apptainer_containall": "false"}},
		{"both", Config{InheritHostEnv: true, ApptainerNoContainall: true},
			map[string]string{"inherit_host_env": "true", "apptainer_containall": "false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := degradedLabels(tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("label %s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
