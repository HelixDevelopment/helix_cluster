package ewmarank

import (
	"math"
	"sort"
	"testing"
)

// runUUIDAdv2 anchors this adversarial binary's observable log lines.
const runUUIDAdv2 = "ewmarank-adv2-7e3b9a14-2c5d-4f80-b6a1-9d0e3c7f5a42"

// CONTEXT: the FIRST NaN fix (HXC-1794) guarded ONE axis — the SAMPLE in
// Observe. These probes hunt the SYMMETRIC holes the first fix missed:
//
//	(A) the ALPHA axis. New clamps alpha with `alpha <= 0 || alpha > 1`, but for
//	    alpha == NaN BOTH comparisons are FALSE, so a NaN alpha slips through
//	    unclamped. The very next smoothed Observe does
//	    `alpha*sample + (1-alpha)*ewma == NaN`, poisoning EVERY backend's EWMA on
//	    its second sample — the exact NaN-poison the Observe guard was added to
//	    prevent, re-entering through the un-guarded alpha door. REAL BUG; fix:
//	    add math.IsNaN(alpha) to the clamp. Mutation-proven below.
//	(B) ±Inf alpha is already clamped (+Inf>1, -Inf<=0) — PINNED so the new
//	    IsNaN clause is not "over-clamped" into rejecting valid edge behavior.
//	(C) return isolation: Rank must hand back a fresh slice; mutating it must not
//	    corrupt internal insertion order / a later Rank.
//	(D) total order at the large-magnitude and multi-+Inf edges.

// --- PROBE (A): NaN ALPHA ESCAPES THE CLAMP AND POISONS EVERY EWMA -----------
//
// REAL-WORLD DAMAGE: a caller computes alpha from config/telemetry (e.g.
// alpha := halfLife / window where window came back 0/0 → NaN, or a parsed
// float that was NaN). New silently accepts it. The first sample for each
// backend initializes EWMA exactly (no smoothing), so rank looks fine — then the
// SECOND sample for any backend runs the smoothing formula with NaN alpha and
// turns that backend's EWMA into a permanent NaN. NaN in Rank's comparator
// (`bi.ewma != bj.ewma` is TRUE for NaN, but `bi.ewma < bj.ewma` is FALSE both
// ways) breaks the strict-weak-ordering and collapses the documented
// least-loaded order to insertion order — steering traffic to an arbitrary/worst
// backend.
//
// SINK PROOF: we drive the public API exactly as a balancer would (two samples
// per backend, the second one engaging smoothing) and assert on the RANK ORDER —
// the package's entire job. With the fix (NaN alpha → DefaultAlpha) the order is
// the correct least-loaded-first [lo mid hi]; without it the EWMAs are all NaN
// and the order is insertion-dependent garbage.
//
// REGRESSION GUARD: load-bearing. Remove `math.IsNaN(alpha)` from New's clamp
// and this FAILS (NaN EWMAs / inverted order). Mutation-proven.
func TestNaNAlpha_EscapesClampAndPoisonsRank(t *testing.T) {
	// A NaN alpha must NOT survive construction.
	r := New(math.NaN())
	if got := r.Alpha(); math.IsNaN(got) {
		t.Fatalf("NaN alpha survived New unclamped: Alpha()=%v (a NaN alpha "+
			"poisons every EWMA on the second sample)", got)
	}
	if got := r.Alpha(); got != DefaultAlpha {
		t.Fatalf("NaN alpha must clamp to DefaultAlpha %.3f, got %.3f", DefaultAlpha, got)
	}
	t.Logf("run=%s probe=A alpha-after-NaN=%.3f", runUUIDAdv2, r.Alpha())

	// Drive the balancer the way a caller does: distinct loads, and crucially a
	// SECOND sample per backend so the smoothing formula (the NaN-bearing path)
	// actually runs.
	type bk struct {
		id        string
		s1, s2    float64
	}
	loads := []bk{
		{"lo", 0.10, 0.12},
		{"mid", 0.50, 0.48},
		{"hi", 0.90, 0.92},
	}
	for _, b := range loads {
		r.Observe(b.id, b.s1)
	}
	for _, b := range loads {
		r.Observe(b.id, b.s2) // engages alpha*sample + (1-alpha)*ewma
	}

	// No backend's EWMA may be NaN — a NaN here is the poison the un-clamped
	// alpha would inject.
	for _, b := range loads {
		e, ok := r.EWMA(b.id)
		if !ok {
			t.Fatalf("backend %s not seeded after two samples", b.id)
		}
		if math.IsNaN(e) {
			t.Fatalf("NaN alpha poisoned backend %s EWMA: got NaN (alpha clamp "+
				"missed math.IsNaN)", b.id)
		}
		t.Logf("run=%s probe=A ewma %s=%.6f", runUUIDAdv2, b.id, e)
	}

	// SINK: the documented least-loaded-first order must hold.
	got := r.Rank()
	want := []string{"lo", "mid", "hi"}
	t.Logf("run=%s probe=A rank=%v want=%v", runUUIDAdv2, got, want)
	if !eqAdv2(got, want) {
		t.Fatalf("NaN-alpha poison broke Rank order: want least-loaded-first %v, got %v", want, got)
	}
}

// --- PROBE (A2): NaN alpha — determinism across insertion permutations -------
//
// Reinforces (A) with the same insertion-order-independence oracle the
// sample-axis probe uses: identical logical state (same backends, same samples)
// must rank identically regardless of the order backends were first observed in.
// With a NaN alpha every EWMA is NaN and the order becomes a function of the scan
// path (insertion order), so the two permutations diverge. With the fix they
// agree. Mutation-proven against removing the IsNaN clamp.
func TestNaNAlpha_RankInsertionOrderIndependent(t *testing.T) {
	build := func(order []string) *Ranker {
		r := New(math.NaN()) // would-be poison alpha
		vals := map[string][2]float64{
			"a": {0.10, 0.12},
			"b": {0.50, 0.48},
			"c": {0.90, 0.92},
		}
		// two passes so the smoothing path runs
		for _, id := range order {
			r.Observe(id, vals[id][0])
		}
		for _, id := range order {
			r.Observe(id, vals[id][1])
		}
		return r
	}
	rk1 := build([]string{"a", "b", "c"}).Rank()
	rk2 := build([]string{"c", "b", "a"}).Rank()
	t.Logf("run=%s probe=A2 rk1=%v rk2=%v", runUUIDAdv2, rk1, rk2)
	if !eqAdv2(rk1, rk2) {
		t.Fatalf("NaN-alpha made Rank insertion-order dependent: %v vs %v", rk1, rk2)
	}
	// And the order is the correct least-loaded-first one.
	if !eqAdv2(rk1, []string{"a", "b", "c"}) {
		t.Fatalf("expected least-loaded-first [a b c], got %v", rk1)
	}
}

// --- PROBE (B): ±Inf alpha stays clamped (PIN, guards against over-clamp) -----
//
// CONTRACT-PIN: the new math.IsNaN clause must not change the established ±Inf
// behavior. +Inf>1 and -Inf<=0 already route both to DefaultAlpha; pin it so the
// fix is exactly the NaN hole and nothing more.
func TestInfAlpha_ClampedToDefault(t *testing.T) {
	for _, a := range []float64{math.Inf(1), math.Inf(-1)} {
		got := New(a).Alpha()
		t.Logf("run=%s probe=B alpha-in=%v alpha-out=%.3f", runUUIDAdv2, a, got)
		if got != DefaultAlpha {
			t.Fatalf("±Inf alpha %v must clamp to DefaultAlpha %.3f, got %.3f", a, DefaultAlpha, got)
		}
		if math.IsInf(got, 0) {
			t.Fatalf("Inf alpha survived New unclamped: %v", got)
		}
	}
}

// --- PROBE (C): Rank return slice isolation ----------------------------------
//
// Rank copies r.order into a fresh slice before sorting, so the returned slice
// must be independent of internal state: mutating it (reordering, truncating)
// must not corrupt the next Rank or the insertion order. PIN that isolation — an
// aliasing regression (e.g. returning r.order directly, or a shared backing
// array) would let a caller scramble routing for everyone.
func TestRankReturnSliceIsolation(t *testing.T) {
	r := New(0.5)
	r.Observe("a", 0.10)
	r.Observe("b", 0.50)
	r.Observe("c", 0.90)

	first := r.Rank()
	want := []string{"a", "b", "c"}
	if !eqAdv2(first, want) {
		t.Fatalf("precondition: expected %v got %v", want, first)
	}
	// Hostile caller scrambles the returned slice in place.
	for i := range first {
		first[i] = "CORRUPTED"
	}
	if len(first) > 1 {
		first[0], first[len(first)-1] = first[len(first)-1], first[0]
	}

	// A fresh Rank must be unaffected.
	second := r.Rank()
	t.Logf("run=%s probe=C afterCallerMutation rank=%v", runUUIDAdv2, second)
	if !eqAdv2(second, want) {
		t.Fatalf("Rank return slice aliases internal state: caller mutation "+
			"corrupted a later Rank: want %v got %v", want, second)
	}
}

// --- PROBE (D): total order at large-magnitude & multi-+Inf edges -------------
//
// Large finite samples must not mis-rank (no overflow/conditioning flip), and
// multiple +Inf backends (all "saturated") must still produce a deterministic,
// ID-tie-broken total order — never a non-deterministic shuffle. We assert the
// produced order is sorted under the valid (EWMA asc, ID tie-break) oracle.
func TestLargeMagnitudeAndMultiInf_TotalOrder(t *testing.T) {
	r := New(0.5)
	// Mix of large finite magnitudes and several +Inf (saturated) backends.
	r.Observe("small", 1e-9)
	r.Observe("big1", 1e18)
	r.Observe("big2", 1e18) // equal large magnitude -> ID tie-break big1<big2
	r.Observe("mid", 12345.0)
	r.Observe("satA", math.Inf(1))
	r.Observe("satB", math.Inf(1)) // two +Inf -> ID tie-break satA<satB
	// Second pass to engage smoothing on the large values (conditioning check).
	r.Observe("big1", 1e18)
	r.Observe("big2", 1e18)

	out := r.Rank()
	t.Logf("run=%s probe=D rank=%v", runUUIDAdv2, out)

	// Oracle: valid least-loaded-first total order (EWMA asc, ID tie-break). No
	// NaN expected here, so a plain key works.
	keyOf := func(id string) float64 {
		e, _ := r.EWMA(id)
		return e
	}
	validLess := func(i, j int) bool {
		ki, kj := keyOf(out[i]), keyOf(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i] < out[j]
	}
	if !sort.SliceIsSorted(out, validLess) {
		t.Fatalf("Rank output %v is not a valid least-loaded-first total order "+
			"at the large-magnitude/multi-Inf edge", out)
	}
	// Concrete sink checks: smallest finite is first; +Inf saturated are last and
	// ID-ordered; equal-large pair is ID-ordered.
	if out[0] != "small" {
		t.Fatalf("least-loaded 'small' must rank first, got %v", out)
	}
	if out[len(out)-2] != "satA" || out[len(out)-1] != "satB" {
		t.Fatalf("two +Inf backends must sort last in ascending-ID order (satA,satB), got %v", out)
	}
	if indexOfAdv2(out, "big1") > indexOfAdv2(out, "big2") {
		t.Fatalf("equal large-magnitude pair must tie-break by ID (big1 before big2): %v", out)
	}
}

// --- helpers (suffixed to avoid clashing with the other test files) ----------

func indexOfAdv2(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func eqAdv2(a, b []string) bool {
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
