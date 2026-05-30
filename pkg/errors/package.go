// Package errors provides structured error handling for Helix Cluster OS.
package errors

import "fmt"

// Error is a structured error with code and context.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Wrap wraps an error with a code and message.
func Wrap(err error, code, message string) *Error {
	return &Error{Code: code, Message: message, Cause: err}
}
