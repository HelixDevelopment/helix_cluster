// Package tracing provides distributed tracing utilities for Helix Cluster OS.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey int

const traceContextKey contextKey = iota

// Span represents a trace span.
type Span struct {
	TraceID   string
	SpanID    string
	ParentID  string
	Name      string
	StartTime time.Time
	mu        sync.RWMutex
	finished  bool
}

// StartSpan creates a new Span. If the context contains a parent Span, the new
// span inherits the TraceID and sets ParentID accordingly.
func StartSpan(ctx context.Context, name string) (*Span, context.Context) {
	var parent *Span
	if v := ctx.Value(traceContextKey); v != nil {
		parent = v.(*Span)
	}

	s := &Span{
		TraceID:   newTraceID(),
		SpanID:    newSpanID(),
		Name:      name,
		StartTime: time.Now(),
	}
	if parent != nil {
		s.TraceID = parent.TraceID
		s.ParentID = parent.SpanID
	}
	return s, context.WithValue(ctx, traceContextKey, s)
}

// SpanFromContext returns the Span stored in the context, or nil.
func SpanFromContext(ctx context.Context) *Span {
	if v := ctx.Value(traceContextKey); v != nil {
		return v.(*Span)
	}
	return nil
}

// Finish marks the span as finished.
func (s *Span) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
}

// IsFinished reports whether the span has been finished.
func (s *Span) IsFinished() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.finished
}

// Duration returns the elapsed time since the span started. It returns zero if
// the span has not been started.
func (s *Span) Duration() time.Duration {
	return time.Since(s.StartTime)
}

// PropagationHeaders returns key-value pairs suitable for HTTP header propagation.
func (s *Span) PropagationHeaders() map[string]string {
	return map[string]string{
		"X-Trace-ID": s.TraceID,
		"X-Span-ID":  s.SpanID,
	}
}

// StartSpanFromHeaders creates a child span from propagated HTTP headers.
func StartSpanFromHeaders(ctx context.Context, name string, headers map[string]string) (*Span, context.Context) {
	traceID := headers["X-Trace-ID"]
	parentSpanID := headers["X-Span-ID"]

	s := &Span{
		TraceID:   traceID,
		SpanID:    newSpanID(),
		ParentID:  parentSpanID,
		Name:      name,
		StartTime: time.Now(),
	}
	if s.TraceID == "" {
		s.TraceID = newTraceID()
	}
	return s, context.WithValue(ctx, traceContextKey, s)
}

var randReader = rand.Reader

func newTraceID() string {
	return mustUUID()
}

func newSpanID() string {
	var b [8]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		panic(fmt.Sprintf("tracing: failed to read random bytes: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func mustUUID() string {
	var b [16]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		panic(fmt.Sprintf("tracing: failed to read random bytes: %v", err))
	}
	// UUID v4 variant
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
