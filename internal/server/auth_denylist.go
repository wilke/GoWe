package server

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// AuthDenylist rejects requests from specific usernames or provider token
// IDs after identity has been established, regardless of whether the
// provider token signature was verified. It is a local, immediate-effect
// complement to token expiry — not a replacement for it.
type AuthDenylist struct {
	users    map[string]struct{}
	tokenIDs map[string]struct{}
}

// NewAuthDenylist builds an AuthDenylist from explicit username and token-ID
// lists. Matching is case-sensitive and exact.
func NewAuthDenylist(users, tokenIDs []string) *AuthDenylist {
	d := &AuthDenylist{
		users:    make(map[string]struct{}, len(users)),
		tokenIDs: make(map[string]struct{}, len(tokenIDs)),
	}
	for _, u := range users {
		if u != "" {
			d.users[u] = struct{}{}
		}
	}
	for _, t := range tokenIDs {
		if t != "" {
			d.tokenIDs[t] = struct{}{}
		}
	}
	return d
}

// Denied reports whether username or tokenID (either may be empty) matches
// an entry on the denylist.
func (d *AuthDenylist) Denied(username, tokenID string) bool {
	if d == nil {
		return false
	}
	if username != "" {
		if _, ok := d.users[username]; ok {
			return true
		}
	}
	if tokenID != "" {
		if _, ok := d.tokenIDs[tokenID]; ok {
			return true
		}
	}
	return false
}

// UserCount returns the number of denylisted usernames.
func (d *AuthDenylist) UserCount() int {
	if d == nil {
		return 0
	}
	return len(d.users)
}

// TokenIDCount returns the number of denylisted token IDs.
func (d *AuthDenylist) TokenIDCount() int {
	if d == nil {
		return 0
	}
	return len(d.tokenIDs)
}

// LoadAuthDenylistFile loads username and token-ID entries from a file with
// one entry per line: "user:<username>" or "tokenid:<uuid>". Blank lines
// and lines starting with "#" are ignored. Any other prefix is a startup
// error. The returned slices are suitable for WithAuthDenylist.
func LoadAuthDenylistFile(path string) (users, tokenIDs []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open auth denylist file %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, fmt.Errorf("auth denylist file %q line %d: expected \"user:<name>\" or \"tokenid:<id>\", got %q", path, lineNum, line)
		}
		value = strings.TrimSpace(value)
		switch prefix {
		case "user":
			users = append(users, value)
		case "tokenid":
			tokenIDs = append(tokenIDs, value)
		default:
			return nil, nil, fmt.Errorf("auth denylist file %q line %d: unknown prefix %q (expected \"user\" or \"tokenid\")", path, lineNum, prefix)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read auth denylist file %q: %w", path, err)
	}

	return users, tokenIDs, nil
}
