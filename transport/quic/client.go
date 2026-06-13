package quic

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	quicgo "github.com/quic-go/quic-go"
)

// QUICClient dials Helix federation QUIC connections from a single local UDP
// socket via a *quic.Transport. Using a Transport (rather than DialAddr) is
// what makes real connection migration (Conn.AddPath) possible.
//
// A shared tls.Config (with a persistent ClientSessionCache) and a shared
// quic TokenStore are reused across dials so a second dial can perform real
// 0-RTT resumption.
type QUICClient struct {
	cfg       Config
	tlsConf   *tls.Config
	quicConf  *quicgo.Config
	transport *quicgo.Transport
	udpConn   *net.UDPConn

	// retired holds transports/sockets that previously carried an active
	// connection. They MUST stay open while that connection lives (quic-go
	// keeps connection IDs registered on the prior path's transport), so we
	// keep them around and close them only at client Close().
	retiredTransports []*quicgo.Transport
	retiredConns      []*net.UDPConn

	Stats *ConnectionMigrationStats
}

// NewQUICClient creates a client bound to a local ephemeral UDP socket
// (127.0.0.1:0). The session cache and token store live for the life of the
// client so 0-RTT resumption works on the second Dial.
func NewQUICClient(cfg Config) (*QUICClient, error) {
	if cfg.TokenStore == nil {
		cfg.TokenStore = quicgo.NewLRUTokenStore(8, 16)
	}
	tlsConf := cfg.clientTLSConfig()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("client listen udp: %w", err)
	}
	tr := &quicgo.Transport{Conn: udpConn}

	return &QUICClient{
		cfg:       cfg,
		tlsConf:   tlsConf,
		quicConf:  cfg.quicConfig(),
		transport: tr,
		udpConn:   udpConn,
		Stats:     &ConnectionMigrationStats{},
	}, nil
}

// LocalAddr returns the client's current local UDP address.
func (c *QUICClient) LocalAddr() net.Addr { return c.udpConn.LocalAddr() }

// Transport exposes the underlying quic Transport (needed by callers that
// orchestrate migration onto a brand-new transport).
func (c *QUICClient) Transport() *quicgo.Transport { return c.transport }

// Dial establishes a QUIC connection to addr using DialEarly. When
// Config.Allow0RTT is set and a session ticket + token are available from a
// previous connection to the same server, this attempt resumes via 0-RTT.
//
// The returned bool reports whether 0-RTT early data was actually used
// (authoritative, read after the handshake completes), and the stats are
// updated accordingly.
func (c *QUICClient) Dial(ctx context.Context, addr net.Addr) (*quicgo.Conn, bool, error) {
	conn, err := c.transport.DialEarly(ctx, addr, c.tlsConf, c.quicConf)
	if err != nil {
		return nil, false, fmt.Errorf("dial early %s: %w", addr, err)
	}
	select {
	case <-conn.HandshakeComplete():
	case <-ctx.Done():
		_ = conn.CloseWithError(0, "handshake-timeout")
		return nil, false, ctx.Err()
	}

	used0RTT := conn.ConnectionState().Used0RTT
	if used0RTT {
		c.Stats.Record0RTTResume()
	}
	// A completed handshake means the server issued (at least one) session
	// ticket we can resume from next time.
	c.Stats.RecordSessionTicket()
	return conn, used0RTT, nil
}

// MigrateToNewSocket performs a real RFC 9000 connection migration of conn onto
// a freshly bound local UDP socket (a new *quic.Transport). It probes the new
// path (PATH_CHALLENGE/PATH_RESPONSE) and, once validated, switches the
// connection to it so subsequent packets flow from the new local address while
// the QUIC connection — and any open streams — stay intact.
//
// On success the client's underlying transport/socket is replaced with the new
// one (the old socket is closed) and stats are updated. The caller's existing
// streams remain usable.
func (c *QUICClient) MigrateToNewSocket(ctx context.Context, conn *quicgo.Conn) (net.Addr, error) {
	newUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("migrate: bind new udp socket: %w", err)
	}
	newTr := &quicgo.Transport{Conn: newUDP}

	path, err := conn.AddPath(newTr)
	if err != nil {
		_ = newTr.Close()
		_ = newUDP.Close()
		return nil, fmt.Errorf("migrate: add path: %w", err)
	}

	c.Stats.RecordMigrationProbe()
	if err := path.Probe(ctx); err != nil {
		_ = path.Close()
		_ = newTr.Close()
		_ = newUDP.Close()
		return nil, fmt.Errorf("migrate: probe path: %w", err)
	}

	if err := path.Switch(); err != nil {
		_ = path.Close()
		_ = newTr.Close()
		_ = newUDP.Close()
		return nil, fmt.Errorf("migrate: switch path: %w", err)
	}
	c.Stats.RecordMigrationSwitch()

	// Adopt the new socket/transport. The OLD transport/socket must remain open
	// for the lifetime of the connection — quic-go still has connection IDs
	// registered there and abruptly closing it would tear the connection down.
	// We retire it and close it only when the client itself is closed.
	oldTr, oldUDP := c.transport, c.udpConn
	newAddr := newUDP.LocalAddr()
	c.transport, c.udpConn = newTr, newUDP
	if oldTr != nil {
		c.retiredTransports = append(c.retiredTransports, oldTr)
	}
	if oldUDP != nil {
		c.retiredConns = append(c.retiredConns, oldUDP)
	}
	return newAddr, nil
}

// Close releases the client's active transport/socket plus any retired
// (pre-migration) transports/sockets still held open for live connections.
func (c *QUICClient) Close() error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if c.transport != nil {
		record(c.transport.Close())
	}
	if c.udpConn != nil {
		record(c.udpConn.Close())
	}
	for _, tr := range c.retiredTransports {
		record(tr.Close())
	}
	for _, u := range c.retiredConns {
		record(u.Close())
	}
	return firstErr
}
