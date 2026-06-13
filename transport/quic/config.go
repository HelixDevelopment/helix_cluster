package quic

import (
	"crypto/tls"
	"time"

	quicgo "github.com/quic-go/quic-go"
)

// ALPNHelixFederation is the single ALPN protocol negotiated by the Helix
// federation QUIC transport. A mismatch (peer offering a different value)
// fails the TLS handshake — this is the anti-bluff anchor for the transport.
const ALPNHelixFederation = "helix-federation/6.0"

// DefaultKeepAlivePeriod is the QUIC keep-alive PING period.
const DefaultKeepAlivePeriod = 15 * time.Second

// DefaultMaxIdleTimeout is the default idle timeout before a connection is
// closed when no packets are exchanged.
const DefaultMaxIdleTimeout = 30 * time.Second

// DefaultHandshakeIdleTimeout bounds the resources a half-open (handshaking)
// connection may hold: if the peer sends no packet within this window the
// connection attempt is aborted, and quic-go also aborts if the handshake does
// not complete within twice this value. Matches quic-go's own default but is
// pinned explicitly so it cannot silently widen.
const DefaultHandshakeIdleTimeout = 5 * time.Second

// Default{MaxIncomingStreams,MaxIncomingUniStreams} cap how many concurrent
// streams a single peer may open against us. quic-go defaults to 100 each;
// we pin an explicit (slightly higher but still bounded) value so the cap is
// part of this package's contract and provable by test rather than an implicit
// upstream default that could change between quic-go releases.
const (
	DefaultMaxIncomingStreams    = 256
	DefaultMaxIncomingUniStreams = 256
)

// DefaultMaxMessageBytes bounds a single length-prefixed inbound message read
// off a stream (see framing.go). A hostile/corrupt length prefix advertising a
// huge size must not make the reader allocate unbounded memory. 8 MiB matches
// the edge/gateway quicFrameMaxLen so framing is parity-consistent across the
// Helix QUIC transports.
const DefaultMaxMessageBytes = 8 << 20 // 8 MiB

// DefaultMaxConns bounds the number of concurrently live accepted connections a
// QUICServer will hold. An unauthenticated peer (or many) opening connections
// without bound is a memory/goroutine-exhaustion vector; past this cap Accept
// refuses (closes) the excess connection instead of returning it. Zero in the
// Config means "no extra cap" (rely on OS/quic-go limits); the server defaults
// it to this value so the guard is on out of the box.
const DefaultMaxConns = 1024

// Config holds the tunable parameters for both the QUIC server and client.
//
// Zero values fall back to the Default* constants where a non-zero value is
// required for correct operation (KeepAlivePeriod, MaxIdleTimeout, NextProtos).
type Config struct {
	// NextProtos is the ALPN list. When empty it defaults to
	// [ALPNHelixFederation]. Setting a different value is how the anti-bluff
	// mutation test forces a handshake failure.
	NextProtos []string

	// Allow0RTT enables 0-RTT session resumption (server side) and early data
	// (client side via DialEarly).
	Allow0RTT bool

	// EnableDatagrams enables the unreliable QUIC DATAGRAM extension (RFC 9221).
	EnableDatagrams bool

	// MaxIdleTimeout is the idle timeout. Zero -> DefaultMaxIdleTimeout.
	MaxIdleTimeout time.Duration

	// HandshakeIdleTimeout bounds half-open/handshaking connections.
	// Zero -> DefaultHandshakeIdleTimeout.
	HandshakeIdleTimeout time.Duration

	// KeepAlivePeriod is the keep-alive PING period. Zero -> DefaultKeepAlivePeriod.
	KeepAlivePeriod time.Duration

	// MaxIncomingStreams caps concurrent bidirectional streams a peer may open.
	// Zero -> DefaultMaxIncomingStreams. A negative value disallows bidi streams
	// entirely (quic-go semantics) and is honored verbatim.
	MaxIncomingStreams int64

	// MaxIncomingUniStreams caps concurrent unidirectional streams a peer may
	// open. Zero -> DefaultMaxIncomingUniStreams. Negative disallows uni streams.
	MaxIncomingUniStreams int64

	// MaxMessageBytes caps a single length-prefixed message read off a stream
	// (framing.go). Zero -> DefaultMaxMessageBytes. A value <= 0 after defaulting
	// is treated as the default; there is no "unlimited" escape hatch.
	MaxMessageBytes int

	// MaxConns caps concurrently live accepted server connections. Zero ->
	// DefaultMaxConns (applied by the server). A negative value disables the
	// extra accept-side cap (rely solely on OS/quic-go limits).
	MaxConns int

	// TLSConfig is the base TLS config. The transport clones it and forces
	// NextProtos + (for the client) a ClientSessionCache so 0-RTT can resume.
	// MinVersion is pinned to TLS 1.3 (mandatory for QUIC).
	TLSConfig *tls.Config

	// TokenStore is the client-side QUIC address-validation token store.
	// It is required (alongside a TLS ClientSessionCache) for real 0-RTT.
	// When nil, the client transport allocates an in-memory store.
	TokenStore quicgo.TokenStore
}

// nextProtos returns the effective ALPN list, defaulting to the Helix protocol.
func (c *Config) nextProtos() []string {
	if len(c.NextProtos) > 0 {
		return c.NextProtos
	}
	return []string{ALPNHelixFederation}
}

func (c *Config) keepAlive() time.Duration {
	if c.KeepAlivePeriod > 0 {
		return c.KeepAlivePeriod
	}
	return DefaultKeepAlivePeriod
}

func (c *Config) maxIdle() time.Duration {
	if c.MaxIdleTimeout > 0 {
		return c.MaxIdleTimeout
	}
	return DefaultMaxIdleTimeout
}

func (c *Config) handshakeIdle() time.Duration {
	if c.HandshakeIdleTimeout > 0 {
		return c.HandshakeIdleTimeout
	}
	return DefaultHandshakeIdleTimeout
}

// maxIncomingStreams resolves the bidi stream cap. A negative configured value
// is honored (quic-go reads it as "disallow"), only the zero value defaults.
func (c *Config) maxIncomingStreams() int64 {
	if c.MaxIncomingStreams != 0 {
		return c.MaxIncomingStreams
	}
	return DefaultMaxIncomingStreams
}

func (c *Config) maxIncomingUniStreams() int64 {
	if c.MaxIncomingUniStreams != 0 {
		return c.MaxIncomingUniStreams
	}
	return DefaultMaxIncomingUniStreams
}

// maxMessageBytes resolves the framed-message cap. There is deliberately no
// "unlimited" option: a non-positive value falls back to the default.
func (c *Config) maxMessageBytes() int {
	if c.MaxMessageBytes > 0 {
		return c.MaxMessageBytes
	}
	return DefaultMaxMessageBytes
}

// maxConns resolves the accept-side concurrent-connection cap. A negative value
// disables the extra cap (returns 0, meaning "unlimited" to the gate); zero
// defaults to DefaultMaxConns.
func (c *Config) maxConns() int {
	switch {
	case c.MaxConns < 0:
		return 0
	case c.MaxConns == 0:
		return DefaultMaxConns
	default:
		return c.MaxConns
	}
}

// quicConfig builds the *quic.Config shared by server and client.
//
// DisablePathMigration is deliberately left false so the connection migration
// path (Conn.AddPath/Path.Switch) is exercisable.
func (c *Config) quicConfig() *quicgo.Config {
	return &quicgo.Config{
		MaxIdleTimeout:        c.maxIdle(),
		HandshakeIdleTimeout:  c.handshakeIdle(),
		KeepAlivePeriod:       c.keepAlive(),
		EnableDatagrams:       c.EnableDatagrams,
		Allow0RTT:             c.Allow0RTT,
		TokenStore:            c.TokenStore,
		MaxIncomingStreams:    c.maxIncomingStreams(),
		MaxIncomingUniStreams: c.maxIncomingUniStreams(),
	}
}

// serverTLSConfig clones the base TLS config and forces the negotiated ALPN
// and TLS 1.3. The caller is responsible for providing Certificates.
func (c *Config) serverTLSConfig() *tls.Config {
	base := c.TLSConfig
	if base == nil {
		base = &tls.Config{} //nolint:gosec // MinVersion pinned below.
	}
	tc := base.Clone()
	tc.MinVersion = tls.VersionTLS13
	tc.NextProtos = c.nextProtos()
	return tc
}

// clientTLSConfig clones the base TLS config, forces ALPN + TLS 1.3, and
// guarantees a ClientSessionCache exists so TLS session tickets (the 0-RTT
// prerequisite) are retained across dials.
func (c *Config) clientTLSConfig() *tls.Config {
	base := c.TLSConfig
	if base == nil {
		base = &tls.Config{} //nolint:gosec // MinVersion pinned below.
	}
	tc := base.Clone()
	tc.MinVersion = tls.VersionTLS13
	tc.NextProtos = c.nextProtos()
	if tc.ClientSessionCache == nil {
		tc.ClientSessionCache = tls.NewLRUClientSessionCache(32)
	}
	return tc
}
