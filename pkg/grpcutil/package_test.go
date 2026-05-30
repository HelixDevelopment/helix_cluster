package grpcutil

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func TestUnaryInterceptorCallsHandler(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	called := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}
	_, err := UnaryInterceptor(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected handler to be called")
	}
	output := buf.String()
	if !strings.Contains(output, "unary") {
		t.Error("expected log to contain 'unary'")
	}
}

func TestUnaryInterceptorRejectsInvalidToken(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "invalid"))
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	_, err := UnaryInterceptor(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestStreamInterceptorCallsHandler(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	called := false
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		called = true
		return nil
	}
	err := StreamInterceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected handler to be called")
	}
	output := buf.String()
	if !strings.Contains(output, "stream") {
		t.Error("expected log to contain 'stream'")
	}
}

func TestStreamInterceptorRejectsInvalidToken(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "invalid"))
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return nil
	}
	err := StreamInterceptor(nil, &mockServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthUnaryInterceptorValidKey(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "secret-key"))
	resp, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("expected ok, got %v", resp)
	}
}

func TestAuthUnaryInterceptorInvalidKey(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "wrong-key"))
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for invalid api key")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthUnaryInterceptorMissingMetadata(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	_, err := ic(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
}

func TestAuthUnaryInterceptorEmptyKeyAllowsAll(t *testing.T) {
	ic := AuthUnaryInterceptor("")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	resp, err := ic(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("expected ok, got %v", resp)
	}
}

func TestChainUnaryInterceptors(t *testing.T) {
	order := []string{}
	ic1 := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		order = append(order, "ic1-before")
		resp, err := handler(ctx, req)
		order = append(order, "ic1-after")
		return resp, err
	}
	ic2 := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		order = append(order, "ic2-before")
		resp, err := handler(ctx, req)
		order = append(order, "ic2-after")
		return resp, err
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		order = append(order, "handler")
		return "ok", nil
	}

	chained := ChainUnaryInterceptors(ic1, ic2)
	_, err := chained(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"ic1-before", "ic2-before", "handler", "ic2-after", "ic1-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected order %v, got %v", expected, order)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("expected order[%d]=%s, got %s", i, v, order[i])
		}
	}
}

// --- Mutation Tests ---

func TestMutationUnaryInterceptorBypassAuthWithEmptyToken(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	// Empty authorization header should NOT be rejected (only "invalid" is rejected)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", ""))
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	_, err := UnaryInterceptor(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Error("mutation: empty auth token should not be rejected")
	}
}

func TestMutationStreamInterceptorBypassAuthWithNoMetadata(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	// No metadata should NOT be rejected (only "invalid" is rejected)
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return nil
	}
	err := StreamInterceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	if err != nil {
		t.Error("mutation: missing metadata should not be rejected by default interceptor")
	}
}

func TestMutationAuthUnaryInterceptorCaseSensitive(t *testing.T) {
	ic := AuthUnaryInterceptor("Secret")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "secret"))
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Error("mutation: expected case-sensitive API key comparison to fail")
	}
}
