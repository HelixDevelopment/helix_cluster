package hybridkex

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// This file holds REAL adversarial tests for the X25519 + ML-KEM-768 hybrid
// key exchange. They use real keygen (crypto/rand via NewInitiator/Respond) and
// drive the REAL Respond/Finish paths — no mocks. Each test asserts a
// security-critical property that BITES a specific combiner bug:
//
//   - drop the classical half from deriveKey  -> classical tamper no longer
//     changes the key -> TestHybridBinding_ClassicalTamperDiffers FAILS.
//   - drop the pq half from deriveKey         -> KEM tamper / wrong-key no
//     longer changes the key -> TestHybridBinding_PQTamperDiffers and
//     TestWrongDecapKey_ImplicitRejectionDiffers FAIL.
//
// The whole point of a HYBRID is that BOTH components bind the final key, so an
// adversary who breaks only one component still cannot derive the session key.
// These tests assert exactly that.

// TestAgreement_MultipleFreshRuns proves the baseline correctness property over
// SEVERAL independent handshakes with fresh keys each run: an honest exchange
// always yields a byte-identical 32-byte session key on the Initiator and
// Responder sides. Different runs MUST yield different keys (fresh ephemerals),
// which also rules out a constant/degenerate combiner that returns the same key
// regardless of inputs.
func TestAgreement_MultipleFreshRuns(t *testing.T) {
	const runUUID = "b7e1d4a2-3c55-4e90-91aa-77ff00d1c2b3"
	const runs = 16
	t.Logf("run-uuid=%s proving: %d fresh honest handshakes each agree, and all %d keys are distinct (no constant combiner)", runUUID, runs, runs)

	seen := make(map[string]int, runs)
	for i := 0; i < runs; i++ {
		in, ihello, err := NewInitiator()
		if err != nil {
			t.Fatalf("run %d NewInitiator: %v", i, err)
		}
		_, rhello, keyResp, err := Respond(ihello)
		if err != nil {
			t.Fatalf("run %d Respond: %v", i, err)
		}
		keyInit, err := in.Finish(rhello)
		if err != nil {
			t.Fatalf("run %d Finish: %v", i, err)
		}
		if len(keyInit) != SessionKeyLen || len(keyResp) != SessionKeyLen {
			t.Fatalf("run %d key length: init=%d resp=%d want %d", i, len(keyInit), len(keyResp), SessionKeyLen)
		}
		// Sink-side: the two independently-derived keys agree byte-for-byte.
		if !bytes.Equal(keyInit, keyResp) {
			t.Fatalf("run %d keys disagree:\n init=%x\n resp=%x", i, keyInit, keyResp)
		}
		if bytes.Equal(keyInit, make([]byte, SessionKeyLen)) {
			t.Fatalf("run %d derived all-zero key", i)
		}
		// Distinct-transcript guard: no two fresh runs may collide. A constant
		// combiner (ignores its inputs) would produce duplicates and fail here.
		if prev, dup := seen[string(keyInit)]; dup {
			t.Fatalf("run %d produced a key already seen in run %d %x — combiner not input-dependent", i, prev, keyInit)
		}
		seen[string(keyInit)] = i
	}
	t.Logf("run-uuid=%s all %d handshakes agreed and produced %d distinct keys", runUUID, runs, len(seen))
}

// TestHybridBinding_ClassicalTamperDiffers is the CLASSICAL half of the hybrid
// binding proof. It tampers ONLY the classical (X25519) component of the
// ResponderHello and proves the Initiator then derives a DIFFERENT key, so the
// two sides do not agree. This is the gap the existing suite left open: the
// prior tests tamper only the PQ ciphertext, never the classical public key via
// a full handshake.
//
// We substitute the Responder's published X25519 public key with a freshly
// generated, UNRELATED X25519 public key while leaving the ML-KEM ciphertext
// untouched. The Initiator's ECDH(in.priv, wrongPub) yields a different
// classical secret, so deriveKey(classical', pq) != deriveKey(classical, pq).
//
// WHY IT BITES: if a mutation drops the classical half from deriveKey (pq-only
// combiner), then tampering the X25519 public key no longer changes the derived
// key, the tampered key would EQUAL the clean key, and this test FAILS. So this
// test is the live guard that the classical component is load-bearing.
func TestHybridBinding_ClassicalTamperDiffers(t *testing.T) {
	const runUUID = "d2c9f180-6a44-4b77-9e10-aa11bb22cc33"
	t.Logf("run-uuid=%s proving: tampered X25519 public key -> initiator key DIFFERS (classical half binds; delta match->mismatch)", runUUID)

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
		t.Fatalf("baseline keys must agree before classical tamper:\n init=%x\n resp=%x", keyClean, keyResp)
	}

	// Tamper ONLY the classical component: replace the Responder's X25519 public
	// key with a fresh unrelated valid public key (a real on-curve point, so the
	// ECDH succeeds but over a different shared secret). The ML-KEM ciphertext is
	// copied unchanged, so the PQ half is identical between clean and tampered.
	wrongPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate wrong x25519 key: %v", err)
	}
	wrongPub := wrongPriv.PublicKey().Bytes()
	if bytes.Equal(wrongPub, rhello.X25519Pub) {
		t.Fatalf("astronomically-unlikely keygen collision; rerun")
	}
	tampered := &ResponderHello{
		X25519Pub:   wrongPub,
		MLKEMCipher: append([]byte(nil), rhello.MLKEMCipher...),
	}

	keyTampered, err := in.Finish(tampered)
	if err != nil {
		t.Fatalf("Finish (classical-tampered): %v", err)
	}
	if len(keyTampered) != SessionKeyLen {
		t.Fatalf("tampered key length = %d want %d", len(keyTampered), SessionKeyLen)
	}
	// Sink-side guard: tampering ONLY the classical half must change the key.
	if bytes.Equal(keyTampered, keyClean) {
		t.Fatalf("SECURITY: tampered X25519 public key still produced the clean key %x — classical half is NOT load-bearing (hybrid binding broken)", keyTampered)
	}
	if bytes.Equal(keyTampered, keyResp) {
		t.Fatalf("SECURITY: tampered X25519 public key still agreed with responder %x — classical half not binding", keyTampered)
	}
	t.Logf("run-uuid=%s classical-tamper confirmed: clean=%x tampered=%x (mismatch as required)", runUUID, keyClean[:8], keyTampered[:8])
}

// TestHybridBinding_PQTamperDiffers is the PQ half of the binding proof, made
// independent of the existing suite and stronger: it tampers ONLY the ML-KEM
// ciphertext (leaving the X25519 public key identical) and proves the derived
// key differs. Because the classical half is held FIXED between clean and
// tampered, any difference is attributable solely to the PQ component.
//
// WHY IT BITES: if a mutation drops the pq half from deriveKey (classical-only
// combiner), the tampered ciphertext no longer changes the key, the tampered
// key EQUALS the clean key, and this test FAILS. Together with the classical
// test above, the pair proves NEITHER component alone determines the key.
func TestHybridBinding_PQTamperDiffers(t *testing.T) {
	const runUUID = "e4807a31-92bd-4c0e-87f1-44d5e6071829"
	t.Logf("run-uuid=%s proving: tampered ML-KEM ciphertext (classical fixed) -> key DIFFERS (pq half binds)", runUUID)

	in, ihello, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	_, rhello, keyResp, err := Respond(ihello)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	keyClean, err := in.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish (clean): %v", err)
	}
	if !bytes.Equal(keyClean, keyResp) {
		t.Fatalf("baseline keys must agree before pq tamper")
	}

	// Tamper ONLY the PQ ciphertext; keep the SAME X25519 public key. ML-KEM-768
	// decapsulation is implicit-rejection, so a flipped bit does not error: it
	// yields a different pq secret and therefore a different session key.
	tampered := &ResponderHello{
		X25519Pub:   append([]byte(nil), rhello.X25519Pub...), // classical held fixed
		MLKEMCipher: append([]byte(nil), rhello.MLKEMCipher...),
	}
	tampered.MLKEMCipher[len(tampered.MLKEMCipher)/2] ^= 0x80

	keyTampered, err := in.Finish(tampered)
	if err != nil {
		t.Fatalf("Finish (pq-tampered): %v", err)
	}
	if len(keyTampered) != SessionKeyLen {
		t.Fatalf("tampered key length = %d want %d", len(keyTampered), SessionKeyLen)
	}
	if bytes.Equal(keyTampered, keyClean) {
		t.Fatalf("SECURITY: tampered ML-KEM ciphertext (classical fixed) still produced the clean key %x — pq half is NOT load-bearing", keyTampered)
	}
	if bytes.Equal(keyTampered, keyResp) {
		t.Fatalf("SECURITY: tampered ML-KEM ciphertext still agreed with responder %x — pq half not binding", keyTampered)
	}
	t.Logf("run-uuid=%s pq-tamper confirmed: clean=%x tampered=%x (mismatch as required)", runUUID, keyClean[:8], keyTampered[:8])
}

// TestWrongDecapKey_ImplicitRejectionDiffers proves ML-KEM implicit rejection at
// the handshake layer: a Responder encapsulates against Initiator A's ML-KEM
// encapsulation key, producing a ciphertext + the Responder's session key. The
// CORRECT Initiator A decapsulates and agrees (the discriminator). A WRONG
// Initiator B — a different party with a different ML-KEM decapsulation key but
// who replays A's classical public key so the classical half matches — feeds the
// same ResponderHello into Finish and gets a DIFFERENT key (implicit rejection),
// so it does NOT agree.
//
// WHY IT BITES: this isolates the secret-key dependence of the PQ half. If the
// combiner dropped the pq secret, then B (wrong KEM key) would derive the SAME
// key as A whenever the classical halves match — i.e. holding the wrong KEM key
// would not matter. We assert B != Responder AND A == Responder, so a pq-dropping
// mutation fails on the "B differs" assertion.
func TestWrongDecapKey_ImplicitRejectionDiffers(t *testing.T) {
	const runUUID = "0f5b8c27-71ad-4e36-9c02-aabbccddeeff"
	t.Logf("run-uuid=%s proving: correct ML-KEM decap key agrees; wrong decap key (same classical) yields DIFFERENT key (implicit rejection)", runUUID)

	// Initiator A: the legitimate party the Responder encapsulates against.
	inA, helloA, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator A: %v", err)
	}
	_, rhello, keyResp, err := Respond(helloA)
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}

	// Correct party A decapsulates -> MUST agree with the Responder (discriminator).
	keyA, err := inA.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish A: %v", err)
	}
	if !bytes.Equal(keyA, keyResp) {
		t.Fatalf("correct Initiator A did not agree with Responder:\n A=%x\n R=%x", keyA, keyResp)
	}

	// Wrong party B: a different Initiator with a DIFFERENT ML-KEM decapsulation
	// key. To isolate the PQ secret-key dependence, we make B reuse A's X25519
	// PRIVATE key so the classical half is IDENTICAL between A and B; only the
	// ML-KEM key differs. Thus any key difference comes solely from the wrong KEM
	// key -> implicit rejection.
	inB, _, err := NewInitiator()
	if err != nil {
		t.Fatalf("NewInitiator B: %v", err)
	}
	inB.x25519Priv = inA.x25519Priv // classical half matched; PQ key differs (B's own mlkemDK)

	keyB, err := inB.Finish(rhello)
	if err != nil {
		t.Fatalf("Finish B: %v", err)
	}
	if len(keyB) != SessionKeyLen {
		t.Fatalf("wrong-key B key length = %d want %d", len(keyB), SessionKeyLen)
	}
	// Sink-side: wrong ML-KEM key (despite matched classical) must NOT agree.
	if bytes.Equal(keyB, keyResp) {
		t.Fatalf("SECURITY: wrong ML-KEM decapsulation key still agreed with Responder %x — implicit rejection / pq binding broken", keyB)
	}
	if bytes.Equal(keyB, keyA) {
		t.Fatalf("SECURITY: wrong ML-KEM key produced the SAME key as the correct key %x — pq secret not feeding the KDF", keyB)
	}
	t.Logf("run-uuid=%s implicit rejection confirmed: A==R=%x (agree), B=%x (differs)", runUUID, keyA[:8], keyB[:8])
}

// TestCombiner_DeterministicAndCollisionFree proves two combiner properties
// directly against the load-bearing deriveKey:
//
//  1. Determinism: the SAME (classical, pq) inputs always derive the SAME key.
//  2. Both-input dependence + no collision: changing EITHER half (even by one
//     bit) changes the output, and distinct transcripts never collide. This is a
//     direct unit-level assertion of "both components bind" that complements the
//     handshake-level tamper tests.
//
// WHY IT BITES: a combiner that ignores the pq half would derive the same key
// for (c, pq) and (c, pq') -> the pq-mutation assertion FAILS; a combiner that
// ignores the classical half would collide (c, pq) with (c', pq) -> the
// classical-mutation assertion FAILS.
func TestCombiner_DeterministicAndCollisionFree(t *testing.T) {
	const runUUID = "5a2e9b14-c803-4f7d-90b6-33cc11ee99dd"
	t.Logf("run-uuid=%s proving: deriveKey deterministic on equal inputs; flipping EITHER half changes the key", runUUID)

	classical := make([]byte, 32)
	pq := make([]byte, 32)
	if _, err := rand.Read(classical); err != nil {
		t.Fatalf("rand classical: %v", err)
	}
	if _, err := rand.Read(pq); err != nil {
		t.Fatalf("rand pq: %v", err)
	}

	base, err := deriveKey(classical, pq)
	if err != nil {
		t.Fatalf("deriveKey base: %v", err)
	}
	again, err := deriveKey(append([]byte(nil), classical...), append([]byte(nil), pq...))
	if err != nil {
		t.Fatalf("deriveKey again: %v", err)
	}
	// 1) Determinism.
	if !bytes.Equal(base, again) {
		t.Fatalf("deriveKey not deterministic:\n a=%x\n b=%x", base, again)
	}

	// 2a) Flip one bit of the CLASSICAL half -> key must change (classical binds).
	cMut := append([]byte(nil), classical...)
	cMut[0] ^= 0x01
	kClassicalMut, err := deriveKey(cMut, pq)
	if err != nil {
		t.Fatalf("deriveKey classical-mut: %v", err)
	}
	if bytes.Equal(kClassicalMut, base) {
		t.Fatalf("SECURITY: flipping the classical half did not change the key %x — classical half ignored by combiner", base)
	}

	// 2b) Flip one bit of the PQ half -> key must change (pq binds).
	pqMut := append([]byte(nil), pq...)
	pqMut[0] ^= 0x01
	kPQMut, err := deriveKey(classical, pqMut)
	if err != nil {
		t.Fatalf("deriveKey pq-mut: %v", err)
	}
	if bytes.Equal(kPQMut, base) {
		t.Fatalf("SECURITY: flipping the pq half did not change the key %x — pq half ignored by combiner", base)
	}

	// 2c) Cross-check no accidental collision between the two single-bit mutants.
	if bytes.Equal(kClassicalMut, kPQMut) {
		t.Fatalf("classical-mutant key collided with pq-mutant key %x — combiner not binding both positions distinctly", kClassicalMut)
	}

	// Empty-half rejection: a degenerate combiner that accepted an empty half
	// could silently downgrade. deriveKey must reject either empty half.
	if _, err := deriveKey(nil, pq); err == nil {
		t.Fatalf("SECURITY: deriveKey accepted an EMPTY classical half — downgrade possible")
	}
	if _, err := deriveKey(classical, nil); err == nil {
		t.Fatalf("SECURITY: deriveKey accepted an EMPTY pq half — downgrade possible")
	}
	t.Logf("run-uuid=%s combiner OK: deterministic; classical-flip and pq-flip both change the key; empty halves rejected", runUUID)
}
