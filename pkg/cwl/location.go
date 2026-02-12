package cwl

import "strings"

// Supported URI schemes for CWL Directory/File location.
const (
	SchemeWorkspace = "ws"
	SchemeFile      = "file"
	SchemeShock     = "shock"
	SchemeHTTPS     = "https"
	SchemeHTTP      = "http"
)

// ParseLocationScheme extracts the scheme from a location URI.
// Returns ("ws", "/user@bvbrc/home/") for "ws:///user@bvbrc/home/".
// Returns ("", raw) for bare strings with no scheme.
func ParseLocationScheme(location string) (scheme, path string) {
	if i := strings.Index(location, "://"); i > 0 {
		scheme = strings.ToLower(location[:i])
		path = location[i+3:]
		// Normalize: ws:///path and file:///path → /path
		if scheme == SchemeWorkspace || scheme == SchemeFile {
			path = "/" + strings.TrimLeft(path, "/")
		}
		return scheme, path
	}
	return "", location
}

// BuildLocation constructs a scheme://path URI.
func BuildLocation(scheme, path string) string {
	switch scheme {
	case SchemeWorkspace:
		return "ws://" + path
	case SchemeFile:
		return "file://" + path
	default:
		return scheme + "://" + path
	}
}

// InferScheme guesses the URI scheme for a bare string based on executor type.
func InferScheme(executorType string) string {
	switch executorType {
	case "bvbrc":
		return SchemeWorkspace
	case "container", "local":
		return SchemeFile
	default:
		return SchemeFile
	}
}
