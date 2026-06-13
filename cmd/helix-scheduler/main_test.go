package main

import (
	"context"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// freePort asks the kernel for an available TCP port on 127.0.0.1 and returns
// it. Binding 127.0.0.1:0 then closing is host-safe and race-tolerant: the
// window before run() re-binds is tiny and local-only.
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

// startServer launches run() on a free 127.0.0.1 port and returns the bound
// address plus a stop func. It blocks (via the ready callback) until the
// listener is actually open, so there is no bind race for the dialing client.
func startServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	cfg := Config{
		Host:            "127.0.0.1",
		Port:            freePort(t),
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	go func() {
		runErr <- run(ctx, cfg, func(a string) { addrCh <- a })
	}()

	select {
	case a := <-addrCh:
		addr = a
	case err := <-runErr:
		t.Fatalf("run exited before binding: %v", err)
	case <-time.After(5 * time.Second):
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
	return addr, stop
}

func dial(t *testing.T, addr string) (helixv1.SchedulerServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	return helixv1.NewSchedulerServiceClient(conn), func() { _ = conn.Close() }
}

// TestRunServesListJobs proves the service actually starts, binds, and serves a
// REAL gRPC request: a real client dials the bound port, calls ListJobs, and
// gets a concrete (non-nil, empty) response back from internal/scheduler.
//
// Mutation that breaks it: in run(), drop the
// `helixv1.RegisterSchedulerServiceServer(gs, scheduler.NewServer())` line.
// The server then has no SchedulerService registered and ListJobs fails with
// codes.Unimplemented, so resp is nil / err != nil and this test fails.
func TestRunServesListJobs(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.ListJobs(ctx, &helixv1.ListJobsRequest{})
	if err != nil {
		t.Fatalf("ListJobs RPC failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil ListJobsResponse")
	}
	// A fresh server has scheduled no jobs, so the list must be empty.
	if len(resp.Jobs) != 0 {
		t.Fatalf("expected 0 jobs on a fresh server, got %d", len(resp.Jobs))
	}
}

// TestRunServesGetJobStatusNotFound proves a second RPC path is really wired
// through to internal/scheduler: querying an unknown job returns a concrete
// gRPC NotFound status (not a transport error, not OK).
//
// Mutation that breaks it: in internal/scheduler is out of scope; at the cmd
// level the load-bearing line is the RegisterSchedulerServiceServer wiring in
// run(). Removing it makes this return codes.Unimplemented instead of
// codes.NotFound, failing the assertion below.
func TestRunServesGetJobStatusNotFound(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.GetJobStatus(ctx, &helixv1.GetJobStatusRequest{JobId: "does-not-exist"})
	if err == nil {
		t.Fatal("expected NotFound error for unknown job, got nil")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v (%v)", got, err)
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually stops
// the service: after stop() returns, the port no longer accepts new gRPC RPCs.
//
// Mutation that breaks it: in run(), replace the `case <-ctx.Done():` shutdown
// path with a `select{}` (block forever) — or remove the gs.GracefulStop()
// call. The listener would keep serving and the post-shutdown RPC below would
// succeed, failing this test (and stop() would also time out).
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	addr, stop := startServer(t)

	// Sanity: it serves before shutdown.
	client, closeConn := dial(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := client.ListJobs(ctx, &helixv1.ListJobsRequest{})
	cancel()
	if err != nil || resp == nil {
		closeConn()
		t.Fatalf("pre-shutdown ListJobs failed: %v", err)
	}
	closeConn()

	// Trigger ctx cancel + wait for run() to fully return.
	stop()

	// The bound TCP port must no longer accept connections. We assert at the
	// transport layer (net.DialTimeout) because that is the unambiguous sink:
	// a stopped listener refuses connections.
	conn, dErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if dErr == nil {
		conn.Close()
		t.Fatalf("expected port %s to stop accepting after shutdown, but dial succeeded", addr)
	}
}

// TestLoadConfigRejectsBadPort proves config validation rejects clearly-bad
// input with a real error instead of starting a broken server.
//
// Mutation that breaks it: in Config.Validate(), change the guard to
// `if c.Port < 0` (or delete the port check). Then port 70000 is accepted and
// LoadConfig returns nil error, failing this test.
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
				if k == "HELIX_SCHEDULER_PORT" {
					return tc.val
				}
				return ""
			}
			if _, err := LoadConfig(env); err == nil {
				t.Fatalf("expected error for HELIX_SCHEDULER_PORT=%q, got nil", tc.val)
			}
		})
	}
}

// TestLoadConfigDefaults proves a clean environment yields the documented
// default port, and that a valid override is honoured.
//
// Mutation that breaks it: change defaultPort to a different value, or make
// LoadConfig ignore HELIX_SCHEDULER_PORT — either assertion below then fails.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig with empty env: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("expected default port %d, got %d", defaultPort, cfg.Port)
	}

	cfg2, err := LoadConfig(func(k string) string {
		if k == "HELIX_SCHEDULER_PORT" {
			return "51234"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadConfig with valid override: %v", err)
	}
	if cfg2.Port != 51234 {
		t.Fatalf("expected overridden port 51234, got %d", cfg2.Port)
	}
}

// TestRunRejectsInvalidConfig proves run() refuses to bind on invalid config
// rather than silently doing something undefined.
//
// Mutation that breaks it: remove the `cfg.Validate()` call at the top of
// run(). The function would then attempt net.Listen on an invalid port and the
// error path / message would differ, failing this assertion.
func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), Config{Port: 0, ShutdownTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("expected run to reject invalid config, got nil error")
	}
}
