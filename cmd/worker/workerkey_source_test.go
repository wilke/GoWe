package main

import "testing"

func TestCheckWorkerKeySource(t *testing.T) {
	tests := []struct {
		name         string
		fromEnv      bool
		runtimes     string
		noContainall bool
		wantWarn     bool
		wantErr      bool
	}{
		{"file source, none runtime", false, "none", false, false, false},
		{"file source, no-containall", false, "apptainer", true, false, false},
		{"env source, apptainer", true, "apptainer", false, true, false},
		{"env source, docker,apptainer", true, "docker, apptainer", false, true, false},
		{"env source, none", true, "none", false, false, true},
		{"env source, list containing none", true, "apptainer, none", false, false, true},
		{"env source, no-containall", true, "apptainer", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warn, err := checkWorkerKeySource(tt.fromEnv, tt.runtimes, tt.noContainall)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if warn != tt.wantWarn {
				t.Errorf("warn = %v, want %v", warn, tt.wantWarn)
			}
		})
	}
}

func TestWorkerKeyFromEnv(t *testing.T) {
	tests := []struct {
		name, file, key string
		want            bool
	}{
		{"file given", "/k", "x", false},
		{"nothing", "", "", false},
		{"env only", "", "x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workerKeyFromEnv(tt.file, tt.key); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
