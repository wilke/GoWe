//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a symbolic link in its
// last element (O_NOFOLLOW: a link swapped in after the Lstat makes the open
// fail) and without blocking on a FIFO swapped in meanwhile (O_NONBLOCK; it
// does not affect reading a regular file).
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// checkFileOwner refuses a file that is not owned by the current user.
func checkFileOwner(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine its owner")
	}
	if uid := os.Getuid(); int64(st.Uid) != int64(uid) {
		return fmt.Errorf("is owned by uid %d, not by the current user (uid %d)", st.Uid, uid)
	}
	return nil
}
