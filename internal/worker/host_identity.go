package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
		if s := strings.TrimSpace(string(data)); s != "" {
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
