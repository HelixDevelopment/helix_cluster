package backoff

import (
	"math"
	"testing"
	"time"
)

// This file adversarially pins the Config.Duration contract for the dominant
// backoff bug classes:
//   (1) computed delay EXCEEDS the configured Max cap,
//   (2) integer/duration OVERFLOW at high iteration counts wrapping to a tiny
//       or NEGATIVE delay (float64 * 2^n eventually exceeds int64/time.Duration),
//   (3) negative/zero iteration producing a negative or wrong delay,
//   (4) non-monotonic growth before the cap.
//
// The centerpiece invariants for any VALID config (Base >= 0, Factor >= 1,
// Max >= Base) are: for every n (including very large n), the returned delay is
// in [0, Max]. The overflow path must SATURATE at Max, never wrap negative.

// validConfigs returns a representative set of well-formed configs whose
// contract is 0 <= Duration(n) <= Max for all n.
func validConfigs() []Config {
	return []Config{
		Default(),
		{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2},
		{Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, Factor: 2},
		{Base: 1 * time.Nanosecond, Max: 30 * time.Second, Factor: 2},
		{Base: 1 * time.Second, Max: 5 * time.Minute, Factor: 3},
		{Base: 50 * time.Millisecond, Max: 10 * time.Second, Factor: 1.5},
		{Base: 1 * time.Millisecond, Max: time.Duration(math.MaxInt64), Factor: 2}, // huge Max: overflow must still not wrap
	}
}

// TestDuration_CapAndNonNegative_AcrossHugeIterations is the CENTERPIECE.
// For every valid config and a wide range of iterations (0..1000, including the
// int64/Duration-overflow region around n=63 for Base=1ns..100ms and well past
// the float64 +Inf threshold near n>=1024), the delay MUST satisfy
// 0 <= d <= Max. This single loop catches BOTH overflow-to-negative wrap AND
// cap-not-enforced-after-exponent.
func TestDuration_CapAndNonNegative_AcrossHugeIterations(t *testing.T) {
	iters := []int{0, 1, 2, 3, 5, 10, 20, 30, 31, 32, 50, 60, 62, 63, 64, 70,
		100, 200, 500, 700, 1000, 1023, 1024, 1025, 2000, 1 << 20, math.MaxInt32}
	for _, c := range validConfigs() {
		for _, n := range iters {
			d := c.Duration(n)
			if d < 0 {
				t.Fatalf("NEGATIVE delay: Config%+v Duration(%d)=%v (int64=%d) — overflow wrapped past int64",
					c, n, d, int64(d))
			}
			if d > c.Max {
				t.Fatalf("OVER-CAP delay: Config%+v Duration(%d)=%v exceeds Max=%v (int64=%d) — cap not enforced",
					c, n, d, c.Max, int64(d))
			}
		}
	}
}

// TestDuration_OverflowSaturatesToMax drives iterations past the point where
// float64(Base)*2^n exceeds math.MaxInt64 (and past where it becomes +Inf), and
// asserts the result is exactly Max — i.e. the overflow saturates to the cap
// instead of wrapping to a tiny/negative duration.
func TestDuration_OverflowSaturatesToMax(t *testing.T) {
	c := Config{Base: 100 * time.Millisecond, Max: 30 * time.Second, Factor: 2}
	// n=30 already exceeds Max; n>=63 overflows int64 range of the product;
	// n>=1024 makes math.Pow(2,n) == +Inf. All must clamp to Max.
	for _, n := range []int{30, 63, 64, 128, 1024, 4096} {
		if d := c.Duration(n); d != c.Max {
			t.Errorf("Duration(%d): got %v (int64=%d), want saturated Max=%v",
				n, d, int64(d), c.Max)
		}
	}
}

// TestDuration_InfinityDoesNotWrap pins that even when the float product is
// literally +Inf, the conversion+cap yields Max (not a wrapped value).
func TestDuration_InfinityDoesNotWrap(t *testing.T) {
	// Sanity: confirm the product really is +Inf at this exponent.
	if prod := float64(100*time.Millisecond) * math.Pow(2, 2000); !math.IsInf(prod, 1) {
		t.Fatalf("test premise broken: expected +Inf product, got %v", prod)
	}
	c := Config{Base: 100 * time.Millisecond, Max: 30 * time.Second, Factor: 2}
	if d := c.Duration(2000); d != c.Max {
		t.Errorf("Duration(2000) with +Inf product: got %v (int64=%d), want Max=%v",
			d, int64(d), c.Max)
	}
}

// TestDuration_ZeroAndNegativeIteration asserts safe behavior at the low edge:
// n=0 returns Base; negative n is clamped to 0 (returns Base), never a fractional
// or negative delay. (Complements the existing NegativeIteration mutation test
// with a swept range of negative inputs.)
func TestDuration_ZeroAndNegativeIteration(t *testing.T) {
	c := Config{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2}
	if d := c.Duration(0); d != c.Base {
		t.Errorf("Duration(0): got %v, want Base=%v", d, c.Base)
	}
	for _, n := range []int{-1, -2, -10, -1000, math.MinInt32} {
		d := c.Duration(n)
		if d < 0 {
			t.Errorf("Duration(%d): negative delay %v", n, d)
		}
		if d != c.Base {
			t.Errorf("Duration(%d): got %v, want clamped Base=%v", n, d, c.Base)
		}
	}
}

// TestDuration_MonotonicBeforeCap asserts the pre-cap sequence is strictly
// non-decreasing and that once Max is reached it stays at Max (no oscillation
// or wrap brings it back down). Uses a huge upper bound so the overflow region
// is included in the monotonicity check.
func TestDuration_MonotonicBeforeCap(t *testing.T) {
	c := Config{Base: 1 * time.Millisecond, Max: 30 * time.Second, Factor: 2}
	prev := c.Duration(0)
	for n := 1; n <= 200; n++ {
		cur := c.Duration(n)
		if cur < prev {
			t.Fatalf("non-monotonic: Duration(%d)=%v < Duration(%d)=%v (int64 cur=%d)",
				n, cur, n-1, prev, int64(cur))
		}
		if cur > c.Max {
			t.Fatalf("Duration(%d)=%v exceeds Max=%v", n, cur, c.Max)
		}
		prev = cur
	}
}

// TestDuration_ExactBoundary pins the last uncapped value and the first capped
// value for a config where the cap binds at a clean exponent. Base=1s, Factor=2,
// Max=8s: 2^3=8s is the last value == Max; 2^4=16s must clamp to 8s.
func TestDuration_ExactBoundary(t *testing.T) {
	c := Config{Base: 1 * time.Second, Max: 8 * time.Second, Factor: 2}
	if d := c.Duration(3); d != 8*time.Second {
		t.Errorf("Duration(3): got %v, want 8s (== Max, uncapped boundary)", d)
	}
	if d := c.Duration(4); d != 8*time.Second {
		t.Errorf("Duration(4): got %v, want 8s (capped, 16s clamped)", d)
	}
}
