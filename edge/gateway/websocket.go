package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// nodeIDQueryParam is the query parameter a WebSocket client uses to declare
// the node identity its connection serves, e.g. ws://host/edge?node=node-A.
const nodeIDQueryParam = "node"

// Unauthenticated-transport caveat (analogous to the CoAP DTLS note).
//
// The WebSocket transport accepts upgrades with CheckOrigin returning true and
// performs NO authentication of its own — it trusts a fronting auth layer to
// gate who may reach this endpoint. When the gateway is exposed without that
// fronting layer, ANY client can open a connection and declare an arbitrary
// node id via the ?node= query parameter. The DoS guards below (connection cap,
// distinct-node cap, per-message read limit, idle reaping, and bounded send
// queues) bound the resource cost of such unauthenticated connections so a flood
// cannot exhaust memory or wedge routing, but they are NOT a substitute for
// authentication: an unauthenticated peer can still occupy a slot up to the cap
// and register/observe node ids. Operators MUST run this transport behind an
// authenticating/authorizing front end (or add per-connection auth) before
// exposing it to untrusted networks.
//
// WebSocket DoS / resource-exhaustion guards.
//
// gorilla/websocket applies NO read limit by default, so without an explicit
// SetReadLimit a single hostile frame can drive an unbounded allocation and
// OOM the gateway. It also performs no idle reaping, so a peer that opens a
// socket and then sends nothing pins a goroutine and a registry slot forever
// (slow-loris). These constants bound both vectors:
const (
	// wsMaxMessageBytes caps a single inbound WebSocket message. A frame larger
	// than this makes ReadMessage return a close error (the read pump then tears
	// the connection down) instead of allocating the whole payload. It matches
	// the QUIC adapter's 8 MiB frame cap so envelope size is bounded uniformly
	// across transports.
	wsMaxMessageBytes = 8 << 20 // 8 MiB

	// wsReadIdleTimeout is the maximum time a connection may be silent before it
	// is reaped. The read deadline is refreshed on every received frame and on
	// every pong, so a live peer (which answers our pings) is never dropped while
	// a dead/stalled one is closed rather than accumulated.
	wsReadIdleTimeout = 90 * time.Second

	// wsPingInterval is how often the server sends a ping to keep a live peer's
	// read deadline fresh. It is comfortably below wsReadIdleTimeout so a healthy
	// connection always refreshes before the idle deadline elapses.
	wsPingInterval = 30 * time.Second

	// wsWriteTimeout bounds a single socket write (data or control frame) so a
	// stalled consumer cannot block a writer indefinitely.
	wsWriteTimeout = 10 * time.Second
)

// WebSocketTransport is a real WebSocket server transport. It exposes an
// http.Handler that upgrades inbound HTTP requests to WebSocket connections,
// registers each upgraded connection with the gateway under the node id from
// the request, and pumps frames in both directions:
//
//   - inbound text/binary frames are decoded to Envelopes and handed to
//     Gateway.Route;
//   - envelopes the gateway routes to this connection are encoded and written
//     to the socket.
//
// It uses real network sockets (the standard gorilla/websocket upgrader); there
// are no in-memory shortcuts on the WebSocket path.
type WebSocketTransport struct {
	gw       *Gateway
	upgrader websocket.Upgrader

	mu     sync.Mutex
	conns  map[*wsConn]struct{}
	closed bool

	// testReadIdleOverride, when > 0, replaces wsReadIdleTimeout for connections
	// served by this transport. It exists ONLY to let the slow-loris test drive
	// the real reaping path on a short, deterministic deadline instead of the
	// 90s production constant; production code never sets it, so the default
	// behavior is unchanged.
	testReadIdleOverride time.Duration
}

// readIdleTimeout returns the effective idle read timeout for this transport,
// honoring the test override when set.
func (t *WebSocketTransport) readIdleTimeout() time.Duration {
	if t.testReadIdleOverride > 0 {
		return t.testReadIdleOverride
	}
	return wsReadIdleTimeout
}

// NewWebSocketTransport creates a WebSocket transport bound to a gateway and
// registers it for lifecycle ownership.
func NewWebSocketTransport(gw *Gateway) *WebSocketTransport {
	t := &WebSocketTransport{
		gw:    gw,
		conns: make(map[*wsConn]struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// The gateway is an internal cluster component; cross-origin checks
			// are handled by the fronting auth layer, so accept the upgrade here.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	gw.AddTransport(t)
	return t
}

// Name implements Transport.
func (t *WebSocketTransport) Name() string { return "websocket" }

// Handler returns the http.Handler that upgrades and serves WebSocket
// connections. Mount it on a real net.Listener (e.g. via http.Server or
// httptest.NewServer) to accept real socket connections.
func (t *WebSocketTransport) Handler() http.Handler {
	return http.HandlerFunc(t.serve)
}

func (t *WebSocketTransport) serve(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get(nodeIDQueryParam)
	if nodeID == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}
	raw, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		return
	}

	c := &wsConn{
		t:      t,
		raw:    raw,
		nodeID: nodeID,
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = raw.Close()
		return
	}
	t.conns[c] = struct{}{}
	t.mu.Unlock()

	if err := t.gw.Register(c); err != nil {
		// Registry is full (ErrTooManyConns / ErrTooManyNodes) or the gateway is
		// closing: refuse the connection by sending a WebSocket close frame with a
		// "try again later" status and tearing the socket down, rather than leaking
		// a registry slot. The peer learns it was rejected instead of silently
		// hanging.
		t.mu.Lock()
		delete(t.conns, c)
		t.mu.Unlock()
		_ = c.writeControlClose(websocket.CloseTryAgainLater, err.Error())
		_ = raw.Close()
		return
	}

	// Block in the read pump for the lifetime of the connection. The HTTP
	// server runs this handler in its own goroutine, so blocking here is the
	// correct per-connection model.
	c.readPump()
}

func (t *WebSocketTransport) dropConn(c *wsConn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
	t.gw.Unregister(c)
}

// Close stops accepting new connections and closes all open sockets. Safe to
// call once.
func (t *WebSocketTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conns := make([]*wsConn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.conns = make(map[*wsConn]struct{})
	t.mu.Unlock()

	for _, c := range conns {
		t.gw.Unregister(c)
		_ = c.raw.Close()
	}
	return nil
}

// wsConn is one upgraded server-side WebSocket connection. It implements Conn:
// Send writes a routed envelope to the socket; the read pump decodes inbound
// frames into envelopes and routes them.
type wsConn struct {
	t      *WebSocketTransport
	raw    *websocket.Conn
	nodeID string

	writeMu sync.Mutex
}

// NodeID implements Conn.
func (c *wsConn) NodeID() string { return c.nodeID }

// Send implements Conn: it encodes the envelope and writes it as a single text
// frame. Writes are serialized because gorilla/websocket forbids concurrent
// writers on one connection.
func (c *wsConn) Send(env Envelope) error {
	b, err := EncodeEnvelope(env)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.raw.WriteMessage(websocket.TextMessage, b)
}

// writeControlClose sends a WebSocket close control frame with the given status
// code and reason, serialized against data writes. Best-effort: a write error
// is returned but the caller closes the socket regardless.
func (c *wsConn) writeControlClose(code int, reason string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	msg := websocket.FormatCloseMessage(code, reason)
	return c.raw.WriteControl(websocket.CloseMessage, msg, time.Now().Add(wsWriteTimeout))
}

// pingLoop periodically pings the peer to keep a live connection's read deadline
// fresh (each pong refreshes it via the pong handler). It exits when done is
// closed (the read pump returned). Ping writes go through writeMu so they never
// race a data write, which gorilla/websocket forbids.
func (c *wsConn) pingLoop(done <-chan struct{}) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.writeMu.Lock()
			err := c.raw.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteTimeout))
			c.writeMu.Unlock()
			if err != nil {
				// A failed ping means the socket is gone; the read pump will observe
				// the read error and tear down. Stop pinging.
				return
			}
		}
	}
}

// readPump reads frames until the socket closes, decoding each into an Envelope
// and routing it through the gateway. On exit it unregisters the connection.
//
// It installs the DoS guards for the WebSocket path:
//   - SetReadLimit bounds a single inbound message so an oversized frame closes
//     the connection instead of allocating unbounded memory;
//   - a read deadline (refreshed per frame and per pong) reaps a silent/stalled
//     peer instead of pinning it forever;
//   - a ping loop keeps a genuinely live peer's deadline fresh.
func (c *wsConn) readPump() {
	done := make(chan struct{})
	defer func() {
		close(done)
		c.t.dropConn(c)
		_ = c.raw.Close()
	}()

	idle := c.t.readIdleTimeout()
	c.raw.SetReadLimit(wsMaxMessageBytes)
	_ = c.raw.SetReadDeadline(time.Now().Add(idle))
	c.raw.SetPongHandler(func(string) error {
		// A live peer answered our ping: extend its idle deadline.
		return c.raw.SetReadDeadline(time.Now().Add(idle))
	})

	go c.pingLoop(done)

	for {
		_, data, err := c.raw.ReadMessage()
		if err != nil {
			return
		}
		// A frame arrived from a live peer: refresh the idle deadline.
		_ = c.raw.SetReadDeadline(time.Now().Add(idle))
		env, err := DecodeEnvelope(data)
		if err != nil {
			// A malformed frame is dropped without tearing down the socket; the
			// peer may recover. The error is intentionally not fatal.
			continue
		}
		// If the frame omitted Source, stamp it from the connection's node id so
		// status fan-back can exclude the originator.
		if env.Source == "" {
			env.Source = c.nodeID
		}
		_ = c.t.gw.Route(env)
	}
}

// WSClient is a real WebSocket client for connecting to a WebSocketTransport's
// server. It is a thin, dependency-light wrapper used by integration callers and
// tests to dial real sockets, send envelopes, and receive routed envelopes.
type WSClient struct {
	raw    *websocket.Conn
	nodeID string
}

// DialWS dials the gateway WebSocket server at url for the given node id. The
// url must be a ws:// or wss:// URL pointing at the transport's Handler; the
// node id is appended as the node query parameter.
func DialWS(ctx context.Context, url, nodeID string) (*WSClient, error) {
	sep := "?"
	for i := 0; i < len(url); i++ {
		if url[i] == '?' {
			sep = "&"
			break
		}
	}
	full := fmt.Sprintf("%s%s%s=%s", url, sep, nodeIDQueryParam, nodeID)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	raw, _, err := dialer.DialContext(ctx, full, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway: dial websocket: %w", err)
	}
	return &WSClient{raw: raw, nodeID: nodeID}, nil
}

// Send encodes and writes an envelope to the server as a single text frame.
func (c *WSClient) Send(env Envelope) error {
	b, err := EncodeEnvelope(env)
	if err != nil {
		return err
	}
	_ = c.raw.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.raw.WriteMessage(websocket.TextMessage, b)
}

// Recv blocks until one envelope is received from the server or the deadline
// elapses. It decodes the frame into an Envelope.
func (c *WSClient) Recv(timeout time.Duration) (Envelope, error) {
	_ = c.raw.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := c.raw.ReadMessage()
	if err != nil {
		return Envelope{}, fmt.Errorf("gateway: recv: %w", err)
	}
	return DecodeEnvelope(data)
}

// Close closes the client socket.
func (c *WSClient) Close() error { return c.raw.Close() }
