package toolexec

import (
	"os"
	"runtime"
	"syscall"
)

// getResourceUsage extracts peak memory usage from process state.
// Returns peak RSS in KB. On Darwin (macOS), Maxrss is in bytes; on Linux, it's in KB.
func getResourceUsage(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}

	rusage, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || rusage == nil {
		return 0
	}

	// Darwin reports Maxrss in bytes, Linux reports in KB
	if runtime.GOOS == "darwin" {
		return rusage.Maxrss / 1024
	}
	return rusage.Maxrss
}
