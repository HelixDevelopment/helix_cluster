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

func dial(t *testing.T, addr string) (helixv1.SecurityServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	return helixv1.NewSecurityServiceClient(conn), func() { _ = conn.Close() }
}

// rpcCtx returns a context with a generous deadline. The internal/security
// Authenticate path issues a REAL self-signed certificate (RSA keygen +
// x509.CreateCertificate), which can be slow on a loaded CI box, so we allow
// plenty of headroom rather than flaking.
func rpcCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 20*time.Second)
}

// TestRunAuthenticateIssuesRealToken proves the service actually starts, binds,
// and serves a REAL gRPC Authenticate request wired to internal/security: a
// real client dials the bound port and gets back Success=true, a non-empty
// token (a genuine issued certificate ID), and the derived SPIFFE id.
//
// Mutation that breaks it: in run(), drop the
// `helixv1.RegisterSecurityServiceServer(gs, newSecurityServer())` line. The
// server then has no SecurityService registered and Authenticate fails with
// codes.Unimplemented, so err != nil and this test fails. (A second killing
// mutation: change newSecurityServer to register the
// UnimplementedSecurityServiceServer directly.)
func TestRunAuthenticateIssuesRealToken(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := rpcCtx(t)
	defer cancel()

	resp, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{
		Identity: "user1",
		Proof:    []byte("secret"),
		Method:   "password",
	})
	if err != nil {
		t.Fatalf("Authenticate RPC failed: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("expected Success=true, got %+v", resp)
	}
	if resp.Token == "" {
		t.Fatal("expected a non-empty token (issued certificate ID)")
	}
	if resp.SpiffeId != "spiffe://helix.local/user1" {
		t.Fatalf("expected spiffe://helix.local/user1, got %q", resp.SpiffeId)
	}
}

// TestRunAuthenticateThenValidateToken proves a real end-to-end credential
// round-trip through internal/security: a token issued by Authenticate is
// accepted by ValidateToken, which returns the same identity. This is the
// sink-side evidence (CLAUDE-1) that the issued credential is genuine state,
// not a fabricated constant — a stub that returned a fixed "stub-token-..."
// string would NOT validate against the orchestrator's cert store.
//
// Mutation that breaks it: in cmd, change newSecurityServer to build TWO
// separate orchestrators (one for the server, and imagine a different one for
// validation) — but more directly, replacing the issued token below with a
// fabricated literal like "stub-token-user1" makes ValidateToken return
// Valid=false, failing the assertion.
func TestRunAuthenticateThenValidateToken(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := rpcCtx(t)
	defer cancel()

	auth, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "user1"})
	if err != nil {
		t.Fatalf("Authenticate RPC failed: %v", err)
	}

	val, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{Token: auth.Token})
	if err != nil {
		t.Fatalf("ValidateToken RPC failed: %v", err)
	}
	if val == nil || !val.Valid {
		t.Fatalf("expected the freshly issued token to validate, got %+v", val)
	}
	if val.Identity != "user1" {
		t.Fatalf("expected identity user1, got %q", val.Identity)
	}
}

// TestRunValidateTokenRejectsUnknown proves ValidateToken does NOT rubber-stamp
// arbitrary strings: an unissued token is reported invalid. This kills the
// class of bluff where any "stub-token-"-prefixed string was accepted.
//
// Mutation that breaks it: at the cmd seam there is no place to weaken this
// without editing internal/security (out of scope); the load-bearing cmd line
// is again the RegisterSecurityServiceServer wiring — removing it yields
// codes.Unimplemented (err != nil) instead of Valid=false, failing this test.
func TestRunValidateTokenRejectsUnknown(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := rpcCtx(t)
	defer cancel()

	resp, err := client.ValidateToken(ctx, &helixv1.ValidateTokenRequest{Token: "stub-token-not-real"})
	if err != nil {
		t.Fatalf("ValidateToken RPC failed: %v", err)
	}
	if resp == nil || resp.Valid {
		t.Fatalf("expected an unissued token to be invalid, got %+v", resp)
	}
}

// TestRunAuthorizeDeniedByDefault proves Authorize runs the REAL RBAC enforcer:
// with no roles loaded, a valid token is denied (deny-by-default) and a concrete
// denial reason is returned. The previous stub bluff allowed every token whose
// string began with "stub-token" — this test would have caught that as a
// privilege-escalation defect.
//
// Mutation that breaks it: in cmd's newSecurityServer, if you pre-loaded a
// wildcard "admin" role and policy granting everything, Authorize would return
// Allowed=true and this assertion (resp.Allowed must be false) would fail.
func TestRunAuthorizeDeniedByDefault(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := rpcCtx(t)
	defer cancel()

	auth, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "user1"})
	if err != nil {
		t.Fatalf("Authenticate RPC failed: %v", err)
	}

	authz, err := client.Authorize(ctx, &helixv1.AuthorizeRequest{
		Token:    auth.Token,
		Resource: "cluster",
		Action:   "read",
	})
	if err != nil {
		t.Fatalf("Authorize RPC failed: %v", err)
	}
	if authz == nil || authz.Allowed {
		t.Fatalf("expected deny-by-default with no roles loaded, got %+v", authz)
	}
	if authz.Reason == "" {
		t.Fatal("expected a non-empty denial reason")
	}
}

// TestRunAuthenticateRejectsMissingIdentity proves the service surfaces input
// validation as a concrete gRPC status (InvalidArgument) over the wire, not a
// silent success.
//
// Mutation that breaks it: this guard lives in internal/security (out of
// scope); at the cmd level, dropping RegisterSecurityServiceServer changes the
// code from InvalidArgument to Unimplemented, failing the assertion below.
func TestRunAuthenticateRejectsMissingIdentity(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, cancel := rpcCtx(t)
	defer cancel()

	_, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{})
	if err == nil {
		t.Fatal("expected error for missing identity, got nil")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v (%v)", got, err)
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually stops
// the service: after stop() returns, the port no longer accepts new
// connections.
//
// Mutation that breaks it: in run(), replace the `case <-ctx.Done():` shutdown
// path with `select{}` (block forever) — or remove the gs.GracefulStop() call.
// The listener would keep serving and the post-shutdown dial below would
// succeed, failing this test (and stop() would also time out).
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	addr, stop := startServer(t)

	// Sanity: it serves before shutdown.
	client, closeConn := dial(t, addr)
	ctx, cancel := rpcCtx(t)
	resp, err := client.Authenticate(ctx, &helixv1.AuthenticateRequest{Identity: "user1"})
	cancel()
	if err != nil || resp == nil || !resp.Success {
		closeConn()
		t.Fatalf("pre-shutdown Authenticate failed: %v (%+v)", err, resp)
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
				if k == "HELIX_SECURITY_PORT" {
					return tc.val
				}
				return ""
			}
			if _, err := LoadConfig(env); err == nil {
				t.Fatalf("expected error for HELIX_SECURITY_PORT=%q, got nil", tc.val)
			}
		})
	}
}

// TestLoadConfigDefaults proves a clean environment yields the documented
// default port, and that a valid override is honoured.
//
// Mutation that breaks it: change defaultPort to a different value, or make
// LoadConfig ignore HELIX_SECURITY_PORT — either assertion below then fails.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfig with empty env: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("expected default port %d, got %d", defaultPort, cfg.Port)
	}

	cfg2, err := LoadConfig(func(k string) string {
		if k == "HELIX_SECURITY_PORT" {
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
