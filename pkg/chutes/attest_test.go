package chutes

import (
	"errors"
	"math"
	"testing"
)

func newTestAttestor(t *testing.T) *HMACAttestor {
	t.Helper()
	a, err := NewHMACAttestor([]byte("shared-gpu-secret"))
	if err != nil {
		t.Fatalf("NewHMACAttestor: %v", err)
	}
	return a
}

// TestAttestRoundTrip proves an honest node's response verifies.
//
// MUTATION: in proof(), drop the nonce from the HMAC input (only hash nodeID).
// Respond and Verify still agree, so this test alone would still pass — but
// TestAttestTamperFails below would no longer detect a swapped nonce. The
// load-bearing assertion is the negative test; this one fixes the happy path.
func TestAttestRoundTrip(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)
	c, err := a.Challenge("gpu-node-1")
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	r, err := a.Respond(c)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if err := a.Verify(c, r); err != nil {
		t.Fatalf("Verify honest response: %v", err)
	}
}

// TestAttestTamperFails proves a tampered proof fails Verify.
//
// MUTATION: in Verify, replace hmac.Equal(want, r.Proof) with `true`. The
// tampered proof would verify and the expected ErrAttestFailed assertion fails.
func TestAttestTamperFails(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)
	c, _ := a.Challenge("gpu-node-1")
	r, _ := a.Respond(c)
	r.Proof[0] ^= 0xFF // corrupt the proof
	if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("tampered Verify = %v, want ErrAttestFailed", err)
	}
}

// TestAttestNonceBinding proves the proof is bound to the nonce: a proof for one
// challenge does not verify against a different challenge's nonce.
//
// MUTATION: in proof(), omit mac.Write(nonce). Then a response to challenge A
// would verify against challenge B and the expected ErrAttestFailed fails.
func TestAttestNonceBinding(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)
	cA, _ := a.Challenge("gpu-node-1")
	cB, _ := a.Challenge("gpu-node-1")
	rA, _ := a.Respond(cA)
	if err := a.Verify(cB, rA); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("cross-nonce Verify = %v, want ErrAttestFailed", err)
	}
}

// TestAttestNodeBinding proves a proof for node A cannot satisfy a challenge for
// node B even with the same nonce.
//
// MUTATION: remove the c.NodeID != r.NodeID check in Verify AND drop nodeID from
// proof(); then a node-B proof would satisfy a node-A challenge. (The NodeID
// guard alone is what this asserts.)
func TestAttestNodeBinding(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)
	c, _ := a.Challenge("gpu-node-A")
	r, _ := a.Respond(c)
	r.NodeID = "gpu-node-B" // claim to be a different node
	if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("node-mismatch Verify = %v, want ErrAttestFailed", err)
	}
}

// TestAttestWrongSecretFails proves a different verifier secret rejects an
// honest node's proof.
//
// MUTATION: hardcode the secret in proof() to a constant. Both attestors would
// then compute the same proof and the cross-secret verification would wrongly
// pass, failing this assertion.
func TestAttestWrongSecretFails(t *testing.T) {
	t.Parallel()
	node := newTestAttestor(t)
	verifier, _ := NewHMACAttestor([]byte("a-different-secret"))
	c, _ := node.Challenge("gpu-node-1")
	r, _ := node.Respond(c)
	if err := verifier.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("wrong-secret Verify = %v, want ErrAttestFailed", err)
	}
}

// TestBatchVerifyExactPassRate proves BatchVerify returns the EXACT pass-rate
// over a mix of valid and invalid attestations: 3 valid + 1 invalid = 0.75.
//
// MUTATION: in BatchVerify, increment Passed unconditionally (drop the err
// check). PassRate would become 1.0 and the 0.75 assertion fails.
func TestBatchVerifyExactPassRate(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)

	mk := func(node string, corrupt bool) AttestationResult {
		c, _ := a.Challenge(node)
		r, _ := a.Respond(c)
		if corrupt {
			r.Proof[0] ^= 0xFF
		}
		return AttestationResult{Challenge: c, Response: r}
	}

	results := []AttestationResult{
		mk("n1", false),
		mk("n2", false),
		mk("n3", true), // invalid
		mk("n4", false),
	}
	rep := BatchVerify(a, results)
	if rep.Total != 4 {
		t.Fatalf("total = %d, want 4", rep.Total)
	}
	if rep.Passed != 3 {
		t.Fatalf("passed = %d, want 3", rep.Passed)
	}
	if math.Abs(rep.PassRate()-0.75) > 1e-9 {
		t.Fatalf("pass-rate = %v, want 0.75", rep.PassRate())
	}
	if len(rep.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(rep.Failures))
	}
	// The failure must be at index 2 (the corrupted entry), not a random one.
	if _, ok := rep.Failures[2]; !ok {
		t.Fatalf("expected failure at index 2, got failures=%v", rep.Failures)
	}
}

// TestBatchVerifyEmpty proves an empty batch has a 0 pass-rate (nothing proved).
//
// MUTATION: in PassRate, return 1 for Total==0. The assertion fails.
func TestBatchVerifyEmpty(t *testing.T) {
	t.Parallel()
	rep := BatchVerify(newTestAttestor(t), nil)
	if rep.PassRate() != 0 {
		t.Fatalf("empty pass-rate = %v, want 0", rep.PassRate())
	}
}

// TestBatchVerifyAllValid proves a fully-valid batch reports a 1.0 pass-rate.
//
// MUTATION: in PassRate, divide by (Total+1). The rate would be < 1.0 and this
// assertion fails.
func TestBatchVerifyAllValid(t *testing.T) {
	t.Parallel()
	a := newTestAttestor(t)
	var results []AttestationResult
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		c, _ := a.Challenge(n)
		r, _ := a.Respond(c)
		results = append(results, AttestationResult{Challenge: c, Response: r})
	}
	rep := BatchVerify(a, results)
	if rep.PassRate() != 1.0 {
		t.Fatalf("pass-rate = %v, want 1.0", rep.PassRate())
	}
}
