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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// freePort asks the kernel for an available TCP port on 127.0.0.1 and returns
// it. Binding 127.0.0.1:0 then closing is host-safe and race-tolerant: the
// window before run() re-binds is tiny and local-only. We use concrete ports
// (not :0 in run) because Config.Validate legitimately rejects port 0 for a
// real deployment.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// startServer launches run() on free 127.0.0.1 ports and returns the bound
// gRPC + HTTP addresses plus a stop func. It blocks (via the ready callback)
// until both listeners are open, so there is no bind race for a dialing client.
func startServer(t *testing.T) (addrs ReadyAddrs, stop func()) {
	t.Helper()

	cfg := Config{
		Host:            "127.0.0.1",
		GRPCPort:        freePort(t),
		HTTPPort:        freePort(t),
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan ReadyAddrs, 1)
	runErr := make(chan error, 1)

	go func() {
		runErr <- run(ctx, cfg, func(a ReadyAddrs) { addrCh <- a })
	}()

	select {
	case a := <-addrCh:
		addrs = a
	case err := <-runErr:
		cancel()
		t.Fatalf("run exited before binding: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server to bind")
	}

	stop = func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("run returned error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("timed out waiting for run to shut down")
		}
	}
	return addrs, stop
}

func dialHealth(t *testing.T, addr string) (helixv1.HealthServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	return helixv1.NewHealthServiceClient(conn), func() { _ = conn.Close() }
}

// TestRunServesGRPCCheckHealthy proves the service actually starts, binds both
// listeners, and serves a REAL gRPC request: a real client dials the bound
// gRPC port, calls Check, and gets a concrete "healthy" status plus the
// self-check in Details — proving the readiness/liveness self-check ran.
//
// Mutation that breaks it: in run(), drop the
// `helixv1.RegisterHealthServiceServer(gs, grpcSrv)` line. Check then fails
// with codes.Unimplemented and this test fails. Alternatively, remove the
// aggregator.RunChecks(ctx) call and the status becomes "unknown" (no results),
// failing the resp.Status == "healthy" assertion.
func TestRunServesGRPCCheckHealthy(t *testing.T) {
	addrs, stop := startServer(t)
	defer stop()

	client, closeConn := dialHealth(t, addrs.GRPC)
	defer closeConn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &helixv1.CheckRequest{})
	if err != nil {
		t.Fatalf("Check RPC failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil CheckResponse")
	}
	if resp.Status != "healthy" {
		t.Fatalf("expected overall status healthy, got %q", resp.Status)
	}
	if _, ok := resp.Details["helix-health"]; !ok {
		t.Fatalf("expected Details to contain the helix-health self-check, got %v", resp.Details)
	}
}

// TestRunServesGRPCCheckNotFound proves a second RPC path is really wired
// through to internal/health: querying an unknown service returns a concrete
// gRPC NotFound status (not a transport error, not OK).
//
// Mutation that breaks it: drop RegisterHealthServiceServer in run() — the call
// returns codes.Unimplemented instead of codes.NotFound, failing the assertion.
func TestRunServesGRPCCheckNotFound(t *testing.T) {
	addrs, stop := startServer(t)
	defer stop()

	client, closeConn := dialHealth(t, addrs.GRPC)
	defer closeConn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Check(ctx, &helixv1.CheckRequest{Service: "no-such-service"})
	if err == nil {
		t.Fatal("expected NotFound error for unknown service, got nil")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v (%v)", got, err)
	}
}

// TestRunServesHTTPReadyz proves the HTTP probe surface is really bound and
// serving: a real HTTP client GETs /readyz on the bound port and receives 200
// with a JSON body reporting ready=true — driven by the aggregator's real
// readiness rollup (the self-check is KindBoth and Healthy).
//
// Mutation that breaks it: in run(), remove the aggregator.RunChecks(ctx) call.
// With no results, Ready() returns false (readiness rollup Unknown), so /readyz
// returns 503 and ready=false, failing both assertions below.
func TestRunServesHTTPReadyz(t *testing.T) {
	addrs, stop := startServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addrs.HTTP+"/readyz", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /readyz, got %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Ready  bool   `json:"ready"`
		Probe  string `json:"probe"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}
	if !body.Ready {
		t.Fatalf("expected ready=true, got %+v", body)
	}
	if body.Status != "healthy" {
		t.Fatalf("expected readiness status healthy, got %q", body.Status)
	}
	if body.Probe != "readiness" {
		t.Fatalf("expected probe=readiness, got %q", body.Probe)
	}
}

// TestRunServesHTTPLivez proves the /livez liveness probe is bound and serves a
// concrete healthy response from the aggregator's liveness rollup.
//
// Mutation that breaks it: in run(), register the self-check with
// ihealth.KindReadiness instead of KindBoth. The liveness rollup then has no
// matching results and Liveness() returns Healthy by its empty default — so to
// truly kill this test, also drop RunChecks; more directly, removing the
// /livez route registration in mux() makes this return 404, failing the 200
// assertion.
func TestRunServesHTTPLivez(t *testing.T) {
	addrs, stop := startServer(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addrs.HTTP+"/livez", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /livez: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /livez, got %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Probe  string `json:"probe"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /livez body: %v", err)
	}
	if body.Status != "healthy" {
		t.Fatalf("expected liveness status healthy, got %q", body.Status)
	}
	if body.Probe != "liveness" {
		t.Fatalf("expected probe=liveness, got %q", body.Probe)
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually stops
// BOTH services: after stop() returns, neither the gRPC nor the HTTP port
// accepts new connections.
//
// Mutation that breaks it: in run(), replace the `case <-ctx.Done():` shutdown
// path with a `select {}` (block forever), or remove gs.GracefulStop() and
// httpServerInst.Shutdown(). The listeners keep serving, the post-shutdown
// dials below succeed, and this test fails (stop() would also time out).
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	addrs, stop := startServer(t)

	// Sanity: gRPC serves before shutdown.
	client, closeConn := dialHealth(t, addrs.GRPC)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := client.Check(ctx, &helixv1.CheckRequest{})
	cancel()
	if err != nil || resp == nil {
		closeConn()
		t.Fatalf("pre-shutdown Check failed: %v", err)
	}
	closeConn()

	// Trigger ctx cancel + wait for run() to fully return.
	stop()

	// Both bound TCP ports must now refuse connections. net.DialTimeout is the
	// unambiguous sink: a stopped listener refuses new connections.
	for _, addr := range []string{addrs.GRPC, addrs.HTTP} {
		conn, dErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if dErr == nil {
			conn.Close()
			t.Fatalf("expected %s to stop accepting after shutdown, but dial succeeded", addr)
		}
	}
}

// TestLoadConfigRejectsBadPort proves config validation rejects clearly-bad
// input with a real error instead of starting a broken server.
//
// Mutation that breaks it: in Config.Validate(), change the gRPC guard to
// `if c.GRPCPort < 0` (or delete it). Then 70000 is accepted and LoadConfig
// returns nil error, failing the "out-of-range-high" case below. Likewise,
// deleting the strconv error check in LoadConfig makes "not-a-port" accepted.
func TestLoadConfigRejectsBadPort(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"out-of-range-high", "70000"},
		{"out-of-range-zero", "0"},
		{"negative", "-1"},
		{"non-integer", "not-a-port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string {
				if k == "HELIX_HEALTH_PORT" {
					return tc.val
				}
				return ""
			}
			if _, err := LoadConfig(env); err == nil {
				t.Fatalf("expected error for HELIX_HEALTH_PORT=%q, got nil", tc.val)
			}
		})
	}
}

// TestLoadConfigRejectsCollidingPorts proves the cross-field guard: two fixed,
// equal, non-zero ports are rejected before run() would fail the second bind.
//
// Mutation that breaks it: delete the `c.GRPCPort == c.HTTPPort` guard in
// Validate(). LoadConfig then returns nil error and this test fails.
func TestLoadConfigRejectsCollidingPorts(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "HELIX_HEALTH_PORT":
			return "51000"
		case "HELIX_HEALTH_HTTP_PORT":
			return "51000"
		}
		return ""
	}
	if _, err := LoadConfig(env); err == nil {
		t.Fatal("expected error for colliding gRPC/HTTP ports, got nil")
	}
}

// TestLoadConfigDefaults proves a clean environment yields the documented
// default ports, and that valid overrides are honoured.
//
// Mutation that breaks it: change defaultGRPCPort/defaultHTTPPort, or make
// LoadConfig ignore the env vars — an assertion below then fails.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig with empty env: %v", err)
	}
	if cfg.GRPCPort != defaultGRPCPort {
		t.Fatalf("expected default gRPC port %d, got %d", defaultGRPCPort, cfg.GRPCPort)
	}
	if cfg.HTTPPort != defaultHTTPPort {
		t.Fatalf("expected default HTTP port %d, got %d", defaultHTTPPort, cfg.HTTPPort)
	}

	cfg2, err := LoadConfig(func(k string) string {
		switch k {
		case "HELIX_HEALTH_PORT":
			return "51234"
		case "HELIX_HEALTH_HTTP_PORT":
			return "51235"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadConfig with valid overrides: %v", err)
	}
	if cfg2.GRPCPort != 51234 {
		t.Fatalf("expected overridden gRPC port 51234, got %d", cfg2.GRPCPort)
	}
	if cfg2.HTTPPort != 51235 {
		t.Fatalf("expected overridden HTTP port 51235, got %d", cfg2.HTTPPort)
	}
}

// TestRunRejectsInvalidConfig proves run() refuses to bind on invalid config
// rather than silently doing something undefined.
//
// Mutation that breaks it: remove the `cfg.Validate()` call at the top of
// run(). run would then attempt net.Listen on an invalid port; the returned
// error message/path differs, failing this assertion.
func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), Config{GRPCPort: 0, HTTPPort: 0, ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("expected run to reject invalid config, got nil error")
	}
}

// --- Handler-level tests for the legacy /health and /check/<service> surface.
// These exercise the JSON contract directly via httptest without a live socket.

func newTestHTTPServer() *httpServer {
	return newHTTPServer(ihealth.NewAggregator())
}

// TestClusterHealth verifies the legacy HTTP /health endpoint returns 200 and
// reports a healthy check.
//
// Mutation that breaks it: in clusterHealth, always WriteHeader(503) — the 200
// assertion fails.
func TestClusterHealth(t *testing.T) {
	srv := newTestHTTPServer()
	srv.checker.AddCheck("db", func() pkghealth.Status { return pkghealth.Healthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.clusterHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Status string                  `json:"status"`
		Checks []pkghealth.CheckResult `json:"checks"`
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

// TestClusterHealthUnhealthy verifies /health returns 503 when a check fails.
//
// Mutation that breaks it: drop the `if status == pkghealth.Unhealthy` branch
// in clusterHealth so it always returns 200 — the 503 assertion fails.
func TestClusterHealthUnhealthy(t *testing.T) {
	srv := newTestHTTPServer()
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

// TestServiceHealth verifies /check/<service> echoes the named service.
//
// Mutation that breaks it: in serviceHealth, set Service to the constant ""
// instead of name — the resp.Service assertion fails.
func TestServiceHealth(t *testing.T) {
	srv := newTestHTTPServer()
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

// TestServiceHealthNotFound verifies /check/<unknown> returns 404.
//
// Mutation that breaks it: change the final WriteHeader to StatusOK — the 404
// assertion fails.
func TestServiceHealthNotFound(t *testing.T) {
	srv := newTestHTTPServer()

	req := httptest.NewRequest(http.MethodGet, "/check/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestServiceHealthMissingName verifies /check/ with no name returns 400.
//
// Mutation that breaks it: remove the empty-name guard in serviceHealth — the
// 400 assertion fails (it would fall through to 404).
func TestServiceHealthMissingName(t *testing.T) {
	srv := newTestHTTPServer()

	req := httptest.NewRequest(http.MethodGet, "/check/", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestLivezUnhealthyReturns503 proves the liveness handler reports 503 when the
// aggregator's liveness rollup is not healthy — i.e. the handler reflects real
// aggregator state, not a hard-coded 200.
//
// Mutation that breaks it: in livez(), always WriteHeader(200) — the 503
// assertion fails.
func TestLivezUnhealthyReturns503(t *testing.T) {
	agg := ihealth.NewAggregator()
	agg.RegisterCheckKind("wedged", ihealth.CheckFunc(func(ctx context.Context) (ihealth.Status, error) {
		return ihealth.Critical, nil
	}), ihealth.KindLiveness)
	agg.RunChecks(context.Background())

	srv := newHTTPServer(agg)
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()
	srv.livez(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for critical liveness, got %d", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body.Status != "critical" {
		t.Fatalf("expected liveness status critical, got %q", body.Status)
	}
}
