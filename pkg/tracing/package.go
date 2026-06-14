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
	endTime   time.Time
	tags      map[string]string
	logs      []LogRecord
}

// LogRecord represents a log entry within a span.
type LogRecord struct {
	Timestamp time.Time
	Message   string
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
		tags:      make(map[string]string),
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
	if s.endTime.IsZero() {
		s.endTime = time.Now()
	}
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
	// Once finished, freeze the duration at endTime-StartTime. Returning
	// time.Since(StartTime) for a finished span makes its reported duration keep
	// growing with wall-clock time, corrupting any consumer that reads a finished
	// span's latency (dashboards, metrics, logs). end.Sub(StartTime) uses the
	// monotonic clock captured at Finish.
	s.mu.RLock()
	finished := s.finished
	end := s.endTime
	s.mu.RUnlock()
	if finished && !end.IsZero() {
		return end.Sub(s.StartTime)
	}
	return time.Since(s.StartTime)
}

// SetTag sets a tag on the span.
func (s *Span) SetTag(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tags == nil {
		s.tags = make(map[string]string)
	}
	s.tags[key] = value
}

// GetTag returns a tag value, or empty string if not set.
func (s *Span) GetTag(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tags[key]
}

// Tags returns a copy of all tags.
func (s *Span) Tags() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.tags))
	for k, v := range s.tags {
		out[k] = v
	}
	return out
}

// Log adds a log record to the span.
func (s *Span) Log(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, LogRecord{Timestamp: time.Now(), Message: message})
}

// Logs returns a copy of all log records.
func (s *Span) Logs() []LogRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogRecord, len(s.logs))
	copy(out, s.logs)
	return out
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
		tags:      make(map[string]string),
	}
	if s.TraceID == "" {
		s.TraceID = newTraceID()
	}
	return s, context.WithValue(ctx, traceContextKey, s)
}

// Tracer is the interface for starting and propagating spans.
type Tracer interface {
	StartSpan(ctx context.Context, name string) (*Span, context.Context)
	Inject(span *Span) map[string]string
	Extract(headers map[string]string) (context.Context, *Span)
}

// DefaultTracer implements Tracer with in-memory span creation.
type DefaultTracer struct{}

// NewTracer creates a new DefaultTracer.
func NewTracer() Tracer {
	return &DefaultTracer{}
}

// StartSpan implements Tracer.
func (dt *DefaultTracer) StartSpan(ctx context.Context, name string) (*Span, context.Context) {
	return StartSpan(ctx, name)
}

// Inject serializes a span into propagation headers.
func (dt *DefaultTracer) Inject(span *Span) map[string]string {
	if span == nil {
		return map[string]string{}
	}
	return span.PropagationHeaders()
}

// Extract deserializes a span from propagation headers and returns a new child span.
func (dt *DefaultTracer) Extract(headers map[string]string) (context.Context, *Span) {
	ctx := context.Background()
	span, ctx := StartSpanFromHeaders(ctx, "extracted", headers)
	return ctx, span
}

// NoOpTracer is a tracer that does nothing. Use it when tracing is disabled.
type NoOpTracer struct{}

// NewNoOpTracer creates a new NoOpTracer.
func NewNoOpTracer() Tracer {
	return &NoOpTracer{}
}

// StartSpan returns a minimal no-op span.
func (nt *NoOpTracer) StartSpan(ctx context.Context, name string) (*Span, context.Context) {
	s := &Span{
		TraceID:   "noop",
		SpanID:    "noop",
		Name:      name,
		StartTime: time.Now(),
		tags:      make(map[string]string),
	}
	return s, ctx
}

// Inject returns empty headers.
func (nt *NoOpTracer) Inject(span *Span) map[string]string {
	return map[string]string{}
}

// Extract returns a no-op span.
func (nt *NoOpTracer) Extract(headers map[string]string) (context.Context, *Span) {
	ctx := context.Background()
	s := &Span{
		TraceID:   "noop",
		SpanID:    "noop",
		Name:      "noop",
		StartTime: time.Now(),
		tags:      make(map[string]string),
	}
	return ctx, s
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
