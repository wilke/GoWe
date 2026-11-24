package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// Host identity sources, in fallback order (#323).
const (
	machineIDPath = "/etc/machine-id"
	bootIDPath    = "/proc/sys/kernel/random/boot_id"
)

// hostIDHexLen is the number of hex characters of the sha256 digest kept as
// the derived host id.
const hostIDHexLen = 16

// resolveHostID returns the --host-id override verbatim when set (after
// validation), otherwise the id derived by deriveHostID.
func resolveHostID(override, machineIDFile, bootIDFile, hostname string) (string, error) {
	if override != "" {
		if err := model.ValidateHostID(override); err != nil {
			return "", fmt.Errorf("invalid --host-id: %w", err)
		}
		return override, nil
	}
	return deriveHostID(machineIDFile, bootIDFile, hostname), nil
}

// deriveHostID hashes the first non-empty identity source: the trimmed
// contents of machineIDFile, then of bootIDFile, then the hostname. Only the
// hash prefix leaves this function; the raw machine-id is never returned or
// logged. Returns "" when every source is missing or empty.
func deriveHostID(machineIDFile, bootIDFile, hostname string) string {
	for _, path := range []string{machineIDFile, bootIDFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// systemd writes the literal "uninitialized" until first boot
		// completes; it identifies nothing.
		if s := strings.TrimSpace(string(data)); s != "" && s != "uninitialized" {
			return hashHostID(s)
		}
	}
	if h := strings.TrimSpace(hostname); h != "" {
		return hashHostID(h)
	}
	return ""
}

// hashHostID returns the first hostIDHexLen hex characters of sha256(s).
func hashHostID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:hostIDHexLen]
}

// hostCPUCount returns the number of online CPUs of the host from the kernel's
// CPU list file (/sys/devices/system/cpu/online), falling back to fallback
// when the file is missing or unparsable. runtime.NumCPU alone is not the host
// total: it honours the process's affinity mask (taskset, cpusets), so two
// workers on one host could report different values.
func hostCPUCount(onlineFile string, fallback int) int {
	data, err := os.ReadFile(onlineFile)
	if err != nil {
		return fallback
	}
	if n := parseCPUList(string(data)); n > 0 {
		return n
	}
	return fallback
}

// parseCPUList counts the CPUs in a kernel CPU list such as "0-383" or
// "0-3,8-11,16". Returns 0 if the list is empty or malformed.
func parseCPUList(list string) int {
	total := 0
	for _, part := range strings.Split(strings.TrimSpace(list), ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		first, err := strconv.Atoi(lo)
		if err != nil || first < 0 {
			return 0
		}
		last := first
		if isRange {
			if last, err = strconv.Atoi(hi); err != nil || last < first {
				return 0
			}
		}
		total += last - first + 1
	}
	return total
}
