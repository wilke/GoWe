//go:build linux

package worker

// procRoot is the /proc directory service mode reads.
const procRoot = "/proc"

// listenerOwnedBy decides whether the TCP listeners on port (IPv4 and IPv6,
// any local address) all belong to the process rootPID or its descendants,
// from this host's /proc (checkListenerOwnership).
func listenerOwnedBy(rootPID, port int) (listenerVerdict, error) {
	return checkListenerOwnership(dirProcSource(procRoot), rootPID, port)
}

// readProcStat reads and parses /proc/<pid>/stat.
func readProcStat(pid int) (procStat, error) {
	return dirProcSource(procRoot).stat(pid)
}
