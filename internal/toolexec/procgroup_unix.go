//go:build unix

package toolexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// setupProcessGroupCancel makes cmd run in its own process group and, when
// cmd's context is cancelled, signals the whole group instead of only the
// direct child: SIGTERM first, then SIGKILL after grace unless release has
// been called. This reaches shell pipelines, tool subprocesses and container
// runtime trees that exec.CommandContext's default (kill the direct child
// only) would leave behind as orphans.
//
// Must be called before cmd.Start. The caller must call release once the
// command has finished (typically deferred): it stops the pending escalation
// and, if a cancel was sent, SIGKILLs whatever is left of the group, so a
// member that ignored SIGTERM cannot outlive the command. release is safe to
// call more than once.
//
// cmd.WaitDelay is deliberately left unset: it would also apply to commands
// that exit normally and fail those that leave a background process holding
// stdout/stderr. A process that left the group (setsid) and holds those pipes
// can therefore still block Wait, as before.
func setupProcessGroupCancel(cmd *exec.Cmd, grace time.Duration) (release func()) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Pgid 0 with Setpgid makes the child the leader of a new group whose ID
	// equals its PID, which is what signalProcessGroup relies on.
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0

	done := make(chan struct{})
	var once sync.Once
	var cancelled atomic.Bool
	release = func() {
		once.Do(func() {
			close(done)
			if cancelled.Load() && cmd.Process != nil {
				_ = signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
			}
		})
	}

	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		cancelled.Store(true)
		if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
			return err
		}
		go func() {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				_ = signalProcessGroup(pid, syscall.SIGKILL)
			}
		}()
		return nil
	}

	return release
}

// signalProcessGroup sends sig to every process in the group led by pid.
// A group that no longer exists (ESRCH) is reported as os.ErrProcessDone so
// exec.Cmd.Wait keeps returning the command's own exit status.
func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		// kill(0, ...) would signal our own group and kill(1, ...) init;
		// never let a bogus PID turn into either.
		return fmt.Errorf("signal process group: invalid pid %d", pid)
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("signal process group %d with %v: %w", pid, sig, err)
	}
	return nil
}
