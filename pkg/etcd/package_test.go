package etcd

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These are the NON-integration tests for the etcd client. They exercise ONLY
// production code paths that do not require a live etcd server:
//   - the constructor's default-injection and error-wrap behavior;
//   - the nil-receiver Close() defensive path.
//
// Every behavior that traverses a real etcd server (Put/Get/Watch/Lease/Lock)
// is proven in etcd_integration_test.go (build tag `integration`), which the
// orchestrator runs against a REAL etcd. Per CLAUDE-1, this file deliberately
// contains NO filler tests that assert against test-local maps or the stdlib —
// the previous TestMockKV / TestContextTimeout proved nothing about this
// package and have been removed.

// TestNew_DefaultTimeoutProducesUsableClient proves the constructor's
// default-injection branch (DialTimeout==0 -> 5s) yields a live, non-nil
// *Client whose underlying clientv3.Client is wired through Raw(). clientv3
// dials lazily, so with a non-empty endpoint New() succeeds without a server.
// (The unexported timeout value itself is not reachable from outside the
// clientv3 package; the observable, real contract here is "New succeeds and
// returns a usable client" — the timeout's effect is exercised end-to-end in
// the integration suite against a real, and a dead, endpoint.)
func TestNew_DefaultTimeoutProducesUsableClient(t *testing.T) {
	c, err := New(Config{Endpoints: []string{"127.0.0.1:2379"}})
	if err != nil {
		t.Fatalf("New with default timeout: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.Raw() == nil {
		t.Fatal("New returned a Client with a nil underlying clientv3.Client")
	}
}

// TestNew_NoEndpointsReturnsWrappedError proves the production error-wrap path:
// constructing a client with no endpoints fails with our %w-wrapped message,
// and the returned *Client is nil (no leaked half-built client). This is a
// genuinely real failure path — clientv3.New rejects an empty endpoint list
// synchronously — so it requires no etcd server.
func TestNew_NoEndpointsReturnsWrappedError(t *testing.T) {
	c, err := New(Config{Endpoints: []string{}, DialTimeout: 100 * time.Millisecond})
	if err == nil {
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("New with no endpoints: expected an error, got nil")
	}
	if c != nil {
		_ = c.Close()
		t.Fatal("New returned a non-nil Client alongside an error")
	}
	const wantPrefix = "failed to create etcd client:"
	if got := err.Error(); len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("error message: got %q, want it to start with %q", got, wantPrefix)
	}
}

// TestClientCloseNilReceiver proves the defensive nil-receiver Close() path:
// calling Close on a nil *Client (or a Client with a nil underlying client)
// must not panic and must return nil. This guards callers that defer Close
// before confirming a successful New.
func TestClientCloseNilReceiver(t *testing.T) {
	var c *Client
	if err := c.Close(); err != nil {
		t.Fatalf("nil *Client Close: got %v, want nil", err)
	}
	if err := (&Client{}).Close(); err != nil {
		t.Fatalf("Client{cli:nil} Close: got %v, want nil", err)
	}
}

// TestWatchEventZeroValue documents the WatchEvent contract used by consumers:
// a zero-value event has empty Type/Key/Value and a nil Err. This is a pure,
// real assertion on the package's exported type (no server needed) and guards
// against an accidental change to the struct's zero semantics that downstream
// watch consumers rely on.
func TestWatchEventZeroValue(t *testing.T) {
	var ev WatchEvent
	if ev.Type != "" || ev.Key != "" || ev.Value != "" {
		t.Fatalf("zero WatchEvent should have empty fields, got %+v", ev)
	}
	if ev.Err != nil {
		t.Fatalf("zero WatchEvent should have nil Err, got %v", ev.Err)
	}
	// errors.Is on a nil Err is well-defined and must be false against any target.
	if errors.Is(ev.Err, context.Canceled) {
		t.Fatal("zero WatchEvent.Err unexpectedly matched context.Canceled")
	}
}
