package gpuattest

import (
	"bytes"
	"errors"
	"testing"
)

// runUUIDAdv2 scopes the evidence lines for this adversarial suite so the
// captured accept/reject output is traceable to this file. This file targets the
// NON-attest.go capabilities (seal / povw / spotcheck / multigpu) which the
// existing adversarial_test.go does not cover, hunting specifically for FAIL-OPEN
// verification gates: an invalid/forged/empty input wrongly treated as "valid".
const runUUIDAdv2 = "gpuattest-ADV2-3c9a17e5-8d42-4f0b-a1c7-6b2e905f7d18"

// ---------------------------------------------------------------------------
// SEAL (HXC-1571) — AES-256-GCM device seal. Fail-open class: a truncated /
// empty / nonce-only ciphertext, or a wrong-secret open, leaking plaintext or
// returning nil error.
// ---------------------------------------------------------------------------

// TestAdv_SealTruncatedAndEmptyCiphertextRejected proves that degenerate
// ciphertexts (nil, empty, shorter-than-nonce, exactly-nonce-length with no
// authenticated body) are all rejected with ErrSealOpen and leak no plaintext.
//
// SECURITY PROPERTY: OpenFromDevice must never return a nil error (success) for
// input that was not produced by SealForDevice under the same secret. The
// length guard (len < nonceSize) plus GCM auth must reject every degenerate.
//
// MUTATION (why it bites): if the length guard `len(ciphertext) < ns` were
// dropped/inverted, the short/empty cases would slice out of range or feed an
// empty body to gcm.Open; and if Open's error were swallowed, a nonce-only blob
// could decrypt to an empty plaintext with nil error (fail-open).
func TestAdv_SealTruncatedAndEmptyCiphertextRejected(t *testing.T) {
	secret := []byte("device-secret-material-xyz")

	// A genuine seal so we know the real nonce size in this build.
	genuine, err := SealForDevice([]byte("payload"), secret)
	if err != nil {
		t.Fatalf("SealForDevice: %v", err)
	}
	if len(genuine) < 13 {
		t.Fatalf("test setup: genuine ciphertext implausibly short (%d)", len(genuine))
	}
	// nonce size is 12 for standard GCM; derive a "nonce-only" blob from the real
	// ciphertext's prefix so the nonce is well-formed but there is no auth body.
	nonceOnly := append([]byte(nil), genuine[:12]...)

	cases := map[string][]byte{
		"nil_ciphertext":    nil,
		"empty_ciphertext":  {},
		"one_byte":          {0x00},
		"short_below_nonce": make([]byte, 11),
		"nonce_only_nobody": nonceOnly,
	}
	for name, ct := range cases {
		name, ct := name, ct
		t.Run(name, func(t *testing.T) {
			pt, err := OpenFromDevice(ct, secret)
			t.Logf("run=%s %s (len=%d) -> err=%v ptLen=%d", runUUIDAdv2, name, len(ct), err, len(pt))
			if err == nil {
				t.Fatalf("FAIL-OPEN: degenerate ciphertext (%s) opened with nil error", name)
			}
			if !errors.Is(err, ErrSealOpen) {
				t.Fatalf("want ErrSealOpen for %s, got %v", name, err)
			}
			if pt != nil {
				t.Fatalf("plaintext leak for %s: %q", name, pt)
			}
		})
	}
}

// TestAdv_SealWrongSecretAndEmptySecret proves the open is bound to the EXACT
// device secret, including the adversarial edge where the attacker supplies an
// empty secret. A sealed-with-empty-secret blob must NOT open under a non-empty
// secret and vice-versa.
//
// SECURITY PROPERTY: deriveKey(secret) is the only thing binding a seal to a
// device; a different (or empty) secret derives a different key, so GCM auth
// fails -> ErrSealOpen, no plaintext.
//
// MUTATION (why it bites): if deriveKey ignored the secret (constant key), the
// cross-secret open below would SUCCEED and leak plaintext.
func TestAdv_SealWrongSecretAndEmptySecret(t *testing.T) {
	payload := []byte("attest-report:ok=true")

	ctReal, err := SealForDevice(payload, []byte("real-secret"))
	if err != nil {
		t.Fatalf("SealForDevice(real): %v", err)
	}
	ctEmpty, err := SealForDevice(payload, []byte{})
	if err != nil {
		t.Fatalf("SealForDevice(empty-secret): %v", err)
	}

	// real ciphertext under empty secret -> rejected.
	if pt, err := OpenFromDevice(ctReal, []byte{}); err == nil || pt != nil {
		t.Fatalf("FAIL-OPEN: real seal opened under empty secret: err=%v pt=%q", err, pt)
	}
	// empty-secret ciphertext under real secret -> rejected.
	if pt, err := OpenFromDevice(ctEmpty, []byte("real-secret")); err == nil || pt != nil {
		t.Fatalf("FAIL-OPEN: empty-secret seal opened under real secret: err=%v pt=%q", err, pt)
	}
	// control: each opens under its own secret.
	if pt, err := OpenFromDevice(ctReal, []byte("real-secret")); err != nil || !bytes.Equal(pt, payload) {
		t.Fatalf("control real round-trip failed: err=%v pt=%q", err, pt)
	}
	if pt, err := OpenFromDevice(ctEmpty, []byte{}); err != nil || !bytes.Equal(pt, payload) {
		t.Fatalf("control empty-secret round-trip failed: err=%v pt=%q", err, pt)
	}
	t.Logf("run=%s seal bound to exact secret incl. empty-vs-nonempty", runUUIDAdv2)
}

// ---------------------------------------------------------------------------
// PoVW (HXC-1569) — seeded matmul proof chain. Fail-open class: a structurally
// malformed proof (wrong chain length, N<1) or a forged proof whose links were
// rebuilt to be internally consistent but for a DIFFERENT seed, wrongly
// returning ok=true.
// ---------------------------------------------------------------------------

// TestAdv_VerifyProofMalformedStructure proves VerifyProof never returns ok=true
// for a structurally malformed proof: N<1, or len(Chain)!=N (too short OR too
// long). These are the "skip the work" fail-open shapes.
//
// MUTATION (why it bites): if the guard `p.N < 1 || len(p.Chain) != p.N` were
// weakened (e.g. only checked p.N<1, ignoring the length), a proof with a
// truncated/padded Chain could index past the recomputed chain or silently pass.
func TestAdv_VerifyProofMalformedStructure(t *testing.T) {
	good, err := GenerateProof(0xDEADBEEF, 5)
	if err != nil {
		t.Fatalf("GenerateProof: %v", err)
	}

	cases := []struct {
		name string
		p    Proof
	}{
		{"n_zero", Proof{Seed: good.Seed, N: 0, Chain: nil, Root: good.Root}},
		{"n_negative", Proof{Seed: good.Seed, N: -3, Chain: good.Chain, Root: good.Root}},
		{"chain_too_short", Proof{Seed: good.Seed, N: good.N, Chain: good.Chain[:good.N-1], Root: good.Root}},
		{"chain_too_long", Proof{Seed: good.Seed, N: good.N, Chain: append(append([][32]byte{}, good.Chain...), good.Chain[0]), Root: good.Root}},
		{"chain_nil", Proof{Seed: good.Seed, N: good.N, Chain: nil, Root: good.Root}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ok, _, err := VerifyProof(tc.p)
			t.Logf("run=%s %s -> ok=%v err=%v", runUUIDAdv2, tc.name, ok, err)
			if ok {
				t.Fatalf("FAIL-OPEN: malformed proof (%s) verified ok=true", tc.name)
			}
			if !errors.Is(err, ErrProofBroken) {
				t.Fatalf("want ErrProofBroken for %s, got %v", tc.name, err)
			}
		})
	}
}

// TestAdv_VerifyProofForgedConsistentChainWrongSeed proves an attacker cannot
// substitute a self-consistent proof computed for a DIFFERENT seed: VerifyProof
// recomputes A,B,C from p.Seed, so a chain genuinely built for seedX presented
// with claimed Seed=seedY (Y!=X) fails at link 0.
//
// SECURITY PROPERTY: the proof is bound to its claimed seed; you cannot mint a
// valid proof by precomputing one seed's chain and relabelling the seed.
//
// MUTATION (why it bites): if VerifyProof trusted p.Chain/p.Root without
// recomputing from p.Seed (e.g. only checked internal chain consistency), this
// relabelled proof would pass.
func TestAdv_VerifyProofForgedConsistentChainWrongSeed(t *testing.T) {
	const n = 6
	real, err := GenerateProof(0x1111, n)
	if err != nil {
		t.Fatalf("GenerateProof: %v", err)
	}
	// A fully self-consistent proof for a different seed.
	other, err := GenerateProof(0x2222, n)
	if err != nil {
		t.Fatalf("GenerateProof(other): %v", err)
	}
	if other.Root == real.Root {
		t.Fatalf("test setup: distinct seeds must give distinct roots")
	}

	// Relabel: claim Seed=0x1111 but carry the chain+root genuinely built for
	// 0x2222. Internally consistent, but for the wrong seed.
	forged := Proof{Seed: real.Seed, N: n, Chain: other.Chain, Root: other.Root}
	ok, link, err := VerifyProof(forged)
	t.Logf("run=%s relabelled-seed proof -> ok=%v link=%d err=%v", runUUIDAdv2, ok, link, err)
	if ok {
		t.Fatalf("FAIL-OPEN: chain for a different seed verified ok=true under relabelled seed")
	}
	if link != 0 {
		t.Fatalf("want first break at link 0 (recompute diverges immediately), got %d", link)
	}
	if !errors.Is(err, ErrProofBroken) {
		t.Fatalf("want ErrProofBroken, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SPOTCHECK (HXC-1570) — Merkle inclusion spot-check. Fail-open class: a forged
// OpenWord (wrong word, mismatched/short/long proof, sibling-collision index)
// wrongly hashing to the trusted root; or a structurally degenerate proof that
// short-circuits acceptance.
// ---------------------------------------------------------------------------

// TestAdv_SpotCheckProofLengthMismatch proves verifyInclusion is bound to the
// EXACT proof length for the committed width n: a proof that is too short (loop
// runs out of siblings) OR too long (trailing siblings) for index i must be
// rejected, even when the word bytes are genuine.
//
// SECURITY PROPERTY: the `p == len(path)` postcondition plus the in-loop
// `p >= len(path)` guard pin the proof to exactly ceil(log2 n) siblings; an
// attacker cannot pad or trim the path to coax a root match.
//
// MUTATION (why it bites): dropping the `p == len(path)` check would accept a
// proof with extra trailing siblings; dropping the `p >= len(path)` guard would
// panic or mis-accept a short proof.
func TestAdv_SpotCheckProofLengthMismatch(t *testing.T) {
	const n = 8
	resp := NewWordResponse(makeWords(n))
	root := resp.Root

	genuine, ok := resp.Open(5)
	if !ok {
		t.Fatalf("Open(5) failed")
	}
	// control: genuine passes.
	if err := SpotCheck(root, n, []OpenWord{genuine}); err != nil {
		t.Fatalf("control genuine spot-check failed: %v", err)
	}

	// too short: drop the last sibling.
	short := OpenWord{Index: genuine.Index, Word: genuine.Word, Proof: genuine.Proof[:len(genuine.Proof)-1]}
	if err := SpotCheck(root, n, []OpenWord{short}); !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("FAIL-OPEN: too-short proof accepted, got %v", err)
	}
	// too long: append a bogus sibling.
	long := OpenWord{Index: genuine.Index, Word: genuine.Word, Proof: append(append([][32]byte{}, genuine.Proof...), [32]byte{0xAB})}
	if err := SpotCheck(root, n, []OpenWord{long}); !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("FAIL-OPEN: too-long proof accepted, got %v", err)
	}
	// empty proof for a multi-level tree.
	none := OpenWord{Index: genuine.Index, Word: genuine.Word, Proof: nil}
	if err := SpotCheck(root, n, []OpenWord{none}); !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("FAIL-OPEN: empty proof accepted for multi-level tree, got %v", err)
	}
	t.Logf("run=%s spotcheck pinned to exact proof length (short/long/empty all rejected)", runUUIDAdv2)
}

// TestAdv_SpotCheckWrongIndexGenuineWordProof proves that presenting a genuine
// word+proof for leaf i but CLAIMING a different index j routes the hash up the
// wrong path and is rejected. This guards against an attacker reusing a valid
// (word,proof) pair under a different committed position.
//
// SECURITY PROPERTY: the index drives the left/right folding at each level
// (idx%2) and the width descent; a mismatched index produces a different root.
//
// MUTATION (why it bites): if verifyInclusion ignored index parity (always
// folded h on the left, say), some wrong-index presentations would collide.
func TestAdv_SpotCheckWrongIndexGenuineWordProof(t *testing.T) {
	const n = 8
	resp := NewWordResponse(makeWords(n))
	root := resp.Root

	src, ok := resp.Open(2) // genuine word+proof for index 2
	if !ok {
		t.Fatalf("Open(2) failed")
	}
	// Present index 2's genuine word and proof but claim it is index 5.
	mis := OpenWord{Index: 5, Word: src.Word, Proof: src.Proof}
	err := SpotCheck(root, n, []OpenWord{mis})
	t.Logf("run=%s genuine(word,proof) of idx2 claimed as idx5 -> err=%v", runUUIDAdv2, err)
	if !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("FAIL-OPEN: genuine word+proof accepted under wrong index, got %v", err)
	}
}

// TestAdv_SpotCheckOddTreeDuplicatedNodeNoForge targets the odd-width Merkle
// edge: when a level has an odd node count the last node is duplicated. An
// attacker might try to exploit the duplicate by presenting the duplicated
// sibling to forge inclusion of a non-committed last word. We assert that the
// genuine last index verifies, but a tampered last word does NOT, on an odd tree.
//
// MUTATION (why it bites): a bug in the odd-duplication sibling selection
// (merkleProof / verifyInclusion `sib >= len(cur)` handling) could let a
// tampered last word still hash to root.
func TestAdv_SpotCheckOddTreeDuplicatedNodeNoForge(t *testing.T) {
	const n = 5 // odd, forces duplication at level 0 (idx 4) and above
	resp := NewWordResponse(makeWords(n))
	root := resp.Root

	last, ok := resp.Open(n - 1)
	if !ok {
		t.Fatalf("Open(last) failed")
	}
	if err := SpotCheck(root, n, []OpenWord{last}); err != nil {
		t.Fatalf("control: genuine last word on odd tree rejected: %v", err)
	}
	// Tamper the last word, keep the genuine duplicated proof path.
	tampered := OpenWord{Index: last.Index, Word: []byte("FORGED-last-word"), Proof: last.Proof}
	err := SpotCheck(root, n, []OpenWord{tampered})
	t.Logf("run=%s odd-tree tampered last word -> err=%v", runUUIDAdv2, err)
	if !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("FAIL-OPEN: tampered last word on odd tree accepted, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// MULTIGPU (HXC-1573) — node enumeration + per-device verification. Fail-open
// class: a node proof bundle whose individual proof is forged/expired still
// passing VerifyNode; or a swapped proof bundle (proof for device A presented in
// device B's slot).
// ---------------------------------------------------------------------------

// TestAdv_VerifyNodeRejectsInternallyInconsistentMember proves VerifyNode is
// fail-CLOSED against a member whose Response is tampered RELATIVE TO the
// descriptor carried in its own bundle: it does NOT silently skip the bad member
// and report success. Here member 1's response signature is corrupted (a signed
// field altered) while its descriptor is left genuine, so the per-device Verify
// catches it. This proves VerifyNode returns the first member error rather than
// continuing past it.
//
// MUTATION (why it bites): if VerifyNode `continue`d on a per-proof error
// instead of returning it (skip-bad-member fail-open), the tampered member would
// be ignored and the node wrongly attested.
func TestAdv_VerifyNodeRejectsInternallyInconsistentMember(t *testing.T) {
	v := NewVerifier()
	d0, _ := NewDevice("NVIDIA", "H100", "uuid-0", 80<<30)
	d1, _ := NewDevice("NVIDIA", "H100", "uuid-1", 80<<30)

	node := NodeDescriptor{NodeID: "node-A", Devices: []*Device{d0, d1}}
	proofs, err := EnumerateNode(v, node, 100, 200, 150)
	if err != nil {
		t.Fatalf("EnumerateNode: %v", err)
	}
	// control: genuine bundle verifies in-window.
	if err := VerifyNode(v, proofs, 150); err != nil {
		t.Fatalf("control: genuine node verify failed: %v", err)
	}

	// Fresh verifier so the rejection we measure is the tamper, not a replayed
	// (already-consumed) nonce from the control run.
	v2 := NewVerifier()
	proofs2, err := EnumerateNode(v2, node, 100, 200, 150)
	if err != nil {
		t.Fatalf("EnumerateNode#2: %v", err)
	}
	// Tamper member 1's response: corrupt the Tick (a signed field) so the
	// signature no longer verifies under member 1's own (genuine) descriptor key.
	proofs2[1].Response.Tick ^= 0x55

	err = VerifyNode(v2, proofs2, 150)
	t.Logf("run=%s node with 1 internally-inconsistent member -> err=%v", runUUIDAdv2, err)
	if err == nil {
		t.Fatalf("FAIL-OPEN: node verification passed with a tampered member")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("want ErrTampered for tampered member, got %v", err)
	}
}

// TestAdv_VerifyNodeSelfAssertedDescriptorIsLatentRisk PINS a SHARP design
// property of VerifyNode that is easy to mistake for a fail-open bug but is the
// documented contract: VerifyNode verifies each member against the descriptor
// CARRIED IN ITS OWN BUNDLE (proof.Descriptor), NOT against an externally-pinned
// trusted descriptor set. Consequently a fully SELF-CONSISTENT member forged by
// an attacker's OWN device (attacker descriptor + attacker key + attacker
// signature over the live challenge) VERIFIES — because nothing pins the bundle
// descriptor to a known-good identity.
//
// This is a LATENT RISK to surface to callers, NOT a code fix: a relying party
// MUST cross-check each returned proof.Descriptor / proof.GPUUUID against the
// node's expected, independently-trusted device set before trusting VerifyNode's
// nil. Pinned so any future hardening (e.g. VerifyNode taking a trusted
// descriptor set) is a deliberate, reviewed change.
func TestAdv_VerifyNodeSelfAssertedDescriptorIsLatentRisk(t *testing.T) {
	v := NewVerifier()
	genuine, _ := NewDevice("NVIDIA", "H100", "uuid-genuine", 80<<30)
	attacker, _ := NewDevice("EVILCORP", "FAKE-GPU", "uuid-attacker", 1<<30)

	// Enumerate the genuine node to obtain a real, live challenge for slot 0.
	node := NodeDescriptor{NodeID: "node-A", Devices: []*Device{genuine}}
	proofs, err := EnumerateNode(v, node, 100, 200, 150)
	if err != nil {
		t.Fatalf("EnumerateNode: %v", err)
	}

	// Build a self-consistent forged bundle: attacker answers the SAME challenge
	// with the attacker's own identity, and the bundle carries the attacker's own
	// descriptor (self-asserted). Use a fresh verifier so replay is not what we
	// observe.
	v2 := NewVerifier() // fresh consumed-nonce set: the challenge nonce is unseen here
	ch := proofs[0].Challenge
	forged := []DeviceProof{{
		GPUUUID:    attacker.Descriptor.UUID,
		Challenge:  ch,
		Response:   attacker.Respond(ch, 150),
		Descriptor: attacker.Descriptor,
	}}

	err = VerifyNode(v2, forged, 150)
	t.Logf("run=%s LATENT-RISK pin: self-asserted attacker bundle -> err=%v (verifies; caller MUST pin descriptors)", runUUIDAdv2, err)
	if err != nil {
		t.Fatalf("contract changed: self-consistent self-asserted bundle now returns %v; if VerifyNode was hardened to pin trusted descriptors, update this pin", err)
	}
}

// TestAdv_VerifyNodeExpiredMemberRejected proves freshness propagates through
// node verification: if nowTick is past the bundle's expiry, every member is
// rejected (here, the first) and the node fails. Replaying a whole stale node
// bundle must not pass.
//
// MUTATION (why it bites): removing the expiry guard in Verify would let the
// stale node bundle pass VerifyNode.
func TestAdv_VerifyNodeExpiredMemberRejected(t *testing.T) {
	v := NewVerifier()
	d0, _ := NewDevice("NVIDIA", "H100", "uuid-A", 80<<30)
	node := NodeDescriptor{NodeID: "node-B", Devices: []*Device{d0}}
	proofs, err := EnumerateNode(v, node, 100, 200, 150)
	if err != nil {
		t.Fatalf("EnumerateNode: %v", err)
	}
	err = VerifyNode(v, proofs, 201) // past expiry 200
	t.Logf("run=%s stale node bundle (now=201 > expiry=200) -> err=%v", runUUIDAdv2, err)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("FAIL-OPEN: stale node bundle not rejected as expired, got %v", err)
	}
}

// TestAdv_VerifyNodeEmptyBundleIsVacuous PINS the documented contract that
// VerifyNode over an EMPTY proof slice returns nil. This is NOT a fix — it is a
// surfaced LATENT RISK: a caller that treats `VerifyNode(...)==nil` as "node is
// attested" without also checking len(proofs)>0 (and that the proofs cover the
// expected devices) would accept a node that proved NOTHING. EnumerateNode never
// produces an empty slice (it errors on ErrNoDevices), so the only way to reach
// this is a caller fabricating/empty bundle. Pinned so a future change to make
// VerifyNode fail-closed on empty input is a deliberate, reviewed decision.
func TestAdv_VerifyNodeEmptyBundleIsVacuous(t *testing.T) {
	v := NewVerifier()
	err := VerifyNode(v, nil, 150)
	t.Logf("run=%s LATENT-RISK pin: VerifyNode(empty) -> err=%v (vacuous pass)", runUUIDAdv2, err)
	if err != nil {
		t.Fatalf("contract changed: VerifyNode(empty) now returns %v; if intentional, update this pin", err)
	}
}

// TestAdv_SpotCheckEmptyOpenedIsVacuous PINS the analogous spot-check contract:
// SpotCheck with zero opened words returns nil ("verify only the supplied
// words"). LATENT RISK: a caller must independently require that a sufficient
// number of indices were actually opened/checked — an empty opened set proves
// nothing yet returns nil. Pinned, not fixed.
func TestAdv_SpotCheckEmptyOpenedIsVacuous(t *testing.T) {
	resp := NewWordResponse(makeWords(8))
	err := SpotCheck(resp.Root, 8, nil)
	t.Logf("run=%s LATENT-RISK pin: SpotCheck(empty opened) -> err=%v (vacuous pass)", runUUIDAdv2, err)
	if err != nil {
		t.Fatalf("contract changed: SpotCheck(empty) now returns %v; if intentional, update this pin", err)
	}
}
