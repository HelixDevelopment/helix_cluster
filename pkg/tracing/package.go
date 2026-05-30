// Package tracing provides distributed tracing utilities for Helix Cluster OS.
package tracing

import "context"

// Span represents a trace span.
type Span struct {
	TraceID string
	SpanID  string
	Name    string
}

// StartSpan creates a new Span.
func StartSpan(ctx context.Context, name string) (*Span, context.Context) {
	s := &Span{TraceID: "trace-1", SpanID: "span-1", Name: name}
	return s, ctx
}

// Finish marks the span as finished.
func (s *Span) Finish() {}
