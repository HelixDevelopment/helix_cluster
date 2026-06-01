package tracing_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/HelixDevelopment/helix_cluster/pkg/tracing"
)

const bufSize = 1 << 20 // 1 MiB

// captureServer is a minimal gRPC Health server whose Check method records
// the trace-ID observed in the handler context for test assertions, and
// optionally calls a downstream gRPC service.
type captureServer struct {
	healthgrpc.UnimplementedHealthServer

	mu       sync.Mutex
	observed []string // trace-IDs seen in each call, in order

	// If downstream is set the handler calls it after capturing the trace-ID.
	downstream func(ctx context.Context) error
}

func (s *captureServer) Check(ctx context.Context, _ *healthgrpc.HealthCheckRequest) (*healthgrpc.HealthCheckResponse, error) {
	id, _ := tracing.TraceIDFromContext(ctx)

	s.mu.Lock()
	s.observed = append(s.observed, id)
	s.mu.Unlock()

	if s.downstream != nil {
		if err := s.downstream(ctx); err != nil {
			return nil, err
		}
	}
	return &healthgrpc.HealthCheckResponse{
		Status: healthgrpc.HealthCheckResponse_SERVING,
	}, nil
}

// newBufServer starts an in-process gRPC server backed by a bufconn.Listener.
// It returns the listener (for dialing) and a shutdown function.
func newBufServer(t *testing.T, srv *captureServer) (*bufconn.Listener, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(tracing.UnaryServerInterceptor()),
	)
	healthgrpc.RegisterHealthServer(gs, srv)
	go func() {
		_ = gs.Serve(lis)
	}()
	return lis, func() {
		gs.GracefulStop()
		lis.Close()
	}
}

// dialBufconn opens a gRPC ClientConn to the given bufconn.Listener with the
// trace-propagating client interceptor wired in.
func dialBufconn(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(tracing.UnaryClientInterceptor()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return cc
}

// TestGRPCTraceID_ThreeHopPropagation is the primary anti-bluff test.
//
// Topology: test-client -> gateway -> scheduler -> session
//
// Each server captures the trace-ID it sees in its handler context.  After the
// full round-trip all three captured IDs must equal the originating trace-ID
// that was seeded on the initial context.  Each hop's parent-span-id will
// differ (generated freshly per hop) but the trace-ID is invariant — that is
// what this test verifies.
//
// A broken interceptor (e.g. one that drops or ignores the traceparent) would
// cause hop 2 and/or hop 3 to see a different (freshly generated) trace-ID,
// making the equality assertions below fail.
func TestGRPCTraceID_ThreeHopPropagation(t *testing.T) {
	// Stand up three servers: session (innermost), scheduler, gateway.
	// We build them inside-out so each server can hold a client to the next.

	// --- session server (leaf, no downstream) ---
	sessionSrv := &captureServer{}
	sessionLis, stopSession := newBufServer(t, sessionSrv)
	defer stopSession()

	// --- scheduler server (calls session) ---
	sessionCC := dialBufconn(t, sessionLis)
	defer sessionCC.Close()
	sessionClient := healthgrpc.NewHealthClient(sessionCC)

	schedulerSrv := &captureServer{
		downstream: func(ctx context.Context) error {
			_, err := sessionClient.Check(ctx, &healthgrpc.HealthCheckRequest{})
			return err
		},
	}
	schedulerLis, stopScheduler := newBufServer(t, schedulerSrv)
	defer stopScheduler()

	// --- gateway server (calls scheduler) ---
	schedulerCC := dialBufconn(t, schedulerLis)
	defer schedulerCC.Close()
	schedulerClient := healthgrpc.NewHealthClient(schedulerCC)

	gatewaySrv := &captureServer{
		downstream: func(ctx context.Context) error {
			_, err := schedulerClient.Check(ctx, &healthgrpc.HealthCheckRequest{})
			return err
		},
	}
	gatewayLis, stopGateway := newBufServer(t, gatewaySrv)
	defer stopGateway()

	// --- test client dials gateway ---
	gatewayCC := dialBufconn(t, gatewayLis)
	defer gatewayCC.Close()
	gatewayClient := healthgrpc.NewHealthClient(gatewayCC)

	// Seed a known trace-ID on the outgoing context.
	originTrace := tracing.NewTraceParent(true).TraceID
	ctx := tracing.ContextWithTraceID(context.Background(), originTrace)

	// Drive the full 3-hop chain.
	_, err := gatewayClient.Check(ctx, &healthgrpc.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("gateway.Check: %v", err)
	}

	// Collect observations.
	gatewaySrv.mu.Lock()
	gatewayIDs := append([]string(nil), gatewaySrv.observed...)
	gatewaySrv.mu.Unlock()

	schedulerSrv.mu.Lock()
	schedulerIDs := append([]string(nil), schedulerSrv.observed...)
	schedulerSrv.mu.Unlock()

	sessionSrv.mu.Lock()
	sessionIDs := append([]string(nil), sessionSrv.observed...)
	sessionSrv.mu.Unlock()

	// Each server must have been called exactly once.
	if len(gatewayIDs) != 1 {
		t.Fatalf("gateway: want 1 observation, got %d", len(gatewayIDs))
	}
	if len(schedulerIDs) != 1 {
		t.Fatalf("scheduler: want 1 observation, got %d", len(schedulerIDs))
	}
	if len(sessionIDs) != 1 {
		t.Fatalf("session: want 1 observation, got %d", len(sessionIDs))
	}

	// Anti-bluff: all three must equal the originating trace-ID.
	// If propagation is dropped any of these will differ and the test fails.
	gatewayObserved := gatewayIDs[0]
	schedulerObserved := schedulerIDs[0]
	sessionObserved := sessionIDs[0]

	if gatewayObserved != originTrace {
		t.Errorf("gateway trace-ID mismatch: want %q, got %q", originTrace, gatewayObserved)
	}
	if schedulerObserved != originTrace {
		t.Errorf("scheduler trace-ID mismatch: want %q, got %q", originTrace, schedulerObserved)
	}
	if sessionObserved != originTrace {
		t.Errorf("session trace-ID mismatch: want %q, got %q", originTrace, sessionObserved)
	}
}

// TestGRPCTraceID_NoIncomingStartsFresh verifies that the server interceptor
// starts a new, valid trace when no traceparent arrives in the metadata.
// The test drives the server directly, bypassing the client interceptor, so
// no traceparent is injected — simulating an un-instrumented caller.
func TestGRPCTraceID_NoIncomingStartsFresh(t *testing.T) {
	srv := &captureServer{}
	lis, stop := newBufServer(t, srv)
	defer stop()

	// Dial WITHOUT the client interceptor so no traceparent is sent.
	rawCC, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// intentionally no UnaryInterceptor
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer rawCC.Close()

	client := healthgrpc.NewHealthClient(rawCC)
	_, err = client.Check(context.Background(), &healthgrpc.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	srv.mu.Lock()
	observed := append([]string(nil), srv.observed...)
	srv.mu.Unlock()

	if len(observed) != 1 {
		t.Fatalf("want 1 observation, got %d", len(observed))
	}

	id := observed[0]
	// The server must have minted a fresh non-empty trace-ID.
	if id == "" {
		t.Fatal("server started no trace-ID for an un-instrumented caller")
	}
	// Must be 32 lowercase hex chars (W3C trace_id).
	if len(id) != 32 {
		t.Errorf("fresh trace-ID must be 32 hex chars, got %d: %q", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("fresh trace-ID contains non-lowercase-hex char %q in %q", c, id)
			break
		}
	}
}
