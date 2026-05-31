// Package burst implements a hysteresis-based burst autoscaler state machine
// for Helix Cluster OS.
//
// The scaler consumes a stream of load samples and decides whether to scale up,
// scale down, or hold the current desired replica count. To prevent flapping it
// applies HYSTERESIS in both directions plus a post-action COOLDOWN:
//
//   - Scale UP fires only after the load has stayed at or above the high
//     threshold for N consecutive samples.
//   - Scale DOWN fires only after the load has stayed at or below the low
//     threshold for M consecutive samples.
//   - After any scale action a cooldown window opens during which every sample
//     returns Hold, regardless of threshold crossings, so a single action is
//     not immediately followed by a reversal.
//
// The clock is injected (Option WithClock) so cooldown behaviour is fully
// deterministic in tests without sleeping.
//
// This package is pure stdlib and self-contained: it owns no provider seam and
// performs no I/O. It only maps a sample stream to scale Decisions and tracks
// the resulting desired replica count.
package burst

import (
	"fmt"
	"time"
)

// Decision is the action the scaler emits for a single observed sample.
type Decision string

const (
	// Hold means keep the current desired replica count unchanged.
	Hold Decision = "hold"
	// ScaleUp means the desired replica count was increased by one Step.
	ScaleUp Decision = "scale_up"
	// ScaleDown means the desired replica count was decreased by one Step.
	ScaleDown Decision = "scale_down"
)

// Config parameterises the hysteresis state machine.
type Config struct {
	// High is the load threshold (inclusive) that a sample must reach to count
	// toward a scale-up streak.
	High float64
	// Low is the load threshold (inclusive) that a sample must fall to or below
	// to count toward a scale-down streak.
	Low float64
	// UpFor is N: the number of consecutive at-or-above-High samples required
	// before a single ScaleUp fires.
	UpFor int
	// DownFor is M: the number of consecutive at-or-below-Low samples required
	// before a single ScaleDown fires.
	DownFor int
	// Step is the number of replicas added on ScaleUp / removed on ScaleDown.
	Step int
	// Cooldown is the window after any scale action during which every sample
	// returns Hold.
	Cooldown time.Duration
	// MinReplicas is the floor for the desired replica count (ScaleDown will
	// not go below it).
	MinReplicas int
	// MaxReplicas is the ceiling for the desired replica count (ScaleUp will
	// not go above it).
	MaxReplicas int
}

// validate returns an error if the config is internally inconsistent.
func (c Config) validate() error {
	if c.Low > c.High {
		return fmt.Errorf("burst: Low (%g) must not exceed High (%g)", c.Low, c.High)
	}
	if c.UpFor < 1 {
		return fmt.Errorf("burst: UpFor must be >= 1, got %d", c.UpFor)
	}
	if c.DownFor < 1 {
		return fmt.Errorf("burst: DownFor must be >= 1, got %d", c.DownFor)
	}
	if c.Step < 1 {
		return fmt.Errorf("burst: Step must be >= 1, got %d", c.Step)
	}
	if c.Cooldown < 0 {
		return fmt.Errorf("burst: Cooldown must be >= 0, got %s", c.Cooldown)
	}
	if c.MinReplicas < 0 {
		return fmt.Errorf("burst: MinReplicas must be >= 0, got %d", c.MinReplicas)
	}
	if c.MaxReplicas < c.MinReplicas {
		return fmt.Errorf("burst: MaxReplicas (%d) must be >= MinReplicas (%d)", c.MaxReplicas, c.MinReplicas)
	}
	return nil
}

// Option customises a Scaler at construction time.
type Option func(*Scaler)

// WithClock overrides the wall-clock source used for cooldown arithmetic. The
// default is time.Now. Tests inject a controllable clock to make cooldown gates
// deterministic without sleeping.
func WithClock(now func() time.Time) Option {
	return func(s *Scaler) {
		if now != nil {
			s.now = now
		}
	}
}

// WithInitialReplicas sets the starting desired replica count. It is clamped
// into [MinReplicas, MaxReplicas]. The default is MinReplicas.
func WithInitialReplicas(n int) Option {
	return func(s *Scaler) { s.initial = n; s.initialSet = true }
}

// Scaler is the hysteresis burst-autoscaler state machine. It is NOT safe for
// concurrent use; callers must serialise Observe (e.g. a single control loop).
type Scaler struct {
	cfg Config
	now func() time.Time

	initial    int
	initialSet bool

	desired  int
	upStreak int
	dnStreak int

	// cooldownUntil is the instant at or after which scaling may resume. A zero
	// value means no cooldown is active.
	cooldownUntil time.Time
}

// New constructs a Scaler from cfg and options. It returns an error if cfg is
// invalid.
func New(cfg Config, opts ...Option) (*Scaler, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	s := &Scaler{
		cfg:     cfg,
		now:     time.Now,
		initial: cfg.MinReplicas,
	}
	for _, opt := range opts {
		opt(s)
	}
	start := cfg.MinReplicas
	if s.initialSet {
		start = s.initial
	}
	s.desired = clamp(start, cfg.MinReplicas, cfg.MaxReplicas)
	return s, nil
}

// Desired returns the current desired replica count.
func (s *Scaler) Desired() int { return s.desired }

// Observe feeds one load sample into the state machine and returns the
// resulting Decision. The desired replica count is updated in place; read it
// with Desired.
//
// Order of evaluation:
//  1. If a cooldown is active, return Hold and do not advance streaks. This is
//     the cooldown gate: it suppresses scaling even when a threshold is crossed.
//  2. Otherwise advance the up/down streaks from this sample. When a streak
//     reaches its threshold (and a step is actually possible within
//     [Min,Max]), apply exactly one Step, reset both streaks, and open the
//     cooldown window.
func (s *Scaler) Observe(load float64) Decision {
	now := s.now()

	// (1) Cooldown gate. While inside the window, hold unconditionally and
	// freeze the streaks so the window cannot be "filled" by buffered samples.
	if !s.cooldownUntil.IsZero() && now.Before(s.cooldownUntil) {
		s.upStreak = 0
		s.dnStreak = 0
		return Hold
	}

	switch {
	case load >= s.cfg.High:
		s.upStreak++
		s.dnStreak = 0
	case load <= s.cfg.Low:
		s.dnStreak++
		s.upStreak = 0
	default:
		// In the dead band between Low and High: neither streak advances and
		// any partial streak is broken.
		s.upStreak = 0
		s.dnStreak = 0
	}

	if s.upStreak >= s.cfg.UpFor {
		next := clamp(s.desired+s.cfg.Step, s.cfg.MinReplicas, s.cfg.MaxReplicas)
		if next != s.desired {
			s.desired = next
			s.resetAfterAction(now)
			return ScaleUp
		}
		// Already at the ceiling: nothing to do but keep the streak from
		// growing unbounded.
		s.upStreak = s.cfg.UpFor
	}

	if s.dnStreak >= s.cfg.DownFor {
		next := clamp(s.desired-s.cfg.Step, s.cfg.MinReplicas, s.cfg.MaxReplicas)
		if next != s.desired {
			s.desired = next
			s.resetAfterAction(now)
			return ScaleDown
		}
		// Already at the floor.
		s.dnStreak = s.cfg.DownFor
	}

	return Hold
}

// resetAfterAction clears the streaks and arms the cooldown window relative to
// now.
func (s *Scaler) resetAfterAction(now time.Time) {
	s.upStreak = 0
	s.dnStreak = 0
	if s.cfg.Cooldown > 0 {
		s.cooldownUntil = now.Add(s.cfg.Cooldown)
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
