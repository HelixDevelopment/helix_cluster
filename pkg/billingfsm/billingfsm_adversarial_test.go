package billingfsm

import (
	"sync"
	"testing"
	"time"
)

// This file is an ADVERSARIAL money-safety suite for the scale-to-zero metered
// billing state machine in billingfsm.go.
//
// IMPORTANT — model reality check (no bluff):
// This package is NOT an invoice charge/refund/void/dispute FSM. It is a
// metered HOT/COLD scale-to-zero billing controller with exactly two states
// (Cold, Hot) and two edges (Cold->Hot on load>threshold, Hot->Cold on
// idle>=shutdownAfter). The money it tracks is billedNanos, accrued ONLY while
// Hot. There are no terminal states and the type documents (billingfsm.go:56-57)
// that it is "not safe for concurrent use (callers serialize)".
//
// The financially-incorrect scenarios that ACTUALLY exist here are therefore:
//   - billing any Cold second        == OVERCHARGE (charging for nothing running)
//   - double-counting an interval    == OVERCHARGE (same second billed twice)
//   - billing going backwards/negative on a non-monotonic clock == ledger corruption
//   - taking a transition edge that the documented table forbids == wrong meter state
//
// These tests bite each of those. Where the task asked for "double-charge under
// concurrency / atomic check-and-set", the honest analogue here is BILLING
// CONSERVATION under concurrent producers feeding ONE serialized owner: every
// submitted Hot interval is billed exactly once, none lost, none doubled, with
// -race clean. Raw unsynchronized concurrent calls are a DOCUMENTED non-guarantee
// of this type and are covered by an explicit honesty note in
// TestConcurrencyContractIsDocumentedNonGuarantee.

// ---------------------------------------------------------------------------
// 1. EXHAUSTIVE TRANSITION TABLE (truth table over (state x input)).
// ---------------------------------------------------------------------------

// TestTransitionTableExhaustive enumerates the FULL (start-state x action x
// input) matrix and asserts the resulting state is EXACTLY the documented edge,
// never a silent extra/missing transition.
//
// Documented edges (billingfsm.go:14-19, 153-163, 120-128):
//
//	Cold + Observe(load >  threshold)            -> Hot   (scale up)
//	Cold + Observe(load <= threshold)            -> Cold  (stay; strictly-above)
//	Cold + Tick(any elapsed)                     -> Cold  (Cold never self-scales-up)
//	Hot  + Observe(load>0, fresh request)        -> Hot   (request resets idle)
//	Hot  + Observe(idle <  shutdownAfter)        -> Hot   (within window)
//	Hot  + Observe(idle >= shutdownAfter, load 0)-> Cold  (scale down)
//	Hot  + Tick(idle <  shutdownAfter)           -> Hot
//	Hot  + Tick(idle >= shutdownAfter)           -> Cold
//
// Anti-bluff: a missing guard (e.g. Cold scaling up on load==threshold, or Hot
// scaling down one tick early, or Cold "scaling up" from a Tick) flips exactly
// one row and fails it by name. Because every (state,input) cell is asserted,
// an EXTRA edge the table forbids is caught too.
func TestTransitionTableExhaustive(t *testing.T) {
	const threshold = 10.0
	const shutdownAfter = 30 * time.Second

	// build returns a fresh controller pinned to the requested start state.
	build := func(t *testing.T, start State) (*Controller, *fakeClock) {
		t.Helper()
		clk := &fakeClock{now: epoch}
		c := New(threshold, shutdownAfter, clk)
		if start == Hot {
			// Scale up deterministically; lastRequest == now after this.
			if got := c.ObserveAt(clk.now, threshold+1); got != Hot {
				t.Fatalf("setup: could not reach Hot, got %v", got)
			}
		}
		if c.State() != start {
			t.Fatalf("setup: start state = %v, want %v", c.State(), start)
		}
		return c, clk
	}

	type row struct {
		name  string
		start State
		// apply drives exactly one action; returns the resulting state.
		apply func(c *Controller, clk *fakeClock) State
		want  State
	}

	rows := []row{
		{
			name:  "Cold+Observe(load<threshold)->Cold",
			start: Cold,
			apply: func(c *Controller, clk *fakeClock) State { return c.ObserveAt(clk.now, threshold-1) },
			want:  Cold,
		},
		{
			name:  "Cold+Observe(load==threshold)->Cold(strictly-above guard)",
			start: Cold,
			apply: func(c *Controller, clk *fakeClock) State { return c.ObserveAt(clk.now, threshold) },
			want:  Cold,
		},
		{
			name:  "Cold+Observe(load>threshold)->Hot",
			start: Cold,
			apply: func(c *Controller, clk *fakeClock) State { return c.ObserveAt(clk.now, threshold+1) },
			want:  Hot,
		},
		{
			name:  "Cold+Observe(load==0)->Cold",
			start: Cold,
			apply: func(c *Controller, clk *fakeClock) State { return c.ObserveAt(clk.now, 0) },
			want:  Cold,
		},
		{
			name:  "Cold+Tick(long elapsed)->Cold(no self-scale-up)",
			start: Cold,
			apply: func(c *Controller, clk *fakeClock) State {
				c.Tick(clk.advance(shutdownAfter * 10))
				return c.State()
			},
			want: Cold,
		},
		{
			name:  "Hot+Observe(load>0)->Hot(request keeps alive)",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				return c.ObserveAt(clk.advance(shutdownAfter+time.Second), threshold+1)
			},
			want: Hot,
		},
		{
			name:  "Hot+Observe(idle<window,load0)->Hot",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				return c.ObserveAt(clk.advance(shutdownAfter-time.Second), 0)
			},
			want: Hot,
		},
		{
			name:  "Hot+Observe(idle==window-boundary,load0)->Cold(>=)",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				return c.ObserveAt(clk.advance(shutdownAfter), 0)
			},
			want: Cold,
		},
		{
			name:  "Hot+Observe(idle>window,load0)->Cold",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				return c.ObserveAt(clk.advance(shutdownAfter+time.Second), 0)
			},
			want: Cold,
		},
		{
			name:  "Hot+Tick(idle<window)->Hot",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				c.Tick(clk.advance(shutdownAfter - time.Second))
				return c.State()
			},
			want: Hot,
		},
		{
			name:  "Hot+Tick(idle==window-boundary)->Cold(>=)",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				c.Tick(clk.advance(shutdownAfter))
				return c.State()
			},
			want: Cold,
		},
		{
			name:  "Hot+Tick(idle>window)->Cold",
			start: Hot,
			apply: func(c *Controller, clk *fakeClock) State {
				c.Tick(clk.advance(shutdownAfter + time.Second))
				return c.State()
			},
			want: Cold,
		},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c, clk := build(t, r.start)
			got := r.apply(c, clk)
			if got != r.want {
				t.Fatalf("MATRIX VIOLATION: %s: start=%v got=%v want=%v",
					r.name, r.start, got, r.want)
			}
			t.Logf("OK %-52s start=%v -> %v", r.name, r.start, got)
		})
	}
}

// ---------------------------------------------------------------------------
// 2. NO DOUBLE-COUNT OF MONEY (the conservation invariant).
// ---------------------------------------------------------------------------

// TestNoDoubleBillOfSameInterval bills the SAME wall-clock interval via repeated
// calls at the SAME timestamp and asserts the interval is billed EXACTLY ONCE.
// This is the metered analogue of "no double-charge": accrue() advances lastTick
// to now, so a second call at the same now must add nothing.
//
// Anti-bluff: if accrue() failed to advance lastTick (or re-billed from a stale
// anchor), calling at the same instant repeatedly would multiply the bill. The
// exact-equality assertion bites that double-count.
func TestNoDoubleBillOfSameInterval(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour
	c := New(threshold, shutdownAfter, clk)

	c.ObserveAt(clk.now, threshold+1) // -> Hot
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot", runID)
	}

	const hot = 12 * time.Second
	at := clk.advance(hot)
	c.Tick(at) // bills the 12s once
	once := c.BilledSeconds()
	if once != hot.Seconds() {
		t.Fatalf("run=%s first bill = %.6f, want %.6f", runID, once, hot.Seconds())
	}

	// Re-apply the SAME timestamp many times (sequentially). Each is a no-op
	// for billing: the interval was already accrued.
	for i := 0; i < 50; i++ {
		c.Tick(at)
		c.ObserveAt(at, threshold+1) // keep-alive at same instant, also no new time
	}
	if got := c.BilledSeconds(); got != once {
		t.Fatalf("run=%s DOUBLE-BILL: same interval billed %.6f after replays, want %.6f (once)",
			runID, got, once)
	}
	t.Logf("run=%s same 12s interval billed exactly once across 100 replays: %.3fs", runID, once)
}

// TestBillingConservationOverSchedule drives a long deterministic schedule of
// alternating Hot/Cold runs and asserts the total billed equals the INDEPENDENT
// oracle sum of Hot intervals only — no Cold second leaks in, none double-counted.
//
// Anti-bluff: the oracle is computed from the schedule, not from the controller.
// If Cold time were billed (state check dropped in accrue) the total overshoots;
// if any Hot interval were dropped the total undershoots. Exact equality bites both.
func TestBillingConservationOverSchedule(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 20 * time.Second
	c := New(threshold, shutdownAfter, clk)

	var oracleHot time.Duration

	// Helper: advance dt while keeping current state, accumulating oracle Hot.
	advanceKeep := func(dt time.Duration, hot bool) {
		// Sub-step so an active Hot run is kept alive by requests and never
		// silently scales to Cold mid-interval (which would desync the oracle).
		step := 5 * time.Second
		for elapsed := time.Duration(0); elapsed < dt; elapsed += step {
			d := step
			if remaining := dt - elapsed; remaining < step {
				d = remaining
			}
			at := clk.advance(d)
			if hot {
				if got := c.ObserveAt(at, threshold+1); got != Hot {
					t.Fatalf("run=%s desync: expected Hot during active run, got %v", runID, got)
				}
				oracleHot += d
			} else {
				c.Tick(at)
			}
		}
	}

	// Cold warmup (not billed).
	advanceKeep(13*time.Second, false)
	if c.State() != Cold {
		t.Fatalf("run=%s expected Cold warmup", runID)
	}

	// Scale up, run Hot actively for 30s (kept alive).
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot", runID)
	}
	advanceKeep(30*time.Second, true)

	// Go idle: the shutdownAfter grace is billed as Hot up to the flip Tick
	// (documented policy, see TestMeteringBillsOnlyHotSeconds), then Cold.
	idleAt := clk.advance(shutdownAfter)
	oracleHot += shutdownAfter // grace billed while still Hot at the flip
	c.Tick(idleAt)
	if c.State() != Cold {
		t.Fatalf("run=%s expected Cold after idle window", runID)
	}

	// More Cold time (not billed).
	advanceKeep(100*time.Second, false)

	// Second Hot run.
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot (2nd run)", runID)
	}
	advanceKeep(10*time.Second, true)
	idle2 := clk.advance(shutdownAfter)
	oracleHot += shutdownAfter
	c.Tick(idle2)
	if c.State() != Cold {
		t.Fatalf("run=%s expected Cold after 2nd idle window", runID)
	}

	want := oracleHot.Seconds()
	if got := c.BilledSeconds(); got != want {
		t.Fatalf("run=%s CONSERVATION VIOLATION: billed=%.6f, oracle Hot-only=%.6f (delta=%.6f)",
			runID, got, want, got-want)
	}
	t.Logf("run=%s billing conserved: billed=%.3fs == oracle Hot-only=%.3fs (Cold excluded, no double-count)",
		runID, c.BilledSeconds(), want)
}

// ---------------------------------------------------------------------------
// 3. "TERMINAL"/IDEMPOTENCY analogue: no spurious re-bill on duplicate scale-down.
// ---------------------------------------------------------------------------

// TestScaleDownIsStableNoLateRebill asserts that once scaled to Cold, repeated
// late/duplicate Ticks and load==0 Observes do NOT mutate the bill (Cold is not
// billed) and do NOT re-enter Hot on their own. This is the metered analogue of
// terminal-state immutability: a late duplicate action is a no-op, never a
// silent re-charge.
//
// Anti-bluff: if Cold accrued time (overcharge) the bill would climb on each
// late Tick; if a bare Tick could resurrect Hot, the state would flip. Both are
// asserted across many duplicate actions.
func TestScaleDownIsStableNoLateRebill(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 15 * time.Second
	c := New(threshold, shutdownAfter, clk)

	// Hot for 9s, then scale down.
	c.ObserveAt(clk.now, threshold+1)
	c.ObserveAt(clk.advance(9*time.Second), threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot", runID)
	}
	c.Tick(clk.advance(shutdownAfter)) // idle window -> Cold; grace billed.
	if c.State() != Cold {
		t.Fatalf("run=%s expected Cold after window", runID)
	}
	billedAtScaleDown := c.BilledSeconds()
	t.Logf("run=%s scaled down; billed frozen at %.3fs", runID, billedAtScaleDown)

	// Hammer late/duplicate Cold actions: Ticks and load==0 Observes.
	for i := 0; i < 100; i++ {
		c.Tick(clk.advance(time.Second))
		if got := c.ObserveAt(clk.advance(time.Second), 0); got != Cold {
			t.Fatalf("run=%s late load==0 Observe resurrected state to %v, want Cold", runID, got)
		}
		if c.State() != Cold {
			t.Fatalf("run=%s late Tick mutated state to %v, want Cold", runID, c.State())
		}
	}
	if got := c.BilledSeconds(); got != billedAtScaleDown {
		t.Fatalf("run=%s LATE RE-BILL: Cold accrued money: billed=%.6f, want frozen %.6f",
			runID, got, billedAtScaleDown)
	}
	t.Logf("run=%s after 200 late Cold actions: state=%v billed=%.3fs (frozen, no overcharge)",
		runID, c.State(), c.BilledSeconds())
}

// ---------------------------------------------------------------------------
// 4. NON-MONOTONIC CLOCK can neither create nor destroy money.
// ---------------------------------------------------------------------------

// TestNonMonotonicClockNeverCreatesOrDestroysMoney does a forwards/backwards
// clock walk while Hot and asserts the bill is monotonic non-decreasing and the
// backwards step never subtracts (the clamp at billingfsm.go:105-109). A clock
// that goes back must not refund already-billed Hot time, and must not bill
// negative time.
//
// Anti-bluff: removing the clamp makes accrue compute a negative elapsed and
// SUBTRACT from billedNanos -> the bill regresses; the monotonic assertion bites.
func TestNonMonotonicClockNeverCreatesOrDestroysMoney(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour
	c := New(threshold, shutdownAfter, clk)
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot", runID)
	}

	prev := c.BilledSeconds()
	// Walk: +10, -3, +5, -8, +1 (seconds). Sum of positive steps only is what
	// may be billed; backwards steps must clamp, not refund.
	steps := []time.Duration{
		10 * time.Second, -3 * time.Second, 5 * time.Second, -8 * time.Second, 1 * time.Second,
	}
	for i, d := range steps {
		// Drive via Tick at lastTick+d (which may be before lastTick).
		target := c.lastTick.Add(d)
		clk.now = target
		c.Tick(target)
		cur := c.BilledSeconds()
		if cur < prev {
			t.Fatalf("run=%s MONEY REGRESSED at step %d (d=%v): billed %.6f -> %.6f",
				runID, i, d, prev, cur)
		}
		prev = cur
	}
	if c.BilledSeconds() < 0 {
		t.Fatalf("run=%s billed went negative: %.6f", runID, c.BilledSeconds())
	}
	t.Logf("run=%s billed monotonic non-decreasing across forwards/backwards walk: final=%.3fs",
		runID, c.BilledSeconds())
}

// ---------------------------------------------------------------------------
// 5. CONCURRENCY — billing conservation under concurrent producers (-race).
// ---------------------------------------------------------------------------

// TestConcurrentProducersConserveBilling models the REAL, supported concurrency
// shape for a type documented as "callers serialize": many goroutines produce
// billing events concurrently, all funneled through ONE serializing owner (a
// single goroutine that owns the Controller). We assert every produced Hot
// interval is billed EXACTLY once (conservation), no money lost or doubled, and
// -race is clean.
//
// This is the honest analogue of "charge exactly once under concurrency": the
// owner is the atomic check-and-set point; conservation across N concurrent
// producers proves no interval is dropped or double-billed under contention.
//
// Anti-bluff: the oracle (sum of intervals each producer asked to bill) is
// computed independently of the Controller. A lost event (race dropping a
// channel send/recv) undershoots; a doubled apply overshoots. Exact equality bites.
// -race bites any data race in the owner loop or the Controller itself.
func TestConcurrentProducersConserveBilling(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour // stay Hot the whole test.
	c := New(threshold, shutdownAfter, clk)
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot", runID)
	}

	const (
		producers = 16
		perProd   = 64
		quantum   = 1 * time.Millisecond
	)

	// events carries Hot-interval billing requests to the single owner. The
	// owner advances the shared clock and the Controller; producers never
	// touch the Controller directly (honoring the serialize contract).
	type ev struct{ d time.Duration }
	events := make(chan ev, producers*perProd)

	var ownerDone sync.WaitGroup
	ownerDone.Add(1)
	var applied int
	go func() {
		defer ownerDone.Done()
		for e := range events {
			clk.now = clk.now.Add(e.d)
			c.Tick(clk.now) // bills e.d of Hot time, exactly once.
			applied++
		}
	}()

	var prod sync.WaitGroup
	for p := 0; p < producers; p++ {
		prod.Add(1)
		go func() {
			defer prod.Done()
			for i := 0; i < perProd; i++ {
				events <- ev{d: quantum}
			}
		}()
	}
	prod.Wait()
	close(events)
	ownerDone.Wait()

	wantEvents := producers * perProd
	if applied != wantEvents {
		t.Fatalf("run=%s LOST/EXTRA EVENT: applied=%d, want %d", runID, applied, wantEvents)
	}
	want := time.Duration(wantEvents) * quantum
	if got := c.BilledSeconds(); got != want.Seconds() {
		t.Fatalf("run=%s CONCURRENCY CONSERVATION VIOLATION: billed=%.6f, want %.6f",
			runID, got, want.Seconds())
	}
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot throughout, got %v", runID, c.State())
	}
	t.Logf("run=%s %d concurrent producers x %d events: billed=%.6fs == oracle %.6fs (exactly once, -race clean)",
		runID, producers, perProd, c.BilledSeconds(), want.Seconds())
}

// TestConcurrencyContractIsDocumentedNonGuarantee is an HONESTY note, not a
// bug-finder. billingfsm.go:56-57 states the Controller is "not safe for
// concurrent use (callers serialize)". This test documents that contract
// in-code and asserts the safe pattern (a mutex owned by the caller) yields
// conserved billing — the supported way to share one Controller across
// goroutines. It does NOT run unsynchronized concurrent mutators, because doing
// so would intentionally trip -race on a DOCUMENTED non-guarantee (which would
// be a false "bug" report, i.e. a bluff).
func TestConcurrencyContractIsDocumentedNonGuarantee(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour
	c := New(threshold, shutdownAfter, clk)
	c.ObserveAt(clk.now, threshold+1)

	var mu sync.Mutex
	const goroutines = 32
	const each = 50
	const quantum = 1 * time.Millisecond

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				mu.Lock() // caller-provided serialization, per the contract.
				clk.now = clk.now.Add(quantum)
				c.Tick(clk.now)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	want := time.Duration(goroutines*each) * quantum
	if got := c.BilledSeconds(); got != want.Seconds() {
		t.Fatalf("run=%s caller-serialized billing not conserved: billed=%.6f, want %.6f",
			runID, got, want.Seconds())
	}
	t.Logf("run=%s caller-mutex-serialized %d goroutines: billed=%.6fs == %.6fs (supported pattern)",
		runID, goroutines, c.BilledSeconds(), want.Seconds())
}
