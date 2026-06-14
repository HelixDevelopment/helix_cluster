package burst

import (
	"testing"
	"time"
)

// adversarialBase mirrors baseConfig but is local so these tests do not depend
// on package_test.go internals being stable. UpFor/DownFor=3, Step=2,
// Cooldown=10s, replicas in [1,10].
func adversarialBase() Config {
	return Config{
		High:        80,
		Low:         20,
		UpFor:       3,
		DownFor:     3,
		Step:        2,
		Cooldown:    10 * time.Second,
		MinReplicas: 1,
		MaxReplicas: 10,
	}
}

// TestAdversarial_ScaleUpExactNthBoundary is the centerpiece: with UpFor=N, the
// first N-1 consecutive over-High samples MUST Hold and MUST NOT move desired;
// the Nth sample MUST ScaleUp by exactly Step. This pins both the "too early"
// (>=N-1 or >=1) and "too late" (>N) off-by-one on the streak comparison.
func TestAdversarial_ScaleUpExactNthBoundary(t *testing.T) {
	for _, N := range []int{1, 2, 3, 5} {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		cfg := adversarialBase()
		cfg.UpFor = N
		s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(4))
		if err != nil {
			t.Fatalf("N=%d New: %v", N, err)
		}
		// Samples 1..N-1 must Hold and leave desired untouched.
		for i := 1; i <= N-1; i++ {
			if got := s.Observe(90); got != Hold {
				t.Fatalf("N=%d sample %d/%d: got %q, want Hold (scaled too early)", N, i, N, got)
			}
			if s.Desired() != 4 {
				t.Fatalf("N=%d after sample %d desired=%d, want 4 (moved before Nth)", N, i, s.Desired())
			}
		}
		// The Nth sample must ScaleUp by exactly Step (4 -> 6).
		if got := s.Observe(90); got != ScaleUp {
			t.Fatalf("N=%d Nth sample: got %q, want ScaleUp (scaled too late / never)", N, got)
		}
		if s.Desired() != 6 {
			t.Fatalf("N=%d desired after ScaleUp=%d, want 6 (4 + Step 2)", N, s.Desired())
		}
	}
}

// TestAdversarial_ScaleDownExactMthBoundary mirrors the up boundary for the
// down direction (DownFor=M).
func TestAdversarial_ScaleDownExactMthBoundary(t *testing.T) {
	for _, M := range []int{1, 2, 3, 4} {
		clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
		cfg := adversarialBase()
		cfg.DownFor = M
		s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(7))
		if err != nil {
			t.Fatalf("M=%d New: %v", M, err)
		}
		for i := 1; i <= M-1; i++ {
			if got := s.Observe(10); got != Hold {
				t.Fatalf("M=%d sample %d/%d: got %q, want Hold (scaled down too early)", M, i, M, got)
			}
			if s.Desired() != 7 {
				t.Fatalf("M=%d after sample %d desired=%d, want 7 (moved before Mth)", M, i, s.Desired())
			}
		}
		if got := s.Observe(10); got != ScaleDown {
			t.Fatalf("M=%d Mth sample: got %q, want ScaleDown", M, got)
		}
		if s.Desired() != 5 {
			t.Fatalf("M=%d desired after ScaleDown=%d, want 5 (7 - Step 2)", M, s.Desired())
		}
	}
}

// TestAdversarial_InBandBreaksStreakRequiresFreshN proves a dead-band sample
// fully resets the streak: after high,high,in-band, two MORE highs (total 4
// highs but only 2 consecutive) must NOT scale; a third fresh high (3rd
// consecutive) finally scales. Catches "in-band does not reset streak".
func TestAdversarial_InBandBreaksStreakRequiresFreshN(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(adversarialBase(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 2 highs, then a dead-band sample resets the streak to 0.
	mustHold := func(load float64, label string) {
		if got := s.Observe(load); got != Hold {
			t.Fatalf("%s (load %g): got %q, want Hold", label, load, got)
		}
	}
	mustHold(90, "high#1")
	mustHold(90, "high#2")
	mustHold(50, "in-band reset")
	// Only 2 fresh consecutive highs after the reset: still must NOT scale even
	// though 4 highs have now been seen in total.
	mustHold(90, "fresh high#1")
	mustHold(90, "fresh high#2")
	if s.Desired() != 4 {
		t.Fatalf("desired=%d after non-consecutive highs, want 4 (streak must have reset)", s.Desired())
	}
	// The 3rd fresh consecutive high completes a clean UpFor streak -> ScaleUp.
	if got := s.Observe(90); got != ScaleUp {
		t.Fatalf("3rd fresh consecutive high: got %q, want ScaleUp", got)
	}
	if s.Desired() != 6 {
		t.Fatalf("desired after ScaleUp=%d, want 6", s.Desired())
	}
}

// TestAdversarial_LowSampleBreaksUpStreak proves a sample at/under Low (the
// opposite direction) also resets a partial up-streak — non-consecutive highs
// interleaved with a low never reach UpFor.
func TestAdversarial_LowSampleBreaksUpStreak(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(adversarialBase(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seq := []float64{90, 90, 10, 90, 90}
	for i, load := range seq {
		if got := s.Observe(load); got != Hold {
			t.Fatalf("sample %d (%g): got %q, want Hold (low must break up-streak)", i, load, got)
		}
	}
	if s.Desired() != 4 {
		t.Fatalf("desired=%d, want 4 (no scale on interrupted up-streak)", s.Desired())
	}
}

// TestAdversarial_CooldownWindowBoundaryTriple drives the injected clock to the
// three load-bearing offsets around the cooldown window: window-1ns (suppress),
// window exactly (the contract boundary), window+1ns (allow). After a ScaleUp
// at T arming cooldownUntil=T+Cooldown, a fresh UpFor streak completing while
// now<cooldownUntil must be suppressed; at now>=cooldownUntil it must scale.
//
// The implementation gate is `now.Before(cooldownUntil)`: suppression is the
// half-open interval [T, T+Cooldown), so the boundary sample at exactly
// T+Cooldown is ALLOWED. We pin all three points.
func TestAdversarial_CooldownWindowBoundaryTriple(t *testing.T) {
	cooldown := 10 * time.Second

	// helper: build a scaler, drive the first ScaleUp at base time, then jump
	// the clock to base+offset and feed a fresh UpFor streak; return the
	// decision on the streak-completing sample and the desired count.
	run := func(offset time.Duration) (Decision, int) {
		base := time.Unix(1_700_000_000, 0)
		clk := &fakeClock{t: base}
		cfg := adversarialBase()
		cfg.Cooldown = cooldown
		s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(4))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// First ScaleUp at base time arms cooldownUntil = base + cooldown.
		s.Observe(90)
		s.Observe(90)
		if got := s.Observe(90); got != ScaleUp {
			t.Fatalf("setup ScaleUp: got %q, want ScaleUp", got)
		}
		// Jump to base+offset and feed exactly UpFor fresh highs.
		clk.t = base.Add(offset)
		var last Decision
		for i := 0; i < cfg.UpFor; i++ {
			last = s.Observe(95)
		}
		return last, s.Desired()
	}

	// window-1ns: still inside cooldown -> suppressed (Hold), desired stays 6.
	if got, des := run(cooldown - time.Nanosecond); got != Hold || des != 6 {
		t.Fatalf("window-1ns: got=%q desired=%d, want Hold/6 (cooldown must suppress)", got, des)
	}
	// window exactly: gate open (now >= cooldownUntil) -> a fresh streak scales.
	if got, des := run(cooldown); got != ScaleUp || des != 8 {
		t.Fatalf("window exactly: got=%q desired=%d, want ScaleUp/8 (boundary must allow)", got, des)
	}
	// window+1ns: clearly outside -> scales.
	if got, des := run(cooldown + time.Nanosecond); got != ScaleUp || des != 8 {
		t.Fatalf("window+1ns: got=%q desired=%d, want ScaleUp/8 (past window must allow)", got, des)
	}
}

// TestAdversarial_CooldownFreezesStreakNoBufferedFill proves the cooldown gate
// not only Holds but ZEROES the streak, so in-cooldown highs cannot be "banked"
// to fire the instant the window opens. After cooldown ends, the very first
// post-window high must NOT immediately scale — a full fresh UpFor is required.
func TestAdversarial_CooldownFreezesStreakNoBufferedFill(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	clk := &fakeClock{t: base}
	cfg := adversarialBase()
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Observe(90)
	s.Observe(90)
	if got := s.Observe(90); got != ScaleUp { // arms cooldown at base+10s
		t.Fatalf("setup ScaleUp: got %q, want ScaleUp", got)
	}
	// Feed UpFor highs WHILE inside cooldown (at base+5s). These must all Hold
	// and must NOT bank streak.
	clk.t = base.Add(5 * time.Second)
	for i := 0; i < cfg.UpFor; i++ {
		if got := s.Observe(95); got != Hold {
			t.Fatalf("in-cooldown banked sample %d: got %q, want Hold", i, got)
		}
	}
	// Open the window exactly. The FIRST post-window high must Hold (streak was
	// frozen at 0); it cannot be the banked Nth.
	clk.t = base.Add(10 * time.Second)
	if got := s.Observe(95); got != Hold {
		t.Fatalf("first post-cooldown high: got %q, want Hold (no banked streak)", got)
	}
	if got := s.Observe(95); got != Hold {
		t.Fatalf("second post-cooldown high: got %q, want Hold", got)
	}
	if got := s.Observe(95); got != ScaleUp {
		t.Fatalf("third post-cooldown high: got %q, want ScaleUp (fresh UpFor met)", got)
	}
	if s.Desired() != 8 {
		t.Fatalf("desired=%d, want 8", s.Desired())
	}
}

// TestAdversarial_CeilingHoldsNoCooldownArmed proves that hitting UpFor while
// already pinned at MaxReplicas yields Hold, does not overshoot Max, and does
// NOT arm a cooldown (no action occurred) — a subsequent legitimate ScaleDown
// must be free to fire immediately.
func TestAdversarial_CeilingHoldsNoCooldownArmed(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{
		High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 3,
		Cooldown: 10 * time.Second, MinReplicas: 1, MaxReplicas: 10,
	}
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(10))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// At ceiling: over-High must Hold, desired pinned at 10, no cooldown armed.
	if got := s.Observe(99); got != Hold {
		t.Fatalf("at ceiling over-High: got %q, want Hold", got)
	}
	if s.Desired() != 10 {
		t.Fatalf("desired=%d, want 10 (must not overshoot Max)", s.Desired())
	}
	// Because no action occurred, no cooldown is armed: a single under-Low
	// sample (DownFor=1) must ScaleDown immediately.
	if got := s.Observe(0); got != ScaleDown {
		t.Fatalf("post-ceiling-hold under-Low: got %q, want ScaleDown (no cooldown should be armed)", got)
	}
	if s.Desired() != 7 {
		t.Fatalf("desired after ScaleDown=%d, want 7 (10 - Step 3)", s.Desired())
	}
}

// TestAdversarial_StepClampedAtCeiling proves a ScaleUp whose full Step would
// exceed Max is clamped to Max (partial step), still counts as a ScaleUp, and
// arms cooldown.
func TestAdversarial_StepClampedAtCeiling(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{
		High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 5,
		Cooldown: 10 * time.Second, MinReplicas: 1, MaxReplicas: 10,
	}
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(8))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 8 + 5 = 13, clamped to Max 10.
	if got := s.Observe(99); got != ScaleUp {
		t.Fatalf("near-ceiling ScaleUp: got %q, want ScaleUp", got)
	}
	if s.Desired() != 10 {
		t.Fatalf("desired=%d, want 10 (step clamped to Max, not 13)", s.Desired())
	}
}

// TestAdversarial_StepClampedAtFloor mirrors the floor for ScaleDown.
func TestAdversarial_StepClampedAtFloor(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{
		High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 5,
		Cooldown: 10 * time.Second, MinReplicas: 1, MaxReplicas: 10,
	}
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(3))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 3 - 5 = -2, clamped to Min 1.
	if got := s.Observe(0); got != ScaleDown {
		t.Fatalf("near-floor ScaleDown: got %q, want ScaleDown", got)
	}
	if s.Desired() != 1 {
		t.Fatalf("desired=%d, want 1 (step clamped to Min, not -2)", s.Desired())
	}
}
