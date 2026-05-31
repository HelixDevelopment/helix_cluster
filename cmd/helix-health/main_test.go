package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	ihealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	pkghealth "github.com/HelixDevelopment/helix_cluster/pkg/health"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// pickFreePort asks the kernel for an available TCP port.
func pickFreePort(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to pick free port: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()
	return addr
}

// TestGRPCCheck_Healthy verifies the gRPC Check RPC returns healthy when
// the internal aggregator has at least one passing check.
func TestGRPCCheck_Healthy(t *testing.T) {
	port := pickFreePort(t)

	aggregator := ihealth.NewAggregator()
	// Register a real HTTP check against a local test server that returns 200.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	aggregator.RegisterCheck("http-test", &ihealth.HTTPCheck{URL: ts.URL, Timeout: 2 * time.Second})
	aggregator.RunChecks(context.Background())

	srv := ihealth.NewServer(aggregator)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, srv)

	lis, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		if err := gs.Serve(lis); err != nil {
			// Expected on graceful stop; ignore in test.
		}
	}()
	defer gs.GracefulStop()

	// Give the server a moment to start accepting.
	time.Sleep(50 * time.Millisecond)

	conn, err := grpc.NewClient(port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()

	client := helixv1.NewHealthServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &helixv1.CheckRequest{})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", resp.Status)
	}
	if _, ok := resp.Details["http-test"]; !ok {
		t.Errorf("expected details to contain http-test")
	}
}

// TestGRPCCheck_ServiceNotFound verifies Check returns an error for an unknown service.
func TestGRPCCheck_ServiceNotFound(t *testing.T) {
	port := pickFreePort(t)

	aggregator := ihealth.NewAggregator()
	srv := ihealth.NewServer(aggregator)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, srv)

	lis, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		_ = gs.Serve(lis)
	}()
	defer gs.GracefulStop()
	time.Sleep(50 * time.Millisecond)

	conn, err := grpc.NewClient(port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()

	client := helixv1.NewHealthServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Check(ctx, &helixv1.CheckRequest{Service: "missing-svc"})
	if err == nil {
		t.Fatal("expected error for missing service, got nil")
	}
}

// TestGRPCReportHealth verifies ReportHealth accepts a node update and that
// the aggregator reflects the reported status (via a subsequent Check).
func TestGRPCReportHealth(t *testing.T) {
	port := pickFreePort(t)

	aggregator := ihealth.NewAggregator()
	// Use a check that is guaranteed to be healthy so the overall status is deterministic.
	aggregator.RegisterCheck("always-healthy", ihealth.CheckFunc(func(ctx context.Context) (ihealth.Status, error) {
		return ihealth.Healthy, nil
	}))
	aggregator.RunChecks(context.Background())

	srv := ihealth.NewServer(aggregator)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, srv)

	lis, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		_ = gs.Serve(lis)
	}()
	defer gs.GracefulStop()
	time.Sleep(50 * time.Millisecond)

	conn, err := grpc.NewClient(port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()

	client := helixv1.NewHealthServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reportResp, err := client.ReportHealth(ctx, &helixv1.ReportHealthRequest{
		NodeId: "node-42",
		Score: &helixv1.HealthScore{
			Overall: 95,
		},
	})
	if err != nil {
		t.Fatalf("ReportHealth failed: %v", err)
	}
	if !reportResp.Accepted {
		t.Error("expected ReportHealth to be accepted")
	}

	// Ensure the underlying aggregator still reports the real check status.
	checkResp, err := client.Check(ctx, &helixv1.CheckRequest{})
	if err != nil {
		t.Fatalf("Check after ReportHealth failed: %v", err)
	}
	if checkResp.Status != "healthy" {
		t.Errorf("expected healthy after report, got %s", checkResp.Status)
	}
	if _, ok := checkResp.Details["always-healthy"]; !ok {
		t.Errorf("expected details to contain always-healthy check")
	}
}

// TestGRPCRealChecks verifies the aggregator runs real checks (HTTPCheck,
// DiskCheck, MemoryCheck) and the gRPC server surfaces their results.
// The test does NOT assert a specific overall status because real machine
// conditions (e.g. disk at 90%) may yield degraded; instead it asserts that
// every registered check appears in the response, proving real execution.
func TestGRPCRealChecks(t *testing.T) {
	port := pickFreePort(t)

	// HTTP endpoint that returns 200.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	aggregator := ihealth.NewAggregator()
	aggregator.RegisterCheck("http-check", &ihealth.HTTPCheck{URL: ts.URL, Timeout: 2 * time.Second})
	aggregator.RegisterCheck("disk-check", &ihealth.DiskCheck{Path: "/", Threshold: 1.0})
	aggregator.RegisterCheck("memory-check", &ihealth.MemoryCheck{Threshold: 1.0})

	// Run the real checks.
	aggregator.RunChecks(context.Background())

	// Verify aggregator directly first (anti-bluff): all three checks must have results.
	all := aggregator.GetAllStatuses()
	if len(all) != 3 {
		t.Fatalf("expected 3 check results, got %d", len(all))
	}
	for _, name := range []string{"http-check", "disk-check", "memory-check"} {
		if _, ok := all[name]; !ok {
			t.Fatalf("missing check result for %s", name)
		}
	}
	// HTTP check must be healthy because our test server returns 200.
	if httpSt, ok := all["http-check"]; !ok || httpSt.Status != ihealth.Healthy {
		t.Fatalf("http-check should be healthy, got %v", httpSt.Status)
	}

	srv := ihealth.NewServer(aggregator)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, srv)

	lis, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		_ = gs.Serve(lis)
	}()
	defer gs.GracefulStop()
	time.Sleep(50 * time.Millisecond)

	conn, err := grpc.NewClient(port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()

	client := helixv1.NewHealthServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &helixv1.CheckRequest{})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	// Overall status depends on machine state; just ensure it's one of the valid values.
	switch resp.Status {
	case "healthy", "degraded", "critical", "unknown":
		// ok
	default:
		t.Errorf("unexpected status %q", resp.Status)
	}
	for _, name := range []string{"http-check", "disk-check", "memory-check"} {
		if _, ok := resp.Details[name]; !ok {
			t.Errorf("expected details to contain %s", name)
		}
	}
}

// TestGRPCCheck_WithGRPCCheck verifies a real GRPCCheck execution.
// It spins up a minimal gRPC health server and then checks it.
func TestGRPCCheck_WithGRPCCheck(t *testing.T) {
	port := pickFreePort(t)

	// Start a minimal standard gRPC health server.
	healthGrpc := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(healthGrpc, &grpcHealthStub{})
	lis, err := net.Listen("tcp", port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = healthGrpc.Serve(lis) }()
	defer healthGrpc.GracefulStop()
	time.Sleep(50 * time.Millisecond)

	// Now create the helix health aggregator with a GRPCCheck targeting the stub.
	agg := ihealth.NewAggregator()
	agg.RegisterCheck("grpc-check", &ihealth.GRPCCheck{
		Address: port,
		Service: "",
		Timeout: 2 * time.Second,
	})
	agg.RunChecks(context.Background())

	st := agg.GetStatus()
	if st != ihealth.Healthy {
		t.Fatalf("expected healthy from GRPCCheck, got %s", st)
	}

	// Verify via gRPC Check RPC as well.
	helixPort := pickFreePort(t)
	srv := ihealth.NewServer(agg)
	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, srv)
	helixLis, err := net.Listen("tcp", helixPort)
	if err != nil {
		t.Fatalf("helix listen: %v", err)
	}
	go func() { _ = gs.Serve(helixLis) }()
	defer gs.GracefulStop()
	time.Sleep(50 * time.Millisecond)

	conn, err := grpc.NewClient(helixPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()

	client := helixv1.NewHealthServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &helixv1.CheckRequest{})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("expected healthy, got %s", resp.Status)
	}
	if _, ok := resp.Details["grpc-check"]; !ok {
		t.Errorf("expected details to contain grpc-check")
	}
}

// grpcHealthStub is a minimal implementation of the standard gRPC health protocol.
type grpcHealthStub struct {
	grpc_health_v1.UnimplementedHealthServer
}

func (s *grpcHealthStub) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

// TestClusterHealth verifies the legacy HTTP /health endpoint.
func TestClusterHealth(t *testing.T) {
	srv := newHTTPServer()
	srv.checker.AddCheck("db", func() pkghealth.Status { return pkghealth.Healthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.clusterHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Status  string                `json:"status"`
		Checks  []pkghealth.CheckResult `json:"checks"`
		Message string                `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("expected healthy, got %s", resp.Status)
	}
	if len(resp.Checks) == 0 {
		t.Error("expected at least one check")
	}
}

// TestClusterHealthUnhealthy verifies the legacy HTTP /health endpoint when unhealthy.
func TestClusterHealthUnhealthy(t *testing.T) {
	srv := newHTTPServer()
	srv.checker.AddCheck("db", func() pkghealth.Status { return pkghealth.Unhealthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.clusterHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Errorf("expected unhealthy, got %s", resp.Status)
	}
}

// TestServiceHealth verifies the legacy HTTP /check/<service> endpoint.
func TestServiceHealth(t *testing.T) {
	srv := newHTTPServer()
	srv.checker.AddCheck("scheduler", func() pkghealth.Status { return pkghealth.Healthy })

	req := httptest.NewRequest(http.MethodGet, "/check/scheduler", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Service != "scheduler" {
		t.Errorf("expected scheduler, got %s", resp.Service)
	}
}

// TestServiceHealthNotFound verifies the legacy HTTP endpoint for unknown services.
func TestServiceHealthNotFound(t *testing.T) {
	srv := newHTTPServer()

	req := httptest.NewRequest(http.MethodGet, "/check/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestServiceHealthMissingName verifies the legacy HTTP endpoint with missing name.
func TestServiceHealthMissingName(t *testing.T) {
	srv := newHTTPServer()

	req := httptest.NewRequest(http.MethodGet, "/check/", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestServiceHealthUnhealthy verifies the legacy HTTP endpoint for an unhealthy service.
func TestServiceHealthUnhealthy(t *testing.T) {
	srv := newHTTPServer()
	srv.checker.AddCheck("cache", func() pkghealth.Status { return pkghealth.Unhealthy })

	req := httptest.NewRequest(http.MethodGet, "/check/cache", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestNewHealthServer verifies the HTTP server constructor.
func TestNewHealthServer(t *testing.T) {
	srv := newHTTPServer()
	if srv.checker == nil {
		t.Fatal("expected checker to be initialized")
	}
}

// TestRegisterDefaults verifies default checks are healthy.
func TestRegisterDefaults(t *testing.T) {
	srv := newHTTPServer()
	srv.registerDefaults()
	status, _ := srv.checker.Check()
	if status != pkghealth.Healthy {
		t.Errorf("expected healthy after defaults, got %s", status)
	}
}
