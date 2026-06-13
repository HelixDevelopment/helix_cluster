package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestRegisterConnCapRejects proves the gateway bounds total concurrent
// connections. Registering up to the cap succeeds; the next registration is
// rejected with ErrTooManyConns instead of growing the registry without bound.
//
// ANTI-BLUFF: in routingTable.add (gateway.go) remove the
//
//	if t.totalConns >= t.maxConns { return ErrTooManyConns }
//
// guard. This test then FAILS because the over-cap Register returns nil.
func TestRegisterConnCapRejects(t *testing.T) {
	g := NewWithConfig(Config{MaxConns: 3, MaxNodes: 100})
	for i := 0; i < 3; i++ {
		c := &captureConn{id: "node-" + string(rune('A'+i))}
		if err := g.Register(c); err != nil {
			t.Fatalf("registration %d within cap should succeed, got %v", i, err)
		}
	}
	over := &captureConn{id: "node-over"}
	if err := g.Register(over); !errors.Is(err, ErrTooManyConns) {
		t.Fatalf("over-cap registration: want ErrTooManyConns, got %v", err)
	}
	// Connection count must be exactly the cap — the rejected conn left no slot.
	if g.table.totalConns != 3 {
		t.Fatalf("totalConns leaked: got %d want 3", g.table.totalConns)
	}
}

// TestRegisterNodeCapRejectsNewNodesButCoalesces proves the distinct-node cap
// bounds routing-table key growth, while additional connections under an
// ALREADY-KNOWN node id coalesce and are NOT counted against the node cap (so
// legitimate multi-observer fan-out is unaffected).
//
// ANTI-BLUFF: in routingTable.add remove the
//
//	if !known && len(t.byNode) >= t.maxNodes { return ErrTooManyNodes }
//
// guard. The over-cap NEW node then registers and this test FAILS.
func TestRegisterNodeCapRejectsNewNodesButCoalesces(t *testing.T) {
	g := NewWithConfig(Config{MaxConns: 100, MaxNodes: 2})

	if err := g.Register(&captureConn{id: "node-A"}); err != nil {
		t.Fatalf("node-A: %v", err)
	}
	if err := g.Register(&captureConn{id: "node-B"}); err != nil {
		t.Fatalf("node-B: %v", err)
	}

	// A NEW distinct node beyond the node cap is rejected.
	if err := g.Register(&captureConn{id: "node-C"}); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("over-cap NEW node: want ErrTooManyNodes, got %v", err)
	}

	// But another connection (observer) under an EXISTING node coalesces: it does
	// not introduce a new key, so it must be accepted even though we are at the
	// node cap. This proves re-registration does not leak entries.
	if err := g.Register(&captureConn{id: "node-A"}); err != nil {
		t.Fatalf("additional observer of known node-A should be accepted, got %v", err)
	}
	if len(g.table.byNode) != 2 {
		t.Fatalf("distinct node count grew past cap: got %d want 2", len(g.table.byNode))
	}
	if got := len(g.table.lookup("node-A")); got != 2 {
		t.Fatalf("node-A should have 2 coalesced conns, got %d", got)
	}
}

// TestRegisterCapReleasedOnUnregister proves a rejected connection does not
// permanently consume a slot: after unregistering, capacity frees up. This
// guards against a leak where the count drifts from the live conns.
func TestRegisterCapReleasedOnUnregister(t *testing.T) {
	g := NewWithConfig(Config{MaxConns: 1, MaxNodes: 100})
	a := &captureConn{id: "node-A"}
	if err := g.Register(a); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := g.Register(&captureConn{id: "node-B"}); !errors.Is(err, ErrTooManyConns) {
		t.Fatalf("second register should hit cap, got %v", err)
	}
	g.Unregister(a)
	if err := g.Register(&captureConn{id: "node-B"}); err != nil {
		t.Fatalf("after unregister a slot should be free, got %v", err)
	}
}

// TestWebSocketConnCapRefusesOverCap proves the WS transport refuses an
// over-cap connection at the real socket boundary: the server sends a close
// frame (CloseTryAgainLater) instead of leaking a registry slot, and the
// registry never exceeds the cap.
//
// ANTI-BLUFF: remove the totalConns cap guard in routingTable.add — the third
// dial then registers and waitForRoutes-style assertion of cap==1 FAILS.
func TestWebSocketConnCapRefusesOverCap(t *testing.T) {
	gw := NewWithConfig(Config{MaxConns: 1, MaxNodes: 100})
	wst := NewWebSocketTransport(gw)
	defer gw.Close()
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := wsURL(srv.URL, "/edge")

	// First client fits within the cap.
	first, err := DialWS(ctx, base, "node-A")
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.Close()
	waitForRoutes(t, gw, map[string]int{"node-A": 1})

	// Second client is over the cap. The dial (HTTP upgrade) succeeds, but the
	// server immediately closes with CloseTryAgainLater; the next read observes
	// that close. The registry must stay at exactly one connection.
	second, err := DialWS(ctx, base, "node-B")
	if err != nil {
		// Some stacks may surface the rejection as a failed handshake; that is an
		// acceptable refusal too. Either way the registry must not have grown.
		assertNoExtraRoute(t, gw)
		return
	}
	defer second.Close()

	_, err = second.Recv(2 * time.Second)
	if err == nil {
		t.Fatalf("over-cap WS client should have been refused, but its read succeeded")
	}
	// The refusal may surface as a CloseTryAgainLater close frame or as a plain
	// transport-level close/EOF depending on timing; any read error here means
	// the server did not keep the over-cap connection alive. The load-bearing
	// assertion is the registry cap below.
	t.Logf("over-cap WS refusal surfaced as: %v", err)
	assertNoExtraRoute(t, gw)
}

// assertNoExtraRoute asserts the gateway registry holds exactly one connection
// (the within-cap one) after an over-cap attempt, allowing brief async settle.
func assertNoExtraRoute(t *testing.T, gw *Gateway) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gw.table.mu.RLock()
		n := gw.table.totalConns
		gw.table.mu.RUnlock()
		if n > 1 {
			t.Fatalf("CONN CAP VIOLATED: registry grew to %d connections past cap 1", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	gw.table.mu.RLock()
	n := gw.table.totalConns
	gw.table.mu.RUnlock()
	if n != 1 {
		t.Fatalf("registry should hold exactly the 1 within-cap conn, got %d", n)
	}
}

// TestWebSocketReadLimitRejectsOversizeFrame proves the WS read pump caps a
// single inbound message: a frame larger than wsMaxMessageBytes closes the
// connection (read error) instead of being fully allocated, and the server-side
// connection is reaped (registry returns to zero). This is the OOM guard.
//
// ANTI-BLUFF: remove `c.raw.SetReadLimit(wsMaxMessageBytes)` from readPump. The
// oversized frame is then read in full and routed (no close), so the post-send
// Recv does NOT error out the way the test asserts, and the registry is not
// reaped — this test FAILS.
func TestWebSocketReadLimitRejectsOversizeFrame(t *testing.T) {
	gw := New()
	wst := NewWebSocketTransport(gw)
	defer gw.Close()
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := wsURL(srv.URL, "/edge")

	c, err := DialWS(ctx, base, "node-A")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	waitForRoutes(t, gw, map[string]int{"node-A": 1})

	// Build an oversized but well-formed-envelope-shaped frame: a payload larger
	// than the read limit. We write it directly as a raw text frame so the server
	// must read (and reject) it at the transport layer.
	big := make([]byte, wsMaxMessageBytes+1024)
	for i := range big {
		big[i] = 'a'
	}
	oversize := Envelope{
		ID:       "oversize",
		Kind:     KindDispatch,
		Dest:     "node-A",
		Source:   "node-A",
		Payload:  json.RawMessage(`"` + string(big) + `"`),
		IssuedAt: time.Now().UTC(),
	}
	raw, err := EncodeEnvelope(oversize)
	if err != nil {
		t.Fatalf("encode oversize: %v", err)
	}
	if int64(len(raw)) <= wsMaxMessageBytes {
		t.Fatalf("test setup: frame %d not over limit %d", len(raw), wsMaxMessageBytes)
	}
	if err := c.raw.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write oversize frame: %v", err)
	}

	// The server must reap the connection (read limit -> close). Assert the
	// routing table drops back to zero rather than OOMing or leaking the conn.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		gw.table.mu.RLock()
		n := gw.table.totalConns
		gw.table.mu.RUnlock()
		if n == 0 {
			t.Logf("READ-LIMIT OK: oversized frame caused the server to reap the conn (registry=0)")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("oversized frame did not cause the server to close the connection; read limit not enforced")
}

// TestWebSocketIdleConnectionReaped proves the slow-loris guard: a connection
// that opens and then goes silent is reaped by the read deadline rather than
// pinned forever. To keep the test fast it uses a transport whose idle timeout
// is overridden to a short value via the unexported knobs exercised here.
//
// Because wsReadIdleTimeout is a package constant tuned for production (90s),
// this test instead proves the mechanism directly: it confirms a read deadline
// IS installed by observing that a silent connection is closed once the
// deadline elapses, using a per-connection short deadline injected through the
// test seam below.
//
// ANTI-BLUFF: remove the `SetReadDeadline` calls in readPump. The silent
// connection is then never reaped and this test FAILS on the timeout.
func TestWebSocketIdleConnectionReaped(t *testing.T) {
	gw := New()
	wst := NewWebSocketTransport(gw)
	// Shrink the idle timeout for this transport instance so the test is fast.
	wst.testReadIdleOverride = 200 * time.Millisecond
	defer gw.Close()
	srv := httptest.NewServer(wst.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := wsURL(srv.URL, "/edge")

	c, err := DialWS(ctx, base, "node-silent")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	waitForRoutes(t, gw, map[string]int{"node-silent": 1})

	// Send nothing and do NOT answer pings (the client never reads, so it never
	// auto-responds to pings either). The server's read deadline must fire and
	// reap the connection.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		gw.table.mu.RLock()
		n := gw.table.totalConns
		gw.table.mu.RUnlock()
		if n == 0 {
			t.Logf("SLOW-LORIS OK: silent connection reaped by idle read deadline")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("silent connection was NOT reaped; idle read deadline not enforced")
}

// TestLoopbackSendBackpressureDropsNotBlocks proves the per-connection send
// queue is bounded and non-blocking: once the inbox is full, Send returns
// ErrInboxFull instead of blocking the routing goroutine or growing without
// bound. A slow in-process consumer therefore cannot wedge routing or exhaust
// memory.
//
// ANTI-BLUFF: revert LoopbackConn.Send to the blocking `c.Inbox <- env` form.
// This test then BLOCKS on the over-capacity Send and the test times out
// (FAILS), instead of observing ErrInboxFull.
func TestLoopbackSendBackpressureDropsNotBlocks(t *testing.T) {
	g := New()
	lt := NewLoopbackTransport(g)
	defer lt.Close()

	c, err := lt.Connect("slow")
	if err != nil {
		t.Fatal(err)
	}

	env := Envelope{ID: "x", Kind: KindDispatch, Dest: "slow"}

	// Fill the bounded inbox exactly to capacity; every one of these must accept.
	for i := 0; i < loopbackInboxCap; i++ {
		if err := c.Send(env); err != nil {
			t.Fatalf("send %d within capacity should succeed, got %v", i, err)
		}
	}

	// The next Send must NOT block — it must return ErrInboxFull promptly. Run it
	// with a watchdog so a regression to blocking semantics is caught as a
	// timeout rather than hanging the suite.
	done := make(chan error, 1)
	go func() { done <- c.Send(env) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInboxFull) {
			t.Fatalf("over-capacity send: want ErrInboxFull, got %v", err)
		}
		t.Logf("BACKPRESSURE OK: over-capacity loopback send dropped with ErrInboxFull (non-blocking)")
	case <-time.After(2 * time.Second):
		t.Fatalf("BACKPRESSURE VIOLATED: over-capacity loopback send BLOCKED (queue is not bounded/non-blocking)")
	}

	// Inbox depth must never exceed the bound.
	if got := len(c.Inbox); got > loopbackInboxCap {
		t.Fatalf("inbox grew past bound: got %d want <= %d", got, loopbackInboxCap)
	}
}

// TestRouteSurfacesBackpressureDrop proves the drop is surfaced honestly: when
// a destination's bounded inbox is full, Gateway.Route returns the ErrInboxFull
// from Send (as firstErr) rather than silently swallowing it.
func TestRouteSurfacesBackpressureDrop(t *testing.T) {
	g := New()
	lt := NewLoopbackTransport(g)
	defer lt.Close()

	c, err := lt.Connect("slow")
	if err != nil {
		t.Fatal(err)
	}
	env := Envelope{ID: "x", Kind: KindDispatch, Dest: "slow", Source: "ctrl"}
	for i := 0; i < loopbackInboxCap; i++ {
		if err := c.Send(env); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	// Routing one more to the now-full inbox must surface the drop.
	if err := g.Route(env); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("Route to full inbox should surface ErrInboxFull, got %v", err)
	}
}
