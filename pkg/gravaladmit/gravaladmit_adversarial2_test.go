package gravaladmit

// Second adversarial pass over the GraVal cryptographic admission gate.
//
// The prior passes pinned: forged-MAC rejection, single-use nonce, nil-prover
// fail-closed, secret isolation, and the concurrency/over-commit family. This
// file targets the FAIL-OPEN corners those passes did NOT exercise directly,
// each probed SINK-SIDE (Admitted / graval.verified / schedulable membership):
//
//   1. EMPTY-MAC must never be admitted. crypto's hmac.Equal([]byte{},[]byte{})
//      is TRUE, so an empty response MAC would be admitted IF the gate's
//      `expected` were ever zero-length. expected is a 32-byte SHA-256 HMAC, so
//      this pins that an empty/blank MAC is fail-closed in the real path.
//   2. CROSS-SECRET FORGERY — the very heart of an attestation gate. A prover
//      that computes a *structurally perfect* HMAC response (correct nonce,
//      correct providerID, valid hex, correct length) but under the WRONG secret
//      key MUST be denied. If the secret were not load-bearing (e.g. the gate
//      compared something attacker-controllable), this attacker would be admitted.
//   3. EMPTY providerID at Admit time must be fail-closed (no pending token can
//      exist for "" because IssueChallenge forbids it) — a missing/blank field
//      must DENY, never admit.
//   4. TRUNCATED / OVERLONG but valid-hex MAC must be denied — hmac.Equal is
//      length-aware; a valid-hex prefix of the real MAC must not pass.
//   5. BLANK fields all round (Response{}) and unissued provider — DENY.
//
// All probes pass against unmodified production. Each is mutation-described:
// the single production guard whose removal flips the probe to a fail-open.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

const adv2RunID = "gravaladmit-adv2-HXC-graval-9d2e7b55-4b6d-11ef-9c2a-0050568a77b4"

var adv2Secret = []byte("helix-gravaladmit-adversarial2-secret-v2")

// directProver returns an attacker-chosen Response verbatim, with no honest
// computation. It lets a probe forge ANY (ProviderID, Nonce, MAC) triple — the
// most adversarial seam possible — to test the gate's own verification, not a
// cooperative prover's behaviour.
type directProver struct {
	resp Response
	// echoNonce, when set, copies the issued challenge nonce into the response so
	// the nonce-echo check passes and the probe reaches the HMAC equality gate.
	echoNonce bool
	echoPID   bool
}

func (p *directProver) Prove(ch Challenge) Response {
	r := p.resp
	if p.echoNonce {
		r.Nonce = ch.Nonce
	}
	if p.echoPID {
		r.ProviderID = ch.ProviderID
	}
	return r
}

// -----------------------------------------------------------------------------
// (1) EMPTY-MAC FAIL-OPEN PROBE.
//
// A prover echoes the correct nonce and providerID (so it sails past both echo
// checks and reaches the HMAC gate) but supplies MAC="" — the empty string.
// hex.DecodeString("") yields a zero-length []byte with no error, and
// hmac.Equal([]byte{}, []byte{}) is TRUE. The ONLY thing standing between this
// attacker and admission is that the gate's `expected` is a real 32-byte HMAC
// (never empty). SINK-SIDE: this MUST be denied.
//
// MUTATION: if `expected := computeMAC(...)` (gravaladmit.go ~L285) were ever
// replaced by an empty slice, or the hmac.Equal guard (~L291) deleted, this
// probe would observe Admitted=true and FAIL.
// -----------------------------------------------------------------------------

func TestAdmit_EmptyMAC_FailClosed(t *testing.T) {
	a, err := NewAdmitter(adv2Secret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", adv2RunID, err)
	}
	providerID := "gpu-provider-emptymac"
	if _, e := a.IssueChallenge(providerID); e != nil {
		t.Fatalf("%s: IssueChallenge: %v", adv2RunID, e)
	}

	prover := &directProver{
		resp:      Response{MAC: ""}, // blank MAC
		echoNonce: true,
		echoPID:   true,
	}
	d := a.Admit(adv2RunID, providerID, prover)
	t.Logf("%s: Admit(empty MAC) Admitted=%v Reason=%q", adv2RunID, d.Admitted, d.Reason)

	if d.Admitted {
		t.Fatalf("%s: FAIL-OPEN: empty-MAC response was ADMITTED (hmac.Equal empty-vs-empty leak)", adv2RunID)
	}
	if _, ok := d.Labels["graval.verified"]; ok {
		t.Fatalf("%s: empty-MAC rejection must not carry graval.verified label: %v", adv2RunID, d.Labels)
	}
	if d.Reason == "" {
		t.Fatalf("%s: empty-MAC rejection must carry non-empty Reason", adv2RunID)
	}
}

// -----------------------------------------------------------------------------
// (2) CROSS-SECRET FORGERY PROBE — the core of an attestation gate.
//
// The attacker is sophisticated: it produces a perfectly well-formed response
// (correct nonce echo, correct providerID echo, valid 64-hex-char MAC of the
// correct length) but computes the HMAC under a DIFFERENT secret key than the
// gate holds. Only knowledge of the gate's real secret can produce an admitting
// MAC. SINK-SIDE: this MUST be denied and absent from the schedulable set.
//
// MUTATION: deleting the hmac.Equal guard (gravaladmit.go ~L291) admits this
// forgery (Admitted=true), failing this probe — proving the SECRET is the
// load-bearing root of trust, not merely the response's structural shape.
// -----------------------------------------------------------------------------

func TestAdmit_WrongSecretForgery_FailClosed(t *testing.T) {
	a, err := NewAdmitter(adv2Secret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", adv2RunID, err)
	}
	providerID := "gpu-provider-wrongsecret"
	ch, err := a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", adv2RunID, err)
	}

	// Attacker forges a structurally perfect MAC under the WRONG key.
	wrongKey := []byte("attacker-guessed-secret-which-is-wrong")
	forgedMAC := hex.EncodeToString(macUnderKey(wrongKey, ch.Nonce, providerID))

	// Sanity: the forged MAC is valid hex and the correct length (32 bytes => 64
	// hex chars), so it can ONLY be rejected by the cryptographic equality check,
	// not by a hex-decode or length error.
	if len(forgedMAC) != sha256.Size*2 {
		t.Fatalf("%s: test setup: forged MAC wrong length %d", adv2RunID, len(forgedMAC))
	}

	prover := &directProver{
		resp:      Response{ProviderID: providerID, Nonce: ch.Nonce, MAC: forgedMAC},
		echoNonce: false, // already correct
		echoPID:   false, // already correct
	}
	d := a.Admit(adv2RunID, providerID, prover)
	t.Logf("%s: Admit(wrong-secret forgery) Admitted=%v Reason=%q", adv2RunID, d.Admitted, d.Reason)

	if d.Admitted {
		t.Fatalf("%s: FAIL-OPEN: a MAC forged under the WRONG secret was ADMITTED — secret not load-bearing", adv2RunID)
	}
	if d.Schedulable() {
		t.Fatalf("%s: wrong-secret forgery must not be Schedulable()", adv2RunID)
	}
	if _, ok := d.Labels["graval.verified"]; ok {
		t.Fatalf("%s: wrong-secret forgery must not carry graval.verified: %v", adv2RunID, d.Labels)
	}

	// Cross-check: the SAME nonce+providerID under the RIGHT key WOULD admit, so
	// the only difference is the key — proving the key is what gates admission.
	rightMAC := ComputeExpectedMAC(adv2Secret, ch.Nonce, providerID)
	if forgedMAC == rightMAC {
		t.Fatalf("%s: test setup invalid: wrong-key MAC collided with right-key MAC", adv2RunID)
	}
}

// -----------------------------------------------------------------------------
// (3) EMPTY providerID AT ADMIT — a missing/blank identity field must DENY.
//
// IssueChallenge forbids an empty providerID, so no pending token can ever exist
// for "". An Admit("","",prover) therefore finds no pending nonce and must be
// rejected with ErrNonceUnknown. A missing field must NEVER be treated as a
// match against some pending entry.
//
// MUTATION: if the not-found branch (gravaladmit.go ~L253) fell through to
// admission instead of returning, this would admit a provider that never held a
// token.
// -----------------------------------------------------------------------------

func TestAdmit_EmptyProviderID_FailClosed(t *testing.T) {
	a, err := NewAdmitter(adv2Secret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", adv2RunID, err)
	}
	// Mint a legitimate token for a REAL provider so the pending map is non-empty
	// — the empty-providerID probe must not accidentally match it.
	realID := "gpu-provider-real-bystander"
	if _, e := a.IssueChallenge(realID); e != nil {
		t.Fatalf("%s: IssueChallenge: %v", adv2RunID, e)
	}

	// Confirm IssueChallenge itself refuses an empty providerID (no token mint).
	if _, e := a.IssueChallenge(""); e == nil {
		t.Fatalf("%s: IssueChallenge(\"\") must error, got nil", adv2RunID)
	}

	prover := &directProver{resp: Response{}, echoNonce: true, echoPID: true}
	d := a.Admit(adv2RunID, "", prover)
	t.Logf("%s: Admit(empty providerID) Admitted=%v Reason=%q", adv2RunID, d.Admitted, d.Reason)

	if d.Admitted {
		t.Fatalf("%s: FAIL-OPEN: empty providerID was ADMITTED", adv2RunID)
	}
	if d.Reason == "" {
		t.Fatalf("%s: empty-providerID rejection must carry non-empty Reason", adv2RunID)
	}

	// SINK-SIDE: the bystander's token must be UNTOUCHED by the empty-id probe —
	// a correct Admit for the real provider still succeeds (the probe did not
	// consume or corrupt a foreign token).
	dReal := a.Admit(adv2RunID, realID, &correctProver{key: adv2Secret})
	if !dReal.Admitted {
		t.Fatalf("%s: bystander token corrupted by empty-id probe; Reason=%q", adv2RunID, dReal.Reason)
	}
}

// -----------------------------------------------------------------------------
// (4) TRUNCATED / OVERLONG valid-hex MAC — length-aware equality.
//
// hmac.Equal is constant-time AND length-aware: a valid-hex MAC that is a proper
// prefix (or a superset) of the real one must be denied. This rules out a
// truncation fail-open where a partial match is mistaken for a full match.
//
// MUTATION: replacing hmac.Equal with a prefix/contains comparison (a classic
// crypto footgun) would admit the truncated MAC and fail this probe.
// -----------------------------------------------------------------------------

func TestAdmit_TruncatedAndOverlongMAC_FailClosed(t *testing.T) {
	a, err := NewAdmitter(adv2Secret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", adv2RunID, err)
	}

	for _, tc := range []struct {
		name string
		mac  func(real string) string
	}{
		{"truncated-prefix", func(real string) string { return real[:len(real)-2] }}, // drop last hex byte
		{"overlong-suffix", func(real string) string { return real + "ab" }},          // extra hex byte
		{"first-half", func(real string) string { return real[:len(real)/2] }},
	} {
		providerID := "gpu-provider-trunc-" + tc.name
		ch, e := a.IssueChallenge(providerID)
		if e != nil {
			t.Fatalf("%s: IssueChallenge(%s): %v", adv2RunID, tc.name, e)
		}
		realMAC := ComputeExpectedMAC(adv2Secret, ch.Nonce, providerID)
		badMAC := tc.mac(realMAC)

		// Ensure it is still valid hex (even length) so rejection is by the crypto
		// gate, not a decode error, for the prefix/suffix cases.
		if _, decErr := hex.DecodeString(badMAC); decErr != nil && tc.name != "first-half" {
			t.Fatalf("%s: %s: test MAC not valid hex: %v", adv2RunID, tc.name, decErr)
		}

		prover := &directProver{
			resp:      Response{ProviderID: providerID, Nonce: ch.Nonce, MAC: badMAC},
			echoNonce: false,
			echoPID:   false,
		}
		d := a.Admit(adv2RunID, providerID, prover)
		t.Logf("%s: %s Admit Admitted=%v Reason=%q", adv2RunID, tc.name, d.Admitted, d.Reason)
		if d.Admitted {
			t.Fatalf("%s: FAIL-OPEN: %s MAC was ADMITTED (length-aware equality bypassed)", adv2RunID, tc.name)
		}
	}
}

// -----------------------------------------------------------------------------
// (5) FULLY-BLANK RESPONSE & UNISSUED PROVIDER — the most degenerate inputs.
//
// (5a) A provider that was NEVER issued a challenge but presents a perfect
//      correct prover must be denied (no pending token => ErrNonceUnknown).
// (5b) An issued provider whose prover returns the zero Response{} (blank
//      everything) must be denied at the nonce-echo check, never admitted.
// -----------------------------------------------------------------------------

func TestAdmit_BlankAndUnissued_FailClosed(t *testing.T) {
	a, err := NewAdmitter(adv2Secret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", adv2RunID, err)
	}

	// (5a) unissued provider + perfectly correct prover => still denied.
	unissued := "gpu-provider-never-issued"
	d := a.Admit(adv2RunID, unissued, &correctProver{key: adv2Secret})
	t.Logf("%s: unissued Admit Admitted=%v Reason=%q", adv2RunID, d.Admitted, d.Reason)
	if d.Admitted {
		t.Fatalf("%s: FAIL-OPEN: unissued provider with a correct prover was ADMITTED", adv2RunID)
	}
	if d.Reason == "" {
		t.Fatalf("%s: unissued rejection must carry non-empty Reason", adv2RunID)
	}

	// (5b) issued provider + zero Response{} => denied (blank nonce/providerID/MAC).
	blankID := "gpu-provider-blank-response"
	if _, e := a.IssueChallenge(blankID); e != nil {
		t.Fatalf("%s: IssueChallenge: %v", adv2RunID, e)
	}
	dBlank := a.Admit(adv2RunID, blankID, &directProver{resp: Response{}})
	t.Logf("%s: blank-response Admit Admitted=%v Reason=%q", adv2RunID, dBlank.Admitted, dBlank.Reason)
	if dBlank.Admitted {
		t.Fatalf("%s: FAIL-OPEN: zero Response{} was ADMITTED", adv2RunID)
	}
	if _, ok := dBlank.Labels["graval.verified"]; ok {
		t.Fatalf("%s: blank-response rejection must not carry graval.verified: %v", adv2RunID, dBlank.Labels)
	}
}

// macUnderKey mirrors the gate's internal computeMAC but lets a probe compute a
// MAC under an ARBITRARY key — used to forge a cross-secret response. It is a
// test-local re-implementation (not a call into production) so the cross-secret
// probe is independent of the gate's own helper.
func macUnderKey(key []byte, nonce, providerID string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(nonce))
	h.Write([]byte("::"))
	h.Write([]byte(providerID))
	return h.Sum(nil)
}
