// Package doublecrypt implements the HXC-1361 double-encryption data path for
// Helix Cluster OS: WireGuard L3 (injected dependency) + mTLS L7 (real, in this
// package via crypto/tls and crypto/x509 from the Go standard library only).
//
// # Architecture
//
// The design follows the §11.4 two-layer envelope:
//
//	┌─────────────────────────────────────────────────┐
//	│  L7  mTLS (real — crypto/tls TLS 1.3, ECDSA)   │
//	│  implemented here; wraps any net.Conn            │
//	├─────────────────────────────────────────────────┤
//	│  L3  WireGuard (injected dependency)             │
//	│  DialWireGuard returns ErrUnsupported when no    │
//	│  WG device is present; never fabricates a conn  │
//	└─────────────────────────────────────────────────┘
//
// In production the caller:
//  1. Obtains a *net.Conn from the WG tunnel (L3), then
//  2. Wraps it with ClientConn / ServerConn to add L7 mTLS.
//
// In tests net.Pipe() is passed as the inner conn so that the mTLS handshake
// and data transfer are exercised fully without requiring a real WG device.
//
// # CLAUDE-2 compliance
//
// Where a genuine OS or environment capability is absent (WireGuard kernel
// device / wireguard-go user-space not configured), DialWireGuard returns the
// typed ErrUnsupported sentinel and a nil conn.  It MUST NOT fabricate a
// connection or return nil error for a missing capability.
package doublecrypt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
)

// ErrUnsupported is returned by DialWireGuard when no WireGuard device (kernel
// or wireguard-go user-space) is available in the current environment.  Callers
// must check with errors.Is(err, ErrUnsupported) and either inject a pre-dialed
// conn or abort cleanly.
//
// CLAUDE-2 rationale: fabricating a conn would be a PASS-bluff (§7.1) for a
// feature that claims real L3 WireGuard encryption.
var ErrUnsupported = errors.New("doublecrypt: WireGuard L3 dialer unsupported in this environment")

// NewMTLSConfig builds a *tls.Config for mutual TLS over any net.Conn.
//
// Parameters:
//   - certPEM  PEM-encoded certificate for this endpoint (server or client).
//   - keyPEM   PEM-encoded private key matching certPEM.
//   - caPEM    PEM-encoded CA certificate used to verify the peer.
//   - isServer true → server-side config (sets ClientCAs + RequireAndVerifyClientCert);
//     false → client-side config (sets RootCAs + Certificates).
//
// The minimum TLS version is TLS 1.3.  InsecureSkipVerify is never set.
//
// Returns a non-nil error when:
//   - caPEM cannot be parsed or cannot be loaded into the cert pool.
//   - certPEM / keyPEM cannot be parsed as a valid key pair.
func NewMTLSConfig(certPEM, keyPEM, caPEM []byte, isServer bool) (*tls.Config, error) {
	// Validate + load the CA into an X.509 pool.
	pool := x509.NewCertPool()
	if len(caPEM) == 0 {
		return nil, fmt.Errorf("doublecrypt: caPEM is empty")
	}
	// Verify we can at least decode the first PEM block before AppendCertsFromPEM.
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, fmt.Errorf("doublecrypt: caPEM contains no valid PEM block")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("doublecrypt: caPEM is not a valid DER certificate: %w", err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("doublecrypt: failed to append caPEM to certificate pool")
	}

	// Load the local certificate + key pair.
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("doublecrypt: invalid certPEM/keyPEM pair: %w", err)
	}

	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}

	if isServer {
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.RootCAs = pool
	}

	return cfg, nil
}

// ClientConn wraps inner with TLS as a client using cfg.
// In the double-encryption stack, inner is the WireGuard L3 net.Conn; in tests
// it is one end of a net.Pipe().
//
// The caller must call Handshake() (or perform a read/write which triggers it)
// before exchanging application data.
func ClientConn(inner net.Conn, cfg *tls.Config) *tls.Conn {
	return tls.Client(inner, cfg)
}

// ServerConn wraps inner with TLS as a server using cfg.
// In the double-encryption stack, inner is the WireGuard L3 net.Conn; in tests
// it is one end of a net.Pipe().
//
// The caller must call Handshake() (or perform a read/write which triggers it)
// before exchanging application data.
func ServerConn(inner net.Conn, cfg *tls.Config) *tls.Conn {
	return tls.Server(inner, cfg)
}

// DialWireGuard represents the L3 WireGuard dialer for the double-encryption
// stack.  It always returns ErrUnsupported because no WireGuard kernel device
// or wireguard-go user-space process is guaranteed to be present in the sandbox
// or CI environment.
//
// CLAUDE-2 mandate: where a real OS capability is absent, return a typed
// ErrUnsupported sentinel and a nil conn.  Fabricating a conn is a §7.1
// PASS-bluff and is FORBIDDEN.
//
// In production, callers replace this with their WireGuard dialer and pass the
// resulting net.Conn into ClientConn / ServerConn to add the L7 mTLS layer.
func DialWireGuard(_ context.Context, _ string) (net.Conn, error) {
	return nil, ErrUnsupported
}
