package gateway

import (
	"errors"
	"sync"
	"testing"
)

// captureConn is a Conn that records delivered envelopes, used for fast
// in-memory routing-table unit tests (the network round-trip is proven
// separately in the WebSocket integration test).
type captureConn struct {
	id   string
	mu   sync.Mutex
	recv []Envelope
}

func (c *captureConn) NodeID() string { return c.id }
func (c *captureConn) Send(e Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recv = append(c.recv, e)
	return nil
}
func (c *captureConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.recv)
}

func TestRouteUnicastIsolation(t *testing.T) {
	g := New()
	a := &captureConn{id: "node-A"}
	b := &captureConn{id: "node-B"}
	if err := g.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := g.Register(b); err != nil {
		t.Fatal(err)
	}

	env := Envelope{ID: "m1", Kind: KindDispatch, Dest: "node-A"}
	if err := g.Route(env); err != nil {
		t.Fatalf("route: %v", err)
	}
	// Core isolation assertion: A got it, B did NOT.
	if a.count() != 1 {
		t.Fatalf("node-A should have received 1 envelope, got %d", a.count())
	}
	if b.count() != 0 {
		t.Fatalf("isolation violated: node-B received %d envelopes, want 0", b.count())
	}
}

func TestRouteNoRoute(t *testing.T) {
	g := New()
	env := Envelope{ID: "m1", Kind: KindDispatch, Dest: "ghost"}
	if err := g.Route(env); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
}

func TestUnregisterRemovesRoute(t *testing.T) {
	g := New()
	a := &captureConn{id: "node-A"}
	_ = g.Register(a)
	g.Unregister(a)
	env := Envelope{ID: "m1", Kind: KindDispatch, Dest: "node-A"}
	if err := g.Route(env); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute after unregister, got %v", err)
	}
}

func TestStatusFanBackExcludesSource(t *testing.T) {
	g := New()
	a := &captureConn{id: "node-A"}
	b := &captureConn{id: "node-B"}
	_ = g.Register(a)
	_ = g.Register(b)

	// Unaddressed status from node-A fans to others (B) but not back to A.
	env := Envelope{ID: "s1", Kind: KindStatus, Source: "node-A"}
	if err := g.Route(env); err != nil {
		t.Fatalf("route status: %v", err)
	}
	if a.count() != 0 {
		t.Fatalf("source node-A should not receive its own status, got %d", a.count())
	}
	if b.count() != 1 {
		t.Fatalf("observer node-B should receive status, got %d", b.count())
	}
}

func TestRouteAfterCloseFails(t *testing.T) {
	g := New()
	_ = g.Close()
	env := Envelope{ID: "m1", Kind: KindDispatch, Dest: "node-A"}
	if err := g.Route(env); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestLoopbackCrossDelivery(t *testing.T) {
	g := New()
	lt := NewLoopbackTransport(g)
	a, err := lt.Connect("node-A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := lt.Connect("node-B")
	if err != nil {
		t.Fatal(err)
	}

	env := Envelope{ID: "m1", Kind: KindDispatch, Dest: "node-B", Source: "node-A"}
	if err := a.Route(env); err != nil {
		t.Fatalf("route: %v", err)
	}
	select {
	case got := <-b.Inbox:
		if got.ID != "m1" {
			t.Fatalf("got wrong envelope id %q", got.ID)
		}
	default:
		t.Fatal("node-B loopback inbox empty; envelope not delivered")
	}
	// Isolation: A must not have received its own dispatch.
	select {
	case got := <-a.Inbox:
		t.Fatalf("isolation violated: node-A received %q", got.ID)
	default:
	}
}
