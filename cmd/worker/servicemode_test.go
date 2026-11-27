package main

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestCheckServiceModeFlags(t *testing.T) {
	ok := serviceModeFlags{
		serviceMode:      true,
		portRange:        "20000-20999",
		declaredRuntime:  "apptainer",
		installedRuntime: "apptainer",
		apptainerOnPath:  true,
		goos:             "linux",
		workerKeyFile:    "/etc/gowe/worker.key",
		concurrency:      2,
	}
	tests := []struct {
		name    string
		mutate  func(*serviceModeFlags)
		wantErr string // substring; "" = accepted
		lo, hi  int
	}{
		{"valid", nil, "", 20000, 20999},
		{"custom range", func(f *serviceModeFlags) { f.portRange = "31000-31001"; f.portRangeSet = true }, "", 31000, 31001},
		{"runtime none", func(f *serviceModeFlags) { f.declaredRuntime, f.installedRuntime = "none", "none" }, "--runtime apptainer", 0, 0},
		{"runtime apptainer and none", func(f *serviceModeFlags) { f.declaredRuntime = "apptainer,none" }, "--runtime apptainer", 0, 0},
		{"runtime docker", func(f *serviceModeFlags) { f.declaredRuntime, f.installedRuntime = "docker", "docker" }, "--runtime apptainer", 0, 0},
		{"apptainer not installed", func(f *serviceModeFlags) { f.installedRuntime = "none"; f.apptainerOnPath = false }, "neither apptainer nor singularity", 0, 0},
		{"only singularity installed", func(f *serviceModeFlags) { f.apptainerOnPath = false }, "singularity alone is not enough", 0, 0},
		{"not linux", func(f *serviceModeFlags) { f.goos = "darwin" }, "Linux only", 0, 0},
		{"concurrency above the heartbeat service limit", func(f *serviceModeFlags) { f.concurrency = model.MaxHeartbeatServices + 1; f.portRange = "20000-20999" }, "at most --concurrency 64", 0, 0},
		{"concurrency at the heartbeat service limit", func(f *serviceModeFlags) { f.concurrency = model.MaxHeartbeatServices }, "", 20000, 20999},
		{"no worker key file", func(f *serviceModeFlags) { f.workerKeyFile = "" }, "--worker-key-file", 0, 0},
		{"inherit host env", func(f *serviceModeFlags) { f.inheritHostEnv = true }, "--inherit-host-env", 0, 0},
		{"no containall", func(f *serviceModeFlags) { f.noContainall = true }, "--apptainer-no-containall", 0, 0},
		{"range not LO-HI", func(f *serviceModeFlags) { f.portRange = "20000" }, "want LO-HI", 0, 0},
		{"range not numbers", func(f *serviceModeFlags) { f.portRange = "a-b" }, "two port numbers", 0, 0},
		{"range below 1024", func(f *serviceModeFlags) { f.portRange = "80-90" }, "1024-65535", 0, 0},
		{"range above 65535", func(f *serviceModeFlags) { f.portRange = "65000-70000" }, "1024-65535", 0, 0},
		{"range reversed", func(f *serviceModeFlags) { f.portRange = "20010-20000" }, "LO <= HI", 0, 0},
		{"fewer ports than concurrency", func(f *serviceModeFlags) { f.portRange = "20000-20000" }, "fewer than --concurrency 2", 0, 0},
		{"port range without service mode", func(f *serviceModeFlags) { *f = serviceModeFlags{portRangeSet: true} }, "require --service-mode", 0, 0},
		{"advertise host without service mode", func(f *serviceModeFlags) { *f = serviceModeFlags{advertiseHostSet: true} }, "require --service-mode", 0, 0},
		{"batch mode untouched", func(f *serviceModeFlags) {
			*f = serviceModeFlags{portRange: "20000-20999", declaredRuntime: "none", installedRuntime: "none", inheritHostEnv: true, noContainall: true}
		}, "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := ok
			if tt.mutate != nil {
				tt.mutate(&f)
			}
			lo, hi, err := checkServiceModeFlags(f)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if lo != tt.lo || hi != tt.hi {
				t.Errorf("range = %d-%d, want %d-%d", lo, hi, tt.lo, tt.hi)
			}
		})
	}
}
