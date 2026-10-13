//go:build !unix

package worker

// fileIdentity is unavailable off unix; the digest cache is bypassed and every
// token-bearing task re-hashes its image.
func fileIdentity(string) (string, bool) { return "", false }
