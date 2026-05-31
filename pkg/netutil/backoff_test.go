package netutil

import (
	"testing"
	"time"
)

// fixedRand is a deterministic rand source returning a constant value in [0,1).
type fixedRand float64

func (f fixedRand) Float64() float64 { return float64(f) }

func TestBackoff_GrowsExponentially(t *testing.T) {
	b := Backoff{
		Base:   100 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 2.0,
		Jitter: 0, // no jitter → deterministic, pure exponential
	}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
	}
	for attempt, w := range want {
		got := b.Duration(attempt, nil)
		if got != w {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, w)
		}
	}
}

func TestBackoff_Bounded(t *testing.T) {
	b := Backoff{
		Base:   1 * time.Second,
		Max:    5 * time.Second,
		Factor: 2.0,
		Jitter: 0,
	}
	// Large attempt count would explode without the Max cap.
	for attempt := 0; attempt < 40; attempt++ {
		got := b.Duration(attempt, nil)
		if got > b.Max {
			t.Fatalf("attempt %d: %v exceeds Max %v", attempt, got, b.Max)
		}
	}
	// Far-out attempt must be pinned exactly at Max.
	if got := b.Duration(40, nil); got != b.Max {
		t.Fatalf("attempt 40: got %v, want Max %v", got, b.Max)
	}
}

func TestBackoff_DeterministicWithInjectedRand(t *testing.T) {
	b := Backoff{
		Base:   100 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 2.0,
		Jitter: 0.5, // up to ±50%
	}
	// Injected rand always returns 0.0 → jitter multiplier = (1 - 0.5) = 0.5.
	// attempt 2 base value = 400ms → 400ms * 0.5 = 200ms.
	got := b.Duration(2, fixedRand(0.0))
	if got != 200*time.Millisecond {
		t.Fatalf("rand=0.0 jitter: got %v, want 200ms", got)
	}

	// Injected rand returns ~1.0 → multiplier ≈ (1 + 0.5) = 1.5 → 600ms.
	gotHi := b.Duration(2, fixedRand(0.999999))
	if gotHi < 599*time.Millisecond || gotHi > 600*time.Millisecond {
		t.Fatalf("rand≈1.0 jitter: got %v, want ~600ms", gotHi)
	}

	// Same inputs → same output (determinism).
	if again := b.Duration(2, fixedRand(0.0)); again != got {
		t.Fatalf("non-deterministic: %v != %v", again, got)
	}
}

// Mutation: jitter must actually move the value off the pure-exponential base.
func TestBackoff_JitterAppliesMutation(t *testing.T) {
	b := Backoff{
		Base:   1 * time.Second,
		Max:    1 * time.Minute,
		Factor: 2.0,
		Jitter: 0.5,
	}
	pure := 4 * time.Second // attempt 2 = 1s * 2^2
	low := b.Duration(2, fixedRand(0.0))
	if low == pure {
		t.Fatal("jitter=0.0 rand should reduce below the pure exponential value")
	}
	if low != 2*time.Second {
		t.Fatalf("expected 2s (0.5 multiplier), got %v", low)
	}
}

// Mutation: with zero jitter the value MUST equal pure exponential regardless of rand.
func TestBackoff_ZeroJitterIgnoresRand_Mutation(t *testing.T) {
	b := Backoff{
		Base:   1 * time.Second,
		Max:    1 * time.Minute,
		Factor: 2.0,
		Jitter: 0,
	}
	a := b.Duration(3, fixedRand(0.0))
	bb := b.Duration(3, fixedRand(0.9))
	if a != bb {
		t.Fatalf("zero jitter must ignore rand: %v != %v", a, bb)
	}
	if a != 8*time.Second {
		t.Fatalf("attempt 3 with factor 2 base 1s = 8s, got %v", a)
	}
}

// Mutation: negative attempt must be clamped (treated as attempt 0), not panic/negative.
func TestBackoff_NegativeAttemptClamped_Mutation(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Max: time.Second, Factor: 2.0, Jitter: 0}
	if got := b.Duration(-5, nil); got != 100*time.Millisecond {
		t.Fatalf("negative attempt should clamp to Base, got %v", got)
	}
}

// Mutation: nil rand with non-zero jitter must not panic and must stay bounded.
func TestBackoff_NilRandWithJitterSafe_Mutation(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Max: time.Second, Factor: 2.0, Jitter: 0.5}
	got := b.Duration(1, nil) // nil rand → treated as no jitter (deterministic base)
	if got != 200*time.Millisecond {
		t.Fatalf("nil rand should fall back to pure exponential (200ms), got %v", got)
	}
}
