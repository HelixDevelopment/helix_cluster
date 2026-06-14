package edgeverify

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// advRun identifies this adversarial run in logs (CLAUDE-1 sink-side evidence).
const advRun = "edgeverify-adversarial-9c4e2b71-HXC1197"

// oracleChecksum is an INDEPENDENT SHA-256 oracle for adversarial assertions.
// It deliberately does not call the production Checksum so that a mutation of
// the production primitive cannot also corrupt the expected value.
func oracleChecksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// peerVerdictByID is a local lookup so the adversarial file is self-contained.
func peerVerdictByID(rep Report, id string) *PeerVerdict {
	for i := range rep.Peers {
		if rep.Peers[i].DeviceID == id {
			return &rep.Peers[i]
		}
	}
	return nil
}

// TestVerify_GenuineBaseline_Accepted is the anti-always-reject baseline.
//
// A genuine redundant run where every peer reproduced the trusted anchor
// byte-for-byte MUST be ACCEPTED. If a mutation made Verify reject
// unconditionally (e.g. OK:=false, or always appending to Mismatched), this
// test FAILS, so the deny-tests below cannot be satisfied by a trivially
// always-rejecting verifier.
func TestVerify_GenuineBaseline_Accepted(t *testing.T) {
	t.Logf("run=%s case=genuine-baseline start", advRun)

	payload := []byte("EDGE-WORKUNIT-77::RESULT=0x0F1E2D3C4B5A6978::v1")
	trusted := Result{DeviceID: "anchor", Payload: payload}
	peers := []Result{
		{DeviceID: "edge-1", Payload: append([]byte(nil), payload...)},
		{DeviceID: "edge-2", Payload: append([]byte(nil), payload...)},
	}

	rep := Verify(trusted, peers)
	t.Logf("run=%s case=genuine-baseline accepted=%v mismatched=%v trustedSum=%s",
		advRun, rep.Accepted, rep.Mismatched, rep.TrustedChecksum)

	if !rep.Accepted {
		t.Fatalf("run=%s GENUINE run must be ACCEPTED, got Accepted=false mismatched=%v", advRun, rep.Mismatched)
	}
	if len(rep.Mismatched) != 0 {
		t.Fatalf("run=%s GENUINE run must have zero mismatches, got %v", advRun, rep.Mismatched)
	}
	if rep.TrustedChecksum != oracleChecksum(payload) {
		t.Fatalf("run=%s trusted checksum %s != independent oracle %s", advRun, rep.TrustedChecksum, oracleChecksum(payload))
	}
	for _, v := range rep.Peers {
		if !v.OK {
			t.Fatalf("run=%s genuine peer %s must be OK, got %+v", advRun, v.DeviceID, v)
		}
	}
}

// TestVerify_TamperedAndForged_Rejected is the table-driven adversarial deny
// battery. Every row is an edge result that DISAGREES with the trusted anchor
// in some way an attacker might attempt; each MUST be flagged not-OK and the
// overall verdict MUST be REJECTED. A fail-open mutation (accept-on-mismatch,
// inverted/removed comparison, length-only or substring-weakened compare) makes
// at least one row FAIL.
func TestVerify_TamperedAndForged_Rejected(t *testing.T) {
	trustedPayload := []byte("0123456789ABCDEF0123456789ABCDEF") // 32 bytes

	// Construct adversarial peer payloads relative to the trusted payload.
	tamperedTail := append([]byte(nil), trustedPayload...)
	tamperedTail[len(tamperedTail)-1] ^= 0x01 // flip last bit (off-by-one boundary)

	tamperedHead := append([]byte(nil), trustedPayload...)
	tamperedHead[0] ^= 0x80 // flip first bit (boundary, high bit)

	truncated := append([]byte(nil), trustedPayload[:len(trustedPayload)-1]...) // shorter
	extended := append(append([]byte(nil), trustedPayload...), 'X')             // longer

	// A peer whose payload's checksum is a SUBSTRING relationship would only
	// matter if the gate used Contains/HasPrefix; we additionally prove the
	// gate is exact-string equality in a dedicated test below. Here we add a
	// classic forged-but-plausible result.
	forged := []byte("0123456789ABCDEF0123456789ABCDEG") // last char F->G, same length

	cases := []struct {
		name    string
		payload []byte
	}{
		{"tampered-last-byte", tamperedTail},
		{"tampered-first-byte", tamperedHead},
		{"truncated-shorter", truncated},
		{"extended-longer", extended},
		{"forged-same-length", forged},
		{"empty-vs-nonempty", []byte{}},
		{"nil-vs-nonempty", nil},
		{"totally-different", []byte("attacker-controlled-result")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			trusted := Result{DeviceID: "anchor", Payload: append([]byte(nil), trustedPayload...)}
			peers := []Result{{DeviceID: "edge-evil", Payload: tc.payload}}

			rep := Verify(trusted, peers)
			v := peerVerdictByID(rep, "edge-evil")
			t.Logf("run=%s case=%s accepted=%v peerOK=%v peerSum=%s trustedSum=%s",
				advRun, tc.name, rep.Accepted, v != nil && v.OK, func() string {
					if v == nil {
						return "<nil>"
					}
					return v.Checksum
				}(), rep.TrustedChecksum)

			if rep.Accepted {
				t.Fatalf("run=%s case=%s: tampered/forged peer must REJECT, got Accepted=true", advRun, tc.name)
			}
			if v == nil || v.OK {
				t.Fatalf("run=%s case=%s: peer must be flagged not-OK, got %+v", advRun, tc.name, v)
			}
			if v.Checksum == rep.TrustedChecksum {
				t.Fatalf("run=%s case=%s: peer checksum must differ from trusted, both=%s", advRun, tc.name, v.Checksum)
			}
			if len(rep.Mismatched) != 1 || rep.Mismatched[0] != "edge-evil" {
				t.Fatalf("run=%s case=%s: expected [edge-evil] mismatched, got %v", advRun, tc.name, rep.Mismatched)
			}
			// Independent oracle confirms the gate's checksum is the real
			// SHA-256, not a constant that merely happens to differ.
			if v.Checksum != oracleChecksum(tc.payload) {
				t.Fatalf("run=%s case=%s: peer checksum %s != independent oracle %s",
					advRun, tc.name, v.Checksum, oracleChecksum(tc.payload))
			}
		})
	}
}

// TestVerify_ExactMatchNotSubstring proves the acceptance gate is EXACT string
// equality of the full hex checksum, not a substring/prefix weakening.
//
// We feed a peer whose REAL checksum is a strict, non-equal value, then assert
// the gate rejects it. Critically we also assert that the production comparison
// is not satisfiable by any proper substring relationship: a genuine match has
// equal length AND equal content. If Verify were weakened to
// strings.Contains(trustedSum, sum) or HasPrefix, a shorter-but-contained hex
// string could wrongly pass; we guard the invariant directly.
func TestVerify_ExactMatchNotSubstring(t *testing.T) {
	t.Logf("run=%s case=exact-not-substring start", advRun)

	trustedPayload := []byte("anchor-result-bytes")
	trustedSum := oracleChecksum(trustedPayload)

	// A peer payload whose checksum we know differs from trustedSum.
	peerPayload := []byte("imposter-result-bytes")
	peerSum := oracleChecksum(peerPayload)

	// Sanity: the two real checksums must not be substrings of one another, so
	// that a Contains/HasPrefix weakening would be measurably wrong here.
	if strings.Contains(trustedSum, peerSum) || strings.HasPrefix(trustedSum, peerSum) {
		t.Fatalf("run=%s test setup invalid: peerSum is a substring/prefix of trustedSum", advRun)
	}

	trusted := Result{DeviceID: "anchor", Payload: trustedPayload}
	peers := []Result{{DeviceID: "edge-imposter", Payload: peerPayload}}

	rep := Verify(trusted, peers)
	v := peerVerdictByID(rep, "edge-imposter")
	t.Logf("run=%s case=exact-not-substring accepted=%v peerOK=%v peerSum=%s trustedSum=%s",
		advRun, rep.Accepted, v.OK, v.Checksum, rep.TrustedChecksum)

	if rep.Accepted || v == nil || v.OK {
		t.Fatalf("run=%s imposter peer must be REJECTED (exact equality), got accepted=%v verdict=%+v", advRun, rep.Accepted, v)
	}
	// The gate's OK must be precisely Checksum==TrustedChecksum.
	if v.OK != (v.Checksum == v.TrustedChecksum) {
		t.Fatalf("run=%s OK flag must equal exact checksum equality, got OK=%v sum=%s trusted=%s",
			advRun, v.OK, v.Checksum, v.TrustedChecksum)
	}
	if v.Checksum != peerSum || v.TrustedChecksum != trustedSum {
		t.Fatalf("run=%s checksums must match independent oracle: peer %s/%s trusted %s/%s",
			advRun, v.Checksum, peerSum, v.TrustedChecksum, trustedSum)
	}
}

// TestVerify_NilAndEmptyInputs_NoPanicFailClosed exercises degenerate inputs:
// nil/empty payloads must NOT panic and must follow the content-equality
// contract. Two empty payloads agree (same SHA-256), so a nil-trusted/nil-peer
// pair is ACCEPTED; an empty-trusted vs non-empty peer is REJECTED. This proves
// the verifier neither panics nor silently fails open on a zero credential.
func TestVerify_NilAndEmptyInputs_NoPanicFailClosed(t *testing.T) {
	t.Logf("run=%s case=nil-empty start", advRun)

	// nil trusted, nil + empty peers: all are SHA-256 of zero bytes -> agree.
	repAgree := Verify(Result{DeviceID: "anchor", Payload: nil}, []Result{
		{DeviceID: "edge-nil", Payload: nil},
		{DeviceID: "edge-empty", Payload: []byte{}},
	})
	t.Logf("run=%s case=nil-empty agree accepted=%v mismatched=%v sum=%s",
		advRun, repAgree.Accepted, repAgree.Mismatched, repAgree.TrustedChecksum)

	if repAgree.TrustedChecksum != oracleChecksum(nil) {
		t.Fatalf("run=%s nil payload checksum %s != oracle %s", advRun, repAgree.TrustedChecksum, oracleChecksum(nil))
	}
	if !repAgree.Accepted || len(repAgree.Mismatched) != 0 {
		t.Fatalf("run=%s empty==empty must be ACCEPTED, got accepted=%v mismatched=%v",
			advRun, repAgree.Accepted, repAgree.Mismatched)
	}

	// empty trusted vs a non-empty peer: MUST be rejected (fail-closed).
	repReject := Verify(Result{DeviceID: "anchor", Payload: nil}, []Result{
		{DeviceID: "edge-real", Payload: []byte("non-empty")},
	})
	v := peerVerdictByID(repReject, "edge-real")
	t.Logf("run=%s case=nil-empty reject accepted=%v peerOK=%v mismatched=%v",
		advRun, repReject.Accepted, v != nil && v.OK, repReject.Mismatched)

	if repReject.Accepted || v == nil || v.OK {
		t.Fatalf("run=%s empty-trusted vs non-empty peer must REJECT, got accepted=%v verdict=%+v",
			advRun, repReject.Accepted, v)
	}
}

// TestVerify_EmptyPeers_VacuousAcceptDocumented pins the no-peers behavior. With
// zero peers there is nothing that disagreed, so the documented contract is a
// vacuous Accept. This is asserted explicitly so the behavior is INTENTIONAL
// and a future change that silently flips it (either direction) is caught.
func TestVerify_EmptyPeers_VacuousAcceptDocumented(t *testing.T) {
	t.Logf("run=%s case=empty-peers start", advRun)

	rep := Verify(Result{DeviceID: "anchor", Payload: []byte("x")}, nil)
	t.Logf("run=%s case=empty-peers accepted=%v peers=%d mismatched=%v",
		advRun, rep.Accepted, len(rep.Peers), rep.Mismatched)

	if !rep.Accepted {
		t.Fatalf("run=%s zero-peer verdict is documented vacuous-Accept, got Accepted=false", advRun)
	}
	if len(rep.Peers) != 0 || len(rep.Mismatched) != 0 {
		t.Fatalf("run=%s zero-peer verdict must have empty Peers/Mismatched, got peers=%d mismatched=%v",
			advRun, len(rep.Peers), rep.Mismatched)
	}
}

// TestVerify_MultipleMismatches_AllFlaggedSorted ensures that when several peers
// disagree, EVERY offender is flagged (not just the first), the matching peer
// stays OK, and Mismatched is sorted. A break that stopped at the first mismatch
// or skipped the sort is caught.
func TestVerify_MultipleMismatches_AllFlaggedSorted(t *testing.T) {
	t.Logf("run=%s case=multi-mismatch start", advRun)

	good := []byte("GOOD-RESULT")
	trusted := Result{DeviceID: "anchor", Payload: good}
	peers := []Result{
		{DeviceID: "zeta-bad", Payload: []byte("BAD-1")},
		{DeviceID: "alpha-good", Payload: append([]byte(nil), good...)},
		{DeviceID: "mike-bad", Payload: []byte("BAD-2")},
	}

	rep := Verify(trusted, peers)
	t.Logf("run=%s case=multi-mismatch accepted=%v mismatched=%v", advRun, rep.Accepted, rep.Mismatched)

	if rep.Accepted {
		t.Fatalf("run=%s multiple disagreeing peers must REJECT, got Accepted=true", advRun)
	}
	if len(rep.Mismatched) != 2 || rep.Mismatched[0] != "mike-bad" || rep.Mismatched[1] != "zeta-bad" {
		t.Fatalf("run=%s expected sorted [mike-bad zeta-bad], got %v", advRun, rep.Mismatched)
	}
	if g := peerVerdictByID(rep, "alpha-good"); g == nil || !g.OK {
		t.Fatalf("run=%s matching peer alpha-good must stay OK, got %+v", advRun, g)
	}
}
