// Package fmea_test — ADVERSARIAL probe of the FMEA RPN scoring + ranking
// surface (pkg/fmea). FMEA computes RPN = Severity × Occurrence × Detection
// and HighRisk ranks failure modes by it. Numeric scoring + ranking is exactly
// the surface where score-poison / overflow / comparator-total-order bugs live.
//
// This file is package-external (fmea_test) and additive: it does NOT modify
// production code. It probes SINK-SIDE behavior (actual ranked order, actual
// top/bottom risk, actual RPN values), never merely "no panic".
//
// FINDINGS (honest three-way discrimination — see report):
//
//   - NUMERIC POISON (NaN/±Inf): NOT APPLICABLE. Severity/Occurrence/Detection
//     and RPN are all `int`. There is no float64 anywhere in the package, so
//     NaN/±Inf cannot exist. Pinned by TestAdv_NoFloatSurface_IntOnly.
//
//   - INTEGER OVERFLOW (catastrophic→bottom inversion): REACHABLE but only on
//     inputs the documented contract DISCLAIMS. RPN() doc says S/O/D follow the
//     "standard FMEA 1-10 scale"; Validate() enforces [1,10]; the package usage
//     example runs ValidateCatalog BEFORE HighRisk. Within [1,10] max RPN=1000,
//     overflow impossible. The overflow only bites if a caller feeds HighRisk
//     UN-validated entries. → DOCUMENTED CONTRACT / LATENT RISK, not a prod bug.
//     TestAdv_Contract_ValidRangeNeverOverflows pins the safe contract (PASS).
//     TestAdv_LATENT_OverflowInvertsRanking documents the latent inversion as a
//     true sink-side fact on the CURRENT code (PASS on unfixed prod): with
//     out-of-range factors a CATASTROPHIC mode wraps to a negative RPN and sorts
//     to the BOTTOM, below a benign mode. This is the worst possible inversion;
//     it is a hardening opportunity (HighRisk could re-validate or saturate),
//     not a contract violation.
//
//   - COMPARATOR / TOTAL ORDER: on valid catalogs (unique IDs, the enforced
//     invariant) HighRisk's comparator is a strict weak order with a
//     deterministic tiebreak (ascending ID); ranking is stable & deterministic.
//     Pinned by TestAdv_Comparator_DeterministicAcrossShuffles. Nondeterminism
//     is reachable ONLY with duplicate IDs + equal RPN — which ValidateCatalog
//     rejects → LATENT, documented by TestAdv_LATENT_DupIDEqualRPN_OrderUnpinned.
//
//   - CONCURRENCY: package is pure (no goroutines, no package-level mutable
//     state; DefaultCatalog returns a fresh slice). Pinned race-safe by
//     TestAdv_Concurrent_HighRisk_RaceClean.
package fmea_test

import (
	"math"
	"sort"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/fmea"
)

// advEntry builds a minimal valid-shaped FailureMode with explicit S/O/D so we
// can drive RPN/HighRisk directly. (Local helper; avoids depending on the other
// test file's unexported helpers.)
func advEntry(id string, s, o, d int) fmea.FailureMode {
	return fmea.FailureMode{
		ID:              id,
		Component:       "Comp",
		Mode:            "mode " + id,
		Effect:          "effect " + id,
		Cause:           "cause",
		Severity:        s,
		Occurrence:      o,
		Detection:       d,
		DetectionSignal: "sig > 0",
		Recovery:        "step 1",
		RunbookURL:      "https://runbooks.helixcluster.dev/test/" + id,
	}
}

// ---------------------------------------------------------------------------
// NUMERIC POISON probe — proves the surface is int-only (NaN/Inf impossible).
// ---------------------------------------------------------------------------

// TestAdv_NoFloatSurface_IntOnly documents that RPN is integer arithmetic, so
// the NaN/±Inf score-poison class that broke ewmarank/rescorer/forecast cannot
// occur here. RPN of the largest in-range entry is exactly 1000.
// Mutation: change RPN() to S+O+D → 30 ≠ 1000 → fails.
func TestAdv_NoFloatSurface_IntOnly(t *testing.T) {
	fm := advEntry("FM-INT", 10, 10, 10)
	if got := fm.RPN(); got != 1000 {
		t.Fatalf("max in-range RPN = %d, want 1000 (int 10×10×10)", got)
	}
}

// ---------------------------------------------------------------------------
// INTEGER-OVERFLOW probes.
// ---------------------------------------------------------------------------

// TestAdv_Contract_ValidRangeNeverOverflows is the CONTRACT-PINNING test: for
// every (S,O,D) in the documented [1,10] range, RPN stays in [1,1000] and is
// strictly positive — overflow is structurally impossible inside the contract.
// Mutation: change RPN() to use a wrapping shift (e.g. S<<O<<D) → some triple
// exceeds 1000 or goes ≤0 → fails.
func TestAdv_Contract_ValidRangeNeverOverflows(t *testing.T) {
	for s := 1; s <= 10; s++ {
		for o := 1; o <= 10; o++ {
			for d := 1; d <= 10; d++ {
				r := advEntry("x", s, o, d).RPN()
				if r < 1 || r > 1000 {
					t.Fatalf("RPN(%d,%d,%d)=%d outside [1,1000] — overflow inside contract!", s, o, d, r)
				}
			}
		}
	}
}

// TestAdv_LATENT_OverflowInvertsRanking is a SINK-SIDE reproducer of the latent
// integer-overflow inversion. It feeds HighRisk UN-validated entries (outside
// the documented [1,10] range — a contract the caller is responsible for). The
// CATASTROPHIC mode's factors overflow int64, wrapping its RPN NEGATIVE, so it
// sorts to the BOTTOM of the ranking — below a benign RPN=8 mode. This is the
// worst-possible inversion (max risk ranked as min risk).
//
// This asserts the CURRENT (unfixed) behavior and therefore PASSES on prod as a
// documentation-of-defect. It is NOT gated behind an env var. If HighRisk is
// ever hardened to re-validate/saturate, this test's expectations must flip and
// the comment updated — that flip is the signal the latent risk was closed.
func TestAdv_LATENT_OverflowInvertsRanking(t *testing.T) {
	// Sanity: prove the overflow wraps negative at the RPN sink itself.
	catRPN := advEntry("CAT", math.MaxInt, 2, 2).RPN()
	if catRPN >= 0 {
		t.Fatalf("expected MaxInt×2×2 to wrap negative, got RPN=%d "+
			"(int width changed? overflow no longer reachable)", catRPN)
	}

	catalog := []fmea.FailureMode{
		advEntry("FM-CATASTROPHIC", math.MaxInt, 2, 2), // overflows → negative RPN
		advEntry("FM-BENIGN", 2, 2, 2),                 // RPN = 8
	}
	// threshold = 1: both "pass" the >= filter? The benign one (8) does; the
	// catastrophic one has negative RPN so it is EXCLUDED by `>= threshold`,
	// which is itself the bug — a catastrophic mode silently dropped from the
	// high-risk list. Use threshold = MinInt to force both in and inspect order.
	got := fmea.HighRisk(catalog, math.MinInt)
	if len(got) != 2 {
		t.Fatalf("expected both entries, got %d", len(got))
	}
	// SINK: catastrophic mode is ranked LAST (bottom), benign mode is TOP.
	if got[0].ID != "FM-BENIGN" || got[len(got)-1].ID != "FM-CATASTROPHIC" {
		t.Fatalf("overflow inversion not reproduced: top=%s bottom=%s "+
			"(want top=FM-BENIGN bottom=FM-CATASTROPHIC)", got[0].ID, got[len(got)-1].ID)
	}

	// SINK #2: with a normal positive threshold the catastrophic mode is
	// DROPPED entirely from the high-risk set — the most dangerous outcome.
	high := fmea.HighRisk(catalog, 1)
	for _, fm := range high {
		if fm.ID == "FM-CATASTROPHIC" {
			t.Fatalf("LATENT-risk expectation changed: catastrophic mode now "+
				"appears in HighRisk(t=1) set %v — code may have been hardened; "+
				"update this test", high)
		}
	}
	t.Logf("LATENT (documented contract boundary): out-of-range factors wrap "+
		"RPN negative (RPN=%d); catastrophic mode sorts to bottom and is dropped "+
		"from HighRisk(threshold>=1). Reachable only when HighRisk is called on "+
		"entries that did not pass Validate/ValidateCatalog.", catRPN)
}

// ---------------------------------------------------------------------------
// COMPARATOR / TOTAL-ORDER probes.
// ---------------------------------------------------------------------------

// TestAdv_Comparator_DeterministicAcrossShuffles proves the ranking is
// deterministic on a VALID catalog (unique IDs) regardless of input order,
// including across an RPN tie that must be broken by ascending ID.
// Mutation: drop the `result[i].ID < result[j].ID` tiebreak (return false) →
// the two RPN-tied entries reorder under shuffling → fails.
func TestAdv_Comparator_DeterministicAcrossShuffles(t *testing.T) {
	// FM-A and FM-E both RPN=252 (tie) → must order FM-A before FM-E by ID.
	base := []fmea.FailureMode{
		advEntry("FM-A", 9, 4, 7), // 252
		advEntry("FM-B", 8, 3, 4), // 96
		advEntry("FM-C", 7, 5, 5), // 175
		advEntry("FM-E", 9, 4, 7), // 252 tie
	}
	want := []string{"FM-A", "FM-E", "FM-C"} // RPN desc, tie A<E; B(96) excluded at t=100

	// Try several distinct input permutations; output must be identical each time.
	perms := [][]int{
		{0, 1, 2, 3},
		{3, 2, 1, 0},
		{1, 3, 0, 2},
		{2, 0, 3, 1},
	}
	for _, p := range perms {
		in := make([]fmea.FailureMode, len(p))
		for i, idx := range p {
			in[i] = base[idx]
		}
		got := fmea.HighRisk(in, 100)
		if len(got) != len(want) {
			t.Fatalf("perm %v: len=%d want %d", p, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("perm %v: got[%d]=%s want %s (nondeterministic ranking)",
					p, i, got[i].ID, want[i])
			}
		}
	}
}

// TestAdv_LATENT_DupIDEqualRPN_OrderUnpinned documents that the comparator's
// total order depends on the catalog invariant "IDs are unique" (enforced by
// ValidateCatalog). If a caller bypasses validation and supplies two entries
// with the SAME ID and the SAME RPN that differ in other fields, the comparator
// returns false in both directions, so sort.Slice (NOT stable) may order them
// either way. We assert the SET is preserved (both present) rather than a fixed
// order, and document the latent nondeterminism.
//
// This PASSES on prod (it asserts what is actually guaranteed). It is a LATENT
// note, not a bug: unique IDs are an enforced precondition of a valid catalog.
func TestAdv_LATENT_DupIDEqualRPN_OrderUnpinned(t *testing.T) {
	x1 := advEntry("FM-DUP", 9, 4, 7) // RPN 252
	x1.Mode = "variant-1"
	x2 := advEntry("FM-DUP", 9, 4, 7) // same ID, same RPN
	x2.Mode = "variant-2"

	got := fmea.HighRisk([]fmea.FailureMode{x1, x2}, 100)
	if len(got) != 2 {
		t.Fatalf("expected both dup-ID entries retained, got %d", len(got))
	}
	// Only a SET assertion is safe — order between equal-ID/equal-RPN entries is
	// not pinned by a total order. Confirm ValidateCatalog would have caught this.
	if err := fmea.ValidateCatalog([]fmea.FailureMode{x1, x2}); err == nil {
		t.Fatalf("ValidateCatalog should reject duplicate ID FM-DUP (the invariant "+
			"that makes HighRisk's order deterministic) — got nil")
	}
}

// ---------------------------------------------------------------------------
// CONCURRENCY probe (-race).
// ---------------------------------------------------------------------------

// TestAdv_Concurrent_HighRisk_RaceClean runs HighRisk + DefaultCatalog from many
// goroutines. Each call must return an identical, fully-sorted result with no
// data race (package is pure; DefaultCatalog returns a fresh slice per call).
// Run under -race; a shared mutable catalog or in-place aliasing would trip it.
func TestAdv_Concurrent_HighRisk_RaceClean(t *testing.T) {
	// Reference result computed once, single-threaded.
	refCat := fmea.DefaultCatalog()
	ref := fmea.HighRisk(refCat, 1)
	refIDs := idsOf(ref)
	// Confirm reference is actually descending (sink-side correctness).
	if !sort.SliceIsSorted(ref, func(i, j int) bool {
		return ref[i].RPN() > ref[j].RPN() ||
			(ref[i].RPN() == ref[j].RPN() && ref[i].ID < ref[j].ID)
	}) {
		t.Fatalf("reference HighRisk not in (RPN desc, ID asc) order: %v", refIDs)
	}

	const workers = 32
	var wg sync.WaitGroup
	errCh := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := fmea.DefaultCatalog()
			got := idsOf(fmea.HighRisk(c, 1))
			if !equalStrs(got, refIDs) {
				errCh <- "nondeterministic concurrent ranking"
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}

func idsOf(fms []fmea.FailureMode) []string {
	out := make([]string, len(fms))
	for i, fm := range fms {
		out[i] = fm.ID
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
