//go:build unix

package toolexec

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestSetupProcessGroupCancel_KillsWholeGroup starts a shell that backgrounds
// a long sleep (a grandchild of the test), cancels the context, and asserts
// that Wait returns promptly and the grandchild is gone too, i.e. the signal
// reached the whole process group rather than only the direct child.
func TestSetupProcessGroupCancel_KillsWholeGroup(t *testing.T) {
	tests := []struct {
		name   string
		script string
		grace  time.Duration
		// minElapsed is a lower bound on cancel-to-Wait time; non-zero when
		// the group ignores SIGTERM and only the SIGKILL escalation can end it.
		minElapsed time.Duration
	}{
		{
			name:   "SIGTERM reaches background grandchild",
			script: `sleep 300 & echo $!; wait`,
			// Full grace: Wait returning within 5s proves SIGTERM did the
			// work, not the SIGKILL escalation.
			grace: processGroupGrace,
		},
		{
			name:       "SIGKILL after grace when group ignores SIGTERM",
			script:     `trap "" TERM; sleep 300 & echo $!; wait`,
			grace:      300 * time.Millisecond,
			minElapsed: 300 * time.Millisecond,
		},
		{
			// The shell dies on SIGTERM and the survivor holds no pipe, so
			// Wait returns at once; only release's final SIGKILL removes it.
			// The PID is printed after the trap is set, so the cancel cannot
			// arrive before SIGTERM is ignored.
			name:   "release kills a SIGTERM-ignoring member left behind",
			script: `(trap "" TERM; sh -c 'echo $PPID'; exec sleep 300 >/dev/null 2>&1) & wait`,
			grace:  processGroupGrace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", tt.script)
			release := setupProcessGroupCancel(cmd, tt.grace)
			defer release()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("StdoutPipe: %v", err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			leader := cmd.Process.Pid

			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				_ = syscall.Kill(-leader, syscall.SIGKILL)
				_ = cmd.Wait()
				t.Fatalf("read grandchild PID: %v", err)
			}
			grandchild, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				_ = syscall.Kill(-leader, syscall.SIGKILL)
				_ = cmd.Wait()
				t.Fatalf("parse grandchild PID %q: %v", line, err)
			}

			start := time.Now()
			cancel()
			waitDone := make(chan error, 1)
			go func() { waitDone <- cmd.Wait() }()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-leader, syscall.SIGKILL)
				<-waitDone
				t.Fatalf("Wait did not return within 5s of cancel")
			}
			elapsed := time.Since(start)
			release()
			if elapsed < tt.minElapsed {
				t.Errorf("Wait returned after %v, want >= %v (group should have ignored SIGTERM until SIGKILL)", elapsed, tt.minElapsed)
			}

			if !waitProcessGone(grandchild, 5*time.Second) {
				_ = syscall.Kill(grandchild, syscall.SIGKILL)
				t.Errorf("grandchild PID %d still alive after cancel", grandchild)
			}
		})
	}
}

func TestSetupProcessGroupCancel(t *testing.T) {
	t.Run("preserves existing SysProcAttr and leaves WaitDelay unset", func(t *testing.T) {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		attr := &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
		release := setupProcessGroupCancel(cmd, time.Second)
		defer release()
		if cmd.SysProcAttr != attr {
			t.Errorf("SysProcAttr was replaced, want the existing struct to be reused")
		}
		if !attr.Setpgid {
			t.Errorf("Setpgid = false, want true")
		}
		if cmd.Cancel == nil {
			t.Errorf("Cancel = nil, want a process-group cancel func")
		}
		if cmd.WaitDelay != 0 {
			t.Errorf("WaitDelay = %v, want 0 (it would fail tools that exit normally)", cmd.WaitDelay)
		}
	})

	t.Run("release twice after normal exit does not panic", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
		release := setupProcessGroupCancel(cmd, processGroupGrace)
		if err := cmd.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		release()
		release()
	})
}

// onKill is called exactly when the escalation sends SIGKILL.
func TestSetupProcessGroupCancel_OnKill(t *testing.T) {
	tests := []struct {
		name   string
		script string
		grace  time.Duration
		want   bool
	}{
		{"exits on SIGTERM", `echo ready; exec sleep 300`, 5 * time.Second, false},
		{"ignores SIGTERM", `trap "" TERM; echo ready; while :; do sleep 1; done`, 200 * time.Millisecond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", tt.script)
			var called atomic.Bool
			release := setupProcessGroupCancel(cmd, tt.grace, func() { called.Store(true) })
			defer release()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
				t.Fatalf("read: %v", err)
			}
			cancel()
			_ = cmd.Wait()
			release()
			if got := called.Load(); got != tt.want {
				t.Errorf("onKill called = %v, want %v", got, tt.want)
			}
		})
	}
}

// waitProcessGone polls until pid no longer exists (kill(pid, 0) == ESRCH)
// or timeout elapses. Reports whether the process is gone.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
