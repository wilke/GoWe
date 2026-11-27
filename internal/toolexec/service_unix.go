//go:build unix

package toolexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// serviceLogCreateFlags create a service log file: a new file only (a name
// that exists, a symlink included, fails), never through a symlink.
const serviceLogCreateFlags = os.O_CREATE | os.O_EXCL | os.O_WRONLY | os.O_APPEND | syscall.O_NOFOLLOW

// serviceLogReadFlags open the read descriptor of a service log: never
// through a symlink, and without blocking on a FIFO put in its place (the
// descriptor is then refused, as it is not the created file).
const serviceLogReadFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// removeStaleServiceLog removes what is at path before a service log is
// created there: nothing to do when nothing is there; a regular file owned by
// this process's user is removed; anything else (symlink, FIFO, directory,
// another user's file) refuses the launch.
func removeStaleServiceLog(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create service log: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("create service log: %s exists and is not a regular file owned by this worker (%s); not launching", path, fi.Mode().Type())
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("create service log: remove the stale %s: %w", path, err)
	}
	return nil
}
