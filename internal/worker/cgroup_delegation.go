package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Apptainer cgroup limit modes for --apptainer-cgroups.
const (
	ApptainerCgroupsAuto = "auto" // detect which controllers Apptainer can use (default)
	ApptainerCgroupsOn   = "on"   // pass --cpus and --memory unconditionally
	ApptainerCgroupsOff  = "off"  // never pass --cpus or --memory
)

// cgroupLimits says which Apptainer resource flags the worker may pass.
type cgroupLimits struct {
	CPU      bool     // pass --cpus
	Memory   bool     // pass --memory
	Source   string   // cgroup whose controllers were consulted ("" when none)
	Warnings []string // operator-facing notes on limits that will not be enforced
}

// detectCgroupLimits decides which Apptainer resource limits can be applied.
//
// root is the cgroup v2 mount (normally /sys/fs/cgroup); selfCgroup is the
// contents of /proc/self/cgroup, used only for diagnostics.
//
// Root Apptainer creates container cgroups directly, so every controller
// enabled at the cgroup root is usable. Rootless Apptainer asks the systemd
// user manager for a scope under user@<uid>.service/user.slice regardless of
// which cgroup the worker process itself runs in (a worker started with nohup
// from an ssh session lives in session-N.scope but its containers still land
// under user@<uid>.service). Usable controllers are the ones *delegated* to
// the user manager: user@<uid>.service/cgroup.controllers (the same check
// podman documents for rootless limits). Child slices such as user.slice list
// only what systemd has enabled so far and gain delegated controllers on
// demand, so they must not be consulted. Distros often delegate just
// "memory pids"; requesting --cpus there fails at container creation
// ("openat2 .../cpu.max: no such file").
func detectCgroupLimits(root string, uid int, mode, selfCgroup string) (cgroupLimits, error) {
	switch mode {
	case ApptainerCgroupsOff:
		return cgroupLimits{}, nil
	case ApptainerCgroupsOn:
		return cgroupLimits{CPU: true, Memory: true, Source: "forced by --apptainer-cgroups=on"}, nil
	case ApptainerCgroupsAuto, "":
	default:
		return cgroupLimits{}, fmt.Errorf("invalid --apptainer-cgroups %q (want auto, on or off)", mode)
	}

	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		// No unified cgroup v2 hierarchy at root: cgroups v1, hybrid mode (v2
		// mounted only at /sys/fs/cgroup/unified, as on coconut) or no cgroupfs.
		// Apptainer --cpus/--memory are not applied (common on HPC).
		return cgroupLimits{}, nil
	}

	var candidates []string
	if uid == 0 {
		candidates = []string{root}
	} else {
		candidates = []string{filepath.Join(root, "user.slice", fmt.Sprintf("user-%d.slice", uid), fmt.Sprintf("user@%d.service", uid))}
	}

	for _, dir := range candidates {
		data, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
		if err != nil {
			continue
		}
		set := map[string]bool{}
		for _, c := range strings.Fields(string(data)) {
			set[c] = true
		}
		lim := cgroupLimits{CPU: set["cpu"], Memory: set["memory"], Source: dir}
		for _, c := range []struct {
			name, flag, note string
			ok               bool
		}{
			{"cpu", "--cpus", "; tools still get --cpu via runtime.cores", lim.CPU},
			{"memory", "--memory", "", lim.Memory},
		} {
			if !c.ok {
				lim.Warnings = append(lim.Warnings, fmt.Sprintf(
					"%s limit not enforced: %s controller not delegated to %s (Apptainer %s skipped)%s",
					c.name, c.name, dir, c.flag, c.note))
			}
		}
		return lim, nil
	}

	return cgroupLimits{Warnings: []string{fmt.Sprintf(
		"cpu and memory limits not enforced: no user@%d.service cgroup found under %s (worker cgroup %q); "+
			"set --apptainer-cgroups=on to force them", uid, root, strings.TrimSpace(selfCgroup))}}, nil
}
