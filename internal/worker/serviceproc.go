package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Parsers for the /proc files service mode reads (serviceproc_linux.go):
// /proc/<pid>/stat and /proc/net/tcp{,6}, and the decision whether a port's
// listeners belong to a service (checkListenerOwnership). Platform
// independent, so they are tested everywhere; the /proc access itself is a
// parameter (procSource).

// procStat holds the fields of /proc/<pid>/stat that service mode uses.
type procStat struct {
	ppid      int    // field 4
	pgrp      int    // field 5: process group id
	startTime uint64 // field 22: start time in clock ticks since boot
}

// parseProcStat parses one /proc/<pid>/stat line. The comm field (2) is in
// parentheses and may itself contain spaces and parentheses, so the fields
// are split after the LAST ')'.
func parseProcStat(line string) (procStat, error) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return procStat{}, errors.New("proc stat: no ')' after the command name")
	}
	// rest[0] is field 3 (state), so field n is rest[n-3].
	rest := strings.Fields(line[i+1:])
	if len(rest) < 20 {
		return procStat{}, fmt.Errorf("proc stat: %d fields after the command name, want at least 20", len(rest))
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return procStat{}, fmt.Errorf("proc stat: ppid: %w", err)
	}
	pgrp, err := strconv.Atoi(rest[2])
	if err != nil {
		return procStat{}, fmt.Errorf("proc stat: pgrp: %w", err)
	}
	start, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil {
		return procStat{}, fmt.Errorf("proc stat: starttime: %w", err)
	}
	return procStat{ppid: ppid, pgrp: pgrp, startTime: start}, nil
}

// tcpStateListen is the st column of a socket in LISTEN state in
// /proc/net/tcp{,6}.
const tcpStateListen = "0A"

// parseListenInodes returns the inodes of the sockets in LISTEN state whose
// local port is port, from the text of /proc/net/tcp or /proc/net/tcp6 (any
// local address). The header and malformed lines are skipped.
func parseListenInodes(data string, port int) []uint64 {
	var out []uint64
	for len(data) > 0 {
		var line string
		if j := strings.IndexByte(data, '\n'); j >= 0 {
			line, data = data[:j], data[j+1:]
		} else {
			line, data = data, ""
		}
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode ...
		f := strings.Fields(line)
		if len(f) < 10 || !strings.HasSuffix(f[0], ":") || f[3] != tcpStateListen {
			continue
		}
		c := strings.LastIndexByte(f[1], ':')
		if c < 0 {
			continue
		}
		p, err := strconv.ParseUint(f[1][c+1:], 16, 32)
		if err != nil || int(p) != port {
			continue
		}
		ino, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		out = append(out, ino)
	}
	return out
}

// socketInode returns the inode of a /proc/<pid>/fd link target of the form
// "socket:[<inode>]".
func socketInode(link string) (uint64, bool) {
	const prefix, suffix = "socket:[", "]"
	if !strings.HasPrefix(link, prefix) || !strings.HasSuffix(link, suffix) {
		return 0, false
	}
	ino, err := strconv.ParseUint(link[len(prefix):len(link)-len(suffix)], 10, 64)
	return ino, err == nil
}

// procSource is the /proc information the listener ownership check reads.
// dirProcSource reads it from a /proc directory; tests inject fixtures.
type procSource struct {
	// netTable returns the text of /proc/net/<name>, name "tcp" or "tcp6".
	netTable func(name string) ([]byte, error)
	// pids lists the process ids.
	pids func() ([]int, error)
	// stat reads /proc/<pid>/stat.
	stat func(pid int) (procStat, error)
	// fdLinks returns the link targets of /proc/<pid>/fd/*: those it could
	// read, and an error when the directory (or a link) could not be read.
	fdLinks func(pid int) ([]string, error)
}

// dirProcSource reads /proc information below root (normally "/proc").
func dirProcSource(root string) procSource {
	return procSource{
		netTable: func(name string) ([]byte, error) {
			return os.ReadFile(filepath.Join(root, "net", name))
		},
		pids: func() ([]int, error) {
			entries, err := os.ReadDir(root)
			if err != nil {
				return nil, err
			}
			var out []int
			for _, e := range entries {
				if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
					out = append(out, pid)
				}
			}
			return out, nil
		},
		stat: func(pid int) (procStat, error) {
			data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
			if err != nil {
				return procStat{}, err
			}
			return parseProcStat(string(data))
		},
		fdLinks: func(pid int) ([]string, error) {
			dir := filepath.Join(root, strconv.Itoa(pid), "fd")
			f, err := os.Open(dir)
			if err != nil {
				return nil, err
			}
			names, err := f.Readdirnames(-1)
			_ = f.Close()
			var links []string
			for _, n := range names {
				link, lerr := os.Readlink(filepath.Join(dir, n))
				if lerr != nil {
					if err == nil && errors.Is(lerr, fs.ErrPermission) {
						err = lerr
					}
					continue // closed meanwhile, or unreadable (reported above)
				}
				links = append(links, link)
			}
			return links, err
		},
	}
}

// listenerVerdict is the outcome of the ownership check of a service's port.
type listenerVerdict int

const (
	// listenerOwned: there is at least one LISTEN socket on the port, and
	// every one of them is held by the service's process tree.
	listenerOwned listenerVerdict = iota + 1
	// listenerForeign: at least one LISTEN socket on the port is not held by
	// the service's process tree.
	listenerForeign
	// listenerNone: no LISTEN socket on the port at all (the answer came
	// from somewhere this host's socket tables do not show).
	listenerNone
)

// checkListenerOwnership decides whether the TCP listeners on port belong to
// the process rootPID and its descendants. It collects every socket in LISTEN
// state on the port from /proc/net/tcp and /proc/net/tcp6 (any local
// address) and looks for each socket's inode among the descriptors of the
// process tree (built from the ppid links of /proc/<pid>/stat).
//
// The result is listenerOwned only when there is at least one listener and
// every one is held by the tree; listenerForeign when any listener is not;
// listenerNone when there is none. Processes that vanish meanwhile are
// skipped. An error means it cannot be verified: neither socket table could
// be read, or no listener was found held by the tree while the descriptors of
// a process of the tree could not be read for lack of permission (the
// listener may be that process's; "cannot read /proc/<pid>/fd: <error>").
func checkListenerOwnership(src procSource, rootPID, port int) (listenerVerdict, error) {
	if rootPID <= 0 {
		return 0, errors.New("no service process id")
	}
	inodes := make(map[uint64]bool)
	var readErr error
	tables := 0
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := src.netTable(name)
		if err != nil {
			// tcp6 is missing when IPv6 is disabled.
			readErr = err
			continue
		}
		tables++
		for _, ino := range parseListenInodes(string(data), port) {
			inodes[ino] = true
		}
	}
	if tables == 0 {
		return 0, readErr
	}
	if len(inodes) == 0 {
		return listenerNone, nil
	}
	held := make(map[uint64]bool, len(inodes))
	var permErr error
	for _, pid := range processTree(src, rootPID) {
		links, err := src.fdLinks(pid)
		if err != nil && permErr == nil && errors.Is(err, fs.ErrPermission) {
			permErr = fmt.Errorf("cannot read /proc/%d/fd: %w", pid, pathErrCause(err))
		}
		for _, l := range links {
			if ino, ok := socketInode(l); ok && inodes[ino] {
				held[ino] = true
			}
		}
		if len(held) == len(inodes) {
			return listenerOwned, nil
		}
	}
	if len(held) == 0 && permErr != nil {
		return 0, permErr
	}
	return listenerForeign, nil
}

// processTree returns root followed by all its descendants, from the ppid
// links of /proc/<pid>/stat. Processes whose stat cannot be read or parsed
// are skipped.
func processTree(src procSource, root int) []int {
	children := make(map[int][]int)
	if pids, err := src.pids(); err == nil {
		for _, pid := range pids {
			if pid <= 0 || pid == root {
				continue
			}
			st, err := src.stat(pid)
			if err != nil {
				continue
			}
			children[st.ppid] = append(children[st.ppid], pid)
		}
	}
	out := []int{root}
	seen := map[int]bool{root: true}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i]] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// pathErrCause returns the cause of a *fs.PathError (e.g. "permission
// denied"), err itself otherwise, so a message names the path only once.
func pathErrCause(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
