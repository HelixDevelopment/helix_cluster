package gpuattest

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

const runUUIDAttest = "gpuattest-HXC1568-3f2a7c10-9b41-4e6d-8c2f-1d0a5e8b7c30"

// Genuine challenge/response round-trip MUST be accepted, and each of the five
// attack variants MUST be rejected with its OWN distinct typed error. This is
// the full closure for HXC-1568.
//
// Mutation: if Verify drops any single check (e.g. always return nil, or skip
// the expiry / replay / fingerprint / signature comparison), at least one
// sub-assertion below flips from its expected sentinel to nil and the test
// fails. The genuine sub-test fails if signedMessage layout diverges between
// Respond and Verify.
func TestVerify_GenuineAndFiveAttacks(t *testing.T) {
	mkDevice := func() *Device {
		d, err := NewDevice("NVIDIA", "H100", "uuid-AAAA", 80<<30)
		if err != nil {
			t.Fatalf("NewDevice: %v", err)
		}
		return d
	}

	// ---- genuine ----
	t.Run("genuine_accepts", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, err := v.Issue(100, 200)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		r := d.Respond(c, 150)
		t.Logf("run=%s before: verify(genuine) pending", runUUIDAttest)
		if err := v.Verify(c, r, d.Descriptor, 150); err != nil {
			t.Fatalf("genuine response rejected: %v", err)
		}
		t.Logf("run=%s after: verify(genuine)=accepted(nil)", runUUIDAttest)
	})

	// ---- (1) expired ----
	t.Run("expired_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, _ := v.Issue(100, 200)
		r := d.Respond(c, 150)
		// nowTick 201 is past expiry 200.
		err := v.Verify(c, r, d.Descriptor, 201)
		t.Logf("run=%s expired: nowTick=201 expiry=200 -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrExpired) {
			t.Fatalf("want ErrExpired, got %v", err)
		}
	})

	// ---- (2) replayed ----
	t.Run("replay_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, _ := v.Issue(100, 200)
		r := d.Respond(c, 150)
		if err := v.Verify(c, r, d.Descriptor, 150); err != nil {
			t.Fatalf("first verify should pass: %v", err)
		}
		t.Logf("run=%s replay before: first verify accepted, nonce consumed", runUUIDAttest)
		err := v.Verify(c, r, d.Descriptor, 150) // same nonce again
		t.Logf("run=%s replay after: second verify -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrReplay) {
			t.Fatalf("want ErrReplay, got %v", err)
		}
	})

	// ---- (3) tampered ----
	t.Run("tampered_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, _ := v.Issue(100, 200)
		r := d.Respond(c, 150)
		r.Tick = 151 // alter a signed field; signature no longer matches msg
		err := v.Verify(c, r, d.Descriptor, 150)
		t.Logf("run=%s tampered: signed tick 150 presented as 151 -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrTampered) {
			t.Fatalf("want ErrTampered, got %v", err)
		}
	})

	// ---- (4) wrong key ----
	t.Run("wrongkey_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		other := mkDevice() // different key pair, SAME descriptor identity fields? no — own
		c, _ := v.Issue(100, 200)
		// Device 'other' signs, but we present d's descriptor whose fingerprint
		// 'other' must match. Build a response: other signs over d's fingerprint
		// so the fingerprint matches the descriptor but the KEY does not.
		fp := Fingerprint(d.Descriptor)
		r := Response{
			Nonce:       c.Nonce,
			Fingerprint: fp,
			Tick:        150,
			SignerPub:   other.Descriptor.PubKey, // signed by a DIFFERENT key
			Sig:         ed25519.Sign(other.priv, signedMessage(c.Nonce, fp, 150)),
		}
		err := v.Verify(c, r, d.Descriptor, 150)
		t.Logf("run=%s wrongkey: signed by other key, descriptor=d -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrWrongKey) {
			t.Fatalf("want ErrWrongKey, got %v", err)
		}
	})

	// ---- (6) nonce rebinding: VALID signature over a DIFFERENT nonce ----
	// The device genuinely signs (its own real key, real fingerprint, in-window
	// tick) but over an UNRELATED nonce, while the attacker presents challenge c.
	// Steps 1-5 all pass: expiry ok, nonce c.Nonce not yet seen, fingerprint
	// matches, key matches d.PubKey, and the signature is internally VALID over
	// r.Nonce (== otherNonce). The ONLY thing that rejects this is the
	// nonce-rebinding guard at attest.go:206-208 (r.Nonce != c.Nonce).
	//
	// Mutation: deleting lines 206-208 makes this sub-test pass with nil instead
	// of ErrTampered — i.e. a valid signature replayed over a different challenge
	// nonce would be accepted. This sub-case is the kill-test for that guard.
	t.Run("noncerebinding_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, _ := v.Issue(100, 200) // the challenge we PRESENT to Verify

		// The device signs a genuine response to an UNRELATED challenge whose
		// nonce differs from c.Nonce. Everything else (key, fingerprint, tick) is
		// genuine, so steps 1-5 cannot reject it.
		var otherNonce [32]byte
		copy(otherNonce[:], c.Nonce[:])
		otherNonce[0] ^= 0xFF // guarantee a DIFFERENT nonce
		if otherNonce == c.Nonce {
			t.Fatalf("test setup: otherNonce must differ from c.Nonce")
		}
		otherChallenge := Challenge{Nonce: otherNonce, IssueTick: 100, ExpiryTick: 200}
		r := d.Respond(otherChallenge, 150) // VALID signature over otherNonce

		// Sanity: r carries the device's real key and real fingerprint, so the
		// only mismatch vs c is the nonce — proving this isolates the guard.
		if !d.Descriptor.PubKey.Equal(r.SignerPub) {
			t.Fatalf("test setup: signer key should match descriptor")
		}
		if r.Fingerprint != Fingerprint(d.Descriptor) {
			t.Fatalf("test setup: fingerprint should match descriptor")
		}
		if r.Nonce == c.Nonce {
			t.Fatalf("test setup: r.Nonce must differ from c.Nonce for this case")
		}
		// Independent confirmation that the signature itself is valid (step 5
		// would PASS): if it weren't, the rejection could be attributed to
		// ErrTampered via the signature check rather than the nonce guard.
		if !ed25519.Verify(r.SignerPub, signedMessage(r.Nonce, r.Fingerprint, r.Tick), r.Sig) {
			t.Fatalf("test setup: signature must be valid over r.Nonce")
		}

		t.Logf("run=%s noncerebind before: c.Nonce[0]=%d r.Nonce[0]=%d (valid sig over r.Nonce)",
			runUUIDAttest, c.Nonce[0], r.Nonce[0])
		err := v.Verify(c, r, d.Descriptor, 150)
		t.Logf("run=%s noncerebind after: valid sig over different nonce -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrTampered) {
			t.Fatalf("want ErrTampered for nonce rebinding, got %v", err)
		}
	})

	// ---- (5) forged descriptor ----
	t.Run("forgeddescriptor_rejected", func(t *testing.T) {
		v := NewVerifier()
		d := mkDevice()
		c, _ := v.Issue(100, 200)
		r := d.Respond(c, 150) // signs over d's real fingerprint
		// Present a DIFFERENT descriptor (more VRAM) — its fingerprint differs
		// from the signed one.
		forged := d.Descriptor
		forged.VRAM = 999 << 30
		err := v.Verify(c, r, forged, 150)
		t.Logf("run=%s forged: presented descriptor fp != signed fp -> err=%v", runUUIDAttest, err)
		if !errors.Is(err, ErrForgedDescriptor) {
			t.Fatalf("want ErrForgedDescriptor, got %v", err)
		}
	})
}

// Fingerprint MUST be stable for equal descriptors and MUST change when ANY
// field changes — including the length-prefix ambiguity case.
//
// Mutation: if canonical() drops length-prefixing, the {Vendor:"ab",Model:"c"}
// vs {Vendor:"a",Model:"bc"} pair below collide and the inequality assertion
// fails. If Fingerprint returns a constant, the equality-after-change check
// (different VRAM) fails.
func TestFingerprint_StableAndCollisionFree(t *testing.T) {
	base := Descriptor{Vendor: "ab", Model: "c", UUID: "u", VRAM: 1, PubKey: ed25519.PublicKey{1, 2, 3}}
	same := Descriptor{Vendor: "ab", Model: "c", UUID: "u", VRAM: 1, PubKey: ed25519.PublicKey{1, 2, 3}}
	if Fingerprint(base) != Fingerprint(same) {
		t.Fatalf("identical descriptors fingerprint differently")
	}
	t.Logf("run=%s before: fp(base)==fp(same)", runUUIDAttest)

	shifted := Descriptor{Vendor: "a", Model: "bc", UUID: "u", VRAM: 1, PubKey: ed25519.PublicKey{1, 2, 3}}
	if Fingerprint(base) == Fingerprint(shifted) {
		t.Fatalf("field-boundary collision: ab|c collided with a|bc")
	}
	changed := base
	changed.VRAM = 2
	if Fingerprint(base) == Fingerprint(changed) {
		t.Fatalf("VRAM change did not alter fingerprint")
	}
	t.Logf("run=%s after: fp distinct for boundary-shift and VRAM-change", runUUIDAttest)
}
