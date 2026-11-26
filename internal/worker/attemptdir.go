package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// taskDirSuffixes are the suffixes of a task's three directories:
// "<id>", "<id>_tmp" and "<id>_staging".
var taskDirSuffixes = []string{"", "_tmp", "_staging"}

// archivePreviousAttempt moves the directories an earlier attempt of task
// taskID left in workDir aside, so the next attempt starts in fresh ones. A
// retried or requeued task keeps its id, and files staged by the earlier
// attempt (e.g. InitialWorkDirRequirement symlinks) would make it fail.
//
// When none of "<id>", "<id>_tmp", "<id>_staging" exists it returns "", nil.
// Otherwise it picks the smallest N >= 1 for which none of
// "<id>.attempt-N", "<id>.attempt-N_tmp", "<id>.attempt-N_staging" exists,
// renames each existing directory to its ".attempt-N" counterpart and returns
// the archived base path "<workDir>/<id>.attempt-N". Nothing is deleted; on a
// failed rename the error is returned and earlier renames are not undone.
func archivePreviousAttempt(workDir, taskID string) (string, error) {
	base := filepath.Join(workDir, taskID)
	var present []string
	for _, s := range taskDirSuffixes {
		ok, err := pathExists(base + s)
		if err != nil {
			return "", err
		}
		if ok {
			present = append(present, s)
		}
	}
	if len(present) == 0 {
		return "", nil
	}

	for n := 1; ; n++ {
		archived := base + ".attempt-" + strconv.Itoa(n)
		taken := false
		for _, s := range taskDirSuffixes {
			ok, err := pathExists(archived + s)
			if err != nil {
				return "", err
			}
			if ok {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		for _, s := range present {
			from, to := base+s, archived+s
			if err := os.Rename(from, to); err != nil {
				return "", fmt.Errorf("move %s to %s: %w", from, to, err)
			}
		}
		return archived, nil
	}
}

// pathExists reports whether p exists (without following a final symlink).
func pathExists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", p, err)
}
