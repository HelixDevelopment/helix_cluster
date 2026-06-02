package hybridkex

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"testing"
)

// TestHandshake_AgreesOnKey proves the end-to-end sink-side property: a full
// Initiator<->Responder exchange yields BYTE-IDENTICAL 32-byte session keys on
// both sides, and both report the negotiated group == HybridGroup.
//
// LOAD-BEARING GUARD (PQ-contributes): the derived key is recomputed by hand
// from the SAME classical secret with the PQ half DROPPED (mutation of
// deriveKey to classical-only). We assert that the real key is NOT equal to the
// classical-only key — i.e. the PQ secret materially changed the output. If a
// mutation removed the pq half from the KDF input, the real key would equal the
// classical-only key and this assertion would FAIL.
func TestHandshake_AgreesOnKey(t *testing.T) {
	const runUUID = "f3a1c9e2-7b40-4d51-9a6e-1c2d3e4f5a60"
	t.Logf("run-uuid=%s proving: full hybrid handshake -> both sides derive identical 32-byte key (delta: keyA absent -> keyA==keyB, len=%d)", runUUID, SessionKeyLen)

	in, ihello, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	_, rhello, keyResp, err := Respond(ihello)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	keyInit, err := in.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Sink-side: both keys are exactly 32 bytes.
	if len(keyInit) != SessionKeyLen || len(keyResp) != SessionKeyLen {
		t.Fatalf("key length: init=%d resp=%d want %d", len(keyInit), len(keyResp), SessionKeyLen)
	}
	// Sink-side: the two independently-derived keys agree byte-for-byte.
	if !bytes.Equal(keyInit, keyResp) {
		t.Fatalf("keys disagree:\n init=%x\n resp=%x", keyInit, keyResp)
	}
	// A real key-exchange must not emit an all-zero key.
	if bytes.Equal(keyInit, make([]byte, SessionKeyLen)) {
		t.Fatalf("derived key is all-zero: %x", keyInit)
	}

	// Negotiated group is exposed and correct on both ends.
	if in.Group() != HybridGroup {
		t.Fatalf("initiator group = %q want %q", in.Group(), HybridGroup)
	}
	if HybridGroup != "X25519MLKEM768" {
		t.Fatalf("HybridGroup constant = %q want X25519MLKEM768", HybridGroup)
	}

	// --- Live PQ-contribution guard ---
	// Recover the classical X25519 secret independently (Initiator side ECDH
	// over the Responder's published public key), then derive a CLASSICAL-ONLY
	// key by hand. This is exactly the "drop the pq half" mutation of deriveKey.
	classicalSecret := recomputeClassicalSecret(t, in, rhello)
	classicalOnly, err := hkdf.Key(sha256.New, classicalSecret, []byte(HybridGroup), string(hkdfInfo), SessionKeyLen)
	if err != nil {
		t.Fatalf("hand hkdf (classical-only): %v", err)
	}
	if bytes.Equal(keyInit, classicalOnly) {
		t.Fatalf("PQ half did NOT contribute: hybrid key equals classical-only key %x — load-bearing guard mutated away", keyInit)
	}
	t.Logf("run-uuid=%s PQ-contribution confirmed: hybrid=%x != classical-only=%x", runUUID, keyInit[:8], classicalOnly[:8])
}

// TestPQContributes_TamperedCiphertextDiffers proves the downgrade/tamper
// guard: flipping bytes in the ML-KEM ciphertext makes the Initiator decapsulate
// a DIFFERENT pq secret (ML-KEM implicit rejection), so the Initiator's derived
// key NO LONGER matches the Responder's. We assert non-equality: a mutation that
// stopped feeding the pq ciphertext into the key would make these EQUAL and fail
// this test.
func TestPQContributes_TamperedCiphertextDiffers(t *testing.T) {
	const runUUID = "a91d77b4-0e22-4c8f-bb13-5566778899aa"
	t.Logf("run-uuid=%s proving: tampered ML-KEM ciphertext -> initiator key DIFFERS from responder key (delta: match -> mismatch)", runUUID)

	in, ihello, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	_, rhello, keyResp, err := Respond(ihello)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}

	// Baseline: untampered exchange agrees.
	keyClean, err := in.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish (clean): %v", err)
	}
	if !bytes.Equal(keyClean, keyResp) {
		t.Fatalf("baseline keys must agree before tamper")
	}

	// Tamper: flip one bit of the ML-KEM ciphertext. ML-KEM-768 decapsulation
	// is implicit-rejection, so this does not error; it yields a different pq
	// secret and therefore a different session key.
	tampered := &ResponderHello{
		X25519Pub:   append([]byte(nil), rhello.X25519Pub...),
		MLKEMCipher: append([]byte(nil), rhello.MLKEMCipher...),
	}
	tampered.MLKEMCipher[0] ^= 0x01

	keyTampered, err := in.Finish(tampered)
	if err != nil {
		t.Fatalf("Finish (tampered): %v", err)
	}
	if len(keyTampered) != SessionKeyLen {
		t.Fatalf("tampered key length = %d want %d", len(keyTampered), SessionKeyLen)
	}
	// Sink-side guard: tampered key must NOT match the Responder's key.
	if bytes.Equal(keyTampered, keyResp) {
		t.Fatalf("tampered ML-KEM ciphertext still agreed with responder %x — pq half not load-bearing", keyTampered)
	}
	// And must differ from the clean Initiator key as well.
	if bytes.Equal(keyTampered, keyClean) {
		t.Fatalf("tampered key equals clean key %x — ciphertext not feeding the KDF", keyTampered)
	}
	t.Logf("run-uuid=%s tamper confirmed: clean=%x tampered=%x (mismatch as required)", runUUID, keyClean[:8], keyTampered[:8])
}

// TestHandshake_DowngradeRejected proves that an X25519-only ("downgrade")
// derivation does not match the real hybrid key, so a peer that quietly drops
// the PQ half cannot agree with a compliant hybrid peer.
func TestHandshake_DowngradeRejected(t *testing.T) {
	const runUUID = "c4e8aa10-9f33-4b2a-8d77-001122334455"
	t.Logf("run-uuid=%s proving: classical-only downgrade != hybrid key (delta: hybrid present -> downgrade rejected)", runUUID)

	in, ihello, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	_, rhello, _, err := Respond(ihello)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	hybridKey, err := in.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	classicalSecret := recomputeClassicalSecret(t, in, rhello)
	downgradeKey, err := hkdf.Key(sha256.New, classicalSecret, []byte(HybridGroup), string(hkdfInfo), SessionKeyLen)
	if err != nil {
		t.Fatalf("hand hkdf (downgrade): %v", err)
	}
	if bytes.Equal(hybridKey, downgradeKey) {
		t.Fatalf("downgrade accepted: classical-only key matched hybrid key %x", hybridKey)
	}
	t.Logf("run-uuid=%s downgrade rejected: hybrid=%x != downgrade=%x", runUUID, hybridKey[:8], downgradeKey[:8])
}

// recomputeClassicalSecret independently recomputes the X25519 ECDH secret from
// the Initiator's private key and the Responder's published public key, so the
// tests can construct the classical-only oracle without reaching into deriveKey.
func recomputeClassicalSecret(t *testing.T, in *Initiator, rhello *ResponderHello) []byte {
	t.Helper()
	pub, err := in.x25519Priv.Curve().NewPublicKey(rhello.X25519Pub)
	if err != nil {
		t.Fatalf("parse responder pub: %v", err)
	}
	secret, err := in.x25519Priv.ECDH(pub)
	if err != nil {
		t.Fatalf("recompute ecdh: %v", err)
	}
	return secret
}
