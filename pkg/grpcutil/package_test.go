package grpcutil

import (
	"context"
	"testing"
)

func TestUnaryInterceptor(t *testing.T) {
	// Minimal smoke test: interceptor exists and compiles.
	if UnaryInterceptor == nil {
		t.Error("expected non-nil interceptor")
	}
}

func TestStreamInterceptor(t *testing.T) {
	if StreamInterceptor == nil {
		t.Error("expected non-nil interceptor")
	}
}
