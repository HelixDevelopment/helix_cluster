package attestadmit

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/gpuattest"
)

// runUUIDAdv is the forensic anchor for this adversarial suite. It is distinct
// from the existing admit_test.go runUUID so evidence lines are attributable to
// THIS file (the fail-closed / fail-open hardening wave).
const runUUIDAdv = "attestadmit-adv-HXC-1586-9b7d2c11-4e8a-4af3-bd60-1c2e7f04a9d3"

// ---------------------------------------------------------------------------
// Adversarial coverage of the admission gate's FAIL-CLOSED contract.
//
// The security property under test (admit.go doc): Admit returns true ONLY when
// gpuattest.Verifier.Verify accepts the triple (Verify == nil). EVERY other
// outcome — every sentinel error, a nil/zero attestation, a nil verifier — MUST
// yield false (deny). A node admitted WITHOUT a genuine attestation is a SEVERE
// fail-open security bug.
//
// The existing admit_test.go already bites two mutations (always-true via the
// spoof test, always-false via the all-genuine test) and covers ErrWrongKey,
// ErrExpired and the nil-verifier guard. This file closes the remaining GAP:
// the OTHER sentinel error paths (ErrTampered, ErrForgedDescriptor, ErrReplay)
// must each deny through Admit, and zero/empty attestations must deny without
// panic. Crucially it asserts the fail-open invariant DIRECTLY on Admit: for an
// attestation that Verify genuinely rejects, Admit MUST be false.
//
// ANTI-BLUFF — why each denial bites:
// Each adversarial Node is FIRST proven to be rejected by the REAL
// gpuattest.Verify against an independent probe verifier (sentinel asserted
// non-nil). The triple is therefore genuinely bad. The ONLY way Admit can
// return true for it is if the gate stops honouring Verify's verdict — i.e. the
// classic fail-open mutation `return verifier.Verify(...) == nil` ->
// `verifier.Verify(...); return true` (default-allow on error). Under that
// mutation every assertWiredDeny below flips to admitted and FAILS. The genuine
// baseline (TestAdmit_GenuineAdmitted) simultaneously bites the dual
// always-false mutation. So the suite pins Admit to EXACTLY Verify's verdict.
// ---------------------------------------------------------------------------

// genuineNodeAdv builds a Node whose Response is really produced by its device
// for a challenge issued by v, so Verify(v, ...) accepts it. Uses the SHARED
// verifier v so the issued nonce lives in v's namespace.
func genuineNodeAdv(t *testing.T, v *gpuattest.Verifier, id string, issue, expiry, prod int64) Node {
	t.Helper()
	dev, err := gpuattest.NewDevice("nvidia", "H100", "uuid-"+id, 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(%s): %v", runUUIDAdv, id, err)
	}
	ch, err := v.Issue(issue, expiry)
	if err != nil {
		t.Fatalf("%s: Issue(%s): %v", runUUIDAdv, id, err)
	}
	resp := dev.Respond(ch, prod)
	return Node{ID: id, Challenge: ch, Response: resp, Descriptor: dev.Descriptor}
}

// assertProbeRejects confirms the adversarial triple is GENUINELY bad: a fresh
// throwaway probe verifier rejects it with the expected sentinel (so the shared
// verifier's nonce is not consumed before the call under test). This is the
// anti-bluff anchor: the node is proven non-genuine independently of Admit.
func assertProbeRejects(t *testing.T, label string, n Node, nowTick int64, wantSentinel error) {
	t.Helper()
	probe := gpuattest.NewVerifier()
	err := probe.Verify(n.Challenge, n.Response, n.Descriptor, nowTick)
	if err == nil {
		t.Fatalf("%s: %s: real gpuattest.Verify ACCEPTED a triple that must be rejected — test fixture is not adversarial", runUUIDAdv, label)
	}
	if wantSentinel != nil && !errors.Is(err, wantSentinel) {
		t.Fatalf("%s: %s: real Verify rejected with %v, want sentinel %v", runUUIDAdv, label, err, wantSentinel)
	}
	t.Logf("%s: %s: real gpuattest.Verify rejects (sentinel=%v) — triple is genuinely bad", runUUIDAdv, label, err)
}

// assertWiredDeny is the core fail-closed assertion. It runs Admit with the
// SHARED verifier v against an attestation already proven bad, and demands
// deny=false. If Admit were fail-open (default-allow on Verify error) this
// FAILS: it would admit a node with no genuine attestation.
func assertWiredDeny(t *testing.T, label string, v *gpuattest.Verifier, n Node, nowTick int64) {
	t.Helper()
	admitted := Admit(v, n, nowTick)
	t.Logf("%s: %s: Admit(real verifier, bad-attestation)=%v (want false / DENY)", runUUIDAdv, label, admitted)
	if admitted {
		t.Fatalf("%s: SECURITY: %s admitted WITHOUT a genuine attestation — admission is FAIL-OPEN", runUUIDAdv, label)
	}
}

// TestAdmit_GenuineAdmitted is the positive baseline: a valid, policy-compliant
// (in-window, genuine signature, matching descriptor) attestation IS admitted.
//
// Mutation: an always-false / always-deny mutation of Admit makes this FAIL
// (genuine node denied). Paired with the deny tests below this pins Admit to
// EXACTLY Verify's verdict rather than a constant.
func TestAdmit_GenuineAdmitted(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNodeAdv(t, v, "genuine", 10, 1000, 20)

	admitted := Admit(v, n, 20)
	t.Logf("%s: BASELINE genuine node Admit=%v (want true / ADMIT)", runUUIDAdv, admitted)
	if !admitted {
		t.Fatalf("%s: genuine, in-window, correctly-signed attestation MUST be admitted but was denied", runUUIDAdv)
	}
}

// TestAdmit_TamperedSignedFieldDenied: the device signs a genuine response, then
// an attacker alters a SIGNED field (Tick) without re-signing. Verify returns
// ErrTampered; Admit must DENY.
//
// Bites the fail-open mutation: under default-allow-on-error the tampered node
// would be admitted.
func TestAdmit_TamperedSignedFieldDenied(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNodeAdv(t, v, "tampered", 10, 1000, 20)

	// Flip a signed field after signing. Sig now no longer matches the message.
	n.Response.Tick = n.Response.Tick + 1

	assertProbeRejects(t, "tampered-tick", n, 20, gpuattest.ErrTampered)
	assertWiredDeny(t, "tampered-tick", v, n, 20)
}

// TestAdmit_ForgedDescriptorDenied: the node presents a DIFFERENT descriptor
// than the one whose fingerprint was signed (identity swap). Verify returns
// ErrForgedDescriptor; Admit must DENY.
func TestAdmit_ForgedDescriptorDenied(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNodeAdv(t, v, "victim", 10, 1000, 20)

	// Swap in an unrelated descriptor; the signed Fingerprint no longer matches.
	other, err := gpuattest.NewDevice("nvidia", "H100", "uuid-other", 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(other): %v", runUUIDAdv, err)
	}
	n.Descriptor = other.Descriptor

	assertProbeRejects(t, "forged-descriptor", n, 20, gpuattest.ErrForgedDescriptor)
	assertWiredDeny(t, "forged-descriptor", v, n, 20)
}

// TestAdmit_ReplayedNonceDenied: a genuine attestation that has ALREADY been
// consumed by the verifier (its nonce is burned) must be DENIED on a second
// presentation. This proves replay protection survives the admission gate: an
// attacker capturing a once-valid attestation cannot re-present it.
//
// Note: replay state lives in the SHARED verifier, so this test legitimately
// uses one verifier for both the consuming admit and the replay admit.
func TestAdmit_ReplayedNonceDenied(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNodeAdv(t, v, "replay", 10, 1000, 20)

	// First presentation against the shared verifier: genuine -> admitted, and
	// the nonce is consumed (single-use).
	first := Admit(v, n, 20)
	t.Logf("%s: replay: first Admit=%v (want true; consumes nonce)", runUUIDAdv, first)
	if !first {
		t.Fatalf("%s: replay setup: first presentation of a genuine node should be admitted", runUUIDAdv)
	}

	// Second presentation of the SAME triple: nonce already seen -> Verify
	// returns ErrReplay -> Admit must DENY.
	second := Admit(v, n, 20)
	t.Logf("%s: replay: second Admit=%v (want false / DENY — nonce already consumed)", runUUIDAdv, second)
	if second {
		t.Fatalf("%s: SECURITY: replayed attestation admitted a second time — replay protection bypassed (FAIL-OPEN)", runUUIDAdv)
	}
}

// TestAdmit_ZeroValueNodeFailClosed: a completely empty Node (zero challenge,
// zero response, nil PubKey, nil Sig, zero descriptor) is a missing/garbage
// attestation. The gate must DENY it fail-closed and MUST NOT panic.
//
// Bites the fail-open mutation: under default-allow-on-error an empty,
// unverifiable attestation would be admitted.
func TestAdmit_ZeroValueNodeFailClosed(t *testing.T) {
	v := gpuattest.NewVerifier()

	var empty Node // all zero values: empty descriptor, nil PubKey, nil Sig.
	// nowTick 0 is within the zero ExpiryTick window (0 > 0 is false) so the
	// expiry pre-check does NOT short-circuit; the deny must come from the
	// cryptographic core rejecting an empty/garbage attestation, proving the
	// gate is genuinely fail-closed and not merely tripping the expiry guard.

	// Independent probe: the empty triple must be rejected (not accepted) and
	// must not panic.
	probe := gpuattest.NewVerifier()
	if err := probe.Verify(empty.Challenge, empty.Response, empty.Descriptor, 0); err == nil {
		t.Fatalf("%s: SECURITY: real Verify ACCEPTED an empty/zero attestation", runUUIDAdv)
	} else {
		t.Logf("%s: zero-node: real Verify rejects empty attestation (sentinel=%v)", runUUIDAdv, err)
	}

	admitted := Admit(v, empty, 0)
	t.Logf("%s: zero-node: Admit(empty attestation)=%v (want false / DENY, no panic)", runUUIDAdv, admitted)
	if admitted {
		t.Fatalf("%s: SECURITY: empty/zero attestation admitted — admission is FAIL-OPEN on missing attestation", runUUIDAdv)
	}
}

// TestAdmit_NilPubKeyDescriptorFailClosed: a descriptor with a nil/garbage
// PubKey (a node presenting an unverifiable identity) must be DENIED. ed25519
// verification with a wrong-sized key must not panic the gate.
func TestAdmit_NilPubKeyDescriptorFailClosed(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNodeAdv(t, v, "nilkey", 10, 1000, 20)

	// Strip the public key from the presented descriptor; the signed fingerprint
	// no longer matches AND there is no key to verify under.
	n.Descriptor.PubKey = nil

	admitted := Admit(v, n, 20)
	t.Logf("%s: nil-pubkey: Admit=%v (want false / DENY, no panic)", runUUIDAdv, admitted)
	if admitted {
		t.Fatalf("%s: SECURITY: node with nil descriptor PubKey admitted — FAIL-OPEN", runUUIDAdv)
	}
}

// TestPlaceable_NilVerifierAdmitsNothing: the batch path must also be fail-closed
// under a nil verifier — even a set of genuine-looking nodes yields an EMPTY
// placeable set rather than admitting everything unverified.
func TestPlaceable_NilVerifierAdmitsNothing(t *testing.T) {
	v := gpuattest.NewVerifier()
	a := genuineNodeAdv(t, v, "alpha", 10, 1000, 20)
	b := genuineNodeAdv(t, v, "beta", 10, 1000, 30)

	got := Placeable(nil, []Node{a, b}, 25)
	t.Logf("%s: Placeable(nil verifier, 2 genuine nodes)=%v (want empty / admit nothing)", runUUIDAdv, ids(got))
	if len(got) != 0 {
		t.Fatalf("%s: SECURITY: nil verifier must admit NOTHING but placeable set has %d: %v", runUUIDAdv, len(got), ids(got))
	}
}

// TestPlaceable_MixedAdmitsOnlyGenuine exercises the batch gate end-to-end with a
// mixed candidate set (genuine + tampered + forged-descriptor + expired) and
// asserts ONLY the genuine nodes survive — the fail-closed property holds across
// heterogeneous failure modes in one pass.
func TestPlaceable_MixedAdmitsOnlyGenuine(t *testing.T) {
	v := gpuattest.NewVerifier()

	good1 := genuineNodeAdv(t, v, "good-1", 10, 1000, 20)

	tampered := genuineNodeAdv(t, v, "bad-tampered", 10, 1000, 20)
	tampered.Response.Fingerprint[0] ^= 0xFF // corrupt a signed field

	forged := genuineNodeAdv(t, v, "bad-forged", 10, 1000, 20)
	other, err := gpuattest.NewDevice("nvidia", "H100", "uuid-mixother", 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(mixother): %v", runUUIDAdv, err)
	}
	forged.Descriptor = other.Descriptor

	expired := genuineNodeAdv(t, v, "bad-expired", 10, 30, 20) // expiry 30

	good2 := genuineNodeAdv(t, v, "good-2", 10, 1000, 25)

	in := []Node{good1, tampered, forged, expired, good2}
	const nowTick = int64(40) // > expired.expiry(30); in-window for the rest
	t.Logf("%s: MIXED before filter=%v nowTick=%d", runUUIDAdv, ids(in), nowTick)

	got := Placeable(v, in, nowTick)
	t.Logf("%s: MIXED after filter=%v (count=%d, want [good-1 good-2])", runUUIDAdv, ids(got), len(got))

	if len(got) != 2 || got[0].ID != "good-1" || got[1].ID != "good-2" {
		t.Fatalf("%s: SECURITY: expected only [good-1 good-2] genuine nodes placeable, got %v", runUUIDAdv, ids(got))
	}
	for _, n := range got {
		switch n.ID {
		case "bad-tampered", "bad-forged", "bad-expired":
			t.Fatalf("%s: SECURITY: non-genuine node %q survived admission (FAIL-OPEN)", runUUIDAdv, n.ID)
		}
	}
}

// staticGuard keeps the ed25519 import meaningful even if future refactors drop
// the only direct use; it documents that PublicKeySize is the size the gate's
// wrong-key check (in gpuattest.Verify) relies upon.
var _ = ed25519.PublicKeySize
