package compliancedoc

// Adversarial probes for pkg/compliancedoc. These test the SINK SIDE:
// the actual rendered model-card bytes, the numeric pass/fail counts the card
// reports, the provenance digest, and determinism across many runs (incl. the
// map-backed GPU dedup path). They are CONTRACT-PINNING + LATENT-RISK probes.
//
// Documented contract (from compliancedoc.go package doc + Generate doc):
//   - Generate turns attestation records + model metadata into a "deterministic,
//     provenance-bound model card document".
//   - ProvenanceHash is SHA-256 over a canonical (sorted) serialization of the
//     records; any field change rebinds it; record order does not matter.
//   - Empty logs -> ErrNoAttestations (a card cannot be produced w/o evidence).
//
// What the contract does NOT promise (verified by reading the source): there is
// NO compliance verdict field on ModelCard and the rendered document never
// emits "COMPLIANT"/"NON-COMPLIANT". Generate emits a full card even when every
// attestation FAILED (PassedCount==0). That is a LATENT RISK for a document the
// package doc calls "EU AI Act compliance documentation" — a downstream reader
// could treat the mere existence of the card as a pass. We pin the *honest*
// current behavior (card still rendered, failed-count surfaced correctly) so a
// future regression that hid the failure signal would be caught, and we
// document the gap rather than asserting a verdict the code never claimed.

import (
	"strings"
	"testing"
	"time"
)

// adversBaseMeta is local metadata to avoid coupling to the sibling test file.
func adversBaseMeta() ModelMetadata {
	return ModelMetadata{
		Name:        "helix-adversarial-probe",
		Version:     "9.9.9",
		Provider:    "HelixDevelopment",
		IntendedUse: "high-risk compliance evidence ledger",
	}
}

// TestAdversarial_AllFailedStillRendersCard_NoVerdict is the LATENT-RISK pin.
// SINK: every attestation Passed=false. We assert the actual card bytes:
//   - the card IS still produced (no error) even though zero controls passed;
//   - PassedCount==0 and the document says "Passed count: 0" / "Failed count: N";
//   - the document does NOT contain any "NON-COMPLIANT"/"FAIL"-verdict string,
//     proving there is no fail-open *verdict* (there is no verdict at all).
//
// This is the closest honest analogue to a fail-open compliance probe: a
// compliance deliverable is emitted over wholly-failing evidence with no verdict
// surface. We pin it; if a verdict is ever added, this test must be revisited.
func TestAdversarial_AllFailedStillRendersCard_NoVerdict(t *testing.T) {
	const runUUID = "run-adv-ALLFAIL-NOVERDICT-0001"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	logs := []AttestationRecord{
		{Timestamp: ts, GPUUUID: "GPU-FAIL-1", ModelHash: "h1", Passed: false},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-FAIL-2", ModelHash: "h2", Passed: false},
		{Timestamp: ts.Add(2 * time.Minute), GPUUUID: "GPU-FAIL-3", ModelHash: "h3", Passed: false},
	}

	card, err := Generate(adversBaseMeta(), logs, now)
	if err != nil {
		t.Fatalf("%s: unexpected error over all-failed evidence: %v", runUUID, err)
	}

	// Sink-side numeric: zero passed, all failed.
	if card.PassedCount != 0 {
		t.Fatalf("%s: PassedCount=%d want 0 (all attestations failed)", runUUID, card.PassedCount)
	}
	if card.AttestationCount != 3 {
		t.Fatalf("%s: AttestationCount=%d want 3", runUUID, card.AttestationCount)
	}

	doc := card.Document
	if !strings.Contains(doc, "Passed count: 0") {
		t.Fatalf("%s: document must surface 'Passed count: 0'\n---DOC---\n%s", runUUID, doc)
	}
	if !strings.Contains(doc, "Failed count: 3") {
		t.Fatalf("%s: document must surface 'Failed count: 3'\n---DOC---\n%s", runUUID, doc)
	}

	// LATENT-RISK assertion: there is provably NO verdict surface. The doc must
	// not claim compliance, and (the gap) it also emits no explicit failure
	// verdict. We assert the absence of a positive-compliance claim so a future
	// change that fabricates "COMPLIANT" over failing evidence (a true
	// PASS-bluff) would bite here.
	for _, banned := range []string{"COMPLIANT", "Compliant: true", "VERDICT: PASS", "status: compliant"} {
		if strings.Contains(doc, banned) {
			t.Fatalf("%s: FAIL-OPEN: all-failed evidence produced a positive-compliance claim %q in doc:\n%s",
				runUUID, banned, doc)
		}
	}
	t.Logf("%s: all-failed evidence -> card rendered, PassedCount=0, Failed count: 3 surfaced, no COMPLIANT verdict (LATENT RISK: no verdict field at all)", runUUID)
}

// TestAdversarial_FailedCountBoundary pins the count-passed arithmetic at the
// numeric boundaries: all-passed (failed==0), all-failed (failed==count), and
// the single-record edges. SINK: the rendered "Failed count: N" line.
func TestAdversarial_FailedCountBoundary(t *testing.T) {
	const runUUID = "run-adv-FAILED-BOUNDARY-0002"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	cases := []struct {
		name       string
		passedFlag []bool
		wantPassed int
		wantFailed int
	}{
		{"all-passed-1", []bool{true}, 1, 0},
		{"all-failed-1", []bool{false}, 0, 1},
		{"all-passed-3", []bool{true, true, true}, 3, 0},
		{"all-failed-3", []bool{false, false, false}, 0, 3},
		{"mixed-1of3", []bool{true, false, false}, 1, 2},
	}

	for _, tc := range cases {
		logs := make([]AttestationRecord, len(tc.passedFlag))
		for i, p := range tc.passedFlag {
			logs[i] = AttestationRecord{
				Timestamp: ts.Add(time.Duration(i) * time.Minute),
				GPUUUID:   "GPU-" + tc.name + "-" + string(rune('A'+i)),
				ModelHash: "h",
				Passed:    p,
			}
		}
		card, err := Generate(adversBaseMeta(), logs, now)
		if err != nil {
			t.Fatalf("%s/%s: unexpected error: %v", runUUID, tc.name, err)
		}
		if card.PassedCount != tc.wantPassed {
			t.Fatalf("%s/%s: PassedCount=%d want %d", runUUID, tc.name, card.PassedCount, tc.wantPassed)
		}
		wantFailedLine := "Failed count: " + itoa(tc.wantFailed)
		if !strings.Contains(card.Document, wantFailedLine) {
			t.Fatalf("%s/%s: doc missing %q\n---DOC---\n%s", runUUID, tc.name, wantFailedLine, card.Document)
		}
	}
	t.Logf("%s: count-passed arithmetic correct at all-passed/all-failed/mixed boundaries", runUUID)
}

// TestAdversarial_DeterministicBytes is the DETERMINISM probe. The card render
// dedups GPUs through a Go map (distinctGPUUUIDs) before sorting. A naive
// implementation that ranged the map for output would be nondeterministic.
// SINK: assert the full Document bytes AND the ProvenanceHash are byte-identical
// across many runs with a deliberately unsorted, duplicate-GPU input.
func TestAdversarial_DeterministicBytes(t *testing.T) {
	const runUUID = "run-adv-DETERMINISM-0003"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	// Unsorted GPU UUIDs + duplicates to exercise the map dedup + sort path.
	logs := []AttestationRecord{
		{Timestamp: ts.Add(3 * time.Minute), GPUUUID: "GPU-ZZZ", ModelHash: "h4", Passed: false},
		{Timestamp: ts, GPUUUID: "GPU-MMM", ModelHash: "h1", Passed: true},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-AAA", ModelHash: "h2", Passed: true},
		{Timestamp: ts.Add(2 * time.Minute), GPUUUID: "GPU-MMM", ModelHash: "h3", Passed: false}, // dup GPU
		{Timestamp: ts.Add(4 * time.Minute), GPUUUID: "GPU-AAA", ModelHash: "h5", Passed: true},  // dup GPU
	}

	first, err := Generate(adversBaseMeta(), logs, now)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", runUUID, err)
	}
	const iters = 200
	for i := 0; i < iters; i++ {
		got, err := Generate(adversBaseMeta(), logs, now)
		if err != nil {
			t.Fatalf("%s: iter %d error: %v", runUUID, i, err)
		}
		if got.Document != first.Document {
			t.Fatalf("%s: NONDETERMINISTIC document at iter %d\n---FIRST---\n%s\n---GOT---\n%s",
				runUUID, i, first.Document, got.Document)
		}
		if got.ProvenanceHash != first.ProvenanceHash {
			t.Fatalf("%s: NONDETERMINISTIC provenance at iter %d: %s != %s",
				runUUID, i, first.ProvenanceHash, got.ProvenanceHash)
		}
	}

	// Sink-side: distinct GPUs appear once each, in sorted order, in the doc.
	gpuSection := first.Document
	idxAAA := strings.Index(gpuSection, "- GPU-AAA")
	idxMMM := strings.Index(gpuSection, "- GPU-MMM")
	idxZZZ := strings.Index(gpuSection, "- GPU-ZZZ")
	if idxAAA < 0 || idxMMM < 0 || idxZZZ < 0 {
		t.Fatalf("%s: a distinct GPU is missing from the doc:\n%s", runUUID, first.Document)
	}
	if !(idxAAA < idxMMM && idxMMM < idxZZZ) {
		t.Fatalf("%s: GPUs not in sorted order in doc (AAA=%d MMM=%d ZZZ=%d):\n%s",
			runUUID, idxAAA, idxMMM, idxZZZ, first.Document)
	}
	if strings.Count(gpuSection, "- GPU-MMM\n") != 1 {
		t.Fatalf("%s: duplicate GPU-MMM not deduped to a single bullet:\n%s", runUUID, first.Document)
	}
	t.Logf("%s: %d runs byte-identical; doc=%dB hash=%s; GPUs deduped+sorted", runUUID, iters, len(first.Document), first.ProvenanceHash)
}

// TestAdversarial_EmptyGPUUUIDAccepted pins a VALIDATION gap (LATENT RISK):
// a record with an empty/whitespace GPUUUID is accepted and rendered as an
// empty bullet, and contributes to the provenance hash. No malformed-input
// rejection exists. We assert the *current* behavior so the gap is visible and
// a future tightening is a deliberate, test-driven change.
func TestAdversarial_EmptyGPUUUIDAccepted(t *testing.T) {
	const runUUID = "run-adv-EMPTY-GPU-0004"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	logs := []AttestationRecord{
		{Timestamp: ts, GPUUUID: "", ModelHash: "h1", Passed: true},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-REAL", ModelHash: "h2", Passed: true},
	}
	card, err := Generate(adversBaseMeta(), logs, now)
	if err != nil {
		t.Fatalf("%s: empty GPUUUID unexpectedly rejected: %v", runUUID, err)
	}
	if card.AttestationCount != 2 {
		t.Fatalf("%s: AttestationCount=%d want 2 (empty-UUID record counted)", runUUID, card.AttestationCount)
	}
	// The empty UUID renders as a bare "- \n" bullet under Attesting GPUs.
	if !strings.Contains(card.Document, "## Attesting GPUs\n\n- \n") {
		t.Logf("%s: doc=\n%s", runUUID, card.Document)
		t.Fatalf("%s: expected empty-UUID bare bullet '- ' first (sorted), behavior changed", runUUID)
	}
	t.Logf("%s: LATENT RISK pinned: empty GPUUUID accepted, counted, rendered as bare bullet, hash-bound", runUUID)
}

// itoa is a tiny dependency-free int->string for boundary expectations.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
