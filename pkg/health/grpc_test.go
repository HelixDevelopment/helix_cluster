package health_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/health"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// dial connects to addr using insecure credentials with a short deadline.
func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cc, err := grpc.DialContext(ctx, addr, //nolint:staticcheck // DialContext is the stable API on this grpc version
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return cc
}

// checkStatus calls the grpc.health.v1 Check RPC and returns the
// HealthCheckResponse_ServingStatus, failing the test on any RPC error.
func checkStatus(t *testing.T, client grpc_health_v1.HealthClient, service string) grpc_health_v1.HealthCheckResponse_ServingStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("health Check(%q): %v", service, err)
	}
	return resp.Status
}

// TestRegisterGRPC_ServingWhenHealthy is the primary anti-bluff test:
//  1. Start a real in-process gRPC server with a Healthy checker → dial a real
//     grpc_health_v1 client → assert SERVING.
//  2. Flip the checker to Unhealthy via SetServing(hs, "", false) → assert
//     NOT_SERVING.
//  3. Flip back to healthy via SetServing(hs, "", true) → assert SERVING again.
//
// All three assertions can fail independently; no path always returns SERVING.
func TestRegisterGRPC_ServingWhenHealthy(t *testing.T) {
	// --- server setup ---
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	checker := health.NewChecker() // starts Healthy
	srv := grpc.NewServer()
	hs := health.RegisterGRPC(srv, checker)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() { srv.Stop() })

	// --- client setup ---
	cc := dial(t, lis.Addr().String())
	t.Cleanup(func() { _ = cc.Close() })
	client := grpc_health_v1.NewHealthClient(cc)

	// 1. Checker is Healthy → SERVING.
	got := checkStatus(t, client, "")
	if got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("step 1 (healthy checker): got %v, want SERVING", got)
	}

	// 2. Flip to NOT_SERVING via SetServing.
	health.SetServing(hs, "", false)
	got = checkStatus(t, client, "")
	if got != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("step 2 (SetServing false): got %v, want NOT_SERVING", got)
	}

	// 3. Flip back to SERVING.
	health.SetServing(hs, "", true)
	got = checkStatus(t, client, "")
	if got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("step 3 (SetServing true): got %v, want SERVING", got)
	}
}

// TestRegisterGRPC_SeedsUnhealthyChecker verifies that when RegisterGRPC is
// called with an already-Unhealthy checker the initial status seeded on the
// health server is NOT_SERVING (not SERVING).  This asserts the seed path, not
// just the SetServing path.
func TestRegisterGRPC_SeedsUnhealthyChecker(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	checker := health.NewChecker()
	checker.SetStatus(health.Unhealthy) // pre-seed as unhealthy BEFORE registration

	srv := grpc.NewServer()
	health.RegisterGRPC(srv, checker) // should seed NOT_SERVING

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() { srv.Stop() })

	cc := dial(t, lis.Addr().String())
	t.Cleanup(func() { _ = cc.Close() })
	client := grpc_health_v1.NewHealthClient(cc)

	got := checkStatus(t, client, "")
	if got != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("unhealthy checker seed: got %v, want NOT_SERVING", got)
	}
}

// TestRegisterGRPC_SeedsDegradedChecker verifies that a Degraded checker seeds
// SERVING (degraded = impaired but still serving).
func TestRegisterGRPC_SeedsDegradedChecker(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	checker := health.NewChecker()
	checker.SetStatus(health.Degraded)

	srv := grpc.NewServer()
	health.RegisterGRPC(srv, checker)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() { srv.Stop() })

	cc := dial(t, lis.Addr().String())
	t.Cleanup(func() { _ = cc.Close() })
	client := grpc_health_v1.NewHealthClient(cc)

	got := checkStatus(t, client, "")
	if got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("degraded checker seed: got %v, want SERVING", got)
	}
}
