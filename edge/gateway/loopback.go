package gateway

import "sync"

// LoopbackTransport is an in-process transport with no sockets. It is used to
// attach in-memory endpoints (e.g. a co-located scheduler or a test harness)
// to the gateway alongside real network transports. Because the gateway is
// transport-agnostic, an envelope sent by a WebSocket client can be delivered
// to a loopback endpoint and vice versa, with no special-casing in the core.
type LoopbackTransport struct {
	gw     *Gateway
	mu     sync.Mutex
	conns  []*LoopbackConn
	closed bool
}

// NewLoopbackTransport creates a loopback transport bound to a gateway.
func NewLoopbackTransport(gw *Gateway) *LoopbackTransport {
	t := &LoopbackTransport{gw: gw}
	gw.AddTransport(t)
	return t
}

// Name implements Transport.
func (t *LoopbackTransport) Name() string { return "loopback" }

// Connect creates an in-process endpoint for nodeID, registers it with the
// gateway, and returns a LoopbackConn. Envelopes routed to nodeID are delivered
// to the returned conn's Inbox channel. Envelopes the endpoint wants to send
// into the gateway go through LoopbackConn.Route.
func (t *LoopbackTransport) Connect(nodeID string) (*LoopbackConn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	c := &LoopbackConn{
		gw:     t.gw,
		nodeID: nodeID,
		Inbox:  make(chan Envelope, 16),
	}
	t.conns = append(t.conns, c)
	t.mu.Unlock()

	if err := t.gw.Register(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Close unregisters and closes every loopback connection. Safe to call once.
func (t *LoopbackTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conns := t.conns
	t.conns = nil
	t.mu.Unlock()

	for _, c := range conns {
		t.gw.Unregister(c)
		c.close()
	}
	return nil
}

// LoopbackConn is a single in-process endpoint. Delivered envelopes arrive on
// Inbox; the endpoint pushes outbound envelopes into the gateway via Route.
type LoopbackConn struct {
	gw     *Gateway
	nodeID string
	// Inbox receives envelopes the gateway routes to this endpoint.
	Inbox chan Envelope

	mu     sync.Mutex
	closed bool
}

// NodeID implements Conn.
func (c *LoopbackConn) NodeID() string { return c.nodeID }

// Send implements Conn: it delivers a routed envelope to this endpoint's Inbox.
// The Envelope carries an immutable byte slice (json.RawMessage) which is not
// reused by the gateway, so the receiver observes byte-identical payload.
func (c *LoopbackConn) Send(env Envelope) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.mu.Unlock()
	c.Inbox <- env
	return nil
}

// Route pushes an envelope from this endpoint into the gateway for routing to
// its destination on any transport.
func (c *LoopbackConn) Route(env Envelope) error {
	return c.gw.Route(env)
}

func (c *LoopbackConn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.Inbox)
}
