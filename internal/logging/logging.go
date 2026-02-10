package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a configured slog.Logger.
//
// level: slog level (DEBUG, INFO, WARN, ERROR)
// format: "text" (human-readable) or "json" (structured)
//
// Output goes to stderr by default (stdout is reserved for program output).
func NewLogger(level slog.Level, format string) *slog.Logger {
	return NewLoggerWithWriter(level, format, os.Stderr)
}

// NewLoggerWithWriter creates a logger writing to the given writer.
func NewLoggerWithWriter(level slog.Level, format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}

// ParseLevel converts a string log level to slog.Level.
// Returns slog.LevelInfo for unrecognized values.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
