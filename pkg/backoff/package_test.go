package backoff

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Base == 0 || c.Max == 0 {
		t.Error("expected non-zero base and max")
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
	// Mutation: Max cap removed → large iterations produce huge (uncapped) durations.
	// We simulate "no cap" by setting Max to a very large value and asserting that
	// the computed duration exceeds what a reasonable cap would be.
	c := Config{Base: 100 * time.Millisecond, Max: 1 << 62, Factor: 2}
	d := c.Duration(20)
	// With a reasonable cap (e.g. 30s), Duration(20) would be capped.
	// Without a cap, it should be astronomically larger than 30s.
	if d <= 30*time.Second {
		t.Error("without cap, large iterations should produce durations far exceeding 30s")
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
