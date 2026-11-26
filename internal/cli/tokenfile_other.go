//go:build !unix

package cli

import (
	"errors"
	"os"
)

// openNoFollow is not available here: --token-file is refused (fail closed)
// rather than read without the symbolic link and owner checks.
func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("--token-file is not supported on this platform")
}

// checkFileOwner cannot check the owner here and refuses.
func checkFileOwner(os.FileInfo) error {
	return errors.New("cannot determine its owner on this platform")
}
