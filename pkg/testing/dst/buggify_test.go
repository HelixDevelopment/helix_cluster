package dst

import (
	"math/rand"
	"testing"
	"time"
)

// TestBuggifyDeterminism proves that two independently-constructed Buggify
// instances with the same seed produce the IDENTICAL boolean sequence over
// 1000 Fire(0.25) calls.  Would fail if the PRNG were not seeded.
func TestBuggifyDeterminism(t *testing.T) {
	const seed = int64(12345)
	const trials = 1000
	const p = 0.25

	b1 := NewBuggify(seed, true)
	b2 := NewBuggify(seed, true)

	for i := 0; i < trials; i++ {
		r1 := b1.Fire(p)
		r2 := b2.Fire(p)
		if r1 != r2 {
			t.Fatalf("divergence at trial %d: b1=%v b2=%v (same seed must yield identical sequence)", i, r1, r2)
		}
	}
}

// TestBuggifyFireRate_Quarter proves that over 100,000 FireQuarter() calls the
// observed rate is within [0.24, 0.26], confirming the ~25% distribution.
// Uses real counts — would fail if the implementation were wrong (e.g. always
// true or always false).
func TestBuggifyFireRate_Quarter(t *testing.T) {
	const trials = 100_000
	b := NewBuggify(99, true)

	hits := 0
	for i := 0; i < trials; i++ {
		if b.FireQuarter() {
			hits++
		}
	}

	rate := float64(hits) / float64(trials)
	if rate < 0.24 || rate > 0.26 {
		t.Errorf("FireQuarter rate = %.5f (want 0.24..0.26); hits=%d trials=%d", rate, hits, trials)
	}
}

// TestBuggifyFireRate_Half proves Fire(0.5) hits within [0.49, 0.51] over
// 100,000 trials — the real arithmetic on floating-point samples must drive this.
func TestBuggifyFireRate_Half(t *testing.T) {
	const trials = 100_000
	b := NewBuggify(77, true)

	hits := 0
	for i := 0; i < trials; i++ {
		if b.Fire(0.5) {
			hits++
		}
	}

	rate := float64(hits) / float64(trials)
	if rate < 0.49 || rate > 0.51 {
		t.Errorf("Fire(0.5) rate = %.5f (want 0.49..0.51); hits=%d trials=%d", rate, hits, trials)
	}
}

// TestBuggifyDisabledGate proves that a disabled Buggify never fires —
// 0 of 100,000 calls may return true (a single true is a defect).
func TestBuggifyDisabledGate(t *testing.T) {
	const trials = 100_000
	b := NewBuggify(42, false)

	for i := 0; i < trials; i++ {
		if b.Fire(1.0) {
			t.Fatalf("disabled Buggify fired at trial %d (Fire(1.0) should always return false when disabled)", i)
		}
		if b.FireQuarter() {
			t.Fatalf("disabled Buggify FireQuarter fired at trial %d", i)
		}
	}
}

// TestBuggifySetEnabled confirms the enabled toggle is honoured immediately.
func TestBuggifySetEnabled(t *testing.T) {
	b := NewBuggify(1, false)
	if b.Enabled() {
		t.Fatal("expected disabled after NewBuggify(_, false)")
	}
	b.SetEnabled(true)
	if !b.Enabled() {
		t.Fatal("expected enabled after SetEnabled(true)")
	}
	b.SetEnabled(false)
	if b.Enabled() {
		t.Fatal("expected disabled after SetEnabled(false)")
	}
}

// TestBuggifyDuration proves BuggifyDuration returns the rare value exactly
// when FireQuarter fires and the normal value otherwise, over a fixed-seed
// sequence.  We reconstruct the expected sequence independently using a
// parallel rand.Rand with the same seed so the test can fail if the
// implementation deviates.
func TestBuggifyDuration(t *testing.T) {
	const seed = int64(55)
	const iters = 200

	normal := 5 * time.Second
	rare := 100 * time.Millisecond

	b := NewBuggify(seed, true)

	// Build the expected sequence using the same seeding logic.
	// We replicate the internal PRNG independently to know the exact outputs.
	oracle := rand.New(rand.NewSource(seed))

	for i := 0; i < iters; i++ {
		// BuggifyDuration calls FireQuarter which calls rng.Intn(4)==0.
		oracleVal := oracle.Intn(4)
		wantFires := oracleVal == 0
		var want time.Duration
		if wantFires {
			want = rare
		} else {
			want = normal
		}

		got := BuggifyDuration(b, normal, rare)
		if got != want {
			t.Fatalf("iter %d: BuggifyDuration()=%v want=%v (oracle Intn(4)=%d, fires=%v)",
				i, got, want, oracleVal, wantFires)
		}
	}
}

// TestBuggifyDuration_DisabledAlwaysNormal proves that BuggifyDuration always
// returns normal when Buggify is disabled, regardless of PRNG state.
func TestBuggifyDuration_DisabledAlwaysNormal(t *testing.T) {
	const iters = 1000
	normal := 10 * time.Second
	rare := 1 * time.Millisecond

	b := NewBuggify(42, false)
	for i := 0; i < iters; i++ {
		got := BuggifyDuration(b, normal, rare)
		if got != normal {
			t.Fatalf("iter %d: disabled BuggifyDuration returned %v, want %v", i, got, normal)
		}
	}
}

// TestEngineBuggifyDeterminism proves Engine.Buggify(p) is deterministic for
// a fixed engine seed: two NewEngine(seed) instances must yield the IDENTICAL
// boolean sequence.  Would fail if eng.rng were not seeded.
func TestEngineBuggifyDeterminism(t *testing.T) {
	const seed = int64(9876)
	const trials = 500
	const p = 0.3

	eng1 := NewEngine(seed)
	eng2 := NewEngine(seed)

	for i := 0; i < trials; i++ {
		r1 := eng1.Buggify(p)
		r2 := eng2.Buggify(p)
		if r1 != r2 {
			t.Fatalf("Engine.Buggify divergence at trial %d: eng1=%v eng2=%v (same seed must match)", i, r1, r2)
		}
	}
}

// TestEngineBuggifyRate proves Engine.Buggify(0.3) produces a rate within
// [0.28, 0.32] over 100,000 calls — the real probability arithmetic must drive
// this; hardcoded or always-true implementations would fail.
func TestEngineBuggifyRate(t *testing.T) {
	const trials = 100_000
	eng := NewEngine(1111)

	hits := 0
	for i := 0; i < trials; i++ {
		if eng.Buggify(0.3) {
			hits++
		}
	}

	rate := float64(hits) / float64(trials)
	if rate < 0.28 || rate > 0.32 {
		t.Errorf("Engine.Buggify(0.3) rate = %.5f (want 0.28..0.32); hits=%d trials=%d", rate, hits, trials)
	}
}
