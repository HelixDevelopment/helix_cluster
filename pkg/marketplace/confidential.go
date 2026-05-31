package marketplace

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// Encryptor is the seam for protecting a job payload before it is dispatched to
// a (possibly third-party) confidential marketplace. Seal encrypts; Open
// reverses it. The reference implementation below is real AES-256-GCM and
// actually round-trips. The production digital.vasic.security submodule's PQ
// E2EE implements this SAME interface, so callers are unchanged when it is
// swapped in.
type Encryptor interface {
	// Seal encrypts plaintext, returning ciphertext that Open can recover.
	Seal(plaintext []byte) (ciphertext []byte, err error)
	// Open decrypts ciphertext produced by Seal. It returns an error if the
	// ciphertext was tampered with or was not produced by this Encryptor.
	Open(ciphertext []byte) (plaintext []byte, err error)
}

// Attestor is the seam for verifying that a marketplace offer really runs in a
// genuine TEE before confidential work is sent to it. The reference
// implementation is a real HMAC-SHA256 over the quote that actually verifies.
// The production submodule's proof-of-GPU attestation implements this SAME
// interface.
type Attestor interface {
	// Attest produces a verifiable attestation token for the given offer.
	Attest(offer Offer) (token []byte, err error)
	// Verify reports whether token is a genuine attestation for offer. A
	// tampered token or wrong offer must fail verification.
	Verify(offer Offer, token []byte) bool
}

// ErrCiphertextTooShort is returned when Open is given input too small to hold
// the GCM nonce.
var ErrCiphertextTooShort = errors.New("marketplace: ciphertext too short")

// AESGCMEncryptor is the stdlib reference Encryptor: AES-256-GCM with a random
// per-message nonce prepended to the ciphertext. It genuinely encrypts and
// authenticates — flipping a ciphertext byte makes Open fail.
type AESGCMEncryptor struct {
	aead cipher.AEAD
}

// NewAESGCMEncryptor builds an AESGCMEncryptor from a 16/24/32-byte key (AES-128
// /192/256). A 32-byte key selects AES-256.
func NewAESGCMEncryptor(key []byte) (*AESGCMEncryptor, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMEncryptor{aead: aead}, nil
}

// Seal implements Encryptor. Layout: nonce || GCM(ciphertext+tag).
func (e *AESGCMEncryptor) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return e.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open implements Encryptor. It rejects truncated or tampered ciphertext.
func (e *AESGCMEncryptor) Open(ciphertext []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrCiphertextTooShort
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	return e.aead.Open(nil, nonce, ct, nil)
}

// HMACAttestor is the stdlib reference Attestor: an HMAC-SHA256 over a canonical
// encoding of the offer's identity. It genuinely verifies — a wrong offer or a
// tampered token fails Verify via constant-time comparison.
type HMACAttestor struct {
	key []byte
}

// NewHMACAttestor builds an HMACAttestor with the given secret key.
func NewHMACAttestor(key []byte) *HMACAttestor {
	k := make([]byte, len(key))
	copy(k, key)
	return &HMACAttestor{key: k}
}

// quote is the canonical bytes the attestation binds to: the offer's marketplace
// identity and its confidentiality claim. Two different offers therefore yield
// different tokens, so a token cannot be replayed across offers.
func quote(offer Offer) []byte {
	conf := byte('0')
	if offer.Confidential {
		conf = '1'
	}
	q := make([]byte, 0, len(offer.Provider)+len(offer.ID)+2)
	q = append(q, offer.Provider...)
	q = append(q, 0)
	q = append(q, offer.ID...)
	q = append(q, conf)
	return q
}

// Attest implements Attestor.
func (a *HMACAttestor) Attest(offer Offer) ([]byte, error) {
	mac := hmac.New(sha256.New, a.key)
	mac.Write(quote(offer))
	return mac.Sum(nil), nil
}

// Verify implements Attestor using a constant-time comparison.
func (a *HMACAttestor) Verify(offer Offer, token []byte) bool {
	mac := hmac.New(sha256.New, a.key)
	mac.Write(quote(offer))
	return hmac.Equal(mac.Sum(nil), token)
}
