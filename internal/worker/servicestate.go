package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Services must not outlive their worker (this version): each running
// service has a state file in <workdir>/.gowe-services, outside every task
// dir (so it is never bound into a container), and a service-mode worker that
// starts on a work directory first stops what a dead worker process left
// running there (crash, SIGKILL, shutdown wait expired). A state file names
// its writer (host, boot, worker pid and start time), so a worker never
// touches the services of another host, of an earlier boot, or of a worker
// process that is still running: the sweep does not depend on being the only
// worker on the work directory.

// serviceStateDirName is the directory of the state files in the workdir. It
// starts with a dot: never a task id, never a kept-dir base (validKeptBase).
const serviceStateDirName = ".gowe-services"

// serviceStateSuffix ends the name "<task id>.json" of a state file.
const serviceStateSuffix = ".json"

// serviceSweepLockName is the lock file in the state directory that keeps two
// start-up sweeps from running at once. It does not end in serviceStateSuffix.
const serviceSweepLockName = ".sweep.lock"

// serviceStateMaxBytes caps the size of a state file the sweep reads.
const serviceStateMaxBytes = 4096

// leftoverServiceStopWait is how long the start-up sweep waits after SIGTERM
// before it sends SIGKILL to a left-behind service's process group. A
// variable so tests can shrink it.
var leftoverServiceStopWait = 10 * time.Second

// testHookServiceIdentity, when set, may change the identity a worker writes
// into its state files (tests: two workers in one test process).
var testHookServiceIdentity func(w *Worker, id *serviceIdentity)

// serviceIdentity identifies the worker process writing state files.
type serviceIdentity struct {
	hostID    string // the worker's host id
	bootID    string // /proc/sys/kernel/random/boot_id, trimmed
	pid       int    // the worker's pid
	startTime uint64 // field 22 of the worker's /proc/<pid>/stat
}

// serviceState is the content of a state file.
type serviceState struct {
	TaskID string `json:"task_id"`
	PID    int    `json:"pid"`
	PGID   int    `json:"pgid"`

	// StartTime is field 22 of /proc/<pid>/stat (clock ticks since boot):
	// with PID and PGID it identifies the process even after a pid reuse.
	StartTime  uint64    `json:"start_time"`
	Port       int       `json:"port"`
	LaunchedAt time.Time `json:"launched_at"`

	// The writer: host and boot (a file of another host on a shared work
	// directory, or of an earlier boot, is never acted on) and the worker
	// process (while it runs, it owns the service).
	HostID          string `json:"host_id"`
	BootID          string `json:"boot_id"`
	WorkerPID       int    `json:"worker_pid"`
	WorkerStartTime uint64 `json:"worker_start_time"`
}

// validServiceTaskID reports whether id may name a state file (and a service
// task dir): one or more of [A-Za-z0-9._-], not starting with a dot.
func validServiceTaskID(id string) bool {
	if id == "" || id[0] == '.' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// serviceStateDir is the state directory of the work directory workDir.
func serviceStateDir(workDir string) string {
	return filepath.Join(workDir, serviceStateDirName)
}

// serviceStatePath is the state file of task taskID in the state dir.
func serviceStatePath(dir, taskID string) string {
	return filepath.Join(dir, taskID+serviceStateSuffix)
}

// readBootID returns the trimmed contents of the boot id file.
func readBootID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return id, nil
}

// initServiceState prepares the state files of a service-mode worker at
// start-up: it creates <workdir>/.gowe-services (mode 0700; an existing one
// must be a directory, not a symlink, and is set to 0700) and determines this
// worker's identity (host id, boot id, pid, start time). An error stops the
// start: without state files a dead worker's services would never be found.
func (w *Worker) initServiceState() error {
	dir := serviceStateDir(w.workDir)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("service mode: create the state directory %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("service mode: state directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("service mode: %s is not a directory (%s)", dir, fi.Mode().Type())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("service mode: restrict the state directory %s: %w", dir, err)
	}
	ps, err := readProcStat(os.Getpid())
	if err != nil {
		return fmt.Errorf("service mode: read this worker's start time: %w", err)
	}
	boot, err := readBootID(bootIDPath)
	if err != nil {
		return fmt.Errorf("service mode: read the boot id: %w", err)
	}
	id := serviceIdentity{hostID: w.hostID, bootID: boot, pid: os.Getpid(), startTime: ps.startTime}
	if testHookServiceIdentity != nil {
		testHookServiceIdentity(w, &id)
	}
	w.svcStateDir = dir
	w.svcIdent = id
	return nil
}

// writeServiceState writes st to <dir>/<task id>.json atomically (a temp file
// of mode 0600 in the same directory, fsync, rename) and syncs the directory
// (best effort).
func writeServiceState(dir string, st serviceState) error {
	if !validServiceTaskID(st.TaskID) {
		return fmt.Errorf("service state: task id %q cannot name a state file", st.TaskID)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("service state: %w", err)
	}
	// The temporary name starts with a dot and does not end in ".json": the
	// sweep never reads it.
	tmp, err := os.CreateTemp(dir, "."+st.TaskID+serviceStateSuffix+".tmp-*")
	if err != nil {
		return fmt.Errorf("service state: %w", err)
	}
	name := tmp.Name()
	if err := writeSyncClose(tmp, data); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("service state: %w", err)
	}
	if err := os.Rename(name, serviceStatePath(dir, st.TaskID)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("service state: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// writeSyncClose writes data to f (mode 0600), syncs and closes it; f is
// closed on every path.
func writeSyncClose(f *os.File, data []byte) error {
	err := f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// removeServiceState removes the state file of task taskID in dir.
func removeServiceState(dir, taskID string) error {
	err := os.Remove(serviceStatePath(dir, taskID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// readServiceState reads a state file: a regular file only (never through a
// symlink, never blocking on a FIFO), at most serviceStateMaxBytes, with a
// valid task id, a pid and process group above 1, a start time, and a
// writing worker (pid above 1, start time).
func readServiceState(path string) (serviceState, error) {
	var st serviceState
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return st, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return st, err
	}
	if !fi.Mode().IsRegular() {
		return st, fmt.Errorf("not a regular file (%s)", fi.Mode().Type())
	}
	if fi.Size() > serviceStateMaxBytes {
		return st, fmt.Errorf("larger than %d bytes", serviceStateMaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, serviceStateMaxBytes+1))
	if err != nil {
		return st, err
	}
	if len(data) > serviceStateMaxBytes {
		return st, fmt.Errorf("larger than %d bytes", serviceStateMaxBytes)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if !validServiceTaskID(st.TaskID) {
		return st, fmt.Errorf("invalid task id %q", st.TaskID)
	}
	if st.PID <= 1 || st.PGID <= 1 || st.StartTime == 0 {
		return st, fmt.Errorf("invalid content (pid %d, pgid %d, start time %d)", st.PID, st.PGID, st.StartTime)
	}
	if st.WorkerPID <= 1 || st.WorkerStartTime == 0 {
		return st, fmt.Errorf("invalid content (worker pid %d, worker start time %d)", st.WorkerPID, st.WorkerStartTime)
	}
	return st, nil
}

// recordServiceState writes the state file of the service of task taskID
// with process pid. The process's start time and process group come from
// /proc/<pid>/stat; done (the handle's Done) still open after that read
// proves the pid was still the service's (not reaped, so not reused). An
// error means no state file was written (logged by the caller; the service
// runs anyway).
func (w *Worker) recordServiceState(taskID string, pid, port int, launched time.Time, done <-chan struct{}) error {
	if w.svcStateDir == "" {
		return errors.New("service state: no state directory")
	}
	ps, err := readProcStat(pid)
	if err != nil {
		return fmt.Errorf("service state: read process %d: %w", pid, err)
	}
	select {
	case <-done:
		return errors.New("service state: the process has already exited")
	default:
	}
	return writeServiceState(w.svcStateDir, serviceState{
		TaskID:          taskID,
		PID:             pid,
		PGID:            ps.pgrp,
		StartTime:       ps.startTime,
		Port:            port,
		LaunchedAt:      launched.UTC(),
		HostID:          w.svcIdent.hostID,
		BootID:          w.svcIdent.bootID,
		WorkerPID:       w.svcIdent.pid,
		WorkerStartTime: w.svcIdent.startTime,
	})
}

// lockServiceSweep takes the sweep lock (<state dir>/.sweep.lock, flock
// exclusive), waiting while another worker process's sweep holds it. Release
// it with unlockFile.
func (w *Worker) lockServiceSweep() (*os.File, error) {
	path := filepath.Join(w.svcStateDir, serviceSweepLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	err = flock(f, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		w.logger.Info("waiting for another worker process to finish looking for services left behind", "workdir", w.workDir)
		err = flock(f, syscall.LOCK_EX)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}

// sweepLeftoverServices stops the services dead worker processes left running
// on this work directory, from their state files (<workdir>/.gowe-services/
// <task id>.json; regular files only). Service mode only, at start-up after
// initServiceState, before registration; under the sweep lock, so two sweeps
// never run at once. Whether other worker processes use the directory does
// not matter: a file of another host or boot, or of a worker process that is
// still running, is left alone (sweepServiceState).
func (w *Worker) sweepLeftoverServices() {
	if w.svcStateDir == "" {
		w.logger.Warn("not looking for services left behind by an earlier worker process: no state directory", "workdir", w.workDir)
		return
	}
	lock, err := w.lockServiceSweep()
	if err != nil {
		w.logger.Warn("not looking for services left behind by an earlier worker process: cannot take the sweep lock", "workdir", w.workDir, "error", err)
		return
	}
	defer func() { _ = unlockFile(lock) }()

	entries, err := os.ReadDir(w.svcStateDir)
	if err != nil {
		w.logger.Warn("not looking for services left behind by an earlier worker process", "dir", w.svcStateDir, "error", err)
		return
	}
	otherHost := 0
	for _, e := range entries {
		name := e.Name()
		// Regular "<task id>.json" files only: no lock file, no temporary
		// file, no symlink, no directory.
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, serviceStateSuffix) || !e.Type().IsRegular() {
			continue
		}
		if w.sweepServiceState(name) {
			otherHost++
		}
	}
	if otherHost > 0 {
		w.logger.Info("service state files of another host or an earlier boot left alone", "dir", w.svcStateDir, "count", otherHost)
	}
}

// sweepServiceState handles the state file name of the state dir:
//
//   - unreadable or invalid, or its name not "<task id>.json" of its own task
//     id: left alone with a WARN;
//   - written on another host or in another boot: left alone, never signalled;
//     it returns true (counted by the caller for one INFO line);
//   - its writing worker is still running (same pid and start time): left
//     alone, that worker owns the service;
//   - otherwise, the recorded process still running with the same pid, start
//     time and process group, and leading it (pid = pgid), has its group
//     stopped (SIGTERM, SIGKILL after leftoverServiceStopWait) with one WARN;
//     the file is then removed, as is a file whose process is gone. Never
//     signals on a pid match alone.
func (w *Worker) sweepServiceState(name string) (otherHost bool) {
	path := filepath.Join(w.svcStateDir, name)
	st, err := readServiceState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		w.logger.Warn("ignoring an unreadable service state file", "path", path, "error", err)
		return false
	}
	if name != st.TaskID+serviceStateSuffix {
		w.logger.Warn("ignoring a service state file whose name does not match its task id", "path", path, "task_id", st.TaskID)
		return false
	}
	if st.HostID != w.svcIdent.hostID || st.BootID != w.svcIdent.bootID || w.svcIdent.bootID == "" {
		return true
	}
	if ws, err := readProcStat(st.WorkerPID); err == nil && ws.startTime == st.WorkerStartTime {
		w.logger.Info("service state file of a running worker process left alone", "task_id", st.TaskID, "worker_pid", st.WorkerPID)
		return false
	}
	if ps, err := readProcStat(st.PID); err == nil && st.PID == st.PGID && ps.startTime == st.StartTime && ps.pgrp == st.PGID {
		killed := stopProcessGroup(st.PGID, leftoverServiceStopWait)
		w.logger.Warn("stopped a service left behind by an earlier worker process",
			"task_id", st.TaskID, "pid", st.PID, "port", st.Port, "launched_at", st.LaunchedAt, "worker_pid", st.WorkerPID, "killed", killed)
	} else {
		w.logger.Debug("service state file of an ended service removed", "path", path, "task_id", st.TaskID, "pid", st.PID)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.logger.Warn("cannot remove a service state file", "path", path, "error", err)
	}
	return false
}

// stopProcessGroup sends SIGTERM to process group pgid, waits up to wait for
// it to be gone and sends SIGKILL if it is still there. It reports whether
// SIGKILL was sent.
func stopProcessGroup(pgid int, wait time.Duration) bool {
	if pgid <= 1 {
		return false
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return false // gone (ESRCH) or not ours (EPERM)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(-pgid, syscall.SIGKILL) == nil
}
