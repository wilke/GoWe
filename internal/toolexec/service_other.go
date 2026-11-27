//go:build !unix

package toolexec

import (
	"errors"
	"os"
)

const (
	serviceLogCreateFlags = os.O_CREATE | os.O_EXCL | os.O_WRONLY | os.O_APPEND
	serviceLogReadFlags   = os.O_RDONLY
)

// removeStaleServiceLog refuses: services run on Linux only.
func removeStaleServiceLog(path string) error {
	return errors.New("create service log: services are supported on Linux only")
}
