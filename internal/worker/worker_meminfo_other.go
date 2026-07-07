//go:build !linux

package worker

// sysTotalMemMB returns 0 on platforms without a supported total-memory probe;
// the worker then leaves MaxMemMB unset (no limit). Workers run on Linux in
// production — this stub exists so the module cross-compiles (e.g. for the
// macOS release binaries GoReleaser builds).
func sysTotalMemMB() int64 { return 0 }
