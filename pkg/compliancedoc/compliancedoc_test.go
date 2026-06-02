package compliancedoc

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// baseMeta is shared model metadata for the tests.
func baseMeta() ModelMetadata {
	return ModelMetadata{
		Name:        "helix-vision-7b",
		Version:     "1.4.2",
		Provider:    "HelixDevelopment",
		IntendedUse: "high-risk medical triage assistance",
	}
}

// TestGenerate_EmptyLogs proves empty logs -> ErrNoAttestations.
// before->after delta: no attestation evidence -> refusal to emit a card.
func TestGenerate_EmptyLogs(t *testing.T) {
	const runUUID = "run-9f2c1a7e-EMPTY-LOGS-0001"
	t.Logf("%s: before=no records / after=ErrNoAttestations (no doc generated)", runUUID)

	card, err := Generate(baseMeta(), nil, time.Unix(1700000000, 0).UTC())
	if !errors.Is(err, ErrNoAttestations) {
		t.Fatalf("%s: expected ErrNoAttestations, got err=%v", runUUID, err)
	}
	if card.Document != "" {
		t.Fatalf("%s: expected empty card on error, got document of len %d", runUUID, len(card.Document))
	}
}

// TestGenerate_ProvenanceBindsLogs is the LOAD-BEARING GUARD.
// Two log sets differ only in one record's ModelHash; their ProvenanceHash
// values MUST differ. If Generate is mutated to hash a constant / ignore the
// logs, this test fails.
// before->after delta: change one record's ModelHash -> provenance hash changes.
func TestGenerate_ProvenanceBindsLogs(t *testing.T) {
	const runUUID = "run-9f2c1a7e-PROV-BINDS-0002"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	logsA := []AttestationRecord{
		{Timestamp: ts, GPUUUID: "GPU-AAA", ModelHash: "hash-original", Passed: true},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-BBB", ModelHash: "hash-second", Passed: false},
	}
	// Identical except record[0].ModelHash.
	logsB := []AttestationRecord{
		{Timestamp: ts, GPUUUID: "GPU-AAA", ModelHash: "hash-MUTATED", Passed: true},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-BBB", ModelHash: "hash-second", Passed: false},
	}

	cardA, err := Generate(baseMeta(), logsA, now)
	if err != nil {
		t.Fatalf("%s: unexpected error A: %v", runUUID, err)
	}
	cardB, err := Generate(baseMeta(), logsB, now)
	if err != nil {
		t.Fatalf("%s: unexpected error B: %v", runUUID, err)
	}

	if cardA.ProvenanceHash == "" || len(cardA.ProvenanceHash) != 64 {
		t.Fatalf("%s: expected 64-hex-char provenance, got %q", runUUID, cardA.ProvenanceHash)
	}
	if cardA.ProvenanceHash == cardB.ProvenanceHash {
		t.Fatalf("%s: provenance did NOT change when ModelHash changed: A=%s B=%s (provenance ignores log content)",
			runUUID, cardA.ProvenanceHash, cardB.ProvenanceHash)
	}
	t.Logf("%s: before=%s after=%s (one ModelHash flip rebinds provenance)", runUUID, cardA.ProvenanceHash, cardB.ProvenanceHash)
}

// TestGenerate_ProvenanceOrderIndependent asserts that reordering the same
// records yields the same provenance (canonical sort), while still being
// content-bound (covered by the guard above). This pins the sort behavior.
func TestGenerate_ProvenanceOrderIndependent(t *testing.T) {
	const runUUID = "run-9f2c1a7e-PROV-ORDER-0003"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	r1 := AttestationRecord{Timestamp: ts, GPUUUID: "GPU-AAA", ModelHash: "h1", Passed: true}
	r2 := AttestationRecord{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-BBB", ModelHash: "h2", Passed: false}

	cardForward, err := Generate(baseMeta(), []AttestationRecord{r1, r2}, now)
	if err != nil {
		t.Fatalf("%s: err forward: %v", runUUID, err)
	}
	cardReverse, err := Generate(baseMeta(), []AttestationRecord{r2, r1}, now)
	if err != nil {
		t.Fatalf("%s: err reverse: %v", runUUID, err)
	}
	if cardForward.ProvenanceHash != cardReverse.ProvenanceHash {
		t.Fatalf("%s: provenance changed under reorder: %s != %s", runUUID, cardForward.ProvenanceHash, cardReverse.ProvenanceHash)
	}
	t.Logf("%s: before=[r1,r2] after=[r2,r1] both -> %s (order-independent)", runUUID, cardForward.ProvenanceHash)
}

// TestGenerate_DocumentContainsEvidence asserts the Document contains the GPU
// UUIDs, the passed count, the model identity, and the provenance hash.
// before->after delta: structured inputs -> rendered model card containing them.
func TestGenerate_DocumentContainsEvidence(t *testing.T) {
	const runUUID = "run-9f2c1a7e-DOC-EVIDENCE-0004"
	now := time.Unix(1700000000, 0).UTC()
	ts := time.Unix(1699990000, 0).UTC()

	logs := []AttestationRecord{
		{Timestamp: ts, GPUUUID: "GPU-AAA", ModelHash: "h1", Passed: true},
		{Timestamp: ts.Add(time.Minute), GPUUUID: "GPU-BBB", ModelHash: "h2", Passed: true},
		{Timestamp: ts.Add(2 * time.Minute), GPUUUID: "GPU-CCC", ModelHash: "h3", Passed: false},
	}
	meta := baseMeta()

	card, err := Generate(meta, logs, now)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", runUUID, err)
	}

	if card.AttestationCount != 3 {
		t.Fatalf("%s: AttestationCount=%d want 3", runUUID, card.AttestationCount)
	}
	if card.PassedCount != 2 {
		t.Fatalf("%s: PassedCount=%d want 2", runUUID, card.PassedCount)
	}

	doc := card.Document
	mustContain := []string{
		meta.Name,
		meta.Version,
		meta.Provider,
		"GPU-AAA", "GPU-BBB", "GPU-CCC",
		"Passed count: 2",
		card.ProvenanceHash,
	}
	for _, want := range mustContain {
		if want == "" {
			t.Fatalf("%s: empty expectation token (provenance hash unset?)", runUUID)
		}
		if !strings.Contains(doc, want) {
			t.Fatalf("%s: document missing %q\n---DOC---\n%s", runUUID, want, doc)
		}
	}
	t.Logf("%s: before=3 records(2 passed,3 gpus) after=card embeds gpus+passed=2+provenance=%s", runUUID, card.ProvenanceHash)
}
