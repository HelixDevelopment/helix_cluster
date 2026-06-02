package gpuattest

import (
	"bytes"
	"errors"
	"testing"
)

const runUUIDSeal = "gpuattest-HXC1571-0e6c8b91-7f24-4a3e-bd55-2a90c61e4f8b"

// Device-A seals a payload; device-A OpenFromDevice round-trips the exact
// plaintext; device-B (different secret) OpenFromDevice is REJECTED with a typed
// GCM auth failure and leaks no plaintext. Full closure for HXC-1571.
//
// Mutation: if deriveKey ignores deviceSecret (constant key), device-B would
// successfully open and the rejection assertion fails. If SealForDevice forgets
// to prefix the nonce, OpenFromDevice on device-A fails and the round-trip
// assertion fails.
func TestSeal_RoundTripAndCrossDeviceRejected(t *testing.T) {
	payload := []byte("attestation-report:gpu=H100;vram=80GiB;ok=true")
	secretA := []byte("device-A-root-secret-32-bytes-long!")
	secretB := []byte("device-B-root-secret-DIFFERENT-value")

	ct, err := SealForDevice(payload, secretA)
	if err != nil {
		t.Fatalf("SealForDevice: %v", err)
	}
	if bytes.Contains(ct, payload) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	t.Logf("run=%s before: device-A sealed %d-byte payload into %d-byte ct", runUUIDSeal, len(payload), len(ct))

	// Device-A opens -> exact round-trip.
	got, err := OpenFromDevice(ct, secretA)
	if err != nil {
		t.Fatalf("device-A open failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, payload)
	}
	t.Logf("run=%s after(A): device-A open round-tripped plaintext OK", runUUIDSeal)

	// Device-B opens -> rejected, no plaintext.
	got2, err := OpenFromDevice(ct, secretB)
	t.Logf("run=%s after(B): device-B open -> err=%v plaintextLen=%d", runUUIDSeal, err, len(got2))
	if !errors.Is(err, ErrSealOpen) {
		t.Fatalf("device-B open should fail with ErrSealOpen, got %v", err)
	}
	if got2 != nil {
		t.Fatalf("device-B must not recover plaintext, got %q", got2)
	}
}

// A per-seal random nonce makes two seals of the SAME payload+secret produce
// DIFFERENT ciphertext, yet both decrypt. Also, flipping a ciphertext byte must
// be caught by GCM authentication.
//
// Mutation: if the nonce were fixed/zero, the two ciphertexts would be equal and
// the inequality assertion fails. If OpenFromDevice ignored the GCM tag, the
// bit-flip sub-case would decrypt garbage instead of failing.
func TestSeal_NonceUniquenessAndTamperDetected(t *testing.T) {
	payload := []byte("same-payload")
	secret := []byte("secret-key-material")

	c1, _ := SealForDevice(payload, secret)
	c2, _ := SealForDevice(payload, secret)
	if bytes.Equal(c1, c2) {
		t.Fatalf("two seals of same payload produced identical ciphertext (nonce not random)")
	}
	p1, err1 := OpenFromDevice(c1, secret)
	p2, err2 := OpenFromDevice(c2, secret)
	if err1 != nil || err2 != nil || !bytes.Equal(p1, payload) || !bytes.Equal(p2, payload) {
		t.Fatalf("both distinct ciphertexts must decrypt: e1=%v e2=%v", err1, err2)
	}
	t.Logf("run=%s nonce-unique: c1!=c2 but both decrypt to payload", runUUIDSeal)

	// Tamper a byte in the ciphertext body (past the nonce prefix).
	bad := append([]byte(nil), c1...)
	bad[len(bad)-1] ^= 0x80
	_, err := OpenFromDevice(bad, secret)
	t.Logf("run=%s tamper: flipped last ct byte -> err=%v", runUUIDSeal, err)
	if !errors.Is(err, ErrSealOpen) {
		t.Fatalf("tampered ciphertext should fail auth, got %v", err)
	}
}
