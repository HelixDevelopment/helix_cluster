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
	// recvCalls / sendCalls record how many times the underlying
	// ServerStream's RecvMsg/SendMsg were actually reached.
	recvCalls int
	sendCalls int
}

func (m *mockServerStream) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func (m *mockServerStream) RecvMsg(interface{}) error {
	m.recvCalls++
	return nil
}

func (m *mockServerStream) SendMsg(interface{}) error {
	m.sendCalls++
	return nil
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
	// Assert dynamic, behavior-dependent fields so the assertion fails if the
	// wrong branch runs or the logged fields are dropped.
	for _, want := range []string{"unary", "/test/Method", "code=OK", "err=<nil>"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected log to contain %q, got %q", want, output)
		}
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
	for _, want := range []string{"stream", "/test/Stream", "code=OK", "err=<nil>"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected log to contain %q, got %q", want, output)
		}
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

// --- LoggingStreamInterceptor counter tests ---

// TestLoggingStreamInterceptorCounts drives a stream through the handler that
// calls RecvMsg N times and SendMsg M times, then asserts BOTH the wrappedStream
// counters and the emitted log line report recv=N send=M. This is the only
// sink-side proof that the counting in wrappedStream is real and not a no-op:
// a no-op implementation (recvCount/sendCount never incremented) would log
// recv=0 send=0 and FAIL this test.
func TestLoggingStreamInterceptorCounts(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	const wantRecv = 3
	const wantSend = 2

	var capturedRecv, capturedSend int
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		for i := 0; i < wantRecv; i++ {
			if err := stream.RecvMsg(nil); err != nil {
				t.Fatalf("RecvMsg: %v", err)
			}
		}
		for i := 0; i < wantSend; i++ {
			if err := stream.SendMsg(nil); err != nil {
				t.Fatalf("SendMsg: %v", err)
			}
		}
		// The stream the handler receives MUST be the *wrappedStream so the
		// interceptor's counters observe these calls.
		ws, ok := stream.(*wrappedStream)
		if !ok {
			t.Fatalf("handler did not receive *wrappedStream, got %T", stream)
		}
		capturedRecv = ws.recvCount
		capturedSend = ws.sendCount
		return nil
	}

	inner := &mockServerStream{}
	err := LoggingStreamInterceptor(nil, inner, &grpc.StreamServerInfo{FullMethod: "/test/LogStream"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Direct sink-side state: the wrappedStream actually incremented.
	if capturedRecv != wantRecv {
		t.Errorf("wrappedStream.recvCount = %d, want %d", capturedRecv, wantRecv)
	}
	if capturedSend != wantSend {
		t.Errorf("wrappedStream.sendCount = %d, want %d", capturedSend, wantSend)
	}
	// The wrappedStream must delegate to the underlying ServerStream.
	if inner.recvCalls != wantRecv {
		t.Errorf("underlying RecvMsg called %d times, want %d", inner.recvCalls, wantRecv)
	}
	if inner.sendCalls != wantSend {
		t.Errorf("underlying SendMsg called %d times, want %d", inner.sendCalls, wantSend)
	}
	// The emitted log line must report the dynamic counts.
	output := buf.String()
	for _, want := range []string{"/test/LogStream", "recv=3", "send=2", "err=<nil>"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected log to contain %q, got %q", want, output)
		}
	}
}

// TestLoggingStreamInterceptorPropagatesError asserts the handler's error is
// surfaced unchanged and that the counts are still logged.
func TestLoggingStreamInterceptorPropagatesError(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	wantErr := status.Error(codes.Internal, "boom")
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		_ = stream.RecvMsg(nil)
		return wantErr
	}
	err := LoggingStreamInterceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{FullMethod: "/test/LogStream"}, handler)
	if err != wantErr {
		t.Fatalf("expected error %v to be propagated unchanged, got %v", wantErr, err)
	}
	if !strings.Contains(buf.String(), "recv=1") {
		t.Errorf("expected log to contain recv=1, got %q", buf.String())
	}
}

// --- Handler-returns-error code extraction tests ---

// TestUnaryInterceptorLogsHandlerErrorCode exercises the status.FromError
// branch: when the handler returns a non-OK status, the interceptor must (a)
// propagate the exact code and (b) log code=<that code>. A broken extraction
// (e.g. always logging code=OK) would FAIL the code= assertion.
func TestUnaryInterceptorLogsHandlerErrorCode(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, status.Error(codes.Internal, "boom")
	}
	_, err := UnaryInterceptor(context.Background(), "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("expected Internal code propagated, got %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "code=Internal") {
		t.Errorf("expected log to contain code=Internal, got %q", output)
	}
	if strings.Contains(output, "code=OK") {
		t.Errorf("log must not report code=OK for an errored handler, got %q", output)
	}
}

// TestStreamInterceptorLogsHandlerErrorCode is the stream counterpart.
func TestStreamInterceptorLogsHandlerErrorCode(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return status.Error(codes.Internal, "boom")
	}
	err := StreamInterceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("expected Internal code propagated, got %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "code=Internal") {
		t.Errorf("expected log to contain code=Internal, got %q", output)
	}
	if strings.Contains(output, "code=OK") {
		t.Errorf("log must not report code=OK for an errored handler, got %q", output)
	}
}

// TestAuthUnaryInterceptorEmptyTokenSlice pins the len(tokens) == 0 branch:
// the x-api-key metadata key is present but its value list is empty.
func TestAuthUnaryInterceptorEmptyTokenSlice(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	// MD with the key present but no values.
	md := metadata.MD{"x-api-key": []string{}}
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err == nil {
		t.Fatal("expected error for empty token slice")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}
