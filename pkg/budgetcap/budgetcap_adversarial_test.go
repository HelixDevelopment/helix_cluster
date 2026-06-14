package budgetcap

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDAdv is the forensic anchor for this adversarial audit suite so its
// output is unambiguously attributable.
const runUUIDAdv = "budgetcap-adv-3d61f0aa-HXC1497"

// ---------------------------------------------------------------------------
// CENTERPIECE 1: NO OVER-SPEND — cumulative spend may never exceed the cap,
// and the inclusive-edge boundary is pinned exactly.
// ---------------------------------------------------------------------------

// TestExactBoundaryInclusiveEdge pins the admission edge: a charge that brings
// committed to EXACTLY the cap is admitted (<= cap), while the next positive
// epsilon is rejected and leaves committed untouched. This catches an off-by-one
// flip of `committed+cost > cap` to `>=` (which would reject the legal exact-fill)
// or to `committed+cost >= cap+eps` (which would admit one unit over).
func TestExactBoundaryInclusiveEdge(t *testing.T) {
	bc := NewBudgetCap(10.0)

	// Fill exactly to the cap in two steps: 6 + 4 = 10 (== cap, must be admitted).
	if err := bc.Allocate("a", 6.0); err != nil {
		t.Fatalf("[%s] Allocate(a,6) want nil, got %v", runUUIDAdv, err)
	}
	if err := bc.Allocate("b", 4.0); err != nil {
		t.Fatalf("[%s] exact-fill Allocate(b,4) to cap=10 must be admitted (<=), got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 10.0 {
		t.Fatalf("[%s] committed at exact cap want 10.0, got %v", runUUIDAdv, c)
	}
	if r := bc.Remaining(); r != 0.0 {
		t.Fatalf("[%s] remaining at exact cap want 0.0, got %v", runUUIDAdv, r)
	}

	// The smallest *representable* overshoot above the cap must be rejected and
	// must not mutate state. We use the ULP at 10.0 (the gap to the next float64
	// above 10.0) so that 10.0+eps is genuinely > 10.0 in float64 arithmetic — a
	// denormal-sized epsilon would round back to 10.0 and is not a real overshoot.
	eps := math.Nextafter(10.0, math.Inf(1)) - 10.0
	if err := bc.Allocate("c", eps); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] cap+epsilon must be rejected with ErrWouldExceedBudget, got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 10.0 {
		t.Fatalf("[%s] committed must stay 10.0 after epsilon rejection, got %v", runUUIDAdv, c)
	}

	// A zero-cost charge at a full cap is still admissible (committed+0 <= cap).
	if err := bc.Allocate("zero", 0.0); err != nil {
		t.Fatalf("[%s] zero-cost charge at full cap must be admitted (==cap), got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 10.0 {
		t.Fatalf("[%s] zero-cost charge must not change committed, want 10.0 got %v", runUUIDAdv, c)
	}
}

// TestNoOverSpendAcrossArbitrarySequence hammers a deterministic but irregular
// sequence of allocate/release calls and, after EVERY operation, asserts the
// global safety invariant: committed never exceeds the cap and never goes
// negative, and committed always equals the sum of currently-active allocations
// (independently recomputed). This is the "cumulative spend never exceeds cap
// across any sequence" contract, checked at every step rather than just the end.
func TestNoOverSpendAcrossArbitrarySequence(t *testing.T) {
	const cap = 100.0
	bc := NewBudgetCap(cap)

	// Shadow ledger the test maintains independently of the limiter.
	active := make(map[string]float64)

	sumActive := func() float64 {
		s := 0.0
		for _, v := range active {
			s += v
		}
		return s
	}

	type op struct {
		alloc bool
		id    string
		cost  float64
	}
	// An irregular workload: costs that do not evenly divide the cap, repeated
	// allocate attempts on full/near-full state, and interleaved releases.
	ops := []op{
		{true, "x1", 40}, {true, "x2", 35}, {true, "x3", 30}, // x3 would be 105 -> reject
		{true, "x4", 25},                   // 75+25=100 exact -> admit
		{true, "x5", 0.1},                  // 100.1 -> reject
		{false, "x2", 0},                   // release 35 -> committed 65
		{true, "x3", 30},                   // 95 -> admit
		{true, "x6", 6},                    // 101 -> reject
		{true, "x6", 5},                    // 100 exact -> admit
		{false, "x1", 0}, {false, "x4", 0}, // release 40+25 -> committed 35
		{true, "x7", 64.9}, // 99.9 -> admit
		{true, "x8", 0.2},  // 100.1 -> reject
	}

	for i, o := range ops {
		if o.alloc {
			err := bc.Allocate(o.id, o.cost)
			// Predict admission from the shadow ledger.
			_, dup := active[o.id]
			wouldExceed := sumActive()+o.cost > cap
			switch {
			case dup:
				if !errors.Is(err, ErrDuplicateAllocation) {
					t.Fatalf("[%s] op %d Allocate(%s) dup want ErrDuplicateAllocation, got %v", runUUIDAdv, i, o.id, err)
				}
			case wouldExceed:
				if !errors.Is(err, ErrWouldExceedBudget) {
					t.Fatalf("[%s] op %d Allocate(%s,%v) want reject (sum %v + %v > %v), got %v",
						runUUIDAdv, i, o.id, o.cost, sumActive(), o.cost, cap, err)
				}
			default:
				if err != nil {
					t.Fatalf("[%s] op %d Allocate(%s,%v) want admit, got %v", runUUIDAdv, i, o.id, o.cost, err)
				}
				active[o.id] = o.cost
			}
		} else {
			bc.Release(o.id)
			delete(active, o.id)
		}

		// Invariants checked after EVERY operation.
		c := bc.Committed()
		if c > cap {
			t.Fatalf("[%s] op %d: OVER-SPEND committed=%v > cap=%v", runUUIDAdv, i, c, cap)
		}
		if c < 0 {
			t.Fatalf("[%s] op %d: committed went negative=%v", runUUIDAdv, i, c)
		}
		if want := sumActive(); !floatEq(c, want) {
			t.Fatalf("[%s] op %d: ledger mismatch committed=%v want sum(active)=%v", runUUIDAdv, i, c, want)
		}
	}
	t.Logf("[%s] arbitrary alloc/release sequence: %d ops, committed tracked sum(active) exactly, never exceeded cap=%v",
		runUUIDAdv, len(ops), cap)
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: CONCURRENCY — sub-unit charges from a swarm of goroutines
// against a tight cap can never over-admit. This complements the existing
// integral/unit-cost concurrency tests with fractional sub-cap costs whose
// over-admission would surface as an impossible fractional overshoot.
// ---------------------------------------------------------------------------

// TestConcurrentSubUnitChargesNeverOverSpend uses tiny fractional charges so the
// cap is reached only after MANY successful admissions, widening the window for a
// check-then-add TOCTOU race. If the admission check and commit were not atomic
// under one lock, two goroutines could both observe headroom near the boundary
// and both commit, pushing committed past the cap. Looped to expose probabilistic
// races; run under -race for the data-race signal as well.
func TestConcurrentSubUnitChargesNeverOverSpend(t *testing.T) {
	const cap = 50.0
	const unit = 0.25 // 200 admissions exactly fill the cap
	const goroutines = 1000
	const rounds = 30
	expectedAdmits := int(cap / unit) // 200

	for round := 0; round < rounds; round++ {
		bc := NewBudgetCap(cap)
		var admitted int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				if err := bc.Allocate("s-"+itoa(round)+"-"+itoa(n), unit); err == nil {
					atomic.AddInt64(&admitted, 1)
				}
			}(i)
		}
		wg.Wait()

		got := int(atomic.LoadInt64(&admitted))
		committed := bc.Committed()

		if committed > cap {
			t.Fatalf("[%s] round %d: OVER-SPEND committed=%v > cap=%v", runUUIDAdv, round, committed, cap)
		}
		if got != expectedAdmits {
			t.Fatalf("[%s] round %d: want exactly %d sub-unit admits, got %d (over/under-spend race)",
				runUUIDAdv, round, expectedAdmits, got)
		}
		if !floatEq(committed, float64(expectedAdmits)*unit) {
			t.Fatalf("[%s] round %d: committed=%v != admits*unit=%v", runUUIDAdv, round, committed, float64(expectedAdmits)*unit)
		}
	}
	t.Logf("[%s] sub-unit concurrency: %d rounds x %d goroutines, cap=%v unit=%v — exactly %d admitted each round",
		runUUIDAdv, rounds, goroutines, cap, unit, expectedAdmits)
}

// ---------------------------------------------------------------------------
// REFUND / RELEASE semantics: a release frees exactly its cost; a double-release
// of the same id must NOT inflate budget (free its cost twice).
// ---------------------------------------------------------------------------

// TestDoubleReleaseDoesNotInflateBudget pins the release contract: Release is
// idempotent. After admitting two allocations and releasing one of them TWICE,
// committed must reflect only a single subtraction; the cap headroom must not be
// inflated, and a subsequent allocation must still be admitted only within the
// true headroom — never beyond cap.
func TestDoubleReleaseDoesNotInflateBudget(t *testing.T) {
	bc := NewBudgetCap(10.0)
	if err := bc.Allocate("p", 6.0); err != nil {
		t.Fatalf("[%s] Allocate(p,6) want nil, got %v", runUUIDAdv, err)
	}
	if err := bc.Allocate("q", 4.0); err != nil { // committed = 10 (exact cap)
		t.Fatalf("[%s] Allocate(q,4) want nil, got %v", runUUIDAdv, err)
	}

	bc.Release("p") // committed 10 -> 4
	if c := bc.Committed(); c != 4.0 {
		t.Fatalf("[%s] after Release(p) want committed 4.0, got %v", runUUIDAdv, c)
	}
	// Second release of the same id must be a no-op (idempotent), NOT a second
	// subtraction that would push committed to -2 and fabricate 12 of headroom.
	bc.Release("p")
	if c := bc.Committed(); c != 4.0 {
		t.Fatalf("[%s] double Release(p) must be a no-op: want committed 4.0, got %v (budget inflated)", runUUIDAdv, c)
	}
	if c := bc.Committed(); c < 0 {
		t.Fatalf("[%s] committed must never go negative via double-release, got %v", runUUIDAdv, c)
	}

	// True headroom is 6 (cap 10 - committed 4). A 7 must reject; a 6 must admit.
	if err := bc.Allocate("r", 7.0); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] Allocate(r,7) at committed 4 must reject (would be 11), got %v", runUUIDAdv, err)
	}
	if err := bc.Allocate("r", 6.0); err != nil {
		t.Fatalf("[%s] Allocate(r,6) at committed 4 must admit (==cap), got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 10.0 {
		t.Fatalf("[%s] committed want 10.0 after refilling to cap, got %v", runUUIDAdv, c)
	}
}

// ---------------------------------------------------------------------------
// EDGE: zero cap, and the NaN / non-finite charge contract.
// ---------------------------------------------------------------------------

// TestZeroCapRejectsAnyPositiveCharge pins the degenerate cap=0 case: no positive
// charge may ever be admitted, but a zero charge is admissible (0+0 <= 0).
func TestZeroCapRejectsAnyPositiveCharge(t *testing.T) {
	bc := NewBudgetCap(0.0)
	if err := bc.Allocate("z", math.Nextafter(0, 1)); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] zero cap must reject any positive charge, got %v", runUUIDAdv, err)
	}
	if err := bc.Allocate("z0", 0.0); err != nil {
		t.Fatalf("[%s] zero cap must admit a zero charge, got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 0.0 {
		t.Fatalf("[%s] committed under zero cap want 0.0, got %v", runUUIDAdv, c)
	}
}

// TestNaNChargeMustNotPoisonCap is the headline non-finite probe and a guard
// against a TOTAL CAP BYPASS.
//
// THE BUG IT TARGETS: Allocate only guards `cost < 0`. A NaN cost satisfies
// neither `NaN < 0` nor `committed+NaN > cap` (all NaN comparisons are false),
// so a NaN is ADMITTED and committed becomes NaN permanently. Thereafter every
// admission check `committed+cost > cap` is `NaN > cap` == false, so EVERY
// subsequent allocation — of arbitrary size — is admitted: the cap is fully
// bypassed and spend is unbounded. This is the over-spend safety invariant
// (cumulative spend must never exceed the cap) defeated by one poisoned charge.
//
// CONTRACT (what correct code must do): a non-finite (NaN or +Inf) cost is
// invalid input and MUST be rejected with ErrInvalidCost, leaving committed
// unchanged so the cap remains enforceable.
//
// This test FAILS against the current production code (documenting the real
// bug) and PASSES once Allocate rejects non-finite cost. +Inf is already
// rejected by the `> cap` branch, but the contract is that it be an explicit
// ErrInvalidCost (input validation), not an incidental budget rejection.
func TestNaNChargeMustNotPoisonCap(t *testing.T) {
	bc := NewBudgetCap(10.0)

	// A NaN charge must be rejected as invalid input and must NOT mutate state.
	if err := bc.Allocate("nan", math.NaN()); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("[%s] NaN cost must be rejected with ErrInvalidCost, got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 0.0 || math.IsNaN(c) {
		t.Fatalf("[%s] committed must stay 0.0 (un-poisoned) after NaN rejection, got %v", runUUIDAdv, c)
	}

	// The cap must remain ENFORCEABLE: a wildly over-cap charge is still rejected,
	// proving the NaN did not poison committed into a permanent cap bypass.
	if err := bc.Allocate("huge", 1_000_000.0); !errors.Is(err, ErrWouldExceedBudget) {
		t.Fatalf("[%s] CAP BYPASS — over-cap charge admitted after NaN; got %v (committed=%v)",
			runUUIDAdv, err, bc.Committed())
	}

	// And a legal in-cap charge still works normally.
	if err := bc.Allocate("ok", 5.0); err != nil {
		t.Fatalf("[%s] legal in-cap charge after NaN rejection must admit, got %v", runUUIDAdv, err)
	}
	if c := bc.Committed(); c != 5.0 {
		t.Fatalf("[%s] committed want 5.0 after legal charge, got %v", runUUIDAdv, c)
	}
}

// TestPositiveInfinityChargeRejected pins that a +Inf charge is rejected and does
// not mutate committed. Under the current code it is rejected (as
// ErrWouldExceedBudget); under the recommended fix it becomes ErrInvalidCost.
// Either way it must NOT be admitted and must leave committed unchanged. We
// assert the safety-critical part (rejected + state unchanged) without binding
// to the specific error so this test passes both before and after the fix.
func TestPositiveInfinityChargeRejected(t *testing.T) {
	bc := NewBudgetCap(10.0)
	if err := bc.Allocate("inf", math.Inf(1)); err == nil {
		t.Fatalf("[%s] +Inf cost must be rejected, got nil (committed=%v)", runUUIDAdv, bc.Committed())
	}
	if c := bc.Committed(); c != 0.0 || math.IsNaN(c) {
		t.Fatalf("[%s] committed must stay 0.0 after +Inf rejection, got %v", runUUIDAdv, c)
	}
}
