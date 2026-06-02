package billingfsm

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

// fakeClock is a deterministic, manually advanced clock. It NEVER reads the
// host wall clock, so all tests are reproducible.
type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time { return f.now }
func (f *fakeClock) advance(d time.Duration) time.Time {
	f.now = f.now.Add(d)
	return f.now
}

func newRunUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

var epoch = time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

// TestHysteresisColdHotCold drives load above the scaling threshold to scale
// Cold->Hot, then idles past ShutdownAfter to scale Hot->Cold.
//
// Mutation: if Cold->Hot used `>=` vs `>` or never transitioned, the Hot
// assertion fails; if Hot->Cold never fired (idle check removed/inverted),
// the final Cold assertion fails.
func TestHysteresisColdHotCold(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 30 * time.Second
	c := New(threshold, shutdownAfter, clk)

	t.Logf("run=%s BEFORE state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())

	if got := c.State(); got != Cold {
		t.Fatalf("run=%s initial state = %v, want Cold", runID, got)
	}

	// Load at the threshold must NOT scale up (strictly-above hysteresis).
	if got := c.ObserveAt(clk.now, threshold); got != Cold {
		t.Fatalf("run=%s load==threshold scaled to %v, want stay Cold", runID, got)
	}

	// Load above threshold scales Cold->Hot.
	upAt := clk.advance(1 * time.Second)
	if got := c.ObserveAt(upAt, threshold+5); got != Hot {
		t.Fatalf("run=%s load>threshold gave %v, want Hot", runID, got)
	}
	t.Logf("run=%s Cold->Hot captured at +%s", runID, upAt.Sub(epoch))

	// Just before ShutdownAfter elapses, still Hot.
	nearAt := clk.advance(shutdownAfter - time.Second)
	c.Tick(nearAt)
	if got := c.State(); got != Hot {
		t.Fatalf("run=%s before shutdown window state = %v, want Hot", runID, got)
	}

	// Crossing ShutdownAfter (idle, no requests) scales Hot->Cold.
	downAt := clk.advance(2 * time.Second)
	c.Tick(downAt)
	if got := c.State(); got != Cold {
		t.Fatalf("run=%s after idle window state = %v, want Cold", runID, got)
	}
	t.Logf("run=%s Hot->Cold captured at +%s AFTER state=%v billed=%.3fs",
		runID, downAt.Sub(epoch), c.State(), c.BilledSeconds())
}

// TestMeteringBillsOnlyHotSeconds asserts BilledSeconds accrues ONLY while
// Hot. The expected total is DERIVED from the hot duration of the schedule;
// Cold time before scale-up and after scale-down must contribute nothing.
//
// Mutation: if accrue billed Cold seconds too (state check removed), the
// billed total would exceed the derived hot duration and this fails.
func TestMeteringBillsOnlyHotSeconds(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 20 * time.Second
	c := New(threshold, shutdownAfter, clk)

	t.Logf("run=%s BEFORE state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())

	// 7 Cold seconds: not billed.
	const coldBefore = 7 * time.Second
	c.Tick(clk.advance(coldBefore))
	if c.State() != Cold {
		t.Fatalf("run=%s expected still Cold during warmup", runID)
	}
	if bs := c.BilledSeconds(); bs != 0 {
		t.Fatalf("run=%s billed during Cold warmup = %.6f, want 0", runID, bs)
	}

	// Scale up.
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot after load>threshold", runID)
	}

	// Run Hot for a known duration, keeping it alive with requests so it
	// does not scale to zero mid-run. We issue a request every 5s for a
	// total of 15s of Hot time.
	hotStart := clk.now
	for i := 0; i < 3; i++ {
		c.ObserveAt(clk.advance(5*time.Second), threshold+1)
		if c.State() != Hot {
			t.Fatalf("run=%s scaled away during active Hot run at step %d", runID, i)
		}
	}
	hotDuration := clk.now.Sub(hotStart) // 15s, derived from the schedule.

	// Now go idle until it scales to Cold. The accrual up to the moment of
	// the scale-down Tick adds shutdownAfter of Hot time before flipping.
	idleTick := clk.advance(shutdownAfter)
	c.Tick(idleTick)
	if c.State() != Cold {
		t.Fatalf("run=%s expected Cold after idle window", runID)
	}
	// Hot time billed = active hotDuration + the idle wait that elapsed
	// while still Hot (billed at the Tick that flips the state).
	wantHot := hotDuration + shutdownAfter

	// Add more Cold time afterwards: must NOT be billed.
	c.Tick(clk.advance(100 * time.Second))
	if c.State() != Cold {
		t.Fatalf("run=%s expected to remain Cold", runID)
	}

	wantSeconds := wantHot.Seconds()
	if got := c.BilledSeconds(); got != wantSeconds {
		t.Fatalf("run=%s BilledSeconds = %.6f, want %.6f (derived hot only)",
			runID, got, wantSeconds)
	}
	t.Logf("run=%s AFTER state=%v billed=%.3fs wantHot=%.3fs (cold time excluded)",
		runID, c.State(), c.BilledSeconds(), wantSeconds)
}

// TestRequestResetsShutdownTimer proves that a request issued inside the idle
// window resets the shutdown timer and delays scale-to-zero. With the same
// clock schedule, the only difference between staying Hot and scaling Cold is
// whether a request landed mid-window.
//
// Mutation: if Observe did not reset lastRequest on a request (the reset
// removed), the workload would scale to Cold despite the mid-window request
// and the "still Hot" assertion fails.
func TestRequestResetsShutdownTimer(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 30 * time.Second
	c := New(threshold, shutdownAfter, clk)

	// Scale up to Hot.
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot after scale-up", runID)
	}
	t.Logf("run=%s BEFORE state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())

	// Advance to 20s idle (< 30s window): still Hot.
	c.Tick(clk.advance(20 * time.Second))
	if c.State() != Hot {
		t.Fatalf("run=%s prematurely scaled at 20s idle", runID)
	}

	// A keep-alive REQUEST at t=20s resets the idle timer.
	c.ObserveAt(clk.now, threshold+1)

	// Advance another 20s (cumulative 40s since scale-up, but only 20s
	// since the request): without the reset this would exceed the 30s
	// window and scale to Cold. With the reset, it must stay Hot.
	c.Tick(clk.advance(20 * time.Second))
	if got := c.State(); got != Hot {
		t.Fatalf("run=%s request did not reset shutdown timer: state = %v, want Hot", runID, got)
	}
	t.Logf("run=%s after mid-window request, still Hot at +%s", runID, clk.now.Sub(epoch))

	// Now stop issuing requests; after a full ShutdownAfter of idle it
	// finally scales to Cold (proves the timer still works after reset).
	c.Tick(clk.advance(shutdownAfter))
	if got := c.State(); got != Cold {
		t.Fatalf("run=%s expected Cold after sustained idle post-reset, got %v", runID, got)
	}
	t.Logf("run=%s AFTER state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())
}

// TestObserveAtScalesDownWhenIdle drives scale-down through ObserveAt (the Hot
// idle-shutdown branch at billingfsm.go:158-163, distinct from Tick's branch).
// While Hot, we call ObserveAt with load==0 once the idle window has elapsed:
// load==0 is NOT request activity, so lastRequest is not reset and the Hot
// workload must scale to Cold inside ObserveAt itself.
//
// Mutation: if the Hot-case idle check in ObserveAt is removed/disabled (e.g.
// the `if idle >= c.shutdownAfter` branch gated out), state stays Hot and the
// final Cold assertion fails. This branch is otherwise dead w.r.t. the suite.
func TestObserveAtScalesDownWhenIdle(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 30 * time.Second
	c := New(threshold, shutdownAfter, clk)

	// Scale up to Hot via ObserveAt.
	if got := c.ObserveAt(clk.now, threshold+1); got != Hot {
		t.Fatalf("run=%s expected Hot after scale-up, got %v", runID, got)
	}
	t.Logf("run=%s BEFORE state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())

	// One step short of the window via ObserveAt with load==0 (no request
	// reset): must still be Hot, proving the boundary is strictly >=.
	nearAt := clk.advance(shutdownAfter - time.Second)
	if got := c.ObserveAt(nearAt, 0); got != Hot {
		t.Fatalf("run=%s ObserveAt(load=0) before window scaled to %v, want Hot", runID, got)
	}

	// Crossing the idle window via ObserveAt with load==0 scales Hot->Cold
	// inside ObserveAt (NOT via Tick). lastRequest was never reset because
	// load was 0, so idle = now - scaleUpTime >= shutdownAfter.
	downAt := clk.advance(2 * time.Second)
	if got := c.ObserveAt(downAt, 0); got != Cold {
		t.Fatalf("run=%s ObserveAt(load=0) past idle window state = %v, want Cold", runID, got)
	}
	t.Logf("run=%s AFTER state=%v billed=%.3fs scaled down via ObserveAt at +%s",
		runID, c.State(), c.BilledSeconds(), downAt.Sub(epoch))
}

// TestAccrueClampsNonMonotonicClock feeds a `now` that is BEFORE lastTick while
// Hot and asserts billing does not regress: billedNanos must not go negative
// and must not change on the backwards step (the clamp at billingfsm.go:105-108
// resets lastTick and returns without accruing).
//
// Mutation: if the `if now.Before(c.lastTick) { ...; return }` clamp is removed,
// accrue computes elapsed = (earlier - later) < 0 and SUBTRACTS from billedNanos,
// making the post-clamp BilledSeconds smaller than the pre-clamp value and
// failing the no-regression assertion below.
func TestAccrueClampsNonMonotonicClock(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour // large: keep Hot for the whole test.
	c := New(threshold, shutdownAfter, clk)

	// Scale up and bill a known amount of Hot time forward.
	c.ObserveAt(clk.now, threshold+1)
	if c.State() != Hot {
		t.Fatalf("run=%s expected Hot after scale-up", runID)
	}
	const forward = 10 * time.Second
	c.Tick(clk.advance(forward))
	billedBefore := c.BilledSeconds()
	if billedBefore != forward.Seconds() {
		t.Fatalf("run=%s billed forward = %.6f, want %.6f", runID, billedBefore, forward.Seconds())
	}
	t.Logf("run=%s BEFORE backwards-clock state=%v billed=%.3fs", runID, c.State(), billedBefore)

	// Feed a timestamp BEFORE lastTick (clock went backwards by 4s). The
	// clamp must absorb this: no negative accrual, billed unchanged.
	backAt := c.lastTick.Add(-4 * time.Second)
	c.Tick(backAt)
	billedAfter := c.BilledSeconds()
	if billedAfter < 0 {
		t.Fatalf("run=%s billed went negative after backwards clock = %.6f", runID, billedAfter)
	}
	if billedAfter != billedBefore {
		t.Fatalf("run=%s backwards clock changed billed: before=%.6f after=%.6f (clamp removed?)",
			runID, billedBefore, billedAfter)
	}
	t.Logf("run=%s AFTER backwards-clock state=%v billed=%.3fs (clamped, no regression)",
		runID, c.State(), billedAfter)
}

// TestObserveDelegatesToClock proves the public wall-clock entry point
// Observe(load) actually delegates to ObserveAt(clock.Now(), load): driving the
// injected fakeClock forward and calling Observe must accrue Hot time and scale
// exactly as the explicit-timestamp path would.
//
// Mutation: if Observe ignored the clock (e.g. always passed a fixed/zero time
// or did not delegate), the billed Hot seconds derived from the advanced
// fakeClock would not match and the assertion fails.
func TestObserveDelegatesToClock(t *testing.T) {
	runID := newRunUUID(t)
	clk := &fakeClock{now: epoch}
	const threshold = 10.0
	const shutdownAfter = 1 * time.Hour
	c := New(threshold, shutdownAfter, clk)
	t.Logf("run=%s BEFORE state=%v billed=%.3fs", runID, c.State(), c.BilledSeconds())

	// Scale up via the clock-driven Observe (reads clk.Now()).
	if got := c.Observe(threshold + 1); got != Hot {
		t.Fatalf("run=%s Observe scale-up gave %v, want Hot", runID, got)
	}

	// Advance the injected clock, then Observe again: it must read the new
	// Now() and bill the elapsed Hot seconds derived from that advance.
	const step = 8 * time.Second
	clk.advance(step)
	if got := c.Observe(threshold + 1); got != Hot {
		t.Fatalf("run=%s Observe gave %v, want Hot", runID, got)
	}
	want := step.Seconds()
	if billed := c.BilledSeconds(); billed != want {
		t.Fatalf("run=%s Observe did not delegate to clock: billed=%.6f, want %.6f",
			runID, billed, want)
	}
	t.Logf("run=%s AFTER state=%v billed=%.3fs (Observe delegated to clk.Now())",
		runID, c.State(), c.BilledSeconds())
}
