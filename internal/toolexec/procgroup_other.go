//go:build !unix

package toolexec

import (
	"os/exec"
	"time"
)

// setupProcessGroupCancel is a no-op on non-unix platforms: cmd keeps
// exec.CommandContext's default cancellation (kill the direct child only).
func setupProcessGroupCancel(cmd *exec.Cmd, grace time.Duration) (release func()) {
	return func() {}
}
