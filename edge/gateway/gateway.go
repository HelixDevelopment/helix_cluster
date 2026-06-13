package gateway

import (
	"errors"
	"strings"
	"sync"
)

// ErrNoRoute is returned when a unicast (dispatch) envelope addresses a node
// that has no connection registered with the gateway. It is surfaced honestly
// to the caller rather than silently dropping the work item.
var ErrNoRoute = errors.New("gateway: no route to destination node")

// ErrClosed is returned by gateway operations after the gateway is closed.
var ErrClosed = errors.New("gateway: closed")

// Conn is a single registered connection on a transport. It represents one
// reachable endpoint (typically one edge node, or one observer of a node's
// status). A transport creates a Conn when an endpoint attaches and hands it to
// the gateway via Register; the gateway calls Send to deliver routed envelopes.
type Conn interface {
	// NodeID is the identity this connection serves. For a dispatch the gateway
	// routes to the Conn(s) whose NodeID matches the destination; for status it
	// fans to observers. Multiple Conns may share a NodeID (e.g. a node plus its
	// observers); routing isolation is by exact node-id match.
	NodeID() string
	// Send delivers a routed envelope to this connection's underlying transport.
	// It must copy or fully consume the payload before returning; the gateway
	// reuses no buffers, so byte-identity is preserved end to end.
	Send(Envelope) error
}

// Transport is the protocol-agnostic surface the gateway routes across. A
// concrete transport (WebSocket server, in-process loopback, or a future
// MQTT/QUIC adapter) accepts connections in its own way and, for each attached
// endpoint, registers a Conn with the gateway and forwards inbound frames to
// the gateway as Envelopes via Gateway.Route.
//
// The interface is deliberately minimal: the gateway drives nothing on the
// transport except Name (for diagnostics) and Close (for shutdown). Inbound
// flow is push-based — transports call Gateway.Route — which is what lets the
// gateway stay free of any per-protocol polling logic.
type Transport interface {
	// Name identifies the transport for logging/metrics, e.g. "websocket".
	Name() string
	// Close releases the transport's resources (listeners, sockets). It must be
	// safe to call once; subsequent calls return nil.
	Close() error
}

// routingTable maps a node id to the set of connections that serve it. It is
// the authoritative routing structure: a dispatch addressed to node A is
// delivered to exactly the Conns registered under A and to no other key, which
// is what guarantees node-to-node isolation.
type routingTable struct {
	mu     sync.RWMutex
	byNode map[string][]Conn
}

func newRoutingTable() *routingTable {
	return &routingTable{byNode: make(map[string][]Conn)}
}

func (t *routingTable) add(c Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := c.NodeID()
	t.byNode[id] = append(t.byNode[id], c)
}

func (t *routingTable) remove(c Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := c.NodeID()
	conns := t.byNode[id]
	for i, existing := range conns {
		if existing == c {
			conns = append(conns[:i], conns[i+1:]...)
			break
		}
	}
	if len(conns) == 0 {
		delete(t.byNode, id)
	} else {
		t.byNode[id] = conns
	}
}

// lookup returns a snapshot copy of the connections registered for a node id,
// so callers iterate without holding the lock (and without racing concurrent
// register/unregister).
func (t *routingTable) lookup(nodeID string) []Conn {
	t.mu.RLock()
	defer t.mu.RUnlock()
	conns := t.byNode[nodeID]
	if len(conns) == 0 {
		return nil
	}
	out := make([]Conn, len(conns))
	copy(out, conns)
	return out
}

// nodeFromTopic extracts the node id from a canonical topic of the form
// helix/edge/<node_id>/<segment>. It returns "" if the topic is not in that
// form, in which case the gateway falls back to the envelope's Dest field.
func nodeFromTopic(topic string) string {
	if !strings.HasPrefix(topic, topicRoot+topicSeparator) {
		return ""
	}
	rest := strings.TrimPrefix(topic, topicRoot+topicSeparator)
	parts := strings.Split(rest, topicSeparator)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// Gateway is the transport-agnostic routing core. It owns the routing table and
// fans envelopes from any transport to the destination connection(s) on any
// transport. It is safe for concurrent use.
type Gateway struct {
	table  *routingTable
	mu     sync.Mutex
	closed bool
	ts     []Transport
}

// New creates an empty Gateway with no transports registered.
func New() *Gateway {
	return &Gateway{table: newRoutingTable()}
}

// AddTransport registers a transport so the gateway closes it on shutdown. The
// transport drives inbound flow itself by calling Route; registration here is
// purely for lifecycle ownership.
func (g *Gateway) AddTransport(t Transport) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ts = append(g.ts, t)
}

// Register attaches a connection to the routing table. After registration,
// envelopes addressed to c.NodeID() are delivered to c.
func (g *Gateway) Register(c Conn) error {
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return ErrClosed
	}
	g.table.add(c)
	return nil
}

// Unregister detaches a connection from the routing table. It is idempotent.
func (g *Gateway) Unregister(c Conn) {
	g.table.remove(c)
}

// Route delivers an envelope according to its destination. This is the single
// choke point every transport calls for inbound frames, and it is where routing
// isolation is enforced:
//
//   - A dispatch is unicast: it is delivered ONLY to connections registered
//     under the destination node id. If none exist, ErrNoRoute is returned and
//     the work item is NOT delivered anywhere else. A dispatch for node A can
//     therefore never reach node B.
//   - A status with an explicit Dest is delivered to that node's observers; a
//     status with no Dest is fanned to all registered connections except the
//     originating source (status fan-back to observers).
func (g *Gateway) Route(env Envelope) error {
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := env.Validate(); err != nil {
		return err
	}

	dest := env.Dest
	if dest == "" {
		dest = nodeFromTopic(env.RouteTopic())
	}

	if env.Kind == KindStatus && dest == "" {
		return g.fanStatus(env)
	}

	targets := g.table.lookup(dest)
	if len(targets) == 0 {
		return ErrNoRoute
	}
	var firstErr error
	for _, c := range targets {
		if err := c.Send(env); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// fanStatus delivers an unaddressed status envelope to every registered
// connection except the one matching the source, so a node does not receive
// the echo of its own heartbeat.
func (g *Gateway) fanStatus(env Envelope) error {
	g.table.mu.RLock()
	var targets []Conn
	for nodeID, conns := range g.table.byNode {
		if nodeID == env.Source {
			continue
		}
		targets = append(targets, conns...)
	}
	g.table.mu.RUnlock()
	if len(targets) == 0 {
		return ErrNoRoute
	}
	var firstErr error
	for _, c := range targets {
		if err := c.Send(env); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close shuts the gateway down: it stops accepting routes/registrations and
// closes every registered transport. It is safe to call once.
func (g *Gateway) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	ts := g.ts
	g.ts = nil
	g.mu.Unlock()

	var firstErr error
	for _, t := range ts {
		if err := t.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
