package verifier_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/console"
	"github.com/HelixDevelopment/helix_cluster/internal/verifier"
)

// trustedSquare is the canonical executor: doubles every byte value.
// It is deterministic — same input always yields same output.
func trustedSquare(job verifier.Job) ([]byte, error) {
	out := make([]byte, len(job.Input))
	for i, b := range job.Input {
		out[i] = b * 2
	}
	return out, nil
}

// TestSEMI_CorrectResult_IsAccepted proves that when a SEMI node returns the
// same result as the trusted recomputation, VerifyResult returns nil.
//
// Sink-side fact: correct result produces NO error and NO mismatch record.
func TestSEMI_CorrectResult_IsAccepted(t *testing.T) {
	var logged []verifier.MismatchRecord
	v := verifier.New(trustedSquare, verifier.WithMismatchLogger(func(r verifier.MismatchRecord) {
		logged = append(logged, r)
	}))

	input := []byte{10, 20, 30}
	// Correct result: same as trustedSquare produces.
	correct := []byte{20, 40, 60}

	job := verifier.Job{ID: "job-accept", Input: input}
	err := v.VerifyResult(job, correct, console.TrustSemi, "node-correct")
	if err != nil {
		t.Fatalf("correct SEMI result must be accepted, got err: %v", err)
	}
	if len(logged) != 0 {
		t.Fatalf("no mismatch record expected for accepted result, got %d records", len(logged))
	}
}

// TestSEMI_TamperedResult_IsRejected is the primary sink-side proof for HXC-1157:
// a deliberately tampered result from a SEMI node MUST be rejected with:
//  1. An error wrapping ErrMismatch (errors.Is-checkable)
//  2. A logged MismatchRecord carrying a non-empty RunID (per-call UUID)
//  3. Differing SemiHash and TrustedHash fields
//
// §1.1 mutation pairing (run separately, documented in mutation_proof):
// Skipping the checksum comparison causes VerifyResult to return nil even for
// the tampered result — this test FAILS with "tampered result must be rejected".
func TestSEMI_TamperedResult_IsRejected(t *testing.T) {
	var logged []verifier.MismatchRecord
	v := verifier.New(trustedSquare, verifier.WithMismatchLogger(func(r verifier.MismatchRecord) {
		logged = append(logged, r)
	}))

	input := []byte{10, 20, 30}
	// Tampered: wrong values — NOT what trustedSquare would produce.
	tampered := []byte{99, 99, 99}

	job := verifier.Job{ID: "job-tamper", Input: input}
	err := v.VerifyResult(job, tampered, console.TrustSemi, "node-evil")

	// 1. Must return an error wrapping ErrMismatch.
	if err == nil {
		t.Fatal("tampered SEMI result must be rejected, got nil error")
	}
	if !errors.Is(err, verifier.ErrMismatch) {
		t.Fatalf("error must wrap ErrMismatch, got: %v", err)
	}

	// 2. Exactly one mismatch record must have been logged.
	if len(logged) != 1 {
		t.Fatalf("expected 1 mismatch record, got %d", len(logged))
	}
	rec := logged[0]

	// 3. RunID (UUID) must be non-empty (per-call, not replayed).
	if rec.RunID == "" {
		t.Fatal("MismatchRecord.RunID must be non-empty — UUID not generated")
	}
	if len(rec.RunID) < 32 {
		t.Fatalf("RunID %q too short to be a real UUID hex", rec.RunID)
	}

	// 4. JobID and NodeID must match.
	if rec.JobID != "job-tamper" {
		t.Errorf("MismatchRecord.JobID = %q, want %q", rec.JobID, "job-tamper")
	}
	if rec.NodeID != "node-evil" {
		t.Errorf("MismatchRecord.NodeID = %q, want %q", rec.NodeID, "node-evil")
	}

	// 5. Checksums must differ (tampered != trusted).
	if rec.SemiHash == rec.TrustedHash {
		t.Fatalf("SemiHash==TrustedHash %q — mismatch detection is broken", rec.SemiHash)
	}
	if rec.SemiHash == "" || rec.TrustedHash == "" {
		t.Fatal("SemiHash or TrustedHash is empty — not computed")
	}

	// 6. TrustLevel in the record must be "SEMI".
	if rec.TrustLevel != "SEMI" {
		t.Errorf("MismatchRecord.TrustLevel = %q, want SEMI", rec.TrustLevel)
	}

	// 7. The RunID must also appear in the error message (caller-visible UUID).
	if !strings.Contains(err.Error(), rec.RunID) {
		t.Errorf("error message %q must contain RunID %q", err.Error(), rec.RunID)
	}
}

// TestFULL_BypassesVerification proves FULL-trust nodes never trigger the
// trusted executor (no recompute, no mismatch, regardless of result content).
func TestFULL_BypassesVerification(t *testing.T) {
	executorCalled := false
	executor := func(job verifier.Job) ([]byte, error) {
		executorCalled = true
		return trustedSquare(job)
	}
	var logged []verifier.MismatchRecord
	v := verifier.New(executor, verifier.WithMismatchLogger(func(r verifier.MismatchRecord) {
		logged = append(logged, r)
	}))

	// Even a "wrong" result is accepted for FULL-trust nodes.
	job := verifier.Job{ID: "job-full", Input: []byte{1, 2, 3}}
	err := v.VerifyResult(job, []byte{0xFF, 0xFF, 0xFF}, console.TrustFull, "node-full")
	if err != nil {
		t.Fatalf("FULL trust must bypass verification, got: %v", err)
	}
	if executorCalled {
		t.Fatal("trusted executor must NOT be called for FULL-trust nodes")
	}
	if len(logged) != 0 {
		t.Fatal("no mismatch log expected for FULL-trust bypass")
	}
}

// TestUNTRUSTED_AlwaysRejected proves UNTRUSTED nodes are always rejected,
// even if the result is byte-correct.
func TestUNTRUSTED_AlwaysRejected(t *testing.T) {
	var logged []verifier.MismatchRecord
	v := verifier.New(trustedSquare, verifier.WithMismatchLogger(func(r verifier.MismatchRecord) {
		logged = append(logged, r)
	}))

	// Even a "correct" result is rejected for UNTRUSTED nodes.
	correct := []byte{20, 40, 60}
	job := verifier.Job{ID: "job-untrusted", Input: []byte{10, 20, 30}}
	err := v.VerifyResult(job, correct, console.TrustUntrusted, "node-bad")
	if err == nil {
		t.Fatal("UNTRUSTED node result must always be rejected")
	}
	if !errors.Is(err, verifier.ErrMismatch) {
		t.Fatalf("error must wrap ErrMismatch for UNTRUSTED, got: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("expected 1 mismatch log for UNTRUSTED, got %d", len(logged))
	}
}

// TestSEMI_MultipleJobs_UniqueRunIDs proves each VerifyResult call for a
// rejected result produces a DISTINCT RunID (UUIDs must not repeat).
func TestSEMI_MultipleJobs_UniqueRunIDs(t *testing.T) {
	var logged []verifier.MismatchRecord
	v := verifier.New(trustedSquare, verifier.WithMismatchLogger(func(r verifier.MismatchRecord) {
		logged = append(logged, r)
	}))

	for i := 0; i < 3; i++ {
		job := verifier.Job{ID: "job-multi", Input: []byte{byte(i)}}
		_ = v.VerifyResult(job, []byte{99}, console.TrustSemi, "node-x")
	}

	if len(logged) != 3 {
		t.Fatalf("expected 3 mismatch records, got %d", len(logged))
	}
	// All RunIDs must be distinct.
	seen := make(map[string]bool)
	for _, r := range logged {
		if seen[r.RunID] {
			t.Fatalf("duplicate RunID %q — UUIDs not unique per rejection", r.RunID)
		}
		seen[r.RunID] = true
	}
}

// TestSEMI_CorrectResult_NoLoggedRecord proves zero mismatch records are
// emitted when the SEMI result is correct — the logger is only triggered on
// actual mismatches.
func TestSEMI_CorrectResult_NoLoggedRecord(t *testing.T) {
	var count int
	v := verifier.New(trustedSquare, verifier.WithMismatchLogger(func(_ verifier.MismatchRecord) {
		count++
	}))

	input := []byte{5, 10, 15}
	correct := []byte{10, 20, 30} // trustedSquare(input)
	job := verifier.Job{ID: "job-ok", Input: input}

	if err := v.VerifyResult(job, correct, console.TrustSemi, "node-good"); err != nil {
		t.Fatalf("correct result must be accepted: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 mismatch records for correct result, got %d", count)
	}
}
