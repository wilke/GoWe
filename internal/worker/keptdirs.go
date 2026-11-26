package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kept task directories and their removal (--kept-task-dir-ttl).
//
// A task directory the worker deliberately keeps ("<id>", or an earlier
// attempt's "<id>.attempt-N", each with its "_tmp" / "_staging" siblings) gets
// a marker file "<workdir>/<base>.kept" next to it. Only directories with a
// marker are ever removed by the sweep; directories without one (kept before
// markers existed, outputs referenced in place, a run the worker was killed
// during) are never touched. A sweep runs only in the worker process that owns
// the workdir, and only while no other worker process uses it (workdirlock.go).

// keptMarkerSuffix is the file name suffix of a kept-dir marker.
const keptMarkerSuffix = ".kept"

// keptSweptPrefix starts the name "<prefix><base><suffix>-<unix nanos>" a
// sweep renames an expired directory to before removing it (isSweptName).
const keptSweptPrefix = ".gowe-swept-"

// Reasons recorded in a kept-dir marker.
const (
	keptReasonFailed         = "failed"
	keptReasonCancelled      = "cancelled"
	keptReasonDebug          = "debug"
	keptReasonKeepTaskDirs   = "keep-task-dirs"
	keptReasonEarlierAttempt = "earlier-attempt"
)

// runOutcomeSuccess is the outcome recorded (activeTaskSet.setOutcome) for a
// run whose final report was SUCCESS: cleanupTaskDir decides about the
// directory and its marker, the end of runTask writes none.
const runOutcomeSuccess = "success"

// minKeptTaskDirTTL is the smallest positive --kept-task-dir-ttl, so a typo
// (e.g. "30m" meant as days) cannot delete fresh evidence.
const minKeptTaskDirTTL = time.Hour

// keptDirSweepInterval is the time between sweeps after the first one, which
// keptDirSweepLoop runs right away. A variable so tests can shrink it.
var keptDirSweepInterval = time.Hour

// keptMarkerMaxBytes is the largest marker file that is read; a larger one is
// skipped.
const keptMarkerMaxBytes = 4096

// keptMarker is the content of a "<base>.kept" marker file.
type keptMarker struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
	KeptAt string `json:"kept_at"` // RFC 3339, UTC
}

// ValidateKeptTaskDirTTL checks --kept-task-dir-ttl: 0 (never remove) or at
// least minKeptTaskDirTTL.
func ValidateKeptTaskDirTTL(d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("--kept-task-dir-ttl must not be negative (got %s)", d)
	}
	if d > 0 && d < minKeptTaskDirTTL {
		return fmt.Errorf("--kept-task-dir-ttl must be at least 1h (got %s; 0 never removes kept task dirs)", d)
	}
	return nil
}

// keptMarkerPath is the marker file of the kept directory base in workDir.
func keptMarkerPath(workDir, base string) string {
	return filepath.Join(workDir, base+keptMarkerSuffix)
}

// writeKeptMarker writes m as the marker of base atomically (temporary file in
// workDir, then rename), mode 0644, replacing an existing marker.
func writeKeptMarker(workDir, base string, m keptMarker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode kept-dir marker: %w", err)
	}
	// The temporary name does not end in ".kept", so a sweep never reads it.
	f, err := os.CreateTemp(workDir, "."+base+keptMarkerSuffix+".tmp-*")
	if err != nil {
		return fmt.Errorf("create kept-dir marker: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, keptMarkerPath(workDir, base))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write kept-dir marker %s: %w", keptMarkerPath(workDir, base), err)
	}
	return nil
}

// readKeptMarker reads and checks a marker file. It is opened without
// following a symlink and without blocking (a FIFO), and must be a regular
// file of at most keptMarkerMaxBytes holding valid JSON with a task id and an
// RFC 3339 kept_at that is not zero. It returns the marker, its kept_at and
// the file's modification time. A missing file gives an fs.ErrNotExist error.
func readKeptMarker(path string) (keptMarker, time.Time, time.Time, error) {
	var m keptMarker
	fail := func(err error) (keptMarker, time.Time, time.Time, error) {
		return m, time.Time{}, time.Time{}, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !fi.Mode().IsRegular() {
		return fail(fmt.Errorf("not a regular file (mode %s)", fi.Mode()))
	}
	if fi.Size() > keptMarkerMaxBytes {
		return fail(fmt.Errorf("larger than %d bytes", keptMarkerMaxBytes))
	}
	data, err := io.ReadAll(io.LimitReader(f, keptMarkerMaxBytes+1))
	if err != nil {
		return fail(err)
	}
	if len(data) > keptMarkerMaxBytes {
		return fail(fmt.Errorf("larger than %d bytes", keptMarkerMaxBytes))
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return fail(fmt.Errorf("invalid marker: %w", err))
	}
	if m.TaskID == "" {
		return fail(errors.New("invalid marker: no task_id"))
	}
	keptAt, err := time.Parse(time.RFC3339, m.KeptAt)
	if err != nil {
		return fail(fmt.Errorf("invalid marker: kept_at: %w", err))
	}
	if keptAt.IsZero() || keptAt.Unix() <= 0 {
		return fail(fmt.Errorf("invalid marker: kept_at %q is zero", m.KeptAt))
	}
	return m, keptAt, fi.ModTime(), nil
}

// validKeptBase reports whether base (a marker's file name without ".kept")
// may be removed for a marker naming taskID: a single path element that is
// the task's live directory "<task id>" or one of its earlier attempts
// "<task id>.attempt-<digits>". The task id itself must be a plain name too.
func validKeptBase(base, taskID string) bool {
	plain := func(s string) bool {
		// No leading dot: the worker's own files in the work directory
		// (lock files, tombstones, marker temp files) all start with one.
		return s != "" && !strings.HasPrefix(s, ".") &&
			!strings.Contains(s, "..") &&
			!strings.ContainsRune(s, '/') && !strings.ContainsRune(s, filepath.Separator) &&
			!strings.HasSuffix(s, keptMarkerSuffix)
	}
	if !plain(base) || !plain(taskID) {
		return false
	}
	if base == taskID {
		return true
	}
	n, ok := strings.CutPrefix(base, taskID+".attempt-")
	if !ok || n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// markKeptDir writes the marker of the kept directory base of task taskID,
// stamped now. A failure is logged at WARN; it never fails the task.
func (w *Worker) markKeptDir(taskID, base, reason string) {
	m := keptMarker{TaskID: taskID, Reason: reason, KeptAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeKeptMarker(w.workDir, base, m); err != nil {
		w.logger.Warn("kept-dir marker not written", "task_id", taskID, "dir", base, "reason", reason, "error", err)
	}
}

// removeKeptMarker removes the marker of base if there is one.
func (w *Worker) removeKeptMarker(base string) {
	if err := os.Remove(keptMarkerPath(w.workDir, base)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.logger.Warn("kept-dir marker not removed", "dir", base, "error", err)
	}
}

// markUnsuccessfulRun is called at the end of every run of task taskID that
// created its task dir (deferred in runTask, panic included). Unless the run
// reported SUCCESS — cleanupTaskDir then decided — the task dir gets a marker
// if it is still the one this run created (ownsTaskDir): "cancelled" when the
// run ended through cancellation (SKIPPED on a server cancel, or left for
// requeue at shutdown), "failed" otherwise (a FAILED report, or no report at
// all after a panic).
func (w *Worker) markUnsuccessfulRun(taskID string) {
	reason := keptReasonFailed
	switch w.active.outcome(taskID) {
	case runOutcomeSuccess:
		return
	case keptReasonCancelled:
		reason = keptReasonCancelled
	}
	if !w.ownsTaskDir(taskID) {
		return
	}
	w.markKeptDir(taskID, taskID, reason)
}

// sameTaskDir reports whether dir exists (os.Stat) and is the directory
// created describes (os.SameFile). False when created is nil.
func sameTaskDir(dir string, created os.FileInfo) bool {
	if created == nil {
		return false
	}
	fi, err := os.Stat(dir)
	return err == nil && os.SameFile(created, fi)
}

// ownsTaskDir reports whether the task dir of the running task taskID is
// still the directory its run created (recorded by prepareTaskDir), so a
// marker is never written for a directory that replaced it meanwhile. A
// mismatch, a missing dir or nothing recorded is logged at DEBUG.
func (w *Worker) ownsTaskDir(taskID string) bool {
	if sameTaskDir(filepath.Join(w.workDir, taskID), w.active.taskDir(taskID)) {
		return true
	}
	w.logger.Debug("kept-dir marker not written: the task dir is missing or not the one this run created", "task_id", taskID)
	return false
}

// prepareTaskDir archives what an earlier attempt of task taskID left
// (archivePreviousAttempt, moving its marker along, see moveKeptMarker) and
// creates the fresh task dir, recording its os.FileInfo in w.active for
// ownsTaskDir. It holds keptMu throughout, so a sweep never removes
// directories between these steps. archived is the archived base path ("" if
// nothing was archived), also when creating the task dir failed.
func (w *Worker) prepareTaskDir(taskID string) (archived string, err error) {
	w.keptMu.Lock()
	defer w.keptMu.Unlock()
	archived, err = archivePreviousAttempt(w.workDir, taskID)
	if err != nil {
		return "", fmt.Errorf("archive previous attempt: %w", err)
	}
	w.moveKeptMarker(taskID, archived)
	taskDir := filepath.Join(w.workDir, taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return archived, fmt.Errorf("create task dir: %w", err)
	}
	fi, err := os.Stat(taskDir)
	if err != nil {
		w.logger.Debug("task dir not recorded: this run writes no kept-dir marker", "task_id", taskID, "error", err)
		return archived, nil
	}
	w.active.setTaskDir(taskID, fi)
	return archived, nil
}

// moveKeptMarker moves the marker "<id>.kept" along with the directories
// archivePreviousAttempt renamed to archived ("<workdir>/<id>.attempt-N"),
// replacing a stale "<id>.attempt-N.kept". An archived attempt without a
// marker (the worker stopped during it) gets one with reason
// "earlier-attempt". When nothing was archived, a "<id>.kept" left without
// directories is removed, so the new run starts without a marker.
func (w *Worker) moveKeptMarker(taskID, archived string) {
	if archived == "" {
		w.removeKeptMarker(taskID)
		return
	}
	base := filepath.Base(archived)
	err := os.Rename(keptMarkerPath(w.workDir, taskID), keptMarkerPath(w.workDir, base))
	if err == nil {
		return
	}
	if !errors.Is(err, fs.ErrNotExist) {
		w.logger.Warn("kept-dir marker not moved with the earlier attempt", "task_id", taskID, "archived", base, "error", err)
		w.removeKeptMarker(taskID)
	}
	w.markKeptDir(taskID, base, keptReasonEarlierAttempt)
}

// keptDirSweepLoop runs sweepKeptTaskDirs right away and then every
// keptDirSweepInterval until ctx ends. It runs in its own goroutine, so the
// first sweep never delays registration or polling. In a worker that shares
// the workdir as a sharer, each tick tries to take over the owner lock instead
// (lockWorkDirForSweep).
func (w *Worker) keptDirSweepLoop(ctx context.Context) {
	w.sweepKeptTaskDirs(ctx, time.Now())
	ticker := time.NewTicker(keptDirSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweepKeptTaskDirs(ctx, time.Now())
		}
	}
}

// sweepKeptTaskDirs removes the kept directories whose marker is older than
// --kept-task-dir-ttl at now, and returns for how many markers it did. It does
// nothing when the TTL is 0, and runs only when this worker owns the workdir
// and no other worker process uses it (lockWorkDirForSweep). The workdir is
// listed once, after the sweep lock was taken. While the lock is held, each
// marker is handled (sweepKeptMarker: its directories renamed aside, the
// marker removed). The lock is then released, and the leftovers of an
// interrupted earlier sweep ("<keptSweptPrefix>...") and the directories this
// sweep moved aside are removed, outside the lock and keptMu, so a long
// removal never delays a task start here or another worker's start. It stops
// between markers and removals when ctx is done; what it moved aside but did
// not remove yet goes in the next sweep.
func (w *Worker) sweepKeptTaskDirs(ctx context.Context, now time.Time) (removed int) {
	if w.keptTaskDirTTL <= 0 {
		return 0
	}
	release, ok := w.lockWorkDirForSweep()
	if !ok {
		return 0
	}
	unlock := func() {
		if release != nil {
			release()
			release = nil
		}
	}
	defer unlock()

	ents, err := os.ReadDir(w.workDir)
	if err != nil {
		w.logger.Warn("kept task dir sweep: cannot list workdir", "workdir", w.workDir, "error", err)
		return 0
	}
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		w.logger.Debug("kept task dir sweep stopped", "removed", removed)
		return true
	}
	// Leftovers first, then what this sweep moves aside.
	var tombs []string
	for _, e := range ents {
		if isSweptName(e.Name()) {
			tombs = append(tombs, filepath.Join(w.workDir, e.Name()))
		}
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), keptMarkerSuffix) {
			continue
		}
		if stopped() {
			return removed
		}
		swept, ok := w.sweepKeptMarker(e, now)
		if ok {
			removed++
		}
		tombs = append(tombs, swept...)
	}
	// Everything to delete is out of the way under a name no task uses: a
	// worker waiting to start on the workdir may go on.
	unlock()
	for _, p := range tombs {
		if stopped() {
			return removed
		}
		w.removeSwept(p)
	}
	if removed > 0 {
		w.logger.Info("kept task dirs removed", "removed", removed, "ttl", w.keptTaskDirTTL)
	} else {
		w.logger.Debug("kept task dir sweep: nothing to remove", "ttl", w.keptTaskDirTTL)
	}
	return removed
}

// isSweptName reports whether name is one a sweep gives a directory it moved
// aside: "<keptSweptPrefix><something>-<digits>".
func isSweptName(name string) bool {
	rest, ok := strings.CutPrefix(name, keptSweptPrefix)
	if !ok {
		return false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 || i == len(rest)-1 {
		return false
	}
	for _, c := range rest[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// removeSwept removes the moved-aside path p (removeKeptPath); a failure is
// logged and retried by the next sweep.
func (w *Worker) removeSwept(p string) {
	if err := removeKeptPath(p); err != nil {
		w.logger.Warn("kept task dir sweep: cannot remove, retried next sweep", "dir", p, "error", err)
	}
}

// sweepKeptMarker handles one marker file e of the workdir under keptMu (see
// prepareTaskDir). ok reports whether the marker expired and was handled:
// its existing "<base>", "<base>_tmp" and "<base>_staging" were renamed to
// "<keptSweptPrefix><base><suffix>-<unix nanos>" in the workdir (returned in
// swept, for the caller to remove after keptMu is released), then the marker
// was removed. If a rename fails, the marker stays (retried next sweep) and
// swept holds what was already renamed.
//
// The base is the marker's file name without ".kept", never a path from its
// content, and only when it is a single path element equal to the marker's
// task id or "<task id>.attempt-<digits>" (validKeptBase); an unreadable or
// invalid marker (also a zero kept_at) is skipped with a WARN. The age is now
// minus the later of kept_at and the marker file's modification time. Kept
// are: markers whose age is not above the TTL, a kept_at in the future, the
// live "<task id>" of a task this worker is running (its earlier attempts may
// go), and any base that is itself the id of a running task.
func (w *Worker) sweepKeptMarker(e fs.DirEntry, now time.Time) (swept []string, ok bool) {
	name := e.Name()
	base := strings.TrimSuffix(name, keptMarkerSuffix)
	if !e.Type().IsRegular() {
		w.logger.Warn("kept task dir sweep: marker is not a regular file, skipped", "marker", name)
		return nil, false
	}

	w.keptMu.Lock()
	defer w.keptMu.Unlock()

	// Read under the lock: a run may have moved or rewritten the marker since
	// the workdir was listed.
	m, keptAt, modTime, err := readKeptMarker(filepath.Join(w.workDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		w.logger.Warn("kept task dir sweep: unreadable marker, skipped", "marker", name, "error", err)
		return nil, false
	}
	if !validKeptBase(base, m.TaskID) {
		w.logger.Warn("kept task dir sweep: marker name does not match its task id, skipped", "marker", name, "task_id", m.TaskID)
		return nil, false
	}
	if keptAt.After(now) {
		w.logger.Debug("kept task dir sweep: kept_at is in the future, kept", "marker", name, "kept_at", m.KeptAt)
		return nil, false
	}
	since := keptAt
	if modTime.After(since) {
		since = modTime
	}
	if now.Sub(since) <= w.keptTaskDirTTL {
		return nil, false
	}
	if base == m.TaskID && w.active.has(m.TaskID) {
		w.logger.Debug("kept task dir sweep: task is running, live dir kept", "task_id", m.TaskID)
		return nil, false
	}
	if w.active.has(base) {
		w.logger.Debug("kept task dir sweep: a running task has this directory's name, kept", "dir", base, "task_id", m.TaskID)
		return nil, false
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	for _, s := range taskDirSuffixes {
		p := filepath.Join(w.workDir, base+s)
		exists, err := pathExists(p)
		if err != nil {
			w.logger.Warn("kept task dir sweep: cannot check, retried next sweep", "dir", p, "error", err)
			return swept, false
		}
		if !exists {
			continue
		}
		tomb := filepath.Join(w.workDir, keptSweptPrefix+base+s+"-"+stamp)
		if err := os.Rename(p, tomb); err != nil {
			w.logger.Warn("kept task dir sweep: cannot move aside for removal, retried next sweep", "dir", p, "error", err)
			return swept, false
		}
		swept = append(swept, tomb)
	}
	if err := os.Remove(filepath.Join(w.workDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.logger.Warn("kept task dir sweep: cannot remove marker", "marker", name, "error", err)
		return swept, false
	}
	w.logger.Debug("kept task dir removed", "task_id", m.TaskID, "dir", base, "reason", m.Reason, "kept_at", m.KeptAt)
	return swept, true
}

// removeKeptPath removes p: a symlink is unlinked (its target is untouched), a
// directory removed recursively (os.RemoveAll does not follow symlinks inside
// the tree). A missing p is not an error.
func removeKeptPath(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return os.Remove(p)
	}
	return os.RemoveAll(p)
}
