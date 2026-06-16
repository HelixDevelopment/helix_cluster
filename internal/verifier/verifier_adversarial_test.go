package verifier_test

// Adversarial sink-side probes for internal/verifier (HXC adversarial wave).
//
// Threat model: a SEMI-trust node submits hostile output. The highest-severity
// defect class is FAIL-OPEN — VerifyResult returning nil (ACCEPT) on input that
// MUST be rejected. Every assertion below checks the actual valid/invalid
// VERDICT (nil vs ErrMismatch), never merely "did not panic".
//
// Classes probed:
//   (a) FAIL-OPEN: zero-value / unknown TrustLevel; nil/empty/truncated proof.
//   (b) COMPARE:   1-bit and length-mismatch tamper must be REJECTED.
//   (c) NUMERIC:   empty-vs-empty boundary (no numeric thresholds exist here).
//   (d) RACE:      concurrent VerifyResult on a shared Verifier under -race.

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/console"
	"github.com/HelixDevelopment/helix_cluster/internal/verifier"
)

// doubler is the trusted, deterministic re-executor: out[i] = in[i] * 2.
func doubler(job verifier.Job) ([]byte, error) {
	out := make([]byte, len(job.Input))
	for i, b := range job.Input {
		out[i] = b * 2
	}
	return out, nil
}

// --- (a) FAIL-OPEN: unknown / zero-value TrustLevel must NOT be accepted ------

// TestAdv_ZeroValueTrustLevel_NotAccepted is the headline fail-open probe.
// The zero value of console.TrustLevel is "" — it is NOT in the switch's
// FULL/SEMI/UNTRUSTED arms, so it falls to default. The contract requires a
// non-nil error (fail-closed). A fail-OPEN here (return nil) would silently
// admit any result for an unconfigured/zero trust tier — a CLAUDE-1 PASS-bluff.
//
// SINK assertion: verdict must be a non-nil error (INVALID), never nil (VALID).
func TestAdv_ZeroValueTrustLevel_NotAccepted(t *testing.T) {
	v := verifier.New(doubler)

	var zero console.TrustLevel // ""
	if zero.IsValid() {
		t.Fatalf("precondition: zero TrustLevel %q must be invalid per console", zero)
	}

	job := verifier.Job{ID: "job-zero", Input: []byte{1, 2, 3}}
	// Even a byte-correct result must NOT slip through an unknown trust tier.
	correct := []byte{2, 4, 6}

	err := v.VerifyResult(job, correct, zero, "node-zero")
	if err == nil {
		t.Fatal("FAIL-OPEN: zero-value TrustLevel accepted a result (got nil) — must be rejected")
	}
}

// TestAdv_GarbageTrustLevel_NotAccepted: a fabricated/unknown trust string
// (e.g. injected via a tampered node label) must hit default and be rejected,
// not silently accepted.
func TestAdv_GarbageTrustLevel_NotAccepted(t *testing.T) {
	v := verifier.New(doubler)
	job := verifier.Job{ID: "job-garbage", Input: []byte{9}}
	for _, bogus := range []console.TrustLevel{
		"full", "Semi", "TRUSTED", "ADMIN", " FULL ", "0", "true",
	} {
		err := v.VerifyResult(job, []byte{18}, bogus, "node-g")
		if err == nil {
			t.Fatalf("FAIL-OPEN: bogus TrustLevel %q accepted a result — must be rejected", bogus)
		}
	}
}

// --- (a/b) FAIL-OPEN + COMPARE: nil / empty / truncated proofs ----------------

// TestAdv_NilResult_SemiNonEmpty_Rejected: a SEMI node submits a nil result for
// a job whose trusted output is non-empty. checksum(nil) != checksum(trusted),
// so it MUST be rejected. A fail-open (accept of missing evidence) is the worst
// case: an empty/absent proof admitted as valid.
func TestAdv_NilResult_SemiNonEmpty_Rejected(t *testing.T) {
	v := verifier.New(doubler)
	job := verifier.Job{ID: "job-nil", Input: []byte{1, 2, 3}} // trusted => {2,4,6}

	if err := v.VerifyResult(job, nil, console.TrustSemi, "node-empty"); err == nil {
		t.Fatal("FAIL-OPEN: nil SEMI result accepted for non-empty trusted output — must reject")
	}
	if err := v.VerifyResult(job, []byte{}, console.TrustSemi, "node-empty"); err == nil {
		t.Fatal("FAIL-OPEN: empty SEMI result accepted for non-empty trusted output — must reject")
	}
}

// TestAdv_TruncatedResult_Rejected: a proof truncated by one byte must be
// rejected (length mismatch changes the digest).
func TestAdv_TruncatedResult_Rejected(t *testing.T) {
	v := verifier.New(doubler)
	job := verifier.Job{ID: "job-trunc", Input: []byte{10, 20, 30, 40}} // trusted len 4
	truncated := []byte{20, 40, 60}                                     // correct prefix, 1 byte short

	err := v.VerifyResult(job, truncated, console.TrustSemi, "node-trunc")
	if err == nil {
		t.Fatal("FAIL-OPEN: length-truncated SEMI result accepted — must reject")
	}
	if !errors.Is(err, verifier.ErrMismatch) {
		t.Fatalf("truncated result must wrap ErrMismatch, got: %v", err)
	}
}

// TestAdv_OneBitFlip_Rejected: the single most important COMPARE probe — a
// one-bit difference in the result must be detected and rejected. Proves the
// comparison is not a no-op / prefix / length-only check.
func TestAdv_OneBitFlip_Rejected(t *testing.T) {
	v := verifier.New(doubler)
	input := []byte{0x10, 0x20, 0x30}
	job := verifier.Job{ID: "job-bit", Input: input}

	correct := []byte{0x20, 0x40, 0x60} // doubler(input)
	flipped := []byte{0x20, 0x40, 0x61} // last byte +1: a single-bit tamper

	// Sanity: the correct value is accepted (guards against a vacuous test).
	if err := v.VerifyResult(job, correct, console.TrustSemi, "node-ok"); err != nil {
		t.Fatalf("baseline correct result must be accepted, got: %v", err)
	}
	// Sink: the 1-bit-flipped value must be rejected.
	if err := v.VerifyResult(job, flipped, console.TrustSemi, "node-bit"); err == nil {
		t.Fatal("FAIL-OPEN: 1-bit-flipped SEMI result accepted — comparison broken")
	}
}

// --- (c) NUMERIC / boundary: empty-vs-empty -----------------------------------

// TestAdv_EmptyInput_EmptyResult_Accepted pins the boundary: when the trusted
// recomputation of an empty job is itself empty, an empty submitted result is
// genuinely CORRECT and must be accepted (checksum("")==checksum("")). This is
// the legitimate boundary, NOT a fail-open: it documents that "empty accepted"
// is only valid when the trusted side is also empty (contrast with the nil test
// above, where trusted is non-empty and the empty proof is rejected).
func TestAdv_EmptyInput_EmptyResult_Accepted(t *testing.T) {
	v := verifier.New(doubler)
	job := verifier.Job{ID: "job-empty", Input: nil} // doubler(nil) => empty
	if err := v.VerifyResult(job, []byte{}, console.TrustSemi, "node-e"); err != nil {
		t.Fatalf("empty result for empty trusted output is correct, must accept; got: %v", err)
	}
}

// --- (d) UNGUARDED SHARED STATE: concurrent verify under -race ----------------

// TestAdv_ConcurrentVerify_NoRace runs many VerifyResult calls against ONE
// shared Verifier from capped goroutines. The mismatch logger is mutex-guarded
// (the test owns that, not prod), so any race reported under -race indicts the
// production Verifier's own state. Each verdict is also asserted sink-side:
// correct results accept, tampered results reject.
func TestAdv_ConcurrentVerify_NoRace(t *testing.T) {
	var mu sync.Mutex
	var mismatches int
	v := verifier.New(doubler, verifier.WithMismatchLogger(func(_ verifier.MismatchRecord) {
		mu.Lock()
		mismatches++
		mu.Unlock()
	}))

	const workers = 8 // ANTI-HANG cap
	const perWorker = 25

	var badVerdicts int64
	var verdictMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				in := []byte{byte(w), byte(i)}
				job := verifier.Job{ID: "job-" + strconv.Itoa(w) + "-" + strconv.Itoa(i), Input: in}
				correct := []byte{byte(w) * 2, byte(i) * 2}

				// Correct result MUST be accepted.
				if err := v.VerifyResult(job, correct, console.TrustSemi, "node-c"); err != nil {
					verdictMu.Lock()
					badVerdicts++
					verdictMu.Unlock()
				}
				// Tampered result MUST be rejected.
				tampered := []byte{0xFF, 0xFF}
				if err := v.VerifyResult(job, tampered, console.TrustSemi, "node-c"); err == nil {
					verdictMu.Lock()
					badVerdicts++
					verdictMu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	if badVerdicts != 0 {
		t.Fatalf("%d incorrect verdicts under concurrency (accept-of-invalid or reject-of-valid)", badVerdicts)
	}
	if want := workers * perWorker; mismatches != want {
		t.Fatalf("expected %d logged mismatches (one tampered per iteration), got %d", want, mismatches)
	}
}

// --- contract pin: ErrMismatch wrapping on UNTRUSTED with correct bytes -------

// TestAdv_Untrusted_CorrectBytes_StillRejected pins the documented fail-closed
// contract: an UNTRUSTED node is rejected even when its bytes are correct.
func TestAdv_Untrusted_CorrectBytes_StillRejected(t *testing.T) {
	v := verifier.New(doubler)
	job := verifier.Job{ID: "job-u", Input: []byte{3, 4}}
	correct := []byte{6, 8}
	err := v.VerifyResult(job, correct, console.TrustUntrusted, "node-u")
	if err == nil {
		t.Fatal("UNTRUSTED with correct bytes must still be rejected")
	}
	if !errors.Is(err, verifier.ErrMismatch) {
		t.Fatalf("UNTRUSTED rejection must wrap ErrMismatch, got: %v", err)
	}
}
