package auditproof

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"testing"
)

// runUUID derives a deterministic per-test run identifier from the test name
// and a salt. It avoids any time/random source so the test stays pure and
// reproducible, while still giving each run a distinct logged tag.
func runUUID(t *testing.T, salt string) string {
	t.Helper()
	h := sha256.Sum256([]byte(t.Name() + "|" + salt))
	return fmt.Sprintf("run-%x", h[:8])
}

// sampleReport builds a report whose incentives are non-trivially derived
// from the compute-unit data (each is units*incentiveRatePerUnit).
func sampleReport() Report {
	return Report{ComputeUnits: map[string]float64{
		"node-a": 10,
		"node-b": 4,
		"node-c": 7.5,
	}}
}

// TestClause1_CommitThenProveMatches covers CLOSURE clause 1: generate a
// report + commitment, then an INDEPENDENT Verify recomputes incentives and
// confirms the commitment MATCHES. Incentives are checked to be derived from
// the data (not hardcoded).
//
// Mutation: if ComputeIncentives ignored the data (e.g. returned a constant),
// the expected incentives below — derived from units*rate — would not match,
// failing this test. If Verify always returned true, the explicit false-path
// assertion against a wrong commitment below would fail.
func TestClause1_CommitThenProveMatches(t *testing.T) {
	id := runUUID(t, "clause1")
	data := sampleReport()

	// Expected incentives are pinned as LITERALS, independent of the in-code
	// incentiveRatePerUnit constant. With units {10, 4, 7.5} and the audit
	// model's rate of 1.25 these MUST equal {12.5, 5, 9.375}. Writing the
	// literals (rather than units*incentiveRatePerUnit) means the rate
	// magnitude is independently verified: mutating the rate (e.g. 1.25 -> 1.5)
	// changes these products and fails this test.
	want := map[string]float64{
		"node-a": 12.5,  // 10  * 1.25
		"node-b": 5,     // 4   * 1.25
		"node-c": 9.375, // 7.5 * 1.25
	}
	got := ComputeIncentives(data)
	if len(got) != len(want) {
		t.Fatalf("%s: incentive count = %d, want %d", id, len(got), len(want))
	}
	for node, w := range want {
		if !approxEqual(got[node], w) {
			t.Fatalf("%s: incentive[%s] = %v, want %v (literal expected value)",
				id, node, got[node], w)
		}
	}
	// Sanity: incentives are NOT all the same constant (proves data-dependence).
	if approxEqual(got["node-a"], got["node-b"]) {
		t.Fatalf("%s: incentives are constant across nodes; not derived from data", id)
	}

	committed := CommitReport(data)
	t.Logf("%s before: committed=%x incentives=%v", id, committed[:8], got)

	// Independent verifier recomputes from the SAME data and confirms match.
	verified := Verify(data, committed)
	t.Logf("%s after: verified=%v", id, verified)
	if !verified {
		t.Fatalf("%s: Verify returned false for an untampered report", id)
	}

	// A deliberately wrong commitment must NOT verify (guards always-true Verify).
	var wrong [32]byte
	copy(wrong[:], committed[:])
	wrong[0] ^= 0xFF
	if Verify(data, wrong) {
		t.Fatalf("%s: Verify accepted a wrong commitment", id)
	}
}

// TestGoldenCommitment pins CommitReport(sampleReport()) to a fixed,
// independently computed [32]byte. This is a cross-check that does NOT use any
// in-code constant: the bytes were derived from the audit model's defining
// rate (1.25) applied to the sample units {10, 4, 7.5} -> incentives
// {12.5, 5, 9.375} and the documented canonical encoding. Any drift in the
// rate magnitude (e.g. 1.25 -> 1.5), the incentive formula, the canonical
// serialization, the float format, or the hash function changes this digest
// and fails the test. It therefore catches mutations that a rate-parameterized
// expectation would silently absorb.
//
// Mutation: changing incentiveRatePerUnit from 1.25 to any other value, or
// altering canonicalSerialize/canonicalFloat/Commit, breaks this golden hash.
func TestGoldenCommitment(t *testing.T) {
	id := runUUID(t, "golden")
	want := [32]byte{
		0x19, 0x24, 0xa6, 0xe9, 0xce, 0xda, 0x6e, 0x89,
		0x95, 0xa4, 0xf5, 0xbf, 0x21, 0xd8, 0x05, 0xf0,
		0x5c, 0xf0, 0xe1, 0x2d, 0x9a, 0xd0, 0x45, 0xf0,
		0xc0, 0x96, 0xd3, 0x6d, 0xa2, 0xcd, 0x3c, 0xd5,
	}
	got := CommitReport(sampleReport())
	t.Logf("%s before: want_golden=%x", id, want[:8])
	t.Logf("%s after: got_commitment=%x equal=%v", id, got[:8], got == want)
	if got != want {
		t.Fatalf("%s: golden commitment mismatch\n got=%x\nwant=%x", id, got, want)
	}
}

// TestClause2_TamperDetected covers CLOSURE clause 2: altering one node's
// compute units and verifying against the ORIGINAL commitment returns false.
//
// Mutation: a Verify that ignores the data and always returns true would NOT
// detect the tamper here and would fail this test.
func TestClause2_TamperDetected(t *testing.T) {
	id := runUUID(t, "clause2")
	data := sampleReport()
	original := CommitReport(data)
	t.Logf("%s before: original_commitment=%x node-a units=%v",
		id, original[:8], data.ComputeUnits["node-a"])

	// Verify untampered passes first (establishes the baseline is real).
	if !Verify(data, original) {
		t.Fatalf("%s: baseline untampered data failed to verify", id)
	}

	// Tamper: alter exactly one node's compute units.
	tampered := Report{ComputeUnits: map[string]float64{}}
	for k, v := range data.ComputeUnits {
		tampered.ComputeUnits[k] = v
	}
	tampered.ComputeUnits["node-a"] = data.ComputeUnits["node-a"] + 1 // change one node

	tamperedCommitment := CommitReport(tampered)
	ok := Verify(tampered, original)
	t.Logf("%s after: tampered_commitment=%x verify_against_original=%v",
		id, tamperedCommitment[:8], ok)

	if ok {
		t.Fatalf("%s: tamper NOT detected — Verify accepted altered data against original commitment", id)
	}
	if tamperedCommitment == original {
		t.Fatalf("%s: tampered data produced identical commitment; serialization not data-sensitive", id)
	}
}

// TestClause3_Determinism covers CLOSURE clause 3: the same data always
// yields the same commitment.
//
// Mutation: a non-canonical (map-order-dependent) serialization would make
// the commitment depend on iteration order, so independently constructed maps
// with the same contents — and repeated commits — could differ, failing this
// test.
func TestClause3_Determinism(t *testing.T) {
	id := runUUID(t, "clause3")
	data := sampleReport()

	first := CommitReport(data)
	t.Logf("%s before: first_commitment=%x", id, first[:8])

	// Recommit many times from the same data.
	for i := 0; i < 64; i++ {
		again := CommitReport(data)
		if again != first {
			t.Fatalf("%s: commitment not deterministic on iter %d: %x != %x",
				id, i, again[:8], first[:8])
		}
	}

	// Build an equivalent report with a DIFFERENT insertion order and confirm
	// the commitment is identical — this catches map-order-dependent encoding.
	reordered := Report{ComputeUnits: map[string]float64{}}
	keys := []string{"node-c", "node-a", "node-b"}
	for _, k := range keys {
		reordered.ComputeUnits[k] = data.ComputeUnits[k]
	}
	reorderedCommitment := CommitReport(reordered)
	t.Logf("%s after: reordered_commitment=%x stable=%v",
		id, reorderedCommitment[:8], reorderedCommitment == first)
	if reorderedCommitment != first {
		t.Fatalf("%s: commitment depends on map insertion order: %x != %x",
			id, reorderedCommitment[:8], first[:8])
	}
}

// TestCanonicalSerialize_Injective guards against collisions in the canonical
// serialization: two maps whose naive concatenation could collide must
// produce different bytes thanks to length-prefixing/separators.
//
// Mutation: removing the length prefix or separators would let these two
// distinct maps serialize to the same bytes, failing this test.
func TestCanonicalSerialize_Injective(t *testing.T) {
	id := runUUID(t, "injective")

	// {"ab": x} vs {"a": ...} style boundary ambiguity.
	m1 := map[string]float64{"ab": 1, "c": 2}
	m2 := map[string]float64{"a": 1, "bc": 2}
	s1 := canonicalSerialize(m1)
	s2 := canonicalSerialize(m2)
	t.Logf("%s s1=%q s2=%q", id, s1, s2)
	if string(s1) == string(s2) {
		t.Fatalf("%s: distinct maps serialized identically (non-injective encoding)", id)
	}

	// Distinct float values must also distinguish.
	a := canonicalSerialize(map[string]float64{"n": 1})
	b := canonicalSerialize(map[string]float64{"n": 2})
	if string(a) == string(b) {
		t.Fatalf("%s: different incentive values produced identical bytes", id)
	}
}

// TestEmptyReport confirms an empty report commits deterministically and
// verifies, exercising the boundary case without panicking.
func TestEmptyReport(t *testing.T) {
	id := runUUID(t, "empty")
	empty := Report{}
	c1 := CommitReport(empty)
	c2 := CommitReport(Report{ComputeUnits: map[string]float64{}})
	t.Logf("%s before: empty_commitment=%x", id, c1[:8])
	if c1 != c2 {
		t.Fatalf("%s: nil and empty maps committed differently", id)
	}
	if !Verify(empty, c1) {
		t.Fatalf("%s: empty report failed to verify against its own commitment", id)
	}
	// Adding a node must change the commitment.
	withNode := Report{ComputeUnits: map[string]float64{"n": 3}}
	if CommitReport(withNode) == c1 {
		t.Fatalf("%s: adding a node did not change commitment", id)
	}
	t.Logf("%s after: nonempty_commitment=%x", id, sha256Tag(CommitReport(withNode)))
}

func sha256Tag(b [32]byte) string { return strconv.QuoteToASCII(string(b[:4])) }
