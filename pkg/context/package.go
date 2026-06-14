// Package context provides context utilities for Helix Cluster OS.
package context

import (
	stdctx "context"
	"time"
)

// WithTimeout creates a context with timeout from a parent context.
func WithTimeout(parent stdctx.Context, timeout time.Duration) (stdctx.Context, stdctx.CancelFunc) {
	return stdctx.WithTimeout(parent, timeout)
}

// Detach returns a context that is never cancelled, keeping only values.
func Detach(ctx stdctx.Context) stdctx.Context {
	return detached{Context: ctx}
}

type detached struct {
	stdctx.Context
}

func (detached) Done() <-chan struct{} { return nil }
func (detached) Err() error            { return nil }

// Deadline must also be severed: a detached context is "never cancelled", but
// leaving the parent's Deadline() inherited reports an (often already-past)
// deadline while Done() never fires and Err() is nil — a self-contradictory
// fail-open that mis-serves deadline-aware callers (gRPC/net-http read
// Deadline() to compute the remaining budget). Clear it, like stdlib's
// context.WithoutCancel.
func (detached) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }
