package log

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelInfo).WithOutput(&buf)

	l.Debug("should not appear")
	if buf.Len() > 0 {
		t.Error("debug message appeared at info level")
	}

	l.Info("visible msg")
	if !strings.Contains(buf.String(), "visible msg") {
		t.Error("info message should appear at info level")
	}

	buf.Reset()
	l.Error("err msg")
	if !strings.Contains(buf.String(), "err msg") {
		t.Error("error message should appear at info level")
	}
}

func TestLogFormatting(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug).WithOutput(&buf)

	l.Info("count=%d", 42)
	output := buf.String()

	if !strings.Contains(output, "[hermai]") {
		t.Errorf("expected prefix [hermai], got %q", output)
	}
	if !strings.Contains(output, "INFO") {
		t.Errorf("expected INFO level, got %q", output)
	}
	if !strings.Contains(output, "count=42") {
		t.Errorf("expected formatted arg, got %q", output)
	}
}

func TestNop(t *testing.T) {
	l := Nop()
	l.Error("should not panic")
	l.Info("should not panic")
	l.Debug("should not panic")
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"silent", LevelSilent},
		{"quiet", LevelSilent},
		{"error", LevelError},
		{"info", LevelInfo},
		{"debug", LevelDebug},
		{"verbose", LevelDebug},
		{"unknown", LevelInfo},
	}

	for _, tt := range tests {
		got := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSilentLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelSilent).WithOutput(&buf)

	l.Error("nope")
	l.Info("nope")
	l.Debug("nope")

	if buf.Len() > 0 {
		t.Error("silent logger should produce no output")
	}
}
