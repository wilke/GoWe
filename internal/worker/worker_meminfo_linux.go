//go:build linux

package worker

import "syscall"

// sysTotalMemMB returns total physical memory in MiB via the Linux sysinfo(2)
// syscall, or 0 if it cannot be determined.
func sysTotalMemMB() int64 {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return 0
	}
	return (int64(si.Totalram) * int64(si.Unit)) / (1024 * 1024)
}
