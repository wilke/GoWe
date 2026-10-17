package scheduler

import (
	"strings"
	"unicode/utf8"
)

// maxSubmissionStderr bounds the stderr kept in a submission's error context.
const maxSubmissionStderr = 1000

// maxSnippetHead bounds the leading part of a truncated snippet.
const maxSnippetHead = 200

// snippetMarker separates head and tail of a truncated snippet.
const snippetMarker = "\n...(truncated)...\n"

// stderrSnippet shortens s to about max bytes for a submission error. A
// failing tool usually prints warnings first and the real error last (a
// Python traceback ends with the exception), so a plain prefix hides the
// cause. The snippet keeps the first line (the executor's own message,
// capped at maxSnippetHead bytes) and fills the rest of the budget from the
// end of s, starting at a line boundary where one is close. Cuts never split
// a UTF-8 sequence.
func stderrSnippet(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	head := s
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimRight(head, "\r")
	if headMax := min(maxSnippetHead, limit/2); len(head) > headMax {
		head = head[:runeStart(head, headMax)]
	}

	// len(s) > limit, so the tail starts after the head.
	start := runeStart(s, len(s)-(limit-len(head)))
	tail := s[start:]
	// Start the tail on a whole line if a line break is near its start.
	if s[start-1] != '\n' {
		if i := strings.IndexByte(tail, '\n'); i >= 0 && i < maxSnippetHead && i+1 < len(tail) {
			tail = tail[i+1:]
		}
	}
	return head + snippetMarker + tail
}

// runeStart moves i forward to the start of the next UTF-8 sequence, so
// s[i:] and s[:i] are both valid when s is.
func runeStart(s string, i int) int {
	if i <= 0 {
		return 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}
