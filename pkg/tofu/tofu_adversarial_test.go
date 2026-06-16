package tofu

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"math/rand"
	"testing"
)

// advKey deterministically derives an independent ed25519 keypair from seed so
// these adversarial probes are fully reproducible (no host entropy / no clock).
func advKey(t *testing.T, seed int64) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	pub, priv, err := ed25519.GenerateKey(r)
	if err != nil {
		t.Fatalf("GenerateKey(seed=%d): %v", seed, err)
	}
	return pub, priv
}

// TestCrossNodeIDSignatureReplayRejected is the highest-value TOFU correctness
// probe. The package promises (tofu.go:47-57) that the self-signature is bound
// to the claimed nodeID via canonicalMessage(nodeID). An attacker who observes a
// genuine (pub, sig) self-signed by a node for nodeID "A" must NOT be able to
// replay that SAME (pub, sig) to first-pin a DIFFERENT nodeID "B" — because the
// signature was made over canonicalMessage("A"), not canonicalMessage("B").
//
// This is a fail-open of the worst kind: if VerifyAndPin verified the signature
// over a constant/nodeID-independent message, a single captured self-signature
// would let an attacker pin keys for arbitrary nodeIDs it does not control.
//
// SINK-SIDE: we assert both the returned error (ErrBadSignature) AND that
// nodeID "B" remains UNPINNED afterwards — proving nothing was trusted.
func TestCrossNodeIDSignatureReplayRejected(t *testing.T) {
	const nodeA = "node-A-genuine"
	const nodeB = "node-B-victim"

	pub, priv := advKey(t, 11)
	// Genuine self-signature for nodeA only.
	sigForA := Sign(priv, nodeA)

	s := NewStore()

	// Sanity: the captured signature really is valid for nodeA (so a rejection
	// for nodeB is a genuine nodeID-binding rejection, not a broken signature).
	if !ed25519.Verify(pub, canonicalMessage(nodeA), sigForA) {
		t.Fatalf("setup invalid: genuine sig does not verify for nodeA")
	}
	// And it must NOT verify for nodeB's canonical message — that is the binding.
	if ed25519.Verify(pub, canonicalMessage(nodeB), sigForA) {
		t.Fatalf("setup invalid: sig for nodeA also verifies for nodeB (no binding)")
	}

	// Attacker replays nodeA's (pub, sig) against nodeB on FIRST contact.
	err := s.VerifyAndPin(nodeB, pub, sigForA)
	t.Logf("replay sig(A) -> nodeB err=%v", err)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("cross-nodeID replay accepted: sig bound to %q was NOT rejected for %q; got err=%v", nodeA, nodeB, err)
	}

	// SINK: nodeB must remain unpinned — nothing forged was trusted.
	if _, ok := s.Pinned(nodeB); ok {
		t.Fatalf("FAIL-OPEN: a replayed signature pinned a key for victim nodeID %q", nodeB)
	}
}

// TestDefensiveCopyOnPin proves the docstring guarantee (tofu.go:121-124): the
// store pins a DEFENSIVE COPY, so a caller mutating its own pub slice AFTER a
// successful first-contact pin cannot retroactively alter what the store trusts.
//
// Without the copy, the store would hold the caller's backing array; a later
// in-place mutation (or buffer reuse) by the caller would silently change the
// pinned key — a TOCTOU that could turn a future spoof key into an accepted pin.
//
// SINK-SIDE: after mutating the caller slice we re-read Pinned() and assert it
// still equals the ORIGINAL key bytes, not the mutated ones.
func TestDefensiveCopyOnPin(t *testing.T) {
	const nodeID = "node-copy"
	pub, priv := advKey(t, 12)
	sig := Sign(priv, nodeID)

	// Keep an immutable snapshot of the original key bytes for comparison.
	original := make([]byte, len(pub))
	copy(original, pub)

	s := NewStore()
	if err := s.VerifyAndPin(nodeID, pub, sig); err != nil {
		t.Fatalf("genuine first contact rejected: %v", err)
	}

	// Caller now mutates ITS OWN slice in place (e.g. buffer reuse).
	for i := range pub {
		pub[i] ^= 0xFF
	}
	if bytes.Equal(pub, original) {
		t.Fatalf("setup invalid: mutation did not change the caller slice")
	}

	got, ok := s.Pinned(nodeID)
	t.Logf("after caller mutation: pinnedEqualsOriginal=%v pinnedEqualsMutated=%v",
		bytes.Equal(got, original), bytes.Equal(got, pub))
	if !ok {
		t.Fatalf("pin disappeared after caller mutation")
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("FAIL-OPEN: caller mutation of its slice retroactively altered the pinned key")
	}
}

// TestPinnedReturnsDefensiveCopy proves the Pinned() docstring guarantee
// (tofu.go:129-141): the returned key is a COPY, so a caller mutating the
// returned slice must NOT corrupt the store's internal pin (which would later
// cause a genuine reconnect to be wrongly rejected as ErrPinMismatch, or a
// spoof key to match).
func TestPinnedReturnsDefensiveCopy(t *testing.T) {
	const nodeID = "node-readcopy"
	pub, priv := advKey(t, 13)
	sig := Sign(priv, nodeID)

	s := NewStore()
	if err := s.VerifyAndPin(nodeID, pub, sig); err != nil {
		t.Fatalf("genuine first contact rejected: %v", err)
	}

	// Mutate the returned copy aggressively.
	out1, _ := s.Pinned(nodeID)
	for i := range out1 {
		out1[i] ^= 0xFF
	}

	// A genuine reconnect with the real key+sig must STILL be accepted, proving
	// the internal pin was untouched by the mutation of the returned slice.
	if err := s.VerifyAndPin(nodeID, pub, sig); err != nil {
		t.Fatalf("FAIL: mutating the Pinned() return value corrupted the internal pin; genuine reconnect now rejected: %v", err)
	}
	out2, ok := s.Pinned(nodeID)
	if !ok || !bytes.Equal(out2, pub) {
		t.Fatalf("FAIL: internal pin no longer equals the genuine key after returned-copy mutation")
	}
}

// TestNilAndWrongSizeSignatureRejected probes hostile signature inputs on FIRST
// contact: a nil signature and a wrong-length (non-empty) signature. The
// contract is that an invalid self-signature yields ErrBadSignature and pins
// nothing — and crucially that VerifyAndPin does NOT panic on malformed sig
// bytes (a DoS / liveness concern for a node-facing verifier).
//
// SINK-SIDE: assert ErrBadSignature AND that the nodeID stays unpinned, with a
// recover() guard so a panic surfaces as an explicit test failure.
func TestNilAndWrongSizeSignatureRejected(t *testing.T) {
	pub, _ := advKey(t, 14)

	cases := []struct {
		name string
		sig  []byte
	}{
		{"nil-sig", nil},
		{"empty-sig", []byte{}},
		{"short-sig", make([]byte, ed25519.SignatureSize-1)},
		{"long-sig", make([]byte, ed25519.SignatureSize+1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := "node-" + tc.name
			s := NewStore()

			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("VerifyAndPin panicked on %s: %v", tc.name, r)
					}
				}()
				err = s.VerifyAndPin(nodeID, pub, tc.sig)
			}()

			t.Logf("%s err=%v", tc.name, err)
			if !errors.Is(err, ErrBadSignature) {
				t.Fatalf("%s: malformed signature NOT rejected as ErrBadSignature; got err=%v", tc.name, err)
			}
			if _, ok := s.Pinned(nodeID); ok {
				t.Fatalf("%s: FAIL-OPEN — node pinned despite malformed signature", tc.name)
			}
		})
	}
}

// TestSpoofRejectedBeforeSignatureTrust proves the ORDERING guarantee in the
// docstring (tofu.go:104-108): after a nodeID is pinned, a DIFFERENT key is
// rejected with ErrPinMismatch *regardless of whether its own self-signature is
// valid*. The pin-equality check must come BEFORE (and independent of) any
// signature trust. Here the spoof presents a different key with a GENUINELY
// VALID self-signature under that different key — the ONLY thing that can stop
// it is the pin-equality check, and it must win even though the sig verifies.
//
// This complements the existing spoof test by additionally asserting the spoof
// is rejected as ErrPinMismatch (not ErrBadSignature), pinning down the order.
func TestSpoofRejectedBeforeSignatureTrust(t *testing.T) {
	const nodeID = "node-order"

	gPub, gPriv := advKey(t, 15)
	gSig := Sign(gPriv, nodeID)

	aPub, aPriv := advKey(t, 16)
	aSig := Sign(aPriv, nodeID) // valid under attacker's own key

	if bytes.Equal(gPub, aPub) {
		t.Fatalf("setup invalid: key collision")
	}
	if !ed25519.Verify(aPub, canonicalMessage(nodeID), aSig) {
		t.Fatalf("setup invalid: attacker sig does not verify under attacker key")
	}

	s := NewStore()
	if err := s.VerifyAndPin(nodeID, gPub, gSig); err != nil {
		t.Fatalf("genuine first contact rejected: %v", err)
	}

	err := s.VerifyAndPin(nodeID, aPub, aSig)
	t.Logf("spoof (valid own sig, different key) err=%v", err)
	// Must be ErrPinMismatch specifically — proving equality check precedes and
	// overrides signature trust.
	if !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("spoof with a VALID self-signature under a different key was not rejected as ErrPinMismatch; got err=%v", err)
	}
	pinned, ok := s.Pinned(nodeID)
	if !ok || !bytes.Equal(pinned, gPub) {
		t.Fatalf("FAIL-OPEN: spoof altered the pin")
	}
	if bytes.Equal(pinned, aPub) {
		t.Fatalf("FAIL-OPEN: attacker key got pinned")
	}
}
