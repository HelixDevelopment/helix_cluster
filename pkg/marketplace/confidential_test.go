package marketplace

import (
	"bytes"
	"testing"
)

// key32 is a fixed 32-byte AES-256 key for deterministic tests.
var key32 = []byte("0123456789abcdef0123456789abcdef")

// TestAESGCMEncryptor_RoundTrip proves the reference Encryptor REALLY encrypts
// and decrypts: Open(Seal(pt)) == pt, and the ciphertext is not the plaintext.
// This is the "real working reference, not a no-op" guarantee.
//
// Mutation that this kills: make Seal return plaintext unchanged
// (`return plaintext, nil`). Then ciphertext == plaintext and the
// "ciphertext differs from plaintext" assertion FAILS (and Open would mis-handle
// the missing nonce). Equivalently, a no-op Open breaks the round-trip equality.
func TestAESGCMEncryptor_RoundTrip(t *testing.T) {
	enc, err := NewAESGCMEncryptor(key32)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	pt := []byte("confidential prompt: launch codes 4242")
	ct, err := enc.Seal(pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(ct, pt) {
		t.Fatalf("ciphertext equals plaintext — encryption is a no-op")
	}
	got, err := enc.Open(ct)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip = %q, want %q", got, pt)
	}
}

// TestAESGCMEncryptor_TamperDetected proves GCM authentication: a single
// flipped ciphertext byte makes Open fail.
//
// Mutation that this kills: replace AES-GCM with a stream/no-MAC construction
// (e.g. return plaintext from Open ignoring the tag). Then a tampered byte would
// decrypt "successfully" and the "expected error on tamper" assertion FAILS.
func TestAESGCMEncryptor_TamperDetected(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)
	ct, _ := enc.Seal([]byte("authentic"))
	ct[len(ct)-1] ^= 0xFF // flip a tag/ciphertext byte
	if _, err := enc.Open(ct); err == nil {
		t.Fatalf("Open accepted tampered ciphertext — authentication is broken")
	}
}

// TestAESGCMEncryptor_ShortCiphertext proves truncated input is rejected.
//
// Mutation that this kills: remove the length guard in Open; slicing
// ciphertext[:nonceSize] would panic instead of returning ErrCiphertextTooShort.
func TestAESGCMEncryptor_ShortCiphertext(t *testing.T) {
	enc, _ := NewAESGCMEncryptor(key32)
	if _, err := enc.Open([]byte{0x01}); err != ErrCiphertextTooShort {
		t.Fatalf("err = %v, want ErrCiphertextTooShort", err)
	}
}

// TestHMACAttestor_VerifyGenuine proves the reference Attestor REALLY verifies a
// genuine token for the attested offer.
//
// Mutation that this kills: make Verify always return true. That would still
// pass here — so the negative tests below are what actually pin Verify; this one
// pins that a correct token is ACCEPTED (a Verify that always returns false
// fails here).
func TestHMACAttestor_VerifyGenuine(t *testing.T) {
	att := NewHMACAttestor([]byte("attestation-secret"))
	offer := Offer{Provider: "chutes", ID: "gpu-tee-1", Confidential: true}
	tok, err := att.Attest(offer)
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if !att.Verify(offer, tok) {
		t.Fatalf("genuine token rejected — attestation verify is broken")
	}
}

// TestHMACAttestor_RejectsWrongOfferAndTamper proves attestation binds to the
// offer and to the token bytes.
//
// Mutation that this kills: make Verify return true unconditionally (or drop the
// offer from the quote so any offer verifies any token). Then a token minted for
// gpu-tee-1 would verify against a DIFFERENT offer, and the
// "reject wrong offer" assertion FAILS. The flipped-byte case kills a Verify
// that ignores the token.
func TestHMACAttestor_RejectsWrongOfferAndTamper(t *testing.T) {
	att := NewHMACAttestor([]byte("attestation-secret"))
	offer := Offer{Provider: "chutes", ID: "gpu-tee-1", Confidential: true}
	tok, _ := att.Attest(offer)

	wrongOffer := Offer{Provider: "chutes", ID: "gpu-tee-2", Confidential: true}
	if att.Verify(wrongOffer, tok) {
		t.Fatalf("token for gpu-tee-1 verified against gpu-tee-2 — binding broken")
	}

	// A non-confidential offer with the same ID must also fail (conf bit is in quote).
	plain := Offer{Provider: "chutes", ID: "gpu-tee-1", Confidential: false}
	if att.Verify(plain, tok) {
		t.Fatalf("confidential token verified against non-confidential offer")
	}

	bad := make([]byte, len(tok))
	copy(bad, tok)
	bad[0] ^= 0x01
	if att.Verify(offer, bad) {
		t.Fatalf("tampered token verified — token binding broken")
	}
}

// TestAttestor_InterfaceSeam documents that HMACAttestor satisfies Attestor and
// AESGCMEncryptor satisfies Encryptor — the exact seams the production
// digital.vasic.security submodule (proof-of-GPU / PQ E2EE) plugs into.
//
// Mutation that this kills: change a method signature (e.g. Verify -> bool to
// error) so the type no longer implements the interface; this file fails to
// COMPILE, which is a hard failure.
func TestAttestor_InterfaceSeam(t *testing.T) {
	var _ Attestor = (*HMACAttestor)(nil)
	var _ Encryptor = (*AESGCMEncryptor)(nil)
}
