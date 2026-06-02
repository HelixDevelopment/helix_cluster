package ringavg

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"testing"
)

// eps is the tolerance for floating-point mean comparisons. The running sum and
// the oracle accumulate in different orders, so exact equality is not
// guaranteed; the gap is bounded by rounding error far below any threshold gap.
const eps = 1e-9

func approxEqual(a, b float64) bool { return math.Abs(a-b) <= eps }

// runUUID returns a random run identifier for log correlation. It is used only
// for t.Logf observability and never feeds into any assertion, keeping the
// tests deterministic.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b)
}

// meanOf is an independent oracle: it computes the arithmetic mean of a slice
// directly (not via RingAverage), so expected numbers are DERIVED from inputs
// rather than hardcoded.
func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

const (
	threshold = 0.90 // the level a raw spike trips but the smoothed value must not
	baseline  = 0.50 // noisy stream centers here
	spike     = 0.95 // single transient sample, exceeds threshold on its own
	window    = 8    // N
)

// TestSpikeSmoothing_NeverCrossesThreshold covers CLOSURE CLAUSE 1:
// A noisy stream around 0.5 with a SINGLE transient 0.95 spike, replayed
// through a window of N=8, must keep the moving Average() below 0.90 at every
// step — while the RAW spike sample itself (0.95) does exceed 0.90. This proves
// the SMOOTHING is what suppresses the spike, not the data being benign.
//
// Mutation: an Average() that returns only the latest sample (no smoothing)
// would equal 0.95 on the spike step and cross 0.90 -> this test fails.
func TestSpikeSmoothing_NeverCrossesThreshold(t *testing.T) {
	id := runUUID(t)
	// Deterministic "noise" around baseline: small alternating offsets, all
	// well below threshold. No time.Now / no RNG in the logic under test.
	noise := []float64{-0.03, 0.04, -0.02, 0.05, -0.04, 0.03, -0.05, 0.02, -0.01, 0.04, -0.03, 0.05}
	stream := make([]float64, 0, len(noise)+1)
	spikeIdx := 5 // inject the single transient spike partway through
	for i, n := range noise {
		if i == spikeIdx {
			stream = append(stream, spike)
		}
		stream = append(stream, baseline+n)
	}

	t.Logf("run=%s clause=1 BEFORE: window=%d threshold=%.2f spike=%.2f streamLen=%d", id, window, threshold, spike, len(stream))

	ra := NewRingAverage(window)
	var maxAvg float64
	var maxRaw float64
	for i, v := range stream {
		ra.Push(v)
		avg := ra.Average()
		if avg > maxAvg {
			maxAvg = avg
		}
		if v > maxRaw {
			maxRaw = v
		}
		if avg > threshold {
			t.Fatalf("run=%s clause=1 smoothed average crossed threshold at step %d: avg=%.4f > %.2f (sample=%.4f)", id, i, avg, threshold, v)
		}
	}

	// The raw stream MUST contain a sample above threshold, otherwise the test
	// would be vacuously true (nothing to suppress).
	if maxRaw <= threshold {
		t.Fatalf("run=%s clause=1 raw stream never exceeded threshold (maxRaw=%.4f); test would be vacuous", id, maxRaw)
	}
	// And the smoothing must have produced a strictly lower peak than the raw
	// peak — that gap is the suppression we are proving.
	if !(maxAvg < maxRaw) {
		t.Fatalf("run=%s clause=1 smoothing did not reduce the peak: maxAvg=%.4f maxRaw=%.4f", id, maxAvg, maxRaw)
	}

	t.Logf("run=%s clause=1 AFTER: maxRaw=%.4f (>threshold) maxAvg=%.4f (<threshold) suppression=%.4f", id, maxRaw, maxAvg, maxRaw-maxAvg)
}

// TestSustainedHigh_DrivesAverageAboveThreshold covers CLOSURE CLAUSE 2:
// A stream that stays at 0.95 for >= N samples DOES drive Average() above 0.90.
// This proves the smoother is not "always below" — it does not falsely suppress
// a real sustained signal.
//
// Mutation: clamping Average() to always return < threshold would fail here.
func TestSustainedHigh_DrivesAverageAboveThreshold(t *testing.T) {
	id := runUUID(t)
	ra := NewRingAverage(window)

	// Prime with benign baseline samples, then sustain the high value for a
	// full window so the buffer holds only high samples.
	for i := 0; i < window; i++ {
		ra.Push(baseline)
	}
	t.Logf("run=%s clause=2 BEFORE sustain: avg=%.4f (baseline-filled)", id, ra.Average())

	sustained := make([]float64, window)
	for i := range sustained {
		sustained[i] = spike
		ra.Push(spike)
	}
	got := ra.Average()
	want := meanOf(sustained) // derived oracle: full window of spike == spike
	if !approxEqual(got, want) {
		t.Fatalf("run=%s clause=2 sustained average mismatch: got=%.6f want=%.6f", id, got, want)
	}
	if !(got > threshold) {
		t.Fatalf("run=%s clause=2 sustained high did NOT cross threshold: avg=%.4f <= %.2f (false suppression)", id, got, threshold)
	}
	t.Logf("run=%s clause=2 AFTER sustain: avg=%.4f (>threshold=%.2f) over %d samples", id, got, threshold, window)
}

// TestEviction_OldestNoLongerInfluences covers CLOSURE CLAUSE 3:
// After N+1 pushes the oldest sample must no longer influence the average; the
// average must equal the mean of the last N samples only.
//
// Mutation: never evicting (cumulative average over all pushes) would include
// the first sample and fail this check.
func TestEviction_OldestNoLongerInfluences(t *testing.T) {
	id := runUUID(t)
	ra := NewRingAverage(window)

	// Build N+1 distinct, deterministic samples so the evicted one is large and
	// would visibly skew a cumulative average.
	samples := make([]float64, window+1)
	samples[0] = 100.0 // the soon-to-be-evicted outlier
	for i := 1; i < len(samples); i++ {
		samples[i] = float64(i) // 1,2,...,window
	}

	for _, v := range samples {
		ra.Push(v)
	}

	got := ra.Average()
	lastN := samples[len(samples)-window:] // exactly the last N pushed
	want := meanOf(lastN)                  // derived oracle excluding samples[0]

	// Cross-check: a cumulative (never-evicting) average would differ, proving
	// the assertion is mutation-sensitive and not vacuous.
	cumulative := meanOf(samples)
	if want == cumulative {
		t.Fatalf("run=%s clause=3 test setup vacuous: windowed mean equals cumulative mean (%.6f)", id, want)
	}

	t.Logf("run=%s clause=3 BEFORE assert: got=%.6f want(lastN mean)=%.6f cumulative(all)=%.6f evicted=%.1f", id, got, want, cumulative, samples[0])

	if !approxEqual(got, want) {
		t.Fatalf("run=%s clause=3 eviction broken: average=%.6f want=%.6f (oldest sample %.1f still influencing)", id, got, want, samples[0])
	}
	if ra.Len() != window || !ra.Full() {
		t.Fatalf("run=%s clause=3 buffer state wrong: Len=%d Full=%v want Len=%d Full=true", id, ra.Len(), ra.Full(), window)
	}
	t.Logf("run=%s clause=3 AFTER assert: average=%.6f matches last-%d mean; oldest %.1f evicted; Len=%d Full=%v", id, got, window, samples[0], ra.Len(), ra.Full())
}

// TestLenAndFull_Progression exercises the Len/Full accessors as the window
// fills, with values derived from the push count (not magic numbers).
func TestLenAndFull_Progression(t *testing.T) {
	id := runUUID(t)
	ra := NewRingAverage(window)
	if ra.Len() != 0 || ra.Full() {
		t.Fatalf("run=%s fresh buffer not empty: Len=%d Full=%v", id, ra.Len(), ra.Full())
	}
	for i := 1; i <= window; i++ {
		ra.Push(0.1)
		wantFull := i == window
		if ra.Len() != i {
			t.Fatalf("run=%s after %d pushes Len=%d want=%d", id, i, ra.Len(), i)
		}
		if ra.Full() != wantFull {
			t.Fatalf("run=%s after %d pushes Full=%v want=%v", id, i, ra.Full(), wantFull)
		}
	}
	// Extra pushes keep Len pinned at capacity.
	ra.Push(0.1)
	ra.Push(0.1)
	if ra.Len() != window || !ra.Full() {
		t.Fatalf("run=%s over-full Len=%d Full=%v want Len=%d Full=true", id, ra.Len(), ra.Full(), window)
	}
	t.Logf("run=%s len/full progression ok: capped at Len=%d Full=%v", id, ra.Len(), ra.Full())
}
