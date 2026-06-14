package gpuattest

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

// runUUIDAdversarial scopes the evidence lines for this adversarial suite so the
// captured accept/reject output is traceable to this file.
const runUUIDAdversarial = "gpuattest-ADV-7f31c0d2-4a85-4b19-9c63-2e0f8d1a6b44"

// --- Trust model recap (attest.go) ---------------------------------------
// A GPU attestation here is: device signs ed25519 over signedMessage =
// nonce || Fingerprint(descriptor) || tick, where Fingerprint is SHA-256 over
// the canonical (length-prefixed) descriptor {Vendor,Model,UUID,VRAM,PubKey}.
// Verify(c, r, d, now) pins the TRUSTED descriptor d (the known-good identity
// the relying party expects) and checks, in order: expiry, replay(single-use
// nonce), forged-descriptor (Fingerprint(d)==r.Fingerprint), wrong-key
// (d.PubKey==r.SignerPub), tampered (ed25519.Verify under d.PubKey), and
// nonce-rebinding (r.Nonce==c.Nonce). The descriptor d.PubKey is the trust
// anchor: r.SignerPub is NOT trusted on its own — it must equal the pinned key.
//
// These tests attack each cryptographic guard with REAL forged/tampered/stale
// evidence. Every negative is constructed so that removing the specific guard
// it targets would make Verify accept the forgery (stated per-test).

// TestAdversarial_SelfConsistentForgedAttestation is the CORE bypass test.
//
// SCENARIO: an attacker controls their OWN GPU/device (own ed25519 key, own
// descriptor) and crafts a FULLY SELF-CONSISTENT attestation: the signature is
// genuine over the attacker's descriptor fingerprint, SignerPub is the
// attacker's real public key, the nonce is the live challenge nonce, and the
// tick is in-window. The ONLY thing wrong is that this is NOT the device the
// relying party trusts. The verifier pins the genuine device's descriptor
// (genuine.Descriptor) as the trust anchor.
//
// SECURITY PROPERTY: a self-consistent attestation from an UNTRUSTED key/identity
// MUST be rejected. If it were accepted, ANY attacker could mint a valid-looking
// GPU attestation for any node by signing with their own key — total bypass.
//
// Expected: ErrForgedDescriptor (the attacker's descriptor fingerprint != the
// trusted descriptor's fingerprint, so it is caught at guard (3) before even the
// key/signature checks). This proves the verifier anchors trust in the PINNED
// descriptor, not in the self-asserted r.SignerPub / r.Fingerprint.
//
// MUTATION (why it bites): if Verify trusted the response's self-asserted key
// instead of the pinned descriptor — e.g. compute wantFP := r.Fingerprint, or
// verify under r.SignerPub instead of d.PubKey, or drop guard (3) — this forged
// attestation would VERIFY (nil). A non-nil rejection here is the proof that
// such a substitution is NOT present.
func TestAdversarial_SelfConsistentForgedAttestation(t *testing.T) {
	v := NewVerifier()

	genuine, err := NewDevice("NVIDIA", "H100", "uuid-GENUINE", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice(genuine): %v", err)
	}
	attacker, err := NewDevice("EVILCORP", "FAKE-GPU", "uuid-ATTACKER", 1<<30)
	if err != nil {
		t.Fatalf("NewDevice(attacker): %v", err)
	}

	c, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Attacker produces a GENUINE attestation for THEIR OWN device, on the live
	// challenge. It is internally perfect — just from the wrong identity.
	forged := attacker.Respond(c, 150)

	// Sanity: the forged attestation is internally self-consistent (a valid
	// signature by attacker over attacker's own fingerprint). If this weren't
	// valid, a later rejection could be mis-attributed to a broken signature.
	if !ed25519.Verify(forged.SignerPub,
		signedMessage(forged.Nonce, forged.Fingerprint, forged.Tick), forged.Sig) {
		t.Fatalf("test setup: forged attestation must be internally valid")
	}
	if forged.Fingerprint == Fingerprint(genuine.Descriptor) {
		t.Fatalf("test setup: attacker fingerprint must differ from trusted one")
	}

	t.Logf("run=%s before: presenting attacker's self-consistent attestation against pinned genuine descriptor", runUUIDAdversarial)
	// Verify against the PINNED, TRUSTED descriptor (genuine.Descriptor).
	err = v.Verify(c, forged, genuine.Descriptor, 150)
	t.Logf("run=%s after: self-consistent forged attestation -> err=%v", runUUIDAdversarial, err)
	if err == nil {
		t.Fatalf("SECURITY BUG: self-consistent forged attestation from UNTRUSTED key was ACCEPTED")
	}
	if !errors.Is(err, ErrForgedDescriptor) {
		t.Fatalf("want ErrForgedDescriptor for forged identity, got %v", err)
	}
}

// TestAdversarial_ForgedDescriptorReSignedByAttackerKey hardens the prior case:
// here the attacker makes the PRESENTED-vs-SIGNED fingerprints AGREE by signing
// over the trusted descriptor's fingerprint but with the ATTACKER's key, and
// also sets SignerPub to the attacker's key so the response is self-consistent.
// This slips PAST guard (3) forged-descriptor (fingerprints match) and lands on
// guard (4) wrong-key, because the trusted descriptor's pinned PubKey is NOT the
// attacker's key.
//
// SECURITY PROPERTY: even when an attacker can make the fingerprint match (e.g.
// by replaying a known descriptor), a signature under a key OTHER than the pinned
// descriptor key MUST be rejected. Only the trusted device's key may sign.
//
// MUTATION (why it bites): if guard (4) verified under r.SignerPub instead of
// d.PubKey (i.e. trusted the self-asserted signer), this attacker-signed but
// fingerprint-matching attestation would VERIFY. Rejection proves the verifier
// binds the signature to the PINNED key.
func TestAdversarial_ForgedDescriptorReSignedByAttackerKey(t *testing.T) {
	v := NewVerifier()

	genuine, err := NewDevice("NVIDIA", "H100", "uuid-GENUINE", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice(genuine): %v", err)
	}
	attacker, err := NewDevice("EVILCORP", "FAKE", "uuid-ATTACKER", 1<<30)
	if err != nil {
		t.Fatalf("NewDevice(attacker): %v", err)
	}

	c, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Attacker signs over the TRUSTED device's fingerprint (so guard (3) passes)
	// but with their OWN key, and claims their own key as the signer.
	trustedFP := Fingerprint(genuine.Descriptor)
	forged := Response{
		Nonce:       c.Nonce,
		Fingerprint: trustedFP,
		Tick:        150,
		SignerPub:   attacker.Descriptor.PubKey,
		Sig:         ed25519.Sign(attacker.priv, signedMessage(c.Nonce, trustedFP, 150)),
	}

	// Sanity: fingerprint matches (passes guard 3) AND the signature is valid
	// under the attacker's own self-asserted key (so the ONLY failing guard is
	// the key-pinning check, not a broken signature).
	if forged.Fingerprint != Fingerprint(genuine.Descriptor) {
		t.Fatalf("test setup: forged fingerprint must equal trusted fingerprint")
	}
	if !ed25519.Verify(forged.SignerPub,
		signedMessage(forged.Nonce, forged.Fingerprint, forged.Tick), forged.Sig) {
		t.Fatalf("test setup: forged signature must be valid under attacker key")
	}

	t.Logf("run=%s before: attacker-signed attestation with MATCHING fingerprint, untrusted key", runUUIDAdversarial)
	err = v.Verify(c, forged, genuine.Descriptor, 150)
	t.Logf("run=%s after: fingerprint-matching, attacker-keyed attestation -> err=%v", runUUIDAdversarial, err)
	if err == nil {
		t.Fatalf("SECURITY BUG: attestation signed by UNTRUSTED key (fingerprint matched) was ACCEPTED")
	}
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("want ErrWrongKey for untrusted signer, got %v", err)
	}
}

// TestAdversarial_SignatureByteTamper flips a single byte of an otherwise genuine
// signature. Unlike the existing tampered-FIELD tests (which alter Tick/Nonce),
// this corrupts the signature blob ITSELF — the literal "tamper the signature"
// adversary. Every signed field is genuine; only r.Sig is corrupted.
//
// SECURITY PROPERTY: a corrupted/forged signature MUST NOT verify under the
// genuine key. ed25519.Verify rejects it -> ErrTampered.
//
// MUTATION (why it bites): if guard (5) skipped ed25519.Verify (e.g. returned
// nil whenever key+fingerprint matched), a garbage signature would be ACCEPTED.
// Rejection proves the signature is actually checked.
func TestAdversarial_SignatureByteTamper(t *testing.T) {
	v := NewVerifier()
	d, err := NewDevice("NVIDIA", "H100", "uuid-SIGTAMPER", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	c, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := d.Respond(c, 150)

	// Corrupt one byte of the signature (last byte; flip all bits).
	if len(r.Sig) == 0 {
		t.Fatalf("test setup: signature must be non-empty")
	}
	orig := r.Sig[len(r.Sig)-1]
	r.Sig[len(r.Sig)-1] ^= 0xFF

	// Sanity: the corrupted signature is NOT valid under the genuine key, so a
	// rejection is attributable to the corrupted signature, not anything else.
	if ed25519.Verify(d.Descriptor.PubKey,
		signedMessage(r.Nonce, r.Fingerprint, r.Tick), r.Sig) {
		t.Fatalf("test setup: tampered signature must not validate")
	}

	t.Logf("run=%s before: genuine sig last byte 0x%02x -> 0x%02x (corrupted)", runUUIDAdversarial, orig, r.Sig[len(r.Sig)-1])
	err = v.Verify(c, r, d.Descriptor, 150)
	t.Logf("run=%s after: byte-tampered signature -> err=%v", runUUIDAdversarial, err)
	if err == nil {
		t.Fatalf("SECURITY BUG: byte-tampered signature was ACCEPTED")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("want ErrTampered for corrupted signature, got %v", err)
	}
}

// TestAdversarial_MeasurementClaimTamper proves that tampering an attested
// IDENTITY/MEASUREMENT claim — here the Vendor and (separately) Model fields of
// the descriptor, which together with UUID/VRAM are this model's "measurement"
// of the device — is rejected. A different/compromised firmware-or-identity claim
// must NOT pass against the pinned known-good descriptor.
//
// This complements the existing forged-descriptor test (which alters VRAM): here
// we alter the STRING identity claims, exercising the canonical length-prefixed
// encoding for those fields.
//
// SECURITY PROPERTY: any change to ANY measured field changes Fingerprint(d), so
// the presented descriptor no longer hashes to the signed fingerprint ->
// ErrForgedDescriptor. Only the exact known-good measurement is accepted.
//
// MUTATION (why it bites): if Fingerprint ignored a field (e.g. dropped Vendor
// from canonical()), the Vendor-tamper case below would hash equal and VERIFY.
// Rejection of BOTH sub-cases proves Vendor and Model are bound into the
// fingerprint.
func TestAdversarial_MeasurementClaimTamper(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d *Descriptor)
	}{
		{"vendor_tamper", func(d *Descriptor) { d.Vendor = "EVILCORP" }},
		{"model_tamper", func(d *Descriptor) { d.Model = "BACKDOORED-GPU" }},
		{"uuid_tamper", func(d *Descriptor) { d.UUID = "uuid-SWAPPED" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			v := NewVerifier()
			d, err := NewDevice("NVIDIA", "H100", "uuid-MEASURE", 80<<30)
			if err != nil {
				t.Fatalf("NewDevice: %v", err)
			}
			c, err := v.Issue(100, 200)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			r := d.Respond(c, 150) // signs over the GENUINE measurement

			// Present a descriptor whose measured claim has been altered.
			forged := d.Descriptor
			tc.mutate(&forged)

			// Sanity: the altered claim genuinely changed the fingerprint.
			if Fingerprint(forged) == r.Fingerprint {
				t.Fatalf("test setup: mutated descriptor must change the fingerprint")
			}

			err = v.Verify(c, r, forged, 150)
			t.Logf("run=%s %s: tampered measurement claim -> err=%v", runUUIDAdversarial, tc.name, err)
			if err == nil {
				t.Fatalf("SECURITY BUG: tampered measurement (%s) was ACCEPTED", tc.name)
			}
			if !errors.Is(err, ErrForgedDescriptor) {
				t.Fatalf("want ErrForgedDescriptor for %s, got %v", tc.name, err)
			}
		})
	}
}

// TestAdversarial_StaleAttestationReplayedAfterExpiry proves a previously-genuine
// attestation cannot be replayed once its challenge window has closed. The
// attestation was 100% valid at production time; the attacker captures it and
// re-presents it later (nowTick past ExpiryTick).
//
// SECURITY PROPERTY: freshness is enforced by the expiry window — a stale
// (expired) attestation is rejected even though its signature is still
// cryptographically valid. This prevents indefinite replay of an old genuine
// attestation. A fresh presentation (in-window) of the SAME attestation passes,
// proving it is the staleness, not the attestation, that is rejected.
//
// MUTATION (why it bites): removing guard (1) expiry (nowTick > c.ExpiryTick)
// would ACCEPT the stale replay. The in-window control sub-case ensures the
// rejection is specifically due to expiry, not a broken attestation.
func TestAdversarial_StaleAttestationReplayedAfterExpiry(t *testing.T) {
	d, err := NewDevice("NVIDIA", "H100", "uuid-STALE", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}

	// Control: the SAME attestation, verified IN-WINDOW, is accepted. Uses its
	// own verifier so the nonce is not yet consumed.
	{
		vFresh := NewVerifier()
		c, err := vFresh.Issue(100, 200)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		r := d.Respond(c, 150)
		if err := vFresh.Verify(c, r, d.Descriptor, 199); err != nil {
			t.Fatalf("control: in-window genuine attestation rejected: %v", err)
		}
		t.Logf("run=%s control: in-window (now=199, expiry=200) attestation ACCEPTED", runUUIDAdversarial)
	}

	// Attack: capture a genuine attestation, replay it AFTER expiry.
	vStale := NewVerifier()
	c, err := vStale.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := d.Respond(c, 150)

	// The captured attestation's signature is still valid in isolation...
	if !ed25519.Verify(r.SignerPub, signedMessage(r.Nonce, r.Fingerprint, r.Tick), r.Sig) {
		t.Fatalf("test setup: captured attestation signature must still be valid")
	}
	// ...but presented past the expiry window it must be rejected as stale.
	t.Logf("run=%s before: replaying captured attestation at now=201 (expiry=200)", runUUIDAdversarial)
	err = vStale.Verify(c, r, d.Descriptor, 201)
	t.Logf("run=%s after: stale replay (now=201 > expiry=200) -> err=%v", runUUIDAdversarial, err)
	if err == nil {
		t.Fatalf("SECURITY BUG: stale (expired) attestation replay was ACCEPTED")
	}
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired for stale replay, got %v", err)
	}
}

// TestAdversarial_CrossChallengeReplay proves a genuine attestation minted for
// challenge A cannot be replayed to satisfy a DIFFERENT live challenge B. The
// attacker captures device's valid response to challenge A and presents it where
// the verifier issued (and expects) challenge B.
//
// SECURITY PROPERTY: the signed nonce binds an attestation to ONE specific
// challenge. Presenting it under a different challenge nonce is rejected, so a
// genuine attestation cannot be lifted from one verification context into
// another. This is the replay-across-challenges defense.
//
// MUTATION (why it bites): removing the nonce-rebinding guard (r.Nonce==c.Nonce
// at attest.go) would ACCEPT response-to-A under challenge-B (every other guard
// passes: in-window, B.Nonce unseen, fingerprint+key+signature all genuine over
// A's nonce). Rejection proves the binding is enforced.
func TestAdversarial_CrossChallengeReplay(t *testing.T) {
	v := NewVerifier()
	d, err := NewDevice("NVIDIA", "H100", "uuid-XREPLAY", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}

	// Two distinct live challenges from the same verifier.
	chA, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	chB, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue B: %v", err)
	}
	if chA.Nonce == chB.Nonce {
		t.Fatalf("test setup: two issued challenges must have distinct nonces")
	}

	// Genuine response to A.
	rA := d.Respond(chA, 150)

	// The response is genuinely valid for A's nonce (so only the cross-challenge
	// binding can be what rejects it under B).
	if !ed25519.Verify(rA.SignerPub, signedMessage(rA.Nonce, rA.Fingerprint, rA.Tick), rA.Sig) {
		t.Fatalf("test setup: response to A must be valid over A's nonce")
	}

	t.Logf("run=%s before: presenting response(A) under challenge B (B.Nonce[0]=%d, rA.Nonce[0]=%d)", runUUIDAdversarial, chB.Nonce[0], rA.Nonce[0])
	err = v.Verify(chB, rA, d.Descriptor, 150) // present A's response under B
	t.Logf("run=%s after: response(A) under challenge(B) -> err=%v", runUUIDAdversarial, err)
	if err == nil {
		t.Fatalf("SECURITY BUG: a genuine attestation for challenge A was accepted under challenge B")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("want ErrTampered for cross-challenge replay, got %v", err)
	}

	// And the GENUINE pairing (response to B under challenge B) is accepted,
	// proving it is the cross-binding, not the device, that was rejected.
	rB := d.Respond(chB, 150)
	if err := v.Verify(chB, rB, d.Descriptor, 150); err != nil {
		t.Fatalf("control: genuine response(B) under challenge(B) rejected: %v", err)
	}
	t.Logf("run=%s control: genuine response(B) under challenge(B) ACCEPTED", runUUIDAdversarial)
}

// TestAdversarial_EmptyAndShortSignatureRejected proves degenerate signatures
// (nil / wrong-length) do not slip through as accepted. An attacker who omits or
// truncates the signature must be rejected, not granted access via some
// length-blind shortcut.
//
// SECURITY PROPERTY: ed25519.Verify returns false for a signature that is not
// exactly SignatureSize bytes (and for an empty one), so guard (5) rejects it ->
// ErrTampered. No path treats "no signature" as "no objection".
//
// MUTATION (why it bites): if guard (5) were bypassed when len(r.Sig)==0 (a
// classic "empty means skip" bug), the nil-signature case would be ACCEPTED.
func TestAdversarial_EmptyAndShortSignatureRejected(t *testing.T) {
	sigs := map[string][]byte{
		"nil_signature":   nil,
		"empty_signature": {},
		"short_signature": make([]byte, ed25519.SignatureSize-1),
		"zero_signature":  make([]byte, ed25519.SignatureSize), // all-zero, correct length
	}
	for name, badSig := range sigs {
		name, badSig := name, badSig
		t.Run(name, func(t *testing.T) {
			v := NewVerifier()
			d, err := NewDevice("NVIDIA", "H100", "uuid-DEGEN", 80<<30)
			if err != nil {
				t.Fatalf("NewDevice: %v", err)
			}
			c, err := v.Issue(100, 200)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			r := d.Respond(c, 150)
			r.Sig = badSig // replace with degenerate signature

			err = v.Verify(c, r, d.Descriptor, 150)
			t.Logf("run=%s %s (len=%d) -> err=%v", runUUIDAdversarial, name, len(badSig), err)
			if err == nil {
				t.Fatalf("SECURITY BUG: degenerate signature (%s) was ACCEPTED", name)
			}
			if !errors.Is(err, ErrTampered) {
				t.Fatalf("want ErrTampered for %s, got %v", name, err)
			}
		})
	}
}

// TestAdversarial_GenuineBaselineEvidence is the explicit ACCEPT baseline for the
// adversarial suite: a fully genuine attestation verifies (nil) and yields the
// expected bound claims (fingerprint == Fingerprint(trusted descriptor), signer
// == descriptor key, nonce == challenge nonce). This anchors every rejection
// above: the SAME machinery that accepts genuine evidence rejects the forgeries.
func TestAdversarial_GenuineBaselineEvidence(t *testing.T) {
	v := NewVerifier()
	d, err := NewDevice("NVIDIA", "H100", "uuid-GENUINE-BASE", 80<<30)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	c, err := v.Issue(100, 200)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := d.Respond(c, 150)

	t.Logf("run=%s before: verifying genuine attestation (in-window, correct identity+key+nonce)", runUUIDAdversarial)
	if err := v.Verify(c, r, d.Descriptor, 150); err != nil {
		t.Fatalf("genuine attestation rejected: %v", err)
	}
	// Bound-claim assertions: the accepted evidence carries exactly the trusted
	// identity, the trusted key, and the live challenge nonce.
	if r.Fingerprint != Fingerprint(d.Descriptor) {
		t.Fatalf("accepted evidence fingerprint != trusted descriptor fingerprint")
	}
	if !d.Descriptor.PubKey.Equal(r.SignerPub) {
		t.Fatalf("accepted evidence signer != trusted descriptor key")
	}
	if r.Nonce != c.Nonce {
		t.Fatalf("accepted evidence nonce != challenge nonce")
	}
	t.Logf("run=%s after: genuine attestation ACCEPTED(nil); claims bound to trusted identity/key/nonce", runUUIDAdversarial)
}
