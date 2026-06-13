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

	// KeepAlivePeriod is the keep-alive PING period. Zero -> DefaultKeepAlivePeriod.
	KeepAlivePeriod time.Duration

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

// quicConfig builds the *quic.Config shared by server and client.
//
// DisablePathMigration is deliberately left false so the connection migration
// path (Conn.AddPath/Path.Switch) is exercisable.
func (c *Config) quicConfig() *quicgo.Config {
	return &quicgo.Config{
		MaxIdleTimeout:  c.maxIdle(),
		KeepAlivePeriod: c.keepAlive(),
		EnableDatagrams: c.EnableDatagrams,
		Allow0RTT:       c.Allow0RTT,
		TokenStore:      c.TokenStore,
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
