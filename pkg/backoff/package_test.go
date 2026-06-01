package backoff

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	// Pin the exact default config so any drift in Base/Max/Factor is caught.
	if c.Base != 100*time.Millisecond {
		t.Errorf("default Base: got %v, want %v", c.Base, 100*time.Millisecond)
	}
	if c.Max != 30*time.Second {
		t.Errorf("default Max: got %v, want %v", c.Max, 30*time.Second)
	}
	if c.Factor != 2 {
		t.Errorf("default Factor: got %v, want 2", c.Factor)
	}
	// Behavioral check: a wrong default Factor (e.g. 1) would yield 100ms here.
	if d := c.Duration(1); d != 200*time.Millisecond {
		t.Errorf("Default().Duration(1): got %v, want %v", d, 200*time.Millisecond)
	}
}

func TestDuration(t *testing.T) {
	c := Config{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2}
	if d := c.Duration(0); d != 100*time.Millisecond {
		t.Errorf("unexpected duration: %v", d)
	}
	if d := c.Duration(10); d != c.Max {
		t.Errorf("expected capped duration, got %v", d)
	}
}

// --- Mutation tests ---

func TestDuration_NegativeIteration_Mutation(t *testing.T) {
	// Mutation: if n < 0 check removed → negative input would produce wrong (large) duration
	c := Config{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2}
	// n=-1 with math.Pow(2, -1) = 0.5 → 50ms. Without the n<0 guard, this would be 50ms.
	// With the guard, n is clamped to 0 → 100ms.
	d := c.Duration(-1)
	if d != c.Base {
		t.Errorf("negative iteration should be clamped to base, got %v", d)
	}
}

func TestDuration_CapRemoved_Mutation(t *testing.T) {
	// Mutation target: the `if d > c.Max { return c.Max }` branch.
	// Using a SMALL Max makes the cap actually bind, so removing the branch is
	// observable. Base=100ms, Factor=2, Max=500ms.
	c := Config{Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, Factor: 2}

	// Mid-range attempt: 100ms * 2^2 = 400ms, below Max → exact UNCAPPED value.
	// This pins the math.Pow growth and proves the cap is NOT applied prematurely.
	if d := c.Duration(2); d != 400*time.Millisecond {
		t.Errorf("Duration(2): got %v, want %v (uncapped)", d, 400*time.Millisecond)
	}

	// Large attempt: 100ms * 2^20 ≈ 104857.6s, far above Max → must be capped to Max.
	// If the cap branch is removed, this returns ~29h and the test fails.
	if d := c.Duration(20); d != 500*time.Millisecond {
		t.Errorf("Duration(20): got %v, want %v (capped to Max)", d, 500*time.Millisecond)
	}
}

func TestDuration_MonotonicGrowth_Mutation(t *testing.T) {
	// Backoff must be non-decreasing as the attempt count rises, and must clamp
	// at Max once reached. Any mutation that breaks growth or the cap is caught.
	c := Config{Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, Factor: 2}
	prev := c.Duration(0)
	if prev != 100*time.Millisecond {
		t.Errorf("Duration(0): got %v, want %v", prev, 100*time.Millisecond)
	}
	for n := 1; n <= 20; n++ {
		cur := c.Duration(n)
		if cur < prev {
			t.Errorf("non-monotonic: Duration(%d)=%v < Duration(%d)=%v", n, cur, n-1, prev)
		}
		if cur > c.Max {
			t.Errorf("Duration(%d)=%v exceeds Max=%v", n, cur, c.Max)
		}
		prev = cur
	}
	// Exact expected sequence up to the cap: 100, 200, 400, then clamped at 500.
	if d := c.Duration(1); d != 200*time.Millisecond {
		t.Errorf("Duration(1): got %v, want 200ms", d)
	}
	if d := c.Duration(3); d != 500*time.Millisecond {
		t.Errorf("Duration(3): got %v, want 500ms (capped)", d)
	}
}

func TestDuration_FactorChanged_Mutation(t *testing.T) {
	// Mutation: Factor changed from 2 to 1 → backoff no longer grows exponentially
	c := Config{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 1}
	d0 := c.Duration(0)
	d5 := c.Duration(5)
	if d0 != d5 {
		t.Error("with factor=1, all iterations should yield the same duration")
	}
	if d0 != c.Base {
		t.Errorf("with factor=1, duration should equal base, got %v", d0)
	}
}

// TestDuration_MidRange_Uncapped pins the exact mid-range value when Max is large
// enough that the cap does NOT apply. Duration(3) = 100ms * 2^3 = 800ms, which is
// below Max=10s, so the cap branch must NOT fire and the raw computed value is
// returned. Mutation: removing the exponential formula (e.g. replacing math.Pow
// with a fixed value) would yield a different result and fail this test.
func TestDuration_MidRange_Uncapped(t *testing.T) {
	// Max intentionally larger than 100*2^3=800ms so the cap does not bind.
	c := Config{Base: 100 * time.Millisecond, Max: 10 * time.Second, Factor: 2}

	// n=3: 100ms * 2^3 = 800ms — must be returned exactly (no cap).
	if d := c.Duration(3); d != 800*time.Millisecond {
		t.Errorf("Duration(3): got %v, want 800ms (uncapped mid-range)", d)
	}

	// Also pin n=0..3 exact values to guard against math.Pow regression.
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
	}
	for _, tc := range cases {
		if d := c.Duration(tc.n); d != tc.want {
			t.Errorf("Duration(%d): got %v, want %v", tc.n, d, tc.want)
		}
	}
}
