package ringavg

import (
	"math"
	"testing"
)

// This file is an adversarial audit of the moving-average CONTRACT of
// RingAverage. The existing ringavg_test.go proves the smoothing/eviction
// story at one window size with hand-picked streams; here we pin the exact
// numeric contract against an independent last-N recompute oracle across many
// window sizes, before-full / at-full / after-wrap regimes, long sequences
// (sum-drift), and the documented edges. Every expected number is DERIVED from
// the inputs via lastNMean / meanOf — none are hardcoded — so an off-by-one in
// the window, a failure to evict, or a divide-by-cap-instead-of-count would
// move the oracle and the assertion would bite.

// lastNMean is the centerpiece oracle: given the full ordered history of pushed
// samples and window size n, it returns the arithmetic mean of EXACTLY the last
// min(len,n) samples — recomputed from scratch, independent of RingAverage's
// running sum. This is what Average() must equal at every step.
func lastNMean(history []float64, n int) float64 {
	if len(history) == 0 {
		return 0
	}
	start := len(history) - n
	if start < 0 {
		start = 0
	}
	window := history[start:]
	var s float64
	for _, x := range window {
		s += x
	}
	return s / float64(len(window))
}

// adversarialEps is a tight per-window tolerance for float mean comparison.
// The running sum and the fresh recompute accumulate in different orders, so we
// allow rounding slack but keep it far below any contract-relevant gap.
const adversarialEps = 1e-9

func closeEnough(a, b float64) bool { return math.Abs(a-b) <= adversarialEps }

// TestAfterWrap_EqualsLastNMean is the CENTERPIECE: push 2N+ DISTINCT,
// monotonically increasing values through several window sizes and assert that
// after every single push Average() equals the mean of exactly the last N
// pushed values, recomputed independently. Distinct increasing values make any
// off-by-one fatal:
//   - if the window kept N+1 (failed to evict / evicted too late), an older,
//     smaller value lingers and drags the mean DOWN below the oracle;
//   - if it kept N-1 (evicted too eagerly / wrong index), the mean rides HIGHER;
//   - if it divided by cap instead of count it would already diverge before
//     full and the at-full step would still differ on partials.
func TestAfterWrap_EqualsLastNMean(t *testing.T) {
	id := runUUID(t)
	for _, n := range []int{1, 2, 3, 5, 8, 16, 33} {
		ra := NewRingAverage(n)
		history := make([]float64, 0, 3*n+2)
		// Push well past one full wrap (>= 3 full windows) with strictly
		// increasing, well-separated values.
		total := 3*n + 2
		for i := 0; i < total; i++ {
			v := float64(i)*1.5 + 0.25 // distinct, increasing
			ra.Push(v)
			history = append(history, v)

			got := ra.Average()
			want := lastNMean(history, n)
			if !closeEnough(got, want) {
				t.Fatalf("run=%s n=%d after %d pushes: Average=%.10f want last-%d mean=%.10f (wraparound/eviction wrong; latest=%.3f)",
					id, n, i+1, got, n, want, v)
			}
			// Len must track min(pushes, n) exactly.
			wantLen := i + 1
			if wantLen > n {
				wantLen = n
			}
			if ra.Len() != wantLen {
				t.Fatalf("run=%s n=%d after %d pushes: Len=%d want=%d", id, n, i+1, ra.Len(), wantLen)
			}
		}
		t.Logf("run=%s n=%d after-wrap ok over %d pushes (>=3 wraps), Average tracked last-%d mean every step", id, n, total, n)
	}
}

// TestBeforeFull_DividesByActualCount is the second centerpiece: while fewer
// than N samples have been pushed, Average() must be the mean of the k samples
// SO FAR (divide by k), never divided by the capacity N. We assert at every
// k=1..N against the independent oracle. We choose values whose running mean is
// clearly distinguishable from sum/N, so a divide-by-N bug is caught at the
// very first push (k=1: mean must equal the single value, not value/N).
func TestBeforeFull_DividesByActualCount(t *testing.T) {
	id := runUUID(t)
	for _, n := range []int{1, 2, 4, 7, 8, 13} {
		ra := NewRingAverage(n)
		history := make([]float64, 0, n)
		for k := 1; k <= n; k++ {
			v := float64(k) * 10.0 // 10,20,30,...
			ra.Push(v)
			history = append(history, v)

			got := ra.Average()
			want := meanOf(history) // mean of exactly the k samples so far
			if !closeEnough(got, want) {
				t.Fatalf("run=%s n=%d at k=%d (before full): Average=%.10f want mean-of-%d=%.10f -- looks divided by cap, not count",
					id, n, k, got, k, want)
			}
			// Independent divide-by-cap trap: what a buggy /cap impl would give.
			var rawSum float64
			for _, x := range history {
				rawSum += x
			}
			divByCap := rawSum / float64(n)
			if k < n && closeEnough(divByCap, want) {
				// Only a problem if our chosen values can't distinguish the two;
				// guard against a vacuous probe.
				t.Fatalf("run=%s n=%d k=%d probe vacuous: divByCap==divByCount==%.6f", id, n, k, want)
			}
			if ra.Full() != (k == n) {
				t.Fatalf("run=%s n=%d k=%d Full=%v want=%v", id, n, k, ra.Full(), k == n)
			}
		}
		t.Logf("run=%s n=%d before-full ok: Average divided by actual count at every k=1..%d", id, n, n)
	}
}

// TestExactBoundary_AtFullAndJustWrapped pins the two most error-prone single
// steps: exactly N samples (just full) and N+1 (just wrapped once). At N the
// window is all N samples; at N+1 the first sample must be gone and the window
// is samples[1..N]. Distinct values make the one-sample shift detectable.
func TestExactBoundary_AtFullAndJustWrapped(t *testing.T) {
	id := runUUID(t)
	n := 8
	ra := NewRingAverage(n)
	history := make([]float64, 0, n+1)

	for i := 0; i < n; i++ {
		v := float64(i+1) * 3.0 // 3,6,...,24
		ra.Push(v)
		history = append(history, v)
	}
	atFull := ra.Average()
	wantAtFull := meanOf(history) // all N
	if !closeEnough(atFull, wantAtFull) {
		t.Fatalf("run=%s at-full(N=%d): Average=%.10f want=%.10f", id, n, atFull, wantAtFull)
	}
	if !ra.Full() || ra.Len() != n {
		t.Fatalf("run=%s at-full state wrong: Len=%d Full=%v", id, ra.Len(), ra.Full())
	}

	// One more push -> just wrapped: oldest (history[0]) must be evicted.
	vNext := 999.0 // big, distinct: if eviction is wrong, mean is far off
	ra.Push(vNext)
	history = append(history, vNext)
	justWrapped := ra.Average()
	wantWrapped := meanOf(history[1:]) // exactly samples[1..N]
	if !closeEnough(justWrapped, wantWrapped) {
		t.Fatalf("run=%s just-wrapped(N+1): Average=%.10f want mean(samples[1:])=%.10f (oldest %.1f not evicted?)",
			id, justWrapped, wantWrapped, history[0])
	}
	// Sanity: including the evicted sample would give a different number.
	withEvicted := meanOf(history) // all N+1 — what a no-evict bug yields
	if closeEnough(withEvicted, wantWrapped) {
		t.Fatalf("run=%s boundary probe vacuous: with/without evicted sample equal (%.6f)", id, wantWrapped)
	}
	t.Logf("run=%s boundary ok: atFull=%.4f (mean of N) justWrapped=%.4f (mean of last N, evicted %.1f; no-evict would be %.4f)",
		id, atFull, justWrapped, history[0], withEvicted)
}

// TestSumDrift_LongSequenceStaysAccurate stresses the running-sum bookkeeping:
// 200*N pushes (many wraps) with values that mix large and tiny magnitudes so a
// subtract-on-evict precision bug or a missed eviction would accumulate visible
// error. After every push (sampled) we compare against a FRESH recompute of the
// current window. If the running sum drifted, Average() would diverge from the
// independent oracle by more than adversarialEps.
func TestSumDrift_LongSequenceStaysAccurate(t *testing.T) {
	id := runUUID(t)
	n := 16
	ra := NewRingAverage(n)
	history := make([]float64, 0, 200*n)
	total := 200 * n
	var maxErr float64
	for i := 0; i < total; i++ {
		// Alternate large and small values to provoke catastrophic
		// cancellation in a naive running sum.
		var v float64
		if i%2 == 0 {
			v = 1e7 + float64(i)
		} else {
			v = 1e-3 * float64(i%7)
		}
		ra.Push(v)
		history = append(history, v)

		got := ra.Average()
		want := lastNMean(history, n)
		err := math.Abs(got - want)
		if err > maxErr {
			maxErr = err
		}
		// Tolerance scaled to magnitude: means here are ~5e6, so a relative
		// 1e-9 corresponds to ~5e-3 absolute. We assert a tight absolute bound
		// that a real drift bug (missed eviction) would blow past by orders.
		if err > 1e-3 {
			t.Fatalf("run=%s sum-drift at push %d: Average=%.6f want=%.6f absErr=%.6g (running sum drifted)",
				id, i+1, got, want, err)
		}
	}
	t.Logf("run=%s sum-drift ok over %d pushes (~%d wraps): maxAbsErr=%.3g vs fresh recompute", id, total, total/n, maxErr)
}

// TestEmptyRing_NoPanicReturnsZero pins the documented empty-ring contract:
// Average() on a fresh ring returns 0 with no divide-by-zero panic.
func TestEmptyRing_NoPanicReturnsZero(t *testing.T) {
	id := runUUID(t)
	for _, n := range []int{1, 4, 8} {
		ra := NewRingAverage(n)
		if got := ra.Average(); got != 0 {
			t.Fatalf("run=%s empty ring n=%d: Average=%.6f want 0", id, n, got)
		}
		if ra.Len() != 0 || ra.Full() {
			t.Fatalf("run=%s empty ring n=%d: Len=%d Full=%v want 0/false", id, n, ra.Len(), ra.Full())
		}
	}
	t.Logf("run=%s empty-ring ok: Average()==0, no div-by-zero", id)
}

// TestWindowSizeOne_AlwaysLatest: N=1 means the window holds only the most
// recent sample, so Average() must always equal the last pushed value exactly.
func TestWindowSizeOne_AlwaysLatest(t *testing.T) {
	id := runUUID(t)
	ra := NewRingAverage(1)
	vals := []float64{3.0, -7.5, 100.0, 0.0, -0.001, 1e9}
	for i, v := range vals {
		ra.Push(v)
		if got := ra.Average(); got != v {
			t.Fatalf("run=%s n=1 step %d: Average=%.6f want latest=%.6f", id, i, got, v)
		}
		if !ra.Full() || ra.Len() != 1 {
			t.Fatalf("run=%s n=1 step %d: Len=%d Full=%v", id, i, ra.Len(), ra.Full())
		}
	}
	t.Logf("run=%s n=1 ok: Average always equals latest sample", id)
}

// TestNonPositiveCapacity_ClampedToOne verifies the documented clamp: n<=0
// becomes capacity 1 and behaves as a window-of-one (no panic, no zero-cap
// divide or index error).
func TestNonPositiveCapacity_ClampedToOne(t *testing.T) {
	id := runUUID(t)
	for _, n := range []int{0, -1, -100} {
		ra := NewRingAverage(n)
		ra.Push(42.0)
		if got := ra.Average(); got != 42.0 {
			t.Fatalf("run=%s clamp n=%d: Average=%.6f want 42", id, n, got)
		}
		ra.Push(8.0) // wraps the size-1 window
		if got := ra.Average(); got != 8.0 {
			t.Fatalf("run=%s clamp n=%d after wrap: Average=%.6f want 8", id, n, got)
		}
		if !ra.Full() || ra.Len() != 1 {
			t.Fatalf("run=%s clamp n=%d: Len=%d Full=%v want 1/true", id, n, ra.Len(), ra.Full())
		}
	}
	t.Logf("run=%s non-positive capacity clamped to 1 and usable", id)
}

// TestNegativeAndLargeValues_MeanCorrect feeds negatives, zeros, and very large
// magnitudes through a wrapping window and checks Average() against the
// independent last-N oracle. Catches sign errors in eviction and large-value
// handling.
func TestNegativeAndLargeValues_MeanCorrect(t *testing.T) {
	id := runUUID(t)
	n := 4
	ra := NewRingAverage(n)
	history := make([]float64, 0, 12)
	vals := []float64{-1e6, 5e6, 0.0, -2.5, 1e8, -1e8, 7.0, -7.0, 3.3, -3.3, 1e-9, -1e-9}
	for i, v := range vals {
		ra.Push(v)
		history = append(history, v)
		got := ra.Average()
		want := lastNMean(history, n)
		if !closeEnough(got, want) {
			t.Fatalf("run=%s neg/large step %d: Average=%.6g want last-%d mean=%.6g (v=%.6g)",
				id, i, got, n, want, v)
		}
	}
	t.Logf("run=%s neg/large ok: Average tracked last-%d mean through sign changes and 1e8-magnitude samples", id, n)
}
