package chutes

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"
)

func newTestEncryptor(t *testing.T) *ReferenceEncryptor {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("key: %v", err)
	}
	e, err := NewReferenceEncryptor(key)
	if err != nil {
		t.Fatalf("NewReferenceEncryptor: %v", err)
	}
	return e
}

// TestEncryptorRoundTrip proves the AES-256-GCM reference actually encrypts and
// decrypts: ciphertext differs from plaintext, and Open recovers it exactly.
//
// MUTATION: in Seal, return `plaintext` directly (skip aead.Seal). The
// "ciphertext must differ from plaintext" assertion fails (and Open would also
// break).
func TestEncryptorRoundTrip(t *testing.T) {
	t.Parallel()
	e := newTestEncryptor(t)
	pt := []byte("the quick brown fox jumps over the lazy dog")
	aad := []byte("model=qwen-2.5")

	ct, err := e.Seal(pt, aad)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(ct, pt) {
		t.Fatalf("ciphertext equals plaintext; encryption is a no-op")
	}
	if bytes.Contains(ct, pt) {
		t.Fatalf("plaintext leaked verbatim inside ciphertext")
	}
	got, err := e.Open(ct, aad)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("Open = %q, want %q", got, pt)
	}
}

// TestEncryptorNonceUniqueness proves a fresh random nonce per Seal: two seals of
// the same plaintext produce different ciphertexts.
//
// MUTATION: in Seal, use a fixed zero nonce instead of rand. The two ciphertexts
// become identical and the inequality assertion fails.
func TestEncryptorNonceUniqueness(t *testing.T) {
	t.Parallel()
	e := newTestEncryptor(t)
	pt := []byte("same plaintext")
	c1, _ := e.Seal(pt, nil)
	c2, _ := e.Seal(pt, nil)
	if bytes.Equal(c1, c2) {
		t.Fatalf("two seals produced identical ciphertext; nonce is not random")
	}
}

// TestEncryptorTamperFails proves a tampered ciphertext byte fails Open via the
// GCM authentication tag.
//
// MUTATION: in Open, ignore the aead.Open error and return the (nil) plaintext
// with nil error. The errors.Is(ErrOpen) assertion fails.
func TestEncryptorTamperFails(t *testing.T) {
	t.Parallel()
	e := newTestEncryptor(t)
	aad := []byte("model=m")
	ct, _ := e.Seal([]byte("secret payload"), aad)

	// Flip a bit in the last byte (inside the GCM tag/ciphertext region).
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := e.Open(tampered, aad); !errors.Is(err, ErrOpen) {
		t.Fatalf("tampered Open err = %v, want ErrOpen", err)
	}
}

// TestEncryptorAADBinding proves the associated data is authenticated: opening
// with a different aad fails.
//
// MUTATION: in Open, pass `nil` as aad to aead.Open. Then the wrong-aad call
// would succeed and the expected ErrOpen assertion fails.
func TestEncryptorAADBinding(t *testing.T) {
	t.Parallel()
	e := newTestEncryptor(t)
	ct, _ := e.Seal([]byte("payload"), []byte("model=alpha"))
	if _, err := e.Open(ct, []byte("model=beta")); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong-aad Open err = %v, want ErrOpen", err)
	}
}

// TestEncryptorKeySize proves a non-32-byte key is rejected.
//
// MUTATION: remove the len(key)!=32 check. NewReferenceEncryptor would accept a
// short key (or fail later) and the ErrKeySize assertion fails.
func TestEncryptorKeySize(t *testing.T) {
	t.Parallel()
	if _, err := NewReferenceEncryptor([]byte("short")); !errors.Is(err, ErrKeySize) {
		t.Fatalf("err = %v, want ErrKeySize", err)
	}
}

// TestProxyWritesAuditEntry proves the EncryptedInferenceProxy seals a request
// AND writes an audit entry with the exact who/when/model/TEE/algorithm/bytes
// fields, and that the sealed payload round-trips back through OpenResponse.
//
// MUTATION: in SealRequest, delete the p.sink.Append(...) call. Entries() would
// be empty and the "exactly one audit entry" assertion fails.
func TestProxyWritesAuditEntry(t *testing.T) {
	t.Parallel()
	e := newTestEncryptor(t)
	sink := &MemoryAuditSink{}
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	proxy, err := NewEncryptedInferenceProxy(ProxyConfig{
		Encryptor: e,
		Sink:      sink,
		TEE:       true,
		Now:       func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("NewEncryptedInferenceProxy: %v", err)
	}

	plaintext := []byte("inference prompt")
	ct, err := proxy.SealRequest("alice@helix", "qwen-2.5", plaintext)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}

	entries := sink.Entries()
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Principal != "alice@helix" {
		t.Fatalf("principal = %q, want alice@helix", got.Principal)
	}
	if got.Model != "qwen-2.5" {
		t.Fatalf("model = %q, want qwen-2.5", got.Model)
	}
	if !got.TEE {
		t.Fatalf("TEE = false, want true")
	}
	if got.Algorithm != "AES-256-GCM" {
		t.Fatalf("algorithm = %q, want AES-256-GCM", got.Algorithm)
	}
	if !got.Time.Equal(fixed) {
		t.Fatalf("time = %v, want %v", got.Time, fixed)
	}
	if got.PlaintextBytes != len(plaintext) {
		t.Fatalf("plaintext bytes = %d, want %d", got.PlaintextBytes, len(plaintext))
	}
	if got.CiphertextBytes != len(ct) {
		t.Fatalf("ciphertext bytes = %d, want %d", got.CiphertextBytes, len(ct))
	}
	if got.CiphertextBytes <= got.PlaintextBytes {
		t.Fatalf("ciphertext bytes %d must exceed plaintext %d (nonce+tag overhead)",
			got.CiphertextBytes, got.PlaintextBytes)
	}

	// The sealed request, bound to the model as aad, must round-trip.
	back, err := proxy.OpenResponse("qwen-2.5", ct)
	if err != nil {
		t.Fatalf("OpenResponse: %v", err)
	}
	if !bytes.Equal(back, plaintext) {
		t.Fatalf("round-trip = %q, want %q", back, plaintext)
	}
}

// TestProxyRequiresSeams proves a proxy without an encryptor or sink is
// rejected.
//
// MUTATION: drop the nil checks in NewEncryptedInferenceProxy. The constructor
// would return a usable proxy and the expected errors disappear.
func TestProxyRequiresSeams(t *testing.T) {
	t.Parallel()
	if _, err := NewEncryptedInferenceProxy(ProxyConfig{Sink: &MemoryAuditSink{}}); err == nil {
		t.Fatalf("expected error for nil Encryptor")
	}
	if _, err := NewEncryptedInferenceProxy(ProxyConfig{Encryptor: newTestEncryptor(t)}); err == nil {
		t.Fatalf("expected error for nil Sink")
	}
}
