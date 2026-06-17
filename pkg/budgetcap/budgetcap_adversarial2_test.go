package budgetcap

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDAdv2 is the forensic anchor for the SECOND adversarial pass so its
// output is unambiguously attributable. This pass targets parts the first
// adversarial suite did not cover: the cap-CONSTRUCTION numeric-poison vector
// (NaN cap), the +Inf "unlimited" contract, negative-cost crediting headroom
// after a release, accumulated float drift never producing a real over-cap
// admit, and allocate/release conservation under concurrent churn.
const runUUIDAdv2 = "budgetcap-adv2-7c4e91b0-HXC1497"

// ---------------------------------------------------------------------------
// CONSTRUCTION-SIDE NUMERIC POISON: a NaN cap is a TOTAL CAP BYPASS.
// ---------------------------------------------------------------------------

// TestNaNCapMustNotBypassCap is the headline second-pass probe.
//
// THE BUG IT TARGETS (construction-side twin of the NaN-cost poison): the cap
// is stored verbatim from NewBudgetCap. If maxPerHour is NaN, the admission test
//
//	committed+cost > maxPerHour      // i.e.  X > NaN
//
// is false for EVERY charge (all NaN comparisons are false), so allocations of
// ARBITRARY size are admitted without bound. committed climbs to 1e18 with no
// rejection: the cap is fully bypassed and spend is unbounded. A budget cap is a
// hard financial gate; a NaN passed in as the ceiling silently disables it.
//
// CONTRACT (what correct code must do): a NaN ceiling is invalid configuration
// and must be treated fail-closed (clamped to 0, the most restrictive cap), so
// the gate stays enforceable rather than silently disabled.
//
// This test FAILS against un-sanitized construction (documents the real bug) and
// PASSES once NewBudgetCap clamps a NaN cap to 0.
func TestNaNCapMustNotBypassCap(t *testing.T) {
	bc := NewBudgetCap(math.NaN())

	// With a fail-closed (clamped-to-0) cap, even a tiny positive charge must be
	// rejected — proving the NaN ceiling did not become an "admit everything" gate.
	if err := bc.Allocate("huge", 1e18); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] CAP BYPASS — NaN cap admitted a 1e18 charge; got err=%v committed=%v",
			runUUIDAdv2, err, bc.Committed())
	}
	if c := bc.Committed(); c != 0.0 || math.IsNaN(c) {
		t.Fatalf("[%s] committed must stay 0.0 under a clamped NaN cap, got %v", runUUIDAdv2, c)
	}
	// A zero charge is still admissible (0+0 <= 0); the gate is restrictive, not broken.
	if err := bc.Allocate("zero", 0.0); err != nil {
		t.Fatalf("[%s] zero charge under clamped NaN cap must be admitted, got %v", runUUIDAdv2, err)
	}
	t.Logf("[%s] NaN cap is fail-closed (clamped): every positive charge rejected, gate enforceable",
		runUUIDAdv2)
}

// TestInfCapIsLegitimatelyUnlimited pins the +Inf-cap contract so the NaN fix
// does NOT over-reach and clamp +Inf too. A +Inf ceiling means "no budget cap":
// any finite charge is admitted (committed+cost <= +Inf), committed accumulates
// honestly, and committed never becomes NaN. This is a documented, intended
// configuration distinct from the invalid NaN cap.
func TestInfCapIsLegitimatelyUnlimited(t *testing.T) {
	bc := NewBudgetCap(math.Inf(1))
	if err := bc.Allocate("a", 1e15); err != nil {
		t.Fatalf("[%s] +Inf cap must admit a large finite charge, got %v", runUUIDAdv2, err)
	}
	if err := bc.Allocate("b", 9e15); err != nil {
		t.Fatalf("[%s] +Inf cap must admit a second large finite charge, got %v", runUUIDAdv2, err)
	}
	if c := bc.Committed(); math.IsNaN(c) || c != 1e15+9e15 {
		t.Fatalf("[%s] +Inf cap committed must accumulate honestly to %v, got %v", runUUIDAdv2, 1e15+9e15, c)
	}
	// An invalid (non-finite) CHARGE is still rejected even under an unlimited cap.
	if err := bc.Allocate("nan", math.NaN()); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("[%s] NaN charge must be ErrInvalidCost even under +Inf cap, got %v", runUUIDAdv2, err)
	}
}

// ---------------------------------------------------------------------------
// NEGATIVE COST CANNOT CREDIT HEADROOM (a different angle than the basic
// negative-cost guard test): even after a release leaves committed positive, a
// negative charge must be rejected and must NOT subtract from committed to
// fabricate extra headroom that later admits an over-cap allocation.
// ---------------------------------------------------------------------------

func TestNegativeCostCannotCreditHeadroom(t *testing.T) {
	bc := NewBudgetCap(10.0)
	if err := bc.Allocate("a", 8.0); err != nil { // committed = 8
		t.Fatalf("[%s] Allocate(a,8) want nil, got %v", runUUIDAdv2, err)
	}
	// A negative charge must be rejected as invalid and leave committed untouched.
	// If it were (mis)applied as committed += (-3), committed would drop to 5 and
	// fabricate 5 of phantom headroom.
	if err := bc.Allocate("credit", -3.0); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("[%s] negative charge must be ErrInvalidCost, got %v", runUUIDAdv2, err)
	}
	if c := bc.Committed(); c != 8.0 {
		t.Fatalf("[%s] negative charge must not credit headroom: committed want 8.0, got %v", runUUIDAdv2, c)
	}
	// True headroom is 2. A 3 must reject; the negative charge must not have
	// opened phantom room for it.
	if err := bc.Allocate("over", 3.0); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] over-cap charge must reject (no phantom headroom from negative cost), got %v", runUUIDAdv2, err)
	}
	if c := bc.Committed(); c != 8.0 {
		t.Fatalf("[%s] committed must stay 8.0, got %v", runUUIDAdv2, c)
	}
}

// ---------------------------------------------------------------------------
// ACCUMULATED FLOAT DRIFT NEVER PRODUCES A REAL OVER-CAP ADMIT.
// ---------------------------------------------------------------------------

// TestFractionalFillNeverExceedsCap charges many fractional costs that, in exact
// arithmetic, would exactly fill the cap. Because the admission test and the
// running total use the SAME float arithmetic, the recorded committed must never
// strictly exceed the cap no matter how rounding accumulates, and the count of
// admits must not exceed the exact-fill count (no extra unit sneaks in via drift).
func TestFractionalFillNeverExceedsCap(t *testing.T) {
	type tc struct {
		cap  float64
		unit float64
		want int // exact-fill admits in real arithmetic
	}
	cases := []tc{
		{1.0, 0.1, 10},
		{3.0, 0.3, 10},
		{1.0, 0.01, 100},
		{7.0, 0.7, 10},
	}
	for _, c := range cases {
		bc := NewBudgetCap(c.cap)
		admitted := 0
		// Attempt well beyond the exact-fill count; drift must never admit extra.
		for i := 0; i < c.want+50; i++ {
			if err := bc.Allocate(itoa(i), c.unit); err == nil {
				admitted++
			}
		}
		if got := bc.Committed(); got > c.cap {
			t.Fatalf("[%s] OVER-CAP via drift: cap=%v unit=%v committed=%v > cap",
				runUUIDAdv2, c.cap, c.unit, got)
		}
		if admitted > c.want {
			t.Fatalf("[%s] drift admitted EXTRA: cap=%v unit=%v admitted=%d > exact-fill %d",
				runUUIDAdv2, c.cap, c.unit, admitted, c.want)
		}
	}
	t.Logf("[%s] fractional-fill drift: committed never exceeded cap; no extra admit across %d cases",
		runUUIDAdv2, len(cases))
}

// TestRepeatedAllocReleaseConservesToZero pins that a long alloc/release cycle of
// the SAME charge returns committed to exactly 0 (the subtract in Release exactly
// undoes the add in Allocate for an integral-binary value), so churn does not
// leak phantom committed spend over time.
func TestRepeatedAllocReleaseConservesToZero(t *testing.T) {
	bc := NewBudgetCap(1.0)
	const cost = 0.25 // exactly representable in binary float -> no drift expected
	for i := 0; i < 500000; i++ {
		if err := bc.Allocate("cycle", cost); err != nil {
			t.Fatalf("[%s] iter %d Allocate(cycle,%v) want nil, got %v", runUUIDAdv2, i, cost, err)
		}
		bc.Release("cycle")
	}
	if c := bc.Committed(); c != 0.0 {
		t.Fatalf("[%s] after 5e5 alloc/release cycles committed must be exactly 0, got %v (phantom leak)",
			runUUIDAdv2, c)
	}
	if r := bc.Remaining(); r != 1.0 {
		t.Fatalf("[%s] remaining must be the full cap 1.0 after balanced churn, got %v", runUUIDAdv2, r)
	}
}

// ---------------------------------------------------------------------------
// CONCURRENT ALLOCATE/RELEASE CHURN CONSERVATION (run under -race).
// Distinct from the existing churn test: here EVERY admitted allocation is later
// released by the SAME goroutine that admitted it, so on a fully-quiesced limiter
// committed MUST settle to exactly 0 and the cap must never have been exceeded.
// A lost update in the committed read/modify/write would leave a nonzero residue.
// ---------------------------------------------------------------------------

func TestConcurrentBalancedChurnSettlesToZero(t *testing.T) {
	const cap = 100.0
	const goroutines = 800
	const rounds = 20
	amounts := []float64{1, 2, 4, 5, 0.5, 0.25}

	for round := 0; round < rounds; round++ {
		bc := NewBudgetCap(cap)
		var maxSeen int64 // committed*1e6 high-water, atomic
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				cost := amounts[n%len(amounts)]
				id := "bc-" + itoa(round) + "-" + itoa(n)
				if err := bc.Allocate(id, cost); err == nil {
					// Record a committed high-water sample, then release.
					c := bc.Committed()
					for {
						cur := atomic.LoadInt64(&maxSeen)
						nv := int64(c * 1e6)
						if nv <= cur || atomic.CompareAndSwapInt64(&maxSeen, cur, nv) {
							break
						}
					}
					bc.Release(id)
				}
			}(i)
		}
		wg.Wait()

		// Every admitted allocation was released by its own goroutine; the
		// quiesced limiter must hold exactly zero committed spend.
		if c := bc.Committed(); c != 0.0 {
			t.Fatalf("[%s] round %d: balanced churn left committed=%v, want exactly 0 (lost update / non-conservation)",
				runUUIDAdv2, round, c)
		}
		if hw := float64(atomic.LoadInt64(&maxSeen)) / 1e6; hw > cap+1e-9 {
			t.Fatalf("[%s] round %d: committed high-water %v exceeded cap %v during churn",
				runUUIDAdv2, round, hw, cap)
		}
	}
	t.Logf("[%s] balanced concurrent churn: %d rounds x %d goroutines — committed settled to 0, cap never exceeded",
		runUUIDAdv2, rounds, goroutines)
}
