// admission.go adds an OPTIONAL signed-identity + stake-based admission gate to
// the fiber transport. It models a miner<->validator admission handshake:
//
//  1. The validator issues a fresh single-use challenge nonce.
//  2. A peer presents a signed identity: its ed25519 public key and a signature
//     over the validator-issued nonce, plus a declared stake amount.
//  3. The validator ADMITS the peer iff the signature verifies against the
//     claimed public key AND the declared stake is >= a configured minimum AND
//     the nonce is fresh (not previously consumed -> replay rejected).
//
// The gate is OPTIONAL: a Conn created without an Admitter is open (the prior
// behavior). AcceptConn applies the gate before returning a *Conn so callers can
// reject a peer before any application frame is exchanged.
//
// The signature check is abstracted behind the IdentityVerifier seam so the
// real digital.vasic.security identity stack can be slotted in unchanged; the
// in-package Ed25519Verifier is a WORKING crypto/ed25519 reference that actually
// verifies (a forged or wrong-key signature fails).
package fiber

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
)

// Admission-related sentinel errors. They are distinct so a caller (and a test)
// can assert the EXACT reason a peer was denied rather than a generic failure.
var (
	// ErrBadSignature is returned when the presented signature does not verify
	// against the claimed public key over the issued nonce. This is the forged-
	// identity rejection.
	ErrBadSignature = errors.New("fiber: identity signature does not verify")
	// ErrInsufficientStake is returned when a peer with an otherwise valid
	// signature declares a stake below the configured minimum.
	ErrInsufficientStake = errors.New("fiber: declared stake below minimum")
	// ErrStaleNonce is returned when a handshake presents a nonce that was not
	// issued by this validator or that has already been consumed (replay).
	ErrStaleNonce = errors.New("fiber: challenge nonce is stale or unknown")
	// ErrMalformedHandshake is returned for structurally invalid input: an empty
	// nonce, a public key of the wrong length, or an empty signature.
	ErrMalformedHandshake = errors.New("fiber: malformed admission handshake")
)

// nonceSize is the length of a validator-issued challenge nonce in bytes. 32
// bytes (256 bits) makes accidental or adversarial nonce guessing infeasible.
const nonceSize = 32

// Decision is the outcome of evaluating an admission Handshake.
type Decision struct {
	// Admit reports whether the peer is admitted.
	Admit bool
	// Reason is nil when Admit is true; otherwise it is the specific sentinel
	// error explaining the denial (ErrBadSignature, ErrInsufficientStake,
	// ErrStaleNonce, or ErrMalformedHandshake).
	Reason error
}

// Handshake is the signed identity + stake a peer presents to be admitted.
type Handshake struct {
	// PublicKey is the peer's claimed ed25519 public key (ed25519.PublicKeySize
	// bytes for the reference verifier).
	PublicKey []byte
	// Nonce is the validator-issued challenge the peer signed. It binds the
	// signature to a single admission attempt and is consumed on use.
	Nonce []byte
	// Signature is the peer's signature over Nonce, verifiable under PublicKey.
	Signature []byte
	// Stake is the peer's declared stake amount. Admission requires
	// Stake >= the Admitter's configured minimum.
	Stake uint64
}

// IdentityVerifier is the signature-verification seam. It reports whether sig is
// a valid signature by pubKey over msg. The real digital.vasic.security identity
// verifier implements this same method and plugs in unchanged; Ed25519Verifier
// is the working stdlib reference.
//
// Implementations MUST return false (never panic) for malformed inputs such as a
// wrong-length public key.
type IdentityVerifier interface {
	Verify(pubKey, msg, sig []byte) bool
}

// Ed25519Verifier is a REAL, working crypto/ed25519 reference IdentityVerifier.
// It verifies a signature using the stdlib ed25519.Verify, so a forged signature,
// a signature by a different key, or a tampered nonce all fail. A public key that
// is not exactly ed25519.PublicKeySize bytes is rejected (false) rather than
// panicking.
type Ed25519Verifier struct{}

// Verify reports whether sig is a valid ed25519 signature by pubKey over msg.
func (Ed25519Verifier) Verify(pubKey, msg, sig []byte) bool {
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubKey), msg, sig)
}

// Admitter is the optional admission gate. It mints single-use challenge nonces,
// tracks outstanding/consumed nonces for replay protection, and evaluates a
// peer's Handshake against the configured minimum stake and the IdentityVerifier.
//
// An Admitter is safe for concurrent use by multiple goroutines.
type Admitter struct {
	verifier IdentityVerifier
	minStake uint64

	mu     sync.Mutex
	issued map[string]struct{} // hex(nonce) -> outstanding (unconsumed) nonces
}

// NewAdmitter builds an Admitter. verifier supplies signature verification (pass
// Ed25519Verifier{} for the stdlib reference, or the real identity verifier).
// minStake is the inclusive lower bound on a peer's declared stake. A nil
// verifier defaults to Ed25519Verifier{} so the gate is never silently a no-op.
func NewAdmitter(verifier IdentityVerifier, minStake uint64) *Admitter {
	if verifier == nil {
		verifier = Ed25519Verifier{}
	}
	return &Admitter{
		verifier: verifier,
		minStake: minStake,
		issued:   make(map[string]struct{}),
	}
}

// MinStake reports the configured inclusive minimum stake for admission.
func (a *Admitter) MinStake() uint64 { return a.minStake }

// IssueChallenge mints a fresh, cryptographically random single-use nonce and
// records it as outstanding. The peer signs the returned nonce and presents it
// in its Handshake. Each issued nonce is accepted by Admit at most once.
func (a *Admitter) IssueChallenge() ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.issued[hex.EncodeToString(nonce)] = struct{}{}
	a.mu.Unlock()
	return nonce, nil
}

// consume atomically reports whether nonce is currently outstanding and, if so,
// removes it so it can never be admitted twice (replay protection). A nonce that
// was never issued, or was already consumed, returns false.
func (a *Admitter) consume(nonce []byte) bool {
	key := hex.EncodeToString(nonce)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.issued[key]; !ok {
		return false
	}
	delete(a.issued, key)
	return true
}

// Admit evaluates h and returns a Decision. The order of checks is:
//
//  1. structural validity (non-empty nonce/signature, well-formed key length);
//  2. nonce freshness — the nonce must have been issued by this Admitter and not
//     yet consumed (this both binds the signature to a live challenge AND
//     rejects a replayed handshake);
//  3. signature verification over the nonce under the claimed public key;
//  4. stake >= the configured minimum.
//
// A peer is admitted only if ALL checks pass. The returned error mirrors
// Decision.Reason for callers that prefer error-style handling; it is nil on
// admit.
//
// The nonce is consumed (step 2) BEFORE the signature/stake checks so that a
// single live nonce cannot be reused even by a peer that fails a later check;
// this prevents a brute-force probe from reusing one challenge indefinitely.
func (a *Admitter) Admit(h Handshake) (Decision, error) {
	// Step 1: structural validity. A wrong-length key or empty nonce/signature
	// is malformed, not merely a bad signature.
	if len(h.Nonce) == 0 || len(h.Signature) == 0 ||
		len(h.PublicKey) != ed25519.PublicKeySize {
		return deny(ErrMalformedHandshake)
	}

	// Step 2: nonce freshness + single-use consumption (replay protection).
	if !a.consume(h.Nonce) {
		return deny(ErrStaleNonce)
	}

	// Step 3: REAL signature verification over the issued nonce. A forged or
	// wrong-key signature fails here and the peer is denied.
	if !a.verifier.Verify(h.PublicKey, h.Nonce, h.Signature) {
		return deny(ErrBadSignature)
	}

	// Step 4: stake admission (inclusive minimum).
	if h.Stake < a.minStake {
		return deny(ErrInsufficientStake)
	}

	return Decision{Admit: true}, nil
}

// deny is a small helper that builds a denied Decision and mirrors the reason as
// the returned error.
func deny(reason error) (Decision, error) {
	return Decision{Admit: false, Reason: reason}, reason
}

// AcceptConn applies an OPTIONAL admission gate before wrapping rw in a *Conn. If
// admitter is nil the gate is skipped and the connection is accepted exactly as
// NewConn would build it (backward-compatible open behavior). If admitter is
// non-nil the peer's Handshake must be admitted; otherwise the denial error is
// returned and NO *Conn is created.
//
// maxFrameSize is forwarded to NewConn (a value <= 0 selects DefaultMaxFrameSize).
func AcceptConn(rw io.ReadWriter, maxFrameSize int, admitter *Admitter, h Handshake) (*Conn, error) {
	if admitter != nil {
		dec, err := admitter.Admit(h)
		if err != nil {
			return nil, err
		}
		if !dec.Admit {
			// Defensive: Admit returns a non-nil err whenever Admit is false, so
			// this branch is unreachable in practice, but it keeps the contract
			// explicit that a non-admit never yields a Conn.
			return nil, ErrMalformedHandshake
		}
	}
	return NewConn(rw, maxFrameSize), nil
}
