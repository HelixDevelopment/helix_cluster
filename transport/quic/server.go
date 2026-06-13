package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync/atomic"

	quicgo "github.com/quic-go/quic-go"
)

// ErrMaxConnsReached is returned by Accept when the number of concurrently live
// accepted connections is already at the server's MaxConns cap. The offending
// connection is closed (CONNECTION_REFUSED) before Accept returns this error,
// so a peer flooding new connections cannot grow server memory past the cap.
var ErrMaxConnsReached = errors.New("quic server: max concurrent connections reached")

// connRefusedCode is the QUIC application error code sent when a connection is
// refused because the server is at its MaxConns cap.
const connRefusedCode quicgo.ApplicationErrorCode = 0x1

// QUICServer is a Helix federation QUIC listener bound to a single UDP socket.
//
// It uses ListenEarly so 0-RTT (early data) connections are accepted when
// Config.Allow0RTT is set. The underlying *quic.Transport owns the UDP
// PacketConn; closing the server releases it.
type QUICServer struct {
	cfg       Config
	transport *quicgo.Transport
	listener  *quicgo.EarlyListener
	udpConn   *net.UDPConn
	Stats     *ConnectionMigrationStats

	// maxConns is the resolved concurrent-connection cap (0 == unlimited).
	maxConns int
	// liveConns counts connections handed out by Accept and not yet torn down.
	// It is decremented automatically when a connection's context is cancelled.
	liveConns atomic.Int64
}

// LiveConns returns the number of accepted connections currently counted as
// live (handed out by Accept, not yet torn down). Exposed for tests/metrics.
func (s *QUICServer) LiveConns() int64 { return s.liveConns.Load() }

// MaxConns returns the resolved concurrent-connection cap (0 == unlimited).
func (s *QUICServer) MaxConns() int { return s.maxConns }

// NewQUICServer binds a UDP socket on addr (e.g. "127.0.0.1:0" for an
// ephemeral loopback port) and starts an early (0-RTT-capable) QUIC listener.
//
// The Config must carry a TLSConfig with at least one Certificate.
func NewQUICServer(addr string, cfg Config) (*QUICServer, error) {
	tlsConf := cfg.serverTLSConfig()
	if len(tlsConf.Certificates) == 0 && tlsConf.GetCertificate == nil {
		return nil, fmt.Errorf("quic server: TLS config has no certificate")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve udp addr %q: %w", addr, err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %q: %w", addr, err)
	}

	tr := &quicgo.Transport{Conn: udpConn}
	ln, err := tr.ListenEarly(tlsConf, cfg.quicConfig())
	if err != nil {
		_ = tr.Close()
		_ = udpConn.Close()
		return nil, fmt.Errorf("quic listen early: %w", err)
	}

	stats := &ConnectionMigrationStats{}
	return &QUICServer{
		cfg:       cfg,
		transport: tr,
		listener:  ln,
		udpConn:   udpConn,
		Stats:     stats,
		maxConns:  cfg.maxConns(),
	}, nil
}

// Addr returns the actual UDP address the server is bound to (resolves :0).
func (s *QUICServer) Addr() net.Addr {
	return s.udpConn.LocalAddr()
}

// TLSConfig returns the effective server TLS config (post ALPN/MinVersion
// normalization). Exposed for assertions in tests.
func (s *QUICServer) TLSConfig() *tls.Config { return s.cfg.serverTLSConfig() }

// Accept blocks for the next inbound QUIC connection. If the connection used
// 0-RTT, the server's stats are updated.
func (s *QUICServer) Accept(ctx context.Context) (*quicgo.Conn, error) {
	conn, err := s.listener.Accept(ctx)
	if err != nil {
		return nil, fmt.Errorf("accept: %w", err)
	}

	// Concurrent-connection cap (backpressure against goroutine/memory growth):
	// reserve a slot before handing the connection to the caller. If we are at
	// the cap, refuse and close the connection so a flood of new connections
	// cannot grow server-side resources without bound. Reserve optimistically
	// then roll back if over the cap, so the count never exceeds maxConns.
	if s.maxConns > 0 {
		if s.liveConns.Add(1) > int64(s.maxConns) {
			s.liveConns.Add(-1)
			_ = conn.CloseWithError(connRefusedCode, "server at max connections")
			return nil, ErrMaxConnsReached
		}
		// Release the reserved slot when the connection is torn down.
		go func(c *quicgo.Conn) {
			<-c.Context().Done()
			s.liveConns.Add(-1)
		}(conn)
	}

	// Wait for the handshake to complete so ConnectionState() is authoritative.
	select {
	case <-conn.HandshakeComplete():
		if conn.ConnectionState().Used0RTT {
			s.Stats.Record0RTTResume()
		}
	case <-ctx.Done():
		// The decrement goroutine (if any) will fire when the conn context is
		// cancelled; ensure that happens by closing the connection here.
		if s.maxConns > 0 {
			_ = conn.CloseWithError(connRefusedCode, "accept context cancelled")
		}
		return nil, ctx.Err()
	}
	return conn, nil
}

// Close shuts the listener, transport and UDP socket down.
func (s *QUICServer) Close() error {
	var firstErr error
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			firstErr = err
		}
	}
	if s.transport != nil {
		if err := s.transport.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.udpConn != nil {
		if err := s.udpConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
