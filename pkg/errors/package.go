// Package errors provides structured error handling for Helix Cluster OS.
package errors

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
)

// Code is a typed error code enum.
type Code string

const (
	E_UNKNOWN      Code = "E_UNKNOWN"
	E_NOT_FOUND    Code = "E_NOT_FOUND"
	E_INVALID      Code = "E_INVALID"
	E_TIMEOUT      Code = "E_TIMEOUT"
	E_UNAVAILABLE  Code = "E_UNAVAILABLE"
	E_INTERNAL     Code = "E_INTERNAL"
	E_UNAUTHORIZED Code = "E_UNAUTHORIZED"
	E_CONFLICT     Code = "E_CONFLICT"
)

// Error is a structured error with code, fields, and stack trace.
type Error struct {
	Code    Code
	Message string
	Cause   error
	Fields  map[string]interface{}
	Stack   []Frame

	mu sync.RWMutex
}

// Frame represents a single stack frame.
type Frame struct {
	Function string
	File     string
	Line     int
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[%s] %s", e.Code, e.Message))
	// Read Fields under the RLock: WithField/WithFields mutate the map under the
	// write lock, so an unsynchronized read here is a data race and can crash
	// with "concurrent map iteration and map write". Snapshot under the lock.
	e.mu.RLock()
	if len(e.Fields) > 0 {
		b.WriteString(fmt.Sprintf(" | fields=%v", e.Fields))
	}
	e.mu.RUnlock()
	if e.Cause != nil {
		b.WriteString(fmt.Sprintf(": %v", e.Cause))
	}
	return b.String()
}

// Unwrap returns the cause for errors.Is/As compatibility.
func (e *Error) Unwrap() error {
	return e.Cause
}

// IsCode returns true if the error (or any cause) has the given code.
func IsCode(err error, code Code) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*Error); ok {
		if e.Code == code {
			return true
		}
	}
	if unw, ok := err.(interface{ Unwrap() error }); ok {
		return IsCode(unw.Unwrap(), code)
	}
	return false
}

// GetFields returns a merged view of all structured fields from this error
// and all causes.
func GetFields(err error) map[string]interface{} {
	result := make(map[string]interface{})
	collectFields(err, result)
	return result
}

func collectFields(err error, out map[string]interface{}) {
	if err == nil {
		return
	}
	if unw, ok := err.(interface{ Unwrap() error }); ok {
		collectFields(unw.Unwrap(), out)
	}
	if e, ok := err.(*Error); ok {
		e.mu.RLock()
		for k, v := range e.Fields {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
		e.mu.RUnlock()
	}
}

// New creates a new structured error with a stack trace.
func New(code Code, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Fields:  make(map[string]interface{}),
		Stack:   captureStack(1),
	}
}

// Wrap wraps an error with a code and message, preserving the cause.
func Wrap(err error, code Code, message string) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:    code,
		Message: message,
		Cause:   err,
		Fields:  make(map[string]interface{}),
		Stack:   captureStack(1),
	}
}

// WithField attaches a structured field to the error.
func (e *Error) WithField(key string, value interface{}) *Error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Fields[key] = value
	return e
}

// WithFields attaches multiple structured fields.
func (e *Error) WithFields(fields map[string]interface{}) *Error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range fields {
		e.Fields[k] = v
	}
	return e
}

// StackTrace returns a formatted string representation of the stack.
func (e *Error) StackTrace() string {
	if e == nil || len(e.Stack) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range e.Stack {
		b.WriteString(fmt.Sprintf("%s\n    %s:%d\n", f.Function, f.File, f.Line))
	}
	return b.String()
}

func captureStack(skip int) []Frame {
	var frames []Frame
	for i := skip + 1; ; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		fn := runtime.FuncForPC(pc)
		var fnName string
		if fn != nil {
			fnName = fn.Name()
		}
		frames = append(frames, Frame{
			Function: fnName,
			File:     file,
			Line:     line,
		})
		if len(frames) >= 32 {
			break
		}
	}
	return frames
}
