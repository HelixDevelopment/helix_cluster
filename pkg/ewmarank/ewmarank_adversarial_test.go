package ewmarank

import (
	"math"
	"sort"
	"testing"
)

// runUUIDAdv anchors this adversarial binary's observable log lines.
const runUUIDAdv = "ewmarank-adv-1d4f7c92-3b8a-4e61-9f02-6c7a5e9d2b18"

// NaN-poison reproducers (TestNaNPoison_DestroysRankOrder,
// TestRankComparator_NaNBreaksStrictWeakOrdering) ran opt-in while the bug was
// open; the fix landed (HXC-1794: Observe drops NaN samples), so they now run by
// default as permanent regression guards — removing the guard makes them FAIL.

// --- PROBE (a): TOTAL-ORDER AT THE EQUAL-EWMA EDGE ----------------------------
//
// CONTRACT (ewmarank.go Rank doc, verbatim): "Ties on EWMA are broken
// deterministically by ascending backend ID." We PIN that promise: two backends
// with byte-identical EWMA must come out in ascending-ID order, every call.
//
// This is a CONTRACT-PINNING test (expected GREEN on current prod): the seeded
// guard `bi.ewma != bj.ewma` correctly falls through to `ids[i] < ids[j]` for
// real (non-NaN) equal scores. If someone deleted the ID tie-break, this bites.
func TestTieEqualEWMA_DeterministicByID(t *testing.T) {
	r := New(0.5)
	// Seed three backends to the SAME exact EWMA via identical first-sample.
	// First sample initializes EWMA exactly (per Observe doc), so all == 0.5.
	for _, id := range []string{"zeta", "alpha", "mike"} {
		r.Observe(id, 0.5)
	}
	ez, _ := r.EWMA("zeta")
	ea, _ := r.EWMA("alpha")
	em, _ := r.EWMA("mike")
	t.Logf("run=%s probe=a ewma zeta=%.4f alpha=%.4f mike=%.4f", runUUIDAdv, ez, ea, em)
	if !(ez == ea && ea == em) {
		t.Fatalf("precondition: expected identical EWMA, got %.6f %.6f %.6f", ez, ea, em)
	}
	want := []string{"alpha", "mike", "zeta"} // ascending ID
	// Repeat: tie-break must be deterministic across calls.
	for call := 0; call < 8; call++ {
		got := r.Rank()
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("call %d: tie not broken by ascending ID: want %v got %v", call, want, got)
		}
	}
	t.Logf("run=%s probe=a PASS deterministic-id-tiebreak %v", runUUIDAdv, want)
}

// --- PROBE (b): NUMERIC POISON via NaN — REAL BUG ----------------------------
//
// REAL-WORLD DAMAGE under test: a single NaN utilization sample (e.g. a 0/0
// divide from an upstream metric, or a malformed latency reading) is folded into
// a backend's EWMA. The Observe formula `alpha*sample + (1-alpha)*ewma` has NO
// NaN guard, so the backend's EWMA becomes a permanent NaN, and the Rank
// comparator
//
//	if bi.seeded && bj.seeded && bi.ewma != bj.ewma { return bi.ewma < bj.ewma }
//	return ids[i] < ids[j]
//
// is no longer a strict weak ordering: for any NaN-vs-finite pair, `ewma != ewma`
// is TRUE (NaN != x), so it returns `bi.ewma < bj.ewma`, which is FALSE in BOTH
// directions. The pair NEVER reaches the documented ID tie-break, so where the
// NaN element lands — and where the FINITE elements land relative to it — is
// decided by sort.SliceStable's scan path, i.e. by INSERTION ORDER, not by ID
// or by load.
//
// The doc PROMISES three things this VIOLATES:
//  1. "ASCENDING EWMA utilization (least-loaded first)"
//  2. "deterministic tie-break by ascending backend ID"
//  3. determinism (identical state -> identical order)
//
// SINK PROOF (the package's whole job is the rank ORDER): we build two rankers
// with the IDENTICAL set of backends and IDENTICAL values {a:0.10, z:0.90,
// n:NaN} and differ ONLY in the insertion order of the NaN backend. A faithful
// implementation MUST return the same order (it is the same logical state, and
// the doc ties everything to ID, never to insertion order). Instead the orders
// DIFFER, and worse, in one of them the MOST-loaded backend (z, 0.90) is ranked
// AHEAD of the LEAST-loaded (a, 0.10) — the exact inversion that steers traffic
// to the worst node.
// REGRESSION GUARD (HXC-1794, fix landed): the Observe NaN-drop guard keeps a
// NaN sample from poisoning EWMA state and destroying Rank's order. Runs by
// default; if the guard is removed this FAILS (Rank becomes insertion-order
// dependent). Mutation-proven load-bearing.
func TestNaNPoison_DestroysRankOrder(t *testing.T) {
	build := func(order []string) *Ranker {
		r := New(0.5)
		vals := map[string]float64{"a": 0.10, "z": 0.90, "n": math.NaN()}
		for _, id := range order {
			r.Observe(id, vals[id])
		}
		return r
	}

	// Same backends + values; only the position of the NaN insert changes.
	rNaNFirst := build([]string{"n", "a", "z"})
	rNaNLast := build([]string{"a", "z", "n"})

	rankFirst := rNaNFirst.Rank()
	rankLast := rNaNLast.Rank()

	en, seededN := rNaNFirst.EWMA("n")
	t.Logf("run=%s probe=b ewma n=%v seeded=%v", runUUIDAdv, en, seededN)
	t.Logf("run=%s probe=b rank(NaN-inserted-first)=%v", runUUIDAdv, rankFirst)
	t.Logf("run=%s probe=b rank(NaN-inserted-last) =%v", runUUIDAdv, rankLast)
	// A correct implementation must NOT carry a NaN in any backend's EWMA state:
	// if the NaN backend is seeded at all, its score must be finite (NaN poison
	// rejected). A NaN here is the corruption the bug introduces.
	if seededN && math.IsNaN(en) {
		t.Fatalf("NaN sample poisoned EWMA state: backend 'n' carries EWMA=%v", en)
	}

	// (3) DETERMINISM: identical logical state must rank identically. The doc
	// anchors ordering to EWMA then to ID — never to insertion order.
	if !eqAdv(rankFirst, rankLast) {
		t.Fatalf("NaN poison made Rank insertion-order-dependent (non-deterministic): "+
			"NaN-first=%v vs NaN-last=%v — violates documented ID-deterministic ordering",
			rankFirst, rankLast)
	}

	// (1)+(2) ORDER SANITY on each: the least-loaded healthy backend 'a' (0.10)
	// MUST rank ahead of the most-loaded healthy backend 'z' (0.90). A NaN node
	// must never invert two FINITE, well-separated healthy nodes.
	for _, rk := range [][]string{rankFirst, rankLast} {
		if indexOfAdv(rk, "a") > indexOfAdv(rk, "z") {
			t.Fatalf("NaN poison inverted healthy order: most-loaded 'z' (0.90) ranked "+
				"ahead of least-loaded 'a' (0.10): %v", rk)
		}
	}
}

// --- PROBE (b2): ±Inf sample handling ----------------------------------------
//
// CONTRACT-PINNING: +Inf is a meaningful "fully saturated / unreachable" signal.
// Unlike NaN, +Inf is totally ordered (x < +Inf is true for finite x), so the
// comparator still works and the +Inf backend deterministically sorts LAST among
// seeded. We pin this so a future change that, say, clamps or drops +Inf is
// caught, and to contrast it with the NaN hole.
func TestPosInfSample_SortsLastDeterministically(t *testing.T) {
	r := New(0.5)
	r.Observe("a", 0.10)
	r.Observe("b", 0.50)
	r.Observe("z", math.Inf(1)) // saturated backend, ID sorts last anyway
	r.Observe("c", 0.90)
	ez, _ := r.EWMA("z")
	if !math.IsInf(ez, 1) {
		t.Fatalf("expected +Inf EWMA for saturated backend, got %v", ez)
	}
	got := r.Rank()
	t.Logf("run=%s probe=b2 rank=%v", runUUIDAdv, got)
	// +Inf is the largest; with finite a<b<c it must be last.
	if got[len(got)-1] != "z" {
		t.Fatalf("expected +Inf backend 'z' ranked last, got %v", got)
	}
	// Healthy ascending prefix preserved.
	want := []string{"a", "b", "c", "z"}
	if !eqAdv(got, want) {
		t.Fatalf("expected %v got %v", want, got)
	}
}

// --- PROBE (b3): pure total-order oracle on the comparator -------------------
//
// This is the cleanest, most direct teeth on the NaN hole. We build the EXACT
// "less" predicate Rank uses and feed it to sort.SliceIsSorted-style transitivity
// checks. A valid strict-weak-ordering comparator must satisfy: for the produced
// order, there is no pair (i<j in output) with less(j,i) true (antisymmetry on
// the result). We construct a case where the NaN element makes the comparator
// non-transitive and show the SORTED OUTPUT is not actually sorted by the
// comparator — the formal signature of a broken total order.
// REGRESSION GUARD (HXC-1794, fix landed): with the Observe NaN-drop guard,
// Rank always emits a valid least-loaded-first total order even if a NaN sample
// was offered. Runs by default; removing the guard makes this FAIL.
func TestRankComparator_NaNBreaksStrictWeakOrdering(t *testing.T) {
	r := New(0.5)
	// Spread of healthy values plus one NaN in the middle of the ID space.
	r.Observe("a", 0.10)
	r.Observe("m", math.NaN())
	r.Observe("b", 0.50)
	r.Observe("c", 0.90)

	out := r.Rank()
	t.Logf("run=%s probe=b3 produced order=%v", runUUIDAdv, out)

	// Oracle = a VALID strict weak ordering for utilization ranking: order by
	// EWMA ascending, treating NaN (an unusable signal) as the LARGEST value so a
	// poisoned backend deterministically sorts LAST, with ID as the tie-break.
	// This is exactly what a correct "least-loaded first, ID tie-break" total
	// order must yield. If Rank's output disagrees with this valid total order,
	// Rank is not emitting the promised order.
	keyOf := func(id string) float64 {
		e, _ := r.EWMA(id)
		if math.IsNaN(e) {
			return math.Inf(1) // unusable signal sinks to the bottom, deterministically
		}
		return e
	}
	validLess := func(i, j int) bool {
		ki, kj := keyOf(out[i]), keyOf(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i] < out[j]
	}
	sorted := sort.SliceIsSorted(out, validLess)
	t.Logf("run=%s probe=b3 sortedUnderValidTotalOrder=%v", runUUIDAdv, sorted)
	if !sorted {
		t.Fatalf("Rank output %v is NOT a valid least-loaded-first total order "+
			"(NaN broke the strict-weak-ordering comparator)", out)
	}
}

// --- helpers (suffixed to avoid clashing with the other test files) ----------

func indexOfAdv(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func eqAdv(a, b []string) bool {
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
