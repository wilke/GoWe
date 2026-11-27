//go:build !unix

package toolexec

import (
	"os/exec"
	"time"
)

// setupProcessGroupCancel is a no-op on non-unix platforms: cmd keeps
// exec.CommandContext's default cancellation (kill the direct child only).
// onKill is never called.
func setupProcessGroupCancel(cmd *exec.Cmd, grace time.Duration, onKill ...func()) (release func()) {
	return func() {}
}

// killServiceProcessGroup is a no-op on non-unix platforms (services run on
// Linux only).
func killServiceProcessGroup(pid int) {}
