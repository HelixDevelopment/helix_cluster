package gateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	quic "github.com/HelixDevelopment/helix_cluster/transport/quic"
	quicgo "github.com/quic-go/quic-go"
)

// QUIC transport adapter (HXC-1186 integration).
//
// This file plugs the real quic-go transport (transport/quic) into the
// gateway behind the Transport/Conn interfaces, so an edge endpoint that
// speaks QUIC can address — and be addressed by — endpoints on any other
// transport (WebSocket, loopback, MQTT) with no special-casing in the routing
// core. Every hop here is a real QUIC connection over a real UDP socket
// negotiated through a real TLS 1.3 handshake; there are no in-memory shortcuts.
//
// Wire framing
//
// The gateway model is the Envelope (JSON). QUIC gives ordered byte streams,
// so the adapter frames each envelope as a 4-byte big-endian length prefix
// followed by that many JSON bytes. Both directions share one bidirectional
// QUIC stream per connection:
//
//	[uint32 len][JSON envelope bytes]...
//
// The very first frame a client sends is a "hello" envelope whose Source is
// the client's node id; the server adapter reads it to learn the node id under
// which to register the connection, then continues reading subsequent frames
// as routed inbound envelopes. This mirrors how the WebSocket adapter learns
// the node id from the upgrade query parameter.

// quicFrameMaxLen bounds a single envelope frame so a corrupt/hostile length
// prefix cannot make the adapter allocate unbounded memory.
const quicFrameMaxLen = 8 << 20 // 8 MiB

// encodeHello serializes a hello frame declaring nodeID. It is a minimal JSON
// object {"node":"<id>"} kept separate from the Envelope codec so the synthetic
// hello never passes through envelope validation/routing.
func encodeHello(nodeID string) []byte {
	b, _ := json.Marshal(helloFrame{Node: nodeID})
	return b
}

// decodeHello parses a hello frame, returning the declared node id.
func decodeHello(b []byte) (string, error) {
	var h helloFrame
	if err := json.Unmarshal(b, &h); err != nil {
		return "", fmt.Errorf("gateway/quic: decode hello: %w", err)
	}
	if h.Node == "" {
		return "", fmt.Errorf("gateway/quic: hello has no node id")
	}
	return h.Node, nil
}

// helloFrame is the QUIC handshake frame a client sends first to declare its
// node identity to the server adapter.
type helloFrame struct {
	Node string `json:"node"`
}

// writeFrame writes one length-prefixed envelope frame to w.
func writeFrame(w io.Writer, env Envelope) error {
	b, err := EncodeEnvelope(env)
	if err != nil {
		return err
	}
	if len(b) > quicFrameMaxLen {
		return fmt.Errorf("gateway/quic: envelope %q too large (%d bytes)", env.ID, len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// readFrame reads one length-prefixed envelope frame from r.
func readFrame(r io.Reader) (Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Envelope{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > quicFrameMaxLen {
		return Envelope{}, fmt.Errorf("gateway/quic: invalid frame length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Envelope{}, err
	}
	return DecodeEnvelope(buf)
}

// writeRawFrame writes a length-prefixed frame WITHOUT envelope validation. It
// is used only for the hello frame, whose synthetic id/kind would otherwise be
// rejected by Validate. Routed traffic always uses writeFrame.
func writeRawFrame(w io.Writer, b []byte) error {
	if len(b) > quicFrameMaxLen {
		return fmt.Errorf("gateway/quic: hello frame too large (%d bytes)", len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readRawFrame reads a length-prefixed frame as raw bytes (no envelope
// validation). Used for the hello frame.
func readRawFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > quicFrameMaxLen {
		return nil, fmt.Errorf("gateway/quic: invalid hello length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// QUICTransport is a real QUIC server transport for the gateway. It owns a
// quic.QUICServer (a single UDP socket + TLS 1.3 early listener) and, for each
// accepted connection, opens the per-connection bidirectional stream, reads the
// client's hello to learn its node id, registers a quicConn with the gateway,
// and pumps frames in both directions.
type QUICTransport struct {
	gw  *Gateway
	srv *quic.QUICServer

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	conns  map[*quicConn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// NewQUICTransport binds a real QUIC server on addr (e.g. "127.0.0.1:0" for an
// ephemeral loopback port) using cfg, registers the transport with the gateway
// for lifecycle ownership, and starts accepting connections in the background.
//
// The cfg must carry a TLSConfig with at least one certificate (see
// quic.GenerateSelfSignedCert for tests).
func NewQUICTransport(gw *Gateway, addr string, cfg quic.Config) (*QUICTransport, error) {
	srv, err := quic.NewQUICServer(addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("gateway/quic: start server: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &QUICTransport{
		gw:     gw,
		srv:    srv,
		ctx:    ctx,
		cancel: cancel,
		conns:  make(map[*quicConn]struct{}),
	}
	gw.AddTransport(t)
	t.wg.Add(1)
	go t.acceptLoop()
	return t, nil
}

// Name implements Transport.
func (t *QUICTransport) Name() string { return "quic" }

// Addr returns the actual UDP address the QUIC server is bound to (resolves :0).
func (t *QUICTransport) Addr() net.Addr { return t.srv.Addr() }

// acceptLoop accepts QUIC connections until the transport is closed.
func (t *QUICTransport) acceptLoop() {
	defer t.wg.Done()
	for {
		conn, err := t.srv.Accept(t.ctx)
		if err != nil {
			// Context cancelled (close) or listener closed: stop accepting.
			return
		}
		t.wg.Add(1)
		go t.serveConn(conn)
	}
}

// serveConn handles one accepted QUIC connection: it accepts the per-connection
// bidirectional stream, reads the hello to learn the node id, registers a
// quicConn, and runs the read pump.
func (t *QUICTransport) serveConn(raw *quicgo.Conn) {
	defer t.wg.Done()

	stream, err := raw.AcceptStream(t.ctx)
	if err != nil {
		_ = raw.CloseWithError(0, "no-stream")
		return
	}

	helloBytes, err := readRawFrame(stream)
	if err != nil {
		_ = raw.CloseWithError(0, "no-hello")
		return
	}
	hello, err := decodeHello(helloBytes)
	if err != nil || hello == "" {
		_ = raw.CloseWithError(0, "bad-hello")
		return
	}

	c := &quicConn{
		t:      t,
		raw:    raw,
		stream: stream,
		nodeID: hello,
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = raw.CloseWithError(0, "closing")
		return
	}
	t.conns[c] = struct{}{}
	t.mu.Unlock()

	if err := t.gw.Register(c); err != nil {
		t.dropConn(c)
		_ = raw.CloseWithError(0, "register-failed")
		return
	}

	c.readPump()
}

func (t *QUICTransport) dropConn(c *quicConn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
	t.gw.Unregister(c)
}

// Close stops accepting new connections, closes all open QUIC connections, and
// shuts the UDP socket down. Safe to call once.
func (t *QUICTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conns := make([]*quicConn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.conns = make(map[*quicConn]struct{})
	t.mu.Unlock()

	t.cancel()
	for _, c := range conns {
		t.gw.Unregister(c)
		_ = c.raw.CloseWithError(0, "transport-close")
	}
	err := t.srv.Close()
	t.wg.Wait()
	return err
}

// quicConn is one server-side QUIC connection. It implements Conn: Send writes
// a routed envelope to the connection's stream; the read pump decodes inbound
// frames into envelopes and routes them through the gateway.
type quicConn struct {
	t      *QUICTransport
	raw    *quicgo.Conn
	stream *quicgo.Stream
	nodeID string

	writeMu sync.Mutex
}

// NodeID implements Conn.
func (c *quicConn) NodeID() string { return c.nodeID }

// Send implements Conn: it frames and writes the envelope to the QUIC stream.
// Writes are serialized because a quic-go stream is not safe for concurrent
// writers.
func (c *quicConn) Send(env Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return writeFrame(c.stream, env)
}

// readPump reads frames until the stream closes, decoding each into an Envelope
// and routing it. On exit it unregisters the connection.
func (c *quicConn) readPump() {
	defer func() {
		c.t.dropConn(c)
		_ = c.raw.CloseWithError(0, "read-pump-exit")
	}()
	for {
		env, err := readFrame(c.stream)
		if err != nil {
			return
		}
		if env.Source == "" {
			env.Source = c.nodeID
		}
		_ = c.t.gw.Route(env)
	}
}

// QUICClientConn is a real QUIC client for connecting to a QUICTransport's
// server. It is a thin wrapper used by integration callers and tests to dial a
// real QUIC connection, declare a node id, send envelopes, and receive routed
// envelopes — the QUIC analogue of WSClient.
type QUICClientConn struct {
	cli    *quic.QUICClient
	raw    *quicgo.Conn
	stream *quicgo.Stream
	nodeID string

	writeMu sync.Mutex
}

// DialQUIC dials the gateway QUIC server at addr for the given node id using
// cfg, opens the per-connection bidirectional stream, and sends the hello frame
// declaring nodeID. After this returns the connection is registered server-side
// and ready to send/receive routed envelopes.
func DialQUIC(ctx context.Context, addr net.Addr, nodeID string, cfg quic.Config) (*QUICClientConn, error) {
	cli, err := quic.NewQUICClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("gateway/quic: new client: %w", err)
	}
	raw, _, err := cli.Dial(ctx, addr)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("gateway/quic: dial: %w", err)
	}
	stream, err := raw.OpenStreamSync(ctx)
	if err != nil {
		_ = raw.CloseWithError(0, "open-stream-failed")
		_ = cli.Close()
		return nil, fmt.Errorf("gateway/quic: open stream: %w", err)
	}
	if err := writeRawFrame(stream, encodeHello(nodeID)); err != nil {
		_ = raw.CloseWithError(0, "hello-failed")
		_ = cli.Close()
		return nil, fmt.Errorf("gateway/quic: send hello: %w", err)
	}
	return &QUICClientConn{
		cli:    cli,
		raw:    raw,
		stream: stream,
		nodeID: nodeID,
	}, nil
}

// Send frames and writes an envelope to the server over the QUIC stream.
func (c *QUICClientConn) Send(env Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return writeFrame(c.stream, env)
}

// Recv blocks until one routed envelope is received from the server or the
// deadline elapses, decoding the frame into an Envelope.
func (c *QUICClientConn) Recv(timeout time.Duration) (Envelope, error) {
	_ = c.stream.SetReadDeadline(time.Now().Add(timeout))
	return readFrame(c.stream)
}

// Close closes the QUIC client connection and its underlying socket.
func (c *QUICClientConn) Close() error {
	_ = c.raw.CloseWithError(0, "client-close")
	return c.cli.Close()
}
