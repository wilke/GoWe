//go:build !linux && !darwin

package worker

// fileIdentity is unavailable on other platforms; the digest cache is bypassed and every
// token-bearing task re-hashes its image.
func fileIdentity(string) (string, bool) { return "", false }
