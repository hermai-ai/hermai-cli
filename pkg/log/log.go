package log

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Level controls which messages are emitted.
type Level int

const (
	LevelSilent Level = iota
	LevelError
	LevelInfo
	LevelDebug
)

// Logger is a minimal structured logger that writes to stderr.
// Zero-dependency, safe for concurrent use.
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	level  Level
	prefix string
}

// New creates a logger at the given level, writing to stderr.
func New(level Level) *Logger {
	return &Logger{
		out:    os.Stderr,
		level:  level,
		prefix: "hermai",
	}
}

// WithOutput returns a new logger writing to a different writer.
func (l *Logger) WithOutput(w io.Writer) *Logger {
	return &Logger{
		out:    w,
		level:  l.level,
		prefix: l.prefix,
	}
}

// Level returns the current log level.
func (l *Logger) Level() Level {
	return l.level
}

// Error logs at error level.
func (l *Logger) Error(msg string, args ...any) {
	if l.level >= LevelError {
		l.write("ERROR", msg, args...)
	}
}

// Info logs at info level.
func (l *Logger) Info(msg string, args ...any) {
	if l.level >= LevelInfo {
		l.write("INFO", msg, args...)
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string, args ...any) {
	if l.level >= LevelDebug {
		l.write("DEBUG", msg, args...)
	}
}

func (l *Logger) write(level, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	fmt.Fprintf(l.out, "[%s] %s: %s\n", l.prefix, level, msg)
}

// ParseLevel converts a string to a Level. Returns LevelInfo for unknown values.
func ParseLevel(s string) Level {
	switch s {
	case "silent", "quiet", "none":
		return LevelSilent
	case "error":
		return LevelError
	case "info":
		return LevelInfo
	case "debug", "verbose":
		return LevelDebug
	default:
		return LevelInfo
	}
}

// Nop returns a silent logger that discards all output.
func Nop() *Logger {
	return New(LevelSilent)
}
