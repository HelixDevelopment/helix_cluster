package tracing

import (
	"context"
	"testing"
)

func TestStartSpan(t *testing.T) {
	span, ctx := StartSpan(context.Background(), "test")
	if span.Name != "test" {
		t.Errorf("expected name 'test', got %s", span.Name)
	}
	if ctx == nil {
		t.Error("expected non-nil context")
	}
	span.Finish()
}
