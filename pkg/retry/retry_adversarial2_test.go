package retry

// Adversarial SECOND-PASS probes for pkg/retry.
//
// The first overflow fix added saturating multiplication (mulDelay) so that the
// exponential/linear FACTOR can no longer wrap time.Duration's int64 negative, and
// added a `delay > MaxDelay` cap plus a jitter-tiny guard. This file hunts what that
// first pass MISSED: a SECOND overflow that lives one statement later, in the JITTER
// ADDITION — `delay += jitter`.
//
// Root cause of the missed bug:
//   - When MaxDelay == 0 the cap is intentionally disabled (`if r.MaxDelay > 0`),
//     a DOCUMENTED "no cap" config. With no cap, an Exponential/Linear schedule
//     saturates `delay` to math.MaxInt64 ns (mulDelay). The first fix stopped the
//     wrap THERE.
//   - But then `delay += jitter` (jitter up to 25% of delay) is evaluated with NO
//     overflow guard and NO re-cap. delay≈MaxInt64 + positive jitter overflows int64
//     and wraps NEGATIVE. That negative time.Duration is handed straight to
//     time.NewTimer in Do/DoWithResult, which fires immediately => the backoff
//     collapses into a HOT RETRY LOOP — exactly the failure class the first fix was
//     meant to eliminate, just reached through jitter instead of the multiply.
//
// SINK under assertion: the time.Duration returned by (*Retry).computeDelay — the
// exact value passed to time.NewTimer. We assert the value (>= 0), never "no panic".
//
// CLASSIFICATION of probes here:
//   - TestAdversarial2_JitterOverflow_NoCap_BitesUnfixed  -> REAL BUG (always-on for
//     the valid MaxDelay==0 config). FAILS without the saturating-add fix, PASSES with.
//   - TestAdversarial2_JitterOverflow_Linear_NoCap        -> same root cause via Linear.
//   - TestAdversarial2_NoCap_HotLoop_CtxStops             -> end-to-end: proves a
//     negative jittered delay does NOT defeat ctx cancellation (Do still stops).
//   - TestAdversarial2_Jitter_WithinCap_StillExceedsBy25pct -> DOCUMENTED CONTRACT pin:
//     with a nonzero MaxDelay, post-jitter delay MAY exceed MaxDelay by up to 25%
//     (matches existing TestJitter_UpperBound_Exponential). Pinned so a future "hard
//     cap" change is a conscious decision, not an accident.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// TestAdversarial2_JitterOverflow_NoCap_BitesUnfixed is the headline second-overflow probe.
//
// Config: MaxDelay==0 (documented "no cap"), Exponential, Jitter on. At high-but-legal
// attempt indices the pre-jitter delay saturates to MaxInt64; `delay += jitter` then
// overflows int64 and wraps NEGATIVE. We draw many times per attempt (jitter is random)
// so the overflow is reliably exercised.
//
// SINK invariant: computeDelay must ALWAYS return a non-negative duration. A negative
// value is the hot-loop defect.
//
// FAILS on prod without the saturating-add fix; PASSES with it.
func TestAdversarial2_JitterOverflow_NoCap_BitesUnfixed(t *testing.T) {
	r := &Retry{
		MaxAttempts:     64,
		Delay:           1 * time.Second,
		MaxDelay:        0, // no cap — the saturating-multiply leaves delay at MaxInt64
		Jitter:          true,
		BackoffStrategy: Exponential,
	}
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		for i := 0; i < 256; i++ {
			got := r.computeDelay(attempt)
			if got < 0 {
				t.Fatalf("SINK jitter-overflow: computeDelay(attempt=%d) = %v (ns=%d) is NEGATIVE. "+
					"With MaxDelay==0 the exponential saturates to MaxInt64, then `delay += jitter` "+
					"overflowed int64 and wrapped negative. time.NewTimer fires immediately on a "+
					"negative duration => backoff collapses to a hot retry loop. The jitter add must "+
					"saturate at MaxInt64.", attempt, got, int64(got))
			}
		}
	}
}

// TestAdversarial2_JitterOverflow_Linear_NoCap exercises the same missed jitter overflow
// via the Linear strategy with no cap. base=1h, large attempt index -> saturated delay,
// then jitter pushes it over MaxInt64 and wraps without the fix.
func TestAdversarial2_JitterOverflow_Linear_NoCap(t *testing.T) {
	base := 1 * time.Hour
	r := &Retry{
		MaxAttempts:     1, // probing computeDelay directly, not driving the loop
		Delay:           base,
		MaxDelay:        0, // no cap
		Jitter:          true,
		BackoffStrategy: Linear,
	}
	// First multiplier whose product saturates to MaxInt64 (mulDelay clamps it there).
	overflowMultiplier := int(math.MaxInt64/int64(base)) + 1
	overflowAttempt := overflowMultiplier - 1 // (attempt+1) == overflowMultiplier
	for i := 0; i < 512; i++ {
		got := r.computeDelay(overflowAttempt)
		if got < 0 {
			t.Fatalf("SINK Linear jitter-overflow: computeDelay(attempt=%d) = %v (ns=%d) NEGATIVE; "+
				"saturated delay + jitter wrapped int64. Must saturate at MaxInt64.",
				overflowAttempt, got, int64(got))
		}
	}
}

// TestAdversarial2_NoCap_HotLoop_CtxStops is the end-to-end consequence proof. With the
// overflow-prone no-cap config, a negative jittered delay would make every timer fire
// instantly (a hot loop). We DON'T rely on the fix here: we assert the orthogonal safety
// net — context cancellation MUST still stop Do promptly even if a delay were negative.
// This both (a) guards against a hang in this very test and (b) pins that ctx is honored.
func TestAdversarial2_NoCap_HotLoop_CtxStops(t *testing.T) {
	r := &Retry{
		MaxAttempts:     1 << 30, // effectively unbounded attempts
		Delay:           1 * time.Second,
		MaxDelay:        0, // no cap -> overflow-prone schedule
		Jitter:          true,
		BackoffStrategy: Exponential,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- r.Do(ctx, func() error { calls++; return errors.New("fail") })
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Do returned %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Do did not return within 5s — ctx cancellation not honored (or a hot loop "+
			"starved it). calls so far=%d", calls)
	}
}

// TestAdversarial2_Jitter_WithinCap_StillExceedsBy25pct DOCUMENTS (pins) the intended
// envelope: with a NONZERO MaxDelay, the post-jitter delay is in [cap, cap*1.25). The
// cap clamps the PRE-jitter value; jitter then adds up to 25% ON TOP. This is NOT a bug
// (it matches the existing TestJitter_UpperBound_Exponential contract). Pinned so any
// future change to a hard ceiling is a deliberate, reviewed decision.
//
// PASSES on current prod (documents existing behavior).
func TestAdversarial2_Jitter_WithinCap_StillExceedsBy25pct(t *testing.T) {
	maxDelay := 5 * time.Second
	r := &Retry{
		MaxAttempts:     64,
		Delay:           100 * time.Millisecond,
		MaxDelay:        maxDelay,
		Jitter:          true,
		BackoffStrategy: Exponential,
	}
	hardUpper := maxDelay + maxDelay/4 // cap * 1.25
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		for i := 0; i < 200; i++ {
			got := r.computeDelay(attempt)
			if got < 0 {
				t.Fatalf("attempt=%d: NEGATIVE delay %v (ns=%d) — overflow even with a cap?",
					attempt, got, int64(got))
			}
			// Documented envelope: never below 0, never at/above cap*1.25.
			if got >= hardUpper {
				t.Fatalf("attempt=%d: delay %v >= cap*1.25 %v — jitter envelope contract broken",
					attempt, got, hardUpper)
			}
		}
	}
}
