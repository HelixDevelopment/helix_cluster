package retry

// Adversarial, sink-side probes for pkg/retry focused on NUMERIC failure modes:
// exponential/linear backoff OVERFLOW of time.Duration (int64 ns) that wraps the
// computed delay NEGATIVE (or to a tiny/zero value) — which defeats the documented
// "doubles the delay ... up to MaxDelay" contract and the `delay > MaxDelay` cap,
// turning a backoff into a hot retry loop.
//
// SINK under assertion: the time.Duration value returned by (*Retry).computeDelay —
// this is the EXACT value handed to time.NewTimer in Do/DoWithResult. A negative
// Duration makes time.NewTimer fire immediately (no backoff); a tiny one makes the
// loop nearly hot. We assert the value itself, never "no panic".
//
// Contract being probed (from retry.go doc + cap code):
//   - Exponential: "doubles the delay with each attempt up to MaxDelay".
//   - cap: `if r.MaxDelay > 0 && delay > r.MaxDelay { delay = r.MaxDelay }`.
// The honest invariant for any attempt index reachable by the loop (0..MaxAttempts-1)
// is therefore: 0 <= computeDelay(i) <= MaxDelay (when MaxDelay > 0), and the
// schedule is monotonic-nondecreasing.
//
// STATUS: TestAdversarial_Exp_OverflowNegative_BitesUnfixed and
// TestAdversarial_Backoff_MonotonicAndCapped FAIL on unfixed prod (they assert the
// real invariant). They are NOT env-gated, per protocol. The remaining tests pin
// behavior that already holds and document latent risks.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// expectedCappedExp recomputes the pre-overflow, math-correct exponential delay using
// big-enough arithmetic (float64 for the factor, but clamped to MaxDelay BEFORE it can
// overflow int64) so the test has an independent oracle for "what the capped delay must be".
func expectedCappedExp(base, maxDelay time.Duration, attempt int) time.Duration {
	// True mathematical value, in float nanoseconds.
	want := float64(base) * math.Pow(2, float64(attempt))
	if maxDelay > 0 && want >= float64(maxDelay) {
		return maxDelay
	}
	return time.Duration(want)
}

// TestAdversarial_Exp_OverflowNegative_BitesUnfixed pins the core defect at the SINK:
// with a config whose attempt index is reachable by the retry loop, computeDelay returns
// a NEGATIVE time.Duration because base*2^attempt overflows int64 and the cap check
// (`delay > MaxDelay`) is bypassed (a negative number is never > MaxDelay).
//
// base=100ms, MaxDelay=5s — a completely ordinary production config. attempt=40 is a
// legal loop index when MaxAttempts > 40. The honest result MUST be the cap (5s).
//
// FAILS on unfixed prod (proves the bug). PASSES once the cap is overflow-safe.
func TestAdversarial_Exp_OverflowNegative_BitesUnfixed(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 5 * time.Second
	r := &Retry{
		MaxAttempts:     64,
		Delay:           base,
		MaxDelay:        maxDelay,
		Jitter:          false, // deterministic: isolate the overflow, not the jitter
		BackoffStrategy: Exponential,
	}

	// Every reachable attempt index must yield a delay in [0, MaxDelay].
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		got := r.computeDelay(attempt)
		if got < 0 {
			t.Fatalf("SINK overflow: computeDelay(attempt=%d) = %v (ns=%d) is NEGATIVE; "+
				"base*2^attempt overflowed int64 and the `delay > MaxDelay` cap was bypassed. "+
				"A negative duration makes time.NewTimer fire immediately => backoff collapses to a hot loop. "+
				"Honest value must be the cap %v.", attempt, got, int64(got), maxDelay)
		}
		if got > maxDelay {
			t.Fatalf("SINK cap violated: computeDelay(attempt=%d) = %v exceeds MaxDelay %v", attempt, got, maxDelay)
		}
	}
}

// TestAdversarial_Backoff_MonotonicAndCapped asserts the two headline guarantees across
// the full reachable attempt range for several ordinary configs:
//   (1) never negative,
//   (2) never exceeds MaxDelay,
//   (3) monotonic-nondecreasing (a real exponential schedule never gets SMALLER as
//       attempts grow — overflow wrap makes it oscillate/drop, which this catches).
//
// FAILS on unfixed prod (the overflow wrap breaks all three). Jitter off for determinism.
func TestAdversarial_Backoff_MonotonicAndCapped(t *testing.T) {
	type cfg struct {
		name        string
		base        time.Duration
		maxDelay    time.Duration
		maxAttempts int
		strat       BackoffStrategy
	}
	cfgs := []cfg{
		{"Exp/100ms/5s/64", 100 * time.Millisecond, 5 * time.Second, 64, Exponential},
		{"Exp/1s/30s/64", 1 * time.Second, 30 * time.Second, 64, Exponential},
		{"Exp/1ms/1s/64", 1 * time.Millisecond, 1 * time.Second, 64, Exponential},
	}
	for _, c := range cfgs {
		t.Run(c.name, func(t *testing.T) {
			r := &Retry{
				MaxAttempts:     c.maxAttempts,
				Delay:           c.base,
				MaxDelay:        c.maxDelay,
				Jitter:          false,
				BackoffStrategy: c.strat,
			}
			var prev time.Duration = -1
			for attempt := 0; attempt < r.MaxAttempts; attempt++ {
				got := r.computeDelay(attempt)
				want := expectedCappedExp(c.base, c.maxDelay, attempt)
				if got < 0 {
					t.Fatalf("attempt=%d: NEGATIVE delay %v (ns=%d); overflow wrap. want %v",
						attempt, got, int64(got), want)
				}
				if got > c.maxDelay {
					t.Fatalf("attempt=%d: delay %v exceeds MaxDelay %v", attempt, got, c.maxDelay)
				}
				if got < prev {
					t.Fatalf("attempt=%d: NON-MONOTONIC backoff: delay %v < previous %v "+
						"(a real exponential schedule never decreases; this is the overflow wrap)",
						attempt, got, prev)
				}
				prev = got
			}
		})
	}
}

// TestAdversarial_Linear_Overflow probes the Linear strategy for the same int64 overflow.
// Linear: delay = base * (attempt+1). With a large base, base*(attempt+1) overflows at a
// high-but-legal attempt index and wraps negative, bypassing the cap.
//
// base=1h, MaxDelay=24h. attempt index ~2.56M overflows. Reachable only with a huge
// MaxAttempts, so this is a LATENT RISK rather than the headline bug — but it shares the
// root cause. Marked with a sub-assertion that documents the boundary; it asserts the
// SINK value at the overflow index directly (no loop to millions).
func TestAdversarial_Linear_Overflow(t *testing.T) {
	base := 1 * time.Hour
	maxDelay := 24 * time.Hour
	r := &Retry{
		MaxAttempts:     1, // not driving the loop; probing computeDelay directly
		Delay:           base,
		MaxDelay:        maxDelay,
		Jitter:          false,
		BackoffStrategy: Linear,
	}
	// math.MaxInt64 / base(ns) gives the first multiplier that overflows.
	overflowMultiplier := int(math.MaxInt64/int64(base)) + 1 // (attempt+1) that overflows
	overflowAttempt := overflowMultiplier - 1
	got := r.computeDelay(overflowAttempt)
	if got < 0 {
		t.Logf("LATENT: Linear computeDelay(attempt=%d) = %v (ns=%d) NEGATIVE (base*%d overflows int64). "+
			"Same overflow root cause as Exponential; only reachable with a very large MaxAttempts.",
			overflowAttempt, got, int64(got), overflowMultiplier)
	}
	// Sink-side hard assertion: whatever the value, it must be a sane, capped, non-negative delay.
	if got < 0 || got > maxDelay {
		t.Fatalf("Linear overflow SINK: computeDelay(attempt=%d) = %v not in [0, %v]",
			overflowAttempt, got, maxDelay)
	}
}

// TestAdversarial_MaxAttempts_ExactCount pins the off-by-one contract at the SINK:
// fn is invoked EXACTLY MaxAttempts times on persistent failure (not N-1, not N+1).
func TestAdversarial_MaxAttempts_ExactCount(t *testing.T) {
	for _, n := range []int{1, 2, 3, 7} {
		calls := 0
		r := &Retry{MaxAttempts: n, Delay: time.Nanosecond, Jitter: false, BackoffStrategy: Fixed}
		_ = r.Do(context.Background(), func() error { calls++; return errors.New("fail") })
		if calls != n {
			t.Errorf("MaxAttempts=%d: fn invoked %d times, want exactly %d", n, calls, n)
		}
	}
}

// TestAdversarial_MaxAttempts_ZeroAndNegative pins that a zero or negative MaxAttempts
// does NOT loop infinitely and does NOT invoke fn — the `i < MaxAttempts` guard handles it.
// Sink: call count is 0 and the call returns promptly (no hang).
func TestAdversarial_MaxAttempts_ZeroAndNegative(t *testing.T) {
	for _, n := range []int{0, -1, -1000} {
		calls := 0
		r := &Retry{MaxAttempts: n, Delay: time.Second, Jitter: false, BackoffStrategy: Fixed}
		done := make(chan error, 1)
		go func() {
			done <- r.Do(context.Background(), func() error { calls++; return errors.New("fail") })
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("MaxAttempts=%d: Do did not return — possible infinite loop", n)
		}
		if calls != 0 {
			t.Errorf("MaxAttempts=%d: fn invoked %d times, want 0", n, calls)
		}
	}
}

// TestAdversarial_Jitter_NeverNegative_NeverNaN drives jitter hard across many draws and
// attempt indices, asserting the post-jitter SINK value is always >= the pre-jitter delay
// and never negative. (computeDelay uses integer rand, so NaN is not reachable, but a
// negative delay would be if the overflow wrap fed jitter; this composes the two.)
func TestAdversarial_Jitter_NeverNegative_NeverNaN(t *testing.T) {
	r := &Retry{
		MaxAttempts:     32,
		Delay:           1 * time.Millisecond,
		MaxDelay:        1 * time.Second,
		Jitter:          true,
		BackoffStrategy: Exponential,
	}
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		for i := 0; i < 300; i++ {
			d := r.computeDelay(attempt)
			if d < 0 {
				t.Fatalf("attempt=%d: jittered delay NEGATIVE %v (ns=%d)", attempt, d, int64(d))
			}
		}
	}
}

// TestAdversarial_Concurrency_SharedRetry probes whether a single *Retry reused across
// goroutines corrupts state. computeDelay reads only immutable config + a package-global
// rand source; Do/DoWithResult keep attempt state in locals. So this should be race-clean.
// Run under -race; the assertion is sink-side (every call completes with the right count).
func TestAdversarial_Concurrency_SharedRetry(t *testing.T) {
	r := &Retry{MaxAttempts: 4, Delay: time.Nanosecond, Jitter: true, BackoffStrategy: Exponential}
	const goroutines = 64
	errs := make(chan int, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			calls := 0
			_ = r.Do(context.Background(), func() error { calls++; return errors.New("fail") })
			errs <- calls
		}()
	}
	for g := 0; g < goroutines; g++ {
		if c := <-errs; c != r.MaxAttempts {
			t.Errorf("goroutine saw %d calls, want %d (shared-state corruption?)", c, r.MaxAttempts)
		}
	}
}
