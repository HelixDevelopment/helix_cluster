package ewmarank

import (
	"math"
	"testing"
)

// runUUID anchors this test binary's observable log lines for forensic tracing.
const runUUID = "ewmarank-3f1c9d28-7a64-4b0e-9c12-5d8e2f4a6b71"

// feed applies the same sequence of (backend, sample) observations to a Ranker.
type obs struct {
	id     string
	sample float64
}

func feed(r *Ranker, seq []obs) {
	for _, o := range seq {
		r.Observe(o.id, o.sample)
	}
}

func eq(a, b []string) bool {
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

// TestRankDeterministicAscendingAndShifts covers CLOSURE clause 1:
// deterministic ascending-EWMA ordering reproduced across two independent
// Ranker instances fed identical inputs, and that the EWMA actually shifts the
// ranking as load samples change (a once-best backend degrades after high load).
//
// Mutation: ranking by DESCENDING utilization (flip the `<` to `>` in Rank)
// makes the expected ascending order [a b c d] fail, since a/b which carry the
// lowest measured utilization would then sort last instead of first.
func TestRankDeterministicAscendingAndShifts(t *testing.T) {
	t.Logf("run=%s clause=1 phase=before building two independent rankers", runUUID)

	// Distinct steady-state utilizations: a < b < c < d after warmup.
	seq := []obs{
		{"d", 0.90}, {"c", 0.70}, {"b", 0.40}, {"a", 0.10},
		{"d", 0.92}, {"c", 0.68}, {"b", 0.42}, {"a", 0.08},
		{"d", 0.88}, {"c", 0.72}, {"b", 0.38}, {"a", 0.12},
	}

	r1 := New(0.5)
	r2 := New(0.5)
	feed(r1, seq)
	feed(r2, seq)

	rank1 := r1.Rank()
	rank2 := r2.Rank()
	t.Logf("run=%s clause=1 phase=after rank1=%v rank2=%v", runUUID, rank1, rank2)

	// Determinism: identical inputs -> identical ordering across instances.
	if !eq(rank1, rank2) {
		t.Fatalf("non-deterministic ranking: %v vs %v", rank1, rank2)
	}

	// Ascending (least-loaded first): a has lowest EWMA, d highest.
	want := []string{"a", "b", "c", "d"}
	if !eq(rank1, want) {
		t.Fatalf("expected ascending-EWMA order %v, got %v", want, rank1)
	}

	// Confirm the underlying EWMAs are genuinely ordered (guards against a
	// ranking that happened to match by ID rather than by load).
	ea, _ := r1.EWMA("a")
	eb, _ := r1.EWMA("b")
	ec, _ := r1.EWMA("c")
	ed, _ := r1.EWMA("d")
	t.Logf("run=%s clause=1 ewma a=%.4f b=%.4f c=%.4f d=%.4f", runUUID, ea, eb, ec, ed)
	if !(ea < eb && eb < ec && ec < ed) {
		t.Fatalf("EWMA not strictly ascending a<b<c<d: %.4f %.4f %.4f %.4f", ea, eb, ec, ed)
	}

	// Now shift load: "a" (previously best) gets hammered with high samples;
	// it must degrade in rank below at least one previously-worse backend.
	beforeBest := r1.Rank()[0]
	t.Logf("run=%s clause=1 phase=shift-before best=%s", runUUID, beforeBest)
	for i := 0; i < 6; i++ {
		r1.Observe("a", 0.99)
	}
	afterRank := r1.Rank()
	afterBest := afterRank[0]
	eaAfter, _ := r1.EWMA("a")
	t.Logf("run=%s clause=1 phase=shift-after best=%s ewmaA=%.4f rank=%v",
		runUUID, afterBest, eaAfter, afterRank)

	if beforeBest != "a" {
		t.Fatalf("precondition: expected 'a' to be best before shift, got %s", beforeBest)
	}
	if afterBest == "a" {
		t.Fatalf("EWMA failed to shift ranking: 'a' still best after sustained high load")
	}
	// 'a' must now rank strictly worse than 'b' (the next-lowest baseline).
	posA, posB := indexOf(afterRank, "a"), indexOf(afterRank, "b")
	if !(posA > posB) {
		t.Fatalf("after high load, 'a' (pos %d) should rank worse than 'b' (pos %d)", posA, posB)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// TestStickyRoutingAndFailover covers CLOSURE clause 2:
// RouteSticky returns the SAME backend on repeated calls; after MarkFailed on
// that backend the next RouteSticky returns a DIFFERENT (next-best) backend.
//
// Mutation: ignoring MarkFailed (e.g. RouteSticky returning the pinned backend
// regardless of its failed flag) makes the post-failover assertion fail, since
// the client would remain on the failed backend instead of moving to next-best.
func TestStickyRoutingAndFailover(t *testing.T) {
	r := New(0.5)
	// a is least-loaded, then b, then c.
	feed(r, []obs{
		{"a", 0.10}, {"b", 0.40}, {"c", 0.70},
		{"a", 0.12}, {"b", 0.38}, {"c", 0.72},
	})

	client := "client-X"
	first := r.RouteSticky(client)
	t.Logf("run=%s clause=2 phase=before stickyTarget=%s rank=%v", runUUID, first, r.Rank())

	// Best (least-loaded) backend must be 'a'.
	if first != "a" {
		t.Fatalf("expected sticky route to least-loaded 'a', got %s", first)
	}

	// Stickiness: repeated calls return the same backend, even though we now
	// push 'a' to be slightly worse than its baseline — stickiness must hold
	// because the backend has not failed.
	r.Observe("a", 0.30) // a still well below b/c, ranking unchanged but verify pin holds
	for i := 0; i < 5; i++ {
		if got := r.RouteSticky(client); got != first {
			t.Fatalf("sticky violated on call %d: want %s got %s", i, first, got)
		}
	}

	// Make 'a' temporarily the WORST yet still expect stickiness (pin survives
	// rank changes; only failure breaks it).
	for i := 0; i < 8; i++ {
		r.Observe("a", 0.99)
	}
	if got := r.RouteSticky(client); got != "a" {
		t.Fatalf("sticky must survive rank degradation (no failure): want a got %s", got)
	}
	ea, _ := r.EWMA("a")
	t.Logf("run=%s clause=2 sticky-held-despite-degrade ewmaA=%.4f stillRoutedTo=a", runUUID, ea)

	// Now fail 'a'. Next route must move to a DIFFERENT, next-best available
	// backend. With 'a' failed, best available among {b,c} is 'b'.
	r.MarkFailed("a")
	after := r.RouteSticky(client)
	t.Logf("run=%s clause=2 phase=after markedFailed=a newStickyTarget=%s rank=%v",
		runUUID, after, r.Rank())

	if after == first {
		t.Fatalf("failover did not occur: still routed to failed backend %s", after)
	}
	if after != "b" {
		t.Fatalf("expected failover to next-best available 'b', got %s", after)
	}

	// And the new assignment is itself sticky.
	if again := r.RouteSticky(client); again != after {
		t.Fatalf("post-failover assignment not sticky: want %s got %s", after, again)
	}
}

// TestEWMASmoothsNotReplace covers CLOSURE clause 3:
// a single high sample moves the EWMA only PARTIALLY toward it (per alpha),
// proving the smoothing formula rather than a replace.
//
// Mutation: an EWMA that REPLACES (ewma = sample) instead of smoothing makes
// the partial-move assertion fail, because after one high sample the EWMA would
// equal the sample exactly rather than landing strictly between prior and sample.
func TestEWMASmoothsNotReplace(t *testing.T) {
	const alpha = 0.25
	r := New(alpha)

	// Seed with a low baseline (first sample initializes exactly).
	r.Observe("n1", 0.20)
	prior, _ := r.EWMA("n1")
	t.Logf("run=%s clause=3 phase=before ewma=%.6f (seed sample=0.20)", runUUID, prior)
	if prior != 0.20 {
		t.Fatalf("first sample must initialize EWMA exactly: want 0.20 got %.6f", prior)
	}

	// Apply a single HIGH sample.
	const sample = 1.00
	r.Observe("n1", sample)
	got, _ := r.EWMA("n1")

	// Expected per formula: alpha*sample + (1-alpha)*prior.
	want := alpha*sample + (1-alpha)*prior
	t.Logf("run=%s clause=3 phase=after sample=%.2f ewma=%.6f want=%.6f", runUUID, sample, got, want)

	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("EWMA value wrong: want %.12f got %.12f", want, got)
	}

	// Partial move: strictly between the prior and the sample (NOT a replace,
	// NOT unchanged). This is the load-bearing anti-replace assertion.
	if !(got > prior && got < sample) {
		t.Fatalf("EWMA did not partially move: prior=%.6f got=%.6f sample=%.6f", prior, got, sample)
	}

	// Quantitative: the move must be exactly alpha of the gap, not the whole gap.
	movedFraction := (got - prior) / (sample - prior)
	t.Logf("run=%s clause=3 movedFraction=%.6f expectedAlpha=%.6f", runUUID, movedFraction, alpha)
	if math.Abs(movedFraction-alpha) > 1e-9 {
		t.Fatalf("move fraction %.9f != alpha %.9f (formula is not a proper EWMA)", movedFraction, alpha)
	}

	// A second identical high sample moves further but still not all the way —
	// guards against a delayed replace.
	r.Observe("n1", sample)
	got2, _ := r.EWMA("n1")
	t.Logf("run=%s clause=3 second-sample ewma=%.6f", runUUID, got2)
	if !(got2 > got && got2 < sample) {
		t.Fatalf("second high sample should move further but stay below sample: got2=%.6f", got2)
	}
}

// TestAlphaClampDefault guards the alpha configuration boundary: out-of-range
// alpha falls back to DefaultAlpha; in-range alpha is honored.
//
// Mutation: dropping the clamp (storing an invalid alpha like 0) would make
// Observe a no-op-ish or degenerate update and break this expectation.
func TestAlphaClampDefault(t *testing.T) {
	t.Logf("run=%s clause=alpha phase=before constructing rankers", runUUID)
	if a := New(0).Alpha(); a != DefaultAlpha {
		t.Fatalf("alpha=0 should clamp to default %.3f, got %.3f", DefaultAlpha, a)
	}
	if a := New(1.5).Alpha(); a != DefaultAlpha {
		t.Fatalf("alpha=1.5 should clamp to default %.3f, got %.3f", DefaultAlpha, a)
	}
	if a := New(0.42).Alpha(); a != 0.42 {
		t.Fatalf("valid alpha 0.42 must be honored, got %.3f", a)
	}
	t.Logf("run=%s clause=alpha phase=after clamp verified", runUUID)
}
