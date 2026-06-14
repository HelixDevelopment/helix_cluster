package grpcutil

import (
	"bytes"
	"context"
	"log"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// silenceLogs redirects the package's log output to a discard buffer for the
// duration of a test so the interceptor's log.Printf calls don't pollute the
// test stream. Returns a restore func.
func silenceLogs(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(nil) })
}

// recordingUnaryHandler returns a unary handler plus a pointer to a bool that
// is flipped true IFF the handler is actually invoked. This is the load-bearing
// instrument for fail-open detection: if an interceptor returns an auth error
// the handler MUST NOT run, so *called MUST remain false.
func recordingUnaryHandler(resp interface{}, err error) (grpc.UnaryHandler, *bool) {
	called := new(bool)
	h := func(ctx context.Context, req interface{}) (interface{}, error) {
		*called = true
		return resp, err
	}
	return h, called
}

// recordingStreamHandler is the stream counterpart.
func recordingStreamHandler(err error) (grpc.StreamHandler, *bool) {
	called := new(bool)
	h := func(srv interface{}, ss grpc.ServerStream) error {
		*called = true
		return err
	}
	return h, called
}

func assertUnauthenticated(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil (request admitted)")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v", err)
	}
}

// ============================================================================
// AuthUnaryInterceptor — the REAL fail-CLOSED auth gate (x-api-key == apiKey).
// These are the centerpieces: every rejection path must (a) return
// Unauthenticated AND (b) leave the handler un-invoked.
// ============================================================================

// PROBE (a): MISSING metadata entirely -> rejected, handler NOT called.
func TestAdversarial_AuthUnary_MissingMetadata_RejectedHandlerNotCalled(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler, called := recordingUnaryHandler("LEAKED", nil)

	resp, err := ic(context.Background(), "req",
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)

	assertUnauthenticated(t, err)
	if *called {
		t.Error("FAIL-OPEN: handler was invoked despite missing metadata")
	}
	if resp != nil {
		t.Errorf("expected nil response on rejection, got %v", resp)
	}
}

// PROBE (a'): metadata present but NO x-api-key key at all -> rejected, not called.
func TestAdversarial_AuthUnary_NoAPIKeyHeader_RejectedHandlerNotCalled(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler, called := recordingUnaryHandler("LEAKED", nil)

	// Metadata exists, but carries an unrelated key only.
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("some-other-header", "value"))
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)

	assertUnauthenticated(t, err)
	if *called {
		t.Error("FAIL-OPEN: handler was invoked despite no x-api-key header")
	}
}

// PROBE (b): EMPTY token value -> rejected, handler NOT called.
func TestAdversarial_AuthUnary_EmptyToken_RejectedHandlerNotCalled(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler, called := recordingUnaryHandler("LEAKED", nil)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", ""))
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)

	assertUnauthenticated(t, err)
	if *called {
		t.Error("FAIL-OPEN: handler was invoked despite empty x-api-key")
	}
}

// PROBE (b'): WRONG token value -> rejected, handler NOT called.
func TestAdversarial_AuthUnary_WrongToken_RejectedHandlerNotCalled(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler, called := recordingUnaryHandler("LEAKED", nil)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", "not-the-key"))
	_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)

	assertUnauthenticated(t, err)
	if *called {
		t.Error("FAIL-OPEN: handler was invoked despite wrong x-api-key")
	}
}

// PROBE (c): a near-miss / prefix of the real key must NOT be admitted.
// Pins exact-equality (not prefix/substring) matching.
func TestAdversarial_AuthUnary_PrefixOfKey_Rejected(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	for _, tok := range []string{"secret", "secret-key ", " secret-key", "secret-key-extra", "SECRET-KEY"} {
		handler, called := recordingUnaryHandler("LEAKED", nil)
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("x-api-key", tok))
		_, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
		assertUnauthenticated(t, err)
		if *called {
			t.Errorf("FAIL-OPEN: handler invoked for non-exact key %q", tok)
		}
	}
}

// PROBE (d): VALID token -> handler IS called, response + error propagated.
// This is the always-reject mutation catcher.
func TestAdversarial_AuthUnary_ValidToken_HandlerCalledAndPropagates(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")
	handler, called := recordingUnaryHandler("real-response", nil)

	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", "secret-key"))
	resp, err := ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("valid key must be admitted, got error: %v", err)
	}
	if !*called {
		t.Fatal("ALWAYS-REJECT: handler was NOT invoked for a valid key")
	}
	if resp != "real-response" {
		t.Errorf("handler response not propagated: got %v", resp)
	}

	// And the handler's error must propagate unchanged when it errors.
	wantErr := status.Error(codes.FailedPrecondition, "downstream")
	h2, called2 := recordingUnaryHandler(nil, wantErr)
	_, err = ic(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, h2)
	if err != wantErr {
		t.Errorf("handler error not propagated unchanged: got %v", err)
	}
	if !*called2 {
		t.Error("handler should have run for valid key")
	}
}

// PROBE: multiple x-api-key values — metadata.Get returns them in order; only
// the FIRST is checked. A correct first value is admitted; a wrong first value
// (even with a correct second) is rejected. Pins the tokens[0] contract.
func TestAdversarial_AuthUnary_MultipleValues_FirstWins(t *testing.T) {
	ic := AuthUnaryInterceptor("secret-key")

	// Correct first -> admitted.
	hOK, calledOK := recordingUnaryHandler("ok", nil)
	mdOK := metadata.MD{"x-api-key": []string{"secret-key", "junk"}}
	_, err := ic(metadata.NewIncomingContext(context.Background(), mdOK), "req",
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"}, hOK)
	if err != nil || !*calledOK {
		t.Errorf("correct first value must be admitted, err=%v called=%v", err, *calledOK)
	}

	// Wrong first, correct second -> rejected (only first is consulted).
	hBad, calledBad := recordingUnaryHandler("LEAKED", nil)
	mdBad := metadata.MD{"x-api-key": []string{"wrong", "secret-key"}}
	_, err = ic(metadata.NewIncomingContext(context.Background(), mdBad), "req",
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"}, hBad)
	assertUnauthenticated(t, err)
	if *calledBad {
		t.Error("FAIL-OPEN: handler invoked when only the SECOND value matched")
	}
}

// PROBE: empty configured apiKey is an explicit, documented bypass — handler
// runs with no metadata. Pins that the bypass is real (and intentional).
func TestAdversarial_AuthUnary_EmptyConfiguredKey_BypassesAuth(t *testing.T) {
	ic := AuthUnaryInterceptor("")
	handler, called := recordingUnaryHandler("ok", nil)
	resp, err := ic(context.Background(), "req",
		&grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("empty configured key must bypass, got %v", err)
	}
	if !*called || resp != "ok" {
		t.Errorf("empty configured key should pass through to handler")
	}
}

// ============================================================================
// UnaryInterceptor / StreamInterceptor — the demo logging gate.
// CONTRACT (per code comment "we accept any non-empty bearer token" and the
// existing TestMutation* tests): this gate is FAIL-OPEN by design and the ONLY
// rejection trigger is the literal authorization value "invalid". We pin that
// exact contract so a change that either (1) stops rejecting "invalid" or
// (2) starts rejecting valid traffic is caught. NOTE: this is a weak demo gate,
// NOT the real auth boundary — AuthUnaryInterceptor above is the enforcing gate.
// ============================================================================

// The one rejection the demo gate DOES enforce: authorization == "invalid".
func TestAdversarial_UnaryDemo_InvalidLiteral_RejectedHandlerNotCalled(t *testing.T) {
	silenceLogs(t)
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "invalid"))
	handler, called := recordingUnaryHandler("LEAKED", nil)

	_, err := UnaryInterceptor(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	assertUnauthenticated(t, err)
	if *called {
		t.Error("handler invoked despite authorization=invalid (reject path broken)")
	}
}

func TestAdversarial_StreamDemo_InvalidLiteral_RejectedHandlerNotCalled(t *testing.T) {
	silenceLogs(t)
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "invalid"))
	handler, called := recordingStreamHandler(nil)

	err := StreamInterceptor(nil, &mockServerStream{ctx: ctx},
		&grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	assertUnauthenticated(t, err)
	if *called {
		t.Error("stream handler invoked despite authorization=invalid (reject path broken)")
	}
}

// Documented fail-open contract: anything other than the literal "invalid" is
// admitted by the demo gate. We pin the exact set so accidental hardening or
// loosening is surfaced. Each case asserts the handler RAN (admitted).
func TestAdversarial_UnaryDemo_FailOpenContract_Admitted(t *testing.T) {
	silenceLogs(t)
	cases := []struct {
		name string
		md   metadata.MD // nil => no incoming metadata at all
	}{
		{"no-metadata", nil},
		{"no-authorization-key", metadata.Pairs("other", "x")},
		{"empty-token", metadata.Pairs("authorization", "")},
		{"bearer-prefix", metadata.Pairs("authorization", "Bearer abc")},
		{"raw-token", metadata.Pairs("authorization", "abc")},
		{"wrong-scheme", metadata.Pairs("authorization", "Basic abc")},
		{"near-miss", metadata.Pairs("authorization", "Invalid")}, // case-different from the magic word
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.md != nil {
				ctx = metadata.NewIncomingContext(ctx, tc.md)
			}
			handler, called := recordingUnaryHandler("ok", nil)
			_, err := UnaryInterceptor(ctx, "req",
				&grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
			if err != nil {
				t.Fatalf("demo gate unexpectedly rejected %q: %v", tc.name, err)
			}
			if !*called {
				t.Errorf("demo gate fail-open contract: handler should have run for %q", tc.name)
			}
		})
	}
}

// Stream parity for the demo gate: same fail-open admit set, plus the valid
// (admit) and invalid (reject) anchors, asserting handler-call state.
func TestAdversarial_StreamDemo_FailOpenContract_Admitted(t *testing.T) {
	silenceLogs(t)
	cases := []struct {
		name string
		md   metadata.MD
	}{
		{"no-metadata", nil},
		{"empty-token", metadata.Pairs("authorization", "")},
		{"raw-token", metadata.Pairs("authorization", "abc")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ctx context.Context = context.Background()
			if tc.md != nil {
				ctx = metadata.NewIncomingContext(ctx, tc.md)
			}
			handler, called := recordingStreamHandler(nil)
			err := StreamInterceptor(nil, &mockServerStream{ctx: ctx},
				&grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
			if err != nil {
				t.Fatalf("demo stream gate unexpectedly rejected %q: %v", tc.name, err)
			}
			if !*called {
				t.Errorf("demo stream gate fail-open contract: handler should have run for %q", tc.name)
			}
		})
	}
}

// ============================================================================
// ChainUnaryInterceptors composed with the REAL gate: a rejecting auth
// interceptor in the chain MUST short-circuit — downstream interceptors and the
// final handler must NOT run. This proves the gate's rejection is honored when
// wired the way production wires it.
// ============================================================================

func TestAdversarial_Chain_AuthRejectShortCircuits(t *testing.T) {
	auth := AuthUnaryInterceptor("secret-key")

	downstreamRan := false
	downstream := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (interface{}, error) {
		downstreamRan = true
		return h(ctx, req)
	}
	handler, called := recordingUnaryHandler("LEAKED", nil)

	chained := ChainUnaryInterceptors(auth, downstream)
	// Wrong key -> auth must reject before downstream/handler.
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-api-key", "wrong"))
	_, err := chained(ctx, "req", &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)

	assertUnauthenticated(t, err)
	if downstreamRan {
		t.Error("FAIL-OPEN: downstream interceptor ran after auth rejection")
	}
	if *called {
		t.Error("FAIL-OPEN: final handler ran after auth rejection")
	}
}
