// Package cwl provides CWL-specific path handling utilities.
package cwl

import (
	"net/url"
	"strings"
)

// DecodeLocation URL-decodes a CWL file location.
// CWL allows locations to contain URL-encoded characters (e.g., %23 for #).
// This function decodes such characters to their original form.
//
// Examples:
//   - "item %231.txt" → "item #1.txt"
//   - "path/to/file.txt" → "path/to/file.txt" (unchanged)
//   - "file:///path/to/file.txt" → "file:///path/to/file.txt" (unchanged)
func DecodeLocation(loc string) string {
	if loc == "" {
		return loc
	}

	// For file:// URLs, decode the path portion.
	if strings.HasPrefix(loc, "file://") {
		path := loc[7:]
		if decoded, err := url.PathUnescape(path); err == nil {
			return "file://" + decoded
		}
		return loc
	}

	// For bare paths (no URL scheme), decode directly.
	if !strings.Contains(loc, "://") {
		if decoded, err := url.PathUnescape(loc); err == nil {
			return decoded
		}
	}

	// For other URLs (http://, https://), return as-is.
	return loc
}

// DecodePath URL-decodes a file path.
// This is similar to DecodeLocation but operates on plain paths without URL schemes.
func DecodePath(path string) string {
	if path == "" {
		return path
	}
	if decoded, err := url.PathUnescape(path); err == nil {
		return decoded
	}
	return path
}
