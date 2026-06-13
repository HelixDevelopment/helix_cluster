package gpuattest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
)

// HXC-1568: device-info challenge/response + fingerprint.

// Typed errors. Each attack variant in Verify maps to a DISTINCT sentinel so a
// caller (and the closure tests) can prove exactly which check rejected the
// response, rather than seeing one opaque failure.
var (
	// ErrExpired is returned when the challenge's expiry tick has passed at the
	// time of verification.
	ErrExpired = errors.New("gpuattest: challenge expired")
	// ErrReplay is returned when the challenge nonce has already been consumed
	// by this Verifier (single-use nonce set).
	ErrReplay = errors.New("gpuattest: nonce replayed")
	// ErrTampered is returned when the signature does not match the
	// reconstructed signed message (any signed field altered).
	ErrTampered = errors.New("gpuattest: signed payload tampered")
	// ErrWrongKey is returned when the response was signed by a key that does
	// not match the descriptor's public key.
	ErrWrongKey = errors.New("gpuattest: signature key mismatch")
	// ErrForgedDescriptor is returned when the descriptor presented at verify
	// time does not hash to the fingerprint that was signed.
	ErrForgedDescriptor = errors.New("gpuattest: descriptor fingerprint mismatch")
)

// Descriptor is the immutable, signable identity of a GPU device. PubKey is the
// device's ed25519 public key; the matching private key never leaves the device.
type Descriptor struct {
	Vendor string
	Model  string
	UUID   string
	VRAM   uint64 // bytes
	PubKey ed25519.PublicKey
}

// canonical produces a stable, unambiguous byte encoding of the descriptor.
// Length-prefixing every variable field prevents field-boundary ambiguity
// (e.g. {"ab","c"} vs {"a","bc"} must not collide).
func (d Descriptor) canonical() []byte {
	var b []byte
	put := func(s []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		b = append(b, n[:]...)
		b = append(b, s...)
	}
	put([]byte(d.Vendor))
	put([]byte(d.Model))
	put([]byte(d.UUID))
	var vram [8]byte
	binary.BigEndian.PutUint64(vram[:], d.VRAM)
	b = append(b, vram[:]...)
	put([]byte(d.PubKey))
	return b
}

// Fingerprint is the stable SHA-256 over the descriptor's canonical encoding.
func Fingerprint(d Descriptor) [32]byte {
	return sha256.Sum256(d.canonical())
}

// Challenge is issued by a Verifier. The device must echo the nonce and sign a
// message bound to the issuing tick and the device fingerprint.
type Challenge struct {
	Nonce      [32]byte
	IssueTick  int64
	ExpiryTick int64
}

// Response is the device's signed answer. Fingerprint is the fingerprint the
// device claims (and signed); Tick is the device-asserted production tick.
// SignerPub is the public key the device claims signed this response; Verify
// binds it to the descriptor's key (mismatch => wrong-key) AND verifies the
// signature under it (failure under a matching key => tampered field).
type Response struct {
	Nonce       [32]byte
	Fingerprint [32]byte
	Tick        int64
	SignerPub   ed25519.PublicKey
	Sig         []byte
}

// signedMessage reconstructs the exact bytes that are signed: nonce || fp ||
// tick. Both signer and verifier MUST agree on this layout.
func signedMessage(nonce, fp [32]byte, tick int64) []byte {
	msg := make([]byte, 0, 32+32+8)
	msg = append(msg, nonce[:]...)
	msg = append(msg, fp[:]...)
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(tick)) //gosec:disable G115 -- full-width int64->uint64 bit-pattern for signing; no truncation
	msg = append(msg, t[:]...)
	return msg
}

// Device holds a key pair and its descriptor. NewDevice generates a fresh
// ed25519 key (crypto/rand is fine here — tests drive accept/reject via fixed
// challenges and key reuse, not by predicting the key bytes).
type Device struct {
	Descriptor Descriptor
	priv       ed25519.PrivateKey
}

// NewDevice creates a device with a fresh key pair. vendor/model/uuid/vram set
// the descriptor identity; the public key is filled from the generated pair.
func NewDevice(vendor, model, uuid string, vram uint64) (*Device, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Device{
		Descriptor: Descriptor{Vendor: vendor, Model: model, UUID: uuid, VRAM: vram, PubKey: pub},
		priv:       priv,
	}, nil
}

// Respond produces a genuine Response to a challenge at the given production
// tick. It signs nonce || Fingerprint(descriptor) || tick.
func (d *Device) Respond(c Challenge, tick int64) Response {
	fp := Fingerprint(d.Descriptor)
	sig := ed25519.Sign(d.priv, signedMessage(c.Nonce, fp, tick))
	return Response{Nonce: c.Nonce, Fingerprint: fp, Tick: tick, SignerPub: d.Descriptor.PubKey, Sig: sig}
}

// Verifier issues challenges and verifies responses. It owns the single-use
// nonce set used to reject replays.
type Verifier struct {
	mu   sync.Mutex
	seen map[[32]byte]bool
}

// NewVerifier returns a Verifier with an empty consumed-nonce set.
func NewVerifier() *Verifier {
	return &Verifier{seen: make(map[[32]byte]bool)}
}

// Issue creates a Challenge with a fresh random nonce, bound to the issue and
// expiry ticks supplied by the caller's clock.
func (v *Verifier) Issue(issueTick, expiryTick int64) (Challenge, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Challenge{}, err
	}
	return Challenge{Nonce: nonce, IssueTick: issueTick, ExpiryTick: expiryTick}, nil
}

// Verify checks a response against the challenge and the presented descriptor at
// the verifier's current tick. Order of checks matters: expiry and replay are
// cheap pre-conditions; fingerprint binding, key match and signature are the
// cryptographic core. On success the nonce is consumed (single-use).
//
// Returns nil for a genuine response, or one of the distinct sentinel errors.
func (v *Verifier) Verify(c Challenge, r Response, d Descriptor, nowTick int64) error {
	// (1) expiry — past the window is rejected before any crypto work.
	if nowTick > c.ExpiryTick {
		return ErrExpired
	}

	// (2) replay — a consumed nonce cannot be reused. The check AND the consume
	// happen atomically under one lock acquisition so there is no TOCTOU window:
	// two concurrent verifications of the same nonce cannot both pass the gate —
	// the first marks it seen, the second observes seen and is rejected. Any
	// subsequent validation failure RELEASES the nonce (via fail() below) so a
	// failed attempt does not permanently burn an otherwise-legitimate challenge
	// nonce; only a fully genuine response leaves it consumed.
	v.mu.Lock()
	if v.seen[c.Nonce] {
		v.mu.Unlock()
		return ErrReplay
	}
	v.seen[c.Nonce] = true
	v.mu.Unlock()

	// fail releases the nonce consumed above and returns err. Used for every
	// post-consume validation failure so only a genuine response keeps the nonce.
	fail := func(err error) error {
		v.mu.Lock()
		delete(v.seen, c.Nonce)
		v.mu.Unlock()
		return err
	}

	// (3) forged descriptor — the descriptor presented now must hash to the
	// fingerprint the device signed. A swapped descriptor is caught here even
	// before the signature, because the signed fingerprint won't match.
	wantFP := Fingerprint(d)
	if wantFP != r.Fingerprint {
		return fail(ErrForgedDescriptor)
	}

	// (4) wrong key — the response names the public key that signed it
	// (r.SignerPub). It is bound to identity by the descriptor: a genuine device
	// signs with the key embedded in its descriptor, so r.SignerPub MUST equal
	// d.PubKey. A response signed by a different key (even if that signature is
	// internally valid) is rejected here, BEFORE the signature math, giving a
	// precise wrong-key verdict that does not depend on guessing.
	if len(d.PubKey) != ed25519.PublicKeySize || !d.PubKey.Equal(r.SignerPub) {
		return fail(ErrWrongKey)
	}

	// (5) tampered — verify the signature under the descriptor's key over the
	// reconstructed message. Any altered signed field (nonce, fingerprint, tick)
	// makes this fail. Because the key already matches (step 4), a verify failure
	// here can ONLY mean a signed field was tampered.
	msg := signedMessage(r.Nonce, r.Fingerprint, r.Tick)
	if !ed25519.Verify(d.PubKey, msg, r.Sig) {
		return fail(ErrTampered)
	}

	// The signature verified, but we must still bind it to THIS challenge: the
	// signed nonce must equal the challenge nonce, else it is a valid signature
	// over a different (tampered) nonce.
	if r.Nonce != c.Nonce {
		return fail(ErrTampered)
	}

	// Genuine — the nonce was already consumed atomically at step (2); keep it.
	return nil
}
