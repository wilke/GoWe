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
// stdout/stderr. Two cases can therefore still block Wait, as before: a
// process that left the group (setsid) and holds those pipes, and a group
// member that holds them after the direct child has already exited (os/exec
// stops watching the context once the child is reaped, so Cancel never runs).
//
// onKill (optional; the batch call sites pass none) is called right before a
// kill signal is sent by the escalation: the SIGKILL to the group after grace,
// or the fallback kill of the direct child when the group could not be
// signalled. The SIGKILL release sends to what is left of a group after the
// command has finished does not call it. Service handles use it to report
// whether SIGKILL was needed.
func setupProcessGroupCancel(cmd *exec.Cmd, grace time.Duration, onKill ...func()) (release func()) {
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

	killing := func() {
		for _, f := range onKill {
			if f != nil {
				f()
			}
		}
	}

	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		cancelled.Store(true)
		if err := signalProcessGroup(pid, syscall.SIGTERM); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				return err
			}
			// The group could not be signalled (e.g. EPERM): fall back to
			// killing the direct child so Wait cannot block on it.
			killing()
			return cmd.Process.Kill()
		}
		go func() {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				killing()
				_ = signalProcessGroup(pid, syscall.SIGKILL)
			}
		}()
		return nil
	}

	return release
}

// killServiceProcessGroup sends SIGKILL to the process group led by pid,
// ignoring errors (a group that no longer exists included). Service path
// only: called once a service's leader has exited and been reaped, so
// helpers it left in its group cannot keep the port or GPU memory.
func killServiceProcessGroup(pid int) {
	_ = signalProcessGroup(pid, syscall.SIGKILL)
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
