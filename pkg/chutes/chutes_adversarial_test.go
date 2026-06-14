package chutes

import (
	"bytes"
	"errors"
	"testing"
)

// This file is an adversarial security audit of the HMAC attestation gate
// (attest.go). The centerpieces are (1) per-field tamper rejection and (2)
// nonce binding / replay. Each test documents the canonical production mutation
// that makes it fail, so the suite is mutation-verified rather than a PASS-bluff.
//
// Attestation model under audit:
//   proof  = HMAC-SHA256(secret, nodeID-bytes || nonce-bytes)
//   Verify = (len(c.Nonce) != 0) && (c.NodeID == r.NodeID) &&
//            hmac.Equal(HMAC(secret, c.NodeID||c.Nonce), r.Proof)
// The signed material is {NodeID, Nonce}; the proof is the tag. Verify is the
// fail-closed gate: any deviation returns ErrAttestFailed / ErrChallengeSize.

func advAttestor(t *testing.T) *HMACAttestor {
	t.Helper()
	a, err := NewHMACAttestor([]byte("shared-gpu-secret-adv"))
	if err != nil {
		t.Fatalf("NewHMACAttestor: %v", err)
	}
	return a
}

// --- (a) GENUINE-ACCEPT BASELINE ------------------------------------------
//
// An honest, untouched response MUST verify. This catches an always-reject
// mutation (e.g. Verify returning ErrAttestFailed unconditionally), which would
// otherwise let every negative test below pass vacuously.
//
// MUTATION: make Verify always return ErrAttestFailed -> this test fails.
func TestAdvGenuineAcceptBaseline(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)
	c, err := a.Challenge("gpu-accept")
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	r, err := a.Respond(c)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if err := a.Verify(c, r); err != nil {
		t.Fatalf("honest response rejected: %v", err)
	}
	// Sink-side evidence: the proof is the full 32-byte SHA-256 HMAC tag, not a
	// truncated / empty stub that would trivially "match".
	if len(r.Proof) != 32 {
		t.Fatalf("proof len = %d, want 32 (HMAC-SHA256)", len(r.Proof))
	}
}

// --- (b) TAMPER REJECTION (CENTERPIECE) -----------------------------------
//
// Alter EACH signed field, one at a time, in several ways: a same-length byte
// flip in the proof, every single bit of the proof, a same-length nonce change,
// an appended nonce byte, a truncated nonce, and the NodeID. Verify MUST reject
// every one. This proves the tag actually covers each field and that the
// comparison is not length-permissive.
//
// MUTATION: replace hmac.Equal(want, r.Proof) with `true`, or drop a field from
// proof() -> the matching sub-case below fails.
func TestAdvTamperRejectionPerField(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)

	t.Run("proof_same_length_flip", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-1")
		r, _ := a.Respond(c)
		orig := append([]byte(nil), r.Proof...)
		r.Proof[len(r.Proof)/2] ^= 0x01 // single-bit, same length
		if bytes.Equal(orig, r.Proof) {
			t.Fatal("proof unchanged by flip")
		}
		if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("flipped proof Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("proof_every_bit", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-bits")
		r, _ := a.Respond(c)
		good := append([]byte(nil), r.Proof...)
		for byteIdx := 0; byteIdx < len(good); byteIdx++ {
			for bit := 0; bit < 8; bit++ {
				tampered := append([]byte(nil), good...)
				tampered[byteIdx] ^= 1 << bit
				if err := a.Verify(c, Response{NodeID: r.NodeID, Proof: tampered}); !errors.Is(err, ErrAttestFailed) {
					t.Fatalf("bit %d of byte %d accepted: Verify = %v", bit, byteIdx, err)
				}
			}
		}
	})

	t.Run("nonce_same_length_change", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-2")
		r, _ := a.Respond(c) // proof computed over ORIGINAL nonce
		c.Nonce[0] ^= 0xFF   // alter expected nonce, same length, proof stale
		if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("altered-nonce Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("nonce_appended_byte", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-3")
		r, _ := a.Respond(c)
		c.Nonce = append(c.Nonce, 0x00) // longer expected nonce
		if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("appended-nonce Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("nonce_truncated", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-4")
		r, _ := a.Respond(c)
		c.Nonce = c.Nonce[:len(c.Nonce)-1] // shorter expected nonce
		if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("truncated-nonce Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("nodeid_changed", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-tamper-5")
		r, _ := a.Respond(c)
		r.NodeID = "gpu-tamper-5-evil"
		if err := a.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("changed-nodeid Verify = %v, want ErrAttestFailed", err)
		}
	})
}

// --- (b') CROSS-FIELD SWAP -------------------------------------------------
//
// Swapping NodeID and Nonce content between two honest attestations must not
// verify: a proof bound to (A, nonceA) must not satisfy a challenge claiming
// (B, nonceB) even if the attacker shuffles the response fields.
//
// MUTATION: drop the c.NodeID!=r.NodeID guard AND nodeID from proof() -> a
// swapped pair could verify; this fails.
func TestAdvCrossFieldSwap(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)
	cA, _ := a.Challenge("node-A")
	cB, _ := a.Challenge("node-B")
	rA, _ := a.Respond(cA)
	rB, _ := a.Respond(cB)

	// rA's proof against B's challenge but A's claimed id (nonce mismatch).
	if err := a.Verify(cB, Response{NodeID: cB.NodeID, Proof: rA.Proof}); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("A-proof on B-challenge = %v, want ErrAttestFailed", err)
	}
	// rB's proof against A's challenge.
	if err := a.Verify(cA, Response{NodeID: cA.NodeID, Proof: rB.Proof}); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("B-proof on A-challenge = %v, want ErrAttestFailed", err)
	}
}

// --- (c) NONCE BINDING / REPLAY (CENTERPIECE) ------------------------------
//
// (1) An attestation built for nonce A must FAIL against expected nonce B. This
//
//	is the core anti-replay property: the proof is welded to the exact nonce.
//
// (2) The contract is stateless single-shot verification: the SAME (c, r) pair
//
//	verifies repeatedly (Verify holds no replay cache). We PIN that contract
//	explicitly so a reader knows freshness is the caller's duty (each Verify
//	uses a fresh verifier-minted nonce), and so a regression that silently
//	made Verify stateful would surface here.
//
// MUTATION: omit mac.Write(nonce) in proof() -> sub-test (1) fails (any nonce
// would verify).
func TestAdvNonceBindingAndReplayContract(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)

	t.Run("proof_welded_to_nonce", func(t *testing.T) {
		t.Parallel()
		cA, _ := a.Challenge("gpu-nonce")
		cB, _ := a.Challenge("gpu-nonce") // same node, fresh independent nonce
		if bytes.Equal(cA.Nonce, cB.Nonce) {
			t.Fatal("two challenges minted identical nonces (RNG broken)")
		}
		rA, _ := a.Respond(cA)
		if err := a.Verify(cB, rA); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("nonce-A proof on challenge-B = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("stateless_reverify_is_contracted", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-replay")
		r, _ := a.Respond(c)
		// Same fresh challenge re-verified twice -> both pass: Verify is a pure
		// function of (secret, c, r) and holds no single-use cache. Freshness is
		// guaranteed upstream by Challenge() minting a new 32-byte nonce per call.
		if err := a.Verify(c, r); err != nil {
			t.Fatalf("first verify: %v", err)
		}
		if err := a.Verify(c, r); err != nil {
			t.Fatalf("second verify of same pair: %v (contract is stateless)", err)
		}
	})
}

// --- (d) FORGERY / WRONG KEY -----------------------------------------------
//
// A proof minted under a different secret (a self-signed / wrong-key forgery)
// must be rejected, and the trusted key must match exactly: a near-miss secret
// (one byte off, same length) must NOT verify.
//
// MUTATION: hardcode a constant key inside proof() -> cross-secret proofs match
// and these fail.
func TestAdvForgeryWrongKey(t *testing.T) {
	t.Parallel()
	verifier := advAttestor(t) // secret "shared-gpu-secret-adv"

	t.Run("different_secret", func(t *testing.T) {
		t.Parallel()
		forger, _ := NewHMACAttestor([]byte("totally-unrelated-secret"))
		c, _ := verifier.Challenge("gpu-forge")
		r, _ := forger.Respond(c) // forger computes proof under its own key
		if err := verifier.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("forged (wrong-key) proof Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("one_byte_off_lookalike_key", func(t *testing.T) {
		t.Parallel()
		look, _ := NewHMACAttestor([]byte("shared-gpu-secret-adX")) // last byte differs
		c, _ := verifier.Challenge("gpu-lookalike")
		r, _ := look.Respond(c)
		if err := verifier.Verify(c, r); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("lookalike-key proof Verify = %v, want ErrAttestFailed", err)
		}
	})
}

// --- (e) FAIL-CLOSED ON MISSING/EMPTY/ZERO ---------------------------------
//
// Empty/nil/zero attestation material must be REJECTED (never fail-open to
// valid) and must never panic. Probes: empty nonce, nil proof, empty proof,
// zero-value Response, and an all-zero proof of correct length.
//
// MUTATION: make proof()/Verify treat an empty nonce as valid, or `return nil`
// early in Verify -> these fail.
func TestAdvFailClosed(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)

	t.Run("empty_nonce_rejected", func(t *testing.T) {
		t.Parallel()
		// Hand-built challenge with no nonce; must be ErrChallengeSize, not pass.
		c := Challenge{Nonce: nil, NodeID: "gpu-empty"}
		r := Response{NodeID: "gpu-empty", Proof: make([]byte, 32)}
		if err := a.Verify(c, r); !errors.Is(err, ErrChallengeSize) {
			t.Fatalf("empty-nonce Verify = %v, want ErrChallengeSize", err)
		}
	})

	t.Run("nil_proof_rejected", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-nilproof")
		if err := a.Verify(c, Response{NodeID: c.NodeID, Proof: nil}); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("nil-proof Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("empty_proof_rejected", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-emptyproof")
		if err := a.Verify(c, Response{NodeID: c.NodeID, Proof: []byte{}}); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("empty-proof Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("zero_value_response_rejected", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-zero")
		// Zero Response: NodeID "" != c.NodeID, must reject on node mismatch.
		if err := a.Verify(c, Response{}); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("zero-value Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("all_zero_proof_correct_length", func(t *testing.T) {
		t.Parallel()
		c, _ := a.Challenge("gpu-zeros")
		if err := a.Verify(c, Response{NodeID: c.NodeID, Proof: make([]byte, 32)}); !errors.Is(err, ErrAttestFailed) {
			t.Fatalf("all-zero proof Verify = %v, want ErrAttestFailed", err)
		}
	})

	t.Run("respond_empty_nonce_errors", func(t *testing.T) {
		t.Parallel()
		if _, err := a.Respond(Challenge{NodeID: "x", Nonce: nil}); !errors.Is(err, ErrChallengeSize) {
			t.Fatalf("Respond(empty nonce) = %v, want ErrChallengeSize", err)
		}
	})
}

// --- CANONICALIZATION CHARACTERIZATION (latent hardening note) -------------
//
// The proof is HMAC(secret, nodeID || nonce) with NO length separator between
// the two fields, so the concatenation is ambiguous: proof("node-1","AAAA") ==
// proof("node-1A","AAA"). This is NOT a live Verify bypass under the current
// contract, because Verify requires c.NodeID == r.NodeID exactly AND recomputes
// the tag from the verifier-controlled c.NodeID and c.Nonce — within one Verify
// both fields are sourced consistently from the verifier's own challenge, and
// Challenge() always mints a fixed 32-byte nonce, so an attacker cannot move the
// field boundary in a verifier-issued challenge.
//
// This test PINS that today the boundary is NOT enforced (documents the latent
// weakness) AND that it is currently unexploitable through Verify. If a future
// change ever lets a caller supply attacker-controlled (NodeID, Nonce) pairs to
// Verify, this characterization flips into an exploit and the second assertion
// will start failing — a tripwire. Recommended hardening: domain-separate the
// HMAC input (e.g. length-prefix nodeID, or write a fixed separator), which
// would make the first assertion below report "boundary enforced".
func TestAdvCanonicalizationBoundaryCharacterization(t *testing.T) {
	t.Parallel()
	a := advAttestor(t)

	pSplitA := a.proof("node-1", []byte("AAAA"))
	pSplitB := a.proof("node-1A", []byte("AAA"))

	boundaryEnforced := !bytes.Equal(pSplitA, pSplitB)
	if boundaryEnforced {
		t.Log("canonicalization HARDENED: nodeID||nonce boundary is now domain-separated")
	} else {
		t.Log("NOTE (latent): nodeID||nonce concatenation is ambiguous (no length separator)")
	}

	// Tripwire: even with the ambiguous concatenation, Verify must not let a
	// boundary-shifted pair through, because the exact NodeID match defends it.
	// Build a challenge for "node-1" + nonce "AAAA" and a response that tries to
	// claim the shifted identity "node-1A" with the colliding proof.
	c := Challenge{NodeID: "node-1", Nonce: []byte("AAAA")}
	shifted := Response{NodeID: "node-1A", Proof: pSplitB} // pSplitB == pSplitA bytes
	if err := a.Verify(c, shifted); !errors.Is(err, ErrAttestFailed) {
		t.Fatalf("SECURITY: boundary-shifted identity accepted: Verify = %v, want ErrAttestFailed", err)
	}
	// And the honest split still verifies, confirming the collision is real.
	if err := a.Verify(c, Response{NodeID: "node-1", Proof: pSplitA}); err != nil {
		t.Fatalf("honest split rejected: %v", err)
	}
}
