package rescorer

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a random hex id so each test run is traceable in logs.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func indexOf(rank []string, id string) int {
	for i, v := range rank {
		if v == id {
			return i
		}
	}
	return -1
}

// Clause 1: a price spike on the current top provider, applied between cycles,
// drops that provider on the NEXT cycle and the previously-second becomes top.
//
// Mutation: if score() ignores price (e.g. returns a constant), the ranking is
// static, the spiked provider stays top, and the post-spike assertions fail.
func TestPriceSpikeDemotesTopProvider_Clause1(t *testing.T) {
	id := runUUID(t)
	const cycle = 60
	r := New(cycle, 0, 0 /* guard disabled: pure cycle behavior */)

	// pA cheapest, then pB, then pC.
	r.SetPrice("pA", 10)
	r.SetPrice("pB", 20)
	r.SetPrice("pC", 30)

	r.Tick(0) // establish initial ranking
	before := r.Ranking()
	t.Logf("run=%s before spike: ranking=%v top=%s", id, before, r.Top())

	if r.Top() != "pA" {
		t.Fatalf("run=%s expected cheapest pA on top, got %v", id, before)
	}
	if got := []string{"pA", "pB", "pC"}; indexOf(before, "pA") != 0 || indexOf(before, "pB") != 1 || indexOf(before, "pC") != 2 {
		t.Fatalf("run=%s initial ranking %v != %v", id, before, got)
	}

	// Spike pA's price far above pB and pC, between cycles.
	r.SetPrice("pA", 100)

	// Ranking must still be stable until the boundary.
	mid := r.Ranking()
	if indexOf(mid, "pA") != 0 {
		t.Fatalf("run=%s pA should still be top before boundary, got %v", id, mid)
	}

	// Advance one full cycle -> re-rank.
	r.Tick(cycle)
	after := r.Ranking()
	t.Logf("run=%s after spike+cycle: ranking=%v top=%s", id, after, r.Top())

	// pA must drop OUT of the top.
	if r.Top() == "pA" {
		t.Fatalf("run=%s spiked pA must be demoted from top, got ranking=%v", id, after)
	}
	// Previously-second (pB) becomes top.
	if r.Top() != "pB" {
		t.Fatalf("run=%s previously-second pB must become top, got top=%s ranking=%v", id, r.Top(), after)
	}
	// pA must now be ranked below pB and pC (price 100 vs 20,30).
	if !(indexOf(after, "pA") > indexOf(after, "pB") && indexOf(after, "pA") > indexOf(after, "pC")) {
		t.Fatalf("run=%s pA should be last after spike, got %v", id, after)
	}
}

// Clause 2: re-rank happens on the cycle boundary, NOT every tick. The ranking
// is stable between boundaries and updates AT the boundary.
//
// Mutation: if re-rank runs every tick instead of per-cycle, the mid-cycle Tick
// at now=30 would already reflect the new price and the "stable between
// boundaries" assertion fails.
func TestRerankOnlyAtCycleBoundary_Clause2(t *testing.T) {
	id := runUUID(t)
	const cycle = 60
	r := New(cycle, 0, 0)

	r.SetPrice("pA", 10)
	r.SetPrice("pB", 20)
	r.Tick(0) // boundary 0: ranking [pA, pB]

	baseline := r.Ranking()
	t.Logf("run=%s baseline@0: %v", id, baseline)
	if indexOf(baseline, "pA") != 0 {
		t.Fatalf("run=%s expected pA top at boundary 0, got %v", id, baseline)
	}

	// Make pB cheaper than pA between boundaries.
	r.SetPrice("pB", 1)

	// Ticks strictly inside the cycle must NOT re-rank.
	for _, now := range []int64{1, 30, 59} {
		r.Tick(now)
		mid := r.Ranking()
		t.Logf("run=%s mid-cycle tick now=%d: %v", id, now, mid)
		if indexOf(mid, "pA") != 0 {
			t.Fatalf("run=%s ranking changed mid-cycle at now=%d (got %v); re-rank must only happen at boundary", id, now, mid)
		}
	}

	// At the boundary now=60 the re-rank must apply: pB now cheapest -> top.
	r.Tick(60)
	after := r.Ranking()
	t.Logf("run=%s boundary@60: %v top=%s", id, after, r.Top())
	if r.Top() != "pB" {
		t.Fatalf("run=%s expected pB on top at boundary 60, got %v", id, after)
	}
}

// Clause 2 (guard variant): the price-spike guard reacts WITHIN one cycle.
//
// Mutation: if SetPrice never arms the guard, or Tick ignores the guard, the
// spiked provider would stay top until the boundary and this fails.
func TestSpikeGuardReactsWithinCycle_Clause2Guard(t *testing.T) {
	id := runUUID(t)
	const cycle = 60
	r := New(cycle, 0, 2.0 /* >=2x jump triggers guard */)

	r.SetPrice("pA", 10)
	r.SetPrice("pB", 20)
	r.Tick(0)
	if r.Top() != "pA" {
		t.Fatalf("run=%s expected pA top, got %v", id, r.Ranking())
	}
	t.Logf("run=%s before guard: %v", id, r.Ranking())

	// Abrupt 5x jump on the top provider -> guard arms.
	r.SetPrice("pA", 50)

	// A single tick well inside the cycle should already demote pA.
	r.Tick(5)
	after := r.Ranking()
	t.Logf("run=%s after guard tick@5: %v top=%s", id, after, r.Top())
	if r.Top() != "pB" {
		t.Fatalf("run=%s spike guard should demote pA within cycle, got top=%s ranking=%v", id, r.Top(), after)
	}
}

// Clause 3: with no spike, the ranking is stable across many cycles.
//
// Mutation: re-ranking every tick on changing scores would still be stable here
// (prices don't change), so this clause specifically guards against spurious
// reordering / nondeterminism in rerank's tie-breaking and clock handling.
func TestNoSpikeRankingStable_Clause3(t *testing.T) {
	id := runUUID(t)
	const cycle = 60
	r := New(cycle, 0, 2.0)

	r.SetPrice("pA", 10)
	r.SetPrice("pB", 20)
	r.SetPrice("pC", 30)
	r.Tick(0)
	want := r.Ranking()
	t.Logf("run=%s established: %v", id, want)
	if indexOf(want, "pA") != 0 || indexOf(want, "pB") != 1 || indexOf(want, "pC") != 2 {
		t.Fatalf("run=%s unexpected initial ranking %v", id, want)
	}

	for _, now := range []int64{30, 60, 90, 120, 180, 240} {
		r.Tick(now)
		got := r.Ranking()
		if len(got) != len(want) {
			t.Fatalf("run=%s ranking length changed at now=%d: %v", id, now, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run=%s ranking changed without spike at now=%d: got %v want %v", id, now, got, want)
			}
		}
	}
	t.Logf("run=%s stable across cycles: %v", id, r.Ranking())
}

// Sanity: SetPrice does not panic before any Tick and Ranking is empty.
func TestEmptyBeforeTick(t *testing.T) {
	id := runUUID(t)
	r := New(60, 0, 0)
	if rk := r.Ranking(); len(rk) != 0 {
		t.Fatalf("run=%s expected empty ranking before tick, got %v", id, rk)
	}
	if top := r.Top(); top != "" {
		t.Fatalf("run=%s expected empty top before tick, got %q", id, top)
	}
}
