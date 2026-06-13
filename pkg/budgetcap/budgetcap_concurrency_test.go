package budgetcap

import (
	"sync"
	"sync/atomic"
	"testing"
)

// runUUIDConc is a distinct forensic anchor for the adversarial concurrency
// suite so its output is unambiguously attributable.
const runUUIDConc = "budgetcap-conc-9a14c7e2-HXC1497"

// TestConcurrentNeverExceedsCap_VariedAmounts is the headline adversarial test.
//
// WHAT IT BITES: a check-then-add over-spend (the classic
//
//	if committed+cost <= cap { committed += cost }
//
// TOCTOU where two goroutines both pass the check before either commits). Many
// goroutines concurrently Allocate VARIED amounts whose total demand vastly
// exceeds the cap. We assert three independent invariants:
//
//  1. The recorded `committed` NEVER exceeds the cap at the end.
//  2. The sum of ACTUALLY-ADMITTED costs (independently accumulated by the test,
//     not read from the limiter) never exceeds the cap.
//  3. CONSERVATION: admitted-sum == recorded committed EXACTLY.
//
// In the current implementation the admission check and the `committed += cost`
// mutation are performed inside ONE held mutex critical section, so the
// check-then-add is atomic and cannot over-admit. If that critical section were
// split (e.g. moved to a lock-free atomic check-then-Add), two goroutines could
// both observe headroom and both commit, pushing committed past cap and breaking
// invariants (1) and (3). Run with -race to also catch unguarded shared state.
//
// We loop the whole scenario many times because over-spend races are
// probabilistic; a single pass can get lucky.
func TestConcurrentNeverExceedsCap_VariedAmounts(t *testing.T) {
	const cap = 1000.0
	const rounds = 50
	const goroutines = 256

	// Varied amounts that do NOT all divide the cap evenly, so an over-spend
	// race would surface as a fractional overshoot impossible to reach by any
	// legitimate admitted subset.
	amounts := []float64{1, 2, 3, 5, 7, 11, 13, 17, 23.5, 31.25}

	for round := 0; round < rounds; round++ {
		bc := NewBudgetCap(cap)

		var admittedSum int64 // fixed-point: amount*100 to keep it integral & atomic
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				cost := amounts[n%len(amounts)]
				id := "g-" + itoa(round) + "-" + itoa(n)
				if err := bc.Allocate(id, cost); err == nil {
					atomic.AddInt64(&admittedSum, int64(cost*100))
				}
			}(i)
		}
		wg.Wait()

		committed := bc.Committed()
		admitted := float64(atomic.LoadInt64(&admittedSum)) / 100.0

		// Invariant (1): the limiter's own record must never exceed the cap.
		if committed > cap {
			t.Fatalf("[%s] round %d: OVER-SPEND — committed=%v exceeded cap=%v",
				runUUIDConc, round, committed, cap)
		}
		// Invariant (2): the independently-measured admitted spend must not
		// exceed the cap either.
		if admitted > cap {
			t.Fatalf("[%s] round %d: OVER-SPEND — admitted-sum=%v exceeded cap=%v",
				runUUIDConc, round, admitted, cap)
		}
		// Invariant (3): conservation — every admitted cost is recorded, exactly.
		if !floatEq(admitted, committed) {
			t.Fatalf("[%s] round %d: CONSERVATION VIOLATED — admitted-sum=%v != committed=%v (delta=%v)",
				runUUIDConc, round, admitted, committed, admitted-committed)
		}
	}
	t.Logf("[%s] varied-amounts over-spend test: %d rounds x %d goroutines, cap=%v — committed never exceeded cap, conservation held",
		runUUIDConc, rounds, goroutines, cap)
}

// TestConcurrentExactlyNUnitSpendsSucceed is the cleanest over/under-spend bite.
//
// WHAT IT BITES: with cap=N and unit (cost=1) spends from M>N goroutines,
// EXACTLY N must succeed — not N+1 (over-spend / check-then-add race) and not
// N-1 (spurious under-admission). This is the sharpest signal: a single extra
// admitted unit is a money-critical over-spend; a single missing one is lost
// utilization. Looped many times to expose probabilistic races.
func TestConcurrentExactlyNUnitSpendsSucceed(t *testing.T) {
	const capN = 64
	const rounds = 100
	const goroutines = 512 // M >> N

	for round := 0; round < rounds; round++ {
		bc := NewBudgetCap(float64(capN))

		var admitted int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				id := "u-" + itoa(round) + "-" + itoa(n)
				if err := bc.Allocate(id, 1.0); err == nil {
					atomic.AddInt64(&admitted, 1)
				}
			}(i)
		}
		wg.Wait()

		got := int(atomic.LoadInt64(&admitted))
		committed := bc.Committed()

		if got > capN {
			t.Fatalf("[%s] round %d: OVER-SPEND — %d unit-spends admitted, cap is %d (a check-then-add race admitted extra units)",
				runUUIDConc, round, got, capN)
		}
		if got < capN {
			t.Fatalf("[%s] round %d: UNDER-SPEND — only %d unit-spends admitted, cap is %d (limiter under-admitted available headroom)",
				runUUIDConc, round, got, capN)
		}
		// got == capN exactly; committed must mirror it.
		if !floatEq(committed, float64(capN)) {
			t.Fatalf("[%s] round %d: committed=%v but exactly %d admitted (conservation broken)",
				runUUIDConc, round, committed, capN)
		}
	}
	t.Logf("[%s] exactly-N unit-spends: %d rounds x %d goroutines, cap=%d — EXACTLY %d admitted every round (no over/under-spend)",
		runUUIDConc, rounds, goroutines, capN, capN)
}

// TestConcurrentFullUtilization asserts the limiter is not artificially
// under-admitting: when demand exceeds the cap, admitted spend reaches the cap
// (here exactly, because unit costs divide it). This complements the never-exceed
// tests — a degenerate "always reject" limiter would pass never-exceed but fail
// this. It runs concurrently to prove full utilization holds under contention.
func TestConcurrentFullUtilization(t *testing.T) {
	const cap = 128.0
	const unit = 1.0
	const goroutines = 1000

	bc := NewBudgetCap(cap)
	var admitted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			if err := bc.Allocate("full-"+itoa(n), unit); err == nil {
				atomic.AddInt64(&admitted, 1)
			}
		}(i)
	}
	wg.Wait()

	committed := bc.Committed()
	got := atomic.LoadInt64(&admitted)
	t.Logf("[%s] full-utilization: goroutines=%d cap=%v admitted=%d committed=%v remaining=%v",
		runUUIDConc, goroutines, cap, got, committed, bc.Remaining())

	if committed > cap {
		t.Fatalf("[%s] over-spend: committed=%v > cap=%v", runUUIDConc, committed, cap)
	}
	if committed != cap {
		t.Fatalf("[%s] under-utilization: committed=%v did not reach cap=%v despite demand %d > %v",
			runUUIDConc, committed, cap, goroutines, cap)
	}
}

// TestConcurrentAllocateReleaseConservation exercises Allocate AND Release
// interleaved concurrently, asserting conservation under churn:
//
//	committed == sum(admitted costs) - sum(released costs)
//
// and that committed never goes above cap or below zero at the end. Each worker
// allocates a unique id, and a fraction of workers release their own allocation
// after admission. We independently track admitted and released sums and require
// the limiter's committed to equal their difference EXACTLY. A check-then-add
// over-spend, or a Release that fails to subtract correctly, breaks the equality.
// Run with -race to catch any unsynchronized read/modify/write of committed.
func TestConcurrentAllocateReleaseConservation(t *testing.T) {
	const cap = 500.0
	const rounds = 40
	const goroutines = 300
	amounts := []float64{2, 3, 5, 7, 11}

	for round := 0; round < rounds; round++ {
		bc := NewBudgetCap(cap)

		var admittedSum int64 // amount*100 fixed-point
		var releasedSum int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(n int) {
				defer wg.Done()
				cost := amounts[n%len(amounts)]
				id := "ar-" + itoa(round) + "-" + itoa(n)
				if err := bc.Allocate(id, cost); err == nil {
					atomic.AddInt64(&admittedSum, int64(cost*100))
					// Release roughly every third admitted allocation to create
					// churn that frees headroom for concurrent allocators.
					if n%3 == 0 {
						bc.Release(id)
						atomic.AddInt64(&releasedSum, int64(cost*100))
					}
				}
			}(i)
		}
		wg.Wait()

		committed := bc.Committed()
		admitted := float64(atomic.LoadInt64(&admittedSum)) / 100.0
		released := float64(atomic.LoadInt64(&releasedSum)) / 100.0
		expected := admitted - released

		if committed > cap {
			t.Fatalf("[%s] round %d: OVER-SPEND under churn — committed=%v > cap=%v",
				runUUIDConc, round, committed, cap)
		}
		if committed < -1e-9 {
			t.Fatalf("[%s] round %d: committed went negative=%v (Release over-subtracted)",
				runUUIDConc, round, committed)
		}
		if !floatEq(committed, expected) {
			t.Fatalf("[%s] round %d: CONSERVATION VIOLATED — committed=%v != admitted(%v)-released(%v)=%v",
				runUUIDConc, round, committed, admitted, released, expected)
		}
	}
	t.Logf("[%s] allocate+release conservation: %d rounds x %d goroutines, cap=%v — committed == admitted-released every round, never exceeded cap, never went negative",
		runUUIDConc, rounds, goroutines, cap)
}

// floatEq compares two float64 values within a small epsilon to tolerate
// floating-point summation order differences while still catching real
// over/under-spend (which here are whole-number or >=0.01 discrepancies).
func floatEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
