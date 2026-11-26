package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Work directory lock: tells whether another worker process uses the same
// --workdir, so the kept-dir sweep (keptdirs.go) never removes what that
// process is using. keptMu and w.active only cover this process.
//
// Two flock(2) lock files in the workdir; no lock is ever converted:
//
//   - "<workdir>/.gowe-worker.lock", the OWNER lock: exactly one worker
//     process holds it, exclusively, for the rest of its life. Only the owner
//     sweeps.
//   - "<workdir>/.gowe-worker.shared", the SHARER lock: every other worker
//     process holds it shared for as long as it is not the owner. A sharer
//     never sweeps.
//
// At start (lockWorkDir, from Run, with or without --kept-task-dir-ttl) a
// worker tries the owner lock without blocking; if another process holds it,
// the worker becomes a sharer and takes the sharer lock, blocking while the
// owner's sweep holds it exclusively. Any other error leaves the worker
// without a lock: it is not seen by others and never sweeps.
//
// Before a sweep the owner locks the sharer file exclusively without blocking,
// through a descriptor of its own (lockSweep). That fails while any sharer
// exists, and the sweep is skipped. Otherwise the owner keeps this sweep lock
// while it renames expired directories aside and removes their markers (a
// worker starting meanwhile waits in its shared lock), and releases it before
// deleting the renamed directories, so a long delete never holds up another
// worker's start.
//
// At each sweep tick a sharer tries the owner lock again without blocking (the
// owner may have exited, e.g. after an overlapping restart). Once it has it,
// it releases its sharer lock (promote) and sweeps from the next tick on.
//
// flock locks belong to the open file description, so a held lock's
// descriptor is opened once and kept (os.OpenFile sets O_CLOEXEC: tool
// processes do not inherit it). Workers older than these lock files do not
// take them and are not detected.

// Lock file names in the workdir.
const (
	workDirOwnerLockName  = ".gowe-worker.lock"
	workDirSharerLockName = ".gowe-worker.shared"
)

// flockSyscall is flock(2); a variable so tests can inject an error.
var flockSyscall = syscall.Flock

// workDirLock is this worker's lock on its workdir: exactly one of owner and
// sharer is set.
type workDirLock struct {
	dir string

	mu           sync.Mutex // guards the fields below; held from lockWorkDirForSweep until its release
	owner        *os.File   // owner lock file, held exclusively (this process is the owner)
	sharer       *os.File   // sharer lock file, held shared (this process is a sharer)
	sharedWarned bool       // the "shared with another worker process" WARN was logged
}

// flock calls flock(2) on f, retrying on EINTR.
func flock(f *os.File, how int) error {
	for {
		err := flockSyscall(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// openLockFile opens (creating, mode 0644) the lock file path.
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open work directory lock: %w", err)
	}
	return f, nil
}

// tryLockFile opens the lock file path (openLockFile) and locks it with how
// (LOCK_SH or LOCK_EX) without blocking. On error nothing is held; the error
// wraps EWOULDBLOCK when another open file description holds a conflicting
// lock.
func tryLockFile(path string, how int) (*os.File, error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := flock(f, how|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

// unlockFile releases the lock on f and closes it. Closing releases the lock
// even when LOCK_UN failed.
func unlockFile(f *os.File) error {
	err := flock(f, syscall.LOCK_UN)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// openWorkDirLock takes this worker's lock on workDir: the owner lock if it is
// free, otherwise the sharer lock, blocking while the owner's sweep holds it;
// waiting is called once before blocking. On error nothing is held.
func openWorkDirLock(workDir string, waiting func()) (*workDirLock, error) {
	l := &workDirLock{dir: workDir}
	owner, err := tryLockFile(filepath.Join(workDir, workDirOwnerLockName), syscall.LOCK_EX)
	if err == nil {
		l.owner = owner
		return l, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, err
	}
	path := filepath.Join(workDir, workDirSharerLockName)
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	err = flock(f, syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		if waiting != nil {
			waiting()
		}
		err = flock(f, syscall.LOCK_SH)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s (shared): %w", path, err)
	}
	l.sharer = f
	return l, nil
}

// promote makes a sharer the owner when the owner lock is free: it takes the
// owner lock without blocking and only then releases and closes the sharer
// lock, so this process always holds one of them. Called with l.mu held. On
// error (wrapping EWOULDBLOCK while another process is the owner) the sharer
// lock is kept.
func (l *workDirLock) promote() error {
	f, err := tryLockFile(filepath.Join(l.dir, workDirOwnerLockName), syscall.LOCK_EX)
	if err != nil {
		return err
	}
	l.owner = f
	_ = unlockFile(l.sharer) // released by the close in any case
	l.sharer = nil
	return nil
}

// lockSweep takes the owner's sweep lock: the sharer lock file, exclusively and
// without blocking, through a descriptor of its own. It succeeds only while no
// other process holds the sharer lock, i.e. no other worker process uses the
// workdir; while it is held, a worker starting on the workdir waits in
// openWorkDirLock. The error wraps EWOULDBLOCK when a sharer exists. Release
// it with unlockFile.
func (l *workDirLock) lockSweep() (*os.File, error) {
	return tryLockFile(filepath.Join(l.dir, workDirSharerLockName), syscall.LOCK_EX)
}

// close releases the lock (tests; a worker keeps it until it exits).
func (l *workDirLock) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for _, f := range []*os.File{l.owner, l.sharer} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	l.owner, l.sharer = nil, nil
	return errors.Join(errs...)
}

// lockWorkDir takes this worker's lock on its workdir (see above). On failure
// the worker continues without one, with one WARN, and its sweep is disabled.
func (w *Worker) lockWorkDir() {
	l, err := openWorkDirLock(w.workDir, func() {
		w.logger.Info("waiting for another worker process to finish removing kept task directories in the work directory",
			"workdir", w.workDir)
	})
	if err != nil {
		w.logger.Warn("cannot lock the work directory; continuing without a lock: other worker processes on it cannot see this one, and this worker does not remove kept task directories",
			"workdir", w.workDir, "kept_task_dir_ttl", w.keptTaskDirTTL, "error", err)
		return
	}
	w.wdLock = l
	if l.owner != nil {
		w.logger.Debug("work directory lock taken: owner", "workdir", w.workDir)
	} else {
		w.logger.Debug("work directory lock taken: shared with another worker process", "workdir", w.workDir)
	}
}

// lockWorkDirForSweep reports whether a sweep may run now and, when ok, takes
// the sweep lock; release must then be called once the directories are moved
// aside (before they are deleted). ok is false when there is no workdir lock
// (opening it failed, logged then), when this worker is a sharer (it tries to
// become the owner instead and, if it does, sweeps from the next call on),
// and when the owner finds another worker process on the workdir (WARN once
// per process, DEBUG afterwards) or cannot take the sweep lock otherwise
// (WARN). l.mu is held until release, so one sweep at a time holds the lock.
func (w *Worker) lockWorkDirForSweep() (release func(), ok bool) {
	l := w.wdLock
	if l == nil {
		w.logger.Debug("kept task dir sweep skipped: no work directory lock")
		return nil, false
	}
	l.mu.Lock()
	if l.owner == nil {
		err := l.promote()
		switch {
		case err == nil:
			w.logger.Info("this worker now owns the work directory lock; kept task directories may be removed from the next sweep on",
				"workdir", w.workDir)
		case errors.Is(err, syscall.EWOULDBLOCK):
			w.logSharedWorkDir(l)
		default:
			w.logger.Warn("kept task dir sweep skipped: cannot try to take over the work directory lock", "workdir", w.workDir, "error", err)
		}
		l.mu.Unlock()
		return nil, false
	}
	f, err := l.lockSweep()
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			w.logSharedWorkDir(l)
		} else {
			w.logger.Warn("kept task dir sweep skipped: cannot lock the work directory exclusively", "workdir", w.workDir, "error", err)
		}
		l.mu.Unlock()
		return nil, false
	}
	return func() {
		if err := unlockFile(f); err != nil {
			w.logger.Debug("kept task dir sweep: releasing the sweep lock", "workdir", w.workDir, "error", err)
		}
		l.mu.Unlock()
	}, true
}

// logSharedWorkDir logs that a sweep is skipped because another worker
// process uses the workdir: WARN the first time, DEBUG afterwards. Called with
// l.mu held.
func (w *Worker) logSharedWorkDir(l *workDirLock) {
	if !l.sharedWarned {
		l.sharedWarned = true
		w.logger.Warn("work directory is shared with another worker process; kept task directories are not removed",
			"workdir", w.workDir)
		return
	}
	w.logger.Debug("kept task dir sweep skipped: work directory is shared with another worker process", "workdir", w.workDir)
}
