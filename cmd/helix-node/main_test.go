package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// startServer launches run() on 127.0.0.1:0 in a goroutine and returns the
// resolved listen address, a cancel func, and a channel carrying run()'s error.
// It blocks until the listener is actually bound (ready callback fired).
func startServer(t *testing.T, cfg Config) (addr string, cancel context.CancelFunc, errc <-chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())

	addrCh := make(chan net.Addr, 1)
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, cfg, func(a net.Addr) { addrCh <- a })
	}()

	select {
	case a := <-addrCh:
		return a.String(), cancelFn, runErr
	case err := <-runErr:
		cancelFn()
		t.Fatalf("server exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancelFn()
		t.Fatal("timed out waiting for server to bind")
	}
	return "", nil, nil
}

func dial(t *testing.T, addr string) (helixv1.NodeServiceClient, *grpc.ClientConn) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return helixv1.NewNodeServiceClient(conn), conn
}

// TestRunServesRealRPC proves the wired server actually serves a NodeService
// RPC end to end: a real gRPC client registers a node and reads it back.
//
// MUTATION: delete the helixv1.RegisterNodeServiceServer(gs, node.NewServer())
// line in run() -> RegisterNode fails with "Unimplemented" -> test FAILS.
func TestRunServesRealRPC(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second}
	addr, cancel, errc := startServer(t, cfg)
	defer func() {
		cancel()
		<-errc
	}()

	client, conn := dial(t, addr)
	defer conn.Close()

	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()

	regResp, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "node-A", Region: "eu-west"},
	})
	if err != nil {
		t.Fatalf("RegisterNode: %v", err)
	}
	if !regResp.Accepted {
		t.Fatalf("RegisterNode Accepted = false, want true")
	}
	if regResp.NodeId != "node-A" {
		t.Fatalf("RegisterNode NodeId = %q, want %q", regResp.NodeId, "node-A")
	}

	got, err := client.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "node-A"})
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Id != "node-A" || got.Region != "eu-west" {
		t.Fatalf("GetNode = {Id:%q Region:%q}, want {node-A eu-west}", got.Id, got.Region)
	}
	// Server stamps status "active" on registration; assert the sink-side state.
	if got.Status != "active" {
		t.Fatalf("GetNode Status = %q, want %q", got.Status, "active")
	}
}

// TestRunGracefulShutdownStopsServing proves ctx cancellation actually shuts the
// server down: run() returns nil and the port stops accepting connections.
//
// MUTATION: in run(), replace the ctx-driven shutdown goroutine body with a
// no-op (never call GracefulStop/Stop) -> run() never returns after cancel ->
// the "run did not return" path FAILS the test via timeout.
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second}
	addr, cancel, errc := startServer(t, cfg)

	// Sanity: it serves before shutdown.
	client, conn := dial(t, addr)
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	if _, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "pre-shutdown"},
	}); err != nil {
		c()
		conn.Close()
		cancel()
		<-errc
		t.Fatalf("pre-shutdown RegisterNode: %v", err)
	}
	c()
	conn.Close()

	// Trigger graceful shutdown.
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after ctx cancel (graceful shutdown hung)")
	}

	// Port must no longer accept new gRPC calls. Dial fresh and assert failure.
	c2client, c2conn := dial(t, addr)
	defer c2conn.Close()
	ctx2, cc := context.WithTimeout(context.Background(), 1*time.Second)
	defer cc()
	if _, err := c2client.RegisterNode(ctx2, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "post-shutdown"},
	}); err == nil {
		t.Fatal("RegisterNode succeeded after shutdown; server still serving")
	}
}

// TestConfigValidateRejectsBadPort proves config validation rejects an invalid
// port instead of opening a listener.
//
// MUTATION: make Validate() return nil unconditionally -> these cases no longer
// error -> test FAILS.
func TestConfigValidateRejectsBadPort(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"port too high", Config{Addr: "127.0.0.1:70000", ShutdownTimeout: time.Second}},
		{"non-numeric port", Config{Addr: "127.0.0.1:abc", ShutdownTimeout: time.Second}},
		{"missing port", Config{Addr: "127.0.0.1", ShutdownTimeout: time.Second}},
		{"empty addr", Config{Addr: "", ShutdownTimeout: time.Second}},
		{"non-positive shutdown", Config{Addr: "127.0.0.1:0", ShutdownTimeout: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatalf("Validate(%+v) = nil, want error", tc.cfg)
			}
		})
	}

	// And a valid config must pass.
	if err := (Config{Addr: "127.0.0.1:0", ShutdownTimeout: time.Second}).Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v, want nil", err)
	}
}

// TestRunRejectsBadConfigBeforeListen proves run() refuses an invalid config and
// never binds (the error is the validation error, not a listen error).
//
// MUTATION: remove the cfg.Validate() guard at the top of run() -> run() tries
// to listen on a bad addr and returns a "listen" error (or different wrapping)
// -> the errors.Is/contains assertion FAILS.
func TestRunRejectsBadConfigBeforeListen(t *testing.T) {
	err := run(context.Background(), Config{Addr: "127.0.0.1:70000", ShutdownTimeout: time.Second}, func(net.Addr) {
		t.Fatal("ready callback fired; run bound a listener despite invalid config")
	})
	if err == nil {
		t.Fatal("run with invalid config = nil, want error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid config") {
		t.Fatalf("run error = %q, want it to wrap %q", got, "invalid config")
	}
}

// TestRunListenConflict proves run() surfaces a real listen failure (port in
// use) rather than silently succeeding.
//
// MUTATION: swallow the net.Listen error in run() and return nil -> run()
// returns nil here -> test FAILS.
func TestRunListenConflict(t *testing.T) {
	// Hold a port so run() can't bind it.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer held.Close()

	err = run(context.Background(), Config{Addr: held.Addr().String(), ShutdownTimeout: time.Second}, func(net.Addr) {
		t.Fatal("ready fired despite a conflicting listener")
	})
	if err == nil {
		t.Fatal("run on busy port = nil, want listen error")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("run error = %q, want it to mention %q", err.Error(), "listen")
	}
}
