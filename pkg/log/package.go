// Package log provides structured logging for Helix Cluster OS.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// Level represents a log level.
type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	case FatalLevel:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

func (l Level) toSlogLevel() slog.Level {
	switch l {
	case DebugLevel:
		return slog.LevelDebug
	case InfoLevel:
		return slog.LevelInfo
	case WarnLevel:
		return slog.LevelWarn
	case ErrorLevel:
		return slog.LevelError
	case FatalLevel:
		return slog.LevelError + 4
	default:
		return slog.LevelInfo
	}
}

// Logger is a structured logger wrapping slog.
type Logger struct {
	inner  *slog.Logger
	level  Level
	output io.Writer
	prefix string
	mu     sync.RWMutex
}

// contextKey is the type for context keys used by this package.
type contextKey string

const loggerFieldsKey contextKey = "log_fields"

// New creates a new Logger with the given prefix and output.
func New(prefix string, output io.Writer) *Logger {
	if output == nil {
		output = os.Stdout
	}
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	inner := slog.New(handler).With("logger", prefix)
	return &Logger{
		inner:  inner,
		level:  InfoLevel,
		output: output,
		prefix: prefix,
	}
}

// Default creates a Logger writing to stdout with no prefix.
func Default() *Logger {
	return New("helix", os.Stdout)
}

// SetLevel updates the minimum log level.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
	handler := slog.NewJSONHandler(l.output, &slog.HandlerOptions{
		Level: level.toSlogLevel(),
	})
	l.inner = slog.New(handler).With("logger", l.prefix)
}

// Level returns the current minimum log level.
func (l *Logger) Level() Level {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// WithContext returns a new Logger that includes fields from the context.
func (l *Logger) WithContext(ctx context.Context) *Logger {
	if ctx == nil {
		return l
	}
	fields, _ := ctx.Value(loggerFieldsKey).(map[string]interface{})
	if len(fields) == 0 {
		return l
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &Logger{
		inner:  l.inner.With(args...),
		level:  l.level,
		output: l.output,
		prefix: l.prefix,
	}
}

// Debug logs a debug message.
func (l *Logger) Debug(msg string, keysAndValues ...interface{}) {
	l.log(DebugLevel, msg, keysAndValues...)
}

// Info logs an informational message.
func (l *Logger) Info(msg string, keysAndValues ...interface{}) {
	l.log(InfoLevel, msg, keysAndValues...)
}

// Warn logs a warning message.
func (l *Logger) Warn(msg string, keysAndValues ...interface{}) {
	l.log(WarnLevel, msg, keysAndValues...)
}

// Error logs an error message.
func (l *Logger) Error(msg string, keysAndValues ...interface{}) {
	l.log(ErrorLevel, msg, keysAndValues...)
}

// Fatal logs a fatal message and exits the process.
func (l *Logger) Fatal(msg string, keysAndValues ...interface{}) {
	l.log(FatalLevel, msg, keysAndValues...)
	os.Exit(1)
}

func (l *Logger) log(level Level, msg string, keysAndValues ...interface{}) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if level < l.level {
		return
	}
	switch level {
	case DebugLevel:
		l.inner.Debug(msg, keysAndValues...)
	case InfoLevel:
		l.inner.Info(msg, keysAndValues...)
	case WarnLevel:
		l.inner.Warn(msg, keysAndValues...)
	case ErrorLevel, FatalLevel:
		l.inner.Error(msg, keysAndValues...)
	}
}

// WithFields attaches persistent fields to the logger and returns a new one.
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	if len(fields) == 0 {
		return l
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &Logger{
		inner:  l.inner.With(args...),
		level:  l.level,
		output: l.output,
		prefix: l.prefix,
	}
}

// WithField is a convenience for a single field.
func (l *Logger) WithField(key string, value interface{}) *Logger {
	if l == nil {
		return nil
	}
	return l.WithFields(map[string]interface{}{key: value})
}

// ContextWithFields returns a context enriched with log fields.
func ContextWithFields(ctx context.Context, fields map[string]interface{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	existing, _ := ctx.Value(loggerFieldsKey).(map[string]interface{})
	merged := make(map[string]interface{}, len(existing)+len(fields))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return context.WithValue(ctx, loggerFieldsKey, merged)
}

// ParseLevel parses a level string.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "DEBUG":
		return DebugLevel, nil
	case "INFO":
		return InfoLevel, nil
	case "WARN":
		return WarnLevel, nil
	case "ERROR":
		return ErrorLevel, nil
	case "FATAL":
		return FatalLevel, nil
	default:
		return InfoLevel, fmt.Errorf("unknown log level: %s", s)
	}
}
