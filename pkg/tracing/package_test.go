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

// --- Mutation Tests ---

func TestMutationSpanTamperFinish(t *testing.T) {
	span, _ := StartSpan(context.Background(), "test")
	span.Finish()

	// Mutation: call Finish again should not panic and remain finished
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
