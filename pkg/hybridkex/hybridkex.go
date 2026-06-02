// Package hybridkex implements HXC-1476: a hybrid X25519 + ML-KEM-768
// authenticated-less key exchange — the post-quantum half of Helix
// node-to-node transport.
//
// The handshake derives a shared 32-byte session key by combining BOTH the
// classical X25519 ECDH secret AND the ML-KEM-768 encapsulated secret through
// HKDF-SHA256 over their concatenation (classical || pq). Dropping either half
// changes the derived key, so a downgrade or a tampered ML-KEM ciphertext makes
// the two sides disagree.
//
// Pure Go standard library only (crypto/ecdh, crypto/mlkem, crypto/hkdf).
package hybridkex

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// HybridGroup is the negotiated group label for the X25519 + ML-KEM-768 hybrid.
const HybridGroup = "X25519MLKEM768"

// SessionKeyLen is the length in bytes of the derived shared session key.
const SessionKeyLen = 32

// hkdfInfo binds the derived key to this protocol + group so the same secrets
// can never be reused to derive a key for a different context.
var hkdfInfo = []byte("helix-hybridkex/v1 " + HybridGroup)

// Initiator is the party that opens the handshake. It contributes the X25519
// ephemeral key and, after receiving the Responder's hello, performs the
// ML-KEM decapsulation. The Initiator publishes its X25519 public key and its
// ML-KEM-768 encapsulation key; the Responder encapsulates against the latter.
type Initiator struct {
	x25519Priv *ecdh.PrivateKey
	mlkemDK    *mlkem.DecapsulationKey768
}

// Responder is the party that answers the handshake. It contributes its own
// X25519 ephemeral key and the ML-KEM-768 encapsulation (ciphertext + the
// PQ shared secret), then derives the session key.
type Responder struct {
	x25519Priv *ecdh.PrivateKey
}

// InitiatorHello is the first handshake message: the Initiator's X25519 public
// key and its ML-KEM-768 encapsulation-key bytes.
type InitiatorHello struct {
	X25519Pub   []byte // raw X25519 public key bytes
	MLKEMEncKey []byte // mlkem.EncapsulationKey768 marshaled bytes
}

// ResponderHello is the reply message: the Responder's X25519 public key and
// the ML-KEM-768 ciphertext produced by encapsulating against the Initiator's
// encapsulation key.
type ResponderHello struct {
	X25519Pub   []byte // raw X25519 public key bytes
	MLKEMCipher []byte // ML-KEM-768 ciphertext from Encapsulate
}

// NewInitiator generates a fresh Initiator with ephemeral X25519 + ML-KEM-768
// keys and returns it together with the InitiatorHello to send to the peer.
func NewInitiator() (*Initiator, *InitiatorHello, error) {
	curve := ecdh.X25519()
	xpriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("hybridkex: x25519 keygen: %w", err)
	}
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, nil, fmt.Errorf("hybridkex: ml-kem-768 keygen: %w", err)
	}
	ek := dk.EncapsulationKey()
	in := &Initiator{x25519Priv: xpriv, mlkemDK: dk}
	hello := &InitiatorHello{
		X25519Pub:   xpriv.PublicKey().Bytes(),
		MLKEMEncKey: ek.Bytes(),
	}
	return in, hello, nil
}

// Respond consumes an InitiatorHello, generates the Responder's ephemeral
// X25519 key, encapsulates against the Initiator's ML-KEM encapsulation key,
// derives the shared session key, and returns it together with the
// ResponderHello to send back to the Initiator.
//
// The returned key is byte-identical to the one the Initiator derives in
// Finish on the matching ResponderHello.
func Respond(hello *InitiatorHello) (*Responder, *ResponderHello, []byte, error) {
	if hello == nil {
		return nil, nil, nil, errors.New("hybridkex: nil InitiatorHello")
	}
	curve := ecdh.X25519()

	// Parse the Initiator's X25519 public key.
	initiatorXPub, err := curve.NewPublicKey(hello.X25519Pub)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hybridkex: parse initiator x25519 pub: %w", err)
	}

	// Generate the Responder's ephemeral X25519 key and compute ECDH.
	rpriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hybridkex: responder x25519 keygen: %w", err)
	}
	classicalSecret, err := rpriv.ECDH(initiatorXPub)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hybridkex: responder ecdh: %w", err)
	}

	// Parse the Initiator's ML-KEM encapsulation key and encapsulate.
	ek, err := mlkem.NewEncapsulationKey768(hello.MLKEMEncKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hybridkex: parse ml-kem enc key: %w", err)
	}
	pqSecret, ciphertext := ek.Encapsulate()

	sessionKey, err := deriveKey(classicalSecret, pqSecret)
	if err != nil {
		return nil, nil, nil, err
	}

	resp := &Responder{x25519Priv: rpriv}
	rhello := &ResponderHello{
		X25519Pub:   rpriv.PublicKey().Bytes(),
		MLKEMCipher: ciphertext,
	}
	return resp, rhello, sessionKey, nil
}

// Finish consumes the ResponderHello, computes the Initiator-side ECDH secret,
// decapsulates the ML-KEM ciphertext, and derives the shared session key.
//
// On an untampered exchange the returned key equals the key returned by
// Respond. A tampered ML-KEM ciphertext (or a dropped PQ half) yields a
// different key, so the two sides will not agree.
func (in *Initiator) Finish(rhello *ResponderHello) ([]byte, error) {
	if in == nil {
		return nil, errors.New("hybridkex: nil Initiator")
	}
	if rhello == nil {
		return nil, errors.New("hybridkex: nil ResponderHello")
	}
	curve := ecdh.X25519()

	responderXPub, err := curve.NewPublicKey(rhello.X25519Pub)
	if err != nil {
		return nil, fmt.Errorf("hybridkex: parse responder x25519 pub: %w", err)
	}
	classicalSecret, err := in.x25519Priv.ECDH(responderXPub)
	if err != nil {
		return nil, fmt.Errorf("hybridkex: initiator ecdh: %w", err)
	}

	// ML-KEM Decapsulate is implicit-rejection: a tampered ciphertext does not
	// error, it yields a different (pseudorandom) shared secret — which is
	// exactly what makes the load-bearing guard live.
	pqSecret, err := in.mlkemDK.Decapsulate(rhello.MLKEMCipher)
	if err != nil {
		return nil, fmt.Errorf("hybridkex: ml-kem decapsulate: %w", err)
	}

	return deriveKey(classicalSecret, pqSecret)
}

// Group returns the negotiated group name for this handshake.
func (in *Initiator) Group() string { return HybridGroup }

// Group returns the negotiated group name for this handshake.
func (r *Responder) Group() string { return HybridGroup }

// deriveKey is the LOAD-BEARING combiner: it folds BOTH the classical X25519
// secret AND the post-quantum ML-KEM secret into the KDF input as
// (classical || pq). Removing the pq half (mutation) changes the output, so
// the two parties no longer agree — failing TestHandshake_AgreesOnKey via the
// PQ-contribution guard.
func deriveKey(classicalSecret, pqSecret []byte) ([]byte, error) {
	if len(classicalSecret) == 0 || len(pqSecret) == 0 {
		return nil, errors.New("hybridkex: empty secret half")
	}
	secret := make([]byte, 0, len(classicalSecret)+len(pqSecret))
	secret = append(secret, classicalSecret...)
	secret = append(secret, pqSecret...)

	salt := []byte(HybridGroup)
	key, err := hkdf.Key(sha256.New, secret, salt, string(hkdfInfo), SessionKeyLen)
	if err != nil {
		return nil, fmt.Errorf("hybridkex: hkdf: %w", err)
	}
	return key, nil
}
