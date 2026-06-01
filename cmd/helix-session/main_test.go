package main

import (
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// killSession removes a real tmux session created by the server during a test
// so the suite leaves ZERO leaked sessions even on failure (PTY-exhaustion
// guard; the server-assigned id doubles as the tmux session name). Errors are
// ignored: the session may already be gone.
func killSession(t *testing.T, id string) {
	t.Helper()
	if id == "" {
		return
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", id).Run() })
}

// startRun launches run() on an ephemeral 127.0.0.1 port and blocks until the
// listener is accepting. It returns the bound address, a function to read the
// terminal run() error (after cancel), and the cancel func.
//
// Mutation that makes the suite FAIL: in run(), move the ready(lis.Addr())
// call to BEFORE net.Listen succeeds (or pass a closed listener) — the dial
// below then races a non-listening socket and CreateSession errors.
func startRun(t *testing.T, cfg Config) (addr net.Addr, runErr func() error, cancel context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	readyCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- run(ctx, cfg, func(a net.Addr) { readyCh <- a })
	}()

	select {
	case addr = <-readyCh:
	case err := <-errCh:
		cancel()
		t.Fatalf("run exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server ready")
	}

	return addr, func() error { return <-errCh }, cancel
}

func dial(t *testing.T, addr net.Addr) (helixv1.SessionServiceClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return helixv1.NewSessionServiceClient(conn), func() { _ = conn.Close() }
}

// TestRunServesRealRPC is the anti-bluff core: run() must actually bind, serve,
// and answer a real CreateSession RPC with a concrete, correct response.
//
// Mutation that makes this FAIL: in run(), delete the
// helixv1.RegisterSessionServiceServer(s, session.NewServer()) line. The server
// then has no handler registered and CreateSession returns Unimplemented.
func TestRunServesRealRPC(t *testing.T) {
	addr, runErr, cancel := startRun(t, Config{Host: "127.0.0.1", Port: 0})
	defer func() {
		cancel()
		if err := runErr(); err != nil {
			t.Errorf("run returned error: %v", err)
		}
	}()

	client, closeConn := dial(t, addr)
	defer closeConn()

	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()

	resp, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "cli-e2e", Owner: "neo"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	killSession(t, resp.GetId())
	if resp.Name != "cli-e2e" {
		t.Errorf("name = %q, want cli-e2e", resp.Name)
	}
	if resp.Owner != "neo" {
		t.Errorf("owner = %q, want neo", resp.Owner)
	}
	if resp.Id == "" {
		t.Error("expected a non-empty server-assigned session id")
	}
}

// TestRunGracefulShutdownStopsServing proves ctx-cancel actually stops the
// server: after cancel, run() returns nil AND the port stops accepting.
//
// Mutation that makes this FAIL: in run(), replace the `case <-ctx.Done():`
// branch body + GracefulStop block with a bare `return nil` that never calls
// s.GracefulStop()/s.Stop(). The listener stays open and the post-cancel dial
// below succeeds, failing the "stopped accepting" assertion.
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	addr, runErr, cancel := startRun(t, Config{Host: "127.0.0.1", Port: 0})

	client, closeConn := dial(t, addr)
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	preResp, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "pre", Owner: "trinity"})
	if err != nil {
		c()
		closeConn()
		cancel()
		t.Fatalf("pre-shutdown CreateSession: %v", err)
	}
	killSession(t, preResp.GetId())
	c()
	closeConn()

	// Trigger graceful shutdown and wait for run() to return.
	cancel()
	if err := runErr(); err != nil {
		t.Fatalf("run returned error on graceful shutdown: %v", err)
	}

	// Sink-side proof: the port no longer accepts connections.
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.Dial("tcp", addr.String())
	if err == nil {
		_ = conn.Close()
		t.Fatalf("port %s still accepting after shutdown; server did not stop", addr)
	}
}

// TestRunRejectsInvalidPort proves run() refuses an out-of-range port and never
// binds a listener.
//
// Mutation that makes this FAIL: in run(), delete the `if err := cfg.Validate()`
// guard. net.Listen would then attempt port 70000 and the returned error text
// would not contain the validation message.
func TestRunRejectsInvalidPort(t *testing.T) {
	err := run(context.Background(), Config{Host: "127.0.0.1", Port: 70000}, func(net.Addr) {
		t.Fatal("ready called for an invalid config; listener must not bind")
	})
	if err == nil {
		t.Fatal("expected error for out-of-range port, got nil")
	}
	if !contains(err.Error(), "invalid config") {
		t.Fatalf("error = %v, want invalid config wrap", err)
	}
}

// TestConfigValidate exercises the validation boundary directly.
//
// Mutation that makes this FAIL: in Config.Validate, change the guard to
// `c.Port < 0` — then port 0 and 65536 both pass and the table rows flip.
func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"zero", 0, true},
		{"negative", -1, true},
		{"min", 1, false},
		{"typical", 50053, false},
		{"max", 65535, false},
		{"over", 65536, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Port: tc.port}.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(port=%d) err=%v, wantErr=%v", tc.port, err, tc.wantErr)
			}
		})
	}
}

// TestLoadConfigRejectsNonIntegerPort proves env parsing rejects garbage rather
// than silently falling back to the default.
//
// Mutation that makes this FAIL: in LoadConfig, ignore the strconv.Atoi error
// (e.g. `p, _ := strconv.Atoi(raw)`); "not-a-port" then yields p=0 which is
// caught by Validate but with a different ("out of range") message, or if the
// Validate guard is also relaxed, no error at all.
func TestLoadConfigRejectsNonIntegerPort(t *testing.T) {
	t.Setenv("HELIX_SESSION_PORT", "not-a-port")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for non-integer port, got nil")
	}
	if !contains(err.Error(), "not an integer") {
		t.Fatalf("error = %v, want 'not an integer'", err)
	}
}

// TestLoadConfigDefaultPort proves the default applies when env is unset.
//
// Mutation that makes this FAIL: change defaultPort to a different value, or
// in LoadConfig set cfg.Port from a wrong constant.
func TestLoadConfigDefaultPort(t *testing.T) {
	t.Setenv("HELIX_SESSION_PORT", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("default port = %d, want %d", cfg.Port, defaultPort)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
