package burst

import (
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic cooldown tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }

// baseConfig returns a config with a generous cooldown so cooldown effects are
// observable independently of streak logic. Step=2 lets us assert the desired
// count moves by exactly the step (a Step=1 bug would be invisible otherwise).
func baseConfig() Config {
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

// TestSustainedHighScalesUpAtNthSampleNotBefore proves that a sustained
// over-High load triggers exactly one ScaleUp on the Nth (UpFor) sample, and
// Hold on every sample before it.
//
// Mutation that fails this: making Observe scale on the first over-threshold
// sample (drop the consecutive-count gate, e.g. `if s.upStreak >= 1`) — then
// sample 1 would return ScaleUp instead of Hold.
func TestSustainedHighScalesUpAtNthSampleNotBefore(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// UpFor=3: samples 1 and 2 must Hold; sample 3 must ScaleUp.
	for i := 1; i <= 2; i++ {
		if got := s.Observe(90); got != Hold {
			t.Fatalf("sample %d over High: got %q, want Hold (hysteresis not engaged)", i, got)
		}
		if s.Desired() != 4 {
			t.Fatalf("after sample %d desired=%d, want 4 (must not move before streak met)", i, s.Desired())
		}
	}
	if got := s.Observe(90); got != ScaleUp {
		t.Fatalf("3rd sustained-high sample: got %q, want ScaleUp", got)
	}
	// Desired must move by exactly Step=2: 4 -> 6.
	if s.Desired() != 6 {
		t.Fatalf("desired after ScaleUp=%d, want 6 (initial 4 + step 2)", s.Desired())
	}
}

// TestBriefSpikeDoesNotScale proves hysteresis: a single over-High sample
// surrounded by in-band load must NOT scale.
//
// Mutation that fails this: a scaler that scales on a single over-threshold
// sample (no consecutive-count) returns ScaleUp here instead of Hold.
func TestBriefSpikeDoesNotScale(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := s.Observe(50); got != Hold { // in-band warmup
		t.Fatalf("in-band warmup: got %q, want Hold", got)
	}
	if got := s.Observe(95); got != Hold { // the spike
		t.Fatalf("1-sample spike: got %q, want Hold (hysteresis must absorb spikes)", got)
	}
	if got := s.Observe(50); got != Hold { // back in band; streak broken
		t.Fatalf("post-spike in-band: got %q, want Hold", got)
	}
	if s.Desired() != 4 {
		t.Fatalf("desired after brief spike=%d, want 4 (spike must not scale)", s.Desired())
	}
}

// TestInBandSampleBreaksStreak proves the dead-band resets a partial streak, so
// two over-High samples separated by an in-band one never reach UpFor.
//
// Mutation that fails this: not resetting upStreak in the default (dead-band)
// case — then 2 highs + 1 high after a gap would wrongly accumulate to ScaleUp.
func TestInBandSampleBreaksStreak(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seq := []float64{90, 90, 50, 90, 90}
	for i, load := range seq {
		if got := s.Observe(load); got != Hold {
			t.Fatalf("sample %d (%g): got %q, want Hold (interrupted streak must not scale)", i, load, got)
		}
	}
	if s.Desired() != s.cfg.MinReplicas {
		t.Fatalf("desired=%d, want %d (no scale should have occurred)", s.Desired(), s.cfg.MinReplicas)
	}
}

// TestCooldownGateSuppressesScaling proves that after a scale action, samples
// inside the cooldown window return Hold even when the threshold is sustained,
// and that scaling resumes once the injected clock passes cooldownUntil.
//
// Mutation that fails this: removing the cooldown gate in Observe (delete the
// `now.Before(s.cooldownUntil)` early-return) — then the post-action sustained
// highs would immediately trigger a second ScaleUp inside the window.
func TestCooldownGateSuppressesScaling(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Drive the first ScaleUp (UpFor=3).
	s.Observe(90)
	s.Observe(90)
	if got := s.Observe(90); got != ScaleUp {
		t.Fatalf("setup ScaleUp: got %q, want ScaleUp", got)
	}
	if s.Desired() != 6 {
		t.Fatalf("desired after first ScaleUp=%d, want 6", s.Desired())
	}

	// Cooldown is 10s; advance only 5s. Three more sustained highs must all
	// Hold and must NOT scale a second time.
	clk.Add(5 * time.Second)
	for i := 0; i < 3; i++ {
		if got := s.Observe(95); got != Hold {
			t.Fatalf("in-cooldown sample %d: got %q, want Hold (cooldown gate must suppress)", i, got)
		}
	}
	if s.Desired() != 6 {
		t.Fatalf("desired during cooldown=%d, want 6 (no scaling inside cooldown)", s.Desired())
	}

	// Cross the cooldown boundary: total elapsed becomes 10s == cooldownUntil.
	// Now a fresh sustained-high streak (UpFor=3) must produce a ScaleUp.
	clk.Add(5 * time.Second)
	s.Observe(95)
	s.Observe(95)
	if got := s.Observe(95); got != ScaleUp {
		t.Fatalf("post-cooldown sustained high: got %q, want ScaleUp (scaling must resume)", got)
	}
	if s.Desired() != 8 {
		t.Fatalf("desired after second ScaleUp=%d, want 8 (6 + step 2)", s.Desired())
	}
}

// TestSustainedLowScalesDownAfterM proves a sustained at-or-below-Low load
// triggers exactly one ScaleDown on the Mth (DownFor) sample, decrementing by
// exactly Step.
//
// Mutation that fails this: scaling down on a single under-Low sample (drop the
// DownFor count) — sample 1 would ScaleDown instead of Hold.
func TestSustainedLowScalesDownAfterM(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now), WithInitialReplicas(7))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 1; i <= 2; i++ {
		if got := s.Observe(10); got != Hold {
			t.Fatalf("low sample %d: got %q, want Hold (DownFor not met)", i, got)
		}
		if s.Desired() != 7 {
			t.Fatalf("desired after low sample %d=%d, want 7", i, s.Desired())
		}
	}
	if got := s.Observe(10); got != ScaleDown {
		t.Fatalf("3rd sustained-low sample: got %q, want ScaleDown", got)
	}
	if s.Desired() != 5 {
		t.Fatalf("desired after ScaleDown=%d, want 5 (7 - step 2)", s.Desired())
	}
}

// TestThresholdsAreInclusive pins that load exactly equal to High counts as
// over (and Low as under). A `>` instead of `>=` mutation would make exact-High
// samples land in the dead band and never scale.
func TestThresholdsAreInclusive(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := baseConfig()
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Exactly High=80 three times must ScaleUp.
	s.Observe(80)
	s.Observe(80)
	if got := s.Observe(80); got != ScaleUp {
		t.Fatalf("load==High: got %q, want ScaleUp (threshold must be inclusive)", got)
	}
}

// TestCeilingPreventsScaleUp proves ScaleUp does not exceed MaxReplicas and
// returns Hold once pinned at the ceiling.
//
// Mutation that fails this: dropping the clamp/`next != desired` guard so
// desired overshoots Max — desired would become 11 (> Max 10).
func TestCeilingPreventsScaleUp(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{
		High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 1,
		Cooldown: 0, MinReplicas: 1, MaxReplicas: 10,
	}
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(10))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.Observe(99); got != Hold {
		t.Fatalf("at ceiling: got %q, want Hold (must not scale past Max)", got)
	}
	if s.Desired() != 10 {
		t.Fatalf("desired at ceiling=%d, want 10 (Max must be respected)", s.Desired())
	}
}

// TestFloorPreventsScaleDown proves ScaleDown does not go below MinReplicas.
//
// Mutation that fails this: dropping the floor clamp — desired would become 0
// (< Min 1).
func TestFloorPreventsScaleDown(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cfg := Config{
		High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 1,
		Cooldown: 0, MinReplicas: 1, MaxReplicas: 10,
	}
	s, err := New(cfg, WithClock(clk.Now), WithInitialReplicas(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.Observe(0); got != Hold {
		t.Fatalf("at floor: got %q, want Hold (must not scale below Min)", got)
	}
	if s.Desired() != 1 {
		t.Fatalf("desired at floor=%d, want 1 (Min must be respected)", s.Desired())
	}
}

// TestInitialReplicasClamped proves WithInitialReplicas is clamped into
// [Min,Max] and is the default == Min when unset.
func TestInitialReplicasClamped(t *testing.T) {
	cfg := baseConfig() // Min 1, Max 10
	over, err := New(cfg, WithInitialReplicas(999))
	if err != nil {
		t.Fatalf("New over: %v", err)
	}
	if over.Desired() != 10 {
		t.Fatalf("over-Max initial: desired=%d, want 10", over.Desired())
	}
	under, err := New(cfg, WithInitialReplicas(-5))
	if err != nil {
		t.Fatalf("New under: %v", err)
	}
	if under.Desired() != 1 {
		t.Fatalf("under-Min initial: desired=%d, want 1", under.Desired())
	}
	def, err := New(cfg)
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	if def.Desired() != cfg.MinReplicas {
		t.Fatalf("default initial: desired=%d, want %d", def.Desired(), cfg.MinReplicas)
	}
}

// TestConfigValidation pins the rejected configs so a loosened guard is caught.
func TestConfigValidation(t *testing.T) {
	cases := map[string]Config{
		"low_above_high": {High: 10, Low: 20, UpFor: 1, DownFor: 1, Step: 1, MaxReplicas: 1},
		"zero_upfor":     {High: 80, Low: 20, UpFor: 0, DownFor: 1, Step: 1, MaxReplicas: 1},
		"zero_downfor":   {High: 80, Low: 20, UpFor: 1, DownFor: 0, Step: 1, MaxReplicas: 1},
		"zero_step":      {High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 0, MaxReplicas: 1},
		"max_below_min":  {High: 80, Low: 20, UpFor: 1, DownFor: 1, Step: 1, MinReplicas: 5, MaxReplicas: 1},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// TestUpThenDownAfterCooldownFullCycle exercises a realistic burst: scale up on
// sustained high, then after cooldown scale back down on sustained low, proving
// the streaks and cooldown interact correctly across a direction change.
func TestUpThenDownAfterCooldownFullCycle(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s, err := New(baseConfig(), WithClock(clk.Now), WithInitialReplicas(4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Up.
	s.Observe(90)
	s.Observe(90)
	if got := s.Observe(90); got != ScaleUp {
		t.Fatalf("ScaleUp phase: got %q, want ScaleUp", got)
	}
	if s.Desired() != 6 {
		t.Fatalf("after up desired=%d, want 6", s.Desired())
	}
	// Wait out the cooldown, then sustained low.
	clk.Add(10 * time.Second)
	s.Observe(5)
	s.Observe(5)
	if got := s.Observe(5); got != ScaleDown {
		t.Fatalf("ScaleDown phase: got %q, want ScaleDown", got)
	}
	if s.Desired() != 4 {
		t.Fatalf("after down desired=%d, want 4 (6 - step 2)", s.Desired())
	}
}
