package edgeverify

import (
	"testing"
)

// runUUID identifies this deterministic test run in logs (CLAUDE-1 evidence).
const runUUID = "edgeverify-run-7f3a1c92-HXC1197"

// findVerdict returns the PeerVerdict for deviceID, or nil if absent.
func findVerdict(rep Report, deviceID string) *PeerVerdict {
	for i := range rep.Peers {
		if rep.Peers[i].DeviceID == deviceID {
			return &rep.Peers[i]
		}
	}
	return nil
}

// TestVerify_DetectsAndFlagsWrongDevice covers CLOSURE clause 1.
//
// A redundant work unit is duplicated across edge devices and ONE device
// ("edge-bad") returns a WRONG payload. The naive sink-side baseline would
// accept the wrong result because no anchor comparison happens; the verifier
// must detect the mismatch against the trusted node and REJECT/FLAG exactly
// that device, while every matching device stays OK.
//
// Mutation: if Verify accepted on mismatch (e.g. set OK=true / never appended
// to Mismatched), rep.Accepted would be true and Mismatched empty — this test
// asserts the opposite, so it FAILS under that mutation.
func TestVerify_DetectsAndFlagsWrongDevice(t *testing.T) {
	t.Logf("run=%s clause=1 start", runUUID)

	trusted := Result{DeviceID: "edge-trusted", Payload: []byte("WORKUNIT-42-RESULT=0xDEADBEEF")}
	peers := []Result{
		{DeviceID: "edge-good-1", Payload: []byte("WORKUNIT-42-RESULT=0xDEADBEEF")},
		{DeviceID: "edge-bad", Payload: []byte("WORKUNIT-42-RESULT=0xBADC0DE0")},
		{DeviceID: "edge-good-2", Payload: []byte("WORKUNIT-42-RESULT=0xDEADBEEF")},
	}

	// before: naive acceptance ignores the anchor entirely.
	naiveAccepted := true
	t.Logf("run=%s clause=1 before naiveAccepted=%v (wrong result from edge-bad would be accepted)", runUUID, naiveAccepted)

	rep := Verify(trusted, peers)

	t.Logf("run=%s clause=1 after accepted=%v mismatched=%v", runUUID, rep.Accepted, rep.Mismatched)

	if rep.Accepted {
		t.Fatalf("run=%s expected verdict REJECTED (a peer disagreed with the trusted anchor), got Accepted=true", runUUID)
	}

	if len(rep.Mismatched) != 1 || rep.Mismatched[0] != "edge-bad" {
		t.Fatalf("run=%s expected exactly [edge-bad] flagged, got %v", runUUID, rep.Mismatched)
	}

	// The flagged device must be marked not-OK; matching devices must be OK.
	bad := findVerdict(rep, "edge-bad")
	if bad == nil || bad.OK {
		t.Fatalf("run=%s edge-bad must be flagged not-OK, got %+v", runUUID, bad)
	}
	if bad.Checksum == bad.TrustedChecksum {
		t.Fatalf("run=%s edge-bad checksum must differ from trusted checksum, both=%s", runUUID, bad.Checksum)
	}

	for _, id := range []string{"edge-good-1", "edge-good-2"} {
		v := findVerdict(rep, id)
		if v == nil || !v.OK {
			t.Fatalf("run=%s matching device %s must be OK, got %+v", runUUID, id, v)
		}
		if v.Checksum != rep.TrustedChecksum {
			t.Fatalf("run=%s matching device %s checksum %s must equal trusted %s", runUUID, id, v.Checksum, rep.TrustedChecksum)
		}
	}
}

// TestVerify_AllAgreeAccepted covers CLOSURE clause 2.
//
// When every peer matches the trusted result, all peers are OK and the overall
// verdict is accepted.
//
// Mutation: if Verify wrongly flagged matching peers (e.g. inverted the OK
// comparison), Accepted would be false and Mismatched non-empty — this test
// FAILS under that mutation.
func TestVerify_AllAgreeAccepted(t *testing.T) {
	t.Logf("run=%s clause=2 start", runUUID)

	payload := []byte("CONSENSUS-PAYLOAD-IDENTICAL-ACROSS-EDGES")
	trusted := Result{DeviceID: "edge-trusted", Payload: payload}
	peers := []Result{
		{DeviceID: "edge-a", Payload: append([]byte(nil), payload...)},
		{DeviceID: "edge-b", Payload: append([]byte(nil), payload...)},
		{DeviceID: "edge-c", Payload: append([]byte(nil), payload...)},
	}

	t.Logf("run=%s clause=2 before peers=%d", runUUID, len(peers))

	rep := Verify(trusted, peers)

	t.Logf("run=%s clause=2 after accepted=%v mismatched=%v", runUUID, rep.Accepted, rep.Mismatched)

	if !rep.Accepted {
		t.Fatalf("run=%s expected Accepted=true when all peers agree, mismatched=%v", runUUID, rep.Mismatched)
	}
	if len(rep.Mismatched) != 0 {
		t.Fatalf("run=%s expected no mismatched devices, got %v", runUUID, rep.Mismatched)
	}
	if len(rep.Peers) != 3 {
		t.Fatalf("run=%s expected 3 peer verdicts, got %d", runUUID, len(rep.Peers))
	}
	for _, v := range rep.Peers {
		if !v.OK {
			t.Fatalf("run=%s peer %s must be OK, got %+v", runUUID, v.DeviceID, v)
		}
	}
}

// TestVerify_ChecksumSensitivity covers CLOSURE clause 3.
//
// Two payloads of IDENTICAL LENGTH differ in a SINGLE byte. A content checksum
// must detect this; a length-only comparison would not. The differing peer must
// be flagged and the verdict rejected.
//
// Mutation: if Verify compared payload LENGTHS instead of content checksums,
// the two equal-length payloads would be judged equal, the peer would be OK,
// and Accepted would be true — this test FAILS under that mutation.
func TestVerify_ChecksumSensitivity(t *testing.T) {
	t.Logf("run=%s clause=3 start", runUUID)

	base := []byte("0123456789ABCDEF0123456789ABCDEF")        // 32 bytes
	altered := append([]byte(nil), base...)
	altered[17] ^= 0x01 // flip one bit in one byte; length unchanged

	if len(base) != len(altered) {
		t.Fatalf("run=%s test setup invalid: payload lengths must be equal (%d vs %d)", runUUID, len(base), len(altered))
	}

	trusted := Result{DeviceID: "edge-trusted", Payload: base}
	peers := []Result{
		{DeviceID: "edge-flipped", Payload: altered},
	}

	t.Logf("run=%s clause=3 before lenTrusted=%d lenPeer=%d (length-only would judge EQUAL)", runUUID, len(base), len(altered))

	rep := Verify(trusted, peers)

	v := findVerdict(rep, "edge-flipped")
	t.Logf("run=%s clause=3 after accepted=%v peerOK=%v peerSum=%s trustedSum=%s", runUUID, rep.Accepted, v.OK, v.Checksum, rep.TrustedChecksum)

	if rep.Accepted {
		t.Fatalf("run=%s single-byte difference must be detected: expected REJECTED, got Accepted=true", runUUID)
	}
	if v == nil || v.OK {
		t.Fatalf("run=%s edge-flipped must be flagged not-OK (content checksum), got %+v", runUUID, v)
	}
	if v.Checksum == rep.TrustedChecksum {
		t.Fatalf("run=%s checksums must differ for a one-byte change, both=%s", runUUID, v.Checksum)
	}
	if len(rep.Mismatched) != 1 || rep.Mismatched[0] != "edge-flipped" {
		t.Fatalf("run=%s expected [edge-flipped] mismatched, got %v", runUUID, rep.Mismatched)
	}
}

// TestChecksum_StableAndContentDependent guards the verification primitive:
// the checksum is deterministic for identical bytes and differs for different
// bytes. Mutation: a constant/length-based Checksum would make sumDiff equal
// sumA, failing the inequality assertion.
func TestChecksum_StableAndContentDependent(t *testing.T) {
	t.Logf("run=%s clause=primitive start", runUUID)

	a1 := Checksum([]byte("payload-X"))
	a2 := Checksum([]byte("payload-X"))
	diff := Checksum([]byte("payload-Y"))

	t.Logf("run=%s clause=primitive a1=%s a2=%s diff=%s", runUUID, a1, a2, diff)

	if a1 != a2 {
		t.Fatalf("run=%s checksum must be deterministic for identical input: %s != %s", runUUID, a1, a2)
	}
	if a1 == diff {
		t.Fatalf("run=%s checksum must depend on content: %q and %q produced same sum %s", runUUID, "payload-X", "payload-Y", a1)
	}
}
