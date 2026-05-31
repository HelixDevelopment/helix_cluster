package tracing

import (
	"context"
	"strings"
	"testing"
)

func TestStartSpanCreatesTraceID(t *testing.T) {
	span, ctx := StartSpan(context.Background(), "test")
	if span.Name != "test" {
		t.Errorf("expected name 'test', got %s", span.Name)
	}
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if span.TraceID == "" {
		t.Error("expected non-empty TraceID")
	}
	if span.SpanID == "" {
		t.Error("expected non-empty SpanID")
	}
	if span.ParentID != "" {
		t.Error("expected no ParentID for root span")
	}
}

func TestSpanInheritsTraceID(t *testing.T) {
	parent, ctx := StartSpan(context.Background(), "parent")
	child, _ := StartSpan(ctx, "child")

	if child.TraceID != parent.TraceID {
		t.Errorf("expected child TraceID %s, got %s", parent.TraceID, child.TraceID)
	}
	if child.ParentID != parent.SpanID {
		t.Errorf("expected child ParentID %s, got %s", parent.SpanID, child.ParentID)
	}
}

func TestSpanFromContext(t *testing.T) {
	span, ctx := StartSpan(context.Background(), "test")
	retrieved := SpanFromContext(ctx)
	if retrieved == nil {
		t.Fatal("expected span from context")
	}
	if retrieved.SpanID != span.SpanID {
		t.Error("expected retrieved span to match original")
	}
}

func TestSpanFromContextMissing(t *testing.T) {
	if SpanFromContext(context.Background()) != nil {
		t.Error("expected nil span from empty context")
	}
}

func TestSpanFinish(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	if span.IsFinished() {
		t.Error("expected span not finished initially")
	}
	span.Finish()
	if !span.IsFinished() {
		t.Error("expected span finished after Finish()")
	}
}

func TestPropagationHeaders(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	hdrs := span.PropagationHeaders()
	if hdrs["X-Trace-ID"] != span.TraceID {
		t.Error("expected X-Trace-ID to match span TraceID")
	}
	if hdrs["X-Span-ID"] != span.SpanID {
		t.Error("expected X-Span-ID to match span SpanID")
	}
}

func TestStartSpanFromHeaders(t *testing.T) {
	headers := map[string]string{
		"X-Trace-ID": "abc-123",
		"X-Span-ID":  "parent-span",
	}
	span, ctx := StartSpanFromHeaders(context.Background(), "child", headers)
	if span.TraceID != "abc-123" {
		t.Errorf("expected TraceID abc-123, got %s", span.TraceID)
	}
	if span.ParentID != "parent-span" {
		t.Errorf("expected ParentID parent-span, got %s", span.ParentID)
	}
	if SpanFromContext(ctx) == nil {
		t.Error("expected span in context")
	}
}

func TestStartSpanFromHeadersEmptyGeneratesTraceID(t *testing.T) {
	span, _ := StartSpanFromHeaders(context.Background(), "root", map[string]string{})
	if span.TraceID == "" {
		t.Error("expected generated TraceID when headers empty")
	}
	if span.ParentID != "" {
		t.Error("expected no ParentID when headers empty")
	}
}

func TestTraceIDFormat(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	parts := strings.Split(span.TraceID, "-")
	if len(parts) != 5 {
		t.Errorf("expected UUID format with 5 parts, got %d", len(parts))
	}
}

func TestSpanTags(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	span.SetTag("service", "helix")
	span.SetTag("env", "prod")

	if span.GetTag("service") != "helix" {
		t.Errorf("expected tag service=helix, got %s", span.GetTag("service"))
	}
	if span.GetTag("env") != "prod" {
		t.Errorf("expected tag env=prod, got %s", span.GetTag("env"))
	}
	if span.GetTag("missing") != "" {
		t.Error("expected empty string for missing tag")
	}

	tags := span.Tags()
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}
	if tags["service"] != "helix" {
		t.Error("expected tags copy to contain service=helix")
	}

	// Mutation safety: modifying returned map should not affect span
	tags["service"] = "mutated"
	if span.GetTag("service") != "helix" {
		t.Error("modifying Tags() copy should not affect span")
	}
}

func TestSpanLogs(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	span.Log("event 1")
	span.Log("event 2")

	logs := span.Logs()
	if len(logs) != 2 {
		t.Errorf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Message != "event 1" {
		t.Errorf("expected first log 'event 1', got %s", logs[0].Message)
	}
	if logs[1].Message != "event 2" {
		t.Errorf("expected second log 'event 2', got %s", logs[1].Message)
	}

	// Mutation safety
	logs[0].Message = "mutated"
	logs2 := span.Logs()
	if logs2[0].Message != "event 1" {
		t.Error("modifying Logs() copy should not affect span")
	}
}

func TestDefaultTracer(t *testing.T) {
	tracer := NewTracer()
	span, _ := tracer.StartSpan(context.Background(), "test")
	if span.Name != "test" {
		t.Errorf("expected span name 'test', got %s", span.Name)
	}

	hdrs := tracer.Inject(span)
	if hdrs["X-Trace-ID"] != span.TraceID {
		t.Error("Inject should return correct trace ID")
	}

	ctx2, span2 := tracer.Extract(hdrs)
	if ctx2 == nil {
		t.Error("expected non-nil context from Extract")
	}
	if span2.ParentID != span.SpanID {
		t.Errorf("expected extracted span ParentID %s, got %s", span.SpanID, span2.ParentID)
	}
	if span2.TraceID != span.TraceID {
		t.Errorf("expected extracted span TraceID %s, got %s", span.TraceID, span2.TraceID)
	}
}

func TestDefaultTracerInjectNil(t *testing.T) {
	tracer := NewTracer()
	hdrs := tracer.Inject(nil)
	if len(hdrs) != 0 {
		t.Error("expected empty headers for nil span")
	}
}

func TestNoOpTracer(t *testing.T) {
	tracer := NewNoOpTracer()
	span, _ := tracer.StartSpan(context.Background(), "test")
	if span.TraceID != "noop" {
		t.Errorf("expected noop TraceID, got %s", span.TraceID)
	}
	if span.SpanID != "noop" {
		t.Errorf("expected noop SpanID, got %s", span.SpanID)
	}

	hdrs := tracer.Inject(span)
	if len(hdrs) != 0 {
		t.Error("expected empty headers from NoOpTracer.Inject")
	}

	ctx2, span2 := tracer.Extract(map[string]string{"X-Trace-ID": "abc"})
	if ctx2 == nil {
		t.Error("expected non-nil context from NoOpTracer.Extract")
	}
	if span2.TraceID != "noop" {
		t.Errorf("expected noop TraceID from Extract, got %s", span2.TraceID)
	}
}

// --- Mutation Tests ---

func TestMutationSpanTamperFinish(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	span.Finish()

	span.Finish()
	if !span.IsFinished() {
		t.Error("mutation: expected span to remain finished")
	}
}

func TestMutationContextIsolation(t *testing.T) {
	_, ctxA := StartSpan(context.Background(), "span-a")
	_, ctxB := StartSpan(context.Background(), "span-b")

	spanA := SpanFromContext(ctxA)
	spanB := SpanFromContext(ctxB)

	if spanA.TraceID == spanB.TraceID {
		t.Error("mutation: expected independent trace IDs for independent contexts")
	}
}

func TestMutationPropagatedTraceIDNotOverwritten(t *testing.T) {
	headers := map[string]string{
		"X-Trace-ID": "propagated-trace",
	}
	span, _ := StartSpanFromHeaders(context.Background(), "child", headers)
	if span.TraceID != "propagated-trace" {
		t.Error("mutation: propagated TraceID should not be overwritten")
	}
}
