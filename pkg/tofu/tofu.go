// Package tofu implements trust-on-first-use (TOFU) node identity
// verification with verify-then-pin semantics.
//
// A node presents an identity consisting of (nodeID, ed25519 public key,
// self-signature). The self-signature proves possession of the private key
// corresponding to the presented public key over a canonical message derived
// from the nodeID.
//
// On the FIRST contact for a given nodeID the self-signature is verified and,
// if valid, the public key is PINNED for that nodeID. On every SUBSEQUENT
// contact the presented key MUST equal the previously pinned key (and the
// self-signature must still verify). A different key for the same nodeID is
// rejected as a spoof/MITM (ErrPinMismatch). A bad self-signature is rejected
// (ErrBadSignature) and nothing is pinned.
//
// The package is pure Go, deterministic, and uses only the standard library
// crypto/ed25519 primitives. No clock, network, or host access is used.
package tofu

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"sync"
)

// Sentinel errors returned by VerifyAndPin. Callers can match these with
// errors.Is to distinguish spoof attempts from signature failures.
var (
	// ErrBadSignature is returned when the presented self-signature does not
	// verify under the presented public key over the canonical nodeID message.
	ErrBadSignature = errors.New("tofu: self-signature verification failed")

	// ErrPinMismatch is returned when a public key is presented for a nodeID
	// that has already been pinned to a DIFFERENT public key. This indicates a
	// spoof / man-in-the-middle attempt; the existing pin is left unchanged.
	ErrPinMismatch = errors.New("tofu: presented key does not match pinned key")

	// ErrEmptyNodeID is returned when an empty nodeID is supplied.
	ErrEmptyNodeID = errors.New("tofu: empty nodeID")

	// ErrBadKeySize is returned when the presented public key is not a valid
	// ed25519 public key (wrong length).
	ErrBadKeySize = errors.New("tofu: invalid ed25519 public key size")
)

// canonicalMessage builds the canonical byte message that a node self-signs to
// prove key possession over its nodeID. The fixed domain-separation prefix
// binds the signature to this protocol so a signature cannot be replayed from
// another context, and the nodeID binds it to the identity being claimed.
func canonicalMessage(nodeID string) []byte {
	const prefix = "helix-tofu:node-identity:v1:"
	msg := make([]byte, 0, len(prefix)+len(nodeID))
	msg = append(msg, prefix...)
	msg = append(msg, nodeID...)
	return msg
}

// Sign produces a self-signature over the canonical nodeID message using the
// given ed25519 private key. It is the helper a genuine node (and tests) use to
// build a verifiable identity. The returned signature verifies under the public
// key corresponding to priv.
func Sign(priv ed25519.PrivateKey, nodeID string) []byte {
	return ed25519.Sign(priv, canonicalMessage(nodeID))
}

// Store holds the set of nodeID -> pinned public key bindings. It is safe for
// concurrent use.
type Store struct {
	mu     sync.RWMutex
	pinned map[string]ed25519.PublicKey
}

// NewStore returns an empty Store ready for use.
func NewStore() *Store {
	return &Store{pinned: make(map[string]ed25519.PublicKey)}
}

// VerifyAndPin applies TOFU verify-then-pin semantics for one contact.
//
// First contact (nodeID not yet pinned): the self-signature is verified over
// the canonical nodeID message. If valid the key is pinned and nil is returned;
// otherwise ErrBadSignature is returned and nothing is pinned.
//
// Subsequent contact (nodeID already pinned): if the presented key differs from
// the pinned key, ErrPinMismatch is returned and the existing pin is unchanged
// (the spoof is rejected regardless of whether its self-signature is internally
// valid). If the presented key equals the pinned key, the self-signature is
// re-verified; a valid signature returns nil, an invalid one returns
// ErrBadSignature (and the pin remains unchanged).
func (s *Store) VerifyAndPin(nodeID string, pub ed25519.PublicKey, sig []byte) error {
	if nodeID == "" {
		return ErrEmptyNodeID
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrBadKeySize
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, alreadyPinned := s.pinned[nodeID]
	if alreadyPinned {
		// Pin-equality check FIRST: a different key for a known nodeID is a
		// spoof and is rejected before we even trust its self-signature.
		if !bytes.Equal(existing, pub) {
			return ErrPinMismatch
		}
		// Same key on reconnect: still require a valid self-signature.
		if !ed25519.Verify(pub, canonicalMessage(nodeID), sig) {
			return ErrBadSignature
		}
		return nil
	}

	// First contact: verify the self-signature before pinning.
	if !ed25519.Verify(pub, canonicalMessage(nodeID), sig) {
		return ErrBadSignature
	}

	// Pin a defensive copy so external mutation of the caller's slice cannot
	// retroactively alter what we trust.
	pinnedCopy := make(ed25519.PublicKey, len(pub))
	copy(pinnedCopy, pub)
	s.pinned[nodeID] = pinnedCopy
	return nil
}

// Pinned returns the public key currently pinned for nodeID and whether a pin
// exists. The returned key is a copy; mutating it does not affect the store.
func (s *Store) Pinned(nodeID string) (ed25519.PublicKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, ok := s.pinned[nodeID]
	if !ok {
		return nil, false
	}
	out := make(ed25519.PublicKey, len(pub))
	copy(out, pub)
	return out, true
}
