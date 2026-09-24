// Package wslocation converts a user-entered path or URI into a CWL
// File/Directory "location" value, per GoWe issue #273. It is the one rule
// shared by the CLI's --workspace-upload path (internal/cli/submit.go,
// uploadFileToWorkspace) and the web UI's submission form
// (internal/ui/submission_inputs.go): a value that already carries a URI
// scheme is left untouched, a BV-BRC workspace-form path becomes a ws://
// URI, and anything else (a local filesystem path) is returned unchanged.
package wslocation

import (
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// Resolve converts raw into a CWL location value.
//
//   - A value that already carries a recognized URI scheme (ws://,
//     http://, https://, file://, s3://, shock://, ...) is returned
//     unchanged — see cwl.IsURI.
//   - A workspace-form path — a leading "/" followed by a non-empty
//     "<user>@<domain>" first path segment, e.g.
//     "/awilke@bvbrc/home/data/contigs.fasta" (the form the UI's file/
//     folder picker and placeholder text use, and the form BV-BRC
//     workspace paths for uploaded files take: "/<user>/home/...") —
//     becomes "ws://" + raw.
//   - Anything else (a bare local filesystem path, relative or absolute,
//     or the empty string) is returned unchanged.
func Resolve(raw string) string {
	if raw == "" || cwl.IsURI(raw) {
		return raw
	}
	if IsWorkspaceForm(raw) {
		return "ws://" + raw
	}
	return raw
}

// IsWorkspaceForm reports whether s looks like a BV-BRC workspace path: a
// leading "/" followed by a non-empty "<user>@<domain>" first segment
// (content required on both sides of the "@"), e.g.
// "/awilke@bvbrc/home/data".
func IsWorkspaceForm(s string) bool {
	if !strings.HasPrefix(s, "/") {
		return false
	}
	rest := s[1:]
	seg := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg = rest[:i]
	}
	at := strings.IndexByte(seg, '@')
	return at > 0 && at < len(seg)-1
}
