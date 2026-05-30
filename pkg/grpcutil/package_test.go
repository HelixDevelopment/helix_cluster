package grpcutil

import (
	"context"
	"testing"

	"google.golang.org/grpc"
)

type mockServerStream struct {
	grpc.ServerStream
}

func TestUnaryInterceptor(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}
	_, err := UnaryInterceptor(context.Background(), "req", nil, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected handler to be called")
	}
}

func TestStreamInterceptor(t *testing.T) {
	called := false
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		called = true
		return nil
	}
	err := StreamInterceptor(nil, &mockServerStream{}, nil, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected handler to be called")
	}
}
