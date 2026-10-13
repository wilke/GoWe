//go:build darwin

package worker

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity returns a key that changes whenever the file is replaced or
// modified in place: device, inode, size, mtime and ctime. ok is false when
// the platform cannot supply it (the cache is then bypassed).
func fileIdentity(path string) (key string, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return "", false
	}
	return fmt.Sprintf("%d:%d:%d:%d.%d:%d.%d", st.Dev, st.Ino, st.Size,
		st.Mtimespec.Sec, st.Mtimespec.Nsec, st.Ctimespec.Sec, st.Ctimespec.Nsec), true
}
