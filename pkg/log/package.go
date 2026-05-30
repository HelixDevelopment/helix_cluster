// Package log provides structured logging for Helix Cluster OS.
package log

import "fmt"

// Logger is a simple structured logger.
type Logger struct {
	prefix string
}

// New creates a new Logger.
func New(prefix string) *Logger {
	return &Logger{prefix: prefix}
}

// Info logs an informational message.
func (l *Logger) Info(msg string) {
	fmt.Printf("[%s] INFO: %s\n", l.prefix, msg)
}

// Error logs an error message.
func (l *Logger) Error(msg string) {
	fmt.Printf("[%s] ERROR: %s\n", l.prefix, msg)
}
