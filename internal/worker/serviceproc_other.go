//go:build !linux

package worker

import "errors"

// errProcUnsupported is returned where service mode needs Linux /proc.
// Service mode is refused at start-up on other systems; these stubs make
// every check fail closed.
var errProcUnsupported = errors.New("cannot verify which process holds the port: this needs Linux /proc (service mode runs on Linux only)")

// listenerOwnedBy cannot verify anything here: never owned.
func listenerOwnedBy(rootPID, port int) (listenerVerdict, error) {
	return 0, errProcUnsupported
}

// readProcStat cannot read process information here.
func readProcStat(pid int) (procStat, error) {
	return procStat{}, errProcUnsupported
}
