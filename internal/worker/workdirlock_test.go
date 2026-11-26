package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// openTestLockFD opens the lock file name of wd as another open file
// description (flock locks belong to it, so this behaves like another worker
// process), closed at the end of the test.
func openTestLockFD(t *testing.T, wd, name string) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(wd, name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// testFlock locks f with how (blocking unless how has LOCK_NB).
func testFlock(t *testing.T, f *os.File, how int) {
	t.Helper()
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		t.Fatalf("flock: %v", err)
	}
}

// lockFree reports whether another open file description could lock the lock
// file name of wd with how (LOCK_SH or LOCK_EX) right now.
func lockFree(t *testing.T, wd, name string, how int) bool {
	t.Helper()
	f := openTestLockFD(t, wd, name)
	err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return true
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("probe flock: %v", err)
	}
	return false
}

// newLockTestWorker is a worker on wd with a TTL, logging at DEBUG to logs.
func newLockTestWorker(wd string, logs io.Writer) *Worker {
	return &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// startTestWorker takes w's workdir lock as Run does, released at the end of
// the test.
func startTestWorker(t *testing.T, w *Worker) {
	t.Helper()
	w.lockWorkDir()
	if w.wdLock == nil {
		t.Fatal("no workdir lock")
	}
	l := w.wdLock
	t.Cleanup(func() { _ = l.close() })
}

// mkExpiredTestDir creates task_a and task_a_tmp with an expired marker.
func mkExpiredTestDir(t *testing.T, wd string) {
	t.Helper()
	mkAttemptDirs(t, wd, "task_a", "task_a_tmp")
	writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-3*time.Hour))
}

func wantPresent(t *testing.T, wd string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(wd, p)); err != nil {
			t.Errorf("%s: %v, want it kept", p, err)
		}
	}
}

func wantAbsent(t *testing.T, wd string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(wd, p)); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v)", p, err)
		}
	}
}

const sharedWorkDirWarn = `level=WARN msg="work directory is shared with another worker process; kept task directories are not removed"`

// The first worker on a workdir becomes the owner, a second one a sharer.
func TestLockWorkDir_Roles(t *testing.T) {
	wd := t.TempDir()
	w1 := newLockTestWorker(wd, io.Discard)
	startTestWorker(t, w1)
	if w1.wdLock.owner == nil || w1.wdLock.sharer != nil {
		t.Fatalf("first worker: owner=%v sharer=%v, want the owner", w1.wdLock.owner, w1.wdLock.sharer)
	}
	if lockFree(t, wd, workDirOwnerLockName, syscall.LOCK_EX) {
		t.Error("owner lock not held")
	}
	if !lockFree(t, wd, workDirSharerLockName, syscall.LOCK_EX) {
		t.Error("sharer lock held with no sharer")
	}

	w2 := newLockTestWorker(wd, io.Discard)
	startTestWorker(t, w2)
	if w2.wdLock.owner != nil || w2.wdLock.sharer == nil {
		t.Fatalf("second worker: owner=%v sharer=%v, want a sharer", w2.wdLock.owner, w2.wdLock.sharer)
	}
	if lockFree(t, wd, workDirSharerLockName, syscall.LOCK_EX) {
		t.Error("sharer lock not held")
	}
	// Shared: a third worker on the same workdir starts too.
	if !lockFree(t, wd, workDirSharerLockName, syscall.LOCK_SH) {
		t.Error("a further sharer cannot take the shared lock")
	}

	for _, name := range []string{workDirOwnerLockName, workDirSharerLockName} {
		fi, err := os.Stat(filepath.Join(wd, name))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&^0o644 != 0 {
			t.Errorf("%s mode = %v, want a regular file, 0644 less the umask", name, fi.Mode())
		}
	}
}

// The owner does not sweep while another worker process holds the sharer
// lock: nothing is removed, leftovers of an earlier sweep included (one
// WARN). Once the sharer is gone, it sweeps.
func TestSweepKeptTaskDirs_OwnerWithSharer(t *testing.T) {
	wd := t.TempDir()
	const tomb = ".gowe-swept-task_z-1"
	mkExpiredTestDir(t, wd)
	mkAttemptDirs(t, wd, tomb)
	var logs syncBuffer
	w := newLockTestWorker(wd, &logs)
	lockTestWorkDir(t, w)

	other := openTestLockFD(t, wd, workDirSharerLockName)
	testFlock(t, other, syscall.LOCK_SH)
	for i := 0; i < 3; i++ {
		if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
			t.Errorf("sweep %d with a sharer removed %d, want 0", i, got)
		}
	}
	wantPresent(t, wd, "task_a/f.txt", "task_a_tmp/f.txt", "task_a.kept", tomb)
	if n := strings.Count(logs.String(), sharedWorkDirWarn); n != 1 {
		t.Errorf("shared-workdir WARN logged %d times, want once:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("%d WARNs, want 1:\n%s", n, logs.String())
	}
	if !lockFree(t, wd, workDirSharerLockName, syscall.LOCK_SH) {
		t.Error("a skipped sweep left the sharer file locked exclusively")
	}

	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 1 {
		t.Errorf("removed = %d, want 1", got)
	}
	wantAbsent(t, wd, "task_a", "task_a_tmp", "task_a.kept", tomb)
	// Still the owner; the sweep lock is released.
	if lockFree(t, wd, workDirOwnerLockName, syscall.LOCK_EX) {
		t.Error("the owner lock is not held after the sweep")
	}
	if !lockFree(t, wd, workDirSharerLockName, syscall.LOCK_EX) {
		t.Error("the sweep lock is still held after the sweep")
	}
}

// A sharer never sweeps. When the owner goes away, the sharer becomes the
// owner at its next tick, releases its sharer lock, and sweeps at the tick
// after.
func TestSweepKeptTaskDirs_SharerPromotion(t *testing.T) {
	wd := t.TempDir()
	mkExpiredTestDir(t, wd)
	owner := openTestLockFD(t, wd, workDirOwnerLockName)
	testFlock(t, owner, syscall.LOCK_EX)

	var logs syncBuffer
	w := newLockTestWorker(wd, &logs)
	startTestWorker(t, w)
	if w.wdLock.sharer == nil {
		t.Fatal("not a sharer while another process owns the workdir")
	}
	for i := 0; i < 3; i++ {
		if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
			t.Errorf("sharer sweep %d removed %d, want 0", i, got)
		}
	}
	wantPresent(t, wd, "task_a/f.txt", "task_a_tmp/f.txt", "task_a.kept")
	if n := strings.Count(logs.String(), sharedWorkDirWarn); n != 1 {
		t.Errorf("shared-workdir WARN logged %d times, want once:\n%s", n, logs.String())
	}
	if w.wdLock.owner != nil || w.wdLock.sharer == nil {
		t.Fatal("sharer changed role while the owner is alive")
	}

	// The owner exits: the next tick promotes, without sweeping.
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
		t.Errorf("promoting tick removed %d, want 0", got)
	}
	wantPresent(t, wd, "task_a/f.txt", "task_a_tmp/f.txt", "task_a.kept")
	if w.wdLock.owner == nil || w.wdLock.sharer != nil {
		t.Fatalf("after the owner left: owner=%v sharer=%v, want the owner", w.wdLock.owner, w.wdLock.sharer)
	}
	if lockFree(t, wd, workDirOwnerLockName, syscall.LOCK_EX) {
		t.Error("owner lock not held after the promotion")
	}
	if !lockFree(t, wd, workDirSharerLockName, syscall.LOCK_EX) {
		t.Error("sharer lock still held after the promotion")
	}
	if !strings.Contains(logs.String(), `level=INFO msg="this worker now owns the work directory lock`) {
		t.Errorf("no promotion INFO:\n%s", logs.String())
	}

	// The tick after sweeps.
	if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 1 {
		t.Errorf("removed = %d, want 1", got)
	}
	wantAbsent(t, wd, "task_a", "task_a_tmp", "task_a.kept")
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("%d WARNs, want 1:\n%s", n, logs.String())
	}
}

// A worker starting while the owner holds the sweep lock waits in its shared
// lock until the sweep lock is released, and then is a sharer the owner sees.
func TestLockWorkDir_WaitsForSweep(t *testing.T) {
	wd := t.TempDir()
	w1 := newLockTestWorker(wd, io.Discard)
	startTestWorker(t, w1)
	release, ok := w1.lockWorkDirForSweep()
	if !ok {
		t.Fatal("owner without sharers did not get the sweep lock")
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()

	var logs syncBuffer
	w2 := newLockTestWorker(wd, &logs)
	done := make(chan struct{})
	go func() {
		w2.lockWorkDir()
		close(done)
	}()
	// Wait until the second worker reports that it is blocked, then check
	// that it really has not got the lock.
	waitFor(t, "the second worker to wait for the sweep lock", func() bool {
		return strings.Contains(logs.String(), "waiting for another worker process")
	})
	select {
	case <-done:
		t.Fatal("took the sharer lock while the owner held the sweep lock")
	default:
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not take the sharer lock after the sweep lock was released")
	}
	if w2.wdLock == nil || w2.wdLock.sharer == nil {
		t.Fatal("second worker is not a sharer")
	}
	l2 := w2.wdLock
	t.Cleanup(func() { _ = l2.close() })
	if !strings.Contains(logs.String(), "waiting for another worker process") {
		t.Errorf("no waiting INFO:\n%s", logs.String())
	}

	// The owner now sees the new sharer.
	mkExpiredTestDir(t, wd)
	if got := w1.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
		t.Errorf("owner swept %d with a sharer, want 0", got)
	}
	wantPresent(t, wd, "task_a/f.txt", "task_a.kept")
}

// Without a workdir lock (role "none": opening or locking failed) the worker
// continues with one WARN and never sweeps.
func TestLockWorkDir_None(t *testing.T) {
	check := func(t *testing.T, wd string) {
		t.Helper()
		var logs syncBuffer
		w := newLockTestWorker(wd, &logs)
		w.lockWorkDir()
		if w.wdLock != nil {
			_ = w.wdLock.close()
			t.Fatal("got a workdir lock")
		}
		if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
			t.Errorf("%d WARNs, want 1:\n%s", n, logs.String())
		}
		for i := 0; i < 2; i++ {
			if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
				t.Errorf("sweep %d without a lock removed %d, want 0", i, got)
			}
		}
		wantPresent(t, wd, "task_a/f.txt", "task_a.kept")
		if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
			t.Errorf("%d WARNs after the sweeps, want 1:\n%s", n, logs.String())
		}
	}
	injectFlock := func(t *testing.T, f func(fd, how int) error) {
		orig := flockSyscall
		flockSyscall = f
		t.Cleanup(func() { flockSyscall = orig })
	}

	t.Run("owner lock file cannot be opened", func(t *testing.T) {
		wd := t.TempDir()
		mkExpiredTestDir(t, wd)
		if err := os.Mkdir(filepath.Join(wd, workDirOwnerLockName), 0o755); err != nil {
			t.Fatal(err)
		}
		check(t, wd)
	})

	t.Run("unwritable workdir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write to any directory")
		}
		wd := t.TempDir()
		mkExpiredTestDir(t, wd)
		if err := os.Chmod(wd, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(wd, 0o755) })
		check(t, wd)
	})

	t.Run("flock unsupported", func(t *testing.T) {
		wd := t.TempDir()
		mkExpiredTestDir(t, wd)
		injectFlock(t, func(int, int) error { return syscall.ENOLCK })
		check(t, wd)
	})

	t.Run("sharer lock fails", func(t *testing.T) {
		wd := t.TempDir()
		mkExpiredTestDir(t, wd)
		owner := openTestLockFD(t, wd, workDirOwnerLockName)
		testFlock(t, owner, syscall.LOCK_EX)
		injectFlock(t, func(fd, how int) error {
			if how&syscall.LOCK_SH != 0 {
				return syscall.ENOLCK
			}
			return syscall.Flock(fd, how)
		})
		check(t, wd)
	})
}

// No workdir lock set at all (as after a failed lockWorkDir): the sweep never
// runs.
func TestSweepKeptTaskDirs_NoLock(t *testing.T) {
	wd := t.TempDir()
	mkAttemptDirs(t, wd, "task_a")
	writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-3*time.Hour))
	w := &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 0 {
		t.Errorf("removed = %d without a workdir lock, want 0", got)
	}
	wantPresent(t, wd, "task_a")
}
