package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerWithWriter_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLoggerWithWriter(slog.LevelInfo, "text", &buf)

	logger.Info("test message", "key", "value")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("expected 'test message' in output, got: %s", output)
	}
	if !strings.Contains(output, "key=value") {
		t.Errorf("expected 'key=value' in output, got: %s", output)
	}
}

func TestNewLoggerWithWriter_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLoggerWithWriter(slog.LevelInfo, "json", &buf)

	logger.Info("test message", "key", "value")

	output := buf.String()
	if !strings.Contains(output, `"msg":"test message"`) {
		t.Errorf("expected JSON msg field in output, got: %s", output)
	}
	if !strings.Contains(output, `"key":"value"`) {
		t.Errorf("expected JSON key field in output, got: %s", output)
	}
}

func TestNewLoggerWithWriter_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLoggerWithWriter(slog.LevelWarn, "text", &buf)

	logger.Info("should not appear")
	logger.Warn("should appear")

	output := buf.String()
	if strings.Contains(output, "should not appear") {
		t.Errorf("INFO message should be filtered at WARN level, got: %s", output)
	}
	if !strings.Contains(output, "should appear") {
		t.Errorf("WARN message should appear at WARN level, got: %s", output)
	}
}

func TestNewLoggerWithWriter_ChildLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLoggerWithWriter(slog.LevelDebug, "text", &buf)
	child := logger.With("component", "scheduler")

	child.Debug("tick", "task_id", "task_abc")

	output := buf.String()
	if !strings.Contains(output, "component=scheduler") {
		t.Errorf("expected component in output, got: %s", output)
	}
	if !strings.Contains(output, "task_id=task_abc") {
		t.Errorf("expected task_id in output, got: %s", output)
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := ParseLevel(tt.input); got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
