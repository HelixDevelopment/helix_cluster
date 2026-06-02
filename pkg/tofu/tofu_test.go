package tofu

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"math/rand"
	"testing"
)

// runID returns a deterministic per-test "run-UUID"-style identifier. It is
// derived from the test name + a fixed seed so it is reproducible across runs
// (no time.Now / no host entropy) yet unique per test, satisfying the
// observability requirement without introducing nondeterminism into logic.
func runID(t *testing.T) string {
	t.Helper()
	h := uint64(1469598103934665603) // FNV-1a offset basis
	for _, b := range []byte(t.Name()) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		out[i] = hexd[(h>>(uint(i)*4))&0xf]
	}
	return "run-" + string(out)
}

// deterministicKey generates an ed25519 keypair from a fixed seed so the test
// is fully reproducible. seed selects distinct, independent keypairs.
func deterministicKey(t *testing.T, seed int64) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	// math/rand with a fixed seed is a deterministic, non-cryptographic source
	// — acceptable here because we only need reproducible test key material,
	// not production key security.
	r := rand.New(rand.NewSource(seed))
	pub, priv, err := ed25519.GenerateKey(r)
	if err != nil {
		t.Fatalf("GenerateKey(seed=%d): %v", seed, err)
	}
	return pub, priv
}

// TestFirstContactVerifiesAndPins covers CLOSURE clause 1: a genuine
// (nodeID, key, sig) verifies on first contact and is PINNED.
// before: Pinned==false; after: Pinned==true with that EXACT key.
//
// Mutation: if VerifyAndPin skips the s.pinned[nodeID]=... write, the "after"
// Pinned lookup returns false and this test fails.
func TestFirstContactVerifiesAndPins(t *testing.T) {
	rid := runID(t)
	const nodeID = "node-alpha"
	pub, priv := deterministicKey(t, 1)
	sig := Sign(priv, nodeID)

	s := NewStore()

	// BEFORE observable.
	_, before := s.Pinned(nodeID)
	t.Logf("%s BEFORE: nodeID=%q pinned=%v", rid, nodeID, before)
	if before {
		t.Fatalf("%s precondition failed: nodeID %q already pinned before first contact", rid, nodeID)
	}

	if err := s.VerifyAndPin(nodeID, pub, sig); err != nil {
		t.Fatalf("%s genuine first contact rejected: %v", rid, err)
	}

	// AFTER observable.
	got, after := s.Pinned(nodeID)
	t.Logf("%s AFTER: nodeID=%q pinned=%v keyMatch=%v", rid, nodeID, after, bytes.Equal(got, pub))
	if !after {
		t.Fatalf("%s expected nodeID %q to be PINNED after genuine first contact", rid, nodeID)
	}
	if !bytes.Equal(got, pub) {
		t.Fatalf("%s pinned key mismatch: pinned key is not the presented genuine key", rid)
	}
}

// TestSpoofDetectionRejectsDifferentKey covers CLOSURE clause 2: a SECOND
// endpoint presenting a DIFFERENT key (with a VALID self-signature under that
// different key) for the SAME nodeID is REJECTED with ErrPinMismatch, and the
// original pin is unchanged.
//
// Mutation: if VerifyAndPin drops the pin-equality (bytes.Equal) check and
// accepts any verifying key after first contact, the spoof would NOT return
// ErrPinMismatch (and would overwrite the pin) — both assertions below fail.
func TestSpoofDetectionRejectsDifferentKey(t *testing.T) {
	rid := runID(t)
	const nodeID = "node-beta"

	// Genuine endpoint pins first.
	genuinePub, genuinePriv := deterministicKey(t, 2)
	genuineSig := Sign(genuinePriv, nodeID)

	// Attacker endpoint: a DIFFERENT, independent keypair with its OWN valid
	// self-signature over the same nodeID. The signature is internally valid,
	// so only the pin-equality check can stop it.
	attackerPub, attackerPriv := deterministicKey(t, 3)
	attackerSig := Sign(attackerPriv, nodeID)

	if bytes.Equal(genuinePub, attackerPub) {
		t.Fatalf("%s test setup invalid: genuine and attacker keys collide", rid)
	}
	// Sanity: the attacker's signature really is valid under the attacker key,
	// so a PASS here would be a genuine spoof acceptance, not a sig failure.
	if !ed25519.Verify(attackerPub, canonicalMessage(nodeID), attackerSig) {
		t.Fatalf("%s test setup invalid: attacker self-signature does not verify", rid)
	}

	s := NewStore()
	if err := s.VerifyAndPin(nodeID, genuinePub, genuineSig); err != nil {
		t.Fatalf("%s genuine first contact rejected: %v", rid, err)
	}
	pinnedBefore, _ := s.Pinned(nodeID)
	t.Logf("%s BEFORE spoof: pinned=genuine keyMatch=%v", rid, bytes.Equal(pinnedBefore, genuinePub))

	// Spoof attempt.
	err := s.VerifyAndPin(nodeID, attackerPub, attackerSig)
	t.Logf("%s spoof attempt err=%v", rid, err)
	if !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("%s spoof with different valid key was NOT rejected as ErrPinMismatch; got err=%v", rid, err)
	}

	// Original pin must be UNCHANGED (still the genuine key, not the attacker's).
	pinnedAfter, ok := s.Pinned(nodeID)
	t.Logf("%s AFTER spoof: pinned still genuine=%v stillAttacker=%v",
		rid, bytes.Equal(pinnedAfter, genuinePub), bytes.Equal(pinnedAfter, attackerPub))
	if !ok || !bytes.Equal(pinnedAfter, genuinePub) {
		t.Fatalf("%s original pin was altered by spoof attempt", rid)
	}
	if bytes.Equal(pinnedAfter, attackerPub) {
		t.Fatalf("%s spoof key was pinned — MITM accepted", rid)
	}
}

// TestReconnectAcceptedAndBadSigFirstContactRejected covers CLOSURE clause 3:
//   (a) the SAME genuine key on reconnect is ACCEPTED (matches pin), and
//   (b) a tampered/invalid self-signature on FIRST contact is rejected with
//       ErrBadSignature and NOTHING is pinned.
//
// Mutation: if VerifyAndPin skips ed25519.Verify (signature check), the bad-sig
// first contact in part (b) would wrongly succeed and pin the key — making the
// ErrBadSignature assertion and the "nothing pinned" assertion fail.
func TestReconnectAcceptedAndBadSigFirstContactRejected(t *testing.T) {
	rid := runID(t)

	// (a) Reconnect with the same genuine key is accepted.
	const reconnectNode = "node-gamma"
	pub, priv := deterministicKey(t, 4)
	sig := Sign(priv, reconnectNode)

	s := NewStore()
	if err := s.VerifyAndPin(reconnectNode, pub, sig); err != nil {
		t.Fatalf("%s genuine first contact rejected: %v", rid, err)
	}
	pinned1, _ := s.Pinned(reconnectNode)
	t.Logf("%s BEFORE reconnect: pinned keyMatch=%v", rid, bytes.Equal(pinned1, pub))

	if err := s.VerifyAndPin(reconnectNode, pub, sig); err != nil {
		t.Fatalf("%s genuine reconnect with matching pinned key was rejected: %v", rid, err)
	}
	pinned2, ok := s.Pinned(reconnectNode)
	t.Logf("%s AFTER reconnect: accepted ok=%v keyUnchanged=%v", rid, ok, bytes.Equal(pinned2, pub))
	if !ok || !bytes.Equal(pinned2, pub) {
		t.Fatalf("%s reconnect altered or dropped the genuine pin", rid)
	}

	// (b) Bad self-signature on first contact is rejected; nothing pinned.
	const badSigNode = "node-delta"
	bsPub, bsPriv := deterministicKey(t, 5)
	goodSig := Sign(bsPriv, badSigNode)
	// Tamper: flip a bit so the signature no longer verifies. This is a real
	// invalid signature (ed25519.Verify will return false), not a faked check.
	tampered := make([]byte, len(goodSig))
	copy(tampered, goodSig)
	tampered[0] ^= 0x01
	if ed25519.Verify(bsPub, canonicalMessage(badSigNode), tampered) {
		t.Fatalf("%s test setup invalid: tampered signature still verifies", rid)
	}

	_, beforeBad := s.Pinned(badSigNode)
	t.Logf("%s BEFORE bad-sig first contact: pinned=%v", rid, beforeBad)
	if beforeBad {
		t.Fatalf("%s precondition failed: %q pinned before contact", rid, badSigNode)
	}

	err := s.VerifyAndPin(badSigNode, bsPub, tampered)
	t.Logf("%s bad-sig first contact err=%v", rid, err)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("%s bad self-signature first contact NOT rejected as ErrBadSignature; got err=%v", rid, err)
	}

	_, afterBad := s.Pinned(badSigNode)
	t.Logf("%s AFTER bad-sig first contact: pinned=%v (must stay false)", rid, afterBad)
	if afterBad {
		t.Fatalf("%s a node with a bad self-signature was wrongly PINNED", rid)
	}
}

// TestReconnectSameKeyBadSigRejected covers the reconnect re-verify branch
// (tofu.go:110-112): after a genuine first-contact pin, a reconnect presenting
// the SAME (matching) pinned key but a TAMPERED/INVALID self-signature MUST be
// rejected with ErrBadSignature. The pin-equality check passes (same key), so
// only the re-verification of the signature can reject this contact. This is
// the branch the package docstring promises ("a matching key still re-verifies
// the signature") but which no other test exercises.
//
// Mutation: if VerifyAndPin makes the reconnect ed25519.Verify a no-op (e.g.
// returns nil unconditionally once the key matches the pin), the tampered-sig
// reconnect below would wrongly succeed and this test fails (err would be nil
// instead of ErrBadSignature).
func TestReconnectSameKeyBadSigRejected(t *testing.T) {
	rid := runID(t)
	const nodeID = "node-epsilon"

	// Genuine first contact pins the real key.
	pub, priv := deterministicKey(t, 1)
	goodSig := Sign(priv, nodeID)

	s := NewStore()
	if err := s.VerifyAndPin(nodeID, pub, goodSig); err != nil {
		t.Fatalf("%s genuine first contact rejected: %v", rid, err)
	}
	pinned, _ := s.Pinned(nodeID)
	t.Logf("%s BEFORE bad-sig reconnect: pinned keyMatch=%v", rid, bytes.Equal(pinned, pub))

	// Reconnect with the SAME (matching) key but a TAMPERED signature. Flip a
	// bit so the signature genuinely fails to verify under the pinned key — this
	// is a real invalid signature, not a faked check.
	tampered := make([]byte, len(goodSig))
	copy(tampered, goodSig)
	tampered[0] ^= 0x01
	// Setup assert: the tampered signature really does NOT verify under the
	// pinned key, so a rejection below is a genuine re-verify, not a no-op.
	if ed25519.Verify(pub, canonicalMessage(nodeID), tampered) {
		t.Fatalf("%s test setup invalid: tampered signature still verifies", rid)
	}

	err := s.VerifyAndPin(nodeID, pub, tampered)
	t.Logf("%s bad-sig reconnect (matching key) err=%v", rid, err)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("%s reconnect with matching key but INVALID signature was NOT rejected as ErrBadSignature; got err=%v", rid, err)
	}

	// The original valid pin must remain unchanged after the rejected reconnect.
	pinnedAfter, ok := s.Pinned(nodeID)
	t.Logf("%s AFTER bad-sig reconnect: pinned still genuine=%v", rid, bytes.Equal(pinnedAfter, pub))
	if !ok || !bytes.Equal(pinnedAfter, pub) {
		t.Fatalf("%s rejected bad-sig reconnect altered or dropped the genuine pin", rid)
	}
}

// TestEmptyNodeIDRejected covers the input guard at tofu.go:92-94: an empty
// nodeID MUST be rejected with ErrEmptyNodeID before any pin lookup or signature
// work, and nothing is pinned for the empty key.
//
// Mutation: if the empty-nodeID guard is removed (e.g. the condition is
// neutralized to `nodeID == "" && false`), VerifyAndPin would fall through and
// attempt to verify/pin the empty nodeID. With a VALID signature over the empty
// nodeID it would return nil and pin it — so the errors.Is(ErrEmptyNodeID)
// assertion below fails.
func TestEmptyNodeIDRejected(t *testing.T) {
	rid := runID(t)
	const nodeID = "" // empty on purpose

	// Build a genuinely VALID signature over the empty nodeID, so the ONLY thing
	// that can reject this contact is the empty-nodeID guard — not a sig failure.
	pub, priv := deterministicKey(t, 2)
	sig := Sign(priv, nodeID)
	if !ed25519.Verify(pub, canonicalMessage(nodeID), sig) {
		t.Fatalf("%s test setup invalid: signature over empty nodeID does not verify", rid)
	}

	s := NewStore()
	err := s.VerifyAndPin(nodeID, pub, sig)
	t.Logf("%s empty-nodeID contact err=%v", rid, err)
	if !errors.Is(err, ErrEmptyNodeID) {
		t.Fatalf("%s empty nodeID was NOT rejected as ErrEmptyNodeID; got err=%v", rid, err)
	}

	// Nothing must have been pinned for the empty nodeID.
	_, ok := s.Pinned(nodeID)
	t.Logf("%s AFTER empty-nodeID contact: pinned=%v (must be false)", rid, ok)
	if ok {
		t.Fatalf("%s empty nodeID was wrongly PINNED despite the guard", rid)
	}
}

// TestBadKeySizeRejected covers the input guard at tofu.go:95-97: a public key
// whose length is not ed25519.PublicKeySize MUST be rejected with ErrBadKeySize
// before any signature verification, and nothing is pinned.
//
// Mutation: if the key-size guard is removed (e.g. condition neutralized to
// `len(pub) != ed25519.PublicKeySize && false`), VerifyAndPin would fall
// through to ed25519.Verify with a wrong-length key. ed25519.Verify returns
// false for a wrong-size key, so the error would become ErrBadSignature rather
// than ErrBadKeySize — making the errors.Is(ErrBadKeySize) assertion below
// fail. Thus the guard is genuinely required for this test to pass.
func TestBadKeySizeRejected(t *testing.T) {
	rid := runID(t)
	const nodeID = "node-zeta"

	pub, priv := deterministicKey(t, 3)
	sig := Sign(priv, nodeID)

	// Truncate the public key to a wrong length (one byte short of valid). The
	// signature itself is genuine, isolating the key-size guard as the cause.
	shortPub := make(ed25519.PublicKey, ed25519.PublicKeySize-1)
	copy(shortPub, pub)
	t.Logf("%s bad key size: got=%d want=%d", rid, len(shortPub), ed25519.PublicKeySize)
	if len(shortPub) == ed25519.PublicKeySize {
		t.Fatalf("%s test setup invalid: short key is actually valid length", rid)
	}

	s := NewStore()
	err := s.VerifyAndPin(nodeID, shortPub, sig)
	t.Logf("%s wrong-length-key contact err=%v", rid, err)
	if !errors.Is(err, ErrBadKeySize) {
		t.Fatalf("%s wrong-length public key was NOT rejected as ErrBadKeySize; got err=%v", rid, err)
	}

	// Nothing must have been pinned.
	_, ok := s.Pinned(nodeID)
	t.Logf("%s AFTER wrong-length-key contact: pinned=%v (must be false)", rid, ok)
	if ok {
		t.Fatalf("%s wrong-length key was wrongly PINNED despite the guard", rid)
	}
}
