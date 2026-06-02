package attestadmit

import (
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/gpuattest"
)

const runUUID = "attestadmit-HXC-1586-3f9c1b40-7e2a-4d6c-9a11-0c5b8e2d4f77"

// genuineNode builds a Node whose Response was really produced by its device for
// the issued challenge — Verify must accept it. It uses the SHARED verifier so
// the issued nonce belongs to that verifier's namespace.
func genuineNode(t *testing.T, v *gpuattest.Verifier, id string, issue, expiry, prod int64) Node {
	t.Helper()
	dev, err := gpuattest.NewDevice("nvidia", "H100", "uuid-"+id, 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(%s): %v", runUUID, id, err)
	}
	ch, err := v.Issue(issue, expiry)
	if err != nil {
		t.Fatalf("%s: Issue(%s): %v", runUUID, id, err)
	}
	resp := dev.Respond(ch, prod)
	return Node{ID: id, Challenge: ch, Response: resp, Descriptor: dev.Descriptor}
}

// TestPlaceable_FiltersSpoofedBeforeScoring covers closure clause 1.
//
// Two nodes: one genuine, one SPOOFED (its Response is signed by a DIFFERENT
// device's key while presenting the first device's Descriptor — a wrong-key /
// forged attestation). Placeable must return ONLY the genuine node; the spoofed
// node must be ABSENT from the placeable set (filtered before scoring).
//
// Mutation: if Admit were made always-true (admit without calling Verify), the
// spoofed node would wrongly appear in the placeable set and the absence
// assertion below fails. This is the exact mutation guard the task demands.
func TestPlaceable_FiltersSpoofedBeforeScoring(t *testing.T) {
	v := gpuattest.NewVerifier()

	good := genuineNode(t, v, "genuine", 10, 100, 20)

	// Build a spoofed node: a victim descriptor but a response signed by an
	// attacker device under an attacker-issued nonce. Verify will reject it
	// (wrong key / forged descriptor) — it is NOT a genuine attestation.
	victim, err := gpuattest.NewDevice("nvidia", "H100", "uuid-victim", 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(victim): %v", runUUID, err)
	}
	attacker, err := gpuattest.NewDevice("nvidia", "H100", "uuid-attacker", 80<<30)
	if err != nil {
		t.Fatalf("%s: NewDevice(attacker): %v", runUUID, err)
	}
	ch, err := v.Issue(10, 100)
	if err != nil {
		t.Fatalf("%s: Issue(spoof): %v", runUUID, err)
	}
	// Attacker signs the challenge, then we present the VICTIM descriptor — a
	// spoof attempt to impersonate the victim device.
	attackerResp := attacker.Respond(ch, 20)
	spoof := Node{ID: "spoofed", Challenge: ch, Response: attackerResp, Descriptor: victim.Descriptor}

	// Sanity: confirm the spoof really is rejected by the real verifier and the
	// genuine node really is accepted, using a throwaway verifier so the shared
	// verifier's nonces are not consumed before the actual call under test.
	probe := gpuattest.NewVerifier()
	if err := probe.Verify(spoof.Challenge, spoof.Response, spoof.Descriptor, 20); err == nil {
		t.Fatalf("%s: spoof unexpectedly accepted by real gpuattest.Verify", runUUID)
	} else {
		t.Logf("%s: real Verify rejects spoof with sentinel=%v (expected non-nil)", runUUID, err)
	}

	in := []Node{good, spoof}
	t.Logf("%s: BEFORE filter candidate IDs=%v (count=%d)", runUUID, ids(in), len(in))

	got := Placeable(v, in, 20)

	t.Logf("%s: AFTER filter placeable IDs=%v (count=%d)", runUUID, ids(got), len(got))

	if len(got) != 1 {
		t.Fatalf("%s: expected exactly 1 placeable (genuine only), got %d: %v", runUUID, len(got), ids(got))
	}
	if got[0].ID != "genuine" {
		t.Fatalf("%s: expected genuine node placeable, got %q", runUUID, got[0].ID)
	}
	for _, n := range got {
		if n.ID == "spoofed" {
			t.Fatalf("%s: spoofed node MUST be filtered before scoring but is present in placeable set", runUUID)
		}
	}
}

// TestPlaceable_FiltersExpired covers closure clause 2.
//
// A genuine node whose attestation is past expiry (nowTick > expiryTick) is
// filtered; a second genuine, in-window node survives — proving the filter keys
// on expiry specifically and not on something incidental.
//
// Mutation: if the predicate ignored expiry (e.g. dropped the nowTick>expiry
// check), the expired node would be admitted and the count/absence assertions
// fail. The real expiry logic lives in gpuattest.Verify; this proves Admit
// honours it.
func TestPlaceable_FiltersExpired(t *testing.T) {
	v := gpuattest.NewVerifier()

	// expires at tick 50; we evaluate at tick 999 (past expiry).
	expired := genuineNode(t, v, "expired", 10, 50, 20)
	// expires at tick 5000; in-window at tick 999.
	fresh := genuineNode(t, v, "fresh", 10, 5000, 20)

	const nowTick = int64(999)
	in := []Node{expired, fresh}
	t.Logf("%s: BEFORE filter candidates=%v nowTick=%d (expired.expiry=%d fresh.expiry=%d)",
		runUUID, ids(in), nowTick, expired.Challenge.ExpiryTick, fresh.Challenge.ExpiryTick)

	got := Placeable(v, in, nowTick)

	t.Logf("%s: AFTER filter placeable=%v (count=%d)", runUUID, ids(got), len(got))

	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("%s: expected only in-window 'fresh' node placeable, got %v", runUUID, ids(got))
	}
	for _, n := range got {
		if n.ID == "expired" {
			t.Fatalf("%s: expired node MUST be filtered (nowTick=%d > expiry=%d) but is present", runUUID, nowTick, expired.Challenge.ExpiryTick)
		}
	}
}

// TestPlaceable_AllGenuine covers closure clause 3.
//
// When BOTH nodes are genuine and in-window, BOTH are placeable. This proves the
// predicate is not trivially rejecting everything.
//
// Mutation: if Admit were made always-false (reject without calling Verify), the
// placeable set would be empty and this test fails — the required always-false
// guard.
func TestPlaceable_AllGenuine(t *testing.T) {
	v := gpuattest.NewVerifier()

	a := genuineNode(t, v, "alpha", 10, 1000, 20)
	b := genuineNode(t, v, "beta", 10, 1000, 30)

	in := []Node{a, b}
	t.Logf("%s: BEFORE filter candidates=%v (count=%d)", runUUID, ids(in), len(in))

	got := Placeable(v, in, 40)

	t.Logf("%s: AFTER filter placeable=%v (count=%d)", runUUID, ids(got), len(got))

	if len(got) != 2 {
		t.Fatalf("%s: expected BOTH genuine nodes placeable, got %d: %v", runUUID, len(got), ids(got))
	}
	if got[0].ID != "alpha" || got[1].ID != "beta" {
		t.Fatalf("%s: expected order-preserving [alpha beta], got %v", runUUID, ids(got))
	}
}

// TestAdmit_NilVerifierFailClosed proves the fail-closed contract: with no
// verifier there is no way to attest, so a genuine-looking node is NOT admitted.
//
// Mutation: if the nil guard were removed, Admit would panic (nil deref) or, if
// changed to return true, would admit unverified nodes — either breaks this test.
func TestAdmit_NilVerifierFailClosed(t *testing.T) {
	v := gpuattest.NewVerifier()
	n := genuineNode(t, v, "genuine", 10, 1000, 20)

	admitted := Admit(nil, n, 20)
	t.Logf("%s: BEFORE expect fail-closed; AFTER Admit(nil verifier)=%v (want false)", runUUID, admitted)
	if admitted {
		t.Fatalf("%s: nil verifier must fail closed (admit=false) but admitted node", runUUID)
	}
}

func ids(ns []Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}
