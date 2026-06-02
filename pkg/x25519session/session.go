// Package x25519session implements an X25519 ephemeral session-key handshake.
//
// Model: two parties (e.g. a node and a validator) each generate an ephemeral
// X25519 keypair and exchange their PUBLIC keys. Each side computes the SAME
// Elliptic-Curve Diffie-Hellman (ECDH) shared secret using its own PRIVATE key
// and the peer's PUBLIC key. From that shared secret each side derives a
// per-session HMAC key via HKDF-SHA256 bound to a session label.
//
// Security properties relied upon by callers (and proven in the tests):
//   - Both honest sides derive the IDENTICAL per-session key.
//   - A party holding a DIFFERENT private key cannot reproduce that key
//     (the ECDH secret, and therefore the derived key, differs).
//   - Changing the session label/info yields a DIFFERENT derived key from the
//     same shared secret, so keys are domain-separated per session.
//
// The package is pure Go and uses only the standard library:
// crypto/ecdh (X25519) + crypto/hkdf + crypto/sha256.
package x25519session

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"io"
)

// SessionKeyLength is the length in bytes of the derived per-session HMAC key.
// 32 bytes => a 256-bit key, matching HMAC-SHA256.
const SessionKeyLength = 32

// hkdfSalt is a fixed, non-secret domain-separation salt for the HKDF Extract
// step. A constant salt is acceptable for HKDF; the entropy comes from the
// ECDH shared secret. It binds derivations to this package's purpose.
var hkdfSalt = []byte("helix-x25519session/v1/hkdf-salt")

// ErrEmptyLabel is returned when DeriveSessionKey is called with an empty
// session label. A non-empty label is required so that distinct sessions are
// cryptographically domain-separated.
var ErrEmptyLabel = errors.New("x25519session: session label must not be empty")

// ErrNilKey is returned when a nil private or public key is supplied.
var ErrNilKey = errors.New("x25519session: private and peer public keys must not be nil")

// GenerateEphemeral generates a fresh ephemeral X25519 keypair using the
// supplied randomness source (typically crypto/rand.Reader). The returned
// private key MUST be kept secret; only the public key is exchanged with the
// peer.
func GenerateEphemeral(rand io.Reader) (*ecdh.PrivateKey, *ecdh.PublicKey, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand)
	if err != nil {
		return nil, nil, err
	}
	return priv, priv.PublicKey(), nil
}

// DeriveSessionKey computes the ECDH shared secret between myPriv and peerPub,
// then derives a per-session HMAC key of length SessionKeyLength via
// HKDF-SHA256 over that shared secret, domain-separated by sessionLabel.
//
// Both honest parties — each calling DeriveSessionKey(ownPriv, peerPub, label)
// with the SAME label — obtain the IDENTICAL key, because X25519 ECDH is
// symmetric: ECDH(a, B) == ECDH(b, A) when A=g^a and B=g^b.
//
// A caller holding a different private key (and thus a different public key)
// derives a different shared secret and therefore a different key.
func DeriveSessionKey(myPriv *ecdh.PrivateKey, peerPub *ecdh.PublicKey, sessionLabel string) ([]byte, error) {
	if myPriv == nil || peerPub == nil {
		return nil, ErrNilKey
	}
	if sessionLabel == "" {
		return nil, ErrEmptyLabel
	}

	// X25519 ECDH: this consumes BOTH the private scalar (myPriv) and the
	// peer public point (peerPub). Tampering with either input changes the
	// shared secret.
	shared, err := myPriv.ECDH(peerPub)
	if err != nil {
		return nil, err
	}

	// HKDF-SHA256 Extract-then-Expand. The `info` carries the session label so
	// that two sessions over the same shared secret produce distinct keys.
	key, err := hkdf.Key(sha256.New, shared, hkdfSalt, sessionLabel, SessionKeyLength)
	if err != nil {
		return nil, err
	}
	return key, nil
}
