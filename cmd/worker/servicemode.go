package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/me/gowe/internal/worker"
)

// serviceModeFlags are the command-line settings checkServiceModeFlags
// looks at.
type serviceModeFlags struct {
	serviceMode      bool
	portRangeSet     bool   // --service-port-range given
	advertiseHostSet bool   // --advertise-host given
	portRange        string // --service-port-range value (or its default)
	declaredRuntime  string // --runtime as given
	installedRuntime string // --runtime after validateInstalledRuntimes
	apptainerOnPath  bool   // exec.LookPath("apptainer") succeeded (singularity alone does not count)
	goos             string // runtime.GOOS
	workerKeyFile    string
	inheritHostEnv   bool
	noContainall     bool
	concurrency      int
}

// checkServiceModeFlags refuses an invalid combination of the service-mode
// flags at start-up and returns the parsed --service-port-range (0, 0
// without --service-mode). worker.New checks the rest (host id, GPU
// inventory, advertised host).
func checkServiceModeFlags(f serviceModeFlags) (lo, hi int, err error) {
	if !f.serviceMode {
		if f.portRangeSet || f.advertiseHostSet {
			return 0, 0, errors.New("--service-port-range and --advertise-host require --service-mode")
		}
		return 0, 0, nil
	}
	if f.goos != "linux" {
		return 0, 0, fmt.Errorf("--service-mode runs on Linux only (this system is %s)", f.goos)
	}
	if strings.TrimSpace(f.declaredRuntime) != "apptainer" {
		return 0, 0, fmt.Errorf("--service-mode requires --runtime apptainer and no other runtime (got %q): a service runs only in an Apptainer container", f.declaredRuntime)
	}
	if f.installedRuntime != "apptainer" {
		return 0, 0, errors.New("--service-mode requires Apptainer, and neither apptainer nor singularity was found on PATH")
	}
	if !f.apptainerOnPath {
		return 0, 0, errors.New("--service-mode requires the apptainer executable on PATH: singularity alone is not enough (a service is launched as \"apptainer\")")
	}
	if f.workerKeyFile == "" {
		return 0, 0, errors.New("--service-mode requires --worker-key-file: the server accepts service mode only from an authenticated worker, with a key that names its group")
	}
	if f.inheritHostEnv {
		return 0, 0, errors.New("--service-mode cannot be combined with --inherit-host-env")
	}
	if f.noContainall {
		return 0, 0, errors.New("--service-mode cannot be combined with --apptainer-no-containall")
	}
	if err := worker.ServiceConcurrencyError(f.concurrency); err != nil {
		return 0, 0, err
	}
	return worker.ParseServicePortRange(f.portRange, f.concurrency)
}
